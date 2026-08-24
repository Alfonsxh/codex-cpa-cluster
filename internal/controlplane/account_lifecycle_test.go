package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAccountCreationPreservesUnifiedAPIKeyBytesAndRollsBackExactly(t *testing.T) {
	store := newAccountLifecycleTestStore(t)
	ctx := context.Background()
	creation, err := store.ApplyAccountCreation(ctx, Account{
		ID: "Gamma", Email: "Gamma@Accounts.Example.com", Port: 18320,
		ProxyMode: "direct",
	})
	if err != nil {
		t.Fatalf("ApplyAccountCreation: %v", err)
	}
	if creation.Account.ID != "gamma" || creation.Account.Email != "gamma@accounts.example.com" ||
		creation.Account.DefaultGroup || !creation.Account.GroupEnabled || len(creation.CreatedRows) != 2 {
		t.Fatalf("creation = %#v", creation)
	}
	for _, row := range creation.CreatedRows {
		want := map[string]string{
			"alice@example.com": "cpa_test_alice", "bob@example.com": "cpa_test_bob",
		}[row.User]
		if row.Account != "gamma" || row.AccountEmail != "gamma@accounts.example.com" ||
			row.Key != want || row.Status != "active" {
			t.Fatalf("created row = %#v", row)
		}
	}
	routes, err := store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "alpha" || routes["bob@example.com"] != "beta" {
		t.Fatalf("routes after create = (%#v, %v)", routes, err)
	}
	if err := store.RestoreAccountCreation(ctx, creation); err != nil {
		t.Fatalf("RestoreAccountCreation: %v", err)
	}
	accounts, err := store.ReadAccounts(ctx)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts after rollback = (%#v, %v)", accounts, err)
	}
	records, err := store.ReadKeyRecords(ctx)
	if err != nil || len(records) != 4 {
		t.Fatalf("records after rollback = (%#v, %v)", records, err)
	}
}

func TestAccountCreationRejectsDivergentActiveUserKeysWithoutPartialWrite(t *testing.T) {
	store := newAccountLifecycleTestStore(t)
	ctx := context.Background()
	records, err := store.ReadKeyRecords(ctx)
	if err != nil {
		t.Fatalf("ReadKeyRecords: %v", err)
	}
	records[1].Key = "cpa_test_alice_divergent"
	if err := store.WriteKeyRecords(ctx, records); err != nil {
		t.Fatalf("seed divergent key: %v", err)
	}
	_, err = store.ApplyAccountCreation(ctx, Account{
		ID: "gamma", Email: "gamma@accounts.example.com", Port: 18320,
	})
	if !errors.Is(err, ErrAccountLifecycleConflict) {
		t.Fatalf("creation error = %v", err)
	}
	accounts, readErr := store.ReadAccounts(ctx)
	if readErr != nil || len(accounts) != 2 {
		t.Fatalf("accounts after rejected creation = (%#v, %v)", accounts, readErr)
	}
}

func TestAccountUpdateRenamesDisablesReroutesAndRestoresWithoutRotatingKeys(t *testing.T) {
	store := newAccountLifecycleTestStore(t)
	ctx := context.Background()
	disabled := false
	notDefault := false
	update, err := store.ApplyAccountUpdate(ctx, AccountUpdateRequest{
		AccountID: "alpha", NewAccountID: "gamma",
		Email: "gamma@accounts.example.com", ProxyMode: "custom",
		GroupEnabled: &disabled, DefaultGroup: &notDefault, FallbackAccount: "beta",
	})
	if err != nil {
		t.Fatalf("ApplyAccountUpdate: %v", err)
	}
	if update.Before.ID != "alpha" || update.After.ID != "gamma" ||
		update.After.Email != "gamma@accounts.example.com" || update.After.ProxyMode != "custom" ||
		update.After.GroupEnabled || update.After.DefaultGroup || len(update.Routes) != 1 {
		t.Fatalf("update = %#v", update)
	}
	accounts, err := store.ReadAccounts(ctx)
	if err != nil || len(accounts) != 2 || accounts[0].ID != "gamma" || accounts[1].ID != "beta" || !accounts[1].DefaultGroup {
		t.Fatalf("accounts after update = (%#v, %v)", accounts, err)
	}
	routes, err := store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "beta" || routes["bob@example.com"] != "beta" {
		t.Fatalf("routes after update = (%#v, %v)", routes, err)
	}
	records, err := store.ReadKeyRecords(ctx)
	if err != nil {
		t.Fatalf("ReadKeyRecords: %v", err)
	}
	for _, record := range records {
		if record.Account != "gamma" {
			continue
		}
		want := map[string]string{
			"alice@example.com": "cpa_test_alice", "bob@example.com": "cpa_test_bob",
		}[record.User]
		if record.AccountEmail != "gamma@accounts.example.com" || record.Label != record.User+":gamma" || record.Key != want {
			t.Fatalf("updated account Key row = %#v", record)
		}
	}
	if err := store.RestoreAccountUpdate(ctx, update); err != nil {
		t.Fatalf("RestoreAccountUpdate: %v", err)
	}
	accounts, err = store.ReadAccounts(ctx)
	if err != nil || len(accounts) != 2 || accounts[0].ID != "alpha" || !accounts[0].DefaultGroup || accounts[1].DefaultGroup {
		t.Fatalf("accounts after update rollback = (%#v, %v)", accounts, err)
	}
	routes, err = store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "alpha" || routes["bob@example.com"] != "beta" {
		t.Fatalf("routes after update rollback = (%#v, %v)", routes, err)
	}
	records, err = store.ReadKeyRecords(ctx)
	if err != nil || len(records) != 4 {
		t.Fatalf("records after update rollback = (%#v, %v)", records, err)
	}
	for _, record := range records {
		if record.Account == "alpha" && (record.AccountEmail != "alpha@accounts.example.com" || record.Label != record.User+":alpha") {
			t.Fatalf("restored account Key row = %#v", record)
		}
	}
}

func TestAccountUpdateRejectsDisablingLastEnabledAccountAtomically(t *testing.T) {
	store := newAccountLifecycleTestStore(t)
	ctx := context.Background()
	accounts, err := store.ReadAccounts(ctx)
	if err != nil {
		t.Fatalf("ReadAccounts: %v", err)
	}
	accounts[1].GroupEnabled = false
	if err := store.WriteAccounts(ctx, accounts); err != nil {
		t.Fatalf("disable beta: %v", err)
	}
	disabled := false
	notDefault := false
	_, err = store.ApplyAccountUpdate(ctx, AccountUpdateRequest{
		AccountID: "alpha", Email: "alpha@accounts.example.com", ProxyMode: "inherit",
		GroupEnabled: &disabled, DefaultGroup: &notDefault,
	})
	if !errors.Is(err, ErrAccountDeleteNeedsFallback) {
		t.Fatalf("last-enabled update error = %v", err)
	}
	accounts, readErr := store.ReadAccounts(ctx)
	if readErr != nil || !accounts[0].GroupEnabled || accounts[0].ID != "alpha" {
		t.Fatalf("last-enabled account changed = (%#v, %v)", accounts, readErr)
	}
}

func TestAccountDeletionRequiresExplicitExclusiveKeyRevocationAndRestores(t *testing.T) {
	store := newAccountLifecycleTestStore(t)
	ctx := context.Background()
	records, err := store.ReadKeyRecords(ctx)
	if err != nil {
		t.Fatalf("ReadKeyRecords: %v", err)
	}
	// Bob's beta row is revoked, so deleting alpha would invalidate Bob's
	// only active API Key and must require explicit confirmation.
	for index := range records {
		if records[index].User == "bob@example.com" && records[index].Account == "beta" {
			records[index].Status = "revoked"
		}
	}
	if err := store.WriteKeyRecords(ctx, records); err != nil {
		t.Fatalf("seed exclusive key: %v", err)
	}
	if _, err := store.ApplyAccountDeletion(ctx, "alpha", "beta", false); !errors.Is(err, ErrAccountDeleteRequiresRevoke) {
		t.Fatalf("delete without revoke error = %v", err)
	}

	deletion, err := store.ApplyAccountDeletion(ctx, "alpha", "beta", true)
	if err != nil {
		t.Fatalf("ApplyAccountDeletion: %v", err)
	}
	if deletion.Account.ID != "alpha" || deletion.FallbackAccount != "beta" ||
		deletion.RevokedExclusiveKeys != 1 || len(deletion.Routes) != 1 || len(deletion.Rows) != 2 {
		t.Fatalf("deletion = %#v", deletion)
	}
	accounts, err := store.ReadAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].ID != "beta" || !accounts[0].DefaultGroup {
		t.Fatalf("accounts after delete = (%#v, %v)", accounts, err)
	}
	routes, err := store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "beta" || routes["bob@example.com"] != "beta" {
		t.Fatalf("routes after delete = (%#v, %v)", routes, err)
	}
	if err := store.RestoreAccountDeletion(ctx, deletion); err != nil {
		t.Fatalf("RestoreAccountDeletion: %v", err)
	}
	accounts, err = store.ReadAccounts(ctx)
	if err != nil || len(accounts) != 2 || accounts[0].ID != "alpha" || !accounts[0].DefaultGroup || accounts[1].DefaultGroup {
		t.Fatalf("accounts after restore = (%#v, %v)", accounts, err)
	}
	routes, err = store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "alpha" || routes["bob@example.com"] != "beta" {
		t.Fatalf("routes after restore = (%#v, %v)", routes, err)
	}
	restored, err := store.ReadKeyRecords(ctx)
	if err != nil || len(restored) != 4 {
		t.Fatalf("records after restore = (%#v, %v)", restored, err)
	}
}

func TestAccountDeletionRollbackFailsClosedAfterRouteDrift(t *testing.T) {
	store := newAccountLifecycleTestStore(t)
	ctx := context.Background()
	deletion, err := store.ApplyAccountDeletion(ctx, "alpha", "beta", true)
	if err != nil {
		t.Fatalf("ApplyAccountDeletion: %v", err)
	}
	if err := store.WriteRoutes(ctx, map[string]string{
		"alice@example.com": "unexpected", "bob@example.com": "beta",
	}); err != nil {
		t.Fatalf("drift route: %v", err)
	}
	if err := store.RestoreAccountDeletion(ctx, deletion); !errors.Is(err, ErrAccountLifecycleConflict) {
		t.Fatalf("rollback drift error = %v", err)
	}
	accounts, err := store.ReadAccounts(ctx)
	if err != nil || len(accounts) != 1 || accounts[0].ID != "beta" {
		t.Fatalf("account was partially restored = (%#v, %v)", accounts, err)
	}
}

func TestAccountLifecycleNormalization(t *testing.T) {
	for _, value := range []string{"a", "admin", "Alpha_One", "1alpha", "alpha-" + string(make([]byte, 40))} {
		if _, err := NormalizeAccountID(value); err == nil {
			t.Fatalf("invalid account ID %q was accepted", value)
		}
	}
	for _, value := range []string{"missing-at", "a@localhost", "Display <a@example.com>", "a b@example.com"} {
		if _, err := NormalizeAccountEmail(value); err == nil {
			t.Fatalf("invalid account email %q was accepted", value)
		}
	}
	if id, err := NormalizeAccountID(" Alpha-01 "); err != nil || id != "alpha-01" {
		t.Fatalf("normalized account ID = (%q, %v)", id, err)
	}
}

func newAccountLifecycleTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), t.TempDir(), Options{
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, ProxyMode: "inherit", CreatedAt: 10, GroupEnabled: true, DefaultGroup: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18319, ProxyMode: "direct", CreatedAt: 20, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("WriteAccounts: %v", err)
	}
	if err := store.WriteKeyRecords(ctx, []KeyRecord{
		{Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "alice@example.com", Status: "active", Key: "cpa_test_alice", CreatedAt: 100, UpdatedAt: 100},
		{Label: "alice@example.com:beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "alice@example.com", Status: "active", Key: "cpa_test_alice", CreatedAt: 100, UpdatedAt: 100},
		{Label: "bob@example.com:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "bob@example.com", Status: "active", Key: "cpa_test_bob", CreatedAt: 100, UpdatedAt: 100},
		{Label: "bob@example.com:beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "bob@example.com", Status: "active", Key: "cpa_test_bob", CreatedAt: 100, UpdatedAt: 100},
	}); err != nil {
		t.Fatalf("WriteKeyRecords: %v", err)
	}
	if err := store.WriteRoutes(ctx, map[string]string{
		"alice@example.com": "alpha", "bob@example.com": "beta",
	}); err != nil {
		t.Fatalf("WriteRoutes: %v", err)
	}
	return store
}
