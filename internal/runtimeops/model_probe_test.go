package runtimeops

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
)

type modelProbeFixture struct{ stopped, noKeys bool }

func (f modelProbeFixture) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return []controlplane.Account{{ID: "alpha"}, {ID: "empty"}}, nil
}
func (f modelProbeFixture) ReadInternalKeys(context.Context) (map[string]controlplane.InternalKey, error) {
	if f.noKeys {
		return nil, nil
	}
	return map[string]controlplane.InternalKey{"elsewhere@example.com": {Key: "fixture-internal-secret", Status: "active"}}, nil
}
func (f modelProbeFixture) List(context.Context) ([]Service, error) {
	state := "running"
	if f.stopped {
		state = "exited"
	}
	return []Service{{Service: "cliproxy-alpha", State: state}, {Service: "cliproxy-empty", State: state}}, nil
}

type modelProbeTransport func(*http.Request) (*http.Response, error)

func (f modelProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func probeResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestModelProbeUsesExactAccountWithoutRoutesAndRequiresActualText(t *testing.T) {
	fixture := modelProbeFixture{}
	requests := 0
	probe := NewModelProbe(fixture, fixture, &http.Client{Transport: modelProbeTransport(func(r *http.Request) (*http.Response, error) {
		requests++
		if r.URL.Host != "cliproxy-empty:8317" || r.Header.Get("Authorization") != "Bearer fixture-internal-secret" {
			t.Fatalf("unexpected target or credential")
		}
		if r.URL.Path == "/v1/models" {
			return probeResponse(200, `{"data":[{"id":"gpt-6-astra"},{"id":"gpt-6-astra"},{"id":"private@example.com"}]}`), nil
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil || body["model"] != "gpt-6-astra" || body["max_output_tokens"] != float64(64) || body["input"] != "Reply with exactly OK." || body["stream"] != false {
			t.Fatalf("unexpected generation payload: %#v", body)
		}
		return probeResponse(200, `{"status":"completed","output":[{"content":[{"type":"output_text","text":"OK"}]}]}`), nil
	})})
	models, err := probe.Models(context.Background(), "empty")
	if err != nil || len(models.Models) != 1 || models.Models[0] != "gpt-6-astra" || requests != 1 {
		t.Fatalf("models: %#v %v", models, err)
	}
	result, err := probe.Test(context.Background(), "empty", "gpt-6-astra")
	if err != nil || !result.Success || result.UpstreamStatus != 200 || requests != 2 {
		t.Fatalf("test: %#v %v", result, err)
	}
}

func TestModelProbeFailuresNeverExposeUpstreamBodiesOrFollowRedirects(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		code   string
	}{
		{"auth", 401, `{"error":{"message":"fixture-internal-secret private@example.com"}}`, "model_auth_failed"},
		{"limit", 429, `{"error":"fixture-internal-secret"}`, "model_rate_limited"},
		{"unsupported", 404, `{}`, "model_not_supported"},
		{"failed_event", 200, `{"status":"failed","output":[]}`, "model_test_failed"},
		{"no_text", 200, `{"status":"completed","output":[]}`, "model_test_failed"},
		{"invalid_json", 200, `not json`, "model_test_failed"},
		{"oversized", 200, strings.Repeat("x", modelProbeBodyLimit+1), "model_connection_failed"},
		{"redirect", 307, `{}`, "model_upstream_error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := modelProbeFixture{}
			calls := 0
			probe := NewModelProbe(fixture, fixture, &http.Client{Transport: modelProbeTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				response := probeResponse(tc.status, tc.body)
				response.Header.Set("Location", "https://example.com/untrusted")
				return response, nil
			})})
			result, err := probe.Test(context.Background(), "alpha", "gpt-6-astra")
			encoded, _ := json.Marshal(result)
			if err != nil || result.Success || result.Code != tc.code || calls != 1 || strings.Contains(string(encoded), "fixture-internal-secret") || strings.Contains(string(encoded), "private@example.com") {
				t.Fatalf("result: %s %v calls=%d", encoded, err, calls)
			}
		})
	}
}

func TestModelProbeRejectsInvalidOrUnavailableTargetsBeforeHTTP(t *testing.T) {
	for _, tc := range []struct {
		account, model string
		fixture        modelProbeFixture
	}{
		{"../other", "gpt-6-astra", modelProbeFixture{}},
		{"missing", "gpt-6-astra", modelProbeFixture{}},
		{"alpha", "bad\nmodel", modelProbeFixture{}},
		{"alpha", "gpt-6-astra", modelProbeFixture{stopped: true}},
		{"alpha", "gpt-6-astra", modelProbeFixture{noKeys: true}},
	} {
		probe := NewModelProbe(tc.fixture, tc.fixture, &http.Client{Transport: modelProbeTransport(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected HTTP request"); return nil, nil })})
		if _, err := probe.Test(context.Background(), tc.account, tc.model); err == nil {
			t.Fatal("expected rejection")
		}
	}
}

func TestModelProbeCancellationReleasesAccountAndPreventsConcurrentProbes(t *testing.T) {
	fixture := modelProbeFixture{}
	started := make(chan struct{}, 2)
	probe := NewModelProbe(fixture, fixture, &http.Client{Transport: modelProbeTransport(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _, _ = probe.Test(ctx, "alpha", "gpt-6-astra") }()
	<-started
	_, err := probe.Test(context.Background(), "alpha", "gpt-6-astra")
	var probeError *ModelProbeError
	if !errors.As(err, &probeError) || probeError.Code != "model_test_busy" {
		t.Fatalf("concurrent request: %v", err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop probe")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	result, err := probe.Test(ctx, "alpha", "gpt-6-astra")
	if err != nil || result.Code != "model_test_timeout" {
		t.Fatalf("timeout: %#v %v", result, err)
	}
}
