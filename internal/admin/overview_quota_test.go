package admin

import (
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
)

func TestBuildOverviewAccountQuotaSummary(t *testing.T) {
	boolPointer := func(value bool) *bool { return &value }
	weekly := func(used float64, limitReached bool) *quota.WeeklyWindow {
		return &quota.WeeklyWindow{UsedPercent: used, LimitReached: limitReached}
	}
	tests := []struct {
		name       string
		accounts   []controlplane.Account
		snapshot   quota.Snapshot
		quotaFound bool
		want       overviewAccountQuotaSummary
	}{
		{
			name: "averages known enabled accounts equally",
			accounts: []controlplane.Account{
				{ID: "alpha", GroupEnabled: true},
				{ID: "beta", GroupEnabled: true},
			},
			snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{
				{Account: "alpha", Status: "ok", Weekly: weekly(20, false)},
				{Account: "beta", Status: "ok", Weekly: weekly(70, false)},
			}},
			quotaFound: true,
			want: overviewAccountQuotaSummary{
				Available: true, EnabledAccounts: 2, KnownAccounts: 2,
				AverageUsedPercent: float64Pointer(45), AverageRemainingPercent: float64Pointer(55),
				EquivalentRemainingAccounts: 1.1,
			},
		},
		{
			name: "treats limit signals as exhausted and keeps high risk disjoint",
			accounts: []controlplane.Account{
				{ID: "weekly-limit", GroupEnabled: true},
				{ID: "account-limit", GroupEnabled: true},
				{ID: "not-allowed", GroupEnabled: true},
				{ID: "high-risk", GroupEnabled: true},
			},
			snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{
				{Account: "weekly-limit", Status: "ok", Weekly: weekly(12, true)},
				{Account: "account-limit", Status: "ok", LimitReached: boolPointer(true), Weekly: weekly(34, false)},
				{Account: "not-allowed", Status: "ok", Allowed: boolPointer(false), Weekly: weekly(56, false)},
				{Account: "high-risk", Status: "ok", Weekly: weekly(94, false)},
			}},
			quotaFound: true,
			want: overviewAccountQuotaSummary{
				Available: true, EnabledAccounts: 4, KnownAccounts: 4,
				AverageUsedPercent: float64Pointer(98.5), AverageRemainingPercent: float64Pointer(1.5),
				EquivalentRemainingAccounts: 0.06, ExhaustedAccounts: 3, HighRiskAccounts: 1,
			},
		},
		{
			name: "excludes disabled and unknown accounts from the average",
			accounts: []controlplane.Account{
				{ID: "known", GroupEnabled: true},
				{ID: "missing", GroupEnabled: true},
				{ID: "unavailable", GroupEnabled: true},
				{ID: "disabled", GroupEnabled: false},
			},
			snapshot: quota.Snapshot{Accounts: []quota.AccountQuota{
				{Account: "known", Status: "ok", Weekly: weekly(30, false)},
				{Account: "unavailable", Status: "weekly_unavailable"},
				{Account: "disabled", Status: "ok", Weekly: weekly(100, false)},
			}},
			quotaFound: true,
			want: overviewAccountQuotaSummary{
				Available: true, EnabledAccounts: 3, KnownAccounts: 1, UnknownAccounts: 2,
				AverageUsedPercent: float64Pointer(30), AverageRemainingPercent: float64Pointer(70),
				EquivalentRemainingAccounts: 0.7,
			},
		},
		{
			name: "returns unavailable when there is no quota snapshot",
			accounts: []controlplane.Account{
				{ID: "alpha", GroupEnabled: true},
				{ID: "disabled", GroupEnabled: false},
			},
			quotaFound: false,
			want: overviewAccountQuotaSummary{
				EnabledAccounts: 1, UnknownAccounts: 1,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := buildOverviewAccountQuotaSummary(test.accounts, test.snapshot, test.quotaFound)
			assertOverviewAccountQuotaSummary(t, got, test.want)
		})
	}
}

func assertOverviewAccountQuotaSummary(
	t *testing.T,
	got overviewAccountQuotaSummary,
	want overviewAccountQuotaSummary,
) {
	t.Helper()
	if got.Available != want.Available ||
		got.EnabledAccounts != want.EnabledAccounts ||
		got.KnownAccounts != want.KnownAccounts ||
		got.UnknownAccounts != want.UnknownAccounts ||
		got.EquivalentRemainingAccounts != want.EquivalentRemainingAccounts ||
		got.ExhaustedAccounts != want.ExhaustedAccounts ||
		got.HighRiskAccounts != want.HighRiskAccounts ||
		!equalOptionalFloat(got.AverageUsedPercent, want.AverageUsedPercent) ||
		!equalOptionalFloat(got.AverageRemainingPercent, want.AverageRemainingPercent) {
		t.Fatalf("account quota summary = %#v, want %#v", got, want)
	}
}

func equalOptionalFloat(left, right *float64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func float64Pointer(value float64) *float64 {
	return &value
}
