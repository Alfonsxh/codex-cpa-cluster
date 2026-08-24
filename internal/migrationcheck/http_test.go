package migrationcheck

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRunnerComparesPublicInternalAndStreamingContracts(t *testing.T) {
	testKey := "dedicated-test-key"
	v1 := newComparisonServer(t, testKey, "model-a")
	defer v1.Close()
	v2 := newComparisonServer(t, testKey, "model-a")
	defer v2.Close()
	runner, err := New(Config{
		V1PublicURL: v1.URL, V2PublicURL: v2.URL,
		V1InternalURL: v1.URL, V2InternalURL: v2.URL,
		TestKey: testKey, Timeout: 2 * time.Second,
		StreamBody: []byte(`{"model":"fixture","stream":true,"input":"test-only"}`),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report := runner.Run(context.Background())
	if !report.Passed || len(report.Checks) != 6 {
		t.Fatalf("report = %#v", report)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(raw), testKey) || strings.Contains(string(raw), "test-only") {
		t.Fatalf("sanitized report leaked a test credential or request body: %s", raw)
	}
	models := findCheck(t, report, "dedicated_test_key_models")
	if models.V1.PayloadSHA256 == "" || models.V1.PayloadSHA256 != models.V2.PayloadSHA256 || models.V1.ItemCount != 1 {
		t.Fatalf("model comparison = %#v", models)
	}
}

func TestRunnerFailsClosedOnModelRoutingDifference(t *testing.T) {
	v1 := newComparisonServer(t, "dedicated-test-key", "model-a")
	defer v1.Close()
	v2 := newComparisonServer(t, "dedicated-test-key", "model-b")
	defer v2.Close()
	runner, err := New(Config{
		V1PublicURL: v1.URL, V2PublicURL: v2.URL,
		TestKey: "dedicated-test-key", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report := runner.Run(context.Background())
	if report.Passed || findCheck(t, report, "dedicated_test_key_models").Passed {
		t.Fatalf("model mismatch passed: %#v", report)
	}
}

func TestRunnerAcceptsDifferentActiveSnapshotGenerations(t *testing.T) {
	v1 := newComparisonServerWithGenerations(
		t, "dedicated-test-key", "model-a", strings.Repeat("a", 32), strings.Repeat("b", 32),
	)
	defer v1.Close()
	v2 := newComparisonServerWithGenerations(
		t, "dedicated-test-key", "model-a", strings.Repeat("c", 32), strings.Repeat("d", 32),
	)
	defer v2.Close()
	runner, err := New(Config{
		V1PublicURL: v1.URL, V2PublicURL: v2.URL,
		V1InternalURL: v1.URL, V2InternalURL: v2.URL,
		TestKey: "dedicated-test-key", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report := runner.Run(context.Background())
	if !report.Passed || !findCheck(t, report, "active_snapshot_generations").Passed {
		t.Fatalf("active but independently published generations failed: %#v", report)
	}
}

func TestRunnerReportsSanitizedStreamingErrorContract(t *testing.T) {
	testKey := "dedicated-test-key"
	v1 := newStreamErrorServer(t, testKey)
	defer v1.Close()
	v2 := newStreamErrorServer(t, testKey)
	defer v2.Close()
	runner, err := New(Config{
		V1PublicURL: v1.URL, V2PublicURL: v2.URL,
		TestKey: testKey, Timeout: 2 * time.Second,
		StreamBody: []byte(`{"model":"fixture","stream":true,"input":"must-not-leak"}`),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report := runner.Run(context.Background())
	stream := findCheck(t, report, "codex_responses_stream")
	if stream.Passed || stream.V1.Status != http.StatusServiceUnavailable ||
		stream.V1.ErrorType != "server_error" || stream.V1.ErrorCode != "upstream_overloaded" ||
		stream.V2.Status != stream.V1.Status || stream.V2.ContentType != stream.V1.ContentType ||
		stream.V2.ErrorType != stream.V1.ErrorType || stream.V2.ErrorCode != stream.V1.ErrorCode {
		t.Fatalf("streaming error observation = %#v", stream)
	}
	raw, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(raw), testKey) || strings.Contains(string(raw), "must-not-leak") ||
		strings.Contains(string(raw), "private upstream detail") {
		t.Fatalf("sanitized report leaked a credential, request, or error message: %s", raw)
	}
}

func TestRunnerRejectsUnsafeOrAmbiguousOrigins(t *testing.T) {
	base := Config{
		V1PublicURL: "http://127.0.0.1:18317", V2PublicURL: "http://127.0.0.1:28317",
		TestKey: "dedicated-test-key", Timeout: time.Second,
	}
	for name, mutate := range map[string]func(*Config){
		"remote by default": func(config *Config) { config.V2PublicURL = "https://test.example.invalid" },
		"URL credentials":   func(config *Config) { config.V2PublicURL = "http://user:secret@127.0.0.1:28317" },
		"URL query":         func(config *Config) { config.V2PublicURL = "http://127.0.0.1:28317?key=secret" },
		"same origin":       func(config *Config) { config.V2PublicURL = config.V1PublicURL },
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatalf("unsafe config accepted: %#v", config)
			}
		})
	}
	base.V2PublicURL = "https://test.example.invalid"
	base.AllowNonLoopback = true
	if _, err := New(base); err != nil {
		t.Fatalf("explicit remote Test target rejected: %v", err)
	}
}

func newComparisonServer(t *testing.T, testKey string, model string) *httptest.Server {
	return newComparisonServerWithGenerations(
		t,
		testKey,
		model,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
}

func newComparisonServerWithGenerations(
	t *testing.T,
	testKey string,
	model string,
	authGeneration string,
	quotaGeneration string,
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/__health":
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = writer.Write([]byte("ok\n"))
		case "/v0/management":
			if request.Header.Get("Authorization") != "Bearer "+testKey {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"error":{"type":"invalid_request_error"}}`))
				return
			}
			writer.WriteHeader(http.StatusNotFound)
		case "/__internal/snapshots":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(
				writer,
				`{"auth":{"active_generation":%q},"quota":{"active_generation":%q}}`,
				authGeneration,
				quotaGeneration,
			)
		case "/v1/models":
			if request.Header.Get("Authorization") != "Bearer "+testKey {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"error":{"message":"Invalid API key","type":"invalid_request_error"}}`))
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(writer, `{"object":"list","data":[{"id":%q}]}`, model)
		case "/v1/responses":
			if request.Header.Get("Authorization") != "Bearer "+testKey {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
			_, _ = writer.Write([]byte("data: {\"type\":\"response.created\"}\n\n"))
			writer.(http.Flusher).Flush()
			_, _ = writer.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

func newStreamErrorServer(t *testing.T, testKey string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/__health":
			writer.Header().Set("Content-Type", "text/plain")
			_, _ = writer.Write([]byte("ok\n"))
		case "/v0/management":
			if request.Header.Get("Authorization") != "Bearer "+testKey {
				writer.WriteHeader(http.StatusUnauthorized)
				return
			}
			writer.WriteHeader(http.StatusNotFound)
		case "/v1/models":
			if request.Header.Get("Authorization") != "Bearer "+testKey {
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"error":{"type":"invalid_request_error"}}`))
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"object":"list","data":[{"id":"model-a"}]}`))
		case "/v1/responses":
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"error":{"message":"private upstream detail","type":"server_error","code":"upstream_overloaded"}}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
}

func findCheck(t *testing.T, report Report, name string) CheckResult {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name == name {
			return check
		}
	}
	t.Fatalf("check not found: %s", name)
	return CheckResult{}
}
