package failover

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileAuditRecorderAppendsPythonCompatibleSecretFreeJSON(t *testing.T) {
	root := t.TempDir()
	recorder := FileAuditRecorder{
		Root: root, Now: func() time.Time { return time.Unix(0, 0) }, Fence: &testWriteFence{},
	}
	if err := recorder.Record(context.Background(), "account.failover.rebalance", "alpha:2 -> beta:2", "accepted"); err != nil {
		t.Fatalf("Record: %v", err)
	}
	path := filepath.Join(root, "logs", "admin", "audit.jsonl")
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	text := string(payload)
	for _, expected := range []string{
		`"timestamp":"1970-01-01T00:00:00Z"`,
		`"action":"account.failover.rebalance"`,
		`"target":"alpha:2 -> beta:2"`,
		`"outcome":"accepted"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("audit payload %q is missing %q", text, expected)
		}
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("audit mode = (%v, %v)", info, err)
	}
}

func TestFileAuditRecorderRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "logs", "admin")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	target := filepath.Join(root, "outside")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Symlink(target, filepath.Join(directory, "audit.jsonl")); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	recorder := FileAuditRecorder{Root: root, Fence: &testWriteFence{}}
	if err := recorder.Record(context.Background(), "action", "target", "accepted"); err == nil ||
		!strings.Contains(err.Error(), "symbolic link") {
		t.Fatalf("symlink audit error = %v", err)
	}
}

func TestFileAuditRecorderRejectsStaleLeaseBeforeAppend(t *testing.T) {
	root := t.TempDir()
	sentinel := errors.New("lease generation lost")
	fence := &testWriteFence{err: sentinel}
	recorder := FileAuditRecorder{Root: root, Fence: fence}
	if err := recorder.Record(context.Background(), "action", "target", "accepted"); !errors.Is(err, sentinel) {
		t.Fatalf("stale audit error = %v", err)
	}
	if fence.calls != 1 {
		t.Fatalf("audit fence calls = %d", fence.calls)
	}
	if _, err := os.Stat(filepath.Join(root, "logs", "admin", "audit.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale audit created file: %v", err)
	}
}

type testWriteFence struct {
	calls int
	err   error
}

func (fence *testWriteFence) WithWriteFence(_ context.Context, operation func() error) error {
	fence.calls++
	if fence.err != nil {
		return fence.err
	}
	return operation()
}
