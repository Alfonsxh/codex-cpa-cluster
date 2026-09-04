package failover

import (
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
)

func TestBuildAccountStatesRequiresFreshHealthyDefaultQuotaAndReserve(t *testing.T) {
	accounts := []controlplane.Account{
		{ID: "alpha", GroupEnabled: true},
		{ID: "beta", GroupEnabled: true},
		{ID: "gamma", GroupEnabled: true},
		{ID: "delta", GroupEnabled: true},
		{ID: "percent-only", GroupEnabled: true},
		{ID: "disabled", GroupEnabled: false},
	}
	snapshot := quota.Snapshot{GeneratedAt: 1000, Accounts: []quota.AccountQuota{
		accountQuota("alpha", 100, true, "ok"),
		accountQuota("beta", 10, false, "ok"),
		accountQuota("gamma", 20, false, "auth_missing"),
		accountQuota("delta", 95, false, "ok"),
		accountQuota("percent-only", 100, false, "ok"),
		accountQuota("disabled", 10, false, "ok"),
	}}
	running := map[string]bool{"alpha": true, "beta": true, "gamma": true, "delta": true, "percent-only": true, "disabled": true}
	states := BuildAccountStates(accounts, snapshot, running, time.Unix(1000, 0), 2*time.Minute, 5)
	if !states["alpha"].Exhausted || states["alpha"].Eligible || states["alpha"].Reason != "quota_exhausted" {
		t.Fatalf("alpha = %#v", states["alpha"])
	}
	if !states["beta"].Eligible || states["beta"].Headroom != 85 || states["beta"].Reason != "available" {
		t.Fatalf("beta = %#v", states["beta"])
	}
	if states["gamma"].Reason != "oauth_missing" || states["gamma"].Eligible {
		t.Fatalf("gamma = %#v", states["gamma"])
	}
	if states["delta"].Reason != "reserve_reached" || states["delta"].Eligible {
		t.Fatalf("delta = %#v", states["delta"])
	}
	if !states["percent-only"].Exhausted || states["percent-only"].Eligible || states["percent-only"].Reason != "quota_exhausted" {
		t.Fatalf("percent-only = %#v", states["percent-only"])
	}
	if states["disabled"].Reason != "account_disabled" || states["disabled"].Eligible {
		t.Fatalf("disabled = %#v", states["disabled"])
	}
}

func TestBuildAccountStatesFailsClosedForStaleFutureMissingRuntimeAndAdditionalOnly(t *testing.T) {
	accounts := []controlplane.Account{{ID: "alpha", GroupEnabled: true}}
	running := map[string]bool{"alpha": true}
	snapshot := quota.Snapshot{GeneratedAt: 800, Accounts: []quota.AccountQuota{
		accountQuota("alpha", 10, false, "ok"),
	}}
	states := BuildAccountStates(accounts, snapshot, running, time.Unix(1000, 0), 2*time.Minute, 5)
	if states["alpha"].Reason != "quota_stale" || states["alpha"].Eligible || states["alpha"].Exhausted {
		t.Fatalf("stale alpha = %#v", states["alpha"])
	}
	snapshot.GeneratedAt = 1040
	states = BuildAccountStates(accounts, snapshot, running, time.Unix(1000, 0), 2*time.Minute, 5)
	if states["alpha"].Reason != "quota_stale" {
		t.Fatalf("future alpha = %#v", states["alpha"])
	}
	snapshot.GeneratedAt = 1000
	states = BuildAccountStates(accounts, snapshot, map[string]bool{}, time.Unix(1000, 0), 2*time.Minute, 5)
	if states["alpha"].Reason != "container_not_running" {
		t.Fatalf("unreachable alpha = %#v", states["alpha"])
	}
	additional := accountQuota("alpha", 100, true, "ok")
	additional.Weekly.Key = "additional:model:primary_window"
	additional.WeeklyWindows = []quota.WeeklyWindow{*additional.Weekly}
	topLevelLimitReached := false
	additional.LimitReached = &topLevelLimitReached
	snapshot.Accounts = []quota.AccountQuota{additional}
	states = BuildAccountStates(accounts, snapshot, running, time.Unix(1000, 0), 2*time.Minute, 5)
	if states["alpha"].Reason != "quota_unavailable" || states["alpha"].Exhausted {
		t.Fatalf("additional-only alpha = %#v", states["alpha"])
	}
}

func TestBuildAccountStatesHonorsUpstreamDisallow(t *testing.T) {
	account := accountQuota("alpha", 10, false, "ok")
	allowed := false
	account.Allowed = &allowed
	states := BuildAccountStates(
		[]controlplane.Account{{ID: "alpha", GroupEnabled: true}},
		quota.Snapshot{GeneratedAt: 1000, Accounts: []quota.AccountQuota{account}},
		map[string]bool{"alpha": true}, time.Unix(1000, 0), 2*time.Minute, 5,
	)
	if states["alpha"].Reason != "upstream_disallowed" || states["alpha"].Eligible {
		t.Fatalf("disallowed alpha = %#v", states["alpha"])
	}
}

func accountQuota(account string, used float64, exhausted bool, status string) quota.AccountQuota {
	allowed := true
	limitReached := exhausted
	resetAt := int64(2_000_000_000)
	weekly := quota.WeeklyWindow{
		Key: "default:primary_window", UsedPercent: used, RemainingPercent: 100 - used,
		LimitReached: exhausted, ResetAt: &resetAt, WindowSeconds: quota.WeeklyWindowSeconds,
	}
	return quota.AccountQuota{
		Account: account, Status: status, Allowed: &allowed, LimitReached: &limitReached,
		Weekly: &weekly, WeeklyWindows: []quota.WeeklyWindow{weekly},
	}
}
