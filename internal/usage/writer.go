package usage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const (
	writerSchemaVersion           = 10
	weeklyUsageBackfillKey        = "weekly_usage_backfill_version"
	weeklyUsageBackfillVersion    = "2"
	weeklyUsageLastEventIDKey     = "weekly_usage_last_event_id"
	weeklyUsageTimezoneKey        = "weekly_usage_timezone"
	defaultWeekTimezone           = "UTC"
	reasoningPolicyVersionPrefix  = "reasoning-"
	reasoningMultiplierConfigBase = "user_quota.reasoning_multiplier."
)

var reasoningEfforts = []string{
	"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "auto", "unknown",
}

var requiredWriterColumns = map[string][]string{
	"key_identities": {
		"key_hash", "key_label", "user_email", "account", "team_id",
		"team_membership_version", "first_seen_at", "last_seen_at",
	},
	"usage_events": {
		"event_key", "account", "user_email", "key_label", "occurred_at", "request_id",
		"provider", "model", "alias", "reasoning_effort", "endpoint", "failed", "latency_ms",
		"input_tokens", "output_tokens", "reasoning_tokens", "cached_tokens", "total_tokens",
		"quota_multiplier", "weighted_tokens", "weight_policy_version", "team_id",
		"team_membership_version",
	},
	"usage_meta": {"key", "value"},
	"user_weekly_usage": {
		"user_email", "week_start_at", "total_tokens", "weighted_tokens", "request_count", "updated_at",
	},
	"user_quota_policies": {
		"user_email", "weekly_tokens", "created_at", "updated_at", "created_by", "reset_at",
	},
	"user_quota_adjustments": {
		"id", "user_email", "week_start_at", "action", "token_amount", "reason", "created_at", "created_by",
	},
}

type Writer struct {
	db      *sqlx.DB
	writeDB *sqlx.DB
	now     func() time.Time
}

type Identity struct {
	Key       string
	Label     string
	UserEmail string
	Account   string
}

type TeamIdentity struct {
	TeamID            string
	MembershipVersion int64
}

type Event map[string]any

type IngestCounters struct {
	Received  int `json:"received"`
	Inserted  int `json:"inserted"`
	Duplicate int `json:"duplicate"`
	Unmapped  int `json:"unmapped"`
	Ignored   int `json:"ignored"`
}

type WeeklyQuota struct {
	Period                string   `json:"period"`
	Timezone              string   `json:"timezone"`
	WeekStartAt           int64    `json:"week_start_at"`
	WeekEndAt             int64    `json:"week_end_at"`
	LimitTokens           *int64   `json:"limit_tokens"`
	BaseLimitTokens       *int64   `json:"base_limit_tokens"`
	BonusTokens           int64    `json:"bonus_tokens"`
	UsedTokens            int64    `json:"used_tokens"`
	WeightedUsedTokens    int64    `json:"weighted_used_tokens"`
	RawUsedTokens         int64    `json:"raw_used_tokens"`
	UnweightedUsedTokens  int64    `json:"unweighted_used_tokens"`
	WeightedRawUsedTokens int64    `json:"weighted_raw_used_tokens"`
	UsageResetTokens      int64    `json:"usage_reset_tokens"`
	RemainingTokens       *int64   `json:"remaining_tokens"`
	UsedPercent           *float64 `json:"used_percent"`
	LimitReached          bool     `json:"limit_reached"`
	Source                string   `json:"source"`
	PolicyMode            string   `json:"policy_mode"`
	PolicyTokens          *int64   `json:"policy_tokens"`
	PolicyUpdatedAt       *int64   `json:"policy_updated_at"`
	PolicyUpdatedBy       *string  `json:"policy_updated_by"`
	PolicyResetAt         *int64   `json:"policy_reset_at"`
	DefaultLimitTokens    *int64   `json:"default_limit_tokens"`
	Unlimited             bool     `json:"unlimited"`
	SoftLimit             bool     `json:"soft_limit"`
	QuotaUnit             string   `json:"quota_unit"`
	AdjustmentCount       int64    `json:"adjustment_count"`
}

type QuotaResetConfiguration struct {
	Enabled            bool  `json:"enabled"`
	WeekStartAt        int64 `json:"week_start_at"`
	WeekEndAt          int64 `json:"week_end_at"`
	ExpiredPolicies    int64 `json:"expired_policies"`
	ScheduledPolicies  int64 `json:"scheduled_policies"`
	CancelledSchedules int64 `json:"cancelled_schedules"`
}

type CollectorStatus struct {
	Status                  string `json:"status"`
	HeartbeatAt             int64  `json:"heartbeat_at"`
	LastError               string `json:"last_error"`
	EventCount              int64  `json:"event_count"`
	CollectionStartedAt     int64  `json:"collection_started_at"`
	UsageBreakdownStartedAt int64  `json:"usage_breakdown_started_at"`
	LastEventAt             int64  `json:"last_event_at"`
}

type RebuildResult struct {
	Backfilled bool `json:"backfilled"`
	Events     int  `json:"events"`
	Counters   int  `json:"counters"`
}

type identityRow struct {
	KeyHash               string `db:"key_hash"`
	KeyLabel              string `db:"key_label"`
	UserEmail             string `db:"user_email"`
	Account               string `db:"account"`
	TeamID                string `db:"team_id"`
	TeamMembershipVersion int64  `db:"team_membership_version"`
}

// OpenWriterPath opens an existing Python schema-v10 usage database for the
// Go collector. It never creates or migrates the database, which keeps the
// dual-version cutover explicit and reversible.
func OpenWriterPath(path string, now func() time.Time) (*Writer, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve usage database: %w", err)
	}
	information, err := os.Stat(absolutePath)
	if err != nil {
		return nil, fmt.Errorf("open existing usage database: %w", err)
	}
	if !information.Mode().IsRegular() {
		return nil, errors.New("usage database must be a regular file")
	}
	database, err := openUsageSQLiteHandle(absolutePath, false)
	if err != nil {
		return nil, fmt.Errorf("open usage database for reads: %w", err)
	}
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to usage database for reads: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	writer := &Writer{db: database, now: now}
	if err := writer.validateSchema(context.Background()); err != nil {
		_ = database.Close()
		return nil, err
	}
	if _, err := database.Exec("PRAGMA journal_mode = WAL"); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("enable usage WAL mode: %w", err)
	}
	writeDatabase, err := openUsageSQLiteHandle(absolutePath, true)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("open usage database for writes: %w", err)
	}
	if err := writeDatabase.Ping(); err != nil {
		_ = writeDatabase.Close()
		_ = database.Close()
		return nil, fmt.Errorf("connect to usage database for writes: %w", err)
	}
	writer.writeDB = writeDatabase
	return writer, nil
}

func (writer *Writer) Close() error {
	if writer == nil {
		return nil
	}
	var readError, writeError error
	if writer.db != nil {
		readError = writer.db.Close()
	}
	if writer.writeDB != nil {
		writeError = writer.writeDB.Close()
	}
	return errors.Join(readError, writeError)
}

func (writer *Writer) validateSchema(ctx context.Context) error {
	var version int
	if err := writer.db.GetContext(ctx, &version, "PRAGMA user_version"); err != nil {
		return fmt.Errorf("read usage writer schema version: %w", err)
	}
	if version != writerSchemaVersion {
		return fmt.Errorf(
			"usage writer requires schema version %d, found %d; migration is not automatic",
			writerSchemaVersion,
			version,
		)
	}
	for table, required := range requiredWriterColumns {
		columns := make(map[string]struct{})
		rows, err := writer.db.QueryxContext(ctx, "PRAGMA table_info("+table+")")
		if err != nil {
			return fmt.Errorf("inspect usage writer table %s: %w", table, err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue any
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				return fmt.Errorf("scan usage writer table %s: %w", table, err)
			}
			columns[name] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("close usage writer table %s: %w", table, err)
		}
		missing := make([]string, 0)
		for _, name := range required {
			if _, found := columns[name]; !found {
				missing = append(missing, name)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("usage writer table %s is missing columns: %s", table, strings.Join(missing, ", "))
		}
	}
	return nil
}

func (writer *Writer) SyncIdentities(ctx context.Context, identities []Identity) (int, error) {
	now := writer.now().Unix()
	rows := make([]identityRow, 0, len(identities))
	for _, identity := range identities {
		digest := keyHash(identity.Key)
		if digest == "" {
			continue
		}
		rows = append(rows, identityRow{
			KeyHash: digest, KeyLabel: identity.Label,
			UserEmail: strings.ToLower(strings.TrimSpace(identity.UserEmail)),
			Account:   strings.TrimSpace(identity.Account),
		})
	}
	if len(rows) == 0 {
		return 0, nil
	}
	transaction, err := writer.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin identity synchronization: %w", err)
	}
	defer transaction.Rollback()
	for _, row := range rows {
		if _, err := transaction.ExecContext(ctx, `
            INSERT INTO key_identities(
                key_hash, key_label, user_email, account, first_seen_at, last_seen_at
            ) VALUES (?, ?, ?, ?, ?, ?)
            ON CONFLICT(key_hash) DO UPDATE SET
                key_label = excluded.key_label,
                user_email = excluded.user_email,
                account = excluded.account,
                last_seen_at = excluded.last_seen_at`,
			row.KeyHash, row.KeyLabel, row.UserEmail, row.Account, now, now,
		); err != nil {
			return 0, fmt.Errorf("synchronize usage identity: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit identity synchronization: %w", err)
	}
	return len(rows), nil
}

func (writer *Writer) SyncUserTeams(
	ctx context.Context,
	classifications map[string]TeamIdentity,
) (int, error) {
	if len(classifications) == 0 {
		return 0, nil
	}
	transaction, err := writer.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin team identity synchronization: %w", err)
	}
	defer transaction.Rollback()
	users := make([]string, 0, len(classifications))
	for user := range classifications {
		users = append(users, user)
	}
	sort.Strings(users)
	for _, user := range users {
		classification := classifications[user]
		version := max(classification.MembershipVersion, 0)
		if _, err := transaction.ExecContext(
			ctx,
			"UPDATE key_identities SET team_id = ?, team_membership_version = ? WHERE user_email = ?",
			strings.TrimSpace(classification.TeamID),
			version,
			strings.ToLower(strings.TrimSpace(user)),
		); err != nil {
			return 0, fmt.Errorf("synchronize usage team identity: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit team identity synchronization: %w", err)
	}
	return len(users), nil
}

func (writer *Writer) EnsureUsageBreakdownStarted(ctx context.Context) (int64, error) {
	now := writer.now().Unix()
	transaction, err := writer.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin usage breakdown marker: %w", err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT OR IGNORE INTO usage_meta(key, value) VALUES (?, ?)",
		breakdownStartedAtKey,
		strconv.FormatInt(now, 10),
	); err != nil {
		return 0, fmt.Errorf("create usage breakdown marker: %w", err)
	}
	var raw string
	if err := transaction.GetContext(
		ctx,
		&raw,
		"SELECT value FROM usage_meta WHERE key = ?",
		breakdownStartedAtKey,
	); err != nil {
		return 0, fmt.Errorf("read usage breakdown marker: %w", err)
	}
	value, _ := strconv.ParseInt(raw, 10, 64)
	if err := transaction.Commit(); err != nil {
		return 0, fmt.Errorf("commit usage breakdown marker: %w", err)
	}
	return max(value, 0), nil
}

func (writer *Writer) IngestEvents(
	ctx context.Context,
	account string,
	events []Event,
	multipliers map[string]float64,
) (IngestCounters, error) {
	counters := IngestCounters{}
	account = strings.TrimSpace(account)
	if account == "" {
		return counters, errors.New("usage event account is required")
	}
	prepared := make([]preparedEvent, 0, len(events))
	for _, event := range events {
		counters.Received++
		if !businessEvent(event) {
			counters.Ignored++
			continue
		}
		digest := keyHash(stringValue(event["api_key"]))
		if digest == "" {
			counters.Unmapped++
			continue
		}
		prepared = append(prepared, preparedEvent{payload: event, keyDigest: digest})
	}
	if len(prepared) == 0 {
		return counters, nil
	}

	normalizedMultipliers := reasoningMultipliers(multipliers)
	policyVersion := reasoningPolicyVersion(normalizedMultipliers)
	now := writer.now().Unix()
	transaction, err := writer.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return counters, fmt.Errorf("begin usage event ingest: %w", err)
	}
	defer transaction.Rollback()

	identities, err := loadIdentities(ctx, transaction, prepared)
	if err != nil {
		return counters, err
	}
	timezone, err := loadWeekTimezone(ctx, transaction)
	if err != nil {
		return counters, err
	}
	for _, item := range prepared {
		identity, found := identities[item.keyDigest]
		if !found {
			counters.Unmapped++
			continue
		}
		event := normalizeEvent(account, item, identity, normalizedMultipliers, policyVersion, now)
		result, err := transaction.ExecContext(ctx, `
            INSERT OR IGNORE INTO usage_events(
                event_key, account, user_email, key_label, occurred_at,
                request_id, provider, model, alias, reasoning_effort,
                endpoint, failed, latency_ms,
                input_tokens, output_tokens, reasoning_tokens, cached_tokens,
                total_tokens, quota_multiplier, weighted_tokens,
                weight_policy_version, team_id, team_membership_version
            ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			event.EventKey, event.Account, event.UserEmail, event.KeyLabel, event.OccurredAt,
			event.RequestID, event.Provider, event.Model, event.Alias, event.ReasoningEffort,
			event.Endpoint, event.Failed, event.LatencyMS,
			event.InputTokens, event.OutputTokens, event.ReasoningTokens, event.CachedTokens,
			event.TotalTokens, event.QuotaMultiplier, event.WeightedTokens,
			event.WeightPolicyVersion, event.TeamID, event.TeamMembershipVersion,
		)
		if err != nil {
			return counters, fmt.Errorf("insert usage event: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return counters, fmt.Errorf("read usage event insert result: %w", err)
		}
		if inserted == 0 {
			counters.Duplicate++
			continue
		}
		counters.Inserted++
		weekStart, _ := naturalWeekBounds(event.OccurredAt, timezone)
		if _, err := transaction.ExecContext(ctx, `
            INSERT INTO user_weekly_usage(
                user_email, week_start_at, total_tokens, weighted_tokens,
                request_count, updated_at
            ) VALUES (?, ?, ?, ?, 1, ?)
            ON CONFLICT(user_email, week_start_at) DO UPDATE SET
                total_tokens = total_tokens + excluded.total_tokens,
                weighted_tokens = weighted_tokens + excluded.weighted_tokens,
                request_count = request_count + 1,
                updated_at = excluded.updated_at`,
			event.UserEmail, weekStart, event.TotalTokens, event.WeightedTokens, now,
		); err != nil {
			return counters, fmt.Errorf("update materialized weekly usage: %w", err)
		}
	}
	if counters.Inserted > 0 {
		var lastEventID int64
		if err := transaction.GetContext(ctx, &lastEventID, "SELECT COALESCE(MAX(id), 0) FROM usage_events"); err != nil {
			return counters, fmt.Errorf("read last usage event id: %w", err)
		}
		for key, value := range map[string]string{
			weeklyUsageBackfillKey:    weeklyUsageBackfillVersion,
			weeklyUsageLastEventIDKey: strconv.FormatInt(lastEventID, 10),
		} {
			if _, err := transaction.ExecContext(
				ctx,
				"INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
				key,
				value,
			); err != nil {
				return counters, fmt.Errorf("update usage ingest marker: %w", err)
			}
		}
	}
	if err := transaction.Commit(); err != nil {
		return counters, fmt.Errorf("commit usage event ingest: %w", err)
	}
	return counters, nil
}

func (writer *Writer) ConfigurePersonalQuotaReset(
	ctx context.Context,
	enabled bool,
	reschedule bool,
) (QuotaResetConfiguration, error) {
	now := writer.now().Unix()
	transaction, err := writer.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return QuotaResetConfiguration{}, fmt.Errorf("begin quota reset configuration: %w", err)
	}
	defer transaction.Rollback()
	location, err := loadWeekTimezone(ctx, transaction)
	if err != nil {
		return QuotaResetConfiguration{}, err
	}
	weekStart, weekEnd := naturalWeekBounds(now, location)
	result := QuotaResetConfiguration{Enabled: enabled, WeekStartAt: weekStart, WeekEndAt: weekEnd}
	expired, err := transaction.ExecContext(
		ctx,
		"DELETE FROM user_quota_policies WHERE reset_at IS NOT NULL AND reset_at <= ?",
		now,
	)
	if err != nil {
		return result, fmt.Errorf("delete expired personal quota policies: %w", err)
	}
	result.ExpiredPolicies, err = expired.RowsAffected()
	if err != nil {
		return result, fmt.Errorf("read expired personal quota policy count: %w", err)
	}
	if enabled {
		statement := "UPDATE user_quota_policies SET reset_at = ? WHERE reset_at IS NULL"
		if reschedule {
			statement = "UPDATE user_quota_policies SET reset_at = ?"
		}
		updated, err := transaction.ExecContext(ctx, statement, weekEnd)
		if err != nil {
			return result, fmt.Errorf("schedule personal quota policy reset: %w", err)
		}
		result.ScheduledPolicies, err = updated.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("read scheduled personal quota policy count: %w", err)
		}
	} else {
		updated, err := transaction.ExecContext(
			ctx,
			"UPDATE user_quota_policies SET reset_at = NULL WHERE reset_at IS NOT NULL",
		)
		if err != nil {
			return result, fmt.Errorf("cancel personal quota policy reset: %w", err)
		}
		result.CancelledSchedules, err = updated.RowsAffected()
		if err != nil {
			return result, fmt.Errorf("read cancelled personal quota policy count: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit quota reset configuration: %w", err)
	}
	return result, nil
}

func (writer *Writer) WeeklyQuotas(
	ctx context.Context,
	userEmails []string,
	defaultWeeklyTokens *int64,
) (map[string]WeeklyQuota, error) {
	users := normalizedEmailSet(userEmails)
	result := make(map[string]WeeklyQuota, len(users))
	if len(users) == 0 {
		return result, nil
	}
	now := writer.now().Unix()
	transaction, err := writer.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin weekly quota snapshot: %w", err)
	}
	defer transaction.Rollback()
	location, err := loadWeekTimezone(ctx, transaction)
	if err != nil {
		return nil, err
	}
	weekStart, weekEnd := naturalWeekBounds(now, location)

	type usageRow struct {
		UserEmail      string `db:"user_email"`
		TotalTokens    int64  `db:"total_tokens"`
		WeightedTokens int64  `db:"weighted_tokens"`
	}
	type policyRow struct {
		UserEmail    string        `db:"user_email"`
		WeeklyTokens sql.NullInt64 `db:"weekly_tokens"`
		UpdatedAt    int64         `db:"updated_at"`
		CreatedBy    string        `db:"created_by"`
		ResetAt      sql.NullInt64 `db:"reset_at"`
	}
	type adjustmentRow struct {
		UserEmail        string `db:"user_email"`
		BonusTokens      int64  `db:"bonus_tokens"`
		UsageResetTokens int64  `db:"usage_reset_tokens"`
		AdjustmentCount  int64  `db:"adjustment_count"`
	}
	usageByUser := make(map[string]usageRow)
	policyByUser := make(map[string]policyRow)
	adjustmentByUser := make(map[string]adjustmentRow)
	for offset := 0; offset < len(users); offset += 500 {
		end := min(offset+500, len(users))
		batch := users[offset:end]
		query, arguments, err := sqlx.In(`
            SELECT user_email, total_tokens, weighted_tokens
              FROM user_weekly_usage
             WHERE week_start_at = ? AND user_email IN (?)`, weekStart, batch)
		if err != nil {
			return nil, fmt.Errorf("build weekly usage lookup: %w", err)
		}
		rows := make([]usageRow, 0, len(batch))
		if err := transaction.SelectContext(ctx, &rows, transaction.Rebind(query), arguments...); err != nil {
			return nil, fmt.Errorf("read weekly usage: %w", err)
		}
		for _, row := range rows {
			usageByUser[row.UserEmail] = row
		}

		query, arguments, err = sqlx.In(`
            SELECT user_email, weekly_tokens, updated_at, created_by, reset_at
              FROM user_quota_policies
             WHERE user_email IN (?) AND (reset_at IS NULL OR reset_at > ?)`, batch, now)
		if err != nil {
			return nil, fmt.Errorf("build weekly policy lookup: %w", err)
		}
		policies := make([]policyRow, 0, len(batch))
		if err := transaction.SelectContext(ctx, &policies, transaction.Rebind(query), arguments...); err != nil {
			return nil, fmt.Errorf("read weekly quota policies: %w", err)
		}
		for _, row := range policies {
			policyByUser[row.UserEmail] = row
		}

		query, arguments, err = sqlx.In(`
            SELECT user_email,
                   SUM(CASE WHEN action = 'bonus' THEN token_amount ELSE 0 END) AS bonus_tokens,
                   SUM(CASE WHEN action = 'usage_reset' THEN token_amount ELSE 0 END) AS usage_reset_tokens,
                   COUNT(*) AS adjustment_count
              FROM user_quota_adjustments
             WHERE week_start_at = ? AND user_email IN (?)
             GROUP BY user_email`, weekStart, batch)
		if err != nil {
			return nil, fmt.Errorf("build weekly quota adjustment lookup: %w", err)
		}
		adjustments := make([]adjustmentRow, 0, len(batch))
		if err := transaction.SelectContext(ctx, &adjustments, transaction.Rebind(query), arguments...); err != nil {
			return nil, fmt.Errorf("read weekly quota adjustments: %w", err)
		}
		for _, row := range adjustments {
			adjustmentByUser[row.UserEmail] = row
		}
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit weekly quota snapshot: %w", err)
	}

	for _, user := range users {
		usage := usageByUser[user]
		policy, hasPolicy := policyByUser[user]
		adjustment := adjustmentByUser[user]
		baseLimit := cloneInt64Pointer(defaultWeeklyTokens)
		mode := "inherit"
		source := "default"
		var policyTokens, policyUpdatedAt, policyResetAt *int64
		var policyUpdatedBy *string
		if hasPolicy {
			policyUpdatedAt = int64Pointer(policy.UpdatedAt)
			policyUpdatedBy = stringPointer(policy.CreatedBy)
			if policy.ResetAt.Valid {
				policyResetAt = int64Pointer(policy.ResetAt.Int64)
			}
			if policy.WeeklyTokens.Valid {
				mode = "custom"
				source = "user_custom"
				baseLimit = int64Pointer(policy.WeeklyTokens.Int64)
				policyTokens = int64Pointer(policy.WeeklyTokens.Int64)
			} else {
				mode = "unlimited"
				source = "user_unlimited"
				baseLimit = nil
			}
		}
		var limit *int64
		if baseLimit != nil {
			limit = int64Pointer(*baseLimit + adjustment.BonusTokens)
		}
		used := max(usage.WeightedTokens-adjustment.UsageResetTokens, 0)
		var remaining *int64
		var usedPercent *float64
		if limit != nil {
			remaining = int64Pointer(max(*limit-used, 0))
			percent := math.Round(float64(used)*10000/float64(*limit)) / 100
			usedPercent = &percent
		}
		result[user] = WeeklyQuota{
			Period: "natural_week", Timezone: location.String(),
			WeekStartAt: weekStart, WeekEndAt: weekEnd,
			LimitTokens: limit, BaseLimitTokens: baseLimit, BonusTokens: adjustment.BonusTokens,
			UsedTokens: used, WeightedUsedTokens: used,
			RawUsedTokens: usage.TotalTokens, UnweightedUsedTokens: usage.TotalTokens,
			WeightedRawUsedTokens: usage.WeightedTokens,
			UsageResetTokens:      adjustment.UsageResetTokens,
			RemainingTokens:       remaining,
			UsedPercent:           usedPercent,
			LimitReached:          limit != nil && used >= *limit,
			Source:                source,
			PolicyMode:            mode,
			PolicyTokens:          policyTokens,
			PolicyUpdatedAt:       policyUpdatedAt,
			PolicyUpdatedBy:       policyUpdatedBy,
			PolicyResetAt:         policyResetAt,
			DefaultLimitTokens:    cloneInt64Pointer(defaultWeeklyTokens),
			Unlimited:             limit == nil,
			SoftLimit:             true,
			QuotaUnit:             "weighted_tokens",
			AdjustmentCount:       adjustment.AdjustmentCount,
		}
	}
	return result, nil
}

func (writer *Writer) UpdateCollectorStatus(ctx context.Context, lastError string) error {
	now := writer.now().Unix()
	errorText := truncateRunes(lastError, 500)
	transaction, err := writer.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin collector status update: %w", err)
	}
	defer transaction.Rollback()
	for key, value := range map[string]string{
		"collector_heartbeat_at": strconv.FormatInt(now, 10),
		"collector_last_error":   errorText,
	} {
		if _, err := transaction.ExecContext(
			ctx,
			"INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
			key,
			value,
		); err != nil {
			return fmt.Errorf("write collector status: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit collector status update: %w", err)
	}
	return nil
}

func (writer *Writer) Status(ctx context.Context) (CollectorStatus, error) {
	return loadCollectorStatus(ctx, writer.db, writer.now)
}

func loadCollectorStatus(ctx context.Context, database *sqlx.DB, now func() time.Time) (CollectorStatus, error) {
	metaRows := make([]struct {
		Key   string `db:"key"`
		Value string `db:"value"`
	}, 0)
	if err := database.SelectContext(ctx, &metaRows, "SELECT key, value FROM usage_meta"); err != nil {
		return CollectorStatus{}, fmt.Errorf("read collector metadata: %w", err)
	}
	meta := make(map[string]string, len(metaRows))
	for _, row := range metaRows {
		meta[row.Key] = row.Value
	}
	event := struct {
		Count   int64         `db:"count"`
		FirstAt sql.NullInt64 `db:"first_at"`
		LastAt  sql.NullInt64 `db:"last_at"`
	}{}
	if err := database.GetContext(ctx, &event, `
        SELECT COUNT(*) AS count, MIN(occurred_at) AS first_at, MAX(occurred_at) AS last_at
          FROM usage_events`); err != nil {
		return CollectorStatus{}, fmt.Errorf("read collector event status: %w", err)
	}
	heartbeat := parseNonNegativeInt(meta["collector_heartbeat_at"])
	lastError := meta["collector_last_error"]
	state := "starting"
	if heartbeat > 0 {
		state = "degraded"
		if now().Unix()-heartbeat <= 15 && lastError == "" {
			state = "healthy"
		}
	}
	return CollectorStatus{
		Status: state, HeartbeatAt: heartbeat, LastError: lastError,
		EventCount: event.Count, CollectionStartedAt: nullInt64Value(event.FirstAt),
		UsageBreakdownStartedAt: parseNonNegativeInt(meta[breakdownStartedAtKey]),
		LastEventAt:             nullInt64Value(event.LastAt),
	}, nil
}

func (writer *Writer) RebuildWeeklyUsage(ctx context.Context) (RebuildResult, error) {
	transaction, err := writer.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return RebuildResult{}, fmt.Errorf("begin weekly usage rebuild: %w", err)
	}
	defer transaction.Rollback()
	location, err := loadWeekTimezone(ctx, transaction)
	if err != nil {
		return RebuildResult{}, err
	}
	result, err := writer.rebuildWeeklyUsage(ctx, transaction, location)
	if err != nil {
		return RebuildResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return RebuildResult{}, fmt.Errorf("commit weekly usage rebuild: %w", err)
	}
	result.Backfilled = true
	return result, nil
}

func (writer *Writer) EnsureWeekTimezone(
	ctx context.Context,
	timezone string,
) (bool, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		timezone = defaultWeekTimezone
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return false, fmt.Errorf("load weekly usage timezone %q: %w", timezone, err)
	}
	transaction, err := writer.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin weekly timezone update: %w", err)
	}
	defer transaction.Rollback()
	var previous string
	err = transaction.GetContext(
		ctx,
		&previous,
		"SELECT value FROM usage_meta WHERE key = ?",
		weeklyUsageTimezoneKey,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read previous weekly usage timezone: %w", err)
	}
	if previous == timezone {
		return false, transaction.Commit()
	}
	if _, err := writer.rebuildWeeklyUsage(ctx, transaction, location); err != nil {
		return false, err
	}
	adjustments := make([]struct {
		ID        int64 `db:"id"`
		CreatedAt int64 `db:"created_at"`
	}, 0)
	if err := transaction.SelectContext(
		ctx,
		&adjustments,
		"SELECT id, created_at FROM user_quota_adjustments",
	); err != nil {
		return false, fmt.Errorf("read quota adjustments for timezone update: %w", err)
	}
	for _, adjustment := range adjustments {
		weekStart, _ := naturalWeekBounds(adjustment.CreatedAt, location)
		if _, err := transaction.ExecContext(
			ctx,
			"UPDATE user_quota_adjustments SET week_start_at = ? WHERE id = ?",
			weekStart,
			adjustment.ID,
		); err != nil {
			return false, fmt.Errorf("re-bucket quota adjustment: %w", err)
		}
	}
	if _, err := transaction.ExecContext(
		ctx,
		"INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
		weeklyUsageTimezoneKey,
		timezone,
	); err != nil {
		return false, fmt.Errorf("write weekly usage timezone: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return false, fmt.Errorf("commit weekly timezone update: %w", err)
	}
	return true, nil
}

func (writer *Writer) rebuildWeeklyUsage(
	ctx context.Context,
	transaction *sqlx.Tx,
	location *time.Location,
) (RebuildResult, error) {
	rows := make([]struct {
		ID                  int64  `db:"id"`
		UserEmail           string `db:"user_email"`
		OccurredAt          int64  `db:"occurred_at"`
		TotalTokens         int64  `db:"total_tokens"`
		WeightedTokens      int64  `db:"weighted_tokens"`
		WeightPolicyVersion string `db:"weight_policy_version"`
	}, 0)
	if err := transaction.SelectContext(ctx, &rows, `
        SELECT id, user_email, occurred_at, total_tokens, weighted_tokens, weight_policy_version
          FROM usage_events ORDER BY id`); err != nil {
		return RebuildResult{}, fmt.Errorf("read events for weekly usage rebuild: %w", err)
	}
	type counterKey struct {
		UserEmail   string
		WeekStartAt int64
	}
	type counterValue struct {
		Raw, Weighted, Requests int64
	}
	counters := make(map[counterKey]counterValue)
	for _, row := range rows {
		weekStart, _ := naturalWeekBounds(row.OccurredAt, location)
		key := counterKey{UserEmail: row.UserEmail, WeekStartAt: weekStart}
		value := counters[key]
		weighted := row.WeightedTokens
		if row.WeightPolicyVersion == "legacy-v1" && row.TotalTokens > 0 && weighted == 0 {
			weighted = row.TotalTokens
		}
		value.Raw += row.TotalTokens
		value.Weighted += weighted
		value.Requests++
		counters[key] = value
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM user_weekly_usage"); err != nil {
		return RebuildResult{}, fmt.Errorf("clear materialized weekly usage: %w", err)
	}
	keys := make([]counterKey, 0, len(counters))
	for key := range counters {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(left, right int) bool {
		if keys[left].UserEmail == keys[right].UserEmail {
			return keys[left].WeekStartAt < keys[right].WeekStartAt
		}
		return keys[left].UserEmail < keys[right].UserEmail
	})
	now := writer.now().Unix()
	for _, key := range keys {
		value := counters[key]
		if _, err := transaction.ExecContext(ctx, `
            INSERT INTO user_weekly_usage(
                user_email, week_start_at, total_tokens, weighted_tokens, request_count, updated_at
            ) VALUES (?, ?, ?, ?, ?, ?)`,
			key.UserEmail, key.WeekStartAt, value.Raw, value.Weighted, value.Requests, now,
		); err != nil {
			return RebuildResult{}, fmt.Errorf("write rebuilt weekly usage: %w", err)
		}
	}
	lastEventID := int64(0)
	if len(rows) > 0 {
		lastEventID = rows[len(rows)-1].ID
	}
	for key, value := range map[string]string{
		weeklyUsageBackfillKey:    weeklyUsageBackfillVersion,
		weeklyUsageLastEventIDKey: strconv.FormatInt(lastEventID, 10),
	} {
		if _, err := transaction.ExecContext(
			ctx,
			"INSERT OR REPLACE INTO usage_meta(key, value) VALUES (?, ?)",
			key,
			value,
		); err != nil {
			return RebuildResult{}, fmt.Errorf("write weekly usage rebuild marker: %w", err)
		}
	}
	return RebuildResult{Events: len(rows), Counters: len(counters)}, nil
}

type preparedEvent struct {
	payload   Event
	keyDigest string
}

type normalizedEvent struct {
	EventKey              string
	Account               string
	UserEmail             string
	KeyLabel              string
	OccurredAt            int64
	RequestID             string
	Provider              string
	Model                 string
	Alias                 string
	ReasoningEffort       string
	Endpoint              string
	Failed                int
	LatencyMS             int64
	InputTokens           int64
	OutputTokens          int64
	ReasoningTokens       int64
	CachedTokens          int64
	TotalTokens           int64
	QuotaMultiplier       float64
	WeightedTokens        int64
	WeightPolicyVersion   string
	TeamID                string
	TeamMembershipVersion int64
}

func loadIdentities(
	ctx context.Context,
	transaction *sqlx.Tx,
	prepared []preparedEvent,
) (map[string]identityRow, error) {
	digestSet := make(map[string]struct{}, len(prepared))
	for _, event := range prepared {
		digestSet[event.keyDigest] = struct{}{}
	}
	digests := make([]string, 0, len(digestSet))
	for digest := range digestSet {
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	query, arguments, err := sqlx.In(`
        SELECT key_hash, key_label, user_email, account, team_id, team_membership_version
          FROM key_identities
         WHERE key_hash IN (?)`, digests)
	if err != nil {
		return nil, fmt.Errorf("build usage identity lookup: %w", err)
	}
	rows := make([]identityRow, 0, len(digests))
	if err := transaction.SelectContext(ctx, &rows, transaction.Rebind(query), arguments...); err != nil {
		return nil, fmt.Errorf("load usage identities: %w", err)
	}
	result := make(map[string]identityRow, len(rows))
	for _, row := range rows {
		result[row.KeyHash] = row
	}
	return result, nil
}

func loadWeekTimezone(ctx context.Context, transaction *sqlx.Tx) (*time.Location, error) {
	name := defaultWeekTimezone
	var stored string
	err := transaction.GetContext(
		ctx,
		&stored,
		"SELECT value FROM usage_meta WHERE key = ?",
		weeklyUsageTimezoneKey,
	)
	if err == nil && strings.TrimSpace(stored) != "" {
		name = strings.TrimSpace(stored)
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("read weekly usage timezone: %w", err)
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("load weekly usage timezone %q: %w", name, err)
	}
	return location, nil
}

func normalizeEvent(
	account string,
	item preparedEvent,
	identity identityRow,
	multipliers map[string]float64,
	policyVersion string,
	now int64,
) normalizedEvent {
	payload := item.payload
	tokens, _ := payload["tokens"].(map[string]any)
	inputTokens := nonNegativeInt(tokens["input_tokens"])
	outputTokens := nonNegativeInt(tokens["output_tokens"])
	totalTokens := nonNegativeInt(tokens["total_tokens"])
	if totalTokens == 0 && (inputTokens != 0 || outputTokens != 0) {
		totalTokens = inputTokens + outputTokens
	}
	model := strings.TrimSpace(stringValue(payload["model"]))
	alias := strings.TrimSpace(stringValue(payload["alias"]))
	if alias == "" {
		alias = model
	}
	reasoningEffort := normalizeReasoningEffort(payload["reasoning_effort"])
	multiplier := multipliers[reasoningEffort]
	requestID := strings.TrimSpace(stringValue(payload["request_id"]))
	return normalizedEvent{
		EventKey:              usageEventKey(account, requestID, item.keyDigest, payload),
		Account:               account,
		UserEmail:             identity.UserEmail,
		KeyLabel:              identity.KeyLabel,
		OccurredAt:            eventTimestamp(payload["timestamp"], now),
		RequestID:             requestID,
		Provider:              stringValue(payload["provider"]),
		Model:                 model,
		Alias:                 alias,
		ReasoningEffort:       reasoningEffort,
		Endpoint:              stringValue(payload["endpoint"]),
		Failed:                boolInteger(truthy(payload["failed"])),
		LatencyMS:             nonNegativeInt(payload["latency_ms"]),
		InputTokens:           inputTokens,
		OutputTokens:          outputTokens,
		ReasoningTokens:       nonNegativeInt(tokens["reasoning_tokens"]),
		CachedTokens:          nonNegativeInt(tokens["cached_tokens"]),
		TotalTokens:           totalTokens,
		QuotaMultiplier:       multiplier,
		WeightedTokens:        int64(math.Floor(float64(totalTokens)*multiplier + 0.5)),
		WeightPolicyVersion:   policyVersion,
		TeamID:                identity.TeamID,
		TeamMembershipVersion: max(identity.TeamMembershipVersion, 0),
	}
}

func keyHash(value string) string {
	raw := strings.TrimSpace(value)
	if len(raw) >= 7 && strings.EqualFold(raw[:7], "bearer ") {
		raw = strings.TrimSpace(raw[7:])
	}
	if raw == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(digest[:])
}

func businessEvent(payload Event) bool {
	endpoint := strings.ToLower(strings.TrimSpace(stringValue(payload["endpoint"])))
	return !strings.HasSuffix(endpoint, "/v1/models")
}

func usageEventKey(account, requestID, keyDigest string, payload Event) string {
	if requestID != "" {
		return account + ":" + keyDigest + ":" + requestID
	}
	safePayload := map[string]any{
		"account":          account,
		"key_hash":         keyDigest,
		"timestamp":        payload["timestamp"],
		"endpoint":         payload["endpoint"],
		"model":            payload["model"],
		"alias":            payload["alias"],
		"reasoning_effort": payload["reasoning_effort"],
		"failed":           payload["failed"],
		"tokens":           payload["tokens"],
	}
	encoded := canonicalJSON(safePayload)
	digest := sha256.Sum256(encoded)
	return account + ":sha256:" + hex.EncodeToString(digest[:])
}

func canonicalJSON(value any) []byte {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return []byte("null")
	}
	return bytes.TrimSuffix(output.Bytes(), []byte("\n"))
}

func reasoningMultipliers(configuration map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(reasoningEfforts))
	for _, effort := range reasoningEfforts {
		fallback := 1.0
		if effort == "max" {
			fallback = 2.0
		}
		value, found := configuration[reasoningMultiplierConfigBase+effort]
		if !found {
			value, found = configuration[effort]
		}
		if !found || math.IsNaN(value) || math.IsInf(value, 0) || value <= 0 {
			value = fallback
		}
		result[effort] = value
	}
	return result
}

func reasoningPolicyVersion(multipliers map[string]float64) string {
	keys := append([]string(nil), reasoningEfforts...)
	sort.Strings(keys)
	var semantic strings.Builder
	semantic.WriteByte('{')
	for index, key := range keys {
		if index > 0 {
			semantic.WriteByte(',')
		}
		semantic.WriteString(strconv.Quote(key))
		semantic.WriteByte(':')
		raw := strconv.FormatFloat(multipliers[key], 'f', -1, 64)
		if !strings.Contains(raw, ".") {
			raw += ".0"
		}
		semantic.WriteString(raw)
	}
	semantic.WriteByte('}')
	digest := sha256.Sum256([]byte(semantic.String()))
	return reasoningPolicyVersionPrefix + hex.EncodeToString(digest[:])[:12]
}

func normalizeReasoningEffort(value any) string {
	effort := strings.ToLower(strings.TrimSpace(stringValue(value)))
	for _, candidate := range reasoningEfforts {
		if effort == candidate {
			return effort
		}
	}
	return "unknown"
}

func naturalWeekBounds(timestamp int64, location *time.Location) (int64, int64) {
	if location == nil {
		location = time.UTC
	}
	local := time.Unix(timestamp, 0).In(location)
	daysSinceMonday := (int(local.Weekday()) + 6) % 7
	startDate := local.AddDate(0, 0, -daysSinceMonday)
	start := time.Date(startDate.Year(), startDate.Month(), startDate.Day(), 0, 0, 0, 0, location)
	end := start.AddDate(0, 0, 7)
	return start.Unix(), end.Unix()
}

func eventTimestamp(value any, fallback int64) int64 {
	switch value := value.(type) {
	case int:
		return int64(value)
	case int64:
		return value
	case float64:
		return int64(value)
	case float32:
		return int64(value)
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			return integer
		}
		if decimal, err := value.Float64(); err == nil {
			return int64(decimal)
		}
	case string:
		raw := strings.TrimSpace(value)
		layouts := []string{
			time.RFC3339Nano,
			"2006-01-02 15:04:05.999999999Z07:00",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, raw); err == nil {
				return parsed.Unix()
			}
		}
	}
	return fallback
}

func nonNegativeInt(value any) int64 {
	var result int64
	switch value := value.(type) {
	case int:
		result = int64(value)
	case int8:
		result = int64(value)
	case int16:
		result = int64(value)
	case int32:
		result = int64(value)
	case int64:
		result = value
	case bool:
		if value {
			result = 1
		}
	case uint:
		if uint64(value) > math.MaxInt64 {
			return 0
		}
		result = int64(value)
	case uint64:
		if value > math.MaxInt64 {
			return 0
		}
		result = int64(value)
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value > math.MaxInt64 || value < math.MinInt64 {
			return 0
		}
		result = int64(value)
	case json.Number:
		if integer, err := value.Int64(); err == nil {
			result = integer
		} else if decimal, err := value.Float64(); err == nil {
			result = int64(decimal)
		} else {
			return 0
		}
	case string:
		integer, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return 0
		}
		result = integer
	default:
		return 0
	}
	return max(result, 0)
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func truthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case int:
		return value != 0
	case int64:
		return value != 0
	case float64:
		return value != 0
	case json.Number:
		return value.String() != "" && value.String() != "0"
	case []any:
		return len(value) != 0
	case map[string]any:
		return len(value) != 0
	default:
		return true
	}
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizedEmailSet(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		email := strings.ToLower(strings.TrimSpace(value))
		if email != "" {
			seen[email] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func int64Pointer(value int64) *int64 {
	result := value
	return &result
}

func cloneInt64Pointer(value *int64) *int64 {
	if value == nil {
		return nil
	}
	return int64Pointer(*value)
}

func stringPointer(value string) *string {
	result := value
	return &result
}

func parseNonNegativeInt(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0
	}
	return max(parsed, 0)
}

func nullInt64Value(value sql.NullInt64) int64 {
	if !value.Valid {
		return 0
	}
	return value.Int64
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
