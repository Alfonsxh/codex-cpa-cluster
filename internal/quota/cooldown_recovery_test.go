package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
)

func TestCooldownRecoveryClearsOnlyConfirmedOldPeriod(t *testing.T) {
	for _, naturalReset := range []bool{false, true} {
		t.Run(fmt.Sprint("natural=", naturalReset), func(t *testing.T) {
			f := newRecoveryFixture(t)
			if naturalReset {
				f.auth.Message = fmt.Sprintf(`{"error":{"type":"usage_limit_reached","resets_at":%d}}`, f.now.Unix()-60)
			}
			count, err := f.recovery.Reconcile(context.Background(), f.snapshot)
			if err != nil || count != 1 || f.posts != 1 || f.auth.Unavailable || f.fences != 1 {
				t.Fatalf("recovery = %d, %v; posts=%d auth=%#v fences=%d", count, err, f.posts, f.auth, f.fences)
			}
			count, err = f.recovery.Reconcile(context.Background(), f.snapshot)
			if err != nil || count != 0 || f.posts != 1 {
				t.Fatalf("repeated recovery = %d, %v; posts=%d", count, err, f.posts)
			}
			encoded, err := json.Marshal(f.snapshot)
			if err != nil || strings.Contains(string(encoded), "upstream-identity") {
				t.Fatal("quota serialization exposed recovery identity")
			}
		})
	}
}

func TestCooldownRecoveryPreservesUncertainOrCurrentBlockers(t *testing.T) {
	cases := map[string]func(*recoveryFixture){
		"same weekly period": func(f *recoveryFixture) { f.auth.UpdatedAt = f.now.Add(-30 * time.Second) },
		"same reset deadline": func(f *recoveryFixture) {
			f.auth.Message = fmt.Sprintf(`{"error":{"type":"usage_limit_reached","resets_at":%d}}`, *f.snapshot.Accounts[0].Weekly.ResetAt)
		},
		"missing error time":                   func(f *recoveryFixture) { f.auth.UpdatedAt = time.Time{} },
		"missing error deadline":               func(f *recoveryFixture) { f.auth.Message = `{"error":{"type":"usage_limit_reached"}}` },
		"invalid credential":                   func(f *recoveryFixture) { f.auth.Message = `{"error":{"type":"invalid_grant"}}` },
		"unstructured error":                   func(f *recoveryFixture) { f.auth.Message = "usage_limit_reached" },
		"native disabled":                      func(f *recoveryFixture) { f.auth.Disabled = true },
		"wrong provider":                       func(f *recoveryFixture) { f.auth.Provider = "claude" },
		"missing native identity":              func(f *recoveryFixture) { f.auth.IDToken.AccountID = "" },
		"changed native identity":              func(f *recoveryFixture) { f.auth.IDToken.AccountID = "other-identity" },
		"persisted quota lacks fetch identity": func(f *recoveryFixture) { f.snapshot.Accounts[0].oauthAccountID = "" },
		"short additional window blocked":      func(f *recoveryFixture) { f.snapshot.Accounts[0].recoveryAllowed = false },
		"multiple native identities":           func(f *recoveryFixture) { f.multiple = true },
		"account disabled":                     func(f *recoveryFixture) { f.accounts[0].GroupEnabled = false },
		"account removed":                      func(f *recoveryFixture) { f.accounts = nil },
		"unsafe account":                       func(f *recoveryFixture) { f.snapshot.Accounts[0].Account = "attacker.example.test" },
		"old snapshot":                         func(f *recoveryFixture) { f.snapshot.GeneratedAt -= 121 },
		"future snapshot":                      func(f *recoveryFixture) { f.snapshot.GeneratedAt++ },
		"cached snapshot":                      func(f *recoveryFixture) { f.snapshot.Cached = true },
		"refreshing snapshot":                  func(f *recoveryFixture) { f.snapshot.Refreshing = true },
		"official unavailable":                 func(f *recoveryFixture) { f.snapshot.Accounts[0].Status = "unavailable" },
		"official disallowed":                  func(f *recoveryFixture) { f.snapshot.Accounts[0].Allowed = boolPointer(false) },
		"official allowance unknown":           func(f *recoveryFixture) { f.snapshot.Accounts[0].Allowed = nil },
		"official exhausted":                   func(f *recoveryFixture) { f.snapshot.Accounts[0].LimitReached = boolPointer(true) },
		"official limit unknown":               func(f *recoveryFixture) { f.snapshot.Accounts[0].LimitReached = nil },
		"weekly unavailable":                   func(f *recoveryFixture) { f.snapshot.Accounts[0].Weekly = nil },
		"additional window selected":           func(f *recoveryFixture) { f.snapshot.Accounts[0].Weekly.Key = "additional:spark:secondary_window" },
		"short window":                         func(f *recoveryFixture) { f.snapshot.Accounts[0].Weekly.WindowSeconds = 5 * 3600 },
		"weekly exhausted":                     func(f *recoveryFixture) { f.snapshot.Accounts[0].Weekly.UsedPercent = 100 },
		"NaN remaining":                        func(f *recoveryFixture) { f.snapshot.Accounts[0].Weekly.RemainingPercent = math.NaN() },
		"infinite used":                        func(f *recoveryFixture) { f.snapshot.Accounts[0].Weekly.UsedPercent = math.Inf(1) },
		"future weekly start":                  func(f *recoveryFixture) { *f.snapshot.Accounts[0].Weekly.ResetAt += 120 },
		"expired weekly window":                func(f *recoveryFixture) { *f.snapshot.Accounts[0].Weekly.ResetAt = f.now.Unix() },
		"additional exhausted": func(f *recoveryFixture) {
			f.snapshot.Accounts[0].WeeklyWindows = []WeeklyWindow{{Key: "additional:spark:secondary_window", LimitReached: true}}
		},
		"catalog disabled under fence": func(f *recoveryFixture) {
			f.beforeFence = func() { f.accounts[0].GroupEnabled = false }
		},
		"native changed under fence": func(f *recoveryFixture) {
			f.beforeFence = func() { f.auth.Message = `{"error":{"type":"invalid_grant"}}` }
		},
		"snapshot expired under fence": func(f *recoveryFixture) {
			f.beforeFence = func() { f.now = f.now.Add(121 * time.Second) }
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			f := newRecoveryFixture(t)
			change(f)
			count, err := f.recovery.Reconcile(context.Background(), f.snapshot)
			if err != nil || count != 0 || f.posts != 0 {
				t.Fatalf("unsafe recovery = %d, %v; posts=%d", count, err, f.posts)
			}
		})
	}
}

func TestCooldownRecoveryFailsClosedAndSanitizesNativeErrors(t *testing.T) {
	for _, kind := range []string{"lease", "unsupported", "redirect", "invalid-json", "wrong-ack", "not-recovered"} {
		t.Run(kind, func(t *testing.T) {
			f := newRecoveryFixture(t)
			f.failure = kind
			if kind == "lease" {
				f.fenceError = controlplane.ErrLeaseLost
			}
			count, err := f.recovery.Reconcile(context.Background(), f.snapshot)
			if count != 0 || err == nil || strings.Contains(err.Error(), "private-upstream-body") {
				t.Fatalf("failure handling = %d, %v", count, err)
			}
			if kind == "lease" && (f.posts != 0 || !errors.Is(err, controlplane.ErrLeaseLost)) {
				t.Fatalf("lost ownership sent reset: posts=%d err=%v", f.posts, err)
			}
		})
	}
}

func TestRecoveryChecksShortAndAdditionalUpstreamLimits(t *testing.T) {
	for _, test := range []struct {
		payload string
		allowed bool
	}{
		{`{"rate_limit":{"allowed":true,"limit_reached":false}}`, true},
		{`{"rate_limit":{"allowed":true,"limit_reached":false},"additional_rate_limits":[{"rate_limit":{"allowed":true,"limit_reached":false}}]}`, true},
		{`{"rate_limit":{"allowed":true,"limit_reached":false},"additional_rate_limits":[{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"limit_window_seconds":18000}}}]}`, false},
		{`{"rate_limit":{"allowed":true,"limit_reached":false},"additional_rate_limits":[{"rate_limit":{}}]}`, false},
		{`{"rate_limit":{"allowed":true,"limit_reached":false},"additional_rate_limits":{}}`, false},
		{`{"rate_limit":{"allowed":true,"limit_reached":true}}`, false},
		{`{"rate_limit":{"allowed":false,"limit_reached":false}}`, false},
		{`{}`, false},
	} {
		var payload map[string]any
		if err := json.Unmarshal([]byte(test.payload), &payload); err != nil {
			t.Fatal(err)
		}
		if got := allRateLimitsAllowRecovery(payload); got != test.allowed {
			t.Fatalf("recovery allowance = %v, want %v for %s", got, test.allowed, test.payload)
		}
	}
}

type recoveryFixture struct {
	t           *testing.T
	recovery    *CooldownRecovery
	now         time.Time
	snapshot    Snapshot
	accounts    []controlplane.Account
	auth        cooldownAuth
	multiple    bool
	posts       int
	fences      int
	fenceHeld   bool
	fenceError  error
	beforeFence func()
	failure     string
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
	t.Helper()
	now := time.Unix(2_000_000, 0)
	resetAt := now.Unix() - 60 + WeeklyWindowSeconds
	f := &recoveryFixture{
		t: t, now: now, accounts: []controlplane.Account{{ID: "alpha", GroupEnabled: true}},
		snapshot: Snapshot{GeneratedAt: now.Unix(), Accounts: []AccountQuota{{
			Account: "alpha", oauthAccountID: "upstream-identity", recoveryAllowed: true, Status: "ok", Allowed: boolPointer(true), LimitReached: boolPointer(false),
			Weekly: &WeeklyWindow{Key: "default:primary_window", RemainingPercent: 100, ResetAt: &resetAt, WindowSeconds: WeeklyWindowSeconds},
		}}},
		auth: cooldownAuth{
			Index: "credential-index", Provider: "codex", Status: "error", Unavailable: true,
			UpdatedAt: now.Add(-120 * time.Second), NextRetryAfter: now.Add(48 * time.Hour),
			Message: fmt.Sprintf(`{"error":{"type":"usage_limit_reached","resets_at":%d}}`, now.Unix()+2*86400),
		},
	}
	f.auth.IDToken.AccountID = "upstream-identity"
	f.recovery = NewCooldownRecovery(f)
	f.recovery.now = func() time.Time { return f.now }
	f.recovery.http.Transport = f
	return f
}

func boolPointer(value bool) *bool { return &value }

func (f *recoveryFixture) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return append([]controlplane.Account(nil), f.accounts...), nil
}

func (f *recoveryFixture) ReadSecret(context.Context, string) (string, bool, error) {
	return "management-test-key", true, nil
}

func (f *recoveryFixture) WithWriteFence(_ context.Context, operation func() error) error {
	f.fences++
	if f.fenceError != nil {
		return f.fenceError
	}
	if f.beforeFence != nil {
		f.beforeFence()
	}
	f.fenceHeld = true
	defer func() { f.fenceHeld = false }()
	return operation()
}

func (f *recoveryFixture) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme != "http" || request.URL.Host != "cliproxy-alpha:8317" ||
		request.Header.Get("Authorization") != "Bearer management-test-key" {
		f.t.Fatalf("unexpected native destination or credential: %s", request.URL)
	}
	status := http.StatusOK
	var payload any
	switch request.URL.Path {
	case "/v0/management/auth-files":
		if request.Method != http.MethodGet {
			f.t.Fatal("auth-files request mutated native state")
		}
		files := []cooldownAuth{f.auth}
		if f.multiple {
			files = append(files, f.auth)
		}
		payload = map[string]any{"files": files}
	case "/v0/management/reset-quota":
		if request.Method != http.MethodPost || !f.fenceHeld {
			f.t.Fatal("reset without writer fence")
		}
		var body map[string]string
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body) != 1 || body["auth_index"] != f.auth.Index {
			f.t.Fatal("reset did not target exactly the observed credential")
		}
		f.posts++
		payload = map[string]any{"status": "ok", "auth_index": f.auth.Index}
		switch f.failure {
		case "unsupported":
			status, payload = http.StatusNotFound, "private-upstream-body"
		case "redirect":
			return &http.Response{StatusCode: http.StatusTemporaryRedirect, Header: http.Header{"Location": []string{"http://attacker.example.test/"}}, Body: io.NopCloser(strings.NewReader("private-upstream-body"))}, nil
		case "invalid-json":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("private-upstream-body"))}, nil
		case "wrong-ack":
			payload = map[string]string{"status": "ok", "auth_index": "other"}
		case "not-recovered":
		default:
			f.auth.Status, f.auth.Unavailable, f.auth.Message = "active", false, ""
			f.auth.NextRetryAfter = time.Time{}
		}
	default:
		f.t.Fatalf("unexpected native path %s", request.URL.Path)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatal(err)
	}
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
}
