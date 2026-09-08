package accountprojection

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
)

type readinessStore struct {
	account string
	key     controlplane.InternalKey
}

func (s readinessStore) ReadRoutes(context.Context) (map[string]string, error) {
	return map[string]string{"new@example.com": s.account}, nil
}
func (s readinessStore) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return []controlplane.Account{{ID: "disabled", GroupEnabled: false}, {ID: "alpha", GroupEnabled: true}, {ID: "beta", GroupEnabled: true}}, nil
}
func (s readinessStore) ReadInternalKey(context.Context, string) (controlplane.InternalKey, bool, error) {
	return s.key, true, nil
}

type readinessTransport func(*http.Request) (*http.Response, error)

func (f readinessTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUserKeyReadinessWaitsForNewCredentialAndRejectsFalseSuccess(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		body    string
		success bool
	}{
		{"reload", 200, `{"data":[]}`, true},
		{"stale", 401, `{"error":"Invalid API key"}`, false},
		{"invalid models", 200, `{"ok":true}`, false},
		{"redirect", 302, ``, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			probe := UserKeyReadiness{Store: readinessStore{"alpha", controlplane.InternalKey{Key: "new-private-key", Status: "active"}}, Timeout: 450 * time.Millisecond}
			probe.Client = &http.Client{Transport: readinessTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.String() != "http://cliproxy-alpha:8317/v1/models" || r.Header.Get("Authorization") != "Bearer new-private-key" {
					t.Fatal("probe did not use the assigned account and the new user's Key")
				}
				status, body := test.status, test.body
				if calls == 1 {
					status, body = 401, `{"error":"old config"}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Location": []string{"https://example.com/"}}, Request: r}, nil
			})}
			err := probe.VerifyUserKey(context.Background(), "NEW@example.com")
			if (err == nil) != test.success || calls < 2 {
				t.Fatalf("readiness = %v after %d attempts", err, calls)
			}
		})
	}
}

func TestUserKeyReadinessRejectsUnsafeRouteAndRedactsTransportErrors(t *testing.T) {
	probe := UserKeyReadiness{Store: readinessStore{"../outside", controlplane.InternalKey{Key: "sensitive", Status: "active"}}, Timeout: time.Millisecond}
	calls := 0
	probe.Client = &http.Client{Transport: readinessTransport(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("echo sensitive") })}
	if err := probe.VerifyUserKey(context.Background(), "new@example.com"); err == nil || calls != 0 {
		t.Fatal("unsafe route reached the transport")
	}
	probe.Store = readinessStore{"alpha", controlplane.InternalKey{Key: "sensitive", Status: "active"}}
	if err := probe.VerifyUserKey(context.Background(), "new@example.com"); err == nil || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("unsafe transport error: %v", err)
	}
}

func TestUserKeyReadinessAcceptsUnassignedUserOnAnEnabledAccount(t *testing.T) {
	probe := UserKeyReadiness{Store: readinessStore{"", controlplane.InternalKey{Key: "new-private-key", Status: "active"}}, Timeout: 2 * time.Second}
	var hosts []string
	probe.Client = &http.Client{Transport: readinessTransport(func(r *http.Request) (*http.Response, error) {
		hosts = append(hosts, r.URL.Hostname())
		if r.Header.Get("Authorization") != "Bearer new-private-key" {
			t.Fatal("probe did not use the new user's internal Key")
		}
		if r.URL.Hostname() == "cliproxy-alpha" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Header: http.Header{}}, nil
	})}
	if err := probe.VerifyUserKey(context.Background(), "new@example.com"); err != nil {
		t.Fatal(err)
	}
	if strings.Join(hosts, ",") != "cliproxy-alpha,cliproxy-beta" {
		t.Fatalf("unexpected readiness targets: %v", hosts)
	}
}
