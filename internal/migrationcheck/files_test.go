package migrationcheck

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPrivateTestKeyAndStreamFixture(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "test.key")
	if err := os.WriteFile(keyPath, []byte("dedicated-test-key\n"), 0o600); err != nil {
		t.Fatalf("write Key: %v", err)
	}
	key, err := ReadPrivateTestKey(keyPath)
	if err != nil || key != "dedicated-test-key" {
		t.Fatalf("ReadPrivateTestKey = (%q, %v)", key, err)
	}
	fixturePath := filepath.Join(directory, "stream.json")
	if err := os.WriteFile(fixturePath, []byte(`{"model":"fixture","stream":true}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if raw, err := ReadStreamFixture(fixturePath); err != nil || len(raw) == 0 {
		t.Fatalf("ReadStreamFixture = (%q, %v)", raw, err)
	}
}

func TestReadPrivateTestKeyRejectsPermissionsAndSymlink(t *testing.T) {
	directory := t.TempDir()
	keyPath := filepath.Join(directory, "test.key")
	if err := os.WriteFile(keyPath, []byte("dedicated-test-key"), 0o644); err != nil {
		t.Fatalf("write Key: %v", err)
	}
	if _, err := ReadPrivateTestKey(keyPath); err == nil {
		t.Fatal("group-readable test Key was accepted")
	}
	if err := os.Chmod(keyPath, 0o600); err != nil {
		t.Fatalf("chmod Key: %v", err)
	}
	linkPath := filepath.Join(directory, "link.key")
	if err := os.Symlink(keyPath, linkPath); err != nil {
		t.Fatalf("symlink Key: %v", err)
	}
	if _, err := ReadPrivateTestKey(linkPath); err == nil {
		t.Fatal("symlink test Key was accepted")
	}
}

func TestReadStreamFixtureRequiresExplicitStreamingJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "request.json")
	for _, raw := range []string{"[]", `{}`, `{"stream":false}`, "not-json"} {
		if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		if _, err := ReadStreamFixture(path); err == nil {
			t.Fatalf("unsafe stream fixture accepted: %s", raw)
		}
	}
}
