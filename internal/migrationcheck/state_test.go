package migrationcheck

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCompareStateRootsMatchesDurableDataAndSeparatesOperationalDrift(t *testing.T) {
	v1Root := filepath.Join(t.TempDir(), "v1")
	v2Root := filepath.Join(t.TempDir(), "v2")
	createStateFixture(t, v1Root, "python-v1", 100, "fixture-external-key", strings.Repeat("a", 32))
	createStateFixture(t, v2Root, "go-v2", 200, "fixture-external-key", strings.Repeat("b", 32))

	comparison, err := CompareStateRoots(context.Background(), v1Root, v2Root)
	if err != nil {
		t.Fatalf("CompareStateRoots: %v", err)
	}
	if !comparison.Passed || len(comparison.Differences) != 0 {
		t.Fatalf("durable comparison = %#v", comparison)
	}
	if len(comparison.OperationalDifferences) != 2 {
		t.Fatalf("operational differences = %#v", comparison.OperationalDifferences)
	}
	if comparison.V1.CheckpointSHA256 != comparison.V2.CheckpointSHA256 {
		t.Fatalf(
			"semantic checkpoints differ: %s != %s",
			comparison.V1.CheckpointSHA256,
			comparison.V2.CheckpointSHA256,
		)
	}
}

func TestCompareStateRootsDetectsDurableKeyChangeWithoutExposingValues(t *testing.T) {
	v1Root := filepath.Join(t.TempDir(), "v1")
	v2Root := filepath.Join(t.TempDir(), "v2")
	createStateFixture(t, v1Root, "python-v1", 100, "first-sensitive-key", strings.Repeat("a", 32))
	createStateFixture(t, v2Root, "go-v2", 100, "second-sensitive-key", strings.Repeat("a", 32))

	comparison, err := CompareStateRoots(context.Background(), v1Root, v2Root)
	if err != nil {
		t.Fatalf("CompareStateRoots: %v", err)
	}
	if comparison.Passed || !containsString(comparison.Differences, "control durable state differs") {
		t.Fatalf("durable mismatch was not detected: %#v", comparison.Differences)
	}
	raw, err := json.Marshal(comparison)
	if err != nil {
		t.Fatalf("marshal comparison: %v", err)
	}
	for _, secret := range []string{"first-sensitive-key", "second-sensitive-key", "fixture-internal-key"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("state report exposed %q: %s", secret, raw)
		}
	}
}

func TestCompareStateRootsSeparatesDeploymentLocalSettings(t *testing.T) {
	v1Root := filepath.Join(t.TempDir(), "v1")
	v2Root := filepath.Join(t.TempDir(), "v2")
	createStateFixture(t, v1Root, "same-owner", 100, "fixture-external-key", strings.Repeat("a", 32))
	createStateFixture(t, v2Root, "same-owner", 100, "fixture-external-key", strings.Repeat("a", 32))

	v1 := openFixtureDatabase(t, filepath.Join(v1Root, "state", "control-plane.sqlite3"))
	if _, err := v1.Exec("INSERT INTO settings VALUES ('gateway.internal_port', '19316')"); err != nil {
		v1.Close()
		t.Fatalf("insert v1 deployment setting: %v", err)
	}
	if err := v1.Close(); err != nil {
		t.Fatalf("close v1 deployment setting database: %v", err)
	}
	v2 := openFixtureDatabase(t, filepath.Join(v2Root, "state", "control-plane.sqlite3"))
	if _, err := v2.Exec("INSERT INTO settings VALUES ('gateway.internal_port', '18316')"); err != nil {
		v2.Close()
		t.Fatalf("insert v2 deployment setting: %v", err)
	}
	if err := v2.Close(); err != nil {
		t.Fatalf("close v2 deployment setting database: %v", err)
	}

	comparison, err := CompareStateRoots(context.Background(), v1Root, v2Root)
	if err != nil {
		t.Fatalf("CompareStateRoots: %v", err)
	}
	if !comparison.Passed || len(comparison.Differences) != 0 {
		t.Fatalf("deployment-local setting failed durable gate: %#v", comparison)
	}
	if !containsString(comparison.OperationalDifferences, "control runtime_state differs (expected while owners or worker checkpoints differ)") {
		t.Fatalf("deployment-local difference was not surfaced: %#v", comparison.OperationalDifferences)
	}
}

func TestCompareStateRootsStillRejectsBusinessSettingDifference(t *testing.T) {
	v1Root := filepath.Join(t.TempDir(), "v1")
	v2Root := filepath.Join(t.TempDir(), "v2")
	createStateFixture(t, v1Root, "same-owner", 100, "fixture-external-key", strings.Repeat("a", 32))
	createStateFixture(t, v2Root, "same-owner", 100, "fixture-external-key", strings.Repeat("a", 32))

	v2 := openFixtureDatabase(t, filepath.Join(v2Root, "state", "control-plane.sqlite3"))
	if _, err := v2.Exec("UPDATE settings SET value_json = '\"Asia/Shanghai\"' WHERE key = 'user_quota.timezone'"); err != nil {
		v2.Close()
		t.Fatalf("change business setting: %v", err)
	}
	if err := v2.Close(); err != nil {
		t.Fatalf("close business setting database: %v", err)
	}

	comparison, err := CompareStateRoots(context.Background(), v1Root, v2Root)
	if err != nil {
		t.Fatalf("CompareStateRoots: %v", err)
	}
	if comparison.Passed || !containsString(comparison.Differences, "control durable state differs") {
		t.Fatalf("business setting mismatch passed: %#v", comparison)
	}
}

func TestCompareStateRootsRejectsSameOrSymlinkRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	createStateFixture(t, root, "go-v2", 100, "fixture-external-key", strings.Repeat("a", 32))
	if _, err := CompareStateRoots(context.Background(), root, root); err == nil {
		t.Fatal("same state-copy root was accepted")
	}
	link := filepath.Join(t.TempDir(), "linked-state")
	if err := os.Symlink(root, link); err != nil {
		t.Fatalf("create state root symlink: %v", err)
	}
	if _, err := CompareStateRoots(context.Background(), root, link); err == nil {
		t.Fatal("symlink state-copy root was accepted")
	}
}

func createStateFixture(
	t *testing.T,
	root string,
	runtimeOwner string,
	heartbeatAt int64,
	externalKey string,
	authGeneration string,
) {
	t.Helper()
	stateDirectory := filepath.Join(root, "state")
	gatewayDirectory := filepath.Join(stateDirectory, "gateway")
	if err := os.MkdirAll(gatewayDirectory, 0o700); err != nil {
		t.Fatalf("create state fixture directories: %v", err)
	}
	control := openFixtureDatabase(t, filepath.Join(stateDirectory, "control-plane.sqlite3"))
	controlSchema := `
CREATE TABLE accounts (id TEXT PRIMARY KEY, email TEXT NOT NULL);
CREATE TABLE branding_assets (name TEXT PRIMARY KEY, content BLOB NOT NULL);
CREATE TABLE encrypted_secrets (name TEXT PRIMARY KEY, ciphertext BLOB NOT NULL);
CREATE TABLE internal_keys (user_email TEXT PRIMARY KEY, secret TEXT NOT NULL);
CREATE TABLE key_records (sequence INTEGER PRIMARY KEY, user_email TEXT NOT NULL, secret TEXT NOT NULL);
CREATE TABLE metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
CREATE TABLE settings (key TEXT PRIMARY KEY, value_json TEXT NOT NULL);
CREATE TABLE tags (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE teams (id TEXT PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE user_routes (user_email TEXT PRIMARY KEY, account_id TEXT NOT NULL);
CREATE TABLE user_tags (user_email TEXT NOT NULL, tag_id TEXT NOT NULL, PRIMARY KEY(user_email, tag_id));
CREATE TABLE user_team_memberships (user_email TEXT PRIMARY KEY, team_id TEXT);
CREATE TABLE runtime_state (name TEXT PRIMARY KEY, payload_json TEXT NOT NULL);
INSERT INTO accounts VALUES ('alpha', 'fixture@example.com');
INSERT INTO encrypted_secrets VALUES ('wecom_webhook', X'0011');
INSERT INTO internal_keys VALUES ('fixture@example.com', 'fixture-internal-key');
INSERT INTO schema_migrations VALUES (6, 1);
INSERT INTO settings VALUES ('user_quota.timezone', '"UTC"');
	INSERT INTO user_routes VALUES ('fixture@example.com', 'alpha');`
	if _, err := control.Exec(controlSchema); err != nil {
		control.Close()
		t.Fatalf("create control fixture: %v", err)
	}
	if _, err := control.Exec(
		"INSERT INTO key_records VALUES (1, 'fixture@example.com', ?)",
		externalKey,
	); err != nil {
		control.Close()
		t.Fatalf("insert control Key fixture: %v", err)
	}
	if _, err := control.Exec(
		"INSERT INTO runtime_state VALUES ('ownership_lease:runtime-writer', ?)",
		`{"owner":"`+runtimeOwner+`"}`,
	); err != nil {
		control.Close()
		t.Fatalf("insert control runtime fixture: %v", err)
	}
	if err := control.Close(); err != nil {
		t.Fatalf("close control fixture: %v", err)
	}

	usage := openFixtureDatabase(t, filepath.Join(stateDirectory, "usage.sqlite3"))
	usageSchema := `
PRAGMA user_version = 10;
CREATE TABLE key_identities (key_hash TEXT PRIMARY KEY, user_email TEXT NOT NULL);
CREATE TABLE portal_credentials (user_email TEXT PRIMARY KEY, password_hash TEXT NOT NULL);
CREATE TABLE usage_events (id INTEGER PRIMARY KEY, event_key TEXT NOT NULL UNIQUE, total_tokens INTEGER NOT NULL);
CREATE TABLE usage_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE user_quota_adjustments (id INTEGER PRIMARY KEY, user_email TEXT NOT NULL, token_amount INTEGER NOT NULL);
CREATE TABLE user_quota_policies (user_email TEXT PRIMARY KEY, weekly_tokens INTEGER);
CREATE TABLE user_weekly_usage (user_email TEXT NOT NULL, week_start_at INTEGER NOT NULL, weighted_tokens INTEGER NOT NULL, PRIMARY KEY(user_email, week_start_at));
CREATE TABLE portal_sessions (session_hash TEXT PRIMARY KEY, user_email TEXT NOT NULL);
INSERT INTO key_identities VALUES ('digest', 'fixture@example.com');
INSERT INTO usage_events VALUES (1, 'event-one', 42);
INSERT INTO usage_meta VALUES ('weekly_usage_timezone', 'UTC');
INSERT INTO usage_meta VALUES ('collector_last_error', '');
	INSERT INTO user_weekly_usage VALUES ('fixture@example.com', 1, 42);`
	if _, err := usage.Exec(usageSchema); err != nil {
		usage.Close()
		t.Fatalf("create usage fixture: %v", err)
	}
	if _, err := usage.Exec(
		"INSERT INTO usage_meta VALUES ('collector_heartbeat_at', ?)",
		heartbeatAt,
	); err != nil {
		usage.Close()
		t.Fatalf("insert usage heartbeat fixture: %v", err)
	}
	if _, err := usage.Exec(
		"INSERT INTO portal_sessions VALUES (?, 'fixture@example.com')",
		"session-"+runtimeOwner,
	); err != nil {
		usage.Close()
		t.Fatalf("insert usage session fixture: %v", err)
	}
	if err := usage.Close(); err != nil {
		t.Fatalf("close usage fixture: %v", err)
	}

	writeFixtureJSON(t, filepath.Join(gatewayDirectory, "auth-snapshot.json"), map[string]any{
		"version": 1, "generation": authGeneration, "generated_at": heartbeatAt,
		"records": []map[string]any{{
			"external_key_sha256": strings.Repeat("c", 64),
			"user_email":          "fixture@example.com", "account": "alpha",
			"backend": "cliproxy-alpha:8317", "internal_key": "fixture-internal-key",
			"label": "fixture@example.com:alpha",
		}},
	})
	writeFixtureJSON(t, filepath.Join(gatewayDirectory, "quota-snapshot.json"), map[string]any{
		"version": 1, "generation": strings.Repeat("d", 32), "generated_at": heartbeatAt,
		"content_sha256": strings.Repeat("e", 64),
		"records": []map[string]any{{
			"user_email": "fixture@example.com", "week_start_at": 1, "week_end_at": 2,
			"limit_tokens": 100, "used_tokens": 42, "raw_used_tokens": 42,
			"weighted_raw_used_tokens": 42, "quota_unit": "weighted_tokens",
		}},
	})
	writeFixtureJSON(t, filepath.Join(gatewayDirectory, "quota-heartbeat.json"), map[string]any{
		"version": 1, "updated_at": heartbeatAt, "ok": true, "error": "",
		"stale_after_seconds": 15, "last_success_at": heartbeatAt,
		"fail_open_after_seconds": 300,
	})
}

func openFixtureDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture database: %v", err)
	}
	return database
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture JSON: %v", err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatalf("write fixture JSON: %v", err)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
