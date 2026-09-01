package controlplane

const SchemaVersion = 7

var requiredTables = []string{
	"schema_migrations",
	"metadata",
	"settings",
	"accounts",
	"user_routes",
	"key_records",
	"internal_keys",
	"runtime_state",
	"branding_assets",
	"encrypted_secrets",
	"teams",
	"user_team_memberships",
	// Retain the legacy tag tables while the old Admin remains deployable.
	// The Go Admin will not expose tag-management routes or UI.
	"tags",
	"user_tags",
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS metadata (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS accounts (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    port INTEGER NOT NULL UNIQUE,
    proxy_mode TEXT NOT NULL DEFAULT 'inherit',
    created_at INTEGER NOT NULL,
    group_enabled INTEGER NOT NULL DEFAULT 1,
    default_group INTEGER NOT NULL DEFAULT 0,
    position INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS user_routes (
    user_email TEXT PRIMARY KEY,
    account_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS key_records (
    sequence INTEGER PRIMARY KEY,
    label TEXT NOT NULL,
    account_id TEXT NOT NULL,
    account_email TEXT NOT NULL,
    user_email TEXT NOT NULL,
    status TEXT NOT NULL,
    secret TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_key_records_user_status
    ON key_records(user_email, status);
CREATE INDEX IF NOT EXISTS idx_key_records_secret
    ON key_records(secret);
CREATE TABLE IF NOT EXISTS internal_keys (
    user_email TEXT PRIMARY KEY,
    secret TEXT NOT NULL UNIQUE,
    created_at INTEGER NOT NULL,
    status TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS runtime_state (
    name TEXT PRIMARY KEY,
    payload_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS branding_assets (
    name TEXT PRIMARY KEY,
    filename TEXT NOT NULL,
    content_type TEXT NOT NULL,
    content BLOB NOT NULL,
    sha256 TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS encrypted_secrets (
    name TEXT PRIMARY KEY,
    nonce BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    value_sha256 TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS teams (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    description TEXT NOT NULL DEFAULT '',
    tag_style TEXT NOT NULL DEFAULT 'indigo'
        CHECK(tag_style IN ('indigo', 'blue', 'cyan', 'teal', 'green', 'amber', 'orange', 'rose', 'violet', 'slate')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS user_team_memberships (
    user_email TEXT PRIMARY KEY,
    team_id TEXT REFERENCES teams(id) ON DELETE RESTRICT,
    membership_version INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_user_team_memberships_team
    ON user_team_memberships(team_id, user_email);
CREATE TABLE IF NOT EXISTS tags (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE COLLATE NOCASE,
    color TEXT NOT NULL DEFAULT '#6374d8',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS user_tags (
    user_email TEXT NOT NULL,
    tag_id TEXT NOT NULL REFERENCES tags(id) ON DELETE CASCADE,
    assigned_at INTEGER NOT NULL,
    PRIMARY KEY(user_email, tag_id)
);
CREATE INDEX IF NOT EXISTS idx_user_tags_tag
    ON user_tags(tag_id, user_email);
`

var expectedAccountColumns = map[string]struct{}{
	"id":            {},
	"email":         {},
	"port":          {},
	"proxy_mode":    {},
	"created_at":    {},
	"group_enabled": {},
	"default_group": {},
	"position":      {},
}

var retiredAccountColumns = map[string]struct{}{
	"gost_port": {},
}

var expectedTeamColumns = map[string]struct{}{
	"id":          {},
	"name":        {},
	"description": {},
	"tag_style":   {},
	"created_at":  {},
	"updated_at":  {},
}

const rebuildAccountsSQL = `
DROP TABLE IF EXISTS accounts_v6;
CREATE TABLE accounts_v6 (
    id TEXT PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    port INTEGER NOT NULL UNIQUE,
    proxy_mode TEXT NOT NULL DEFAULT 'inherit',
    created_at INTEGER NOT NULL,
    group_enabled INTEGER NOT NULL DEFAULT 1,
    default_group INTEGER NOT NULL DEFAULT 0,
    position INTEGER NOT NULL
);
INSERT INTO accounts_v6(
    id, email, port, proxy_mode, created_at,
    group_enabled, default_group, position
)
SELECT
    id, email, port, proxy_mode, created_at,
    group_enabled, default_group, position
FROM accounts;
DROP TABLE accounts;
ALTER TABLE accounts_v6 RENAME TO accounts;
`
