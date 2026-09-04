package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

const usageSchemaSQL = `
CREATE TABLE key_identities (
    key_hash TEXT PRIMARY KEY,
    key_label TEXT NOT NULL,
    user_email TEXT NOT NULL,
    account TEXT NOT NULL,
    team_id TEXT NOT NULL DEFAULT '',
    team_membership_version INTEGER NOT NULL DEFAULT 0,
    first_seen_at INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL
);

CREATE TABLE usage_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_key TEXT NOT NULL UNIQUE,
    account TEXT NOT NULL,
    user_email TEXT NOT NULL,
    key_label TEXT NOT NULL,
    occurred_at INTEGER NOT NULL,
    request_id TEXT NOT NULL DEFAULT '',
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    alias TEXT NOT NULL DEFAULT '',
    reasoning_effort TEXT NOT NULL DEFAULT '',
    endpoint TEXT NOT NULL DEFAULT '',
    failed INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens INTEGER NOT NULL DEFAULT 0,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    quota_multiplier REAL NOT NULL DEFAULT 1.0,
    weighted_tokens INTEGER NOT NULL DEFAULT 0,
    weight_policy_version TEXT NOT NULL DEFAULT 'legacy-v1',
    team_id TEXT NOT NULL DEFAULT '',
    team_membership_version INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX usage_events_account_time ON usage_events(account, occurred_at);
CREATE INDEX usage_events_team_time ON usage_events(team_id, occurred_at);
CREATE INDEX usage_events_time_user ON usage_events(occurred_at, user_email);
CREATE INDEX usage_events_user_time ON usage_events(user_email, occurred_at);

CREATE TABLE usage_meta (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

INSERT INTO usage_meta(key, value) VALUES ('weekly_usage_timezone', 'Asia/Shanghai');

CREATE TABLE portal_sessions (
    session_hash TEXT PRIMARY KEY,
    user_email TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL
);

CREATE INDEX portal_sessions_expiry ON portal_sessions(expires_at);

CREATE TABLE portal_credentials (
    user_email TEXT PRIMARY KEY,
    password_hash TEXT NOT NULL,
    must_change INTEGER NOT NULL DEFAULT 1 CHECK(must_change IN (0, 1)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE user_weekly_usage (
    user_email TEXT NOT NULL,
    week_start_at INTEGER NOT NULL,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    weighted_tokens INTEGER NOT NULL DEFAULT 0,
    request_count INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(user_email, week_start_at)
);

CREATE INDEX user_weekly_usage_week ON user_weekly_usage(week_start_at, user_email);

CREATE TABLE user_quota_policies (
    user_email TEXT PRIMARY KEY,
    weekly_tokens INTEGER CHECK(weekly_tokens IS NULL OR weekly_tokens > 0),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    created_by TEXT NOT NULL DEFAULT 'admin',
    reset_at INTEGER
);

CREATE TABLE user_quota_adjustments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_email TEXT NOT NULL,
    week_start_at INTEGER NOT NULL,
    action TEXT NOT NULL CHECK(action IN ('bonus', 'usage_reset')),
    token_amount INTEGER NOT NULL CHECK(token_amount > 0),
    reason TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    created_by TEXT NOT NULL DEFAULT 'admin'
);

CREATE INDEX user_quota_adjustments_user_week
    ON user_quota_adjustments(user_email, week_start_at, created_at);
`

// Initialize creates the current usage database only when the target path does
// not exist. Normal runtimes deliberately continue to use OpenWriterPath and
// OpenPortalPath, which never create or migrate deployment state.
func Initialize(ctx context.Context, path string) (returnError error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve usage database initialization path: %w", err)
	}
	directory := filepath.Dir(absolutePath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create usage database directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("secure usage database directory: %w", err)
	}
	file, err := os.OpenFile(absolutePath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("refusing to initialize existing usage database: %s", absolutePath)
		}
		return fmt.Errorf("create usage database: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(absolutePath)
		return fmt.Errorf("close new usage database file: %w", err)
	}
	created := true
	defer func() {
		if returnError != nil && created {
			_ = os.Remove(absolutePath)
		}
	}()

	database, err := sql.Open("sqlite", absolutePath)
	if err != nil {
		return fmt.Errorf("open new usage database: %w", err)
	}
	defer database.Close()
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin usage database initialization: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, usageSchemaSQL); err != nil {
		return fmt.Errorf("initialize usage database schema: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", writerSchemaVersion)); err != nil {
		return fmt.Errorf("record usage database schema version: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit usage database initialization: %w", err)
	}
	var integrity string
	if err := database.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&integrity); err != nil {
		return fmt.Errorf("verify usage database integrity: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("new usage database failed integrity_check: %s", integrity)
	}
	if err := os.Chmod(absolutePath, 0o600); err != nil {
		return fmt.Errorf("secure usage database: %w", err)
	}
	syncFile, err := os.OpenFile(absolutePath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("open usage database for sync: %w", err)
	}
	if err := syncFile.Sync(); err != nil {
		syncFile.Close()
		return fmt.Errorf("sync usage database: %w", err)
	}
	if err := syncFile.Close(); err != nil {
		return fmt.Errorf("close synced usage database: %w", err)
	}
	created = false
	return nil
}
