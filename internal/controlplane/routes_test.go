package controlplane

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestApplyRoutesExpectedUpdatesWholeBatchAtomically(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	seedRouteUsers(t, store)
	if err := store.WriteRoutes(ctx, map[string]string{
		"alice@example.com": "alpha",
		"bob@example.com":   "alpha",
	}); err != nil {
		t.Fatalf("seed routes: %v", err)
	}
	result, err := store.ApplyRoutesExpected(ctx, map[string]string{
		"alice@example.com": "beta",
		"bob@example.com":   "beta",
	}, map[string]string{
		"alice@example.com": "alpha",
		"bob@example.com":   "alpha",
	})
	if err != nil {
		t.Fatalf("ApplyRoutesExpected: %v", err)
	}
	if result.MovedUsers != 2 || !reflect.DeepEqual(result.Destinations, map[string]int{"beta": 2}) {
		t.Fatalf("route result = %#v", result)
	}
	routes, err := store.ReadRoutes(ctx)
	if err != nil || routes["alice@example.com"] != "beta" || routes["bob@example.com"] != "beta" {
		t.Fatalf("updated routes = (%#v, %v)", routes, err)
	}
}

func TestApplyRoutesExpectedRejectsWholeBatchOnConflict(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	seedRouteUsers(t, store)
	original := map[string]string{
		"alice@example.com": "alpha",
		"bob@example.com":   "beta",
	}
	if err := store.WriteRoutes(ctx, original); err != nil {
		t.Fatalf("seed routes: %v", err)
	}
	_, err := store.ApplyRoutesExpected(ctx, map[string]string{
		"alice@example.com": "beta",
		"bob@example.com":   "alpha",
	}, map[string]string{
		"alice@example.com": "alpha",
		"bob@example.com":   "alpha",
	})
	if !errors.Is(err, ErrRouteConflict) {
		t.Fatalf("route conflict error = %v", err)
	}
	routes, readError := store.ReadRoutes(ctx)
	if readError != nil || !reflect.DeepEqual(routes, original) {
		t.Fatalf("routes after conflict = (%#v, %v), want %#v", routes, readError, original)
	}
}

func TestApplyRoutesExpectedRejectsUnknownTargetsAndUsers(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	seedRouteUsers(t, store)
	if _, err := store.ApplyRoutesExpected(ctx,
		map[string]string{"alice@example.com": "missing"},
		map[string]string{"alice@example.com": ""},
	); !errors.Is(err, ErrRouteTargetNotFound) {
		t.Fatalf("unknown route target error = %v", err)
	}
	if _, err := store.ApplyRoutesExpected(ctx,
		map[string]string{"missing@example.com": "alpha"},
		map[string]string{"missing@example.com": ""},
	); !errors.Is(err, ErrRouteUserNotFound) {
		t.Fatalf("unknown route user error = %v", err)
	}
}

func TestRestoreRoutesExpectedCanRemovePreviouslyUnboundRoute(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	seedRouteUsers(t, store)
	if _, err := store.ApplyRoutesExpected(ctx,
		map[string]string{"alice@example.com": "alpha"},
		map[string]string{"alice@example.com": ""},
	); err != nil {
		t.Fatalf("assign unbound user: %v", err)
	}
	if err := store.RestoreRoutesExpected(ctx,
		map[string]string{"alice@example.com": ""},
		map[string]string{"alice@example.com": "alpha"},
	); err != nil {
		t.Fatalf("RestoreRoutesExpected: %v", err)
	}
	routes, err := store.ReadRoutes(ctx)
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes after rollback = (%#v, %v)", routes, err)
	}
}

func seedRouteUsers(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, CreatedAt: 1, GroupEnabled: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18319, CreatedAt: 1, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	if err := store.WriteKeyRecords(ctx, []KeyRecord{
		{Label: "alice:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "alice@example.com", Status: "active", Key: "key-alice", CreatedAt: 1, UpdatedAt: 1},
		{Label: "alice:beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "alice@example.com", Status: "active", Key: "key-alice", CreatedAt: 1, UpdatedAt: 1},
		{Label: "bob:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "bob@example.com", Status: "active", Key: "key-bob", CreatedAt: 1, UpdatedAt: 1},
		{Label: "bob:beta", Account: "beta", AccountEmail: "beta@accounts.example.com", User: "bob@example.com", Status: "active", Key: "key-bob", CreatedAt: 1, UpdatedAt: 1},
	}); err != nil {
		t.Fatalf("seed key records: %v", err)
	}
}
