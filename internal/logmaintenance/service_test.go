package logmaintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestServiceCopyTruncatesAndBoundsBackups(t *testing.T) {
	ctx := context.Background()
	root, store := newTestServiceStore(t)
	path := filepath.Join(root, "logs", "gateway", "access.tsv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create log directory: %v", err)
	}
	service, err := New(Config{
		Root: root, Store: store, Fence: directWriteFence{}, MaxFileSizeMB: 1, Backups: 2,
		Now: func() time.Time { return time.Unix(100, 0) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	writeLargeLog(t, path, 'a', megabyte+1)
	first, err := service.RunOnce(ctx)
	if err != nil {
		t.Fatalf("first RunOnce: %v", err)
	}
	assertFileSize(t, path, 0)
	assertFileSize(t, path+".1", megabyte+1)
	if len(first.LastRotated) != 1 || first.LastRotated[0] != "logs/gateway/access.tsv" || first.Rotations != 1 {
		t.Fatalf("first state = %#v", first)
	}
	if !Healthy(first, true, time.Unix(101, 0), 5*time.Minute) {
		t.Fatal("fresh successful state is unhealthy")
	}

	writeLargeLog(t, path, 'b', megabyte+2)
	if _, err := service.RunOnce(ctx); err != nil {
		t.Fatalf("second RunOnce: %v", err)
	}
	writeLargeLog(t, path, 'c', megabyte+3)
	third, err := service.RunOnce(ctx)
	if err != nil {
		t.Fatalf("third RunOnce: %v", err)
	}
	assertFirstByte(t, path+".1", 'c')
	assertFirstByte(t, path+".2", 'b')
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("unexpected third backup: %v", err)
	}
	if third.Rotations != 3 {
		t.Fatalf("rotation count = %d", third.Rotations)
	}
	loaded, found, err := ReadState(ctx, store)
	if err != nil || !found || loaded.Rotations != 3 {
		t.Fatalf("stored state = (%#v, %v, %v)", loaded, found, err)
	}
}

func TestServiceLeavesSmallAndMissingLogsUntouched(t *testing.T) {
	root, store := newTestServiceStore(t)
	path := filepath.Join(root, "logs", "admin", "audit.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create log directory: %v", err)
	}
	if err := os.WriteFile(path, []byte("ok\n"), 0o640); err != nil {
		t.Fatalf("write log: %v", err)
	}
	service, err := New(Config{
		Root: root, Store: store, Fence: directWriteFence{}, MaxFileSizeMB: 1, Backups: 2,
		Now: func() time.Time { return time.Unix(200, 0) },
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	state, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "ok\n" {
		t.Fatalf("small log = (%q, %v)", contents, err)
	}
	if len(state.LastRotated) != 0 || state.LastError != "" {
		t.Fatalf("state = %#v", state)
	}
	if Healthy(state, true, time.Unix(501, 0), 5*time.Minute) {
		t.Fatal("stale state reported healthy")
	}
	if Healthy(state, true, time.Unix(199, 0), 5*time.Minute) {
		t.Fatal("future heartbeat reported healthy")
	}
}

func TestServiceRejectsSymlinkTargetAndParentWithoutTouchingOutsideFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink semantics differ on Windows")
	}
	root, store := newTestServiceStore(t)
	outside := t.TempDir()
	outPath := filepath.Join(outside, "access.tsv")
	writeLargeLog(t, outPath, 'x', megabyte+1)
	if err := os.MkdirAll(filepath.Join(root, "logs"), 0o755); err != nil {
		t.Fatalf("create logs root: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "logs", "gateway")); err != nil {
		t.Fatalf("create parent symlink: %v", err)
	}
	service, err := New(Config{
		Root: root, Store: store, Fence: directWriteFence{}, MaxFileSizeMB: 1, Backups: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	state, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !strings.Contains(state.LastError, "refusing symlink") {
		t.Fatalf("last error = %q", state.LastError)
	}
	assertFileSize(t, outPath, megabyte+1)
	if _, err := os.Stat(outPath + ".1"); !os.IsNotExist(err) {
		t.Fatalf("outside backup was created: %v", err)
	}
	if Healthy(state, true, time.Now(), 5*time.Minute) {
		t.Fatal("error state reported healthy")
	}
}

func TestRotateFileRejectsNonRegularTargetAndBackupSymlink(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "directory.log")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatalf("create directory target: %v", err)
	}
	if _, err := RotateFile(directory, 1, 2); err == nil {
		t.Fatal("RotateFile accepted a directory")
	}

	path := filepath.Join(root, "access.tsv")
	writeLargeLog(t, path, 'a', 8)
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("safe"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, path+".1"); err != nil {
		t.Fatalf("create backup symlink: %v", err)
	}
	if _, err := RotateFile(path, 1, 2); err == nil {
		t.Fatal("RotateFile accepted a symlink backup")
	}
	assertFileSize(t, path, 8)
	contents, err := os.ReadFile(outside)
	if err != nil || string(contents) != "safe" {
		t.Fatalf("outside file = (%q, %v)", contents, err)
	}
}

func TestRotateFilePreservesWriterInode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.tsv")
	writer, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		t.Fatalf("open append writer: %v", err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte("before-rotation")); err != nil {
		t.Fatalf("write before rotation: %v", err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatalf("sync before rotation: %v", err)
	}
	rotated, err := RotateFile(path, 1, 2)
	if err != nil || !rotated {
		t.Fatalf("RotateFile = (%v, %v)", rotated, err)
	}
	if _, err := writer.Write([]byte("after-rotation")); err != nil {
		t.Fatalf("write through existing descriptor: %v", err)
	}
	if err := writer.Sync(); err != nil {
		t.Fatalf("sync after rotation: %v", err)
	}
	active, err := os.ReadFile(path)
	if err != nil || string(active) != "after-rotation" {
		t.Fatalf("active log = (%q, %v)", active, err)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil || string(backup) != "before-rotation" {
		t.Fatalf("backup log = (%q, %v)", backup, err)
	}
}

func TestStateJSONMatchesPythonRuntimeContract(t *testing.T) {
	raw, err := json.Marshal(State{
		HeartbeatAt: 100, LastError: "", Rotations: 2, LastRotated: []string{"logs/gateway/access.tsv"},
		MaxFileSizeMB: 32, Backups: 2,
	})
	if err != nil {
		t.Fatalf("marshal state: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal state: %v", err)
	}
	expected := []string{"heartbeat_at", "last_error", "rotations", "last_rotated", "max_file_size_mb", "backups"}
	if len(payload) != len(expected) {
		t.Fatalf("state keys = %#v", payload)
	}
	for _, key := range expected {
		if _, found := payload[key]; !found {
			t.Fatalf("state is missing Python-compatible key %q: %s", key, raw)
		}
	}
}

func TestServiceResetsNonObjectLegacyState(t *testing.T) {
	root, store := newTestServiceStore(t)
	if err := store.WriteRuntimeState(context.Background(), RuntimeStateName, []string{"invalid"}); err != nil {
		t.Fatalf("write invalid legacy state: %v", err)
	}
	service, err := New(Config{
		Root: root, Store: store, Fence: directWriteFence{}, MaxFileSizeMB: 1, Backups: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	state, err := service.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if state.Rotations != 0 || state.LastRotated == nil {
		t.Fatalf("reset state = %#v", state)
	}
}

func TestNewRejectsTargetsOutsideLogs(t *testing.T) {
	root, store := newTestServiceStore(t)
	for _, target := range []string{"../outside.log", "logs/../outside.log", "/tmp/outside.log"} {
		if _, err := New(Config{
			Root: root, Store: store, Fence: directWriteFence{},
			MaxFileSizeMB: 1, Backups: 2, Targets: []string{target},
		}); err == nil {
			t.Fatalf("New accepted target %q", target)
		}
	}
}

func TestNewBoundsBackupCount(t *testing.T) {
	root, store := newTestServiceStore(t)
	for _, backups := range []int{0, maximumBackups + 1} {
		if _, err := New(Config{
			Root: root, Store: store, Fence: directWriteFence{}, MaxFileSizeMB: 1, Backups: backups,
		}); err == nil {
			t.Fatalf("New accepted backup count %d", backups)
		}
	}
}

func TestServiceRejectsStaleLeaseBeforeLogRotation(t *testing.T) {
	root, store := newTestServiceStore(t)
	path := filepath.Join(root, "logs", "gateway", "access.tsv")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create log directory: %v", err)
	}
	writeLargeLog(t, path, 'a', megabyte+1)
	sentinel := errors.New("lease generation lost")
	service, err := New(Config{
		Root: root, Store: store, Fence: rejectingWriteFence{err: sentinel},
		MaxFileSizeMB: 1, Backups: 2,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := service.RunOnce(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("stale rotation error = %v", err)
	}
	assertFileSize(t, path, megabyte+1)
	if _, err := os.Stat(path + ".1"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale rotation created backup: %v", err)
	}
}

type directWriteFence struct{}

func (directWriteFence) WithWriteFence(_ context.Context, operation func() error) error {
	return operation()
}

type rejectingWriteFence struct {
	err error
}

func (fence rejectingWriteFence) WithWriteFence(context.Context, func() error) error {
	return fence.err
}

func newTestServiceStore(t *testing.T) (string, *controlplane.Store) {
	t.Helper()
	root := t.TempDir()
	store, err := controlplane.Open(context.Background(), root, controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return root, store
}

func writeLargeLog(t *testing.T, path string, value byte, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create log directory: %v", err)
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	block := bytesOf(value, 32*1024)
	remaining := size
	for remaining > 0 {
		count := int64(len(block))
		if count > remaining {
			count = remaining
		}
		if _, err := file.Write(block[:count]); err != nil {
			_ = file.Close()
			t.Fatalf("write log: %v", err)
		}
		remaining -= count
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close log: %v", err)
	}
}

func bytesOf(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func assertFileSize(t *testing.T, path string, expected int64) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Size() != expected {
		t.Fatalf("%s size = %d, want %d", path, info.Size(), expected)
	}
}

func assertFirstByte(t *testing.T, path string, expected byte) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	buffer := []byte{0}
	if _, err := file.Read(buffer); err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if buffer[0] != expected {
		t.Fatalf("%s first byte = %q, want %q", path, buffer[0], expected)
	}
}
