package controlplane

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestUserCreationAndRollbackPreservePreviousRouteAndMembership(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []Account{
		{ID: "alpha", Email: "alpha@example.com", Port: 18318, GroupEnabled: true},
		{ID: "beta", Email: "beta@example.com", Port: 18319, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	oldTeam, err := store.CreateTeam(ctx, "Old Team", "")
	if err != nil {
		t.Fatalf("create old team: %v", err)
	}
	newTeam, err := store.CreateTeam(ctx, "New Team", "")
	if err != nil {
		t.Fatalf("create new team: %v", err)
	}
	if err := store.WriteKeyRecords(ctx, []KeyRecord{{
		Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com",
		User: "alice@example.com", Status: "revoked", Key: "old-key", CreatedAt: 10, UpdatedAt: 20,
	}}); err != nil {
		t.Fatalf("seed old key: %v", err)
	}
	if err := store.WriteRoutes(ctx, map[string]string{"alice@example.com": "alpha"}); err != nil {
		t.Fatalf("seed stale route: %v", err)
	}
	if _, err := store.SetUserTeamsExpected(ctx, []string{"alice@example.com"}, &oldTeam.ID, TeamExpectation{}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	beforeTeams, err := store.ReadUserTeams(ctx, []string{"alice@example.com"})
	if err != nil {
		t.Fatalf("read original membership: %v", err)
	}

	creation, err := store.ApplyUserCreation(ctx, "Alice@Example.com", "new-key", &newTeam.ID)
	if err != nil {
		t.Fatalf("ApplyUserCreation: %v", err)
	}
	if len(creation.CreatedRows) != 2 || creation.PreviousRoute == nil || *creation.PreviousRoute != "alpha" {
		t.Fatalf("creation = %#v", creation)
	}
	activeKey, err := store.ActiveUserKey(ctx, "alice@example.com")
	if err != nil || activeKey != "new-key" {
		t.Fatalf("ActiveUserKey = (%q, %v)", activeKey, err)
	}
	routes, err := store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "" {
		t.Fatalf("routes after create = (%#v, %v)", routes, err)
	}
	classifications, err := store.ReadUserTeams(ctx, []string{"alice@example.com"})
	if err != nil || classifications["alice@example.com"].TeamID == nil ||
		*classifications["alice@example.com"].TeamID != newTeam.ID {
		t.Fatalf("membership after create = (%#v, %v)", classifications, err)
	}

	if err := store.RestoreUserCreation(ctx, creation); err != nil {
		t.Fatalf("RestoreUserCreation: %v", err)
	}
	restoredRecords, err := store.ReadKeyRecordsForUsers(ctx, []string{"alice@example.com"})
	if err != nil || len(restoredRecords) != 1 || restoredRecords[0].Key != "old-key" || restoredRecords[0].Status != "revoked" {
		t.Fatalf("restored records = (%#v, %v)", restoredRecords, err)
	}
	routes, err = store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "alpha" {
		t.Fatalf("restored routes = (%#v, %v)", routes, err)
	}
	restoredTeams, err := store.ReadUserTeams(ctx, []string{"alice@example.com"})
	if err != nil || !reflect.DeepEqual(restoredTeams, beforeTeams) {
		t.Fatalf("restored teams = (%#v, %v), want %#v", restoredTeams, err, beforeTeams)
	}
}

func TestUserRevocationIsExactlyRollbackable(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	ctx := context.Background()
	original := []KeyRecord{
		{Label: "alice@example.com:alpha", Account: "alpha", User: "alice@example.com", Status: "active", Key: "shared-key", CreatedAt: 1, UpdatedAt: 10},
		{Label: "alice@example.com:beta", Account: "beta", User: "alice@example.com", Status: "active", Key: "shared-key", CreatedAt: 2, UpdatedAt: 20},
	}
	if err := store.WriteKeyRecords(ctx, original); err != nil {
		t.Fatalf("seed records: %v", err)
	}
	revocation, err := store.ApplyUserRevocation(ctx, "alice@example.com")
	if err != nil || len(revocation.Rows) != 2 {
		t.Fatalf("ApplyUserRevocation = (%#v, %v)", revocation, err)
	}
	if _, err := store.ActiveUserKey(ctx, "alice@example.com"); !errors.Is(err, ErrUserLifecycleNotFound) {
		t.Fatalf("revoked ActiveUserKey error = %v", err)
	}
	if err := store.RestoreUserRevocation(ctx, revocation); err != nil {
		t.Fatalf("RestoreUserRevocation: %v", err)
	}
	restored, err := store.ReadKeyRecordsForUsers(ctx, []string{"alice@example.com"})
	if err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatalf("restored records = (%#v, %v), want %#v", restored, err, original)
	}
}

func TestUserDeletionRequiresExplicitRevokeAndRestoresAllControlState(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	ctx := context.Background()
	team, err := store.CreateTeam(ctx, "Platform", "")
	if err != nil {
		t.Fatalf("create team: %v", err)
	}
	original := []KeyRecord{{
		Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com",
		User: "alice@example.com", Status: "active", Key: "shared-key", CreatedAt: 1, UpdatedAt: 2,
	}}
	if err := store.WriteKeyRecords(ctx, original); err != nil {
		t.Fatalf("seed records: %v", err)
	}
	if err := store.WriteRoutes(ctx, map[string]string{"alice@example.com": "alpha"}); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	if _, err := store.SetUserTeamsExpected(ctx, []string{"alice@example.com"}, &team.ID, TeamExpectation{}); err != nil {
		t.Fatalf("seed membership: %v", err)
	}
	if err := store.WriteInternalKeys(ctx, map[string]InternalKey{
		"alice@example.com": {Key: "cpa_internal_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CreatedAt: 3, Status: "active"},
	}); err != nil {
		t.Fatalf("seed internal key: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO tags(id, name, color, created_at, updated_at) VALUES ('tag-1', 'Legacy', '#000000', 1, 1)`); err != nil {
		t.Fatalf("seed legacy tag: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO user_tags(user_email, tag_id, assigned_at) VALUES ('alice@example.com', 'tag-1', 4)`); err != nil {
		t.Fatalf("seed user tag: %v", err)
	}

	if _, err := store.ApplyUserDeletion(ctx, "alice@example.com", false); !errors.Is(err, ErrUserDeleteRequiresRevoke) {
		t.Fatalf("deletion without revoke error = %v", err)
	}
	if key, err := store.ActiveUserKey(ctx, "alice@example.com"); err != nil || key != "shared-key" {
		t.Fatalf("failed deletion mutated user = (%q, %v)", key, err)
	}
	deletion, err := store.ApplyUserDeletion(ctx, "alice@example.com", true)
	if err != nil || deletion.RevokedActiveKeys != 1 {
		t.Fatalf("ApplyUserDeletion = (%#v, %v)", deletion, err)
	}
	if exists, err := store.UserExists(ctx, "alice@example.com"); err != nil || exists {
		t.Fatalf("deleted user exists = (%v, %v)", exists, err)
	}
	if err := store.RestoreUserDeletion(ctx, deletion); err != nil {
		t.Fatalf("RestoreUserDeletion: %v", err)
	}
	restored, err := store.ReadKeyRecordsForUsers(ctx, []string{"alice@example.com"})
	if err != nil || !reflect.DeepEqual(restored, original) {
		t.Fatalf("restored rows = (%#v, %v), want %#v", restored, err, original)
	}
	routes, _ := store.ReadRoutes(ctx)
	teams, _ := store.ReadUserTeams(ctx, []string{"alice@example.com"})
	internal, _ := store.ReadInternalKeys(ctx)
	var tagCount int
	_ = store.db.GetContext(ctx, &tagCount, "SELECT COUNT(*) FROM user_tags WHERE user_email = 'alice@example.com' AND tag_id = 'tag-1'")
	if routes["alice@example.com"] != "alpha" || teams["alice@example.com"].TeamID == nil ||
		*teams["alice@example.com"].TeamID != team.ID || internal["alice@example.com"].Status != "active" || tagCount != 1 {
		t.Fatalf("restored related state = routes %#v teams %#v internal %#v tags %d", routes, teams, internal, tagCount)
	}
}
