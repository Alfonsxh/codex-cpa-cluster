package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
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
	DatabaseRelativePath         = "state/usage.sqlite3"
	minimumReadableSchemaVersion = 10
	breakdownStartedAtKey        = "usage_breakdown_started_at"
)

var requiredUsageEventColumns = []string{
	"account",
	"user_email",
	"occurred_at",
	"model",
	"alias",
	"reasoning_effort",
	"failed",
	"input_tokens",
	"output_tokens",
	"reasoning_tokens",
	"cached_tokens",
	"total_tokens",
	"weighted_tokens",
	"weight_policy_version",
}

// Store is a live, read-only view of the usage database still written by the
// Python collector during the dual-version migration. It deliberately owns no
// application mutex and relies on SQLite WAL snapshots for consistent reads.
type Store struct {
	db  *sqlx.DB
	now func() time.Time
}

func OpenReadOnly(root string, now func() time.Time) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("usage root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve usage root: %w", err)
	}
	return OpenReadOnlyPath(filepath.Join(absoluteRoot, DatabaseRelativePath), now)
}

func OpenReadOnlyPath(path string, now func() time.Time) (*Store, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve usage database: %w", err)
	}
	if _, err := os.Stat(absolutePath); err != nil {
		return nil, fmt.Errorf("open existing usage database: %w", err)
	}
	dsn := &url.URL{Scheme: "file", Path: absolutePath}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "query_only(1)")
	dsn.RawQuery = query.Encode()
	database, err := sqlx.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open usage database: %w", err)
	}
	database.SetMaxOpenConns(6)
	database.SetMaxIdleConns(3)
	database.SetConnMaxIdleTime(5 * time.Minute)
	if err := database.Ping(); err != nil {
		database.Close()
		return nil, fmt.Errorf("connect to usage database: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	store := &Store{db: database, now: now}
	if err := store.validateSchema(context.Background()); err != nil {
		database.Close()
		return nil, err
	}
	return store, nil
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

// Status reads collector health through the query-only Admin connection. It
// intentionally performs no schema, WAL, or heartbeat mutation.
func (store *Store) Status(ctx context.Context) (CollectorStatus, error) {
	return loadCollectorStatus(ctx, store.db, store.now)
}

func (store *Store) validateSchema(ctx context.Context) error {
	var version int
	if err := store.db.GetContext(ctx, &version, "PRAGMA user_version"); err != nil {
		return fmt.Errorf("read usage schema version: %w", err)
	}
	if version < minimumReadableSchemaVersion {
		return fmt.Errorf(
			"usage schema version %d is older than required version %d",
			version,
			minimumReadableSchemaVersion,
		)
	}
	var metaTable string
	if err := store.db.GetContext(ctx, &metaTable, `
        SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'usage_meta'`); err != nil {
		return fmt.Errorf("validate usage metadata table: %w", err)
	}
	columns := make(map[string]struct{})
	rows, err := store.db.QueryxContext(ctx, "PRAGMA table_info(usage_events)")
	if err != nil {
		return fmt.Errorf("inspect usage event schema: %w", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return fmt.Errorf("scan usage event schema: %w", err)
		}
		columns[name] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close usage event schema: %w", err)
	}
	missing := make([]string, 0)
	for _, name := range requiredUsageEventColumns {
		if _, found := columns[name]; !found {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("usage database is missing required columns: %s", strings.Join(missing, ", "))
	}
	return nil
}

type RawMetrics struct {
	RequestCount    int64 `json:"request_count"`
	SuccessCount    int64 `json:"success_count"`
	FailedCount     int64 `json:"failed_count"`
	InputTokens     int64 `json:"input_tokens"`
	OutputTokens    int64 `json:"output_tokens"`
	ReasoningTokens int64 `json:"reasoning_tokens"`
	CachedTokens    int64 `json:"cached_tokens"`
	TotalTokens     int64 `json:"total_tokens"`
	LastUsedAt      int64 `json:"last_used_at"`
}

type WeightedMetrics struct {
	RawMetrics
	WeightedTokens int64 `json:"weighted_tokens"`
}

type UserTotals struct {
	WeightedMetrics
	KnownEffortCount int64 `json:"known_effort_count"`
}

type UserModelUsage struct {
	Model string `json:"model"`
	WeightedMetrics
}

type UserEffortUsage struct {
	ReasoningEffort string `json:"reasoning_effort"`
	WeightedMetrics
}

type UserCombinationUsage struct {
	Account         string `json:"account"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	WeightedMetrics
}

type UserBreakdown struct {
	CollectionStartedAt int64                  `json:"collection_started_at"`
	EffectiveStartAt    int64                  `json:"effective_start_at"`
	Totals              UserTotals             `json:"totals"`
	Models              []UserModelUsage       `json:"models"`
	ReasoningEfforts    []UserEffortUsage      `json:"reasoning_efforts"`
	Combinations        []UserCombinationUsage `json:"combinations"`
}

type UserAccountUsage struct {
	Account string `db:"account" json:"account"`
	WeightedMetrics
}

type UserAccountSummary struct {
	Totals   WeightedMetrics    `json:"totals"`
	Accounts []UserAccountUsage `json:"accounts"`
}

type AccountModelUsage struct {
	Model string `json:"model"`
	RawMetrics
}

type AccountCombinationUsage struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	RawMetrics
}

type AccountBreakdown struct {
	CollectionStartedAt int64                     `json:"collection_started_at"`
	EffectiveStartAt    int64                     `json:"effective_start_at"`
	Totals              RawMetrics                `json:"totals"`
	Models              []AccountModelUsage       `json:"models"`
	Combinations        []AccountCombinationUsage `json:"combinations"`
}

type breakdownRow struct {
	Account         string `db:"account"`
	Model           string `db:"model"`
	ReasoningEffort string `db:"reasoning_effort"`
	RequestCount    int64  `db:"request_count"`
	SuccessCount    int64  `db:"success_count"`
	FailedCount     int64  `db:"failed_count"`
	InputTokens     int64  `db:"input_tokens"`
	OutputTokens    int64  `db:"output_tokens"`
	ReasoningTokens int64  `db:"reasoning_tokens"`
	CachedTokens    int64  `db:"cached_tokens"`
	TotalTokens     int64  `db:"total_tokens"`
	WeightedTokens  int64  `db:"weighted_tokens"`
	LastUsedAt      int64  `db:"last_used_at"`
}

func (row breakdownRow) rawMetrics() RawMetrics {
	return RawMetrics{
		RequestCount: row.RequestCount, SuccessCount: row.SuccessCount, FailedCount: row.FailedCount,
		InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
		ReasoningTokens: row.ReasoningTokens, CachedTokens: row.CachedTokens,
		TotalTokens: row.TotalTokens, LastUsedAt: row.LastUsedAt,
	}
}

func (row breakdownRow) weightedMetrics() WeightedMetrics {
	return WeightedMetrics{RawMetrics: row.rawMetrics(), WeightedTokens: row.WeightedTokens}
}

func addRaw(target *RawMetrics, value RawMetrics) {
	target.RequestCount += value.RequestCount
	target.SuccessCount += value.SuccessCount
	target.FailedCount += value.FailedCount
	target.InputTokens += value.InputTokens
	target.OutputTokens += value.OutputTokens
	target.ReasoningTokens += value.ReasoningTokens
	target.CachedTokens += value.CachedTokens
	target.TotalTokens += value.TotalTokens
	if value.LastUsedAt > target.LastUsedAt {
		target.LastUsedAt = value.LastUsedAt
	}
}

func addWeighted(target *WeightedMetrics, value WeightedMetrics) {
	addRaw(&target.RawMetrics, value.RawMetrics)
	target.WeightedTokens += value.WeightedTokens
}

func (store *Store) UserBreakdown(
	ctx context.Context,
	userEmail string,
	account string,
	requestedStartAt int64,
	endAt *int64,
) (UserBreakdown, error) {
	result := UserBreakdown{
		Models: make([]UserModelUsage, 0), ReasoningEfforts: make([]UserEffortUsage, 0),
		Combinations: make([]UserCombinationUsage, 0),
	}
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, fmt.Errorf("begin user usage snapshot: %w", err)
	}
	defer transaction.Rollback()
	result.CollectionStartedAt, err = breakdownStart(ctx, transaction)
	if err != nil {
		return result, err
	}
	result.EffectiveStartAt = maxInt64(result.CollectionStartedAt, requestedStartAt)
	if result.CollectionStartedAt == 0 {
		return result, transaction.Commit()
	}
	clauses := []string{"user_email = ?", "occurred_at >= ?", "alias != ''"}
	parameters := []any{strings.ToLower(strings.TrimSpace(userEmail)), result.EffectiveStartAt}
	if endAt != nil {
		clauses = append(clauses, "occurred_at < ?")
		parameters = append(parameters, *endAt)
	}
	if strings.TrimSpace(account) != "" {
		clauses = append(clauses, "account = ?")
		parameters = append(parameters, strings.TrimSpace(account))
	}
	rows := make([]breakdownRow, 0)
	query := `
        SELECT account,
               COALESCE(NULLIF(model, ''), 'unknown') AS model,
               COALESCE(NULLIF(reasoning_effort, ''), 'unknown') AS reasoning_effort,
               COUNT(*) AS request_count,
               SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
               SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
               SUM(CASE WHEN failed = 0 THEN input_tokens ELSE 0 END) AS input_tokens,
               SUM(CASE WHEN failed = 0 THEN output_tokens ELSE 0 END) AS output_tokens,
               SUM(CASE WHEN failed = 0 THEN reasoning_tokens ELSE 0 END) AS reasoning_tokens,
               SUM(CASE WHEN failed = 0 THEN cached_tokens ELSE 0 END) AS cached_tokens,
               SUM(CASE WHEN failed = 0 THEN total_tokens ELSE 0 END) AS total_tokens,
               SUM(CASE WHEN failed = 0 THEN
                   CASE WHEN weight_policy_version = 'legacy-v1'
                             AND weighted_tokens = 0 AND total_tokens > 0
                        THEN total_tokens ELSE weighted_tokens END
                   ELSE 0 END) AS weighted_tokens,
               MAX(CASE WHEN failed = 0 THEN occurred_at ELSE 0 END) AS last_used_at
          FROM usage_events
         WHERE ` + strings.Join(clauses, " AND ") + `
         GROUP BY account, model, reasoning_effort`
	if err := transaction.SelectContext(ctx, &rows, query, parameters...); err != nil {
		return result, fmt.Errorf("query user usage breakdown: %w", err)
	}
	models := make(map[string]*UserModelUsage)
	efforts := make(map[string]*UserEffortUsage)
	for _, row := range rows {
		metrics := row.weightedMetrics()
		addWeighted(&result.Totals.WeightedMetrics, metrics)
		if row.ReasoningEffort != "unknown" {
			result.Totals.KnownEffortCount += row.SuccessCount
		}
		if row.SuccessCount <= 0 {
			continue
		}
		model := models[row.Model]
		if model == nil {
			model = &UserModelUsage{Model: row.Model}
			models[row.Model] = model
		}
		addWeighted(&model.WeightedMetrics, metrics)
		effort := efforts[row.ReasoningEffort]
		if effort == nil {
			effort = &UserEffortUsage{ReasoningEffort: row.ReasoningEffort}
			efforts[row.ReasoningEffort] = effort
		}
		addWeighted(&effort.WeightedMetrics, metrics)
		result.Combinations = append(result.Combinations, UserCombinationUsage{
			Account: row.Account, Model: row.Model, ReasoningEffort: row.ReasoningEffort,
			WeightedMetrics: metrics,
		})
	}
	for _, value := range models {
		result.Models = append(result.Models, *value)
	}
	for _, value := range efforts {
		result.ReasoningEfforts = append(result.ReasoningEfforts, *value)
	}
	sortUserBreakdown(&result)
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit user usage snapshot: %w", err)
	}
	return result, nil
}

// UserAccounts returns the compact per-account rows used by the self-service
// table. Unlike model breakdowns it includes failed requests and events without
// model aliases, matching the Python usage_for_users contract.
func (store *Store) UserAccounts(
	ctx context.Context,
	userEmail string,
	startAt int64,
	endAt *int64,
) (UserAccountSummary, error) {
	result := UserAccountSummary{Accounts: make([]UserAccountUsage, 0)}
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, fmt.Errorf("begin user account usage snapshot: %w", err)
	}
	defer transaction.Rollback()
	clauses := []string{"user_email = ?", "occurred_at >= ?"}
	parameters := []any{strings.ToLower(strings.TrimSpace(userEmail)), maxInt64(startAt, 0)}
	if endAt != nil {
		clauses = append(clauses, "occurred_at < ?")
		parameters = append(parameters, *endAt)
	}
	rows := make([]struct {
		Account         string `db:"account"`
		RequestCount    int64  `db:"request_count"`
		SuccessCount    int64  `db:"success_count"`
		FailedCount     int64  `db:"failed_count"`
		InputTokens     int64  `db:"input_tokens"`
		OutputTokens    int64  `db:"output_tokens"`
		ReasoningTokens int64  `db:"reasoning_tokens"`
		CachedTokens    int64  `db:"cached_tokens"`
		TotalTokens     int64  `db:"total_tokens"`
		WeightedTokens  int64  `db:"weighted_tokens"`
		LastUsedAt      int64  `db:"last_used_at"`
	}, 0)
	query := `
		SELECT account,
		       COUNT(*) AS request_count,
		       SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
		       SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
		       SUM(input_tokens) AS input_tokens,
		       SUM(output_tokens) AS output_tokens,
		       SUM(reasoning_tokens) AS reasoning_tokens,
		       SUM(cached_tokens) AS cached_tokens,
		       SUM(total_tokens) AS total_tokens,
		       SUM(CASE
		               WHEN weight_policy_version = 'legacy-v1'
		                    AND weighted_tokens = 0 AND total_tokens > 0
		               THEN total_tokens
		               ELSE weighted_tokens
		           END) AS weighted_tokens,
		       MAX(occurred_at) AS last_used_at
		  FROM usage_events
		 WHERE ` + strings.Join(clauses, " AND ") + `
		 GROUP BY account
		 ORDER BY account`
	if err := transaction.SelectContext(ctx, &rows, query, parameters...); err != nil {
		return result, fmt.Errorf("query user account usage: %w", err)
	}
	for _, row := range rows {
		metrics := WeightedMetrics{
			RawMetrics: RawMetrics{
				RequestCount: row.RequestCount, SuccessCount: row.SuccessCount, FailedCount: row.FailedCount,
				InputTokens: row.InputTokens, OutputTokens: row.OutputTokens,
				ReasoningTokens: row.ReasoningTokens, CachedTokens: row.CachedTokens,
				TotalTokens: row.TotalTokens, LastUsedAt: row.LastUsedAt,
			},
			WeightedTokens: row.WeightedTokens,
		}
		result.Accounts = append(result.Accounts, UserAccountUsage{Account: row.Account, WeightedMetrics: metrics})
		addWeighted(&result.Totals, metrics)
	}
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit user account usage snapshot: %w", err)
	}
	return result, nil
}

func (store *Store) AccountBreakdown(
	ctx context.Context,
	account string,
	requestedStartAt int64,
	endAt *int64,
) (AccountBreakdown, error) {
	result := AccountBreakdown{
		Models: make([]AccountModelUsage, 0), Combinations: make([]AccountCombinationUsage, 0),
	}
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, fmt.Errorf("begin account usage snapshot: %w", err)
	}
	defer transaction.Rollback()
	result.CollectionStartedAt, err = breakdownStart(ctx, transaction)
	if err != nil {
		return result, err
	}
	result.EffectiveStartAt = maxInt64(result.CollectionStartedAt, requestedStartAt)
	if result.CollectionStartedAt == 0 {
		return result, transaction.Commit()
	}
	clauses := []string{"account = ?", "occurred_at >= ?", "alias != ''"}
	parameters := []any{strings.TrimSpace(account), result.EffectiveStartAt}
	if endAt != nil {
		clauses = append(clauses, "occurred_at < ?")
		parameters = append(parameters, *endAt)
	}
	rows := make([]breakdownRow, 0)
	query := `
        SELECT '' AS account,
               COALESCE(NULLIF(model, ''), 'unknown') AS model,
               COALESCE(NULLIF(reasoning_effort, ''), 'unknown') AS reasoning_effort,
               COUNT(*) AS request_count,
               SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) AS success_count,
               SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) AS failed_count,
               SUM(input_tokens) AS input_tokens,
               SUM(output_tokens) AS output_tokens,
               SUM(reasoning_tokens) AS reasoning_tokens,
               SUM(cached_tokens) AS cached_tokens,
               SUM(total_tokens) AS total_tokens,
               0 AS weighted_tokens,
               MAX(occurred_at) AS last_used_at
          FROM usage_events
         WHERE ` + strings.Join(clauses, " AND ") + `
         GROUP BY model, reasoning_effort`
	if err := transaction.SelectContext(ctx, &rows, query, parameters...); err != nil {
		return result, fmt.Errorf("query account usage breakdown: %w", err)
	}
	models := make(map[string]*AccountModelUsage)
	for _, row := range rows {
		metrics := row.rawMetrics()
		addRaw(&result.Totals, metrics)
		model := models[row.Model]
		if model == nil {
			model = &AccountModelUsage{Model: row.Model}
			models[row.Model] = model
		}
		addRaw(&model.RawMetrics, metrics)
		result.Combinations = append(result.Combinations, AccountCombinationUsage{
			Model: row.Model, ReasoningEffort: row.ReasoningEffort, RawMetrics: metrics,
		})
	}
	for _, value := range models {
		result.Models = append(result.Models, *value)
	}
	sort.Slice(result.Models, func(left, right int) bool {
		if result.Models[left].TotalTokens != result.Models[right].TotalTokens {
			return result.Models[left].TotalTokens > result.Models[right].TotalTokens
		}
		return result.Models[left].Model < result.Models[right].Model
	})
	sort.Slice(result.Combinations, func(left, right int) bool {
		if result.Combinations[left].TotalTokens != result.Combinations[right].TotalTokens {
			return result.Combinations[left].TotalTokens > result.Combinations[right].TotalTokens
		}
		if result.Combinations[left].Model != result.Combinations[right].Model {
			return result.Combinations[left].Model < result.Combinations[right].Model
		}
		return result.Combinations[left].ReasoningEffort < result.Combinations[right].ReasoningEffort
	})
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit account usage snapshot: %w", err)
	}
	return result, nil
}

func (store *Store) RefreshActiveUsersLastHour(ctx context.Context) (map[string]int, error) {
	rows := make([]struct {
		Account string `db:"account"`
		Users   int    `db:"active_users"`
	}, 0)
	if err := store.db.SelectContext(ctx, &rows, `
        SELECT account, COUNT(DISTINCT user_email) AS active_users
          FROM usage_events
         WHERE occurred_at >= ? AND user_email != ''
         GROUP BY account
         ORDER BY account`, store.now().Unix()-int64(time.Hour/time.Second)); err != nil {
		return nil, fmt.Errorf("query one-hour active users: %w", err)
	}
	result := make(map[string]int, len(rows))
	for _, row := range rows {
		result[row.Account] = row.Users
	}
	return result, nil
}

func breakdownStart(ctx context.Context, queryer sqlx.QueryerContext) (int64, error) {
	var value string
	err := sqlx.GetContext(ctx, queryer, &value, "SELECT value FROM usage_meta WHERE key = ?", breakdownStartedAtKey)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read usage breakdown start: %w", err)
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < 0 {
		return 0, fmt.Errorf("usage breakdown start is invalid")
	}
	return parsed, nil
}

func sortUserBreakdown(result *UserBreakdown) {
	sort.Slice(result.Models, func(left, right int) bool {
		if result.Models[left].SuccessCount != result.Models[right].SuccessCount {
			return result.Models[left].SuccessCount > result.Models[right].SuccessCount
		}
		return result.Models[left].Model < result.Models[right].Model
	})
	effortOrder := map[string]int{
		"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4,
		"xhigh": 5, "ultra": 6, "max": 7, "auto": 8, "unknown": 9,
	}
	sort.Slice(result.ReasoningEfforts, func(left, right int) bool {
		leftOrder, leftFound := effortOrder[result.ReasoningEfforts[left].ReasoningEffort]
		rightOrder, rightFound := effortOrder[result.ReasoningEfforts[right].ReasoningEffort]
		if !leftFound {
			leftOrder = len(effortOrder)
		}
		if !rightFound {
			rightOrder = len(effortOrder)
		}
		if leftOrder != rightOrder {
			return leftOrder < rightOrder
		}
		return result.ReasoningEfforts[left].ReasoningEffort < result.ReasoningEfforts[right].ReasoningEffort
	})
	sort.Slice(result.Combinations, func(left, right int) bool {
		if result.Combinations[left].SuccessCount != result.Combinations[right].SuccessCount {
			return result.Combinations[left].SuccessCount > result.Combinations[right].SuccessCount
		}
		if result.Combinations[left].Model != result.Combinations[right].Model {
			return result.Combinations[left].Model < result.Combinations[right].Model
		}
		if result.Combinations[left].ReasoningEffort != result.Combinations[right].ReasoningEffort {
			return result.Combinations[left].ReasoningEffort < result.Combinations[right].ReasoningEffort
		}
		return result.Combinations[left].Account < result.Combinations[right].Account
	})
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
