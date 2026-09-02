package portal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/identity"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/gin-gonic/gin"
)

func TestPortalLoginSetsClockBoundHttpOnlyCookieWithoutLeakingKey(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.server.sessionTTL = time.Hour

	response := fixture.request(
		http.MethodPost,
		"/usage/session",
		`{"email":"Alice@Example.com","password":"initial-password"}`,
		"",
	)
	if response.Code != http.StatusCreated {
		t.Fatalf("login status/body = %d %s", response.Code, response.Body.String())
	}
	cookie := response.Header().Get("Set-Cookie")
	for _, fragment := range []string{
		"cpa_user_session=created-token-1", "Path=/usage", "Max-Age=3600", "HttpOnly", "SameSite=Lax",
	} {
		if !strings.Contains(cookie, fragment) {
			t.Fatalf("login cookie %q does not contain %q", cookie, fragment)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" || strings.Contains(response.Body.String(), "old-key") {
		t.Fatalf("login response headers/body = %#v %s", response.Header(), response.Body.String())
	}
	var payload struct {
		Authenticated bool `json:"authenticated"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || !payload.Authenticated {
		t.Fatalf("login response contract = (%#v, %v)", payload, err)
	}
}

func TestPortalLoginRateLimitsOneAccountAcrossClientAddresses(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.server.loginLimiter = &LoginLimiter{
		entries: make(map[string]*loginLimitEntry), now: fixture.server.now,
		burst: 1, refill: time.Hour,
	}

	first := fixture.requestFrom(
		http.MethodPost,
		"/usage/session",
		`{"email":"alice@example.com","password":"wrong-password"}`,
		"",
		"192.0.2.10:1000",
	)
	if first.Code != http.StatusUnauthorized {
		t.Fatalf("first failed login = %d %s", first.Code, first.Body.String())
	}
	second := fixture.requestFrom(
		http.MethodPost,
		"/usage/session",
		`{"email":"alice@example.com","password":"wrong-password"}`,
		"",
		"192.0.2.11:1000",
	)
	if second.Code != http.StatusTooManyRequests || second.Header().Get("Retry-After") == "" {
		t.Fatalf("shared-account rate limit = %d %#v %s", second.Code, second.Header(), second.Body.String())
	}
}

func TestPortalRequiresInitialPasswordChangeAndKeepsOnlyCurrentSession(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["current-session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	fixture.sessions.sessions["other-session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	credential := fixture.sessions.credentials["alice@example.com"]
	credential.MustChange = true
	fixture.sessions.credentials["alice@example.com"] = credential

	blocked := fixture.request(http.MethodGet, "/usage/me/profile", "", "current-session")
	assertPortalError(t, blocked, http.StatusForbidden, "password_change_required")
	blockedKey := fixture.request(http.MethodGet, "/usage/me/key", "", "current-session")
	assertPortalError(t, blockedKey, http.StatusForbidden, "password_change_required")

	changed := fixture.request(
		http.MethodPut,
		"/usage/me/password",
		`{"current_password":"initial-password","new_password":"replacement-password"}`,
		"current-session",
	)
	if changed.Code != http.StatusOK {
		t.Fatalf("password change = %d %s", changed.Code, changed.Body.String())
	}
	updated := fixture.sessions.credentials["alice@example.com"]
	if updated.MustChange || !VerifyPassword("replacement-password", updated.PasswordHash) {
		t.Fatalf("updated credential = %#v", updated)
	}
	if _, found := fixture.sessions.sessions["other-session"]; found {
		t.Fatal("password change retained another session")
	}
	if _, found := fixture.sessions.sessions["current-session"]; !found || fixture.sessions.lastKept != "current-session" {
		t.Fatalf("password change did not retain current session: %#v", fixture.sessions.sessions)
	}

	profile := fixture.request(http.MethodGet, "/usage/me/profile", "", "current-session")
	if profile.Code != http.StatusOK || strings.Contains(profile.Body.String(), "old-key") ||
		strings.Contains(profile.Body.String(), "bob-key") || !strings.Contains(profile.Body.String(), "alice@example.com") {
		t.Fatalf("profile = %d %s", profile.Code, profile.Body.String())
	}
	key := fixture.request(http.MethodGet, "/usage/me/key", "", "current-session")
	if key.Code != http.StatusOK || !strings.Contains(key.Body.String(), "old-key") ||
		strings.Contains(key.Body.String(), "bob-key") || key.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("key reveal = %d %#v %s", key.Code, key.Header(), key.Body.String())
	}
}

func TestPortalRevokesSessionWhenUserIsDeletedOrDisabled(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["deleted-session"] = usage.PortalSession{User: "deleted@example.com", ExpiresAt: 11_000}
	fixture.sessions.credentials["deleted@example.com"] = fixture.sessions.credentials["alice@example.com"]

	response := fixture.request(http.MethodGet, "/usage/me/profile", "", "deleted-session")
	assertPortalError(t, response, http.StatusUnauthorized, "session_required")
	if _, found := fixture.sessions.sessions["deleted-session"]; found || fixture.sessions.lastRevoked != "deleted-session" {
		t.Fatalf("deleted user session was not revoked: %#v", fixture.sessions)
	}
}

func TestPortalUsageReadsAreUserScopedAndBoundedToOneGeneratedWindow(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	fixture.identity.accounts = append(fixture.identity.accounts,
		controlplane.Account{ID: "private", Email: "private@example.test", GroupEnabled: true},
	)
	fixture.usage.accountsResult = usage.UserAccountSummary{
		Totals: usage.WeightedMetrics{RawMetrics: usage.RawMetrics{TotalTokens: 12}, WeightedTokens: 18},
		Accounts: []usage.UserAccountUsage{{
			Account: "alpha",
			WeightedMetrics: usage.WeightedMetrics{
				RawMetrics: usage.RawMetrics{RequestCount: 1, TotalTokens: 12}, WeightedTokens: 18,
			},
		}},
	}

	response := fixture.request(http.MethodGet, "/usage/me/accounts?window=3600", "", "session")
	if response.Code != http.StatusOK {
		t.Fatalf("account usage = %d %s", response.Code, response.Body.String())
	}
	if fixture.usage.accountsCalls != 1 || fixture.usage.user != "alice@example.com" ||
		fixture.usage.startAt != 6_400 || fixture.usage.endAt == nil || *fixture.usage.endAt != 10_000 {
		t.Fatalf("bounded usage call = %#v", fixture.usage)
	}
	if strings.Contains(response.Body.String(), "bob@example.com") || strings.Contains(response.Body.String(), "bob-key") ||
		strings.Contains(response.Body.String(), "private@example.test") {
		t.Fatalf("account response leaked another user: %s", response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"email":"alpha@example.test"`) ||
		!strings.Contains(response.Body.String(), `"display_name":"alpha@example.test"`) ||
		strings.Contains(response.Body.String(), `"display_name":"CPA `) {
		t.Fatalf("account response does not expose the entitled CPA email: %s", response.Body.String())
	}

	breakdown := fixture.request(http.MethodGet, "/usage/me/usage-breakdown?window=3600", "", "session")
	if breakdown.Code != http.StatusOK || fixture.usage.breakdownCalls != 1 ||
		fixture.usage.breakdownEndAt == nil || *fixture.usage.breakdownEndAt != 10_000 {
		t.Fatalf("breakdown = %d %s, usage=%#v", breakdown.Code, breakdown.Body.String(), fixture.usage)
	}
}

func TestPortalDailyUsageTrendUsesOnlySessionIdentityAndDedicatedBounds(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	fixture.usage.trend = usage.UserDailyTrend{
		WindowDays: 30, Timezone: "Asia/Shanghai", WindowStartAt: 1_000, WindowEndAt: 10_000,
		CollectionStartedAt: 2_000, EffectiveStartAt: 2_000,
		Dimension: usage.UserTrendModelReasoning,
		Days: []usage.UserDailyUsage{{
			Date: "1970-01-01", StartAt: 1_000, EndAt: 10_000,
			CollectionState: usage.DailyCollectionPartial,
			RequestCount:    2, TotalTokens: 100, WeightedTokens: 125,
			Combinations: []usage.UserDailyCombination{{
				Model: "gpt-5.4", ReasoningEffort: "high", RequestCount: 2,
				TotalTokens: 100, WeightedTokens: 125,
			}},
		}},
	}

	response := fixture.request(
		http.MethodGet,
		"/usage/me/usage-trend?window=30d&dimension=model_reasoning",
		"",
		"session",
	)
	if response.Code != http.StatusOK {
		t.Fatalf("daily trend = %d %s", response.Code, response.Body.String())
	}
	if fixture.usage.trendCalls != 1 || fixture.usage.trendUser != "alice@example.com" ||
		fixture.usage.trendDays != 30 || fixture.usage.trendTimezone != "Asia/Shanghai" ||
		fixture.usage.trendEndAt != 10_000 || fixture.usage.trendDimension != usage.UserTrendModelReasoning {
		t.Fatalf("daily trend call = %#v", fixture.usage)
	}
	if fixture.usage.accountsCalls != 0 || fixture.usage.breakdownCalls != 0 {
		t.Fatalf("daily trend widened other usage requests: %#v", fixture.usage)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`"window":"30d"`, `"dimension":"model_reasoning"`, `"window_timezone":"Asia/Shanghai"`,
		`"model":"gpt-5.4"`, `"reasoning_effort":"high"`, `"weighted_tokens":125`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("daily trend response %s does not contain %s", body, expected)
		}
	}
	if strings.Contains(body, `"user"`) || strings.Contains(body, "bob@example.com") {
		t.Fatalf("daily trend response leaked a user identity: %s", body)
	}
}

func TestPortalDailyUsageTrendRejectsUserOverrideAndInvalidSelectors(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}

	for _, path := range []string{
		"/usage/me/usage-trend?window=30d&dimension=total&user=bob@example.com",
		"/usage/me/usage-trend?window=30d&dimension=total&user=",
		"/usage/me/usage-trend?window=1d&dimension=total",
		"/usage/me/usage-trend?window=30d&dimension=model",
	} {
		response := fixture.request(http.MethodGet, path, "", "session")
		assertPortalError(t, response, http.StatusBadRequest, "invalid_request")
	}
	if fixture.usage.trendCalls != 0 {
		t.Fatalf("invalid trend request reached usage store %d times", fixture.usage.trendCalls)
	}
	unauthorized := fixture.request(http.MethodGet, "/usage/me/usage-trend", "", "")
	assertPortalError(t, unauthorized, http.StatusUnauthorized, "session_required")
}

func TestPortalQuotaIsUserScopedAndUsesTheLiveDefaultPolicy(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	fixture.identity.settings["user_quota.default_weekly_tokens"] = float64(20_000_000)
	fixture.identity.settings["user_quota.reset_personal_weekly_on_new_week"] = false
	limit := int64(20_000_000)
	remaining := int64(17_000_000)
	percent := float64(15)
	fixture.weekly.result = usage.WeeklyQuota{
		Period: "natural_week", Timezone: "Asia/Shanghai",
		WeekStartAt: 9_000, WeekEndAt: 20_000,
		LimitTokens: &limit, BaseLimitTokens: &limit,
		UsedTokens: 3_000_000, WeightedUsedTokens: 3_000_000,
		RawUsedTokens: 2_400_000, UnweightedUsedTokens: 2_400_000,
		WeightedRawUsedTokens: 3_000_000, RemainingTokens: &remaining,
		UsedPercent: &percent, Source: "default", PolicyMode: "inherit",
		DefaultLimitTokens: &limit, QuotaUnit: "weighted_tokens",
	}

	response := fixture.request(http.MethodGet, "/usage/me/quota", "", "session")
	if response.Code != http.StatusOK {
		t.Fatalf("weekly quota = %d %s", response.Code, response.Body.String())
	}
	if fixture.weekly.calls != 1 || fixture.weekly.user != "alice@example.com" ||
		fixture.weekly.defaultLimit == nil || *fixture.weekly.defaultLimit != limit {
		t.Fatalf("quota reader arguments = %#v", fixture.weekly)
	}
	body := response.Body.String()
	for _, expected := range []string{
		`"generated_at":10000`, `"weekly_quota"`, `"limit_tokens":20000000`,
		`"weighted_used_tokens":3000000`, `"personal_policy_reset_enabled":false`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("quota response %s does not contain %s", body, expected)
		}
	}
	if strings.Contains(body, "old-key") || strings.Contains(body, "bob@example.com") {
		t.Fatalf("quota response leaked identity material: %s", body)
	}
}

func TestPortalRejectsInvisibleAccountBeforeReadingBreakdown(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}

	response := fixture.request(
		http.MethodGet,
		"/usage/me/usage-breakdown?account=private-account&window=3600",
		"",
		"session",
	)
	assertPortalError(t, response, http.StatusNotFound, "account_not_found")
	if fixture.usage.breakdownCalls != 0 {
		t.Fatalf("invisible account reached usage store %d times", fixture.usage.breakdownCalls)
	}
}

func TestPortalRouteChangeUsesExpectedValueAndSurfacesConflict(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	fixture.routes.err = controlplane.ErrRouteConflict

	response := fixture.request(
		http.MethodPut,
		"/usage/me/group",
		`{"group_id":"beta"}`,
		"session",
	)
	assertPortalError(t, response, http.StatusConflict, "route_conflict")
	if !reflect.DeepEqual(
		[]string{fixture.routes.user, fixture.routes.target, fixture.routes.expected},
		[]string{"alice@example.com", "beta", "alpha"},
	) {
		t.Fatalf("route arguments = %#v", fixture.routes)
	}
}

func TestPortalAutoAssignsLeastUsedReliableEntitledAccount(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.server.states = fixture.states
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	delete(fixture.identity.routes, "alice@example.com")
	usedAlpha, usedBeta, usedPrivate, usedDisabled := 30.0, 10.0, 1.0, 0.0
	fixture.identity.accounts = append(fixture.identity.accounts,
		controlplane.Account{ID: "private", Email: "private@example.test", GroupEnabled: true},
		controlplane.Account{ID: "disabled", Email: "disabled@example.test", GroupEnabled: false},
	)
	fixture.identity.records = append(fixture.identity.records,
		controlplane.KeyRecord{
			Label: "alice:disabled", Account: "disabled", User: "alice@example.com", Status: "active", Key: "old-key",
		},
	)
	fixture.states.states = map[string]failover.AccountState{
		"alpha":    reliablePortalAccountState("alpha", &usedAlpha),
		"beta":     reliablePortalAccountState("beta", &usedBeta),
		"private":  reliablePortalAccountState("private", &usedPrivate),
		"disabled": reliablePortalAccountState("disabled", &usedDisabled),
	}
	fixture.routes.result = failover.RebalanceResult{MovedUsers: 1, SnapshotGeneration: "generation-2"}

	response := fixture.request(http.MethodPost, "/usage/me/route/auto-assign", "", "session")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"current_group":"beta"`) ||
		!strings.Contains(response.Body.String(), `"changed":true`) ||
		!strings.Contains(response.Body.String(), `"snapshot_generation":"generation-2"`) {
		t.Fatalf("automatic route assignment = %d %s", response.Code, response.Body.String())
	}
	if !reflect.DeepEqual(
		[]string{fixture.routes.user, fixture.routes.target, fixture.routes.expected},
		[]string{"alice@example.com", "beta", ""},
	) {
		t.Fatalf("automatic route arguments = %#v", fixture.routes)
	}
}

func TestPortalAutoAssignmentIsIdempotentAndFailsClosedWithoutReliableQuota(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.server.states = fixture.states
	unauthorized := fixture.request(http.MethodPost, "/usage/me/route/auto-assign", "", "")
	assertPortalError(t, unauthorized, http.StatusUnauthorized, "session_required")
	if fixture.routes.user != "" {
		t.Fatalf("unauthenticated automatic assignment reached route changer: %#v", fixture.routes)
	}
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}

	alreadyAssigned := fixture.request(http.MethodPost, "/usage/me/route/auto-assign", "", "session")
	if alreadyAssigned.Code != http.StatusOK || !strings.Contains(alreadyAssigned.Body.String(), `"current_group":"alpha"`) ||
		!strings.Contains(alreadyAssigned.Body.String(), `"changed":false`) || fixture.routes.user != "" {
		t.Fatalf("idempotent automatic route assignment = %d %s, route=%#v", alreadyAssigned.Code, alreadyAssigned.Body.String(), fixture.routes)
	}

	delete(fixture.identity.routes, "alice@example.com")
	fixture.states.states = map[string]failover.AccountState{
		"alpha": {Account: "alpha", Eligible: true, Reason: "quota_stale"},
	}
	unavailable := fixture.request(http.MethodPost, "/usage/me/route/auto-assign", "", "session")
	assertPortalError(t, unavailable, http.StatusConflict, "route_unavailable")
	if fixture.routes.user != "" {
		t.Fatalf("unsafe automatic assignment reached route changer: %#v", fixture.routes)
	}
}

func TestPortalAutoAssignmentTreatsConcurrentWinnerAsIdempotentSuccess(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.server.states = fixture.states
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	delete(fixture.identity.routes, "alice@example.com")
	used := 10.0
	fixture.states.states = map[string]failover.AccountState{"alpha": reliablePortalAccountState("alpha", &used)}
	fixture.routes.onMove = func() {
		fixture.identity.routes["alice@example.com"] = "beta"
	}
	fixture.routes.err = controlplane.ErrRouteConflict

	response := fixture.request(http.MethodPost, "/usage/me/route/auto-assign", "", "session")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"current_group":"beta"`) ||
		!strings.Contains(response.Body.String(), `"changed":false`) {
		t.Fatalf("concurrent automatic route assignment = %d %s", response.Code, response.Body.String())
	}
}

func reliablePortalAccountState(account string, used *float64) failover.AccountState {
	return failover.AccountState{
		Account: account, Eligible: true, Reason: "available", UsedPercent: used,
		RemainingPercent: floatPointer(100 - *used), Headroom: 95 - *used,
	}
}

func floatPointer(value float64) *float64 {
	return &value
}

func TestPortalKeyRotationRequiresConfirmationAndReturnsOnlyNewKey(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11_000}
	fixture.keys.result = identity.RotationResult{APIKey: "new-key", SnapshotGeneration: "generation-2"}

	rejected := fixture.request(http.MethodPost, "/usage/me/key/rotate", `{"confirm":false}`, "session")
	assertPortalError(t, rejected, http.StatusBadRequest, "confirmation_required")

	response := fixture.request(http.MethodPost, "/usage/me/key/rotate", `{"confirm":true}`, "session")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "new-key") ||
		strings.Contains(response.Body.String(), "old-key") {
		t.Fatalf("rotation response = %d %s", response.Code, response.Body.String())
	}
	if fixture.keys.user != "alice@example.com" || fixture.keys.expected != "old-key" {
		t.Fatalf("rotation arguments = %#v", fixture.keys)
	}
}

func TestPortalAccountStatusUsesStablePublicCodes(t *testing.T) {
	account := controlplane.Account{ID: "alpha", GroupEnabled: true}
	tests := []struct {
		name       string
		state      failover.AccountState
		found      bool
		code       string
		selectable bool
	}{
		{name: "available", state: failover.AccountState{Reason: "available"}, found: true, code: "available", selectable: true},
		{name: "stopped", state: failover.AccountState{Reason: "container_not_running"}, found: true, code: "stopped"},
		{name: "auth", state: failover.AccountState{Reason: "oauth_missing"}, found: true, code: "auth_missing"},
		{name: "credential", state: failover.AccountState{Reason: "credential_unavailable"}, found: true, code: "credential_unavailable"},
		{name: "cooldown", state: failover.AccountState{Reason: "transient_cooldown"}, found: true, code: "transient_cooldown", selectable: true},
		{name: "rate-limited", state: failover.AccountState{Reason: "rate_limited"}, found: true, code: "rate_limited", selectable: true},
		{name: "degraded", state: failover.AccountState{Reason: "degraded"}, found: true, code: "degraded", selectable: true},
		{name: "runtime-unknown", state: failover.AccountState{Reason: "runtime_unknown"}, found: true, code: "unknown", selectable: true},
		{name: "reserve", state: failover.AccountState{Reason: "reserve_reached"}, found: true, code: "quota_warning", selectable: true},
		{name: "stale", state: failover.AccountState{Reason: "quota_stale"}, found: true, code: "unknown", selectable: true},
		{name: "unavailable", state: failover.AccountState{Reason: "quota_unavailable"}, found: true, code: "quota_unknown", selectable: true},
		{name: "missing", found: false, code: "unknown", selectable: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := presentAccountState(account, test.state, test.found)
			if status.Code != test.code || status.Selectable != test.selectable || status.Label == "" || status.Reason == "" {
				t.Fatalf("status = %#v", status)
			}
		})
	}
}

func TestPublicUsageLimitsExposeCurrentQuotaWithoutResetCreditsOrActions(t *testing.T) {
	fixture := newPortalFixture(t)
	resetAt := int64(20_000)
	resetAfter := int64(10_000)
	allowed := true
	limitReached := false
	plan := "team"
	credits := int64(3)
	window := quota.WeeklyWindow{
		Key: "default:604800", Label: "周额度", WindowSlot: "primary",
		UsedPercent: 25, RemainingPercent: 75, ReportedUsedPercent: 25,
		ResetAt: &resetAt, ResetAfterSeconds: &resetAfter, WindowSeconds: quota.WeeklyWindowSeconds,
		LimitReached: false, Resettable: true,
	}
	fixture.quota.state = quota.RuntimeState{
		Version: 1, HeartbeatAt: 10_000, LastSuccessAt: 10_000,
		Snapshot: quota.Snapshot{
			GeneratedAt: 10_000, CacheTTLSeconds: 60, Cached: true, Refreshing: false,
			Accounts: []quota.AccountQuota{{
				Account: "alpha", Status: "ok", PlanType: &plan, Allowed: &allowed,
				LimitReached: &limitReached, ResetCreditCount: &credits,
				Weekly: &window, WeeklyWindows: []quota.WeeklyWindow{window},
			}},
		},
	}
	fixture.quota.found = true

	response := fixture.request(http.MethodGet, "/usage/limits", "", "")
	if response.Code != http.StatusOK {
		t.Fatalf("public usage limits = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "reset_credit") ||
		strings.Contains(response.Body.String(), "resettable") ||
		strings.Contains(response.Body.String(), "oauth") {
		t.Fatalf("public usage limits leaked reset or auth details: %s", response.Body.String())
	}
	var payload struct {
		GeneratedAt int64 `json:"generated_at"`
		Accounts    []struct {
			Account       string               `json:"account"`
			Weekly        *quota.WeeklyWindow  `json:"weekly"`
			WeeklyWindows []quota.WeeklyWindow `json:"weekly_windows"`
		} `json:"accounts"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload.GeneratedAt != 10_000 ||
		len(payload.Accounts) != 1 || payload.Accounts[0].Account != "alpha" ||
		payload.Accounts[0].Weekly == nil || payload.Accounts[0].Weekly.RemainingPercent != 75 ||
		len(payload.Accounts[0].WeeklyWindows) != 1 {
		t.Fatalf("public usage limit payload = (%#v, %v)", payload, err)
	}
}

type portalFixture struct {
	t        *testing.T
	server   *Server
	router   *gin.Engine
	identity *portalIdentityFake
	sessions *portalSessionFake
	usage    *portalUsageFake
	routes   *portalRouteFake
	keys     *portalKeyFake
	quota    *portalQuotaStateFake
	weekly   *portalWeeklyQuotaFake
	states   *portalStatesFake
}

func newPortalFixture(t *testing.T) *portalFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	now := func() time.Time { return time.Unix(10_000, 0) }
	hash, err := hashPasswordWithSalt("initial-password", []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("hash fixture password: %v", err)
	}
	identityStore := &portalIdentityFake{
		accounts: []controlplane.Account{
			{ID: "alpha", Email: "alpha@example.test", GroupEnabled: true},
			{ID: "beta", Email: "beta@example.test", GroupEnabled: true},
		},
		routes: map[string]string{"alice@example.com": "alpha"},
		records: []controlplane.KeyRecord{
			{Label: "alice:alpha", Account: "alpha", User: "alice@example.com", Status: "active", Key: "old-key"},
			{Label: "alice:beta", Account: "beta", User: "alice@example.com", Status: "active", Key: "old-key"},
			{Label: "bob:alpha", Account: "alpha", User: "bob@example.com", Status: "active", Key: "bob-key"},
		},
		settings: map[string]any{"user_quota.timezone": "Asia/Shanghai"},
		secrets:  map[string]string{"portal_initial_password": "initial-password"},
	}
	sessions := &portalSessionFake{
		now: now, sessions: make(map[string]usage.PortalSession),
		credentials: map[string]usage.PortalCredential{
			"alice@example.com": {PasswordHash: hash, MustChange: false, CreatedAt: 1, UpdatedAt: 1},
		},
	}
	usageReader := &portalUsageFake{}
	routeChanger := &portalRouteFake{}
	keyRotator := &portalKeyFake{}
	quotaStore := &portalQuotaStateFake{}
	weeklyQuota := &portalWeeklyQuotaFake{}
	states := &portalStatesFake{}
	server, err := New(Config{
		Identity: identityStore, Sessions: sessions, Usage: usageReader,
		Quotas:      weeklyQuota,
		PublicUsage: usageReader,
		Routes:      routeChanger, Keys: keyRotator, QuotaStore: quotaStore,
		Now: now, SessionTTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("New portal server: %v", err)
	}
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		t.Fatalf("disable trusted proxies: %v", err)
	}
	server.Register(router)
	return &portalFixture{
		t: t, server: server, router: router, identity: identityStore,
		sessions: sessions, usage: usageReader, routes: routeChanger, keys: keyRotator,
		quota: quotaStore, weekly: weeklyQuota, states: states,
	}
}

func TestPublicUsageAPIPreservesBoundedAggregateContract(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.usage.publicResult = map[string]usage.PublicAccountUsage{
		"alpha": {Account: "alpha", ActiveKeys: 2, RequestCount: 5},
		"beta":  {Account: "beta"},
	}
	response := fixture.request(http.MethodGet, "/usage/api?window=300", "", "")
	if response.Code != http.StatusOK ||
		!strings.Contains(response.Body.String(), `"window_seconds":300`) ||
		!strings.Contains(response.Body.String(), `"active_keys":2`) ||
		!strings.Contains(response.Body.String(), `"requests":5`) ||
		strings.Contains(response.Body.String(), "account_email") ||
		strings.Contains(response.Body.String(), `"users"`) {
		t.Fatalf("public usage response = %d %s", response.Code, response.Body.String())
	}
	response = fixture.request(http.MethodGet, "/usage/api?window=42", "", "")
	assertPortalError(t, response, http.StatusBadRequest, "invalid_request")
}

func (fixture *portalFixture) request(method string, path string, body string, token string) *httptest.ResponseRecorder {
	return fixture.requestFrom(method, path, body, token, "192.0.2.1:1000")
}

func (fixture *portalFixture) requestFrom(
	method string,
	path string,
	body string,
	token string,
	remoteAddress string,
) *httptest.ResponseRecorder {
	fixture.t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	request.RemoteAddr = remoteAddress
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: token})
	}
	response := httptest.NewRecorder()
	fixture.router.ServeHTTP(response, request)
	return response
}

func assertPortalError(t *testing.T, response *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("response status/body = %d %s", response.Code, response.Body.String())
	}
	var payload ErrorEnvelope
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if payload.Error.Code != code {
		t.Fatalf("error payload = %#v", payload)
	}
}

type portalIdentityFake struct {
	accounts []controlplane.Account
	routes   map[string]string
	records  []controlplane.KeyRecord
	settings map[string]any
	secrets  map[string]string
}

func (store *portalIdentityFake) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return append([]controlplane.Account(nil), store.accounts...), nil
}

func (store *portalIdentityFake) ReadRoutes(context.Context) (map[string]string, error) {
	result := make(map[string]string, len(store.routes))
	for user, account := range store.routes {
		result[user] = account
	}
	return result, nil
}

func (store *portalIdentityFake) ReadKeyRecordsForUsers(
	_ context.Context,
	users []string,
) ([]controlplane.KeyRecord, error) {
	wanted := make(map[string]struct{}, len(users))
	for _, user := range users {
		wanted[strings.ToLower(strings.TrimSpace(user))] = struct{}{}
	}
	result := make([]controlplane.KeyRecord, 0)
	for _, record := range store.records {
		if _, found := wanted[strings.ToLower(strings.TrimSpace(record.User))]; found {
			result = append(result, record)
		}
	}
	return result, nil
}

func (store *portalIdentityFake) ReadSettings(context.Context) (map[string]any, error) {
	return store.settings, nil
}

func (store *portalIdentityFake) ReadSecret(_ context.Context, name string) (string, bool, error) {
	value, found := store.secrets[name]
	return value, found, nil
}

type portalSessionFake struct {
	now          func() time.Time
	sessions     map[string]usage.PortalSession
	credentials  map[string]usage.PortalCredential
	created      int
	lastRevoked  string
	lastKept     string
	lastSetUser  string
	lastMustFlag bool
}

func (store *portalSessionFake) CreateSession(
	_ context.Context,
	user string,
	ttl time.Duration,
) (string, usage.PortalSession, error) {
	store.created++
	token := "created-token-" + strconv.Itoa(store.created)
	session := usage.PortalSession{User: user, ExpiresAt: store.now().Add(ttl).Unix()}
	store.sessions[token] = session
	return token, session, nil
}

func (store *portalSessionFake) ResolveSession(_ context.Context, token string) (usage.PortalSession, error) {
	session, found := store.sessions[token]
	if !found || session.ExpiresAt <= store.now().Unix() {
		return usage.PortalSession{}, usage.ErrPortalSessionNotFound
	}
	return session, nil
}

func (store *portalSessionFake) RevokeSession(_ context.Context, token string) error {
	store.lastRevoked = token
	delete(store.sessions, token)
	return nil
}

func (store *portalSessionFake) Credential(_ context.Context, user string) (usage.PortalCredential, error) {
	credential, found := store.credentials[user]
	if !found {
		return usage.PortalCredential{}, usage.ErrPortalCredentialNotFound
	}
	return credential, nil
}

func (store *portalSessionFake) SetCredential(
	_ context.Context,
	user string,
	passwordHash string,
	mustChange bool,
	keepSessionToken string,
) (usage.PortalCredential, error) {
	credential := store.credentials[user]
	credential.PasswordHash = passwordHash
	credential.MustChange = mustChange
	credential.UpdatedAt = store.now().Unix()
	store.credentials[user] = credential
	store.lastSetUser, store.lastMustFlag, store.lastKept = user, mustChange, keepSessionToken
	for token, session := range store.sessions {
		if session.User == user && token != keepSessionToken {
			delete(store.sessions, token)
		}
	}
	return credential, nil
}

type portalUsageFake struct {
	accountsResult usage.UserAccountSummary
	breakdown      usage.UserBreakdown
	user           string
	startAt        int64
	endAt          *int64
	account        string
	breakdownEndAt *int64
	accountsCalls  int
	breakdownCalls int
	trend          usage.UserDailyTrend
	trendUser      string
	trendDays      int
	trendTimezone  string
	trendEndAt     int64
	trendDimension usage.UserTrendDimension
	trendCalls     int
	publicResult   map[string]usage.PublicAccountUsage
}

func (reader *portalUsageFake) PublicGatewayUsage(
	_ context.Context,
	accounts []string,
	_ int64,
	_ int64,
) (map[string]usage.PublicAccountUsage, error) {
	result := make(map[string]usage.PublicAccountUsage, len(accounts))
	for _, account := range accounts {
		result[account] = reader.publicResult[account]
	}
	return result, nil
}

func (reader *portalUsageFake) UserAccounts(
	_ context.Context,
	user string,
	startAt int64,
	endAt *int64,
) (usage.UserAccountSummary, error) {
	reader.accountsCalls++
	reader.user, reader.startAt, reader.endAt = user, startAt, cloneInt64(endAt)
	if reader.accountsResult.Accounts == nil {
		reader.accountsResult.Accounts = make([]usage.UserAccountUsage, 0)
	}
	return reader.accountsResult, nil
}

func (reader *portalUsageFake) UserBreakdown(
	_ context.Context,
	user string,
	account string,
	startAt int64,
	endAt *int64,
) (usage.UserBreakdown, error) {
	reader.breakdownCalls++
	reader.user, reader.account, reader.startAt, reader.breakdownEndAt = user, account, startAt, cloneInt64(endAt)
	return reader.breakdown, nil
}

func (reader *portalUsageFake) UserDailyTrend(
	_ context.Context,
	user string,
	days int,
	timezone string,
	endAt int64,
	dimension usage.UserTrendDimension,
) (usage.UserDailyTrend, error) {
	reader.trendCalls++
	reader.trendUser, reader.trendDays, reader.trendTimezone = user, days, timezone
	reader.trendEndAt, reader.trendDimension = endAt, dimension
	return reader.trend, nil
}

type portalRouteFake struct {
	user     string
	target   string
	expected string
	result   failover.RebalanceResult
	err      error
	onMove   func()
}

func (changer *portalRouteFake) MoveUser(
	_ context.Context,
	user string,
	target string,
	expected string,
) (failover.RebalanceResult, error) {
	changer.user, changer.target, changer.expected = user, target, expected
	if changer.onMove != nil {
		changer.onMove()
	}
	return changer.result, changer.err
}

type portalStatesFake struct {
	states map[string]failover.AccountState
	err    error
}

func (provider *portalStatesFake) AccountStates(context.Context) (map[string]failover.AccountState, error) {
	result := make(map[string]failover.AccountState, len(provider.states))
	for account, state := range provider.states {
		result[account] = state
	}
	return result, provider.err
}

type portalKeyFake struct {
	user     string
	expected string
	result   identity.RotationResult
	err      error
}

type portalQuotaStateFake struct {
	state quota.RuntimeState
	found bool
	err   error
}

type portalWeeklyQuotaFake struct {
	result       usage.WeeklyQuota
	err          error
	user         string
	defaultLimit *int64
	calls        int
}

func (reader *portalWeeklyQuotaFake) WeeklyQuota(
	_ context.Context,
	user string,
	defaultLimit *int64,
) (usage.WeeklyQuota, error) {
	reader.calls++
	reader.user = user
	reader.defaultLimit = cloneInt64(defaultLimit)
	return reader.result, reader.err
}

func (store *portalQuotaStateFake) ReadRuntimeState(
	_ context.Context,
	_ string,
	target any,
) (bool, error) {
	if store.err != nil || !store.found {
		return store.found, store.err
	}
	raw, err := json.Marshal(store.state)
	if err != nil {
		return false, err
	}
	destination, ok := target.(*json.RawMessage)
	if !ok {
		return false, errors.New("unexpected quota state target")
	}
	*destination = raw
	return true, nil
}

func (rotator *portalKeyFake) RotateUserKey(
	_ context.Context,
	user string,
	expected string,
) (identity.RotationResult, error) {
	rotator.user, rotator.expected = user, expected
	return rotator.result, rotator.err
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
