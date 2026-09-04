package failover

import (
	"math"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
)

// BuildAccountStates converts one complete official-quota snapshot into the
// fail-closed routing capacity model. Unknown, stale, disabled, unreachable,
// or non-default quota windows never become migration targets.
func BuildAccountStates(
	accounts []controlplane.Account,
	snapshot quota.Snapshot,
	running map[string]bool,
	now time.Time,
	staleAfter time.Duration,
	reservePercent float64,
) map[string]AccountState {
	if staleAfter < 5*time.Second || staleAfter > 24*time.Hour {
		staleAfter = defaultAccountStateStaleAfter
	}
	if math.IsNaN(reservePercent) || math.IsInf(reservePercent, 0) || reservePercent < 0 || reservePercent > 50 {
		reservePercent = 5
	}
	observedAt := snapshot.GeneratedAt
	age := now.Unix() - observedAt
	fresh := observedAt > 0 && age >= -30 && age <= int64(staleAfter/time.Second)
	quotas := make(map[string]quota.AccountQuota, len(snapshot.Accounts))
	for _, accountQuota := range snapshot.Accounts {
		if accountQuota.Account != "" {
			quotas[accountQuota.Account] = accountQuota
		}
	}
	result := make(map[string]AccountState, len(accounts))
	for _, account := range accounts {
		accountQuota, found := quotas[account.ID]
		weekly := defaultWeeklyWindow(accountQuota)
		var used, remaining *float64
		if weekly != nil {
			usedValue := weekly.UsedPercent
			remainingValue := weekly.RemainingPercent
			used, remaining = &usedValue, &remainingValue
		}
		exhausted := fresh && found && accountQuota.Status == "ok" &&
			((accountQuota.LimitReached != nil && *accountQuota.LimitReached) ||
				(weekly != nil && (weekly.LimitReached || weekly.UsedPercent >= 100)))
		headroom := 0.0
		if remaining != nil {
			headroom = math.Max(0, *remaining-reservePercent)
		}
		oauthConfigured := found && accountQuota.Status != "auth_missing"
		allowed := accountQuota.Allowed == nil || *accountQuota.Allowed
		eligible := account.GroupEnabled && running[account.ID] && oauthConfigured && fresh &&
			accountQuota.Status == "ok" && weekly != nil && allowed && !exhausted && headroom > 0
		reason := "available"
		switch {
		case exhausted:
			reason = "quota_exhausted"
		case !account.GroupEnabled:
			reason = "account_disabled"
		case !running[account.ID]:
			reason = "container_not_running"
		case !oauthConfigured:
			reason = "oauth_missing"
		case !fresh:
			reason = "quota_stale"
		case !found || accountQuota.Status != "ok" || weekly == nil:
			reason = "quota_unavailable"
		case !allowed:
			reason = "upstream_disallowed"
		case headroom <= 0:
			reason = "reserve_reached"
		}
		resetAt := int64(0)
		if weekly != nil && weekly.ResetAt != nil {
			resetAt = *weekly.ResetAt
		}
		result[account.ID] = AccountState{
			Account: account.ID, Eligible: eligible, Exhausted: exhausted, Reason: reason,
			UsedPercent: used, RemainingPercent: remaining, Headroom: headroom,
			ResetAt: resetAt, ObservedAt: observedAt,
		}
	}
	return result
}

func defaultWeeklyWindow(accountQuota quota.AccountQuota) *quota.WeeklyWindow {
	if accountQuota.Weekly != nil &&
		(accountQuota.Weekly.Key == "" || strings.HasPrefix(accountQuota.Weekly.Key, "default:")) {
		weekly := *accountQuota.Weekly
		return &weekly
	}
	for _, window := range accountQuota.WeeklyWindows {
		if strings.HasPrefix(window.Key, "default:") {
			weekly := window
			return &weekly
		}
	}
	return nil
}
