package quota

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/google/uuid"
)

func TestOAuthLoaderUsesLatestSafeEnabledCodexRecord(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "auth", "alpha")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create auth directory: %v", err)
	}
	writeOAuth(t, filepath.Join(directory, "old.json"), map[string]any{
		"type": "codex", "access_token": "old-token", "account_id": "old-account",
	}, time.Unix(100, 0))
	writeOAuth(t, filepath.Join(directory, "new.json"), map[string]any{
		"type": "codex", "access_token": "new-token", "account_id": 12345,
	}, time.Unix(200, 0))
	writeOAuth(t, filepath.Join(directory, "disabled.json"), map[string]any{
		"type": "codex", "access_token": "disabled-token", "disabled": true,
	}, time.Unix(300, 0))
	outside := filepath.Join(root, "outside.json")
	writeOAuth(t, outside, map[string]any{
		"type": "codex", "access_token": "symlink-token",
	}, time.Unix(400, 0))
	if err := os.Symlink(outside, filepath.Join(directory, "unsafe.json")); err != nil {
		t.Fatalf("create OAuth symlink: %v", err)
	}

	record, err := (OAuthLoader{Root: root}).Load("alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if record.AccessToken != "new-token" || record.AccountID != "12345" {
		t.Fatalf("record = %#v", record)
	}
	if _, err := (OAuthLoader{Root: root}).Load("../outside"); !errors.Is(err, ErrOAuthMissing) {
		t.Fatalf("unsafe account error = %v", err)
	}
}

func TestOAuthLoaderRejectsTrailingJSONAndSymlinkDirectory(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "auth", "alpha")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("create auth directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(directory, "broken.json"), []byte(`{"type":"codex","access_token":"secret"} {}`), 0o600); err != nil {
		t.Fatalf("write trailing JSON: %v", err)
	}
	if _, err := (OAuthLoader{Root: root}).Load("alpha"); !errors.Is(err, ErrOAuthMissing) {
		t.Fatalf("trailing JSON error = %v", err)
	}
	if err := os.RemoveAll(directory); err != nil {
		t.Fatalf("remove account auth directory: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, directory); err != nil {
		t.Fatalf("create auth directory symlink: %v", err)
	}
	if _, err := (OAuthLoader{Root: root}).Load("alpha"); !errors.Is(err, ErrOAuthMissing) {
		t.Fatalf("symlink directory error = %v", err)
	}
}

func TestProxyResolverUsesControlPlanePolicyWithoutLeakingCredentials(t *testing.T) {
	store := &fakeProxyStore{values: map[string]string{
		defaultProxySecret:                 "http://default-user:default-pass@proxy.example.com:8080",
		accountProxySecretPrefix + "alpha": "socks5://account-user:account-pass@proxy.example.com:1080",
	}}
	resolver := ProxyResolver{Store: store, Settings: map[string]any{"cpa.proxy_enabled": true}}
	custom, err := resolver.Resolve(context.Background(), controlplane.Account{ID: "alpha", ProxyMode: "custom"})
	if err != nil || !strings.HasPrefix(custom, "socks5://") {
		t.Fatalf("custom proxy = (%q, %v)", custom, err)
	}
	inherited, err := resolver.Resolve(context.Background(), controlplane.Account{ID: "beta", ProxyMode: "inherit"})
	if err != nil || !strings.HasPrefix(inherited, "http://") {
		t.Fatalf("inherited proxy = (%q, %v)", inherited, err)
	}
	direct, err := resolver.Resolve(context.Background(), controlplane.Account{ID: "gamma", ProxyMode: "direct"})
	if err != nil || direct != "" {
		t.Fatalf("direct proxy = (%q, %v)", direct, err)
	}
	delete(store.values, accountProxySecretPrefix+"alpha")
	_, err = resolver.Resolve(context.Background(), controlplane.Account{ID: "alpha", ProxyMode: "custom"})
	if err == nil || strings.Contains(err.Error(), "account-pass") || strings.Contains(err.Error(), "account-user") {
		t.Fatalf("unsafe proxy error = %v", err)
	}
}

func TestClientUsesRequiredHeadersDoesNotRetryOrFollowRedirect(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path == "/redirect" {
			http.Redirect(writer, request, "/usage", http.StatusFound)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-access-token" ||
			request.Header.Get("ChatGPT-Account-Id") != "account-123" ||
			request.Header.Get("Originator") != "Codex Desktop" {
			http.Error(writer, "missing headers", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"rate_limit":{}}`))
	}))
	defer server.Close()
	client := Client{Endpoint: server.URL + "/usage", Timeout: time.Second}
	payload, err := client.Fetch(context.Background(), OAuthRecord{
		AccessToken: "test-access-token", AccountID: "account-123",
	}, "")
	if err != nil || object(payload["rate_limit"]) == nil {
		t.Fatalf("Fetch = (%#v, %v)", payload, err)
	}
	client.Endpoint = server.URL + "/redirect"
	if _, err := client.Fetch(context.Background(), OAuthRecord{AccessToken: "test-access-token"}, ""); err == nil {
		t.Fatal("Fetch followed a redirect")
	}
	if requests.Load() != 2 {
		t.Fatalf("request count = %d", requests.Load())
	}
}

func TestClientFetchesResetCreditsAndConsumesOneCreditWithoutRetry(t *testing.T) {
	var creditRequests atomic.Int64
	var consumeRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-access-token" ||
			request.Header.Get("ChatGPT-Account-Id") != "account-123" ||
			request.Header.Get("Originator") != "Codex Desktop" {
			http.Error(writer, "missing headers", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/credits":
			creditRequests.Add(1)
			if request.Method != http.MethodGet {
				http.Error(writer, "wrong method", http.StatusMethodNotAllowed)
				return
			}
			_, _ = writer.Write([]byte(`{"available_count":1,"credits":[{"id":"credit-1","status":"available"}]}`))
		case "/consume":
			consumeRequests.Add(1)
			if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
				http.Error(writer, "wrong request", http.StatusBadRequest)
				return
			}
			var body struct {
				RedeemRequestID string `json:"redeem_request_id"`
				CreditID        string `json:"credit_id"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.CreditID != "credit-1" {
				http.Error(writer, "bad payload", http.StatusBadRequest)
				return
			}
			if _, err := uuid.Parse(body.RedeemRequestID); err != nil {
				http.Error(writer, "bad request id", http.StatusBadRequest)
				return
			}
			_, _ = writer.Write([]byte(`{"code":"consumed","windows_reset":1}`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := Client{
		ResetCreditsEndpoint: server.URL + "/credits",
		ResetEndpoint:        server.URL + "/consume",
		Timeout:              time.Second,
	}
	auth := OAuthRecord{AccessToken: "test-access-token", AccountID: "account-123"}
	credits, err := client.FetchResetCredits(context.Background(), auth, "")
	if err != nil || object(credits["credits"]) != nil || len(list(credits["credits"])) != 1 {
		t.Fatalf("FetchResetCredits = (%#v, %v)", credits, err)
	}
	consumed, err := client.ConsumeResetCredit(context.Background(), auth, "", "credit-1")
	if err != nil || stringValue(consumed["code"]) != "consumed" {
		t.Fatalf("ConsumeResetCredit = (%#v, %v)", consumed, err)
	}
	if creditRequests.Load() != 1 || consumeRequests.Load() != 1 {
		t.Fatalf("reset request counts = (%d, %d)", creditRequests.Load(), consumeRequests.Load())
	}
}

func TestClientMapsAuthFailureAndRejectsTrailingOrOversizedPayload(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{name: "auth", want: ErrAuthExpired, handler: func(writer http.ResponseWriter, _ *http.Request) {
			http.Error(writer, "secret body", http.StatusUnauthorized)
		}},
		{name: "trailing", handler: func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{} {}`))
		}},
		{name: "oversized", handler: func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(`{"padding":"` + strings.Repeat("x", maximumResponseBodyBytes) + `"}`))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(test.handler)
			defer server.Close()
			_, err := (Client{Endpoint: server.URL, Timeout: time.Second}).Fetch(
				context.Background(), OAuthRecord{AccessToken: "test-token"}, "",
			)
			if err == nil || test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Fetch error = %v, want %v", err, test.want)
			}
			if strings.Contains(err.Error(), "secret body") || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("Fetch leaked secret in error: %v", err)
			}
		})
	}
}

func TestNormalizeMatchesDefaultAndAdditionalWeeklyContract(t *testing.T) {
	payload := decodeObject(t, `{
      "plan_type":"team",
      "rate_limit":{
        "allowed":true,
        "limit_reached":true,
        "primary_window":{"limit_window_seconds":604800,"used_percent":64.126,"reset_at":2000}
      },
      "additional_rate_limits":[{
        "limit_name":"Codex Models",
        "metered_feature":"codex_models",
        "rate_limit":{
          "limit_reached":false,
          "primary_window":{"limit_window_seconds":604800,"used_percent":20,"reset_after_seconds":90}
        }
      }],
      "rate_limit_reached_type":{"details":"default"},
      "rate_limit_reset_credits":{"available_count":3,"applicable_available_count":1}
    }`)
	result := Normalize("alpha", payload)
	if result.Status != "ok" || result.PlanType == nil || *result.PlanType != "team" ||
		result.Allowed == nil || !*result.Allowed || result.LimitReached == nil || !*result.LimitReached {
		t.Fatalf("normalized quota = %#v", result)
	}
	if result.ResetCreditCount == nil || *result.ResetCreditCount != 3 {
		t.Fatalf("reset credit count = %#v", result.ResetCreditCount)
	}
	if len(result.WeeklyWindows) != 2 || result.Weekly == nil ||
		result.Weekly.Key != "default:primary_window" || result.Weekly.UsedPercent != 100 ||
		result.Weekly.ReportedUsedPercent != 64.13 || result.Weekly.RemainingPercent != 0 ||
		!result.Weekly.Resettable || result.Weekly.ResetAt == nil || *result.Weekly.ResetAt != 2000 {
		t.Fatalf("default weekly = %#v", result.Weekly)
	}
	additional := result.WeeklyWindows[1]
	if additional.Key != "additional:codex_models:primary_window" || additional.Label != "Codex Models" ||
		additional.MeteredFeature == nil || *additional.MeteredFeature != "codex_models" ||
		additional.ResetAfterSeconds == nil || *additional.ResetAfterSeconds != 90 || additional.Resettable {
		t.Fatalf("additional weekly = %#v", additional)
	}
}

func TestRefresherPublishesOrderedSecretFreeSnapshot(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := controlplane.Open(ctx, root, controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	defer store.Close()
	accounts := []controlplane.Account{
		{ID: "alpha", Email: "alpha@example.com", Port: 18319, ProxyMode: "direct", GroupEnabled: true},
		{ID: "beta", Email: "beta@example.com", Port: 18320, ProxyMode: "direct", GroupEnabled: true},
	}
	if err := store.WriteAccounts(ctx, accounts); err != nil {
		t.Fatalf("WriteAccounts: %v", err)
	}
	if err := store.WriteSettings(ctx, map[string]any{
		"usage.quota_cache_seconds": 60, "usage.upstream_timeout_seconds": 5,
	}); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	authDirectory := filepath.Join(root, "auth", "alpha")
	if err := os.MkdirAll(authDirectory, 0o700); err != nil {
		t.Fatalf("create auth directory: %v", err)
	}
	writeOAuth(t, filepath.Join(authDirectory, "codex.json"), map[string]any{
		"type": "codex", "access_token": "never-persist-this-token", "account_id": "official-alpha",
	}, time.Now())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer never-persist-this-token" {
			http.Error(writer, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = writer.Write([]byte(`{
          "rate_limit":{"allowed":true,"limit_reached":false,
          "primary_window":{"limit_window_seconds":604800,"used_percent":25,"reset_at":2000}}
        }`))
	}))
	defer server.Close()
	refresher := Refresher{
		Root: root, Store: store, Endpoint: server.URL,
		Now: func() time.Time { return time.Unix(1000, 0) }, MaxConcurrency: 2,
	}
	snapshot, err := refresher.RunOnce(ctx)
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(snapshot.Accounts) != 2 || snapshot.Accounts[0].Account != "alpha" || snapshot.Accounts[0].Status != "ok" ||
		snapshot.Accounts[1].Account != "beta" || snapshot.Accounts[1].Status != "auth_missing" {
		t.Fatalf("snapshot accounts = %#v", snapshot.Accounts)
	}
	var state RuntimeState
	found, err := store.ReadRuntimeState(ctx, RuntimeStateName, &state)
	if err != nil || !found || state.Version != runtimeStateVersion || state.LastSuccessAt != 1000 {
		t.Fatalf("runtime state = (%#v, %v, %v)", state, found, err)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal runtime state: %v", err)
	}
	for _, secret := range []string{"never-persist-this-token", "official-alpha"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("runtime state leaked secret %q: %s", secret, raw)
		}
	}
	if !reflect.DeepEqual(snapshot.Accounts, state.Snapshot.Accounts) {
		t.Fatalf("stored snapshot differs: %#v != %#v", state.Snapshot.Accounts, snapshot.Accounts)
	}
}

func TestRefreshRequestIsSingleFlightAndHonorsLegacyMinimumAge(t *testing.T) {
	ctx := context.Background()
	store, err := controlplane.Open(ctx, t.TempDir(), controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	defer store.Close()

	requestedAt := time.Unix(100, 0)
	request, requested, err := RequestRefresh(ctx, store, requestedAt)
	if err != nil || !requested || !request.Pending() || request.RequestedAt != 100 {
		t.Fatalf("first refresh request = (%#v, %v, %v)", request, requested, err)
	}
	duplicate, requested, err := RequestRefresh(ctx, store, time.Unix(101, 0))
	if err != nil || requested || duplicate.RequestID != request.RequestID || !duplicate.Pending() {
		t.Fatalf("single-flight refresh request = (%#v, %v, %v)", duplicate, requested, err)
	}
	if err := MarkRefreshStarted(ctx, store, request.RequestID, time.Unix(102, 0)); err != nil {
		t.Fatalf("mark refresh started: %v", err)
	}
	if err := MarkRefreshCompleted(ctx, store, request.RequestID, time.Unix(103, 0), nil); err != nil {
		t.Fatalf("mark refresh completed: %v", err)
	}
	completed, found, err := ReadRefreshRequest(ctx, store)
	if err != nil || !found || completed.Pending() || completed.StartedID != request.RequestID ||
		completed.CompletedID != request.RequestID || completed.CompletedAt != 103 || completed.LastError != "" {
		t.Fatalf("completed refresh request = (%#v, %v, %v)", completed, found, err)
	}

	if err := store.WriteRuntimeState(ctx, RuntimeStateName, RuntimeState{
		Version:  runtimeStateVersion,
		Snapshot: Snapshot{GeneratedAt: 100, CacheTTLSeconds: 60, Accounts: []AccountQuota{}},
	}); err != nil {
		t.Fatalf("write current quota snapshot: %v", err)
	}
	throttled, requested, err := RequestRefresh(ctx, store, time.Unix(114, 0))
	if err != nil || requested || throttled.RequestID != request.RequestID {
		t.Fatalf("throttled refresh request = (%#v, %v, %v)", throttled, requested, err)
	}
	aged, requested, err := RequestRefresh(ctx, store, time.Unix(115, 0))
	if err != nil || !requested || !aged.Pending() || aged.RequestID == request.RequestID {
		t.Fatalf("aged refresh request = (%#v, %v, %v)", aged, requested, err)
	}
}

func TestRefreshRequestFailureIsBoundedAndRecoverable(t *testing.T) {
	ctx := context.Background()
	store, err := controlplane.Open(ctx, t.TempDir(), controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	defer store.Close()
	request, requested, err := RequestRefresh(ctx, store, time.Unix(100, 0))
	if err != nil || !requested {
		t.Fatalf("request refresh = (%#v, %v, %v)", request, requested, err)
	}
	runError := errors.New(strings.Repeat("quota failed ", 100))
	if err := MarkRefreshCompleted(ctx, store, request.RequestID, time.Unix(101, 0), runError); err != nil {
		t.Fatalf("mark failed refresh completed: %v", err)
	}
	failed, _, err := ReadRefreshRequest(ctx, store)
	if err != nil || failed.Pending() || len([]rune(failed.LastError)) != 500 {
		t.Fatalf("failed refresh state = (%#v, %v)", failed, err)
	}
	retry, requested, err := RequestRefresh(ctx, store, time.Unix(102, 0))
	if err != nil || !requested || retry.RequestID == request.RequestID || retry.LastError != "" {
		t.Fatalf("retry refresh request = (%#v, %v, %v)", retry, requested, err)
	}
}

type fakeProxyStore struct {
	values map[string]string
}

func (store *fakeProxyStore) ReadSecret(_ context.Context, name string) (string, bool, error) {
	value, found := store.values[name]
	return value, found, nil
}

func writeOAuth(t *testing.T, path string, payload any, modified time.Time) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal OAuth fixture: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write OAuth fixture: %v", err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatalf("set OAuth fixture time: %v", err)
	}
}

func decodeObject(t *testing.T, raw string) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	return payload
}
