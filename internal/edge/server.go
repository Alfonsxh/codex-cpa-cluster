package edge

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const DefaultMaxBodyBytes = int64(100 * 1024 * 1024)

type ServerConfig struct {
	Selector            *Selector
	WebTarget           string
	BluePublicTarget    string
	BlueInternalTarget  string
	GreenPublicTarget   string
	GreenInternalTarget string
	Transport           http.RoundTripper
	Logger              *zap.Logger
	MaxBodyBytes        int64
}

type slotProxies struct {
	public   *httputil.ReverseProxy
	internal *httputil.ReverseProxy
}

type Server struct {
	selector     *Selector
	logger       *zap.Logger
	maxBodyBytes int64
	web          *httputil.ReverseProxy
	gateways     map[Slot]slotProxies
	public       *gin.Engine
	internal     *gin.Engine
}

var configureGinMode sync.Once

func NewServer(config ServerConfig) (*Server, error) {
	if config.Selector == nil {
		return nil, errors.New("Edge server requires an active Gateway selector")
	}
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.Transport == nil {
		config.Transport = http.DefaultTransport
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = DefaultMaxBodyBytes
	}
	webTarget, err := parseTarget("Web", config.WebTarget)
	if err != nil {
		return nil, err
	}
	bluePublic, err := parseTarget("blue public Gateway", config.BluePublicTarget)
	if err != nil {
		return nil, err
	}
	blueInternal, err := parseTarget("blue internal Gateway", config.BlueInternalTarget)
	if err != nil {
		return nil, err
	}
	greenPublic, err := parseTarget("green public Gateway", config.GreenPublicTarget)
	if err != nil {
		return nil, err
	}
	greenInternal, err := parseTarget("green internal Gateway", config.GreenInternalTarget)
	if err != nil {
		return nil, err
	}
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	server := &Server{
		selector: config.Selector, logger: config.Logger, maxBodyBytes: config.MaxBodyBytes,
		web: newProxy(webTarget, "web", config.Transport, config.Logger),
		gateways: map[Slot]slotProxies{
			Blue: {
				public:   newProxy(bluePublic, "gateway-blue-public", config.Transport, config.Logger),
				internal: newProxy(blueInternal, "gateway-blue-internal", config.Transport, config.Logger),
			},
			Green: {
				public:   newProxy(greenPublic, "gateway-green-public", config.Transport, config.Logger),
				internal: newProxy(greenInternal, "gateway-green-internal", config.Transport, config.Logger),
			},
		},
	}
	server.public, err = server.newRouter()
	if err != nil {
		return nil, err
	}
	server.public.Use(server.recovery(), server.limitBody())
	server.public.Any("/__health", server.proxyPublicGateway)
	server.public.Any("/__stats", server.notFound)
	server.public.Any("/__internal/*path", server.notFound)
	server.public.NoRoute(server.proxyPublicRoute)

	server.internal, err = server.newRouter()
	if err != nil {
		return nil, err
	}
	server.internal.Use(server.recovery(), server.limitBody())
	server.internal.Any("/__stats", server.proxyInternalGateway)
	server.internal.Any("/__internal/*path", server.proxyInternalRoute)
	server.internal.NoRoute(server.notFound)
	return server, nil
}

func (server *Server) activeSlot(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(string(server.selector.Slot())+"\n"))
}

func (server *Server) proxyInternalRoute(c *gin.Context) {
	if c.Request.Method == http.MethodGet && c.Request.URL.Path == "/__internal/edge/slot" {
		server.activeSlot(c)
		return
	}
	server.proxyInternalGateway(c)
}

func (server *Server) PublicHandler() http.Handler {
	return server.public
}

func (server *Server) InternalHandler() http.Handler {
	return server.internal
}

func (server *Server) newRouter() (*gin.Engine, error) {
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleMethodNotAllowed = false
	router.RemoveExtraSlash = false
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("disable trusted proxies: %w", err)
	}
	return router, nil
}

func (server *Server) proxyPublicRoute(c *gin.Context) {
	if isWebPath(c.Request.URL.Path) {
		server.web.ServeHTTP(c.Writer, c.Request)
		return
	}
	server.proxyPublicGateway(c)
}

func (server *Server) proxyPublicGateway(c *gin.Context) {
	proxies, found := server.gateways[server.selector.Slot()]
	if !found || proxies.public == nil {
		c.Data(http.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("Service Unavailable\n"))
		return
	}
	proxies.public.ServeHTTP(c.Writer, c.Request)
}

func (server *Server) proxyInternalGateway(c *gin.Context) {
	proxies, found := server.gateways[server.selector.Slot()]
	if !found || proxies.internal == nil {
		c.Data(http.StatusServiceUnavailable, "text/plain; charset=utf-8", []byte("Service Unavailable\n"))
		return
	}
	proxies.internal.ServeHTTP(c.Writer, c.Request)
}

func (server *Server) notFound(c *gin.Context) {
	c.Status(http.StatusNotFound)
}

func (server *Server) limitBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > server.maxBodyBytes {
			c.AbortWithStatus(http.StatusRequestEntityTooLarge)
			return
		}
		if c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, server.maxBodyBytes)
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
					"Go Edge request panic",
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

func newProxy(target *url.URL, name string, transport http.RoundTripper, logger *zap.Logger) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(target)
	originalDirector := proxy.Director
	proxy.Director = func(request *http.Request) {
		originalHost := request.Host
		originalDirector(request)
		request.Host = originalHost
		request.Header.Set("X-Real-IP", requestIP(request.RemoteAddr))
		if strings.TrimSpace(request.Header.Get("X-Forwarded-Proto")) == "" {
			protocol := "http"
			if request.TLS != nil {
				protocol = "https"
			}
			request.Header.Set("X-Forwarded-Proto", protocol)
		}
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
			"Go Edge upstream request failed",
			zap.String("upstream", name),
			zap.String("method", request.Method),
			zap.String("path", request.URL.Path),
			// A RoundTripper error is not a trusted log payload. Recording only
			// its concrete type keeps transport diagnostics useful without ever
			// admitting an Authorization value embedded by a custom transport.
			zap.String("error_type", fmt.Sprintf("%T", err)),
		)
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(writer, "Bad Gateway\n")
	}
	return proxy
}

func parseTarget(name string, raw string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || target.Scheme != "http" || target.Host == "" || target.User != nil ||
		target.RawQuery != "" || target.Fragment != "" || (target.Path != "" && target.Path != "/") {
		return nil, fmt.Errorf("%s target must be an origin-only http URL", name)
	}
	target.Path = ""
	return target, nil
}

func requestIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddress))
	if err == nil {
		return host
	}
	return strings.TrimSpace(remoteAddress)
}

func isWebPath(path string) bool {
	switch path {
	case "/", "/official-management", "/native", "/my-keys", "/admin", "/usage",
		"/healthz", "/usage/api", "/site-config.json", "/branding/logo":
		return true
	}
	for _, prefix := range []string{"/native/", "/my-keys/", "/admin/", "/usage/", "/portal/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
