package controlplane

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestLeaseTakeRenewReleaseAndFenceStaleOwner(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_000, 0)
	store := openLeaseTestStore(t, t.TempDir(), func() time.Time { return now })
	defer store.Close()

	lease, err := store.TakeLease(ctx, "usage-collector", "go-v2:first", 30*time.Second)
	if err != nil {
		t.Fatalf("TakeLease: %v", err)
	}
	if lease.Generation != 1 || lease.ExpiresAt != 1_030 || lease.Token == "" {
		t.Fatalf("initial lease = %#v", lease)
	}
	if _, err := store.TakeLease(ctx, "usage-collector", "go-v2:second", 30*time.Second); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("second TakeLease error = %v, want ErrLeaseHeld", err)
	}

	now = now.Add(10 * time.Second)
	renewed, err := store.RenewLease(ctx, lease, 30*time.Second)
	if err != nil || renewed.ExpiresAt != 1_040 {
		t.Fatalf("RenewLease = (%#v, %v)", renewed, err)
	}
	if err := store.ReleaseLease(ctx, renewed); err != nil {
		t.Fatalf("ReleaseLease: %v", err)
	}
	if _, err := store.RenewLease(ctx, renewed, 30*time.Second); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale RenewLease error = %v, want ErrLeaseLost", err)
	}

	replacement, err := store.TakeLease(ctx, "usage-collector", "go-v2:second", 30*time.Second)
	if err != nil {
		t.Fatalf("replacement TakeLease: %v", err)
	}
	if replacement.Generation != 2 || replacement.Token == renewed.Token {
		t.Fatalf("replacement lease = %#v", replacement)
	}
}

func TestLeaseExpiresBeforeTakeoverAndCannotBeRevived(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(2_000, 0)
	store := openLeaseTestStore(t, t.TempDir(), func() time.Time { return now })
	defer store.Close()

	first, err := store.TakeLease(ctx, "notifications", "v1", 5*time.Second)
	if err != nil {
		t.Fatalf("TakeLease: %v", err)
	}
	now = now.Add(5 * time.Second)
	second, err := store.TakeLease(ctx, "notifications", "go-v2", 10*time.Second)
	if err != nil {
		t.Fatalf("take expired lease: %v", err)
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("replacement generation = %d, want %d", second.Generation, first.Generation+1)
	}
	if _, err := store.RenewLease(ctx, first, 10*time.Second); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("expired owner RenewLease error = %v, want ErrLeaseLost", err)
	}
}

func TestJoinLeaseRequiresExplicitActiveOwner(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(3_000, 0)
	store := openLeaseTestStore(t, t.TempDir(), func() time.Time { return now })
	defer store.Close()

	if _, err := store.JoinLease(ctx, "runtime-writer", "go-v2", 30*time.Second); !errors.Is(err, ErrLeaseMissing) {
		t.Fatalf("empty JoinLease error = %v, want ErrLeaseMissing", err)
	}
	active, err := store.TakeLease(ctx, "runtime-writer", "go-v2", 30*time.Second)
	if err != nil {
		t.Fatalf("TakeLease: %v", err)
	}
	now = now.Add(5 * time.Second)
	joined, err := store.JoinLease(ctx, "runtime-writer", "go-v2", 30*time.Second)
	if err != nil {
		t.Fatalf("JoinLease: %v", err)
	}
	if joined.Token != active.Token || joined.Generation != active.Generation || joined.ExpiresAt != 3_035 {
		t.Fatalf("joined lease = %#v, active = %#v", joined, active)
	}
	if _, err := store.JoinLease(ctx, "runtime-writer", "v1", 30*time.Second); !errors.Is(err, ErrLeaseHeld) {
		t.Fatalf("foreign JoinLease error = %v, want ErrLeaseHeld", err)
	}
}

func TestLeaseCorruptionFailsClosed(t *testing.T) {
	ctx := context.Background()
	store := openLeaseTestStore(t, t.TempDir(), func() time.Time { return time.Unix(4_000, 0) })
	defer store.Close()
	if err := store.WriteRuntimeState(ctx, "ownership_lease:quota", map[string]any{"version": 99}); err != nil {
		t.Fatalf("seed corrupt lease: %v", err)
	}
	if _, err := store.TakeLease(ctx, "quota", "go-v2", 30*time.Second); !errors.Is(err, ErrLeaseStateInvalid) {
		t.Fatalf("TakeLease corrupt error = %v, want ErrLeaseStateInvalid", err)
	}
}

func TestConcurrentLeaseTakeHasSingleWinner(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := func() time.Time { return time.Unix(5_000, 0) }
	firstStore := openLeaseTestStore(t, root, now)
	defer firstStore.Close()
	secondStore := openLeaseTestStore(t, root, now)
	defer secondStore.Close()

	stores := []*Store{firstStore, secondStore}
	start := make(chan struct{})
	errorsByWorker := make([]error, len(stores))
	var wait sync.WaitGroup
	for index, store := range stores {
		wait.Add(1)
		go func(index int, store *Store) {
			defer wait.Done()
			<-start
			_, errorsByWorker[index] = store.TakeLease(
				ctx,
				"log-maintenance",
				"go-v2:worker",
				30*time.Second,
			)
		}(index, store)
	}
	close(start)
	wait.Wait()

	succeeded := 0
	held := 0
	for _, err := range errorsByWorker {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrLeaseHeld):
			held++
		default:
			t.Fatalf("unexpected TakeLease error: %v", err)
		}
	}
	if succeeded != 1 || held != 1 {
		t.Fatalf("concurrent results = success %d held %d errors %#v", succeeded, held, errorsByWorker)
	}
}

func TestInstalledWriteFenceRejectsStaleGenerationAtomically(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	now := time.Unix(6_000, 0)
	clock := func() time.Time { return now }
	stale := openLeaseTestStore(t, root, clock)
	defer stale.Close()
	runtimeLease, err := stale.TakeLease(ctx, "runtime-writer", "go-v2", 5*time.Second)
	if err != nil {
		t.Fatalf("take runtime lease: %v", err)
	}
	workerLease, err := stale.TakeLease(ctx, "admin", "go-v2:admin", 5*time.Second)
	if err != nil {
		t.Fatalf("take worker lease: %v", err)
	}
	if err := stale.InstallWriteFence(runtimeLease, workerLease); err != nil {
		t.Fatalf("install write fence: %v", err)
	}
	if err := stale.WriteRuntimeState(ctx, "fenced-business-state", map[string]any{"owner": "old"}); err != nil {
		t.Fatalf("write while owned: %v", err)
	}
	externalWrites := 0
	if err := stale.WithWriteFence(ctx, func() error {
		externalWrites++
		_, err := stale.ReadSettings(ctx)
		return err
	}); err != nil {
		t.Fatalf("external write with control-plane read while owned: %v", err)
	}

	now = now.Add(5 * time.Second)
	replacement := openLeaseTestStore(t, root, clock)
	defer replacement.Close()
	newRuntime, err := replacement.TakeLease(ctx, "runtime-writer", "go-v2-next", 30*time.Second)
	if err != nil {
		t.Fatalf("replace runtime lease: %v", err)
	}
	newWorker, err := replacement.TakeLease(ctx, "admin", "go-v2-next:admin", 30*time.Second)
	if err != nil {
		t.Fatalf("replace worker lease: %v", err)
	}
	if err := replacement.InstallWriteFence(newRuntime, newWorker); err != nil {
		t.Fatalf("install replacement fence: %v", err)
	}

	err = stale.WriteRuntimeState(ctx, "fenced-business-state", map[string]any{"owner": "stale"})
	if !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale write error = %v, want ErrLeaseLost", err)
	}
	err = stale.WithWriteFence(ctx, func() error {
		externalWrites++
		return nil
	})
	if !errors.Is(err, ErrLeaseLost) || externalWrites != 1 {
		t.Fatalf("stale external write = error %v writes %d", err, externalWrites)
	}
	var state map[string]any
	found, err := replacement.ReadRuntimeState(ctx, "fenced-business-state", &state)
	if err != nil || !found || state["owner"] != "old" {
		t.Fatalf("state after stale write = (%v, %#v, %v)", found, state, err)
	}
	if err := replacement.WriteRuntimeState(ctx, "fenced-business-state", map[string]any{"owner": "new"}); err != nil {
		t.Fatalf("replacement write: %v", err)
	}
}

func openLeaseTestStore(t *testing.T, root string, now func() time.Time) *Store {
	t.Helper()
	store, err := Open(context.Background(), root, Options{Now: now})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}
