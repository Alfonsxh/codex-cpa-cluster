package gateway

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestSnapshotLoaderKeepsLastValidStateAndExpiresAuthLease(t *testing.T) {
	directory := t.TempDir()
	writeFixtureSnapshots(t, directory, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	engine := NewEngine()
	loader := NewSnapshotLoader(
		engine,
		SnapshotPathsForDirectory(directory),
		time.Hour,
		zap.NewNop(),
	)
	now := time.Unix(1000, 0)
	loader.now = func() time.Time { return now }
	loader.Refresh()

	if decision := engine.Authorize(now, "Bearer "+fixtureExternalKey, false); !decision.Allowed {
		t.Fatalf("initial authorization denied: %#v", decision)
	}
	if err := os.WriteFile(filepath.Join(directory, "auth-snapshot.json"), []byte("{"), 0o600); err != nil {
		t.Fatalf("corrupt auth snapshot: %v", err)
	}
	now = now.Add(AuthSnapshotMaxAge + time.Second)
	loader.Refresh()

	status := engine.Status()
	if status.Auth.ActiveGeneration != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" ||
		status.Auth.SnapshotLoaderSuccessAt != 1000 {
		t.Fatalf("invalid snapshot replaced last good auth state: %#v", status.Auth)
	}
	assertDenied(t, engine.Authorize(now, "Bearer "+fixtureExternalKey, false), 503, "authentication_snapshot_unavailable")

	writeFixtureSnapshots(t, directory, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	now = now.Add(time.Second)
	loader.Refresh()
	if decision := engine.Authorize(now, "Bearer "+fixtureExternalKey, false); !decision.Allowed {
		t.Fatalf("authorization did not recover: %#v", decision)
	}
	if got := engine.Status().Auth.SnapshotLoaderSuccessAt; got != now.Unix() {
		t.Fatalf("recovered loader success time = %d, want %d", got, now.Unix())
	}
}

func TestSnapshotLoaderDetectsAtomicRenameWithFSNotify(t *testing.T) {
	directory := t.TempDir()
	writeFixtureSnapshots(t, directory, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	engine := NewEngine()
	loader := NewSnapshotLoader(
		engine,
		SnapshotPathsForDirectory(directory),
		time.Hour,
		zap.NewNop(),
	)
	loader.watchReady = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loader.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("loader run: %v", err)
		}
	})
	waitForGeneration(t, engine, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	select {
	case <-loader.watchReady:
	case <-time.After(time.Second):
		t.Fatal("snapshot directory watch did not become ready")
	}

	fixture := loadContractFixture(t)
	var auth map[string]any
	if err := json.Unmarshal(fixture.AuthSnapshot, &auth); err != nil {
		t.Fatalf("decode auth fixture: %v", err)
	}
	auth["generation"] = "dddddddddddddddddddddddddddddddd"
	raw, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("encode auth fixture: %v", err)
	}
	temporary := filepath.Join(directory, "auth-snapshot.next")
	if err := os.WriteFile(temporary, raw, 0o600); err != nil {
		t.Fatalf("write replacement snapshot: %v", err)
	}
	if err := os.Rename(temporary, filepath.Join(directory, "auth-snapshot.json")); err != nil {
		t.Fatalf("replace auth snapshot: %v", err)
	}
	waitForGeneration(t, engine, "dddddddddddddddddddddddddddddddd")
	if previous := engine.Status().Auth.PreviousGeneration; previous != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("previous auth generation = %q", previous)
	}
}

func TestSnapshotLoaderTickerWorksWithoutDirectoryWatch(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "created-after-start")
	engine := NewEngine()
	loader := NewSnapshotLoader(
		engine,
		SnapshotPathsForDirectory(directory),
		20*time.Millisecond,
		zap.NewNop(),
	)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- loader.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("loader run: %v", err)
		}
	})

	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create snapshot directory: %v", err)
	}
	writeFixtureSnapshots(t, directory, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	waitForGeneration(t, engine, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
}

func writeFixtureSnapshots(t *testing.T, directory string, authGeneration string) {
	t.Helper()
	fixture := loadContractFixture(t)
	var auth map[string]any
	if err := json.Unmarshal(fixture.AuthSnapshot, &auth); err != nil {
		t.Fatalf("decode auth fixture: %v", err)
	}
	auth["generation"] = authGeneration
	authRaw, err := json.Marshal(auth)
	if err != nil {
		t.Fatalf("encode auth fixture: %v", err)
	}
	files := map[string][]byte{
		"auth-snapshot.json":   authRaw,
		"quota-snapshot.json":  fixture.QuotaSnapshot,
		"quota-heartbeat.json": fixture.QuotaHeartbeat,
	}
	for name, raw := range files {
		if err := os.WriteFile(filepath.Join(directory, name), raw, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
}

func waitForGeneration(t *testing.T, engine *Engine, generation string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if engine.Status().Auth.ActiveGeneration == generation {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("auth generation did not become %s; status=%#v", generation, engine.Status().Auth)
}
