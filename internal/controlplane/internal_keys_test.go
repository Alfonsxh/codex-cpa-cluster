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
