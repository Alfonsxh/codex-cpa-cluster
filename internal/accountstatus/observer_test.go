package accountstatus

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestObserverMatchesV1RuntimePrecedenceAndCaches(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "logs", "gateway"), 0o700); err != nil {
		t.Fatalf("create gateway log directory: %v", err)
	}
	now := time.Unix(10_000, 0)
	access := fmt.Sprintf(
		"%.3f\talice@example.com:alpha\talpha\t500\t0.1\n"+
			"%.3f\tbob@example.com:beta\tbeta\t429\t0.1\n"+
			"%.3f\tignored@example.com:alpha\talpha\t400\t0.1\n",
		float64(now.Add(-5*time.Minute).Unix()),
		float64(now.Add(-2*time.Minute).Unix()),
		float64(now.Add(-time.Minute).Unix()),
	)
	if err := os.WriteFile(filepath.Join(root, "logs", "gateway", "access.tsv"), []byte(access), 0o600); err != nil {
		t.Fatalf("write access log: %v", err)
	}

	payloads := map[string]string{
		"cliproxy-alpha":   `{"files":[{"status":"active"}]}`,
		"cliproxy-beta":    `{"files":[{"status":"active"}]}`,
		"cliproxy-gamma":   `{"files":[{"status":"error","unavailable":true,"status_message":"invalid_grant"}]}`,
		"cliproxy-delta":   `{"files":[{"status":"error","unavailable":true,"status_message":"upstream_error: connection refused"}]}`,
		"cliproxy-epsilon": `{"files":[{"status":"error","unavailable":true,"status_message":"{\"error\":{\"type\":\"usage_limit_reached\"}}"}]}`,
		"cliproxy-zeta":    `{"files":[]}`,
		"cliproxy-eta":     `{"files":[{"status":"active"}]}`,
	}
	transport := &runtimeFixtureTransport{payloads: payloads, requests: make(map[string]int)}
	observer, err := New(Config{
		Root: root, Secrets: fixedSecretReader{value: "management-secret"},
		Transport: transport, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	services := map[string]string{}
	for _, account := range []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta"} {
		services[account] = "cliproxy-" + account
	}

	states := observer.Observe(context.Background(), services)
	assertState(t, states["alpha"], ReasonDegraded, false, false)
	assertState(t, states["beta"], ReasonRateLimited, false, false)
	assertState(t, states["gamma"], ReasonCredentialUnavailable, true, false)
	assertState(t, states["delta"], ReasonTransientCooldown, false, false)
	assertState(t, states["epsilon"], ReasonQuotaExhausted, true, true)
	assertState(t, states["zeta"], ReasonCredentialUnavailable, true, false)
	assertState(t, states["eta"], "", false, false)
	assertState(t, states["theta"], ReasonRuntimeUnknown, false, false)

	firstRequests := transport.requestCount()
	if firstRequests != len(services) {
		t.Fatalf("native request count = %d, want %d", firstRequests, len(services))
	}
	states["alpha"] = State{Reason: "caller-mutation"}
	cached := observer.Observe(context.Background(), services)
	assertState(t, cached["alpha"], ReasonDegraded, false, false)
	if transport.requestCount() != firstRequests {
		t.Fatal("fresh 15-second observer cache issued another native request")
	}
	if transport.invalidAuthorization {
		t.Fatal("native probe omitted or changed the management authorization header")
	}
}

func TestObserverResetsIncrementalAccessStateAfterSameFileRewrite(t *testing.T) {
	root := t.TempDir()
	logDirectory := filepath.Join(root, "logs", "gateway")
	if err := os.MkdirAll(logDirectory, 0o700); err != nil {
		t.Fatalf("create gateway log directory: %v", err)
	}
	path := filepath.Join(logDirectory, "access.tsv")
	now := time.Unix(20_000, 0)
	write := func(status int) {
		t.Helper()
		row := fmt.Sprintf("%.3f\talice@example.com:alpha\talpha\t%d\t0.1\n", float64(now.Unix()), status)
		if err := os.WriteFile(path, []byte(row), 0o600); err != nil {
			t.Fatalf("write access row: %v", err)
		}
	}
	write(http.StatusInternalServerError)
	transport := &runtimeFixtureTransport{
		payloads: map[string]string{"cliproxy-alpha": `{"files":[{"status":"active"}]}`},
		requests: make(map[string]int),
	}
	observer, err := New(Config{
		Root: root, Secrets: fixedSecretReader{value: "management-secret"}, Transport: transport,
		Now: func() time.Time { return now }, CacheTTL: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	services := map[string]string{"alpha": "cliproxy-alpha"}
	assertState(t, observer.Observe(context.Background(), services)["alpha"], ReasonDegraded, false, false)

	write(http.StatusOK)
	now = now.Add(2 * time.Second)
	assertState(t, observer.Observe(context.Background(), services)["alpha"], "", false, false)
}

func TestObserverRejectsUnexpectedServiceBeforeSendingCredential(t *testing.T) {
	root := t.TempDir()
	transport := &runtimeFixtureTransport{payloads: make(map[string]string), requests: make(map[string]int)}
	observer, err := New(Config{
		Root: root, Secrets: fixedSecretReader{value: "management-secret"}, Transport: transport,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	states := observer.Observe(context.Background(), map[string]string{
		"alpha":            "attacker.example.test",
		"attacker.example": "cliproxy-attacker.example",
	})
	assertState(t, states["alpha"], ReasonRuntimeUnknown, false, false)
	assertState(t, states["attacker.example"], ReasonRuntimeUnknown, false, false)
	if transport.requestCount() != 0 {
		t.Fatal("invalid service received a management request")
	}
}

func assertState(t *testing.T, state State, reason string, disabled bool, exhausted bool) {
	t.Helper()
	if state.Reason != reason || state.DisableEligibility != disabled || state.Exhausted != exhausted {
		t.Fatalf("state = %#v, want reason=%q disabled=%v exhausted=%v", state, reason, disabled, exhausted)
	}
}

type fixedSecretReader struct {
	value string
	err   error
}

func (reader fixedSecretReader) ReadSecret(context.Context, string) (string, bool, error) {
	return reader.value, reader.value != "", reader.err
}

type runtimeFixtureTransport struct {
	mu                   sync.Mutex
	payloads             map[string]string
	requests             map[string]int
	invalidAuthorization bool
}

func (transport *runtimeFixtureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.requests[request.URL.Hostname()]++
	if request.Header.Get("Authorization") != "Bearer management-secret" {
		transport.invalidAuthorization = true
	}
	payload, found := transport.payloads[request.URL.Hostname()]
	if !found {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":{"code":"unavailable"}}`)),
			Request:    request,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
		Request:    request,
	}, nil
}

func (transport *runtimeFixtureTransport) requestCount() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	total := 0
	for _, count := range transport.requests {
		total += count
	}
	return total
}
