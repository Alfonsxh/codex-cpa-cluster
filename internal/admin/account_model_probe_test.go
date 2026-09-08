package admin

import (
	"context"
	"net/http"
	"testing"

	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
)

type fakeModelProbe struct{ calls int }

func (probe *fakeModelProbe) Models(context.Context, string) (runtimeops.AccountModels, error) {
	return runtimeops.AccountModels{Account: "alpha", Models: []string{"gpt-6-astra"}}, nil
}
func (probe *fakeModelProbe) Test(_ context.Context, account, model string) (runtimeops.ModelProbeResult, error) {
	probe.calls++
	return runtimeops.ModelProbeResult{Account: account, Model: model, Success: false, UpstreamStatus: 401, Code: "model_auth_failed", Message: "账号授权失败"}, nil
}

func TestAccountModelProbeRequiresAdminAndCSRFWithoutConfusingUpstreamAuth(t *testing.T) {
	server, _ := newTestAdmin(t)
	probe := &fakeModelProbe{}
	server.modelProbe = probe
	body := map[string]any{"account": "alpha", "model": "gpt-6-astra"}
	unauthorized := performAdminRequest(server, http.MethodPost, "/admin/api/accounts/model-test", body, nil, nil)
	if unauthorized.Code != 401 || probe.calls != 0 {
		t.Fatalf("unauthenticated: %d", unauthorized.Code)
	}
	login := performAdminRequest(server, http.MethodPost, "/admin/api/session", nil, map[string]string{"X-Management-Key": "test-management-key"}, nil)
	var session struct {
		CSRF string `json:"csrf_token"`
	}
	decodeAdminResponse(t, login, &session)
	cookie := login.Result().Cookies()[0]
	forbidden := performAdminRequest(server, http.MethodPost, "/admin/api/accounts/model-test", body, nil, cookie)
	if forbidden.Code != 403 || probe.calls != 0 {
		t.Fatalf("CSRF: %d", forbidden.Code)
	}
	response := performAdminRequest(server, http.MethodPost, "/admin/api/accounts/model-test", body, map[string]string{"X-CSRF-Token": session.CSRF}, cookie)
	var result runtimeops.ModelProbeResult
	decodeAdminResponse(t, response, &result)
	if response.Code != 200 || result.Success || result.UpstreamStatus != 401 || probe.calls != 1 {
		t.Fatalf("result: %d %#v", response.Code, result)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("test result should not be cached")
	}
	stillLoggedIn := performAdminRequest(server, http.MethodGet, "/admin/api/session", nil, nil, cookie)
	if stillLoggedIn.Code != 200 {
		t.Fatal("upstream auth invalidated Admin session")
	}
}
