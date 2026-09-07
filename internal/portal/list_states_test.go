package portal

import (
	"net/http"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/failover"
	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
)

func TestDisplayStateCannotAuthorizeAccountSwitch(t *testing.T) {
	fixture := newPortalFixture(t)
	fixture.sessions.sessions["session"] = usage.PortalSession{User: "alice@example.com", ExpiresAt: 11000}
	fixture.server.listStates = &portalStatesFake{states: map[string]failover.AccountState{
		"beta": {Eligible: true, Headroom: 80, Reason: "available"},
	}}
	fixture.server.states = &portalStatesFake{states: map[string]failover.AccountState{
		"beta": {Reason: "credential_unavailable"},
	}}
	response := fixture.request(http.MethodGet, "/usage/me/accounts?window=3600", "", "session")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"code":"available"`) {
		t.Fatalf("list did not use display state: %d %s", response.Code, response.Body.String())
	}
	response = fixture.request(http.MethodPut, "/usage/me/group", `{"group_id":"beta"}`, "session")
	assertPortalError(t, response, http.StatusConflict, "account_unavailable")
	if fixture.routes.target != "" {
		t.Fatal("cached display state reached route mutation")
	}
}
