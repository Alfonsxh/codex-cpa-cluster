package controlplane

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestOpenExistingRequiresExistingTargetWithoutCreatingFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-target")
	if _, err := OpenExisting(context.Background(), root, Options{}); err == nil {
		t.Fatal("OpenExisting succeeded without an existing target")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("OpenExisting created target path: %v", err)
	}
}

func TestOpenExistingDefersInitializationUntilExplicitlyOwned(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "state")
	secretDirectory := filepath.Join(root, "secrets")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	if err := os.MkdirAll(secretDirectory, 0o700); err != nil {
		t.Fatalf("create secret directory: %v", err)
	}
	path := filepath.Join(root, databaseRelativePath)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open seed database: %v", err)
	}
	if _, err := database.Exec(`
		CREATE TABLE runtime_state (
			name TEXT PRIMARY KEY,
			payload_json TEXT NOT NULL,
			updated_at INTEGER NOT NULL
		)`); err != nil {
		t.Fatalf("seed runtime_state: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close seed database: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, keyRelativePath), make([]byte, 32), 0o600); err != nil {
		t.Fatalf("seed encryption key: %v", err)
	}

	store, err := OpenExisting(ctx, root, Options{})
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	defer store.Close()
	var schemaMigrations int
	if err := store.db.Get(
		&schemaMigrations,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'",
	); err != nil {
		t.Fatalf("inspect deferred schema: %v", err)
	}
	if schemaMigrations != 0 {
		t.Fatal("OpenExisting initialized schema before ownership")
	}
	if _, err := store.TakeLease(ctx, "runtime-writer", "go-v2", 30*time.Second); err != nil {
		t.Fatalf("TakeLease before initialization: %v", err)
	}
	if err := store.InitializeExisting(ctx); err != nil {
		t.Fatalf("InitializeExisting: %v", err)
	}
	if err := store.db.Get(
		&schemaMigrations,
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'",
	); err != nil {
		t.Fatalf("inspect initialized schema: %v", err)
	}
	if schemaMigrations != 1 {
		t.Fatalf("InitializeExisting schema_migrations count = %d, want 1", schemaMigrations)
	}
}

func TestInitializeExistingRejectsReleasedWorkerGeneration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seed := openTestStore(t, root)
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed store: %v", err)
	}

	store, err := OpenExisting(ctx, root, Options{})
	if err != nil {
		t.Fatalf("OpenExisting: %v", err)
	}
	defer store.Close()
	runtimeLease, err := store.TakeLease(ctx, "runtime-writer", "go-v2", 30*time.Second)
	if err != nil {
		t.Fatalf("take runtime lease: %v", err)
	}
	workerLease, err := store.TakeLease(ctx, "admin", "go-v2:admin", 30*time.Second)
	if err != nil {
		t.Fatalf("take worker lease: %v", err)
	}
	if err := store.InstallWriteFence(runtimeLease, workerLease); err != nil {
		t.Fatalf("install write fence: %v", err)
	}
	if err := store.ReleaseLease(ctx, workerLease); err != nil {
		t.Fatalf("release worker lease: %v", err)
	}
	if err := store.InitializeExisting(ctx); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale initialization error = %v, want ErrLeaseLost", err)
	}
}

func TestOpenCreatesCurrentCompatibleSchemaAndSecureFiles(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, root)
	defer store.Close()

	var version int
	if err := store.db.Get(&version, "SELECT MAX(version) FROM schema_migrations"); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema version = %d, want %d", version, SchemaVersion)
	}
	for _, table := range requiredTables {
		var count int
		if err := store.db.Get(
			&count,
			"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
			table,
		); err != nil || count != 1 {
			t.Fatalf("required table %s = count %d error %v", table, count, err)
		}
	}
	assertFileMode(t, store.Path(), 0o600)
	assertFileMode(t, store.SecretKeyPath(), 0o600)
	assertFileMode(t, filepath.Join(root, "state"), 0o700)
	assertFileMode(t, filepath.Join(root, "secrets"), 0o700)
}

func TestStoreRoundTripsControlPlaneRecords(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()

	accounts := []Account{
		{
			ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318,
			ProxyMode: "inherit", CreatedAt: 100, GroupEnabled: true, DefaultGroup: true,
		},
		{
			ID: "beta", Email: "beta@accounts.example.com", Port: 18319,
			ProxyMode: "direct", CreatedAt: 101, GroupEnabled: false,
		},
	}
	if err := store.WriteAccounts(ctx, accounts); err != nil {
		t.Fatalf("WriteAccounts: %v", err)
	}
	if got, err := store.ReadAccounts(ctx); err != nil || !reflect.DeepEqual(got, accounts) {
		t.Fatalf("ReadAccounts = (%#v, %v), want %#v", got, err, accounts)
	}

	routes := map[string]string{"alice@example.com": "alpha", "bob@example.com": "beta"}
	if err := store.WriteRoutes(ctx, routes); err != nil {
		t.Fatalf("WriteRoutes: %v", err)
	}
	if got, err := store.ReadRoutes(ctx); err != nil || !reflect.DeepEqual(got, routes) {
		t.Fatalf("ReadRoutes = (%#v, %v), want %#v", got, err, routes)
	}

	keys := []KeyRecord{
		{
			Label: "alice@example.com:alpha", Account: "alpha",
			AccountEmail: "alpha@accounts.example.com", User: "alice@example.com",
			Status: "active", Key: "test_external_alice", CreatedAt: 110, UpdatedAt: 111,
		},
		{
			Label: "bob@example.com:beta", Account: "beta",
			AccountEmail: "beta@accounts.example.com", User: "bob@example.com",
			Status: "active", Key: "test_external_bob", CreatedAt: 112, UpdatedAt: 113,
		},
	}
	if err := store.WriteKeyRecords(ctx, keys); err != nil {
		t.Fatalf("WriteKeyRecords: %v", err)
	}
	if got, err := store.ReadKeyRecords(ctx); err != nil || !reflect.DeepEqual(got, keys) {
		t.Fatalf("ReadKeyRecords = (%#v, %v), want %#v", got, err, keys)
	}
	if got, err := store.ReadKeyRecordsForUsers(
		ctx,
		[]string{" ALICE@EXAMPLE.COM ", "alice@example.com"},
	); err != nil || !reflect.DeepEqual(got, keys[:1]) {
		t.Fatalf("ReadKeyRecordsForUsers = (%#v, %v), want %#v", got, err, keys[:1])
	}

	internal := map[string]InternalKey{
		"alice@example.com": {Key: "test_internal_alice", CreatedAt: 120, Status: "active"},
		"bob@example.com":   {Key: "test_internal_bob", CreatedAt: 121},
	}
	if err := store.WriteInternalKeys(ctx, internal); err != nil {
		t.Fatalf("WriteInternalKeys: %v", err)
	}
	internal["bob@example.com"] = InternalKey{Key: "test_internal_bob", CreatedAt: 121, Status: "active"}
	if got, err := store.ReadInternalKeys(ctx); err != nil || !reflect.DeepEqual(got, internal) {
		t.Fatalf("ReadInternalKeys = (%#v, %v), want %#v", got, err, internal)
	}

	settings := map[string]any{
		"account_failover.mode": "active",
		"cpa.proxy_enabled":     true,
		"user_quota.tokens":     float64(1234),
		"example.list":          []any{"alpha", float64(2)},
	}
	if err := store.WriteSettings(ctx, settings); err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
	if got, err := store.ReadSettings(ctx); err != nil || !reflect.DeepEqual(got, settings) {
		t.Fatalf("ReadSettings = (%#v, %v), want %#v", got, err, settings)
	}
	if err := store.UpdateSettings(ctx, map[string]any{"notification.enabled": true}); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	updated, err := store.ReadSettings(ctx)
	if err != nil {
		t.Fatalf("ReadSettings after update: %v", err)
	}
	if updated["notification.enabled"] != true || updated["account_failover.mode"] != settings["account_failover.mode"] {
		t.Fatalf("fine-grained settings update = %#v", updated)
	}

	state := map[string]any{"version": "v2.0.0", "pending": true}
	if err := store.WriteRuntimeState(ctx, "deployment", state); err != nil {
		t.Fatalf("WriteRuntimeState: %v", err)
	}
	var loadedState map[string]any
	if found, err := store.ReadRuntimeState(ctx, "deployment", &loadedState); err != nil || !found || !reflect.DeepEqual(loadedState, state) {
		t.Fatalf("ReadRuntimeState = (%v, %#v, %v), want %#v", found, loadedState, err, state)
	}
	if err := store.PatchRuntimeState(ctx, "deployment", map[string]any{"last_error": "none"}); err != nil {
		t.Fatalf("PatchRuntimeState: %v", err)
	}
	if found, err := store.ReadRuntimeState(ctx, "deployment", &loadedState); err != nil || !found ||
		loadedState["last_error"] != "none" || loadedState["version"] != state["version"] {
		t.Fatalf("patched ReadRuntimeState = (%v, %#v, %v)", found, loadedState, err)
	}
	if err := store.DeleteRuntimeState(ctx, "deployment"); err != nil {
		t.Fatalf("DeleteRuntimeState: %v", err)
	}
	if found, err := store.ReadRuntimeState(ctx, "deployment", &loadedState); err != nil || found {
		t.Fatalf("deleted ReadRuntimeState = (%v, %v)", found, err)
	}
}

func TestWriteAccountsRollsBackWholeReplacementOnConstraintFailure(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	original := []Account{{
		ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318,
		ProxyMode: "inherit", CreatedAt: 100, GroupEnabled: true,
	}}
	if err := store.WriteAccounts(ctx, original); err != nil {
		t.Fatalf("seed accounts: %v", err)
	}
	invalid := []Account{
		{ID: "beta", Email: "duplicate@accounts.example.com", Port: 18319, GroupEnabled: true},
		{ID: "gamma", Email: "duplicate@accounts.example.com", Port: 18320, GroupEnabled: true},
	}
	if err := store.WriteAccounts(ctx, invalid); err == nil {
		t.Fatal("duplicate account replacement unexpectedly succeeded")
	}
	if got, err := store.ReadAccounts(ctx); err != nil || !reflect.DeepEqual(got, original) {
		t.Fatalf("accounts after rollback = (%#v, %v), want %#v", got, err, original)
	}
}

func TestSecretEncryptionPersistsWithoutPlaintextAndRefusesMissingKey(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := openTestStore(t, root)
	value := "test-management-key"
	if err := store.WriteSecret(ctx, "cpa_management_key", value); err != nil {
		t.Fatalf("WriteSecret: %v", err)
	}
	loaded, found, err := store.ReadSecret(ctx, "cpa_management_key")
	if err != nil || !found || loaded != value {
		t.Fatalf("ReadSecret = (%q, %v, %v)", loaded, found, err)
	}
	var ciphertext []byte
	if err := store.db.Get(&ciphertext, "SELECT ciphertext FROM encrypted_secrets WHERE name = ?", "cpa_management_key"); err != nil {
		t.Fatalf("read encrypted row: %v", err)
	}
	if strings.Contains(string(ciphertext), value) {
		t.Fatal("plaintext secret appears in encrypted ciphertext")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	reopened := openTestStore(t, root)
	loaded, found, err = reopened.ReadSecret(ctx, "cpa_management_key")
	if err != nil || !found || loaded != value {
		t.Fatalf("reopened ReadSecret = (%q, %v, %v)", loaded, found, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("close reopened store: %v", err)
	}
	if err := os.Remove(filepath.Join(root, keyRelativePath)); err != nil {
		t.Fatalf("remove encryption key: %v", err)
	}
	if _, err := Open(ctx, root, Options{}); err == nil || !strings.Contains(err.Error(), "key is missing") {
		t.Fatalf("Open without original key error = %v", err)
	}
}

func TestDecryptsLegacyAESGCMCompatibilityVector(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	key, _ := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	keyPath := filepath.Join(root, keyRelativePath)
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatalf("create key directory: %v", err)
	}
	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		t.Fatalf("write compatibility key: %v", err)
	}
	store := openTestStore(t, root)
	defer store.Close()
	nonce, _ := hex.DecodeString("000102030405060708090a0b")
	ciphertext, _ := hex.DecodeString("3367a56fe888a375ec26f2e6d4870c40e8b3fec18ed5ebb37748c9e02585914f06d1bf")
	if _, err := store.db.Exec(`
        INSERT INTO encrypted_secrets(name, nonce, ciphertext, value_sha256, updated_at)
        VALUES (?, ?, ?, ?, ?)`,
		"cpa_management_key",
		nonce,
		ciphertext,
		"bd3606ad437c79bf692b8949ef0a18047c3d40335e529eb4eba98a402adf246e",
		100,
	); err != nil {
		t.Fatalf("insert legacy AES-GCM vector: %v", err)
	}
	value, found, err := store.ReadSecret(ctx, "cpa_management_key")
	if err != nil || !found || value != "test-management-key" {
		t.Fatalf("compatibility ReadSecret = (%q, %v, %v)", value, found, err)
	}
}

func TestReplaceSettingsAndSecretUpdatesOneAtomicConfigurationSnapshot(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, t.TempDir())
	defer store.Close()
	if err := store.WriteSettings(ctx, map[string]any{"before": true}); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := store.WriteSecret(ctx, "cpa_default_proxy_url", "http://before.example.com"); err != nil {
		t.Fatalf("seed proxy secret: %v", err)
	}
	afterProxy := "socks5://user:secret@127.0.0.1:1080"
	if err := store.ReplaceSettingsAndSecret(ctx, map[string]any{
		"after": "value", "account_failover.mode": "off",
	}, "cpa_default_proxy_url", &afterProxy); err != nil {
		t.Fatalf("ReplaceSettingsAndSecret: %v", err)
	}
	settings, err := store.ReadSettings(ctx)
	if err != nil || !reflect.DeepEqual(settings, map[string]any{
		"after": "value", "account_failover.mode": "off",
	}) {
		t.Fatalf("replaced settings = %#v, %v", settings, err)
	}
	proxy, found, err := store.ReadSecret(ctx, "cpa_default_proxy_url")
	if err != nil || !found || proxy != afterProxy {
		t.Fatalf("replaced secret = (%q, %v, %v)", proxy, found, err)
	}
	if err := store.ReplaceSettingsAndSecret(
		ctx,
		map[string]any{"after": "without-secret"},
		"cpa_default_proxy_url",
		nil,
	); err != nil {
		t.Fatalf("ReplaceSettingsAndSecret delete: %v", err)
	}
	if _, found, err := store.ReadSecret(ctx, "cpa_default_proxy_url"); err != nil || found {
		t.Fatalf("deleted secret = (%v, %v)", found, err)
	}
}

func openTestStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := Open(context.Background(), root, Options{
		Now: func() time.Time { return time.Unix(1000, 0) },
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return store
}

func assertFileMode(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	information, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if got := information.Mode().Perm(); got != expected {
		t.Fatalf("mode %s = %04o, want %04o", path, got, expected)
	}
}
