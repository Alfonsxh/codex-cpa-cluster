package usage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPortalStoreUsesExistingSchemaAndCompatibleSessionHashes(t *testing.T) {
	path := createPortalFixture(t, 10)
	now := time.Unix(10_000, 0)
	store, err := OpenPortalPath(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer store.Close()

	token, created, err := store.CreateSession(context.Background(), "Alice@Example.com", 12*time.Hour)
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if token == "" || created.User != "alice@example.com" || created.ExpiresAt != 10_000+12*60*60 {
		t.Fatalf("created session = (%q, %#v)", token, created)
	}
	digest := sha256.Sum256([]byte(token))
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture readback: %v", err)
	}
	defer database.Close()
	var persisted string
	if err := database.QueryRow("SELECT session_hash FROM portal_sessions").Scan(&persisted); err != nil {
		t.Fatalf("read session hash: %v", err)
	}
	if persisted != hex.EncodeToString(digest[:]) || strings.Contains(persisted, token) {
		t.Fatalf("persisted session hash = %q", persisted)
	}
	resolved, err := store.ResolveSession(context.Background(), token)
	if err != nil || resolved != created {
		t.Fatalf("ResolveSession = (%#v, %v), want %#v", resolved, err, created)
	}
	if err := store.RevokeSession(context.Background(), token); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	if _, err := store.ResolveSession(context.Background(), token); !errors.Is(err, ErrPortalSessionNotFound) {
		t.Fatalf("resolved revoked session: %v", err)
	}
}

func TestPortalCredentialUpdateKeepsOnlyCurrentSession(t *testing.T) {
	path := createPortalFixture(t, 10)
	store, err := OpenPortalPath(path, func() time.Time { return time.Unix(20_000, 0) })
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	keepToken, _, err := store.CreateSession(ctx, "alice@example.com", time.Hour)
	if err != nil {
		t.Fatalf("create kept session: %v", err)
	}
	revokedToken, _, err := store.CreateSession(ctx, "alice@example.com", time.Hour)
	if err != nil {
		t.Fatalf("create revoked session: %v", err)
	}
	credential, err := store.SetCredential(ctx, "alice@example.com", "scrypt$fixture", false, keepToken)
	if err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	if credential.PasswordHash != "scrypt$fixture" || credential.MustChange {
		t.Fatalf("credential = %#v", credential)
	}
	if _, err := store.ResolveSession(ctx, keepToken); err != nil {
		t.Fatalf("kept session: %v", err)
	}
	if _, err := store.ResolveSession(ctx, revokedToken); !errors.Is(err, ErrPortalSessionNotFound) {
		t.Fatalf("replaced session still active: %v", err)
	}
}

func TestPortalUserStateDeletionIsAtomicAndKeepsHistoricalAdjustments(t *testing.T) {
	path := createPortalFixture(t, 10)
	store, err := OpenPortalPath(path, func() time.Time { return time.Unix(30_000, 0) })
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if _, err := store.SetCredential(ctx, "alice@example.com", "scrypt$fixture", true, ""); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	if _, _, err := store.CreateSession(ctx, "alice@example.com", time.Hour); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO user_quota_policies(user_email, weekly_tokens, created_at, updated_at, created_by, reset_at)
		VALUES ('alice@example.com', 1000, 1, 1, 'test', NULL)`); err != nil {
		t.Fatalf("seed quota policy: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO user_quota_adjustments(
			id, user_email, week_start_at, action, token_amount, reason, created_at, created_by
		) VALUES ('adjustment-1', 'alice@example.com', 0, 'bonus', 100, 'audit fixture', 1, 'test')`); err != nil {
		t.Fatalf("seed quota adjustment: %v", err)
	}
	if err := store.DeleteUserState(ctx, "Alice@Example.com"); err != nil {
		t.Fatalf("DeleteUserState: %v", err)
	}
	for _, table := range []string{"portal_sessions", "portal_credentials", "user_quota_policies"} {
		var count int
		if err := store.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM "+table+" WHERE user_email = 'alice@example.com'"); err != nil || count != 0 {
			t.Fatalf("%s count = %d, err = %v", table, count, err)
		}
	}
	var adjustmentCount int
	if err := store.db.GetContext(ctx, &adjustmentCount, `
		SELECT COUNT(*) FROM user_quota_adjustments WHERE user_email = 'alice@example.com'`); err != nil || adjustmentCount != 1 {
		t.Fatalf("historical adjustment count = %d, err = %v", adjustmentCount, err)
	}
}

func TestPortalQuotaPolicyUsesExistingWeeklyAggregationAndNaturalWeekReset(t *testing.T) {
	path := createPortalFixture(t, 10)
	store, err := OpenPortalPath(path, func() time.Time { return time.Unix(1_800_000_000, 0) })
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	defaultLimit := int64(1000)
	initial, err := store.WeeklyQuota(ctx, "alice@example.com", &defaultLimit)
	if err != nil || initial.PolicyMode != "inherit" || initial.LimitTokens == nil || *initial.LimitTokens != 1000 {
		t.Fatalf("initial quota = (%#v, %v)", initial, err)
	}
	if _, err := store.db.ExecContext(ctx, `
		UPDATE usage_meta SET value = 'Asia/Shanghai' WHERE key = 'weekly_usage_timezone'`); err != nil {
		t.Fatalf("set quota timezone: %v", err)
	}
	custom := int64(500)
	if err := store.SetQuotaPolicy(ctx, "Alice@Example.com", "custom", &custom, true, "admin"); err != nil {
		t.Fatalf("SetQuotaPolicy custom: %v", err)
	}
	configured, err := store.WeeklyQuota(ctx, "alice@example.com", &defaultLimit)
	if err != nil || configured.PolicyMode != "custom" || configured.LimitTokens == nil ||
		*configured.LimitTokens != 500 || configured.Timezone != "Asia/Shanghai" ||
		configured.PolicyResetAt == nil || *configured.PolicyResetAt != configured.WeekEndAt {
		t.Fatalf("configured quota = (%#v, %v)", configured, err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO user_quota_adjustments(
			id, user_email, week_start_at, action, token_amount, reason, created_at, created_by
		) VALUES
			('adjustment-1', 'alice@example.com', ?, 'bonus', 100, 'temporary capacity', 10, 'admin'),
			('adjustment-2', 'alice@example.com', ?, 'usage_reset', 20, 'traffic correction', 20, 'admin')`,
		configured.WeekStartAt, configured.WeekStartAt); err != nil {
		t.Fatalf("seed current quota adjustments: %v", err)
	}
	history, err := store.QuotaAdjustmentHistory(ctx, "Alice@Example.com", 20)
	if err != nil || len(history) != 2 || history[0].Action != "usage_reset" || history[1].Action != "bonus" {
		t.Fatalf("quota adjustment history = (%#v, %v)", history, err)
	}
	if err := store.SetQuotaPolicy(ctx, "alice@example.com", "unlimited", nil, false, "admin"); err != nil {
		t.Fatalf("SetQuotaPolicy unlimited: %v", err)
	}
	unlimited, err := store.WeeklyQuota(ctx, "alice@example.com", &defaultLimit)
	if err != nil || unlimited.PolicyMode != "unlimited" || !unlimited.Unlimited || unlimited.PolicyResetAt != nil {
		t.Fatalf("unlimited quota = (%#v, %v)", unlimited, err)
	}
	if err := store.ClearQuotaPolicy(ctx, "alice@example.com"); err != nil {
		t.Fatalf("ClearQuotaPolicy: %v", err)
	}
	cleared, err := store.WeeklyQuota(ctx, "alice@example.com", &defaultLimit)
	if err != nil || cleared.PolicyMode != "inherit" {
		t.Fatalf("cleared quota = (%#v, %v)", cleared, err)
	}
}

func TestPortalQuotaActionsMatchV1BulkSemanticsAndKeepUsageHistory(t *testing.T) {
	path := createPortalFixture(t, 10)
	now := time.Unix(1_800_000_000, 0)
	store, err := OpenPortalPath(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	defaultLimit := int64(1_000)
	quota, err := store.WeeklyQuota(ctx, "alice@example.com", &defaultLimit)
	if err != nil {
		t.Fatalf("read quota period: %v", err)
	}
	if _, err := store.writeDB.ExecContext(ctx, `
		INSERT INTO user_weekly_usage(
			user_email, week_start_at, total_tokens, weighted_tokens, request_count, updated_at
		) VALUES
			('alice@example.com', ?, 150, 300, 1, ?),
			('bob@example.com', ?, 0, 0, 0, ?)`,
		quota.WeekStartAt, now.Unix(), quota.WeekStartAt, now.Unix(),
	); err != nil {
		t.Fatalf("seed current weekly usage: %v", err)
	}
	aliceCustom := int64(500)
	if err := store.SetQuotaPolicy(ctx, "alice@example.com", "custom", &aliceCustom, false, "test"); err != nil {
		t.Fatalf("seed Alice policy: %v", err)
	}
	if err := store.SetQuotaPolicy(ctx, "bob@example.com", "unlimited", nil, false, "test"); err != nil {
		t.Fatalf("seed Bob policy: %v", err)
	}

	bonus, err := store.ApplyQuotaAction(ctx, QuotaActionRequest{
		Action: "add_bonus", Users: []string{"Alice@Example.com"}, TokenAmount: 200,
		Reason: "  temporary   capacity  ", CreatedBy: "admin", DefaultLimit: &defaultLimit,
	})
	if err != nil || bonus.Action != "bonus" || bonus.TokenAmount == nil || *bonus.TokenAmount != 200 ||
		len(bonus.AppliedUsers) != 1 || bonus.AppliedUsers[0] != "alice@example.com" ||
		bonus.Reason != "temporary capacity" {
		t.Fatalf("bonus action = (%#v, %v)", bonus, err)
	}
	afterBonus, err := store.WeeklyQuota(ctx, "alice@example.com", &defaultLimit)
	if err != nil || afterBonus.LimitTokens == nil || *afterBonus.LimitTokens != 700 ||
		afterBonus.WeightedRawUsedTokens != 300 || afterBonus.UsedTokens != 300 {
		t.Fatalf("quota after bonus = (%#v, %v)", afterBonus, err)
	}
	if _, err := store.ApplyQuotaAction(ctx, QuotaActionRequest{
		Action: "add_bonus", Users: []string{"bob@example.com"}, TokenAmount: 10,
		Reason: "not needed", CreatedBy: "admin", DefaultLimit: &defaultLimit,
	}); !errors.Is(err, ErrQuotaActionUnlimited) {
		t.Fatalf("unlimited bonus error = %v", err)
	}

	reset, err := store.ApplyQuotaAction(ctx, QuotaActionRequest{
		Action: "reset_usage", Users: []string{"alice@example.com", "bob@example.com"},
		Reason: "traffic correction", CreatedBy: "admin", DefaultLimit: &defaultLimit,
	})
	if err != nil || reset.Action != "usage_reset" || reset.TokenAmount == nil || *reset.TokenAmount != 300 ||
		len(reset.AppliedUsers) != 1 || reset.AppliedUsers[0] != "alice@example.com" ||
		len(reset.SkippedUsers) != 1 || reset.SkippedUsers[0] != "bob@example.com" {
		t.Fatalf("reset action = (%#v, %v)", reset, err)
	}
	afterReset, err := store.WeeklyQuota(ctx, "alice@example.com", &defaultLimit)
	if err != nil || afterReset.RawUsedTokens != 150 || afterReset.WeightedRawUsedTokens != 300 ||
		afterReset.UsedTokens != 0 {
		t.Fatalf("quota after reset = (%#v, %v)", afterReset, err)
	}
	repeatedReset, err := store.ApplyQuotaAction(ctx, QuotaActionRequest{
		Action: "reset_usage", Users: []string{"alice@example.com"},
		Reason: "repeat correction", CreatedBy: "admin", DefaultLimit: &defaultLimit,
	})
	if err != nil || len(repeatedReset.AppliedUsers) != 0 ||
		len(repeatedReset.SkippedUsers) != 1 || repeatedReset.TokenAmount == nil || *repeatedReset.TokenAmount != 0 {
		t.Fatalf("repeated reset = (%#v, %v)", repeatedReset, err)
	}

	restored, err := store.ApplyQuotaAction(ctx, QuotaActionRequest{
		Action: "restore_default", Users: []string{"alice@example.com", "bob@example.com"},
		CreatedBy: "admin", DefaultLimit: &defaultLimit,
	})
	if err != nil || restored.Action != "restore_default" || restored.ChangedPolicies == nil || *restored.ChangedPolicies != 2 {
		t.Fatalf("restore action = (%#v, %v)", restored, err)
	}
	for _, user := range []string{"alice@example.com", "bob@example.com"} {
		inherited, readError := store.WeeklyQuota(ctx, user, &defaultLimit)
		if readError != nil || inherited.PolicyMode != "inherit" {
			t.Fatalf("restored quota for %s = (%#v, %v)", user, inherited, readError)
		}
	}
}

func TestPortalQuotaActionRejectsUnsafeInputBeforeAnyWrite(t *testing.T) {
	path := createPortalFixture(t, 10)
	store, err := OpenPortalPath(path, func() time.Time { return time.Unix(1_800_000_000, 0) })
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	maximum := int64(1_000_000_000_000)
	if err := store.SetQuotaPolicy(ctx, "alice@example.com", "custom", &maximum, false, "test"); err != nil {
		t.Fatalf("seed maximum policy: %v", err)
	}
	if _, err := store.ApplyQuotaAction(ctx, QuotaActionRequest{
		Action: "add_bonus", Users: []string{"alice@example.com"}, TokenAmount: 1,
		Reason: "too much", CreatedBy: "admin", DefaultLimit: &maximum,
	}); !errors.Is(err, ErrQuotaActionLimitExceeded) {
		t.Fatalf("oversized bonus error = %v", err)
	}
	if _, err := store.ApplyQuotaAction(ctx, QuotaActionRequest{
		Action: "reset_usage", Users: []string{"alice@example.com"},
		Reason: strings.Repeat("x", 201), CreatedBy: "admin", DefaultLimit: &maximum,
	}); !errors.Is(err, ErrInvalidQuotaAction) {
		t.Fatalf("long reason error = %v", err)
	}
	var count int
	if err := store.db.GetContext(ctx, &count, "SELECT COUNT(*) FROM user_quota_adjustments"); err != nil || count != 0 {
		t.Fatalf("unsafe quota action persisted %d rows: %v", count, err)
	}
}

func TestPortalQuotaPolicyWaitsForSQLiteBusyBeforeReadThenWrite(t *testing.T) {
	path := createPortalFixture(t, 10)
	store, err := OpenPortalPath(path, func() time.Time { return time.Unix(1_800_000_000, 0) })
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`
		UPDATE usage_meta SET value = 'Asia/Shanghai' WHERE key = 'weekly_usage_timezone'`); err != nil {
		t.Fatalf("set quota timezone: %v", err)
	}

	blocker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open external portal blocker: %v", err)
	}
	defer blocker.Close()
	if _, err := blocker.Exec("PRAGMA busy_timeout = 1000"); err != nil {
		t.Fatalf("configure external portal blocker: %v", err)
	}
	if _, err := blocker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin external portal write: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	custom := int64(500)
	go func() {
		done <- store.SetQuotaPolicy(ctx, "alice@example.com", "custom", &custom, true, "admin")
	}()
	select {
	case err := <-done:
		t.Fatalf("quota policy update did not wait for SQLite busy state: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := store.WeeklyQuota(ctx, "alice@example.com", nil); err != nil {
		t.Fatalf("read-only quota while external writer holds WAL lock: %v", err)
	}
	if _, err := blocker.Exec("COMMIT"); err != nil {
		t.Fatalf("release external portal write: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("quota policy update after SQLite busy recovery: %v", err)
	}
	quota, err := store.WeeklyQuota(ctx, "alice@example.com", nil)
	if err != nil || quota.PolicyMode != "custom" || quota.PolicyTokens == nil || *quota.PolicyTokens != custom ||
		quota.Timezone != "Asia/Shanghai" || quota.PolicyResetAt == nil || *quota.PolicyResetAt != quota.WeekEndAt {
		t.Fatalf("quota policy after SQLite busy recovery = (%#v, %v)", quota, err)
	}
}

func TestPortalStoreRefusesMissingOrOldDatabaseWithoutCreatingFiles(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sqlite3")
	if _, err := OpenPortalPath(missing, time.Now); err == nil {
		t.Fatal("missing portal database was accepted")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("missing portal database was created: %v", err)
	}
	path := createPortalFixture(t, 9)
	if _, err := OpenPortalPath(path, time.Now); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("old portal schema error = %v", err)
	}
}

func TestFencedPortalStoreRejectsSessionMutationBeforeUsageWrite(t *testing.T) {
	path := createPortalFixture(t, 10)
	inner, err := OpenPortalPath(path, func() time.Time { return time.Unix(40_000, 0) })
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer inner.Close()
	sentinel := errors.New("lease generation lost")
	fence := &portalWriteFenceStub{err: sentinel}
	store, err := NewFencedPortalStore(inner, fence)
	if err != nil {
		t.Fatalf("NewFencedPortalStore: %v", err)
	}
	if _, _, err := store.CreateSession(context.Background(), "alice@example.com", time.Hour); !errors.Is(err, sentinel) {
		t.Fatalf("fenced CreateSession error = %v", err)
	}
	var sessions int
	if err := inner.db.Get(&sessions, "SELECT COUNT(*) FROM portal_sessions"); err != nil || sessions != 0 {
		t.Fatalf("sessions after rejected fence = %d, %v", sessions, err)
	}
	if _, err := store.WeeklyQuota(context.Background(), "alice@example.com", nil); err != nil {
		t.Fatalf("read-only WeeklyQuota: %v", err)
	}
	if _, err := store.ApplyQuotaAction(context.Background(), QuotaActionRequest{
		Action: "restore_default", Users: []string{"alice@example.com"}, CreatedBy: "admin",
	}); !errors.Is(err, sentinel) {
		t.Fatalf("fenced quota action error = %v", err)
	}
	if _, err := store.SyncUserTeams(context.Background(), map[string]TeamIdentity{
		"alice@example.com": {TeamID: "team-platform", MembershipVersion: 7},
	}); !errors.Is(err, sentinel) {
		t.Fatalf("fenced team identity sync error = %v", err)
	}
	if fence.calls != 3 {
		t.Fatalf("write fence calls = %d, want session, quota action, and team sync", fence.calls)
	}
}

func TestPortalStoreSynchronizesCurrentTeamIdentity(t *testing.T) {
	path := createPortalFixture(t, 10)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open portal fixture identity database: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO key_identities(
			key_hash, key_label, user_email, account, team_id,
			team_membership_version, first_seen_at, last_seen_at
		) VALUES ('digest', 'alice:alpha', 'alice@example.com', 'alpha', '', 0, 1, 1)`); err != nil {
		database.Close()
		t.Fatalf("seed portal identity: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close portal fixture identity database: %v", err)
	}
	store, err := OpenPortalPath(path, time.Now)
	if err != nil {
		t.Fatalf("OpenPortalPath: %v", err)
	}
	defer store.Close()
	count, err := store.SyncUserTeams(context.Background(), map[string]TeamIdentity{
		"Alice@Example.com": {TeamID: "team-platform", MembershipVersion: 7},
	})
	if err != nil || count != 1 {
		t.Fatalf("SyncUserTeams = %d, %v", count, err)
	}
	var teamID string
	var membershipVersion int64
	if err := store.db.QueryRow(`
		SELECT team_id, team_membership_version
		  FROM key_identities WHERE user_email = 'alice@example.com'`,
	).Scan(&teamID, &membershipVersion); err != nil {
		t.Fatalf("read synchronized portal identity: %v", err)
	}
	if teamID != "team-platform" || membershipVersion != 7 {
		t.Fatalf("synchronized portal identity = %q, %d", teamID, membershipVersion)
	}
}

type portalWriteFenceStub struct {
	calls int
	err   error
}

func (fence *portalWriteFenceStub) WithWriteFence(_ context.Context, operation func() error) error {
	fence.calls++
	if fence.err != nil {
		return fence.err
	}
	return operation()
}

func createPortalFixture(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.sqlite3")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open portal fixture: %v", err)
	}
	statements := []string{
		`CREATE TABLE key_identities (
			key_hash TEXT PRIMARY KEY,
			key_label TEXT NOT NULL,
			user_email TEXT NOT NULL,
			account TEXT NOT NULL,
			team_id TEXT NOT NULL DEFAULT '',
			team_membership_version INTEGER NOT NULL DEFAULT 0,
			first_seen_at INTEGER NOT NULL,
			last_seen_at INTEGER NOT NULL
		)`,
		`CREATE TABLE portal_sessions (
			session_hash TEXT PRIMARY KEY,
			user_email TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		)`,
		`CREATE TABLE portal_credentials (
			user_email TEXT PRIMARY KEY,
			password_hash TEXT NOT NULL,
			must_change INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		)`,
		`CREATE TABLE user_quota_policies (
			user_email TEXT PRIMARY KEY,
			weekly_tokens INTEGER,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			created_by TEXT NOT NULL,
			reset_at INTEGER
		)`,
		`CREATE TABLE usage_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`INSERT INTO usage_meta(key, value) VALUES ('weekly_usage_timezone', 'UTC')`,
		`CREATE TABLE user_weekly_usage (
			user_email TEXT NOT NULL,
			week_start_at INTEGER NOT NULL,
			total_tokens INTEGER NOT NULL,
			weighted_tokens INTEGER NOT NULL,
			request_count INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY(user_email, week_start_at)
		)`,
		`CREATE TABLE user_quota_adjustments (
			id TEXT PRIMARY KEY,
			user_email TEXT NOT NULL,
			week_start_at INTEGER NOT NULL,
			action TEXT NOT NULL,
			token_amount INTEGER NOT NULL,
			reason TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			created_by TEXT NOT NULL
		)`,
		"PRAGMA user_version = " + strconv.Itoa(version),
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("create portal fixture: %v\n%s", err, statement)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close portal fixture: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("secure portal fixture: %v", err)
	}
	return path
}
