package controlplane

import (
	"context"
	"testing"
)

func TestReadOverviewSummaryUsesOneBoundedCatalogProjection(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	if err := store.WriteAccounts(ctx, []Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, GroupEnabled: true, DefaultGroup: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18319, GroupEnabled: true},
		{ID: "disabled", Email: "disabled@accounts.example.com", Port: 18320, GroupEnabled: false},
	}); err != nil {
		t.Fatalf("WriteAccounts: %v", err)
	}
	if err := store.WriteKeyRecords(ctx, []KeyRecord{
		{Label: "alice:alpha", Account: "alpha", User: "alice@example.com", Status: "active", Key: "secret-alice", CreatedAt: 1, UpdatedAt: 1},
		{Label: "alice:beta", Account: "beta", User: "alice@example.com", Status: "active", Key: "secret-alice", CreatedAt: 1, UpdatedAt: 1},
		{Label: "bob:alpha", Account: "alpha", User: "bob@example.com", Status: "active", Key: "secret-bob", CreatedAt: 1, UpdatedAt: 1},
		{Label: "retired:alpha", Account: "alpha", User: "retired@example.com", Status: "inactive", Key: "secret-retired", CreatedAt: 1, UpdatedAt: 1},
	}); err != nil {
		t.Fatalf("WriteKeyRecords: %v", err)
	}
	if err := store.WriteRoutes(ctx, map[string]string{
		"alice@example.com": "alpha",
		"bob@example.com":   "alpha",
	}); err != nil {
		t.Fatalf("WriteRoutes: %v", err)
	}
	team, err := store.CreateTeam(ctx, "Platform", "")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.SetUserTeams(ctx, []string{"alice@example.com"}, &team.ID); err != nil {
		t.Fatalf("SetUserTeams: %v", err)
	}

	summary, err := store.ReadOverviewSummary(ctx)
	if err != nil {
		t.Fatalf("ReadOverviewSummary: %v", err)
	}
	if summary.Accounts != 3 || summary.EnabledAccounts != 2 || summary.Users != 3 ||
		summary.ActiveUsers != 2 || summary.ActiveKeys != 2 || summary.RoutedUsers != 2 ||
		summary.UnassignedUsers != 2 || summary.Teams != 1 || summary.IncompleteMatrices != 1 {
		t.Fatalf("overview summary = %#v", summary)
	}
}
