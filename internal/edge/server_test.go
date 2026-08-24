package edge

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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

func TestServerPreservesPublicWebAndInternalRoutingBoundaries(t *testing.T) {
	selector, path := newTestSelector(t, Blue)
	type capturedRequest struct {
		host, requestHost, path, authorization, forwardedProto, forwardedFor, realIP string
	}
	var mutex sync.Mutex
	captured := make([]capturedRequest, 0)
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		mutex.Lock()
		captured = append(captured, capturedRequest{
			host: request.URL.Host, requestHost: request.Host, path: request.URL.Path,
			authorization:  request.Header.Get("Authorization"),
			forwardedProto: request.Header.Get("X-Forwarded-Proto"),
			forwardedFor:   request.Header.Get("X-Forwarded-For"),
			realIP:         request.Header.Get("X-Real-IP"),
		})
		mutex.Unlock()
		return &http.Response{
			StatusCode: http.StatusOK, Header: make(http.Header), ContentLength: -1,
			Body: io.NopCloser(strings.NewReader(request.URL.Host)), Request: request,
		}, nil
	})
	server := newTestServer(t, selector, transport)

	web := performEdgeRequest(server.PublicHandler(), http.MethodPost, "/admin/api/session", "Bearer external-key", 0)
	if web.Code != http.StatusOK || web.Body.String() != "web:8080" {
		t.Fatalf("Web route = %d %q", web.Code, web.Body.String())
	}
	health := performEdgeRequest(server.PublicHandler(), http.MethodGet, "/healthz", "", 0)
	if health.Code != http.StatusOK || health.Body.String() != "web:8080" {
		t.Fatalf("health route = %d %q", health.Code, health.Body.String())
	}
	api := performEdgeRequest(server.PublicHandler(), http.MethodPost, "/v1/responses?stream=true", "Bearer external-key", 0)
	if api.Code != http.StatusOK || api.Body.String() != "gateway-blue:8317" {
		t.Fatalf("public API route = %d %q", api.Code, api.Body.String())
	}
	for _, blockedPath := range []string{"/__internal/snapshots", "/__stats"} {
		blocked := performEdgeRequest(server.PublicHandler(), http.MethodGet, blockedPath, "Bearer external-key", 0)
		if blocked.Code != http.StatusNotFound {
			t.Fatalf("public %s status = %d", blockedPath, blocked.Code)
		}
	}
	internal := performEdgeRequest(server.InternalHandler(), http.MethodGet, "/__internal/snapshots", "Bearer internal-probe", 0)
	if internal.Code != http.StatusOK || internal.Body.String() != "gateway-blue:8319" {
		t.Fatalf("internal route = %d %q", internal.Code, internal.Body.String())
	}
	if response := performEdgeRequest(server.InternalHandler(), http.MethodGet, "/v1/models", "", 0); response.Code != http.StatusNotFound {
		t.Fatalf("internal public path status = %d", response.Code)
	}
	activeSlot := performEdgeRequest(server.InternalHandler(), http.MethodGet, "/__internal/edge/slot", "Bearer must-not-forward", 0)
	if activeSlot.Code != http.StatusOK || activeSlot.Body.String() != "blue\n" ||
		activeSlot.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("active Edge slot = %d %q %#v", activeSlot.Code, activeSlot.Body.String(), activeSlot.Header())
	}
	if response := performEdgeRequest(server.PublicHandler(), http.MethodGet, "/__internal/edge/slot", "", 0); response.Code != http.StatusNotFound {
		t.Fatalf("public active-slot status = %d", response.Code)
	}

	mutex.Lock()
	requests := append([]capturedRequest(nil), captured...)
	mutex.Unlock()
	if len(requests) != 4 || requests[2].authorization != "Bearer external-key" ||
		requests[3].authorization != "Bearer internal-probe" || requests[2].forwardedProto != "http" ||
		requests[2].forwardedFor != "192.0.2.1" || requests[2].realIP != "192.0.2.1" ||
		requests[2].requestHost != "api.example.test" {
		t.Fatalf("captured requests = %#v", requests)
	}

	writeSelectionFixture(t, path, Green)
	if changed, err := selector.Refresh(); err != nil || !changed {
		t.Fatalf("switch selector = (%v, %v)", changed, err)
	}
	green := performEdgeRequest(server.PublicHandler(), http.MethodGet, "/__health", "", 0)
	if green.Code != http.StatusOK || green.Body.String() != "gateway-green:8317" {
		t.Fatalf("green health route = %d %q", green.Code, green.Body.String())
	}
	activeSlot = performEdgeRequest(server.InternalHandler(), http.MethodGet, "/__internal/edge/slot", "", 0)
	if activeSlot.Code != http.StatusOK || activeSlot.Body.String() != "green\n" {
		t.Fatalf("active Edge slot after switch = %d %q", activeSlot.Code, activeSlot.Body.String())
	}
}

func TestWebPathAllowlistMatchesCurrentStableEdge(t *testing.T) {
	for _, path := range []string{
		"/", "/official-management", "/native", "/native/", "/native/app.js", "/my-keys",
		"/my-keys/", "/admin", "/admin/api/session", "/usage", "/usage/api", "/usage/me", "/healthz",
		"/portal/app.js", "/site-config.json", "/branding/logo",
	} {
		if !isWebPath(path) {
			t.Fatalf("Web path %q was not routed to Web", path)
		}
	}
	for _, path := range []string{
		"/__health", "/__stats", "/__internal/snapshots", "/v1/responses", "/v1beta/models",
		"/backend-api/codex/responses", "/api/provider/test", "/usage-api", "/administer",
	} {
		if isWebPath(path) {
			t.Fatalf("data-plane path %q was routed to Web", path)
		}
	}
}

func TestServerRejectsUnsafeUpstreamTargets(t *testing.T) {
	selector, _ := newTestSelector(t, Blue)
	base := ServerConfig{
		Selector:         selector,
		WebTarget:        "http://web:8080",
		BluePublicTarget: "http://gateway-blue:8317", BlueInternalTarget: "http://gateway-blue:8319",
		GreenPublicTarget: "http://gateway-green:8317", GreenInternalTarget: "http://gateway-green:8319",
	}
	for _, target := range []string{
		"", "https://web:8080", "http://user:password@web:8080", "http://web:8080/path", "http://web:8080?key=value",
	} {
		config := base
		config.WebTarget = target
		if _, err := NewServer(config); err == nil {
			t.Fatalf("unsafe Web target accepted: %q", target)
		}
	}
}

func TestServerRejectsOversizedRequestBeforeUpstream(t *testing.T) {
	selector, _ := newTestSelector(t, Blue)
	calls := 0
	server := newTestServer(t, selector, roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		return nil, fmt.Errorf("unexpected upstream request: %s", request.URL)
	}))
	response := performEdgeRequest(server.PublicHandler(), http.MethodPost, "/v1/responses", "", DefaultMaxBodyBytes+1)
	if response.Code != http.StatusRequestEntityTooLarge || calls != 0 {
		t.Fatalf("oversized response = %d, upstream calls=%d", response.Code, calls)
	}
}

func TestServerReturns413ForChunkedRequestThatCrossesLimit(t *testing.T) {
	selector, _ := newTestSelector(t, Blue)
	readBytes := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(request.Body)
		readBytes = len(raw)
		if err == nil {
			return nil, errors.New("expected request body limit error")
		}
		return nil, err
	})
	server, err := NewServer(ServerConfig{
		Selector: selector, Transport: transport, MaxBodyBytes: 8,
		WebTarget:        "http://web:8080",
		BluePublicTarget: "http://gateway-blue:8317", BlueInternalTarget: "http://gateway-blue:8319",
		GreenPublicTarget: "http://gateway-green:8317", GreenInternalTarget: "http://gateway-green:8319",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString("123456789"))
	request.ContentLength = -1
	response := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder(), closed: make(chan bool)}
	server.PublicHandler().ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || readBytes != 8 {
		t.Fatalf("chunked oversized response = %d, bytes forwarded=%d", response.Code, readBytes)
	}
}

func TestServerTransportErrorsCannotInjectAuthorizationIntoLogs(t *testing.T) {
	selector, _ := newTestSelector(t, Blue)
	core, observed := observer.New(zap.WarnLevel)
	secret := "Bearer should-never-enter-edge-logs"
	server, err := NewServer(ServerConfig{
		Selector: selector,
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("transport echoed Authorization: %s", secret)
		}),
		Logger:           zap.New(core),
		WebTarget:        "http://web:8080",
		BluePublicTarget: "http://gateway-blue:8317", BlueInternalTarget: "http://gateway-blue:8319",
		GreenPublicTarget: "http://gateway-green:8317", GreenInternalTarget: "http://gateway-green:8319",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	response := performEdgeRequest(server.PublicHandler(), http.MethodPost, "/v1/responses", secret, 0)
	if response.Code != http.StatusBadGateway {
		t.Fatalf("transport failure status = %d", response.Code)
	}
	for _, entry := range observed.All() {
		if strings.Contains(entry.Message, secret) || strings.Contains(entry.ContextMap()["error_type"].(string), secret) {
			t.Fatalf("Edge log leaked Authorization: %#v", entry)
		}
	}
}

func TestServerStreamsAndPropagatesClientCancellation(t *testing.T) {
	upstreamStarted := make(chan struct{})
	upstreamCanceled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		close(upstreamStarted)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: first\n\n")
		writer.(http.Flusher).Flush()
		<-request.Context().Done()
		close(upstreamCanceled)
	}))
	defer upstream.Close()
	address := strings.TrimPrefix(upstream.URL, "http://")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network string, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, address)
	}
	selector, _ := newTestSelector(t, Blue)
	edge := newTestServer(t, selector, transport)
	public := httptest.NewServer(edge.PublicHandler())
	defer public.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, public.URL+"/v1/responses", nil)
	if err != nil {
		t.Fatalf("create streaming request: %v", err)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("start streaming request: %v", err)
	}
	defer response.Body.Close()
	<-upstreamStarted
	firstLine := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(response.Body).ReadString('\n')
		firstLine <- line
	}()
	select {
	case line := <-firstLine:
		if line != "data: first\n" {
			t.Fatalf("first streamed line = %q", line)
		}
	case <-time.After(time.Second):
		t.Fatal("first Edge streaming chunk was buffered")
	}
	cancel()
	select {
	case <-upstreamCanceled:
	case <-time.After(time.Second):
		t.Fatal("Edge client cancellation did not reach upstream")
	}
}

func TestGatewaySwitchMovesOnlyNewRequestsAndLetsExistingStreamDrain(t *testing.T) {
	blueStarted := make(chan struct{})
	releaseBlue := make(chan struct{})
	blue := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/responses" {
			_, _ = io.WriteString(writer, "blue")
			return
		}
		close(blueStarted)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, "data: blue-first\n\n")
		writer.(http.Flusher).Flush()
		<-releaseBlue
		_, _ = io.WriteString(writer, "data: blue-last\n\n")
	}))
	defer blue.Close()
	green := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(writer, "green")
	}))
	defer green.Close()
	selector, path := newTestSelector(t, Blue)
	edge, err := NewServer(ServerConfig{
		Selector:         selector,
		WebTarget:        green.URL,
		BluePublicTarget: blue.URL, BlueInternalTarget: blue.URL,
		GreenPublicTarget: green.URL, GreenInternalTarget: green.URL,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	public := httptest.NewServer(edge.PublicHandler())
	defer public.Close()

	stream, err := http.Get(public.URL + "/v1/responses")
	if err != nil {
		t.Fatalf("start blue stream: %v", err)
	}
	defer stream.Body.Close()
	<-blueStarted
	reader := bufio.NewReader(stream.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "data: blue-first\n" {
		t.Fatalf("first blue line = (%q, %v)", line, err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("read blue event separator: %v", err)
	}
	writeSelectionFixture(t, path, Green)
	if changed, err := selector.Refresh(); err != nil || !changed {
		t.Fatalf("switch to green = (%v, %v)", changed, err)
	}
	newRequest, err := http.Get(public.URL + "/__health")
	if err != nil {
		t.Fatalf("request green health: %v", err)
	}
	greenBody, err := io.ReadAll(newRequest.Body)
	_ = newRequest.Body.Close()
	if err != nil || string(greenBody) != "green" {
		t.Fatalf("green response = (%q, %v)", greenBody, err)
	}
	close(releaseBlue)
	remaining, err := io.ReadAll(reader)
	if err != nil || !strings.Contains(string(remaining), "data: blue-last") || strings.Contains(string(remaining), "green") {
		t.Fatalf("drained blue stream = (%q, %v)", remaining, err)
	}
}

func newTestSelector(t *testing.T, slot Slot) (*Selector, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "active-gateway.conf")
	writeSelectionFixture(t, path, slot)
	selector, err := NewSelector(path, 10*time.Millisecond, nil)
	if err != nil {
		t.Fatalf("NewSelector: %v", err)
	}
	return selector, path
}

func newTestServer(t *testing.T, selector *Selector, transport http.RoundTripper) *Server {
	t.Helper()
	server, err := NewServer(ServerConfig{
		Selector: selector, Transport: transport,
		WebTarget:        "http://web:8080",
		BluePublicTarget: "http://gateway-blue:8317", BlueInternalTarget: "http://gateway-blue:8319",
		GreenPublicTarget: "http://gateway-green:8317", GreenInternalTarget: "http://gateway-green:8319",
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func performEdgeRequest(
	handler http.Handler,
	method string,
	path string,
	authorization string,
	contentLength int64,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Host = "api.example.test"
	request.RemoteAddr = "192.0.2.1:12345"
	request.ContentLength = contentLength
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response := &closeNotifyRecorder{ResponseRecorder: httptest.NewRecorder(), closed: make(chan bool)}
	handler.ServeHTTP(response, request)
	return response.ResponseRecorder
}
