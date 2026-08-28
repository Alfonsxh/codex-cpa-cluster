package usage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

var (
	ErrPortalSessionNotFound    = errors.New("portal session not found")
	ErrPortalCredentialNotFound = errors.New("portal credential not found")
)

var requiredPortalTables = map[string][]string{
	"key_identities": {
		"user_email", "team_id", "team_membership_version",
	},
	"portal_sessions": {
		"session_hash", "user_email", "created_at", "expires_at",
	},
	"portal_credentials": {
		"user_email", "password_hash", "must_change", "created_at", "updated_at",
	},
	"user_quota_policies": {
		"user_email", "weekly_tokens", "created_at", "updated_at", "created_by", "reset_at",
	},
	"usage_meta": {"key", "value"},
	"user_weekly_usage": {
		"user_email", "week_start_at", "total_tokens", "weighted_tokens", "request_count", "updated_at",
	},
	"user_quota_adjustments": {
		"id", "user_email", "week_start_at", "action", "token_amount", "reason", "created_at", "created_by",
	},
}

type PortalStore struct {
	db      *sqlx.DB
	writeDB *sqlx.DB
	now     func() time.Time
}

type PortalSession struct {
	User      string `db:"user" json:"user"`
	ExpiresAt int64  `db:"expires_at" json:"expires_at"`
}

type PortalCredential struct {
	PasswordHash string `db:"password_hash"`
	MustChange   bool   `db:"must_change"`
	CreatedAt    int64  `db:"created_at"`
	UpdatedAt    int64  `db:"updated_at"`
}

type QuotaAdjustment struct {
	Action      string `db:"action" json:"action"`
	TokenAmount int64  `db:"token_amount" json:"token_amount"`
	Reason      string `db:"reason" json:"reason"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
	CreatedBy   string `db:"created_by" json:"created_by"`
}

// OpenPortal opens the existing schema-v10 usage database for the narrowly
// scoped session and credential writes required by the self-service portal. It
// never creates a database, table, column, migration, or parent directory.
func OpenPortal(root string, now func() time.Time) (*PortalStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("usage root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve usage root: %w", err)
	}
	return OpenPortalPath(filepath.Join(filepath.Clean(absoluteRoot), DatabaseRelativePath), now)
}

func OpenPortalPath(path string, now func() time.Time) (*PortalStore, error) {
	absolutePath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return nil, fmt.Errorf("resolve usage database: %w", err)
	}
	information, err := os.Stat(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open existing usage database for portal: %w", err)
	}
	if !information.Mode().IsRegular() {
		return nil, errors.New("usage database must be a regular file")
	}
	database, err := openUsageSQLiteHandle(absolutePath, false)
	if err != nil {
		return nil, fmt.Errorf("open existing usage database for portal reads: %w", err)
	}
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to usage database for portal reads: %w", err)
	}
	store := &PortalStore{db: database, now: now}
	if store.now == nil {
		store.now = time.Now
	}
	if err := store.validate(context.Background()); err != nil {
		_ = database.Close()
		return nil, err
	}
	writeDatabase, err := openUsageSQLiteHandle(absolutePath, true)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("open existing usage database for portal writes: %w", err)
	}
	if err := writeDatabase.Ping(); err != nil {
		_ = writeDatabase.Close()
		_ = database.Close()
		return nil, fmt.Errorf("connect to usage database for portal writes: %w", err)
	}
	store.writeDB = writeDatabase
	return store, nil
}

func (store *PortalStore) Close() error {
	if store == nil {
		return nil
	}
	var readError, writeError error
	if store.db != nil {
		readError = store.db.Close()
	}
	if store.writeDB != nil {
		writeError = store.writeDB.Close()
	}
	return errors.Join(readError, writeError)
}

func (store *PortalStore) validate(ctx context.Context) error {
	var version int
	if err := store.db.GetContext(ctx, &version, "PRAGMA user_version"); err != nil {
		return fmt.Errorf("read usage schema version for portal: %w", err)
	}
	if version < minimumReadableSchemaVersion {
		return fmt.Errorf(
			"usage schema version %d is older than required version %d; Go portal refuses to migrate it",
			version, minimumReadableSchemaVersion,
		)
	}
	for table, requiredColumns := range requiredPortalTables {
		columns, err := portalTableColumns(ctx, store.db, table)
		if err != nil {
			return err
		}
		for _, column := range requiredColumns {
			if _, found := columns[column]; !found {
				return fmt.Errorf("usage table %s is missing required portal column %s", table, column)
			}
		}
	}
	return nil
}

func portalTableColumns(ctx context.Context, database *sqlx.DB, table string) (map[string]struct{}, error) {
	rows, err := database.QueryxContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("inspect usage table %s for portal: %w", table, err)
	}
	defer rows.Close()
	columns := make(map[string]struct{})
	for rows.Next() {
		var sequence int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("scan usage table %s for portal: %w", table, err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect usage table %s for portal: %w", table, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("usage table %s required by portal is missing", table)
	}
	return columns, nil
}

func (store *PortalStore) CreateSession(
	ctx context.Context,
	user string,
	ttl time.Duration,
) (string, PortalSession, error) {
	user = strings.ToLower(strings.TrimSpace(user))
	if user == "" {
		return "", PortalSession{}, errors.New("portal session user is required")
	}
	if ttl <= 0 || ttl > 30*24*time.Hour {
		return "", PortalSession{}, errors.New("portal session TTL is invalid")
	}
	token, err := randomPortalToken()
	if err != nil {
		return "", PortalSession{}, err
	}
	now := store.now().Unix()
	session := PortalSession{User: user, ExpiresAt: now + int64(ttl/time.Second)}
	transaction, err := store.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return "", PortalSession{}, fmt.Errorf("begin portal session creation: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "DELETE FROM portal_sessions WHERE expires_at <= ?", now); err != nil {
		return "", PortalSession{}, fmt.Errorf("clean expired portal sessions: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO portal_sessions(session_hash, user_email, created_at, expires_at)
		VALUES (?, ?, ?, ?)`, portalTokenHash(token), session.User, now, session.ExpiresAt); err != nil {
		return "", PortalSession{}, fmt.Errorf("create portal session: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return "", PortalSession{}, fmt.Errorf("commit portal session creation: %w", err)
	}
	return token, session, nil
}

func (store *PortalStore) ResolveSession(ctx context.Context, token string) (PortalSession, error) {
	digest := portalTokenHash(token)
	if digest == "" {
		return PortalSession{}, ErrPortalSessionNotFound
	}
	var session PortalSession
	err := store.db.GetContext(ctx, &session, `
		SELECT lower(trim(user_email)) AS user, expires_at
		  FROM portal_sessions
		 WHERE session_hash = ? AND expires_at > ?`, digest, store.now().Unix())
	if errors.Is(err, sql.ErrNoRows) {
		return PortalSession{}, ErrPortalSessionNotFound
	}
	if err != nil {
		return PortalSession{}, fmt.Errorf("resolve portal session: %w", err)
	}
	return session, nil
}

// SyncUserTeams updates the current mutable identity attribution used for new
// usage events. Historical usage_events rows remain immutable audit records.
func (store *PortalStore) SyncUserTeams(
	ctx context.Context,
	classifications map[string]TeamIdentity,
) (int, error) {
	writer := &Writer{db: store.db, writeDB: store.writeDB, now: store.now}
	return writer.SyncUserTeams(ctx, classifications)
}

func (store *PortalStore) RevokeSession(ctx context.Context, token string) error {
	digest := portalTokenHash(token)
	if digest == "" {
		return nil
	}
	if _, err := store.writeDB.ExecContext(ctx, "DELETE FROM portal_sessions WHERE session_hash = ?", digest); err != nil {
		return fmt.Errorf("revoke portal session: %w", err)
	}
	return nil
}

func (store *PortalStore) Credential(ctx context.Context, user string) (PortalCredential, error) {
	user = strings.ToLower(strings.TrimSpace(user))
	var credential PortalCredential
	err := store.db.GetContext(ctx, &credential, `
		SELECT password_hash, must_change, created_at, updated_at
		  FROM portal_credentials
		 WHERE lower(trim(user_email)) = ?`, user)
	if errors.Is(err, sql.ErrNoRows) {
		return PortalCredential{}, ErrPortalCredentialNotFound
	}
	if err != nil {
		return PortalCredential{}, fmt.Errorf("read portal credential: %w", err)
	}
	return credential, nil
}

func (store *PortalStore) SetCredential(
	ctx context.Context,
	user string,
	passwordHash string,
	mustChange bool,
	keepSessionToken string,
) (PortalCredential, error) {
	user = strings.ToLower(strings.TrimSpace(user))
	passwordHash = strings.TrimSpace(passwordHash)
	if user == "" || passwordHash == "" {
		return PortalCredential{}, errors.New("portal credential user and password hash are required")
	}
	now := store.now().Unix()
	transaction, err := store.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return PortalCredential{}, fmt.Errorf("begin portal credential update: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO portal_credentials(user_email, password_hash, must_change, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(user_email) DO UPDATE SET
			password_hash = excluded.password_hash,
			must_change = excluded.must_change,
			updated_at = excluded.updated_at`, user, passwordHash, mustChange, now, now); err != nil {
		return PortalCredential{}, fmt.Errorf("update portal credential: %w", err)
	}
	keepDigest := portalTokenHash(keepSessionToken)
	if keepDigest == "" {
		_, err = transaction.ExecContext(ctx, "DELETE FROM portal_sessions WHERE user_email = ?", user)
	} else {
		_, err = transaction.ExecContext(ctx, `
			DELETE FROM portal_sessions WHERE user_email = ? AND session_hash != ?`, user, keepDigest)
	}
	if err != nil {
		return PortalCredential{}, fmt.Errorf("revoke replaced portal sessions: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return PortalCredential{}, fmt.Errorf("commit portal credential update: %w", err)
	}
	return store.Credential(ctx, user)
}

// DeleteIdentity atomically revokes every self-service session and removes the
// login credential for one user. Usage events remain immutable history.
func (store *PortalStore) DeleteIdentity(ctx context.Context, user string) error {
	return store.deleteUserState(ctx, user, false)
}

// DeleteUserState performs the additional Admin deletion cleanup for the
// current quota policy. Historical usage and quota adjustments are retained
// for audit and reporting compatibility with persisted historical data.
func (store *PortalStore) DeleteUserState(ctx context.Context, user string) error {
	return store.deleteUserState(ctx, user, true)
}

func (store *PortalStore) WeeklyQuota(
	ctx context.Context,
	user string,
	defaultWeeklyTokens *int64,
) (WeeklyQuota, error) {
	user = strings.ToLower(strings.TrimSpace(user))
	if user == "" {
		return WeeklyQuota{}, errors.New("weekly quota user is required")
	}
	quotas, err := (&Writer{db: store.db, writeDB: store.writeDB, now: store.now}).WeeklyQuotas(
		ctx, []string{user}, defaultWeeklyTokens,
	)
	if err != nil {
		return WeeklyQuota{}, err
	}
	quota, found := quotas[user]
	if !found {
		return WeeklyQuota{}, errors.New("weekly quota result is missing")
	}
	return quota, nil
}

func (store *PortalStore) QuotaAdjustmentHistory(
	ctx context.Context,
	user string,
	limit int,
) ([]QuotaAdjustment, error) {
	user = strings.ToLower(strings.TrimSpace(user))
	if user == "" {
		return nil, errors.New("quota adjustment user is required")
	}
	if limit <= 0 {
		limit = 20
	}
	limit = min(limit, 100)
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin quota adjustment history: %w", err)
	}
	defer transaction.Rollback()
	location, err := loadWeekTimezone(ctx, transaction)
	if err != nil {
		return nil, err
	}
	weekStart, _ := naturalWeekBounds(store.now().Unix(), location)
	rows := make([]QuotaAdjustment, 0)
	if err := transaction.SelectContext(ctx, &rows, `
		SELECT action, token_amount, reason, created_at, created_by
		  FROM user_quota_adjustments
		 WHERE user_email = ? AND week_start_at = ?
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`, user, weekStart, limit); err != nil {
		return nil, fmt.Errorf("read quota adjustment history: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit quota adjustment history: %w", err)
	}
	return rows, nil
}

func (store *PortalStore) SetQuotaPolicy(
	ctx context.Context,
	user string,
	mode string,
	weeklyTokens *int64,
	resetOnNewWeek bool,
	createdBy string,
) error {
	user = strings.ToLower(strings.TrimSpace(user))
	mode = strings.ToLower(strings.TrimSpace(mode))
	createdBy = strings.TrimSpace(createdBy)
	if user == "" || createdBy == "" {
		return errors.New("quota policy user and actor are required")
	}
	if mode == "inherit" {
		return store.ClearQuotaPolicy(ctx, user)
	}
	if mode != "unlimited" && mode != "custom" {
		return errors.New("quota policy mode must be inherit, unlimited, or custom")
	}
	if mode == "custom" && (weeklyTokens == nil || *weeklyTokens <= 0 || *weeklyTokens > 1_000_000_000_000) {
		return errors.New("custom weekly quota must be between 1 and 1000000000000")
	}
	if mode == "unlimited" {
		weeklyTokens = nil
	}
	now := store.now().Unix()
	transaction, err := store.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin quota policy update: %w", err)
	}
	defer transaction.Rollback()
	var resetAt *int64
	if resetOnNewWeek {
		location, err := loadWeekTimezone(ctx, transaction)
		if err != nil {
			return err
		}
		_, weekEnd := naturalWeekBounds(now, location)
		resetAt = &weekEnd
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO user_quota_policies(
			user_email, weekly_tokens, created_at, updated_at, created_by, reset_at
		) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_email) DO UPDATE SET
			weekly_tokens = excluded.weekly_tokens,
			updated_at = excluded.updated_at,
			created_by = excluded.created_by,
			reset_at = excluded.reset_at`,
		user, weeklyTokens, now, now, createdBy, resetAt,
	); err != nil {
		return fmt.Errorf("update user quota policy: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit quota policy update: %w", err)
	}
	return nil
}

func (store *PortalStore) ClearQuotaPolicy(ctx context.Context, user string) error {
	user = strings.ToLower(strings.TrimSpace(user))
	if user == "" {
		return errors.New("quota policy user is required")
	}
	if _, err := store.writeDB.ExecContext(ctx, `
		DELETE FROM user_quota_policies WHERE user_email = ?`, user); err != nil {
		return fmt.Errorf("clear user quota policy: %w", err)
	}
	return nil
}

func (store *PortalStore) deleteUserState(ctx context.Context, user string, includeQuotaPolicy bool) error {
	user = strings.ToLower(strings.TrimSpace(user))
	if user == "" {
		return errors.New("portal identity user is required")
	}
	transaction, err := store.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin portal identity deletion: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, "DELETE FROM portal_sessions WHERE lower(trim(user_email)) = ?", user); err != nil {
		return fmt.Errorf("delete portal user sessions: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM portal_credentials WHERE lower(trim(user_email)) = ?", user); err != nil {
		return fmt.Errorf("delete portal user credential: %w", err)
	}
	if includeQuotaPolicy {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM user_quota_policies WHERE lower(trim(user_email)) = ?", user); err != nil {
			return fmt.Errorf("delete portal user quota policy: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit portal identity deletion: %w", err)
	}
	return nil
}

func randomPortalToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate portal session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func portalTokenHash(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}
