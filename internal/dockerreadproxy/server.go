package dockerreadproxy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"go.uber.org/zap"
)

const composeProjectLabel = "com.docker.compose.project"

var (
	apiVersionPrefix = regexp.MustCompile(`^/v[0-9]+(?:\.[0-9]+)?`)
	containerID      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)
)

type Config struct {
	Project        string
	UpstreamSocket string
	Logger         *zap.Logger
}

type Server struct {
	project   string
	upstream  *url.URL
	transport http.RoundTripper
	logger    *zap.Logger
}

func New(config Config) (*Server, error) {
	project := strings.TrimSpace(config.Project)
	if project == "" || len(project) > 128 || strings.ContainsAny(project, "\r\n\x00") {
		return nil, errors.New("Docker read proxy requires an exact Compose project")
	}
	socket := strings.TrimSpace(config.UpstreamSocket)
	if socket == "" || !strings.HasPrefix(socket, "/") || strings.ContainsAny(socket, "\r\n\x00") {
		return nil, errors.New("Docker read proxy requires an absolute upstream Unix socket")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
		DisableCompression: true,
	}
	return newWithTransport(project, &url.URL{Scheme: "http", Host: "docker"}, transport, config.Logger)
}

func newWithTransport(project string, upstream *url.URL, transport http.RoundTripper, logger *zap.Logger) (*Server, error) {
	if strings.TrimSpace(project) == "" || upstream == nil || transport == nil {
		return nil, errors.New("Docker read proxy configuration is incomplete")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Server{project: project, upstream: upstream, transport: transport, logger: logger}, nil
}

func (server *Server) Handler() http.Handler {
	return http.HandlerFunc(server.serveHTTP)
}

func (server *Server) CloseIdleConnections() {
	if closer, ok := server.transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (server *Server) serveHTTP(response http.ResponseWriter, request *http.Request) {
	kind, versionPrefix, identifier, allowed := classifyRequest(request)
	if !allowed {
		writeError(response, http.StatusForbidden, "Docker API operation is not available")
		return
	}
	outbound := request.Clone(request.Context())
	outbound.RequestURI = ""
	outbound.URL.Scheme = server.upstream.Scheme
	outbound.URL.Host = server.upstream.Host
	outbound.Host = server.upstream.Host
	removeHopHeaders(outbound.Header)

	switch kind {
	case "containers":
		filters, _ := json.Marshal(map[string]map[string]bool{
			"label": {composeProjectLabel + "=" + server.project: true},
		})
		query := outbound.URL.Query()
		query.Set("filters", string(filters))
		outbound.URL.RawQuery = query.Encode()
	case "logs":
		if !server.containerBelongsToProject(request.Context(), versionPrefix, identifier) {
			writeError(response, http.StatusForbidden, "Docker container is outside the allowed project")
			return
		}
		query := outbound.URL.Query()
		query.Set("follow", "0")
		query.Set("stdout", "1")
		query.Set("stderr", "1")
		query.Set("tail", "200")
		outbound.URL.RawQuery = query.Encode()
	}

	upstreamResponse, err := server.transport.RoundTrip(outbound)
	if err != nil {
		server.logger.Warn("Docker read proxy upstream unavailable")
		writeError(response, http.StatusBadGateway, "Docker read service is unavailable")
		return
	}
	defer upstreamResponse.Body.Close()
	copyHeaders(response.Header(), upstreamResponse.Header)
	response.WriteHeader(upstreamResponse.StatusCode)
	if _, err := io.Copy(response, upstreamResponse.Body); err != nil {
		server.logger.Warn("Docker read proxy response interrupted")
	}
}

func classifyRequest(request *http.Request) (kind string, versionPrefix string, identifier string, allowed bool) {
	if request == nil || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
		return "", "", "", false
	}
	if strings.ContainsRune(request.URL.Path, '\x00') {
		return "", "", "", false
	}
	path := request.URL.Path
	versionPrefix = apiVersionPrefix.FindString(path)
	path = strings.TrimPrefix(path, versionPrefix)
	switch {
	case (path == "/_ping" || path == "/version") && (request.Method == http.MethodGet || request.Method == http.MethodHead):
		return "metadata", versionPrefix, "", true
	case path == "/containers/json" && request.Method == http.MethodGet:
		return "containers", versionPrefix, "", true
	case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/logs") && request.Method == http.MethodGet:
		identifier = strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/logs")
		return "logs", versionPrefix, identifier, containerID.MatchString(identifier)
	case path == "/images/json" && request.Method == http.MethodGet:
		return "images", versionPrefix, "", true
	case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json") && request.Method == http.MethodGet:
		identifier = strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json")
		return "image", versionPrefix, identifier, identifier != "" && len(identifier) <= 512 &&
			!strings.Contains(identifier, "..") && !strings.Contains(identifier, "\\")
	default:
		return "", versionPrefix, "", false
	}
}

func (server *Server) containerBelongsToProject(ctx context.Context, versionPrefix string, identifier string) bool {
	inspectURL := *server.upstream
	inspectURL.Path = versionPrefix + "/containers/" + url.PathEscape(identifier) + "/json"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, inspectURL.String(), nil)
	if err != nil {
		return false
	}
	response, err := server.transport.RoundTrip(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var payload struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024))
	if err := decoder.Decode(&payload); err != nil {
		return false
	}
	return payload.Config.Labels[composeProjectLabel] == server.project
}

func copyHeaders(target http.Header, source http.Header) {
	for name, values := range source {
		if isHopHeader(name) {
			continue
		}
		for _, value := range values {
			target.Add(name, value)
		}
	}
}

func removeHopHeaders(header http.Header) {
	for name := range header {
		if isHopHeader(name) {
			header.Del(name)
		}
	}
}

func isHopHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}

func writeError(response http.ResponseWriter, status int, message string) {
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_, _ = fmt.Fprintf(response, `{"message":%q}`+"\n", message)
}
