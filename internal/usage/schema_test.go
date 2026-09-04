package usage

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitializeCreatesCurrentUsageSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "usage.sqlite3")
	if err := Initialize(context.Background(), path); err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	information, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat initialized usage database: %v", err)
	}
	if information.Mode().Perm() != 0o600 {
		t.Fatalf("initialized usage database mode = %o", information.Mode().Perm())
	}
	writer, err := OpenWriterPath(path, nil)
	if err != nil {
		t.Fatalf("open initialized writer: %v", err)
	}
	var timezone string
	if err := writer.db.Get(&timezone, "SELECT value FROM usage_meta WHERE key = ?", weeklyUsageTimezoneKey); err != nil ||
		timezone != "Asia/Shanghai" {
		t.Fatalf("initialized usage timezone = %q, %v", timezone, err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close initialized writer: %v", err)
	}
	portal, err := OpenPortalPath(path, nil)
	if err != nil {
		t.Fatalf("open initialized portal: %v", err)
	}
	if err := portal.Close(); err != nil {
		t.Fatalf("close initialized portal: %v", err)
	}
}

func TestInitializeRefusesExistingUsageDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "usage.sqlite3")
	if err := os.WriteFile(path, []byte("preserve-me"), 0o600); err != nil {
		t.Fatalf("seed usage database path: %v", err)
	}
	err := Initialize(context.Background(), path)
	if err == nil || !strings.Contains(err.Error(), "refusing to initialize existing") {
		t.Fatalf("Initialize existing error = %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "preserve-me" {
		t.Fatalf("existing usage database changed = %q, %v", string(raw), err)
	}
}
