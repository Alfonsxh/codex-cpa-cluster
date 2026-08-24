package controlplane

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUserKeyRotationIsExpectedAndExactlyRollbackable(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []Account{
		{ID: "alpha", Email: "alpha@example.com", Port: 18318, GroupEnabled: true},
		{ID: "beta", Email: "beta@example.com", Port: 18319, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	original := []KeyRecord{
		{Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com", User: "alice@example.com", Status: "active", Key: "old-key", CreatedAt: 100, UpdatedAt: 101},
		{Label: "alice@example.com:beta", Account: "beta", AccountEmail: "beta@example.com", User: "alice@example.com", Status: "active", Key: "old-key", CreatedAt: 100, UpdatedAt: 102},
		{Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com", User: "alice@example.com", Status: "rotated", Key: "older-key", CreatedAt: 50, UpdatedAt: 60},
	}
	if err := store.WriteKeyRecords(ctx, original); err != nil {
		t.Fatalf("seed records: %v", err)
	}
	rotation, err := store.ApplyUserKeyRotationExpected(ctx, "Alice@Example.com", "old-key", "new-key")
	if err != nil {
		t.Fatalf("ApplyUserKeyRotationExpected: %v", err)
	}
	if len(rotation.RotatedRows) != 2 || len(rotation.CreatedRows) != 2 {
		t.Fatalf("rotation = %#v", rotation)
	}
	records, err := store.ReadKeyRecordsForUsers(ctx, []string{"alice@example.com"})
	if err != nil {
		t.Fatalf("read rotated records: %v", err)
	}
	active := 0
	for _, record := range records {
		if record.Status == "active" {
			active++
			if record.Key != "new-key" || record.CreatedAt != time.Unix(1000, 0).Unix() {
				t.Fatalf("active replacement = %#v", record)
			}
		}
	}
	if active != 2 {
		t.Fatalf("active replacement count = %d", active)
	}
	if _, err := store.ApplyUserKeyRotationExpected(ctx, "alice@example.com", "old-key", "another-key"); !errors.Is(err, ErrUserKeyRotationConflict) {
		t.Fatalf("stale rotation error = %v", err)
	}
	if err := store.RestoreUserKeyRotation(ctx, rotation); err != nil {
		t.Fatalf("RestoreUserKeyRotation: %v", err)
	}
	restored, err := store.ReadKeyRecordsForUsers(ctx, []string{"alice@example.com"})
	if err != nil {
		t.Fatalf("read restored records: %v", err)
	}
	if len(restored) != len(original) {
		t.Fatalf("restored row count = %d, want %d", len(restored), len(original))
	}
	for index := range original {
		if restored[index] != original[index] {
			t.Fatalf("restored[%d] = %#v, want %#v", index, restored[index], original[index])
		}
	}
}

func TestUserKeyRotationRejectsDuplicateActiveAccount(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	ctx := context.Background()
	if err := store.WriteKeyRecords(ctx, []KeyRecord{
		{Label: "one", Account: "alpha", User: "alice@example.com", Status: "active", Key: "old", CreatedAt: 1, UpdatedAt: 1},
		{Label: "two", Account: "alpha", User: "alice@example.com", Status: "active", Key: "old", CreatedAt: 1, UpdatedAt: 1},
	}); err != nil {
		t.Fatalf("seed duplicate records: %v", err)
	}
	if _, err := store.ApplyUserKeyRotationExpected(ctx, "alice@example.com", "old", "new"); !errors.Is(err, ErrUserKeyRotationUnsafe) {
		t.Fatalf("duplicate account rotation error = %v", err)
	}
}

func TestUserKeyRotationRejectsIncompleteCurrentAccountMatrix(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []Account{
		{ID: "alpha", Email: "alpha@example.com", Port: 18318, GroupEnabled: true},
		{ID: "beta", Email: "beta@example.com", Port: 18319, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	if err := store.WriteKeyRecords(ctx, []KeyRecord{{
		Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com",
		User: "alice@example.com", Status: "active", Key: "old", CreatedAt: 1, UpdatedAt: 1,
	}}); err != nil {
		t.Fatalf("seed incomplete records: %v", err)
	}
	if _, err := store.ApplyUserKeyRotationExpected(ctx, "alice@example.com", "old", "new"); !errors.Is(err, ErrUserKeyRotationUnsafe) {
		t.Fatalf("incomplete matrix rotation error = %v", err)
	}
	key, err := store.ActiveUserKey(ctx, "alice@example.com")
	if err != nil || key != "old" {
		t.Fatalf("rejected rotation mutated active key = (%q, %v)", key, err)
	}
}
