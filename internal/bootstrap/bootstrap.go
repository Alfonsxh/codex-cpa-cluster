package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/collector"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/google/renameio/v2"
)

const managementSecretName = "cpa_management_key"

type Config struct {
	Root                  string
	Now                   func() time.Time
	ManagementKeyProvider func() (string, error)
}

type Result struct {
	ManagementKey          string
	AuthSnapshotGeneration string
	QuotaGeneration        string
}

type localBootstrapFence struct{}

func (localBootstrapFence) WithWriteFence(_ context.Context, operation func() error) error {
	return operation()
}

// Initialize creates a new empty deployment state. It refuses every existing
// authoritative file; callers must initialize in a staging root and publish
// that root atomically instead of attempting to repair a partial live target.
func Initialize(ctx context.Context, config Config) (Result, error) {
	root, err := filepath.Abs(strings.TrimSpace(config.Root))
	if err != nil || strings.TrimSpace(config.Root) == "" {
		return Result{}, errors.New("bootstrap root is required")
	}
	root = filepath.Clean(root)
	if root == string(filepath.Separator) {
		return Result{}, errors.New("bootstrap root must not be the filesystem root")
	}
	for _, relative := range []string{
		"state/control-plane.sqlite3",
		"state/usage.sqlite3",
		"secrets/control-plane.key",
	} {
		path := filepath.Join(root, relative)
		if _, statError := os.Lstat(path); statError == nil {
			return Result{}, fmt.Errorf("refusing to bootstrap existing target state: %s", path)
		} else if !errors.Is(statError, os.ErrNotExist) {
			return Result{}, fmt.Errorf("inspect bootstrap target state %s: %w", path, statError)
		}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.ManagementKeyProvider == nil {
		config.ManagementKeyProvider = randomManagementKey
	}
	if err := prepareDirectories(root); err != nil {
		return Result{}, err
	}

	store, err := controlplane.Open(ctx, root, controlplane.Options{Now: config.Now})
	if err != nil {
		return Result{}, fmt.Errorf("initialize control-plane store: %w", err)
	}
	defer store.Close()
	managementKey, err := config.ManagementKeyProvider()
	if err != nil {
		return Result{}, fmt.Errorf("generate management credential: %w", err)
	}
	if err := validateManagementKey(managementKey); err != nil {
		return Result{}, err
	}
	if err := store.WriteSecret(ctx, managementSecretName, managementKey); err != nil {
		return Result{}, fmt.Errorf("store management credential: %w", err)
	}
	if err := usage.Initialize(ctx, filepath.Join(root, usage.DatabaseRelativePath)); err != nil {
		return Result{}, err
	}

	authPublisher := &failover.AuthSnapshotPublisher{
		Root: root, Store: store, Fence: localBootstrapFence{}, Now: config.Now,
	}
	authSnapshot, err := authPublisher.PublishAuthSnapshot(ctx, false)
	if err != nil {
		return Result{}, fmt.Errorf("publish empty authentication snapshot: %w", err)
	}
	quotaPublisher := &collector.SnapshotPublisher{Root: root, Now: config.Now}
	quotaSnapshot, err := quotaPublisher.PublishQuotaSnapshot(ctx, map[string]usage.WeeklyQuota{})
	if err != nil {
		return Result{}, fmt.Errorf("publish empty quota snapshot: %w", err)
	}
	if _, err := quotaPublisher.PublishQuotaHeartbeat(ctx, true, "", 15, 90); err != nil {
		return Result{}, fmt.Errorf("publish initial quota heartbeat: %w", err)
	}
	activeSlot := filepath.Join(root, "state", "edge", "active-gateway.conf")
	if err := renameio.WriteFile(
		activeSlot,
		[]byte("set $active_gateway_backend gateway-blue:8317;\n"),
		0o644,
	); err != nil {
		return Result{}, fmt.Errorf("write initial active Gateway slot: %w", err)
	}
	if err := os.Chmod(activeSlot, 0o644); err != nil {
		return Result{}, fmt.Errorf("secure initial active Gateway slot: %w", err)
	}
	return Result{
		ManagementKey:          managementKey,
		AuthSnapshotGeneration: authSnapshot.Generation,
		QuotaGeneration:        quotaSnapshot.Generation,
	}, nil
}

func prepareDirectories(root string) error {
	directories := []struct {
		relative string
		mode     os.FileMode
		gid      int
	}{
		{"state", 0o700, -1},
		{"state/gateway", 0o750, 65534},
		{"state/edge", 0o755, -1},
		{"secrets", 0o700, -1},
		{"auth", 0o700, -1},
		{"configs", 0o700, -1},
		{"management", 0o700, -1},
		{"logs", 0o700, -1},
		{"logs/gateway", 0o770, 65534},
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create bootstrap root: %w", err)
	}
	for _, directory := range directories {
		path := filepath.Join(root, directory.relative)
		if err := os.MkdirAll(path, directory.mode); err != nil {
			return fmt.Errorf("create bootstrap directory %s: %w", path, err)
		}
		if err := os.Chmod(path, directory.mode); err != nil {
			return fmt.Errorf("secure bootstrap directory %s: %w", path, err)
		}
		if os.Geteuid() == 0 && directory.gid >= 0 {
			if err := os.Chown(path, -1, directory.gid); err != nil {
				return fmt.Errorf("assign bootstrap directory group %s: %w", path, err)
			}
		}
	}
	return nil
}

func randomManagementKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func validateManagementKey(value string) error {
	if len(value) < 12 || len(value) > 128 || strings.TrimSpace(value) != value {
		return errors.New("generated management credential must contain 12-128 non-whitespace characters")
	}
	for _, character := range value {
		if character <= ' ' || character == 0x7f {
			return errors.New("generated management credential contains whitespace or control characters")
		}
	}
	return nil
}
