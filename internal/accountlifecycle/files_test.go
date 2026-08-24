package accountlifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

const testLifecycleOperationID = "00000000-0000-4000-8000-000000000001"

func TestFileManagerCreateRollbackRemovesOnlyCreatedAccountPaths(t *testing.T) {
	root := t.TempDir()
	manager := &FileManager{Root: root}
	transition, err := manager.PrepareCreate(testLifecycleOperationID, "alpha")
	if err != nil {
		t.Fatalf("PrepareCreate: %v", err)
	}
	writeTestFile(t, filepath.Join(root, "configs", "alpha.yaml"), "generated")
	writeTestFile(t, filepath.Join(root, "unrelated.txt"), "keep")
	if err := transition.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	for _, path := range []string{
		filepath.Join(root, "configs", "alpha.yaml"),
		filepath.Join(root, "auth", "alpha"),
		filepath.Join(root, "logs", "alpha"),
	} {
		if exists(path) {
			t.Fatalf("created account path remains after rollback: %s", path)
		}
	}
	if got := readTestFile(t, filepath.Join(root, "unrelated.txt")); got != "keep" {
		t.Fatalf("unrelated file changed: %q", got)
	}
}

func TestFileManagerRenameBacksUpMovesAndRollsBackExactly(t *testing.T) {
	root := t.TempDir()
	seedAccountPaths(t, root, "alpha")
	manager := &FileManager{
		Root: root,
		Now:  func() time.Time { return time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC) },
	}
	transition, err := manager.PrepareUpdate(testLifecycleOperationID, "alpha", "gamma", testBackupData("alpha"))
	if err != nil {
		t.Fatalf("PrepareUpdate: %v", err)
	}
	if transition.BackupPath() == "" || !strings.Contains(transition.BackupPath(), "alpha-renamed-to-gamma") {
		t.Fatalf("backup path = %q", transition.BackupPath())
	}
	if exists(filepath.Join(root, "auth", "alpha")) || !exists(filepath.Join(root, "auth", "gamma")) {
		t.Fatal("OAuth directory was not moved")
	}
	writeTestFile(t, filepath.Join(root, "configs", "gamma.yaml"), "new-config")
	if err := transition.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !exists(filepath.Join(root, "auth", "alpha")) || exists(filepath.Join(root, "auth", "gamma")) ||
		exists(filepath.Join(root, "configs", "gamma.yaml")) {
		t.Fatal("renamed paths were not restored exactly")
	}
	if got := readTestFile(t, filepath.Join(root, "auth", "alpha", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("OAuth data changed: %q", got)
	}
	backup := filepath.Join(root, filepath.FromSlash(transition.BackupPath()))
	for _, path := range []string{"account.json", "keys.json", "config.yaml", "auth/oauth.json", "logs/main.log"} {
		info, err := os.Stat(filepath.Join(backup, filepath.FromSlash(path)))
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("backup file %s is missing or too permissive: info=%v err=%v", path, info, err)
		}
	}
}

func TestFileManagerDeleteCommitAndRollbackRestoresFromSecureBackup(t *testing.T) {
	root := t.TempDir()
	seedAccountPaths(t, root, "alpha")
	manager := &FileManager{Root: root}
	transition, err := manager.PrepareDelete(testLifecycleOperationID, "alpha", testBackupData("alpha"))
	if err != nil {
		t.Fatalf("PrepareDelete: %v", err)
	}
	if err := transition.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if exists(filepath.Join(root, "configs", "alpha.yaml")) || exists(filepath.Join(root, "auth", "alpha")) {
		t.Fatal("deleted account paths remain after commit")
	}
	if err := transition.Rollback(); err != nil {
		t.Fatalf("Rollback committed delete: %v", err)
	}
	if got := readTestFile(t, filepath.Join(root, "configs", "alpha.yaml")); got != "old-config" {
		t.Fatalf("config backup was not restored: %q", got)
	}
	if got := readTestFile(t, filepath.Join(root, "auth", "alpha", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("OAuth backup was not restored: %q", got)
	}
}

func TestFileManagerAuthClearRejectsSymlinkAndRestoresFiles(t *testing.T) {
	root := t.TempDir()
	seedAccountPaths(t, root, "alpha")
	manager := &FileManager{Root: root}
	transition, err := manager.PrepareAuthClear(testLifecycleOperationID, "alpha", testBackupData("alpha"))
	if err != nil {
		t.Fatalf("PrepareAuthClear: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "auth", "alpha"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("OAuth directory not cleared: entries=%v err=%v", entries, err)
	}
	if err := transition.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if got := readTestFile(t, filepath.Join(root, "auth", "alpha", "oauth.json")); got != "oauth-secret" {
		t.Fatalf("OAuth file was not restored: %q", got)
	}

	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(root, "auth", "alpha")); err != nil {
		t.Fatalf("remove auth directory: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "auth", "alpha")); err != nil {
		t.Fatalf("create auth symlink: %v", err)
	}
	if _, err := manager.PrepareAuthClear(testLifecycleOperationID, "alpha", testBackupData("alpha")); err == nil {
		t.Fatal("PrepareAuthClear unexpectedly followed an OAuth directory symlink")
	}
}

func TestFileManagerCreateRejectsStalePaths(t *testing.T) {
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "configs", "alpha.yaml"), "stale")
	manager := &FileManager{Root: root}
	if _, err := manager.PrepareCreate(testLifecycleOperationID, "alpha"); err == nil {
		t.Fatal("PrepareCreate unexpectedly accepted a stale account path")
	}
}

func seedAccountPaths(t *testing.T, root, accountID string) {
	t.Helper()
	writeTestFile(t, filepath.Join(root, "configs", accountID+".yaml"), "old-config")
	writeTestFile(t, filepath.Join(root, "auth", accountID, "oauth.json"), "oauth-secret")
	writeTestFile(t, filepath.Join(root, "logs", accountID, "main.log"), "runtime-log")
}

func testBackupData(accountID string) BackupData {
	return BackupData{
		Account: controlplane.Account{ID: accountID, Email: accountID + "@accounts.example.com", Port: 18318},
		Keys: []controlplane.StoredKeyRecord{{
			Sequence: 1,
			KeyRecord: controlplane.KeyRecord{
				Label: "alice@example.com:" + accountID, Account: accountID,
				AccountEmail: accountID + "@accounts.example.com", User: "alice@example.com",
				Status: "active", Key: "cpa_external_alice",
			},
		}},
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create test directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(payload)
}
