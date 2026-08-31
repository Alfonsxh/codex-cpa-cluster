package snapshotfile

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPendingSnapshotIsReadableByGroupBeforeAtomicReplace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "quota-heartbeat.json")
	pending, err := newPending(path, 0o640, os.Getgid())
	if err != nil {
		t.Fatalf("newPending: %v", err)
	}
	defer pending.Cleanup()
	pendingInformation, err := pending.Stat()
	if err != nil {
		t.Fatalf("stat pending snapshot: %v", err)
	}
	assertFileContract(t, pendingInformation, 0o640, os.Getgid())
	if _, err := pending.Write([]byte("ready\n")); err != nil {
		t.Fatalf("write pending snapshot: %v", err)
	}
	if err := pending.CloseAtomicallyReplace(); err != nil {
		t.Fatalf("CloseAtomicallyReplace: %v", err)
	}
	information, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat published snapshot: %v", err)
	}
	assertFileContract(t, information, 0o640, os.Getgid())
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "ready\n" {
		t.Fatalf("published snapshot = %q, %v", raw, err)
	}
}

func TestPendingSnapshotSetsRequestedGroupBeforeAtomicReplace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to assign the production Gateway reader group")
	}
	path := filepath.Join(t.TempDir(), "auth-snapshot.json")
	pending, err := newPending(path, 0o640, 65534)
	if err != nil {
		t.Fatalf("newPending: %v", err)
	}
	defer pending.Cleanup()
	information, err := pending.Stat()
	if err != nil {
		t.Fatalf("stat pending snapshot: %v", err)
	}
	assertFileContract(t, information, 0o640, 65534)
}

func assertFileContract(t *testing.T, information os.FileInfo, permission os.FileMode, gid int) {
	t.Helper()
	if information.Mode().Perm() != permission {
		t.Fatalf("snapshot mode = %o, want %o", information.Mode().Perm(), permission)
	}
	status, ok := information.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("snapshot stat type = %T", information.Sys())
	}
	if int(status.Gid) != gid {
		t.Fatalf("snapshot gid = %d, want %d", status.Gid, gid)
	}
}
