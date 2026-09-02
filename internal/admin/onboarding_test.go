package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestOnboardingStatusGuidesFreshTargetWithoutLeakingSecrets(t *testing.T) {
	server, _ := newOnboardingTestServer(t, Config{})
	response := performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/onboarding",
		nil,
		map[string]string{"X-Management-Key": "test-management-key"},
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("fresh onboarding = %d %s", response.Code, response.Body.String())
	}
	var payload onboardingStatusResponse
	decodeAdminResponse(t, response, &payload)
	if payload.Version != onboardingVersion || payload.RequiredComplete ||
		payload.Required != (onboardingRequiredProgress{Complete: 0, Total: 2}) ||
		payload.Recommended != (onboardingRecommendedProgress{Complete: 0, Skipped: 0, Total: 6}) {
		t.Fatalf("fresh onboarding payload = %#v", payload)
	}
	assertOnboardingStep(t, payload, "email_domains", onboardingIncompleteStatus)
	assertOnboardingStep(t, payload, "initial_password", onboardingIncompleteStatus)
	for _, removed := range []string{"first_account", "account_authorization", "first_user"} {
		assertOnboardingStepMissing(t, payload, removed)
	}
	if len(payload.Steps) != 8 {
		t.Fatalf("fresh onboarding steps = %d, want 8", len(payload.Steps))
	}
	for _, forbidden := range []string{"test-management-key", "portal_initial_password", "cpa_management_key", "wecom_webhook"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("fresh onboarding leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestOnboardingStatusCompletesFromDurableSettings(t *testing.T) {
	server, store := newOnboardingTestServer(t, Config{})
	ctx := context.Background()
	if err := store.WriteSettings(ctx, map[string]any{
		"identity.allowed_email_domains":   []string{"example.com"},
		"branding.public_base_url":         "https://cpa.example.com",
		"branding.product_name":            "Example CPA",
		"user_quota.timezone":              "Asia/Shanghai",
		"user_quota.default_weekly_tokens": 20_000_000,
		"cpa.proxy_enabled":                true,
	}); err != nil {
		t.Fatalf("write onboarding settings: %v", err)
	}
	for name, value := range map[string]string{
		portalInitialPasswordSecret: "one-time-user-password",
		"wecom_webhook":             "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=test-onboarding-placeholder",
		defaultProxySecretName:      "socks5://proxy.example.com:1080",
	} {
		if err := store.WriteSecret(ctx, name, value); err != nil {
			t.Fatalf("write onboarding secret %s: %v", name, err)
		}
	}
	response := performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/onboarding",
		nil,
		map[string]string{"X-Management-Key": "test-management-key"},
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("complete onboarding = %d %s", response.Code, response.Body.String())
	}
	var payload onboardingStatusResponse
	decodeAdminResponse(t, response, &payload)
	if !payload.RequiredComplete || payload.Required.Complete != 2 || payload.Required.Total != 2 ||
		payload.Recommended.Complete != 6 ||
		payload.Recommended.Skipped != 0 {
		t.Fatalf("complete onboarding payload = %#v", payload)
	}
	for _, step := range payload.Steps {
		if step.Status != onboardingCompleteStatus {
			t.Fatalf("complete onboarding step = %#v", step)
		}
	}
	for _, forbidden := range []string{"one-time-user-password", "qyapi.weixin.qq.com", "proxy.example.com"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("complete onboarding leaked %q: %s", forbidden, response.Body.String())
		}
	}
}

func TestOnboardingPreferencesPersistOnlyRecommendedSkips(t *testing.T) {
	server, store := newOnboardingTestServer(t, Config{})
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(server, http.MethodPut, "/admin/api/onboarding/preferences", map[string]any{
		"confirm":             "save",
		"skipped_recommended": []string{"proxy", "public_base_url", "proxy"},
	}, headers, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("update onboarding preferences = %d %s", response.Code, response.Body.String())
	}
	var payload onboardingStatusResponse
	decodeAdminResponse(t, response, &payload)
	if payload.Recommended.Skipped != 2 ||
		strings.Join(payload.SkippedRecommended, ",") != "proxy,public_base_url" {
		t.Fatalf("updated onboarding preferences = %#v", payload)
	}
	assertOnboardingStep(t, payload, "proxy", onboardingSkippedStatus)
	assertOnboardingStep(t, payload, "public_base_url", onboardingSkippedStatus)

	settings, err := store.ReadSettings(context.Background())
	if err != nil {
		t.Fatalf("read onboarding preference settings: %v", err)
	}
	if _, found := settings["onboarding.deferred_version"]; found {
		t.Fatalf("removed deferred setting was written: %#v", settings["onboarding.deferred_version"])
	}

	invalid := performAdminRequest(server, http.MethodPut, "/admin/api/onboarding/preferences", map[string]any{
		"confirm":             "save",
		"skipped_recommended": []string{"initial_password"},
	}, headers, nil)
	assertAdminError(t, invalid, http.StatusBadRequest, "invalid_request")
}

func TestOnboardingStatusDoesNotDependOnAccountLifecycleState(t *testing.T) {
	server, store := newOnboardingTestServer(t, Config{})
	if err := store.WriteAccounts(context.Background(), []controlplane.Account{{
		ID: "alpha", Email: "alpha@example.com", Port: 18318, GroupEnabled: true, DefaultGroup: true,
	}}); err != nil {
		t.Fatalf("write account state: %v", err)
	}
	if err := store.WriteKeyRecords(context.Background(), []controlplane.KeyRecord{{
		Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com",
		User: "alice@example.com", Status: "active", Key: "must-not-leak-user-key", CreatedAt: 100, UpdatedAt: 100,
	}}); err != nil {
		t.Fatalf("write user state: %v", err)
	}
	response := performAdminRequest(
		server,
		http.MethodGet,
		"/admin/api/onboarding",
		nil,
		map[string]string{"X-Management-Key": "test-management-key"},
		nil,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("account-independent onboarding = %d %s", response.Code, response.Body.String())
	}
	var payload onboardingStatusResponse
	decodeAdminResponse(t, response, &payload)
	if payload.Required.Total != 2 || len(payload.Steps) != 8 {
		t.Fatalf("account-independent onboarding payload = %#v", payload)
	}
	for _, removed := range []string{"first_account", "account_authorization", "first_user"} {
		assertOnboardingStepMissing(t, payload, removed)
	}
	if strings.Contains(response.Body.String(), "must-not-leak-user-key") {
		t.Fatalf("account-independent onboarding leaked user state: %s", response.Body.String())
	}
}

func newOnboardingTestServer(t *testing.T, config Config) (*Server, *controlplane.Store) {
	t.Helper()
	store, err := controlplane.Open(context.Background(), t.TempDir(), controlplane.Options{
		Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("open onboarding control-plane store: %v", err)
	}
	if err := store.WriteSecret(context.Background(), "cpa_management_key", "test-management-key"); err != nil {
		_ = store.Close()
		t.Fatalf("write onboarding management key: %v", err)
	}
	config.Store = store
	config.Now = func() time.Time { return time.Unix(1_800_000_000, 0) }
	server, err := New(config)
	if err != nil {
		_ = store.Close()
		t.Fatalf("new onboarding admin: %v", err)
	}
	t.Cleanup(func() {
		server.Close()
		_ = store.Close()
	})
	return server, store
}

func assertOnboardingStep(t *testing.T, payload onboardingStatusResponse, id string, status string) {
	t.Helper()
	for _, step := range payload.Steps {
		if step.ID == id {
			if step.Status != status {
				t.Fatalf("onboarding step %s status = %s, want %s", id, step.Status, status)
			}
			return
		}
	}
	t.Fatalf("onboarding step %s is missing", id)
}

func assertOnboardingStepMissing(t *testing.T, payload onboardingStatusResponse, id string) {
	t.Helper()
	for _, step := range payload.Steps {
		if step.ID == id {
			t.Fatalf("onboarding step %s should not be present: %#v", id, step)
		}
	}
}
