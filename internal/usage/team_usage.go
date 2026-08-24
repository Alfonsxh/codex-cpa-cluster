package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

type TeamUsageMetrics struct {
	WeightedMetrics
	ActiveUsers int64 `json:"active_users"`
}

type TeamUserUsage struct {
	User string `json:"user"`
	WeightedMetrics
}

type TeamAccountUsage struct {
	Account string `json:"account"`
	WeightedMetrics
}

type TeamModelUsage struct {
	Model string `json:"model"`
	WeightedMetrics
}

type TeamCombinationUsage struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	WeightedMetrics
}

type TeamUsageSeries struct {
	StartAt       int64   `json:"start_at"`
	EndAt         int64   `json:"end_at"`
	BucketSeconds int64   `json:"bucket_seconds"`
	Buckets       []int64 `json:"buckets"`
	Values        []int64 `json:"values"`
}

type TeamBreakdown struct {
	TeamID       string                 `json:"team_id"`
	Attribution  string                 `json:"attribution"`
	Totals       WeightedMetrics        `json:"totals"`
	Users        []TeamUserUsage        `json:"users"`
	Accounts     []TeamAccountUsage     `json:"accounts"`
	Models       []TeamModelUsage       `json:"models"`
	Combinations []TeamCombinationUsage `json:"combinations"`
	Series       TeamUsageSeries        `json:"series"`
}

const teamWeightedTokensSQL = `SUM(CASE
	WHEN weight_policy_version = 'legacy-v1' AND weighted_tokens = 0 AND total_tokens > 0
	THEN total_tokens ELSE weighted_tokens END)`

const teamMetricsSQL = `
	COUNT(*) AS request_count,
	COALESCE(SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END), 0) AS success_count,
	COALESCE(SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END), 0) AS failed_count,
	COALESCE(SUM(input_tokens), 0) AS input_tokens,
	COALESCE(SUM(output_tokens), 0) AS output_tokens,
	COALESCE(SUM(reasoning_tokens), 0) AS reasoning_tokens,
	COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
	COALESCE(SUM(total_tokens), 0) AS total_tokens,
	COALESCE(` + teamWeightedTokensSQL + `, 0) AS weighted_tokens,
	COALESCE(MAX(occurred_at), 0) AS last_used_at`

type teamMetricRow struct {
	User            string `db:"user_email"`
	Account         string `db:"account"`
	Model           string `db:"model"`
	ReasoningEffort string `db:"reasoning_effort"`
	breakdownRow
}

func (row teamMetricRow) metrics() WeightedMetrics {
	return row.weightedMetrics()
}

// TeamUsage attributes every selected user's historical events to that
// user's current control-plane team. Frozen event team columns remain audit
// metadata and are deliberately not used for management aggregation.
func (store *Store) TeamUsage(
	ctx context.Context,
	teamIDs []string,
	currentTeamByUser map[string]string,
	startAt *int64,
	endAt *int64,
) (map[string]TeamUsageMetrics, error) {
	result := make(map[string]TeamUsageMetrics, len(teamIDs)+1)
	for _, rawTeam := range teamIDs {
		team := strings.TrimSpace(rawTeam)
		if team != "" {
			result[team] = TeamUsageMetrics{}
		}
	}
	result["unassigned"] = TeamUsageMetrics{}
	membership := make(map[string]string, len(currentTeamByUser))
	users := make([]string, 0, len(currentTeamByUser))
	for rawUser, rawTeam := range currentTeamByUser {
		user := strings.ToLower(strings.TrimSpace(rawUser))
		if user == "" {
			continue
		}
		membership[user] = strings.TrimSpace(rawTeam)
		users = append(users, user)
	}
	users = normalizedEmailSet(users)
	if len(users) == 0 {
		return result, nil
	}
	selectedUsers, err := json.Marshal(users)
	if err != nil {
		return nil, fmt.Errorf("encode team usage users: %w", err)
	}
	where, parameters := teamUsageScope(string(selectedUsers), startAt, endAt)
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin team usage snapshot: %w", err)
	}
	defer transaction.Rollback()
	rows := make([]teamMetricRow, 0, len(users))
	if err := transaction.SelectContext(ctx, &rows, `
		SELECT user_email, `+teamMetricsSQL+`
		  FROM usage_events
		 WHERE `+where+`
		 GROUP BY user_email`, parameters...); err != nil {
		return nil, fmt.Errorf("query current-membership team usage: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit team usage snapshot: %w", err)
	}
	for _, row := range rows {
		team := membership[strings.ToLower(strings.TrimSpace(row.User))]
		key := team
		if key == "" {
			key = "unassigned"
		}
		metrics, found := result[key]
		if !found {
			continue
		}
		metrics.ActiveUsers++
		addWeighted(&metrics.WeightedMetrics, row.metrics())
		result[key] = metrics
	}
	return result, nil
}

func (store *Store) TeamBreakdown(
	ctx context.Context,
	rawTeamID string,
	currentUsers []string,
	startAt *int64,
	endAt *int64,
) (TeamBreakdown, error) {
	teamID := strings.TrimSpace(rawTeamID)
	if teamID == "" {
		teamID = "unassigned"
	}
	users := normalizedEmailSet(currentUsers)
	selectedUsers, err := json.Marshal(users)
	if err != nil {
		return TeamBreakdown{}, fmt.Errorf("encode team breakdown users: %w", err)
	}
	where, parameters := teamUsageScope(string(selectedUsers), startAt, endAt)
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TeamBreakdown{}, fmt.Errorf("begin team breakdown snapshot: %w", err)
	}
	defer transaction.Rollback()
	result := TeamBreakdown{
		TeamID: teamID, Attribution: "current_membership",
		Users: make([]TeamUserUsage, 0), Accounts: make([]TeamAccountUsage, 0),
		Models: make([]TeamModelUsage, 0), Combinations: make([]TeamCombinationUsage, 0),
	}
	var total teamMetricRow
	if err := transaction.GetContext(ctx, &total, `
		SELECT `+teamMetricsSQL+`
		  FROM usage_events
		 WHERE `+where, parameters...); err != nil {
		return result, fmt.Errorf("query team usage totals: %w", err)
	}
	result.Totals = total.metrics()
	userRows := make([]teamMetricRow, 0)
	if err := transaction.SelectContext(ctx, &userRows, `
		SELECT user_email, `+teamMetricsSQL+`
		  FROM usage_events
		 WHERE `+where+`
		 GROUP BY user_email`, parameters...); err != nil {
		return result, fmt.Errorf("query team user usage: %w", err)
	}
	for _, row := range userRows {
		result.Users = append(result.Users, TeamUserUsage{User: row.User, WeightedMetrics: row.metrics()})
	}
	accountRows := make([]teamMetricRow, 0)
	if err := transaction.SelectContext(ctx, &accountRows, `
		SELECT account, `+teamMetricsSQL+`
		  FROM usage_events
		 WHERE `+where+`
		 GROUP BY account`, parameters...); err != nil {
		return result, fmt.Errorf("query team account usage: %w", err)
	}
	for _, row := range accountRows {
		result.Accounts = append(result.Accounts, TeamAccountUsage{Account: row.Account, WeightedMetrics: row.metrics()})
	}
	modelRows := make([]teamMetricRow, 0)
	if err := transaction.SelectContext(ctx, &modelRows, `
		SELECT COALESCE(NULLIF(model, ''), 'unknown') AS model, `+teamMetricsSQL+`
		  FROM usage_events
		 WHERE `+where+`
		 GROUP BY COALESCE(NULLIF(model, ''), 'unknown')`, parameters...); err != nil {
		return result, fmt.Errorf("query team model usage: %w", err)
	}
	for _, row := range modelRows {
		result.Models = append(result.Models, TeamModelUsage{Model: row.Model, WeightedMetrics: row.metrics()})
	}
	combinationRows := make([]teamMetricRow, 0)
	if err := transaction.SelectContext(ctx, &combinationRows, `
		SELECT COALESCE(NULLIF(model, ''), 'unknown') AS model,
		       COALESCE(NULLIF(reasoning_effort, ''), 'unknown') AS reasoning_effort,
		       `+teamMetricsSQL+`
		  FROM usage_events
		 WHERE `+where+`
		 GROUP BY COALESCE(NULLIF(model, ''), 'unknown'),
		          COALESCE(NULLIF(reasoning_effort, ''), 'unknown')`, parameters...); err != nil {
		return result, fmt.Errorf("query team model effort usage: %w", err)
	}
	for _, row := range combinationRows {
		result.Combinations = append(result.Combinations, TeamCombinationUsage{
			Model: row.Model, ReasoningEffort: row.ReasoningEffort, WeightedMetrics: row.metrics(),
		})
	}
	if err := loadTeamSeries(ctx, transaction, where, parameters, startAt, endAt, store.now().Unix(), &result.Series); err != nil {
		return result, err
	}
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit team breakdown snapshot: %w", err)
	}
	sort.Slice(result.Users, func(left, right int) bool {
		if result.Users[left].WeightedTokens != result.Users[right].WeightedTokens {
			return result.Users[left].WeightedTokens > result.Users[right].WeightedTokens
		}
		return result.Users[left].User < result.Users[right].User
	})
	sort.Slice(result.Accounts, func(left, right int) bool {
		if result.Accounts[left].WeightedTokens != result.Accounts[right].WeightedTokens {
			return result.Accounts[left].WeightedTokens > result.Accounts[right].WeightedTokens
		}
		return result.Accounts[left].Account < result.Accounts[right].Account
	})
	sort.Slice(result.Models, func(left, right int) bool {
		if result.Models[left].WeightedTokens != result.Models[right].WeightedTokens {
			return result.Models[left].WeightedTokens > result.Models[right].WeightedTokens
		}
		return result.Models[left].Model < result.Models[right].Model
	})
	sort.Slice(result.Combinations, func(left, right int) bool {
		if result.Combinations[left].WeightedTokens != result.Combinations[right].WeightedTokens {
			return result.Combinations[left].WeightedTokens > result.Combinations[right].WeightedTokens
		}
		if result.Combinations[left].Model != result.Combinations[right].Model {
			return result.Combinations[left].Model < result.Combinations[right].Model
		}
		return result.Combinations[left].ReasoningEffort < result.Combinations[right].ReasoningEffort
	})
	return result, nil
}

func teamUsageScope(usersJSON string, startAt *int64, endAt *int64) (string, []any) {
	clauses := []string{"user_email IN (SELECT CAST(value AS TEXT) FROM json_each(?))"}
	parameters := []any{usersJSON}
	if startAt != nil {
		clauses = append(clauses, "occurred_at >= ?")
		parameters = append(parameters, *startAt)
	}
	if endAt != nil {
		clauses = append(clauses, "occurred_at < ?")
		parameters = append(parameters, *endAt)
	}
	return strings.Join(clauses, " AND "), parameters
}

func loadTeamSeries(
	ctx context.Context,
	transaction *sqlx.Tx,
	where string,
	parameters []any,
	startAt *int64,
	endAt *int64,
	now int64,
	series *TeamUsageSeries,
) error {
	var first sql.NullInt64
	if err := transaction.GetContext(ctx, &first, `
		SELECT MIN(occurred_at) FROM usage_events WHERE `+where, parameters...); err != nil {
		return fmt.Errorf("query first team event: %w", err)
	}
	series.StartAt = now
	if startAt != nil {
		series.StartAt = *startAt
	} else if first.Valid {
		series.StartAt = first.Int64
	}
	series.EndAt = now
	if endAt != nil {
		series.EndAt = *endAt
	}
	duration := max(series.EndAt-series.StartAt, 1)
	switch {
	case duration <= 6*60*60:
		series.BucketSeconds = 5 * 60
	case duration <= 24*60*60:
		series.BucketSeconds = 15 * 60
	case duration <= 7*24*60*60:
		series.BucketSeconds = 60 * 60
	case duration <= 31*24*60*60:
		series.BucketSeconds = 6 * 60 * 60
	default:
		series.BucketSeconds = max(int64(math.Ceil(float64(duration)/120)), 60*60)
	}
	firstBucket := (series.StartAt / series.BucketSeconds) * series.BucketSeconds
	lastPoint := max(series.StartAt, series.EndAt-1)
	lastBucket := (lastPoint / series.BucketSeconds) * series.BucketSeconds
	series.Buckets = make([]int64, 0, (lastBucket-firstBucket)/series.BucketSeconds+1)
	for bucket := firstBucket; bucket <= lastBucket; bucket += series.BucketSeconds {
		series.Buckets = append(series.Buckets, bucket)
	}
	type seriesRow struct {
		BucketAt int64 `db:"bucket_at"`
		Tokens   int64 `db:"tokens"`
	}
	rows := make([]seriesRow, 0)
	seriesParameters := append([]any{series.BucketSeconds, series.BucketSeconds}, parameters...)
	if err := transaction.SelectContext(ctx, &rows, `
		SELECT CAST(occurred_at / ? AS INTEGER) * ? AS bucket_at,
		       COALESCE(`+teamWeightedTokensSQL+`, 0) AS tokens
		  FROM usage_events
		 WHERE `+where+`
		 GROUP BY bucket_at ORDER BY bucket_at`, seriesParameters...); err != nil {
		return fmt.Errorf("query team usage series: %w", err)
	}
	values := make(map[int64]int64, len(rows))
	for _, row := range rows {
		values[row.BucketAt] = row.Tokens
	}
	series.Values = make([]int64, len(series.Buckets))
	for index, bucket := range series.Buckets {
		series.Values[index] = values[bucket]
	}
	return nil
}
