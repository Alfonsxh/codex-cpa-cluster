package controlplane

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestTeamLifecycleAndMembershipVersioning(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()

	team, err := store.CreateTeam(ctx, "  Platform   Team ", " Core platform owners ")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if team.Name != "Platform Team" || team.Description != "Core platform owners" || team.UserCount != 0 {
		t.Fatalf("created team = %#v", team)
	}
	if team.TagStyle != "indigo" {
		t.Fatalf("first team tag style = %q, want indigo", team.TagStyle)
	}
	if _, err := store.CreateTeam(ctx, "platform team", "duplicate"); err == nil {
		t.Fatal("case-insensitive duplicate team name unexpectedly succeeded")
	} else {
		assertErrorIs(t, err, ErrTeamNameExists)
	}

	teamID := team.ID
	assignments, err := store.SetUserTeams(
		ctx,
		[]string{" Alice@Example.com ", "alice@example.com"},
		&teamID,
	)
	if err != nil {
		t.Fatalf("SetUserTeams: %v", err)
	}
	if len(assignments) != 1 || !assignments[0].Changed || assignments[0].MembershipVersion != 1 {
		t.Fatalf("first assignment = %#v", assignments)
	}
	repeated, err := store.SetUserTeams(ctx, []string{"alice@example.com"}, &teamID)
	if err != nil {
		t.Fatalf("repeat SetUserTeams: %v", err)
	}
	if len(repeated) != 1 || repeated[0].Changed || repeated[0].MembershipVersion != 1 {
		t.Fatalf("repeated assignment = %#v", repeated)
	}

	classifications, err := store.ReadUserTeams(
		ctx,
		[]string{"ALICE@example.com", "idle@example.com"},
	)
	if err != nil {
		t.Fatalf("ReadUserTeams: %v", err)
	}
	alice := classifications["alice@example.com"]
	if alice.TeamID == nil || *alice.TeamID != team.ID || alice.Team == nil || alice.Team.Name != team.Name ||
		alice.Team.TagStyle != team.TagStyle {
		t.Fatalf("alice team classification = %#v", alice)
	}
	if idle := classifications["idle@example.com"]; idle.TeamID != nil || idle.Team != nil || idle.TeamMembershipVersion != 0 {
		t.Fatalf("idle team classification = %#v", idle)
	}
	if _, err := store.DeleteTeam(ctx, team.ID); err == nil {
		t.Fatal("non-empty team deletion unexpectedly succeeded")
	} else {
		assertErrorIs(t, err, ErrTeamNotEmpty)
	}

	unassigned, err := store.SetUserTeams(ctx, []string{"alice@example.com"}, nil)
	if err != nil {
		t.Fatalf("unassign team: %v", err)
	}
	if len(unassigned) != 1 || !unassigned[0].Changed || unassigned[0].MembershipVersion != 2 || unassigned[0].TeamID != nil {
		t.Fatalf("unassigned membership = %#v", unassigned)
	}
	deleted, err := store.DeleteTeam(ctx, team.ID)
	if err != nil || !deleted.Deleted || deleted.Name != team.Name {
		t.Fatalf("DeleteTeam = (%#v, %v)", deleted, err)
	}
	teams, err := store.ListTeams(ctx)
	if err != nil || len(teams) != 0 {
		t.Fatalf("ListTeams after delete = (%#v, %v)", teams, err)
	}
}

func TestTeamTagStylesCycleAndSurviveUpdates(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()

	created := make([]Team, 0, len(teamTagStyles)+1)
	for index := 0; index <= len(teamTagStyles); index++ {
		team, err := store.CreateTeam(ctx, fmt.Sprintf("Team %02d", index+1), "")
		if err != nil {
			t.Fatalf("CreateTeam(%d): %v", index+1, err)
		}
		created = append(created, team)
	}
	for index, team := range created {
		want := teamTagStyles[index%len(teamTagStyles)]
		if team.TagStyle != want {
			t.Fatalf("team %d tag style = %q, want %q", index+1, team.TagStyle, want)
		}
	}
	updated, err := store.UpdateTeam(ctx, created[4].ID, "Renamed Team", "preserve style")
	if err != nil {
		t.Fatalf("UpdateTeam: %v", err)
	}
	if updated.TagStyle != created[4].TagStyle {
		t.Fatalf("updated tag style = %q, want %q", updated.TagStyle, created[4].TagStyle)
	}
	if _, err := store.DeleteTeam(ctx, created[2].ID); err != nil {
		t.Fatalf("DeleteTeam: %v", err)
	}
	replacement, err := store.CreateTeam(ctx, "Replacement Team", "")
	if err != nil {
		t.Fatalf("CreateTeam replacement: %v", err)
	}
	if replacement.TagStyle != created[2].TagStyle {
		t.Fatalf("replacement tag style = %q, want least-used %q", replacement.TagStyle, created[2].TagStyle)
	}
}

func TestTeamMutationsAreAtomicAndBounded(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	team, err := store.CreateTeam(ctx, "Platform", "")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	missing := "team_missing"
	if _, err := store.SetUserTeams(ctx, []string{"alice@example.com"}, &missing); err == nil {
		t.Fatal("assignment to missing team unexpectedly succeeded")
	} else {
		assertErrorIs(t, err, ErrTeamNotFound)
	}
	classifications, err := store.ReadUserTeams(ctx, []string{"alice@example.com"})
	if err != nil || classifications["alice@example.com"].TeamID != nil {
		t.Fatalf("failed assignment left state = (%#v, %v)", classifications, err)
	}
	teamID := team.ID
	if _, err := store.SetUserTeams(ctx, []string{"alice@example.com"}, &teamID); err != nil {
		t.Fatalf("seed team assignment: %v", err)
	}
	if _, err := store.SetUserTeamsExpected(
		ctx,
		[]string{"alice@example.com", "bob@example.com"},
		nil,
		TeamExpectation{Provided: true},
	); err == nil {
		t.Fatal("stale expected team assignment unexpectedly succeeded")
	} else {
		assertErrorIs(t, err, ErrTeamMembershipConflict)
	}
	classifications, err = store.ReadUserTeams(ctx, []string{"alice@example.com", "bob@example.com"})
	if err != nil || classifications["alice@example.com"].TeamID == nil || classifications["bob@example.com"].TeamID != nil {
		t.Fatalf("conflicted assignment was not atomic = (%#v, %v)", classifications, err)
	}

	users := make([]string, 501)
	for index := range users {
		users[index] = "user" + strings.Repeat("x", index%5) + string(rune(0x1000+index)) + "@example.com"
	}
	if _, err := store.SetUserTeams(ctx, users, &team.ID); err == nil {
		t.Fatal("oversized team assignment unexpectedly succeeded")
	} else {
		assertErrorIs(t, err, ErrInvalidCatalogInput)
	}

	if _, err := store.UpdateTeam(ctx, "missing", "Other", ""); err == nil {
		t.Fatal("missing team update unexpectedly succeeded")
	} else {
		assertErrorIs(t, err, ErrTeamNotFound)
	}
	if _, err := store.CreateTeam(ctx, strings.Repeat("界", 65), ""); err == nil {
		t.Fatal("overlong Unicode team name unexpectedly succeeded")
	} else {
		assertErrorIs(t, err, ErrInvalidCatalogInput)
	}
}

func TestListUsersIsPaginatedFilteredAndNeverReturnsKeyMaterial(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	if err := store.WriteKeyRecords(ctx, []KeyRecord{
		{Label: "alice-alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "Alice@Example.com", Status: "active", Key: "same-external-key", CreatedAt: 10, UpdatedAt: 20},
		{Label: "alice-beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "alice@example.com", Status: "active", Key: "same-external-key", CreatedAt: 11, UpdatedAt: 21},
		{Label: "alice-old", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "alice@example.com", Status: "rotated", Key: "retired-external-key", CreatedAt: 9, UpdatedAt: 30},
		{Label: "bob-old", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "bob@example.com", Status: "revoked", Key: "revoked-external-key", CreatedAt: 12, UpdatedAt: 22},
	}); err != nil {
		t.Fatalf("WriteKeyRecords: %v", err)
	}
	if err := store.WriteRoutes(ctx, map[string]string{"alice@example.com": "beta"}); err != nil {
		t.Fatalf("WriteRoutes: %v", err)
	}
	team, err := store.CreateTeam(ctx, "Platform", "Core platform")
	if err != nil {
		t.Fatalf("CreateTeam: %v", err)
	}
	if _, err := store.SetUserTeams(ctx, []string{"alice@example.com"}, &team.ID); err != nil {
		t.Fatalf("SetUserTeams: %v", err)
	}

	page, err := store.ListUsers(ctx, UserListOptions{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	if page.Total != 2 || page.TotalPages != 1 || len(page.Users) != 2 {
		t.Fatalf("user page = %#v", page)
	}
	if exists, err := store.UserExists(ctx, " ALICE@example.com "); err != nil || !exists {
		t.Fatalf("UserExists(alice) = (%v, %v)", exists, err)
	}
	if exists, err := store.UserExists(ctx, "missing@example.com"); err != nil || exists {
		t.Fatalf("UserExists(missing) = (%v, %v)", exists, err)
	}
	alice := page.Users[0]
	if alice.Email != "alice@example.com" || alice.Status != "active" || alice.ActiveKeys != 1 ||
		alice.ActiveAccounts != 2 || alice.TotalRecords != 3 || alice.RouteAccountID == nil ||
		*alice.RouteAccountID != "beta" || alice.Team == nil || alice.Team.Name != "Platform" ||
		alice.Team.TagStyle != team.TagStyle {
		t.Fatalf("alice summary = %#v", alice)
	}
	if page.Users[1].Status != "inactive" || page.Users[1].TeamID != nil {
		t.Fatalf("bob summary = %#v", page.Users[1])
	}
	all, err := store.ListUserSummaries(ctx)
	if err != nil {
		t.Fatalf("ListUserSummaries: %v", err)
	}
	if len(all) != 2 || all[0].Email != "alice@example.com" || all[0].Team == nil ||
		all[0].Team.Name != "Platform" || all[1].Email != "bob@example.com" ||
		all[1].Team != nil {
		t.Fatalf("complete user summaries = %#v", all)
	}

	filtered, err := store.ListUsers(ctx, UserListOptions{Query: "PLATFORM", PageSize: 25})
	if err != nil || filtered.Total != 1 || filtered.Users[0].Email != alice.Email {
		t.Fatalf("team search = (%#v, %v)", filtered, err)
	}
	unassigned, err := store.ListUsers(ctx, UserListOptions{TeamID: "unassigned", PageSize: 25})
	if err != nil || unassigned.Total != 1 || unassigned.Users[0].Email != "bob@example.com" {
		t.Fatalf("unassigned filter = (%#v, %v)", unassigned, err)
	}
	if _, err := store.ListUsers(ctx, UserListOptions{PageSize: 10}); err == nil {
		t.Fatal("unsupported page size unexpectedly succeeded")
	} else {
		assertErrorIs(t, err, ErrInvalidCatalogInput)
	}
}

func TestBrandingAssetRoundTripAndDelete(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	content := []byte("test-png-content")
	digest := sha256.Sum256(content)

	asset, err := store.WriteBrandingAsset(ctx, "logo", "logo.png", "image/png", content)
	if err != nil {
		t.Fatalf("WriteBrandingAsset: %v", err)
	}
	if asset.SHA256 != hex.EncodeToString(digest[:]) {
		t.Fatalf("branding digest = %s", asset.SHA256)
	}
	content[0] = 'X'
	if string(asset.Content) != "test-png-content" {
		t.Fatal("WriteBrandingAsset retained caller content slice")
	}
	loaded, found, err := store.ReadBrandingAsset(ctx, "logo")
	if err != nil || !found || !reflect.DeepEqual(loaded, asset) {
		t.Fatalf("ReadBrandingAsset = (%#v, %v, %v), want %#v", loaded, found, err, asset)
	}
	if err := store.DeleteBrandingAsset(ctx, "logo"); err != nil {
		t.Fatalf("DeleteBrandingAsset: %v", err)
	}
	if _, found, err := store.ReadBrandingAsset(ctx, "logo"); err != nil || found {
		t.Fatalf("deleted ReadBrandingAsset = (%v, %v)", found, err)
	}
}
