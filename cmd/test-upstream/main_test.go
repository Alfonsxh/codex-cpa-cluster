package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveInternalKeyRequiresOneRestrictedSource(t *testing.T) {
	directory := t.TempDir()
	keyFile := filepath.Join(directory, "fixture.key")
	if err := os.WriteFile(keyFile, []byte("fixture-internal-key\n"), 0o600); err != nil {
		t.Fatalf("write key file: %v", err)
	}

	key, err := resolveInternalKey(appConfig{InternalKeyFile: keyFile})
	if err != nil || key != "fixture-internal-key" {
		t.Fatalf("resolve key file = %q, %v", key, err)
	}
	if _, err := resolveInternalKey(appConfig{
		InternalKey:     "fixture-internal-key",
		InternalKeyFile: keyFile,
	}); err == nil {
		t.Fatal("multiple key sources must be rejected")
	}
	if err := os.Chmod(keyFile, 0o644); err != nil {
		t.Fatalf("chmod key file: %v", err)
	}
	if _, err := resolveInternalKey(appConfig{InternalKeyFile: keyFile}); err == nil {
		t.Fatal("group/world-readable key file must be rejected")
	}
}

func TestFixtureRoutesRequireInternalKeyAndStreamDeterministically(t *testing.T) {
	router, err := newRouter("fixture-internal-key")
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(
		unauthorized,
		httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"stream":true}`)),
	)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	authorized := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewBufferString(`{"stream":true}`))
	request.Header.Set("Authorization", "Bearer fixture-internal-key")
	router.ServeHTTP(authorized, request)
	if authorized.Code != http.StatusOK ||
		!strings.Contains(authorized.Body.String(), "response.output_text.delta") ||
		!strings.Contains(authorized.Body.String(), `"delta":"OK"`) ||
		!strings.Contains(authorized.Body.String(), "response.completed") {
		t.Fatalf("stream response = status %d body %q", authorized.Code, authorized.Body.String())
	}

	nonStream := httptest.NewRecorder()
	nonStreamRequest := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		bytes.NewBufferString(`{"stream":false}`),
	)
	nonStreamRequest.Header.Set("Authorization", "Bearer fixture-internal-key")
	router.ServeHTTP(nonStream, nonStreamRequest)
	if nonStream.Code != http.StatusOK ||
		!strings.Contains(nonStream.Body.String(), `"status":"completed"`) ||
		!strings.Contains(nonStream.Body.String(), `"text":"OK"`) {
		t.Fatalf("non-stream response = status %d body %q", nonStream.Code, nonStream.Body.String())
	}
}

func TestFixtureStreamDelaySupportsDrainAndCancellationTests(t *testing.T) {
	router, err := newRouter("fixture-internal-key")
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	started := time.Now()
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		bytes.NewBufferString(`{"stream":true,"fixture_delay_ms":5}`),
	)
	request.Header.Set("Authorization", "Bearer fixture-internal-key")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || time.Since(started) < 10*time.Millisecond {
		t.Fatalf("delayed stream = status %d elapsed %s", response.Code, time.Since(started))
	}
}

func TestFixtureStatsObserveClientCancellation(t *testing.T) {
	router, err := newRouter("fixture-internal-key")
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	server := httptest.NewServer(router)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		server.URL+"/v1/responses",
		bytes.NewBufferString(`{"stream":true,"fixture_delay_ms":10000}`),
	)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer fixture-internal-key")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("start delayed stream: %v", err)
	}
	reader := bufio.NewReader(response.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != "event: response.created\n" {
		t.Fatalf("first stream line = (%q, %v)", line, err)
	}
	cancel()
	_ = response.Body.Close()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		statsRequest, err := http.NewRequest(http.MethodGet, server.URL+"/v1/fixture/stats", nil)
		if err != nil {
			t.Fatalf("new stats request: %v", err)
		}
		statsRequest.Header.Set("Authorization", "Bearer fixture-internal-key")
		statsResponse, err := http.DefaultClient.Do(statsRequest)
		if err != nil {
			t.Fatalf("read fixture stats: %v", err)
		}
		body, readError := io.ReadAll(statsResponse.Body)
		_ = statsResponse.Body.Close()
		if readError != nil {
			t.Fatalf("read fixture stats body: %v", readError)
		}
		var stats map[string]int64
		if err := json.Unmarshal(body, &stats); err != nil {
			t.Fatalf("decode fixture stats: %v", err)
		}
		if stats["active"] == 0 && stats["started"] == 1 && stats["canceled"] == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("client cancellation did not reach the fixture")
}

func TestFixtureDrainBodyModeIsExplicitAndCounted(t *testing.T) {
	router, err := newRouter("fixture-internal-key")
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/responses",
		bytes.NewBufferString("fixture-body"),
	)
	request.Header.Set("Authorization", "Bearer fixture-internal-key")
	request.Header.Set("X-Codex-CPA-Fixture-Drain-Body", "1")
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"drained":true`) {
		t.Fatalf("drain response = status %d body %q", response.Code, response.Body.String())
	}

	stats := httptest.NewRecorder()
	statsRequest := httptest.NewRequest(http.MethodGet, "/v1/fixture/stats", nil)
	statsRequest.Header.Set("Authorization", "Bearer fixture-internal-key")
	router.ServeHTTP(stats, statsRequest)
	if stats.Code != http.StatusOK ||
		!strings.Contains(stats.Body.String(), `"started":1`) ||
		!strings.Contains(stats.Body.String(), `"completed":1`) {
		t.Fatalf("drain stats = status %d body %q", stats.Code, stats.Body.String())
	}
}
