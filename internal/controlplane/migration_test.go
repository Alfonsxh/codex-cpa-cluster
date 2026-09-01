package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
)

func TestOpenMigratesV5AccountsAndDropsOnlyRetiredSecret(t *testing.T) {
	root := t.TempDir()
	database := createLegacyDatabase(t, root, `
        CREATE TABLE accounts (
            id TEXT PRIMARY KEY,
            email TEXT NOT NULL UNIQUE,
            port INTEGER NOT NULL UNIQUE,
            gost_port INTEGER,
            created_at INTEGER NOT NULL,
            group_enabled INTEGER NOT NULL,
            default_group INTEGER NOT NULL,
            position INTEGER NOT NULL
        );
        INSERT INTO accounts VALUES (
            'alpha', 'alpha@example.com', 18319, NULL, 1, 1, 1, 0
        );
        CREATE TABLE schema_migrations (
            version INTEGER PRIMARY KEY,
            applied_at INTEGER NOT NULL
        );
        INSERT INTO schema_migrations VALUES (5, 1);
        CREATE TABLE encrypted_secrets (
            name TEXT PRIMARY KEY,
            nonce BLOB NOT NULL,
            ciphertext BLOB NOT NULL,
            value_sha256 TEXT NOT NULL,
            updated_at INTEGER NOT NULL
        );
        INSERT INTO encrypted_secrets VALUES (
            'gost_tunnel_auth', X'00', X'00', 'retired', 1
        );
        INSERT INTO encrypted_secrets VALUES (
            'future_secret', X'00', X'00', 'preserved', 1
        );`)
	database.Close()
	writeTestEncryptionKey(t, root)

	store := openTestStore(t, root)
	defer store.Close()
	accounts, err := store.ReadAccounts(context.Background())
	if err != nil || len(accounts) != 1 {
		t.Fatalf("ReadAccounts = (%#v, %v)", accounts, err)
	}
	if accounts[0].ProxyMode != "inherit" || accounts[0].ID != "alpha" {
		t.Fatalf("migrated account = %#v", accounts[0])
	}
	transaction, err := store.db.BeginTxx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin accounts inspection: %v", err)
	}
	columns, err := tableColumns(context.Background(), transaction, "accounts")
	if err != nil {
		transaction.Rollback()
		t.Fatalf("tableColumns: %v", err)
	}
	if err := transaction.Rollback(); err != nil {
		t.Fatalf("close accounts inspection: %v", err)
	}
	if _, found := columns["gost_port"]; found {
		t.Fatal("retired gost_port column remains after v5 migration")
	}
	if _, found := columns["proxy_mode"]; !found {
		t.Fatal("proxy_mode was not added during v5 migration")
	}
	statuses, err := store.SecretStatuses(context.Background())
	if err != nil {
		t.Fatalf("SecretStatuses: %v", err)
	}
	if _, found := statuses["gost_tunnel_auth"]; found {
		t.Fatal("retired gost_tunnel_auth secret remains after v5 migration")
	}
	if _, found := statuses["future_secret"]; !found {
		t.Fatal("non-retired secret was removed during v5 migration")
	}
}

func TestOpenMigratesTeamTagStylesInCreationOrder(t *testing.T) {
	root := t.TempDir()
	database := createLegacyDatabase(t, root, `
        CREATE TABLE schema_migrations (
            version INTEGER PRIMARY KEY,
            applied_at INTEGER NOT NULL
        );
        INSERT INTO schema_migrations VALUES (6, 1);
        CREATE TABLE teams (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL UNIQUE COLLATE NOCASE,
            description TEXT NOT NULL DEFAULT '',
            created_at INTEGER NOT NULL,
            updated_at INTEGER NOT NULL
        );
        INSERT INTO teams VALUES ('team_01', 'Team 01', '', 1, 1);
        INSERT INTO teams VALUES ('team_02', 'Team 02', '', 2, 2);
        INSERT INTO teams VALUES ('team_03', 'Team 03', '', 3, 3);
        INSERT INTO teams VALUES ('team_04', 'Team 04', '', 4, 4);
        INSERT INTO teams VALUES ('team_05', 'Team 05', '', 5, 5);
        INSERT INTO teams VALUES ('team_06', 'Team 06', '', 6, 6);
        INSERT INTO teams VALUES ('team_07', 'Team 07', '', 7, 7);
        INSERT INTO teams VALUES ('team_08', 'Team 08', '', 8, 8);
        INSERT INTO teams VALUES ('team_09', 'Team 09', '', 9, 9);
        INSERT INTO teams VALUES ('team_10', 'Team 10', '', 10, 10);
        INSERT INTO teams VALUES ('team_11', 'Team 11', '', 11, 11);`)
	database.Close()

	store := openTestStore(t, root)
	defer store.Close()
	teams, err := store.ListTeams(context.Background())
	if err != nil || len(teams) != 11 {
		t.Fatalf("ListTeams = (%#v, %v)", teams, err)
	}
	for index, team := range teams {
		want := teamTagStyles[index%len(teamTagStyles)]
		if team.TagStyle != want {
			t.Fatalf("migrated team %d tag style = %q, want %q", index+1, team.TagStyle, want)
		}
	}
	var version int
	if err := store.db.Get(&version, "SELECT MAX(version) FROM schema_migrations"); err != nil || version != SchemaVersion {
		t.Fatalf("migrated schema version = (%d, %v), want %d", version, err, SchemaVersion)
	}
}

func TestOpenAddsMissingProxyModeWithoutRetiredColumns(t *testing.T) {
	root := t.TempDir()
	database := createLegacyDatabase(t, root, `
        CREATE TABLE accounts (
            id TEXT PRIMARY KEY,
            email TEXT NOT NULL UNIQUE,
            port INTEGER NOT NULL UNIQUE,
            created_at INTEGER NOT NULL,
            group_enabled INTEGER NOT NULL,
            default_group INTEGER NOT NULL,
            position INTEGER NOT NULL
        );
        INSERT INTO accounts VALUES (
            'alpha', 'alpha@example.com', 18319, 1, 1, 1, 0
        );`)
	database.Close()

	store := openTestStore(t, root)
	defer store.Close()
	accounts, err := store.ReadAccounts(context.Background())
	if err != nil || len(accounts) != 1 || accounts[0].ProxyMode != "inherit" {
		t.Fatalf("migrated accounts = (%#v, %v)", accounts, err)
	}
}

func TestOpenRefusesUnknownAccountsColumnsWithoutPartialMigration(t *testing.T) {
	root := t.TempDir()
	database := createLegacyDatabase(t, root, `
        CREATE TABLE accounts (
            id TEXT PRIMARY KEY,
            email TEXT NOT NULL UNIQUE,
            port INTEGER NOT NULL UNIQUE,
            created_at INTEGER NOT NULL,
            group_enabled INTEGER NOT NULL,
            default_group INTEGER NOT NULL,
            position INTEGER NOT NULL,
            future_policy TEXT
        );`)
	database.Close()

	_, err := Open(context.Background(), root, Options{})
	if err == nil || !strings.Contains(err.Error(), "future_policy") {
		t.Fatalf("Open with unknown accounts column error = %v", err)
	}
	database, err = sql.Open("sqlite", filepath.Join(root, databaseRelativePath))
	if err != nil {
		t.Fatalf("reopen legacy database: %v", err)
	}
	defer database.Close()
	rows, err := database.Query("PRAGMA table_info(accounts)")
	if err != nil {
		t.Fatalf("inspect accounts after failed migration: %v", err)
	}
	defer rows.Close()
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan accounts column: %v", err)
		}
		columns[name] = true
	}
	if columns["proxy_mode"] {
		t.Fatal("failed migration partially added proxy_mode")
	}
	if !columns["future_policy"] {
		t.Fatal("failed migration removed unknown column")
	}
}

func TestOpenImportsLegacyPlaintextSecretsWithoutDeletingFiles(t *testing.T) {
	root := t.TempDir()
	secrets := filepath.Join(root, "secrets")
	if err := os.MkdirAll(secrets, 0o700); err != nil {
		t.Fatalf("create secrets directory: %v", err)
	}
	values := map[string]string{
		"cpa-management.key": "test-management-key",
		"wecom-webhook.url":  "https://example.invalid/wecom-test",
	}
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(secrets, name), []byte(value+"\n"), 0o600); err != nil {
			t.Fatalf("write legacy secret %s: %v", name, err)
		}
	}
	store := openTestStore(t, root)
	defer store.Close()
	for secretName, filename := range legacySecretFiles {
		value, found, err := store.ReadSecret(context.Background(), secretName)
		if err != nil || !found || value != strings.TrimSpace(values[filename]) {
			t.Fatalf("ReadSecret(%s) = (%q, %v, %v)", secretName, value, found, err)
		}
		if _, err := os.Stat(filepath.Join(secrets, filename)); err != nil {
			t.Fatalf("legacy secret %s was removed during import: %v", filename, err)
		}
	}
}

func TestRetiredSecretIsPreservedWhenDatabaseIsAlreadyCurrent(t *testing.T) {
	root := t.TempDir()
	store := openTestStore(t, root)
	if err := store.WriteSecret(context.Background(), "gost_tunnel_auth", "legacy-but-current"); err != nil {
		t.Fatalf("WriteSecret: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopened := openTestStore(t, root)
	defer reopened.Close()
	value, found, err := reopened.ReadSecret(context.Background(), "gost_tunnel_auth")
	if err != nil || !found || value != "legacy-but-current" {
		t.Fatalf("current-schema retired secret = (%q, %v, %v)", value, found, err)
	}
}

func TestTwoStoresSerializeCrossInstanceWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	root := t.TempDir()
	first := openTestStore(t, root)
	defer first.Close()
	second := openTestStore(t, root)
	defer second.Close()

	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- first.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
			close(firstEntered)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := transaction.ExecContext(ctx, "INSERT INTO metadata(key, value) VALUES ('writer', 'first')")
			return err
		})
	}()
	<-firstEntered

	secondStarted := make(chan struct{})
	secondDone := make(chan error, 1)
	go func() {
		close(secondStarted)
		secondDone <- second.WriteMetadata(ctx, "writer", "second")
	}()
	<-secondStarted
	select {
	case err := <-secondDone:
		t.Fatalf("second writer bypassed process lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := <-secondDone; err != nil {
		t.Fatalf("second write: %v", err)
	}
	value, found, err := first.ReadMetadata(ctx, "writer")
	if err != nil || !found || value != "second" {
		t.Fatalf("serialized writer result = (%q, %v, %v)", value, found, err)
	}
}

func createLegacyDatabase(t *testing.T, root string, statements string) *sql.DB {
	t.Helper()
	state := filepath.Join(root, "state")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatalf("create legacy state directory: %v", err)
	}
	database, err := sql.Open("sqlite", filepath.Join(root, databaseRelativePath))
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	if _, err := database.Exec(statements); err != nil {
		database.Close()
		t.Fatalf("create legacy database: %v", err)
	}
	return database
}

func writeTestEncryptionKey(t *testing.T, root string) {
	t.Helper()
	path := filepath.Join(root, keyRelativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create encryption key directory: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("k", 32)), 0o600); err != nil {
		t.Fatalf("write encryption key: %v", err)
	}
}

func assertErrorIs(t *testing.T, err error, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want errors.Is(%v)", err, target)
	}
}
