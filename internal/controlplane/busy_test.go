package controlplane

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestFencedControlWriteWaitsForSQLiteBusyAndCommitsOnce(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	store := openTestStore(t, root)
	defer store.Close()
	runtimeLease, err := store.TakeLease(ctx, "runtime-writer", "go-v2", 30*time.Second)
	if err != nil {
		t.Fatalf("take runtime lease: %v", err)
	}
	workerLease, err := store.TakeLease(ctx, "admin", "go-v2:admin", 30*time.Second)
	if err != nil {
		t.Fatalf("take Admin lease: %v", err)
	}
	if err := store.InstallWriteFence(runtimeLease, workerLease); err != nil {
		t.Fatalf("install write fence: %v", err)
	}

	blocker, err := sql.Open("sqlite", store.Path())
	if err != nil {
		t.Fatalf("open external SQLite blocker: %v", err)
	}
	defer blocker.Close()
	if _, err := blocker.ExecContext(ctx, "PRAGMA busy_timeout = 1000"); err != nil {
		t.Fatalf("configure external SQLite blocker: %v", err)
	}
	if _, err := blocker.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin external SQLite write: %v", err)
	}
	readContext, cancelRead := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancelRead()
	if _, _, err := store.ReadMetadata(readContext, "missing-during-busy"); err != nil {
		t.Fatalf("WAL read was blocked by independent writer: %v", err)
	}

	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		done <- store.WriteMetadata(ctx, "busy-rehearsal", "committed")
	}()
	<-started
	select {
	case err := <-done:
		t.Fatalf("fenced write did not wait for SQLite busy state: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := blocker.ExecContext(ctx, "COMMIT"); err != nil {
		t.Fatalf("release external SQLite write: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("fenced write after SQLite busy recovery: %v", err)
	}
	value, found, err := store.ReadMetadata(ctx, "busy-rehearsal")
	if err != nil || !found || value != "committed" {
		t.Fatalf("busy recovery result = (%q, %v, %v)", value, found, err)
	}
}
