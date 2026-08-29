package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
)

type UserTrendDimension string

const (
	UserTrendTotal          UserTrendDimension = "total"
	UserTrendModelReasoning UserTrendDimension = "model_reasoning"
)

type DailyCollectionState string

const (
	DailyCollectionUncollected DailyCollectionState = "uncollected"
	DailyCollectionPartial     DailyCollectionState = "partial"
	DailyCollectionComplete    DailyCollectionState = "complete"
)

type UserDailyCombination struct {
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	RequestCount    int64  `json:"request_count"`
	TotalTokens     int64  `json:"total_tokens"`
	WeightedTokens  int64  `json:"weighted_tokens"`
}

type UserDailyUsage struct {
	Date            string                 `json:"date"`
	StartAt         int64                  `json:"start_at"`
	EndAt           int64                  `json:"end_at"`
	CollectionState DailyCollectionState   `json:"collection_state"`
	RequestCount    int64                  `json:"request_count"`
	TotalTokens     int64                  `json:"total_tokens"`
	WeightedTokens  int64                  `json:"weighted_tokens"`
	Combinations    []UserDailyCombination `json:"combinations"`
}

type UserDailyTrend struct {
	WindowDays          int                `json:"window_days"`
	Timezone            string             `json:"window_timezone"`
	WindowStartAt       int64              `json:"window_start_at"`
	WindowEndAt         int64              `json:"window_end_at"`
	CollectionStartedAt int64              `json:"collection_started_at"`
	EffectiveStartAt    int64              `json:"effective_start_at"`
	Dimension           UserTrendDimension `json:"dimension"`
	Days                []UserDailyUsage   `json:"days"`
}

type dailyTrendBound struct {
	Index   int    `json:"index"`
	Date    string `json:"date"`
	StartAt int64  `json:"start_at"`
	EndAt   int64  `json:"end_at"`
}

// UserDailyTrend aggregates the same successful Token and legacy weighted
// fallback semantics as UserBreakdown. It adds only natural-day buckets and
// uses one bounded read-only WAL snapshot; no trend facts are persisted.
func (store *Store) UserDailyTrend(
	ctx context.Context,
	userEmail string,
	windowDays int,
	timezone string,
	endAt int64,
	dimension UserTrendDimension,
) (UserDailyTrend, error) {
	result := UserDailyTrend{Dimension: dimension, Days: make([]UserDailyUsage, 0)}
	user := strings.ToLower(strings.TrimSpace(userEmail))
	if user == "" {
		return result, errors.New("daily usage trend user is required")
	}
	if windowDays < 1 || windowDays > 90 || endAt <= 0 {
		return result, errors.New("daily usage trend range is invalid")
	}
	if dimension != UserTrendTotal && dimension != UserTrendModelReasoning {
		return result, errors.New("daily usage trend dimension is invalid")
	}
	timezone = strings.TrimSpace(timezone)
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return result, fmt.Errorf("load daily usage trend timezone: %w", err)
	}

	bounds := naturalDayBounds(windowDays, endAt, location)
	result.WindowDays = windowDays
	result.Timezone = timezone
	result.WindowStartAt = bounds[0].StartAt
	result.WindowEndAt = endAt
	result.Days = make([]UserDailyUsage, len(bounds))
	for index, bound := range bounds {
		result.Days[index] = UserDailyUsage{
			Date: bound.Date, StartAt: bound.StartAt, EndAt: bound.EndAt,
			CollectionState: DailyCollectionUncollected, Combinations: make([]UserDailyCombination, 0),
		}
	}

	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, fmt.Errorf("begin daily usage trend snapshot: %w", err)
	}
	defer transaction.Rollback()
	result.CollectionStartedAt, err = breakdownStart(ctx, transaction)
	if err != nil {
		return result, err
	}
	result.EffectiveStartAt = maxInt64(result.CollectionStartedAt, result.WindowStartAt)
	for index := range result.Days {
		result.Days[index].CollectionState = collectionState(
			result.CollectionStartedAt, result.Days[index].StartAt, result.Days[index].EndAt,
		)
	}
	if result.CollectionStartedAt == 0 || result.EffectiveStartAt >= endAt {
		if err := transaction.Commit(); err != nil {
			return result, fmt.Errorf("commit empty daily usage trend snapshot: %w", err)
		}
		return result, nil
	}

	if dimension == UserTrendTotal {
		encodedBounds, encodeErr := json.Marshal(bounds)
		if encodeErr != nil {
			return result, fmt.Errorf("encode daily usage trend bounds: %w", encodeErr)
		}
		err = queryDailyTotals(
			ctx, transaction, string(encodedBounds), user, result.EffectiveStartAt, result.Days,
		)
	} else {
		err = queryDailyCombinations(
			ctx, transaction, user, result.EffectiveStartAt, endAt, result.Days,
		)
	}
	if err != nil {
		return result, err
	}
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit daily usage trend snapshot: %w", err)
	}
	return result, nil
}

func naturalDayBounds(windowDays int, endAt int64, location *time.Location) []dailyTrendBound {
	localEnd := time.Unix(endAt, 0).In(location)
	today := time.Date(localEnd.Year(), localEnd.Month(), localEnd.Day(), 0, 0, 0, 0, location)
	first := today.AddDate(0, 0, -(windowDays - 1))
	bounds := make([]dailyTrendBound, 0, windowDays)
	for index := 0; index < windowDays; index++ {
		start := first.AddDate(0, 0, index)
		end := start.AddDate(0, 0, 1).Unix()
		if end > endAt {
			end = endAt
		}
		bounds = append(bounds, dailyTrendBound{
			Index: index, Date: start.Format("2006-01-02"), StartAt: start.Unix(), EndAt: end,
		})
	}
	return bounds
}

func collectionState(collectionStartedAt, startAt, endAt int64) DailyCollectionState {
	if collectionStartedAt <= 0 || endAt <= collectionStartedAt {
		return DailyCollectionUncollected
	}
	if startAt < collectionStartedAt {
		return DailyCollectionPartial
	}
	return DailyCollectionComplete
}

func queryDailyTotals(
	ctx context.Context,
	transaction *sqlx.Tx,
	encodedBounds string,
	user string,
	effectiveStartAt int64,
	days []UserDailyUsage,
) error {
	rows := make([]struct {
		Index          int   `db:"day_index"`
		RequestCount   int64 `db:"request_count"`
		TotalTokens    int64 `db:"total_tokens"`
		WeightedTokens int64 `db:"weighted_tokens"`
	}, 0, len(days))
	if err := transaction.SelectContext(ctx, &rows, `
		WITH day_bounds AS (
			SELECT CAST(json_extract(value, '$.index') AS INTEGER) AS day_index,
			       CAST(json_extract(value, '$.start_at') AS INTEGER) AS start_at,
			       CAST(json_extract(value, '$.end_at') AS INTEGER) AS end_at
			  FROM json_each(?)
		)
		SELECT bounds.day_index,
		       COUNT(events.id) AS request_count,
		       COALESCE(SUM(CASE WHEN events.failed = 0 THEN events.total_tokens ELSE 0 END), 0) AS total_tokens,
		       COALESCE(SUM(CASE WHEN events.failed = 0 THEN
		           CASE WHEN events.weight_policy_version = 'legacy-v1'
		                     AND events.weighted_tokens = 0 AND events.total_tokens > 0
		                THEN events.total_tokens ELSE events.weighted_tokens END
		           ELSE 0 END), 0) AS weighted_tokens
		  FROM day_bounds AS bounds
		  LEFT JOIN usage_events AS events
		    ON events.user_email = ?
		   AND events.occurred_at >= MAX(bounds.start_at, ?)
		   AND events.occurred_at < bounds.end_at
		   AND events.alias != ''
		 GROUP BY bounds.day_index
		 ORDER BY bounds.day_index`, encodedBounds, user, effectiveStartAt); err != nil {
		return fmt.Errorf("query daily usage totals: %w", err)
	}
	for _, row := range rows {
		if row.Index < 0 || row.Index >= len(days) {
			return errors.New("daily usage total returned an invalid bucket")
		}
		days[row.Index].RequestCount = row.RequestCount
		days[row.Index].TotalTokens = row.TotalTokens
		days[row.Index].WeightedTokens = row.WeightedTokens
	}
	return nil
}

const dailyCombinationQuery = `
	SELECT occurred_at,
	       COALESCE(NULLIF(model, ''), 'unknown') AS model,
	       COALESCE(NULLIF(reasoning_effort, ''), 'unknown') AS reasoning_effort,
	       COALESCE(failed, 1), COALESCE(total_tokens, 0), COALESCE(weighted_tokens, 0),
	       COALESCE(weight_policy_version, '')
	  FROM usage_events
	 WHERE user_email = ?
	   AND occurred_at >= ?
	   AND occurred_at < ?
	   AND alias != ''
	 ORDER BY occurred_at`

func queryDailyCombinations(
	ctx context.Context,
	transaction *sqlx.Tx,
	user string,
	effectiveStartAt int64,
	endAt int64,
	days []UserDailyUsage,
) error {
	type combinationKey struct {
		model           string
		reasoningEffort string
	}
	rows, err := transaction.QueryxContext(
		ctx, dailyCombinationQuery, user, effectiveStartAt, endAt,
	)
	if err != nil {
		return fmt.Errorf("query daily usage combinations: %w", err)
	}
	defer rows.Close()

	combinations := make([]map[combinationKey]*UserDailyCombination, len(days))
	successfulCombinations := make([]map[combinationKey]bool, len(days))
	dayIndex := 0
	for rows.Next() {
		var (
			occurredAt          int64
			model               string
			reasoningEffort     string
			failed              int
			totalTokens         int64
			weightedTokens      int64
			weightPolicyVersion string
		)
		if err := rows.Scan(
			&occurredAt, &model, &reasoningEffort, &failed, &totalTokens,
			&weightedTokens, &weightPolicyVersion,
		); err != nil {
			return fmt.Errorf("scan daily usage combination: %w", err)
		}
		for dayIndex < len(days) && occurredAt >= days[dayIndex].EndAt {
			dayIndex++
		}
		if dayIndex >= len(days) {
			return errors.New("daily usage combination returned an invalid bucket")
		}
		if occurredAt < days[dayIndex].StartAt {
			return errors.New("daily usage combination returned an out-of-order bucket")
		}

		day := &days[dayIndex]
		day.RequestCount++
		key := combinationKey{model: model, reasoningEffort: reasoningEffort}
		if combinations[dayIndex] == nil {
			combinations[dayIndex] = make(map[combinationKey]*UserDailyCombination)
			successfulCombinations[dayIndex] = make(map[combinationKey]bool)
		}
		combination := combinations[dayIndex][key]
		if combination == nil {
			combination = &UserDailyCombination{Model: model, ReasoningEffort: reasoningEffort}
			combinations[dayIndex][key] = combination
		}
		combination.RequestCount++
		if failed != 0 {
			continue
		}
		successfulCombinations[dayIndex][key] = true
		if weightPolicyVersion == "legacy-v1" && weightedTokens == 0 && totalTokens > 0 {
			weightedTokens = totalTokens
		}
		day.TotalTokens += totalTokens
		day.WeightedTokens += weightedTokens
		combination.TotalTokens += totalTokens
		combination.WeightedTokens += weightedTokens
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate daily usage combinations: %w", err)
	}

	for index, byDimension := range combinations {
		for key, combination := range byDimension {
			if !successfulCombinations[index][key] {
				continue
			}
			days[index].Combinations = append(days[index].Combinations, *combination)
		}
		sort.Slice(days[index].Combinations, func(left, right int) bool {
			leftCombination := days[index].Combinations[left]
			rightCombination := days[index].Combinations[right]
			if leftCombination.WeightedTokens != rightCombination.WeightedTokens {
				return leftCombination.WeightedTokens > rightCombination.WeightedTokens
			}
			if leftCombination.Model != rightCombination.Model {
				return leftCombination.Model < rightCombination.Model
			}
			return leftCombination.ReasoningEffort < rightCombination.ReasoningEffort
		})
	}
	return nil
}
