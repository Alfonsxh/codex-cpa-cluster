package controlplane

import (
	"context"
	"strings"
	"testing"
)

func TestEnsureInternalKeysCreatesStableCredentialsAndDeactivatesRetiredUsers(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	ctx := context.Background()
	first, err := store.EnsureInternalKeys(ctx, []string{"Alice@Example.com", "bob@example.com"})
	if err != nil {
		t.Fatalf("EnsureInternalKeys first: %v", err)
	}
	if len(first) != 2 || !strings.HasPrefix(first["alice@example.com"].Key, "cpa_internal_") {
		t.Fatalf("first internal keys = %#v", first)
	}
	aliceKey := first["alice@example.com"].Key
	second, err := store.EnsureInternalKeys(ctx, []string{"alice@example.com"})
	if err != nil {
		t.Fatalf("EnsureInternalKeys second: %v", err)
	}
	if second["alice@example.com"].Key != aliceKey {
		t.Fatalf("stable Alice key changed: first=%q second=%q", aliceKey, second["alice@example.com"].Key)
	}
	all, err := store.ReadInternalKeys(ctx)
	if err != nil {
		t.Fatalf("ReadInternalKeys: %v", err)
	}
	if all["bob@example.com"].Status != "inactive" {
		t.Fatalf("retired Bob key = %#v", all["bob@example.com"])
	}
}

func TestRestoreInternalKeyChangesOnlyTheTargetUser(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	ctx := context.Background()
	originalAlice := InternalKey{Key: "cpa_internal_alice_original", CreatedAt: 10, Status: "active"}
	originalBob := InternalKey{Key: "cpa_internal_bob_original", CreatedAt: 20, Status: "active"}
	if err := store.WriteInternalKeys(ctx, map[string]InternalKey{
		"alice@example.com": originalAlice,
		"bob@example.com":   originalBob,
	}); err != nil {
		t.Fatalf("seed internal Keys: %v", err)
	}
	aliceBefore, found, err := store.ReadInternalKey(ctx, " Alice@Example.com ")
	if err != nil || !found || aliceBefore != originalAlice {
		t.Fatalf("read Alice internal Key = (%#v, %v, %v)", aliceBefore, found, err)
	}

	concurrentBob := InternalKey{Key: "cpa_internal_bob_concurrent", CreatedAt: 30, Status: "active"}
	mutatedAlice := InternalKey{Key: "cpa_internal_alice_mutated", CreatedAt: 40, Status: "inactive"}
	if err := store.WriteInternalKeys(ctx, map[string]InternalKey{
		"alice@example.com": mutatedAlice,
		"bob@example.com":   concurrentBob,
	}); err != nil {
		t.Fatalf("simulate concurrent internal Key state: %v", err)
	}
	if err := store.RestoreInternalKey(ctx, "alice@example.com", &aliceBefore); err != nil {
		t.Fatalf("restore Alice internal Key: %v", err)
	}
	all, err := store.ReadInternalKeys(ctx)
	if err != nil || all["alice@example.com"] != originalAlice || all["bob@example.com"] != concurrentBob {
		t.Fatalf("targeted restore result = (%#v, %v)", all, err)
	}

	if err := store.RestoreInternalKey(ctx, "alice@example.com", nil); err != nil {
		t.Fatalf("remove Alice internal Key: %v", err)
	}
	all, err = store.ReadInternalKeys(ctx)
	if err != nil || len(all) != 1 || all["bob@example.com"] != concurrentBob {
		t.Fatalf("targeted removal result = (%#v, %v)", all, err)
	}
}
