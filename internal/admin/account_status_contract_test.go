package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/accountstatus"
	"github.com/Alfonsxh/codex-cpa-pool/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/portal"
	"github.com/Alfonsxh/codex-cpa-pool/internal/quota"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
)

type statusSessions struct{ portal.SessionStore }

func (statusSessions) ResolveSession(_ context.Context, token string) (usage.PortalSession, error) {
	if token != "session" {
		return usage.PortalSession{}, usage.ErrPortalSessionNotFound
	}
	return usage.PortalSession{User: "alice@example.com", ExpiresAt: time.Now().Add(time.Hour).Unix()}, nil
}
func (statusSessions) Credential(context.Context, string) (usage.PortalCredential, error) {
	return usage.PortalCredential{}, nil
}

type statusUsage struct{ portal.UsageReader }

func (statusUsage) UserAccounts(context.Context, string, int64, *int64) (usage.UserAccountSummary, error) {
	return usage.UserAccountSummary{}, nil
}

func statusPortal(t *testing.T, store *controlplane.Store, states failover.AccountStateProvider) func(string, string) *httptest.ResponseRecorder {
	t.Helper()
	server, err := portal.New(portal.Config{Identity: store, Sessions: statusSessions{}, Usage: statusUsage{}, States: states, QuotaStore: store})
	if err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	server.Register(router)
	return func(query, token string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/usage/me/accounts?window=3600"+query, nil)
		request.AddCookie(&http.Cookie{Name: "cpa_user_session", Value: token})
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
}

func TestAccountSurfacesShareCanonicalStatusAndRecovery(t *testing.T) {
	server, store := newTestAdmin(t)
	states := staticAdminAccountStates{states: map[string]failover.AccountState{}}
	server.accountStates = states
	readPortal := statusPortal(t, store, states)
	// Admin-only detail reads deliberately disagree with the canonical snapshot.
	// They must not recreate a second operational-status policy.
	denied := false
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, quota.RuntimeState{Version: 1,
		Snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{{Account: "alpha", Status: "ok", Allowed: &denied,
			Weekly: &quota.WeeklyWindow{UsedPercent: 100, LimitReached: true}}}},
	}); err != nil {
		t.Fatal(err)
	}
	server.accountRuntime = staticAdminAccountRuntime{states: map[string]accountstatus.State{"alpha": {Runtime: accountstatus.Runtime{State: "unavailable"}}}}
	// Account visibility remains user scoped.
	catalog, err := store.ReadAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	catalog = append(catalog, controlplane.Account{ID: "hidden", Email: "hidden@example.com", Port: 18319, GroupEnabled: true})
	if err := store.WriteAccounts(context.Background(), catalog); err != nil {
		t.Fatal(err)
	}
	low, normal := 10.0, 80.0
	tests := []struct {
		name, reason, code string
		remaining          *float64
		selectable         bool
	}{
		{"exhausted", "quota_exhausted", "quota_exhausted", nil, false},
		{"recovered", "available", "available", &normal, true},
		{"low quota", "available", "quota_warning", &low, true},
		{"reserve", "reserve_reached", "quota_warning", &low, true},
		{"cooldown", "transient_cooldown", "transient_cooldown", nil, true},
		{"rate limit", "rate_limited", "rate_limited", nil, true},
		{"credential", "credential_unavailable", "credential_unavailable", nil, false},
		{"stopped", "container_not_running", "stopped", nil, false},
		{"oauth", "oauth_missing", "auth_missing", nil, false},
		{"disabled", "account_disabled", "disabled", nil, false},
		{"stale", "quota_stale", "unknown", nil, true},
		{"unknown quota", "quota_unavailable", "quota_unknown", nil, true},
		{"runtime unknown", "runtime_unknown", "unknown", nil, true},
		{"degraded", "degraded", "degraded", nil, true},
		{"disallowed", "upstream_disallowed", "quota_exhausted", nil, false},
		{"missing state", "", "unknown", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delete(states.states, "alpha")
			if tt.reason != "" {
				states.states["alpha"] = failover.AccountState{Account: "alpha", Reason: tt.reason, RemainingPercent: tt.remaining}
			}
			adminResponse := performAdminRequest(server, http.MethodGet, "/admin/api/accounts?window=3600", nil, map[string]string{"X-Management-Key": "test-management-key"}, nil)
			portalResponse := readPortal("", "session")
			if adminResponse.Code != http.StatusOK || portalResponse.Code != http.StatusOK {
				t.Fatalf("admin=%s portal=%s", adminResponse.Body, portalResponse.Body)
			}
			var adminResult struct {
				Accounts []accountListItem `json:"accounts"`
			}
			var portalResult struct {
				Accounts []struct {
					ID         string                     `json:"id"`
					Status     accountstatus.Presentation `json:"status"`
					Selectable bool                       `json:"selectable"`
				} `json:"accounts"`
			}
			decodeAdminResponse(t, adminResponse, &adminResult)
			decodeAdminResponse(t, portalResponse, &portalResult)
			if len(portalResult.Accounts) != 1 || portalResult.Accounts[0].ID != "alpha" {
				t.Fatalf("unscoped accounts: %s", portalResponse.Body)
			}
			a, p := adminResult.Accounts[0].OperationalStatus, portalResult.Accounts[0]
			if a != p.Status || a.Code != tt.code || a.Selectable != tt.selectable || p.Selectable != tt.selectable {
				t.Fatalf("admin=%+v portal=%+v want=%+v", a, p, tt)
			}
			overview := performAdminRequest(server, http.MethodGet, "/admin/api/overview/catalog", nil, map[string]string{"X-Management-Key": "test-management-key"}, nil)
			var overviewResult overviewCatalogResponse
			decodeAdminResponse(t, overview, &overviewResult)
			if overview.Code != http.StatusOK || overviewResult.Accounts[0].OperationalStatus != a {
				t.Fatalf("overview drift: %s", overview.Body)
			}
		})
	}
}

func TestAccountSurfacesJoinOfficialRefreshAfterAuthentication(t *testing.T) {
	server, store := newTestAdmin(t)
	readPortal := statusPortal(t, store, nil)
	ctx := context.Background()
	response := readPortal("&fresh=1", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized=%s", response.Body)
	}
	if _, found, err := quota.ReadRefreshRequest(ctx, store); err != nil || found {
		t.Fatalf("unauthorized refresh wrote state: %v %v", found, err)
	}
	response = readPortal("&fresh=1", "session")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"quota_refreshing":true`) {
		t.Fatalf("portal fresh=%s", response.Body)
	}
	request, found, err := quota.ReadRefreshRequest(ctx, store)
	if err != nil || !found || !request.Pending() {
		t.Fatalf("request=%+v %v", request, err)
	}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/accounts?fresh=1", nil, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"quota_refreshing":true`) {
		t.Fatalf("admin fresh=%s", response.Body)
	}
	joined, _, err := quota.ReadRefreshRequest(ctx, store)
	if err != nil || joined.RequestID != request.RequestID {
		t.Fatalf("refresh was duplicated: %+v %v", joined, err)
	}
	if err := quota.MarkRefreshCompleted(ctx, store, request.RequestID, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRuntimeState(ctx, quota.RuntimeStateName, quota.RuntimeState{Version: 1, Snapshot: quota.Snapshot{GeneratedAt: time.Now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	response = readPortal("&fresh=1", "session")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"quota_refreshing":false`) {
		t.Fatalf("completed/throttled=%s", response.Body)
	}
	for _, forbidden := range []string{"reset_credit", "request_id", "test-management-key", "test_external_alice"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("portal leaked %s", forbidden)
		}
	}
}
