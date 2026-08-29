package web

import (
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const DefaultMaxBodyBytes = int64(3 * 1024 * 1024)

const (
	appCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"
)

type Config struct {
	PortalRoot   string
	AdminRoot    string
	UsageRoot    string
	AdminTarget  string
	Transport    http.RoundTripper
	Logger       *zap.Logger
	MaxBodyBytes int64
}

type Server struct {
	portalRoot  string
	adminRoot   string
	usageRoot   string
	logger      *zap.Logger
	maxBody     int64
	adminProxy  *httputil.ReverseProxy
	publicProxy *httputil.ReverseProxy
	router      *gin.Engine
}

var configureGinMode sync.Once

func NewServer(config Config) (*Server, error) {
	portalRoot, err := existingDirectory(config.PortalRoot, "Portal")
	if err != nil {
		return nil, err
	}
	adminRoot, err := existingDirectory(config.AdminRoot, "Admin assets")
	if err != nil {
		return nil, err
	}
	usageRoot, err := existingDirectory(config.UsageRoot, "Usage assets")
	if err != nil {
		return nil, err
	}
	target, err := parseTarget(config.AdminTarget)
	if err != nil {
		return nil, err
	}
	if config.Transport == nil {
		config.Transport = http.DefaultTransport
	}
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	server := &Server{
		portalRoot:  portalRoot,
		adminRoot:   adminRoot,
		usageRoot:   usageRoot,
		logger:      config.Logger,
		maxBody:     config.MaxBodyBytes,
		adminProxy:  newProxy(target, config.Transport, config.Logger, false),
		publicProxy: newProxy(target, config.Transport, config.Logger, true),
	}
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleMethodNotAllowed = false
	router.RemoveExtraSlash = false
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("disable trusted proxies: %w", err)
	}
	router.Use(server.recovery(), server.securityHeaders(), server.limitBody())
	router.Any("/", server.landing)
	// The public API contract describes the Admin backing-store health, not
	// merely this static Web process. Proxy it through the credential-stripping
	// public path so Edge exposes the same JSON contract without admitting
	// caller Authorization or management headers.
	router.Any("/healthz", server.publicAdmin)
	router.Any("/official-management", redirect(http.StatusFound, "/native/"))
	router.Any("/native", redirect(http.StatusPermanentRedirect, "/native/"))
	router.Any("/native/*path", server.native)
	router.Any("/portal/*path", server.portalAsset)
	router.Any("/admin", redirect(http.StatusPermanentRedirect, "/admin/"))
	router.Any("/admin/*path", server.admin)
	router.Any("/usage", redirect(http.StatusPermanentRedirect, "/usage/"))
	router.Any("/usage/*path", server.usage)
	router.Any("/my-keys", redirect(http.StatusPermanentRedirect, "/usage/"))
	router.Any("/my-keys/*path", server.myKeys)
	router.Any("/site-config.json", server.publicAdmin)
	router.Any("/branding/logo", server.publicAdmin)
	router.NoRoute(func(c *gin.Context) { c.Status(http.StatusNotFound) })
	server.router = router
	return server, nil
}

func (server *Server) Handler() http.Handler {
	return server.router
}

func (server *Server) landing(c *gin.Context) {
	server.serveStatic(c, server.portalRoot, "index.html", "no-cache", appCSP)
}

func (server *Server) native(c *gin.Context) {
	if c.Param("path") == "/" {
		server.serveStatic(c, server.portalRoot, "index.html", "no-cache", appCSP)
		return
	}
	// The retired public account map must never become a filesystem fallback.
	c.Status(http.StatusNotFound)
}

func (server *Server) portalAsset(c *gin.Context) {
	relative, ok := safeAssetPath(c.Param("path"))
	if !ok {
		c.Status(http.StatusNotFound)
		return
	}
	if relative == "index.html" {
		server.serveStatic(c, server.portalRoot, relative, "no-cache", appCSP)
		return
	}
	if !strings.HasPrefix(relative, "assets/") {
		c.Status(http.StatusNotFound)
		return
	}
	server.serveStatic(c, server.portalRoot, relative, assetCacheControl(relative), appCSP)
}

func (server *Server) admin(c *gin.Context) {
	path := c.Param("path")
	if strings.HasPrefix(path, "/api/") || path == "/api" {
		server.adminProxy.ServeHTTP(c.Writer, c.Request)
		return
	}
	if path == "/reasoning-effort-colors.css" {
		server.publicProxy.ServeHTTP(c.Writer, c.Request)
		return
	}
	if strings.HasPrefix(path, "/assets/") {
		relative, ok := safeAssetPath(path)
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		server.serveStatic(c, server.adminRoot, relative, assetCacheControl(relative), appCSP)
		return
	}
	if path == "/" || isSinglePageRoute(path) {
		server.serveStatic(c, server.adminRoot, "index.html", "no-cache", appCSP)
		return
	}
	c.Status(http.StatusNotFound)
}

func (server *Server) usage(c *gin.Context) {
	path := c.Param("path")
	if isUsageAPIPath(path) {
		server.publicProxy.ServeHTTP(c.Writer, c.Request)
		return
	}
	if strings.HasPrefix(path, "/assets/") {
		relative, ok := safeAssetPath(path)
		if !ok {
			c.Status(http.StatusNotFound)
			return
		}
		server.serveStatic(c, server.usageRoot, relative, assetCacheControl(relative), appCSP)
		return
	}
	if path == "/" || isSinglePageRoute(path) {
		server.serveStatic(c, server.usageRoot, "index.html", "no-cache", appCSP)
		return
	}
	c.Status(http.StatusNotFound)
}

func assetCacheControl(relative string) string {
	name := filepath.Base(relative)
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	separator := strings.LastIndex(stem, "-")
	if separator >= 0 && len(stem)-separator-1 == 8 {
		fingerprint := stem[separator+1:]
		for _, character := range fingerprint {
			if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9' || character == '_' || character == '-' {
				continue
			}
			return "no-cache"
		}
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}

func (server *Server) myKeys(c *gin.Context) {
	if c.Param("path") == "/api" {
		server.publicProxy.ServeHTTP(c.Writer, c.Request)
		return
	}
	redirect(http.StatusPermanentRedirect, "/usage/")(c)
}

func (server *Server) publicAdmin(c *gin.Context) {
	server.publicProxy.ServeHTTP(c.Writer, c.Request)
}

func (server *Server) serveStatic(
	c *gin.Context,
	root string,
	relative string,
	cacheControl string,
	contentSecurityPolicy string,
) {
	if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	path, err := regularFileWithin(root, relative)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer file.Close()
	information, err := file.Stat()
	if err != nil || !information.Mode().IsRegular() {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", cacheControl)
	c.Header("Content-Security-Policy", contentSecurityPolicy)
	if contentType := mime.TypeByExtension(filepath.Ext(path)); contentType != "" {
		c.Header("Content-Type", contentType)
	}
	http.ServeContent(c.Writer, c.Request, information.Name(), information.ModTime(), file)
}

func (server *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if forwardedProtocol(c.Request) == "https" {
			c.Header("Strict-Transport-Security", "max-age=0")
		}
		c.Next()
	}
}

func (server *Server) limitBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > server.maxBody {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, server.maxBody)
		}
		c.Next()
	}
}

func (server *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				server.logger.Error(
					"Go Web request panic",
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path),
					zap.String("panic_type", fmt.Sprintf("%T", recovered)),
					zap.Stack("stack"),
				)
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	}
}

func newProxy(
	target *url.URL,
	transport http.RoundTripper,
	logger *zap.Logger,
	clearCredentials bool,
) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalHost := request.Host
		originalDirector(request)
		request.Host = originalHost
		if clearCredentials {
			request.Header.Del("Authorization")
			request.Header.Del("X-Management-Key")
		}
		realIP := strings.TrimSpace(request.Header.Get("X-Real-IP"))
		if realIP == "" {
			realIP = requestIP(request.RemoteAddr)
		}
		request.Header.Set("X-Real-IP", realIP)
		request.Header.Set("X-Forwarded-Proto", forwardedProtocol(request))
	}
	proxy.Transport = transport
	proxy.FlushInterval = -1
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, err error) {
		var bodyError *http.MaxBytesError
		if errors.As(err, &bodyError) {
			writer.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		logger.Warn(
			"Go Web Admin request failed",
			zap.String("method", request.Method),
			zap.String("path", request.URL.Path),
			zap.String("error_type", fmt.Sprintf("%T", err)),
		)
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(writer, "Bad Gateway\n")
	}
	return proxy
}

func parseTarget(raw string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Scheme != "http" || target.Host == "" || target.User != nil ||
		target.RawQuery != "" || target.Fragment != "" || (target.Path != "" && target.Path != "/") {
		return nil, errors.New("Admin target must be an origin-only http URL")
	}
	target.Path = ""
	return target, nil
}

func existingDirectory(raw string, name string) (string, error) {
	path, err := filepath.Abs(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("resolve %s root: %w", name, err)
	}
	information, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("open existing %s root: %w", name, err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return "", fmt.Errorf("%s root must be a real directory", name)
	}
	return filepath.Clean(path), nil
}

func regularFileWithin(root string, relative string) (string, error) {
	relative, ok := safeAssetPath("/" + strings.TrimPrefix(relative, "/"))
	if !ok {
		return "", errors.New("invalid static asset path")
	}
	path := filepath.Join(root, filepath.FromSlash(relative))
	current := root
	for _, component := range strings.Split(filepath.FromSlash(relative), string(filepath.Separator)) {
		current = filepath.Join(current, component)
		information, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if information.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("static assets must not use symbolic links")
		}
	}
	information, err := os.Stat(path)
	if err != nil || !information.Mode().IsRegular() {
		return "", errors.New("static asset is not a regular file")
	}
	return path, nil
}

func safeAssetPath(raw string) (string, bool) {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.Contains(raw, "\\") {
		return "", false
	}
	for _, component := range strings.Split(strings.TrimPrefix(raw, "/"), "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") {
			return "", false
		}
	}
	cleaned := strings.TrimPrefix(filepath.ToSlash(filepath.Clean(raw)), "/")
	if cleaned == "" || cleaned == "." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	for _, component := range strings.Split(cleaned, "/") {
		if component == "" || component == "." || component == ".." || strings.HasPrefix(component, ".") {
			return "", false
		}
	}
	return cleaned, true
}

func isSinglePageRoute(path string) bool {
	if path == "" || path == "/" || strings.HasSuffix(path, "/") || strings.Contains(path, ".") {
		return false
	}
	_, ok := safeAssetPath(path)
	return ok
}

func isUsageAPIPath(path string) bool {
	if path == "/api" || path == "/limits" || path == "/session" || path == "/me" {
		return true
	}
	return strings.HasPrefix(path, "/me/")
}

func redirect(status int, target string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(http.StatusMethodNotAllowed)
			return
		}
		c.Redirect(status, target)
	}
}

func requestIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddress)
}

func forwardedProtocol(request *http.Request) string {
	protocol := strings.ToLower(strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")))
	if protocol == "http" || protocol == "https" {
		return protocol
	}
	if request.TLS != nil {
		return "https"
	}
	return "http"
}
