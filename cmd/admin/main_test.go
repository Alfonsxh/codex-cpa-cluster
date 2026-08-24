package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/accountstatus"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	quotaapi "github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/runtimeops"
)

func TestLiveAccountStateProviderOverlaysRuntimeAndOAuthBeforeQuota(t *testing.T) {
	provider := liveAccountStateProvider{
		Base: fixedAccountStates{states: map[string]failover.AccountState{
			"alpha": {Reason: "quota_stale"},
			"beta":  {Reason: "quota_stale"},
			"gamma": {Reason: "available", Eligible: true, Headroom: 80},
			"delta": {Reason: "available", Eligible: true, Headroom: 80},
		}},
		Accounts: fixedAccountCatalog{accounts: []controlplane.Account{
			{ID: "alpha", GroupEnabled: true},
			{ID: "beta", GroupEnabled: true},
			{ID: "gamma", GroupEnabled: true},
			{ID: "delta", GroupEnabled: false},
		}},
		Runtime: fixedRuntimeServices{services: []runtimeops.Service{
			{Service: "cliproxy-alpha", State: "running"},
			{Service: "cliproxy-beta", State: "running"},
			{Service: "cliproxy-delta", State: "running"},
		}},
		OAuth: fixedOAuthAccounts{configured: map[string]bool{
			"alpha": true, "delta": true,
		}},
	}
	states, err := provider.AccountStates(context.Background())
	if err != nil {
		t.Fatalf("AccountStates: %v", err)
	}
	if states["alpha"].Reason != "quota_stale" {
		t.Fatalf("running authorized alpha = %#v", states["alpha"])
	}
	if states["beta"].Reason != "oauth_missing" || states["beta"].Eligible || states["beta"].Headroom != 0 {
		t.Fatalf("missing OAuth beta = %#v", states["beta"])
	}
	if states["gamma"].Reason != "container_not_running" || states["gamma"].Eligible || states["gamma"].Headroom != 0 {
		t.Fatalf("stopped gamma = %#v", states["gamma"])
	}
	if states["delta"].Reason != "account_disabled" || states["delta"].Eligible || states["delta"].Headroom != 0 {
		t.Fatalf("disabled delta = %#v", states["delta"])
	}
}

func TestLiveAccountStateProviderFailsClosedWhenDockerReadFails(t *testing.T) {
	provider := liveAccountStateProvider{
		Base: fixedAccountStates{states: map[string]failover.AccountState{
			"alpha": {Reason: "available", Eligible: true, Headroom: 80},
		}},
		Accounts: fixedAccountCatalog{accounts: []controlplane.Account{{ID: "alpha", GroupEnabled: true}}},
		Runtime:  fixedRuntimeServices{err: errors.New("Docker unavailable")},
		OAuth:    fixedOAuthAccounts{configured: map[string]bool{"alpha": true}},
	}
	states, err := provider.AccountStates(context.Background())
	if err != nil {
		t.Fatalf("AccountStates: %v", err)
	}
	if states["alpha"].Reason != "container_not_running" || states["alpha"].Eligible {
		t.Fatalf("Docker failure widened alpha = %#v", states["alpha"])
	}
}

func TestLiveAccountStateProviderAppliesRuntimeOverlayWithoutChangingSoftCapacity(t *testing.T) {
	base := map[string]failover.AccountState{}
	accounts := make([]controlplane.Account, 0, 9)
	services := make([]runtimeops.Service, 0, 8)
	configured := make(map[string]bool)
	for _, account := range []string{"alpha", "beta", "gamma", "delta", "epsilon", "zeta", "eta", "theta", "iota"} {
		base[account] = failover.AccountState{Reason: "available", Eligible: true, Headroom: 80}
		accounts = append(accounts, controlplane.Account{ID: account, GroupEnabled: true})
		configured[account] = true
		if account != "eta" {
			services = append(services, runtimeops.Service{Service: "cliproxy-" + account, State: "running"})
		}
	}
	accounts[2].GroupEnabled = false
	configured["zeta"] = false
	base["delta"] = failover.AccountState{Reason: "quota_exhausted", Exhausted: true}
	base["epsilon"] = failover.AccountState{Reason: "upstream_disallowed"}
	observer := &fixedRuntimeObserver{states: map[string]accountstatus.State{
		"alpha": {Reason: accountstatus.ReasonDegraded},
		"beta":  {Reason: accountstatus.ReasonCredentialUnavailable, DisableEligibility: true},
		"gamma": {Reason: accountstatus.ReasonDegraded},
		"delta": {Reason: accountstatus.ReasonDegraded},
		"zeta":  {Reason: accountstatus.ReasonDegraded},
		"theta": {Reason: accountstatus.ReasonQuotaExhausted, DisableEligibility: true, Exhausted: true},
		"iota":  {Reason: accountstatus.ReasonRuntimeUnknown},
	}}
	provider := liveAccountStateProvider{
		Base: fixedAccountStates{states: base}, Accounts: fixedAccountCatalog{accounts: accounts},
		Runtime: fixedRuntimeServices{services: services}, OAuth: fixedOAuthAccounts{configured: configured},
		Observer: observer,
	}

	states, err := provider.AccountStates(context.Background())
	if err != nil {
		t.Fatalf("AccountStates: %v", err)
	}
	if state := states["alpha"]; state.Reason != accountstatus.ReasonDegraded || !state.Eligible || state.Headroom != 80 {
		t.Fatalf("soft degraded alpha changed capacity: %#v", state)
	}
	if state := states["beta"]; state.Reason != accountstatus.ReasonCredentialUnavailable || state.Eligible || state.Headroom != 0 {
		t.Fatalf("invalid credential beta remained eligible: %#v", state)
	}
	for account, reason := range map[string]string{
		"gamma": "account_disabled", "delta": "quota_exhausted", "epsilon": "quota_exhausted",
		"zeta": "oauth_missing", "eta": "container_not_running",
	} {
		if state := states[account]; state.Reason != reason || state.Eligible {
			t.Fatalf("hard state %s = %#v, want %s", account, state, reason)
		}
	}
	if state := states["theta"]; state.Reason != accountstatus.ReasonQuotaExhausted || !state.Exhausted || state.Eligible {
		t.Fatalf("runtime quota theta = %#v", state)
	}
	if state := states["iota"]; state.Reason != accountstatus.ReasonRuntimeUnknown || !state.Eligible || state.Headroom != 80 {
		t.Fatalf("runtime unknown iota changed capacity: %#v", state)
	}
	if observer.services["eta"] != "" {
		t.Fatalf("stopped service was probed: %#v", observer.services)
	}
}

type fixedAccountStates struct {
	states map[string]failover.AccountState
	err    error
}

func (provider fixedAccountStates) AccountStates(context.Context) (map[string]failover.AccountState, error) {
	result := make(map[string]failover.AccountState, len(provider.states))
	for account, state := range provider.states {
		result[account] = state
	}
	return result, provider.err
}

type fixedAccountCatalog struct {
	accounts []controlplane.Account
	err      error
}

func (catalog fixedAccountCatalog) ReadAccounts(context.Context) ([]controlplane.Account, error) {
	return append([]controlplane.Account(nil), catalog.accounts...), catalog.err
}

type fixedRuntimeServices struct {
	services []runtimeops.Service
	err      error
}

type fixedRuntimeObserver struct {
	states   map[string]accountstatus.State
	services map[string]string
}

func (observer *fixedRuntimeObserver) Observe(_ context.Context, services map[string]string) map[string]accountstatus.State {
	observer.services = make(map[string]string, len(services))
	for account, service := range services {
		observer.services[account] = service
	}
	result := make(map[string]accountstatus.State, len(observer.states))
	for account, state := range observer.states {
		result[account] = state
	}
	return result
}

func (runtime fixedRuntimeServices) List(context.Context) ([]runtimeops.Service, error) {
	return append([]runtimeops.Service(nil), runtime.services...), runtime.err
}

type fixedOAuthAccounts struct {
	configured map[string]bool
}

func (loader fixedOAuthAccounts) Load(account string) (quotaapi.OAuthRecord, error) {
	if !loader.configured[account] {
		return quotaapi.OAuthRecord{}, quotaapi.ErrOAuthMissing
	}
	return quotaapi.OAuthRecord{AccessToken: "test-token"}, nil
}
