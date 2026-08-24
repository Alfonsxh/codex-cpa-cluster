package web

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/zap/zaptest"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
	closed chan bool
}

func (recorder *closeNotifyRecorder) CloseNotify() <-chan bool {
	return recorder.closed
}

func TestServerServesOnlyExpectedStaticSurfaces(t *testing.T) {
	roots := createWebFixture(t)
	server := newTestServer(t, roots, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/healthz" {
			header := make(http.Header)
			header.Set("Content-Type", "application/json")
			return &http.Response{
				StatusCode: http.StatusOK, Header: header, ContentLength: -1,
				Body: io.NopCloser(strings.NewReader(`{"status":"ok"}`)), Request: request,
			}, nil
		}
		return nil, fmt.Errorf("unexpected proxy request: %s", request.URL.Path)
	}))

	for _, test := range []struct {
		path, body, cache string
		status            int
	}{
		{path: "/", body: "landing", cache: "no-cache", status: http.StatusOK},
		{path: "/portal/assets/app-12345678.js", body: "portal-script", cache: "public, max-age=31536000, immutable", status: http.StatusOK},
		{path: "/portal/assets/codex-cpa-cluster-logo.svg", body: "portal-logo", cache: "no-cache", status: http.StatusOK},
		{path: "/portal/private.txt", status: http.StatusNotFound},
		{path: "/native/", body: "landing", cache: "no-cache", status: http.StatusOK},
		{path: "/native/accounts.json", status: http.StatusNotFound},
		{path: "/admin/", body: "admin-index", cache: "no-cache", status: http.StatusOK},
		{path: "/admin/accounts", body: "admin-index", cache: "no-cache", status: http.StatusOK},
		{path: "/admin/assets/app-12345678.js", body: "admin-script", cache: "public, max-age=31536000, immutable", status: http.StatusOK},
		{path: "/usage/", body: "usage-index", cache: "no-cache", status: http.StatusOK},
		{path: "/usage/history", body: "usage-index", cache: "no-cache", status: http.StatusOK},
		{path: "/usage/assets/app-12345678.css", body: "usage-style", cache: "public, max-age=31536000, immutable", status: http.StatusOK},
		{path: "/admin/.secret", status: http.StatusNotFound},
		{path: "/portal/.secret", status: http.StatusNotFound},
		{path: "/unknown", status: http.StatusNotFound},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := performRequest(server.Handler(), http.MethodGet, test.path, nil)
			if response.Code != test.status || (test.body != "" && response.Body.String() != test.body) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
			if test.cache != "" && response.Header().Get("Cache-Control") != test.cache {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
			if test.status == http.StatusOK && response.Header().Get("X-Content-Type-Options") != "nosniff" {
				t.Fatalf("security headers = %#v", response.Header())
			}
		})
	}

	for path, location := range map[string]string{
		"/admin": "/admin/", "/usage": "/usage/", "/native": "/native/",
		"/my-keys": "/usage/", "/official-management": "/native/",
	} {
		response := performRequest(server.Handler(), http.MethodGet, path, nil)
		if response.Code < 300 || response.Code >= 400 || response.Header().Get("Location") != location {
			t.Fatalf("redirect %s = %d %q", path, response.Code, response.Header().Get("Location"))
		}
	}
	health := newCloseNotifyRecorder()
	healthRequest := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	healthRequest.RemoteAddr = "192.0.2.10:12345"
	server.Handler().ServeHTTP(health, healthRequest)
	if health.Code != http.StatusOK || health.Body.String() != `{"status":"ok"}` || health.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("health = %d %q %#v", health.Code, health.Body.String(), health.Header())
	}
}

func TestServerRejectsStaticTraversalAndSymbolicLinks(t *testing.T) {
	roots := createWebFixture(t)
	if err := os.Symlink(filepath.Join(roots.portal, "app.js"), filepath.Join(roots.portal, "linked.js")); err != nil {
		t.Fatalf("create static symlink: %v", err)
	}
	server := newTestServer(t, roots, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("unexpected proxy request: %s", request.URL.Path)
	}))
	for _, path := range []string{
		"/portal/linked.js", "/portal/../admin/index.html", "/admin/assets/../index.html",
		"/usage/assets/../../portal/app.js", "/portal/%2e%2e/admin/index.html",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		response := httptest.NewRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("unsafe path %q returned %d", path, response.Code)
		}
	}
}

func TestServerProxiesFineGrainedAPIsAndStripsPublicCredentials(t *testing.T) {
	roots := createWebFixture(t)
	type capturedRequest struct {
		path, authorization, managementKey, host, realIP, forwardedProto string
	}
	var mutex sync.Mutex
	captured := make([]capturedRequest, 0)
	server := newTestServer(t, roots, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		captured = append(captured, capturedRequest{
			path: request.URL.Path, authorization: request.Header.Get("Authorization"),
			managementKey: request.Header.Get("X-Management-Key"), host: request.Host,
			realIP: request.Header.Get("X-Real-IP"), forwardedProto: request.Header.Get("X-Forwarded-Proto"),
		})
		mutex.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: -1,
			Body: io.NopCloser(strings.NewReader(request.URL.Path)), Request: request,
		}, nil
	}))

	admin := httptest.NewRequest(http.MethodGet, "http://web.example.test/admin/api/session", nil)
	admin.RemoteAddr = "192.0.2.20:12345"
	admin.Header.Set("Authorization", "Bearer admin-automation")
	admin.Header.Set("X-Management-Key", "management-secret")
	adminResponse := newCloseNotifyRecorder()
	server.Handler().ServeHTTP(adminResponse, admin)
	if adminResponse.Code != http.StatusOK || adminResponse.Body.String() != "/admin/api/session" {
		t.Fatalf("Admin proxy = %d %q", adminResponse.Code, adminResponse.Body.String())
	}

	for _, path := range []string{"/usage/me", "/usage/me/route", "/site-config.json", "/branding/logo", "/my-keys/api", "/admin/reasoning-effort-colors.css"} {
		request := httptest.NewRequest(http.MethodGet, "http://web.example.test"+path, nil)
		request.RemoteAddr = "192.0.2.21:12345"
		request.Header.Set("Authorization", "Bearer must-be-removed")
		request.Header.Set("X-Management-Key", "must-be-removed")
		request.Header.Set("X-Forwarded-Proto", "https")
		response := newCloseNotifyRecorder()
		server.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != path || response.Header().Get("Strict-Transport-Security") != "max-age=0" {
			t.Fatalf("public proxy %s = %d %q %#v", path, response.Code, response.Body.String(), response.Header())
		}
	}

	mutex.Lock()
	requests := append([]capturedRequest(nil), captured...)
	mutex.Unlock()
	if len(requests) != 7 || requests[0].authorization != "Bearer admin-automation" ||
		requests[0].managementKey != "management-secret" || requests[0].host != "web.example.test" ||
		requests[0].realIP != "192.0.2.20" || requests[0].forwardedProto != "http" {
		t.Fatalf("Admin capture = %#v", requests)
	}
	for _, request := range requests[1:] {
		if request.authorization != "" || request.managementKey != "" || request.forwardedProto != "https" {
			t.Fatalf("public credentials were forwarded: %#v", request)
		}
	}
}

func TestServerRejectsOversizedProxyBodyBeforeAdmin(t *testing.T) {
	roots := createWebFixture(t)
	calls := 0
	server, err := NewServer(Config{
		PortalRoot: roots.portal, AdminRoot: roots.admin, UsageRoot: roots.usage,
		AdminTarget: "http://admin:8318", MaxBodyBytes: 8,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			calls++
			return nil, fmt.Errorf("unexpected Admin request: %s", request.URL.Path)
		}),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	response := performRequest(server.Handler(), http.MethodPost, "/admin/api/users", strings.NewReader("123456789"))
	if response.Code != http.StatusRequestEntityTooLarge || calls != 0 {
		t.Fatalf("oversized response = %d, Admin calls=%d", response.Code, calls)
	}
}

func TestServerRejectsMissingRootsAndUnsafeAdminTarget(t *testing.T) {
	roots := createWebFixture(t)
	base := Config{
		PortalRoot: roots.portal, AdminRoot: roots.admin, UsageRoot: roots.usage,
		AdminTarget: "http://admin:8318",
	}
	for name, mutate := range map[string]func(*Config){
		"missing portal": func(config *Config) { config.PortalRoot = filepath.Join(t.TempDir(), "missing") },
		"https target":   func(config *Config) { config.AdminTarget = "https://admin:8318" },
		"target path":    func(config *Config) { config.AdminTarget = "http://admin:8318/private" },
		"target user":    func(config *Config) { config.AdminTarget = "http://user:secret@admin:8318" },
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := NewServer(config); err == nil {
				t.Fatal("unsafe Web configuration was accepted")
			}
		})
	}
}

type webRoots struct {
	portal string
	admin  string
	usage  string
}

func createWebFixture(t *testing.T) webRoots {
	t.Helper()
	root := t.TempDir()
	roots := webRoots{
		portal: filepath.Join(root, "portal"),
		admin:  filepath.Join(root, "admin"),
		usage:  filepath.Join(root, "usage"),
	}
	for _, directory := range []string{
		roots.portal, roots.admin, roots.usage,
		filepath.Join(roots.portal, "assets"), filepath.Join(roots.admin, "assets"), filepath.Join(roots.usage, "assets"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatalf("create Web fixture directory: %v", err)
		}
	}
	for path, content := range map[string]string{
		filepath.Join(roots.portal, "index.html"):                           "landing",
		filepath.Join(roots.portal, "assets", "app-12345678.js"):            "portal-script",
		filepath.Join(roots.portal, "assets", "codex-cpa-cluster-logo.svg"): "portal-logo",
		filepath.Join(roots.portal, "private.txt"):                          "private",
		filepath.Join(roots.portal, ".secret"):                              "hidden",
		filepath.Join(roots.admin, "index.html"):                            "admin-index",
		filepath.Join(roots.admin, "assets", "app-12345678.js"):             "admin-script",
		filepath.Join(roots.usage, "index.html"):                            "usage-index",
		filepath.Join(roots.usage, "assets", "app-12345678.css"):            "usage-style",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("create Web fixture file: %v", err)
		}
	}
	return roots
}

func newTestServer(t *testing.T, roots webRoots, transport http.RoundTripper) *Server {
	t.Helper()
	server, err := NewServer(Config{
		PortalRoot: roots.portal, AdminRoot: roots.admin, UsageRoot: roots.usage,
		AdminTarget: "http://admin:8318", Transport: transport, Logger: zaptest.NewLogger(t),
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func performRequest(handler http.Handler, method string, path string, body io.Reader) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, body)
	request.RemoteAddr = "192.0.2.10:12345"
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func newCloseNotifyRecorder() *closeNotifyRecorder {
	return &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder(), closed: make(chan bool)}
}
