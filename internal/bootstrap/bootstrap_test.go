package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/gateway"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

func TestInitializeCreatesEmptyTargetState(t *testing.T) {
	root := t.TempDir()
	result, err := Initialize(context.Background(), Config{
		Root: root,
		Now:  func() time.Time { return time.Unix(1_700_000_000, 0) },
		ManagementKeyProvider: func() (string, error) {
			return "bootstrap-management-key", nil
		},
	})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	if result.ManagementKey != "bootstrap-management-key" ||
		len(result.AuthSnapshotGeneration) != 32 || len(result.QuotaGeneration) != 32 {
		t.Fatalf("bootstrap result = %#v", result)
	}
	store, err := controlplane.OpenExisting(context.Background(), root, controlplane.Options{})
	if err != nil {
		t.Fatalf("open bootstrapped control plane: %v", err)
	}
	defer store.Close()
	if err := store.InitializeExisting(context.Background()); err != nil {
		t.Fatalf("initialize bootstrapped control plane: %v", err)
	}
	managementKey, found, err := store.ReadSecret(context.Background(), managementSecretName)
	if err != nil || !found || managementKey != result.ManagementKey {
		t.Fatalf("stored management key = %q, %v, %v", managementKey, found, err)
	}
	writer, err := usage.OpenWriterPath(filepath.Join(root, usage.DatabaseRelativePath), nil)
	if err != nil {
		t.Fatalf("open bootstrapped usage database: %v", err)
	}
	_ = writer.Close()
	authRaw, err := os.ReadFile(filepath.Join(root, "state/gateway/auth-snapshot.json"))
	if err != nil {
		t.Fatalf("read auth snapshot: %v", err)
	}
	auth, err := gateway.ParseAuthSnapshot(bytes.NewReader(authRaw))
	if err != nil || len(auth.Records) != 0 {
		t.Fatalf("empty auth snapshot = %#v, %v", auth, err)
	}
	active, err := os.ReadFile(filepath.Join(root, "state/edge/active-gateway.conf"))
	if err != nil || string(active) != "set $active_gateway_backend gateway-blue:8317;\n" {
		t.Fatalf("active Gateway slot = %q, %v", string(active), err)
	}
	for _, relative := range []string{"management", "management/config", "management/config/static"} {
		path := filepath.Join(root, relative)
		information, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("inspect bootstrap runtime directory %s: %v", relative, err)
		}
		if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 || information.Mode().Perm() != 0o700 {
			t.Fatalf("bootstrap runtime directory %s mode = %v", relative, information.Mode())
		}
	}
}

func TestInitializeRefusesExistingAuthority(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state", "usage.sqlite3")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(context.Background(), Config{Root: root})
	if err == nil || !strings.Contains(err.Error(), "refusing to bootstrap existing") {
		t.Fatalf("existing authority error = %v", err)
	}
	raw, readError := os.ReadFile(path)
	if readError != nil || string(raw) != "existing" {
		t.Fatalf("existing authority changed = %q, %v", string(raw), readError)
	}
}

func TestInitializeRefusesSymbolicLinkRuntimeLayout(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	management := filepath.Join(root, "management")
	if err := os.Symlink(outside, management); err != nil {
		t.Fatal(err)
	}
	_, err := Initialize(context.Background(), Config{Root: root})
	if err == nil || !strings.Contains(err.Error(), "must not be a symbolic link") {
		t.Fatalf("symbolic link runtime layout error = %v", err)
	}
	information, statError := os.Lstat(management)
	if statError != nil {
		t.Fatalf("inspect management link: %v", statError)
	}
	if information.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("management link changed: mode=%v", information.Mode())
	}
	for _, relative := range []string{
		"state/control-plane.sqlite3",
		"state/usage.sqlite3",
		"secrets/control-plane.key",
	} {
		if _, statError := os.Lstat(filepath.Join(root, relative)); !errors.Is(statError, os.ErrNotExist) {
			t.Fatalf("bootstrap created authority after unsafe layout: %s (%v)", relative, statError)
		}
	}
}
