package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

type TokenSeries struct {
	Name            string  `json:"name"`
	Values          []int64 `json:"values"`
	Current         int64   `json:"current"`
	Average         int64   `json:"average"`
	Maximum         int64   `json:"maximum"`
	Total           int64   `json:"total"`
	WeightedValues  []int64 `json:"weighted_values"`
	WeightedCurrent int64   `json:"weighted_current"`
	WeightedAverage int64   `json:"weighted_average"`
	WeightedMaximum int64   `json:"weighted_maximum"`
	WeightedTotal   int64   `json:"weighted_total"`
}

type TokenTrend struct {
	GeneratedAt   int64         `json:"generated_at"`
	WindowStartAt int64         `json:"window_start_at"`
	WindowSeconds int64         `json:"window_seconds"`
	BucketSeconds int64         `json:"bucket_seconds"`
	Buckets       []int64       `json:"buckets"`
	Accounts      []TokenSeries `json:"accounts"`
	Users         []TokenSeries `json:"users"`
}

type PublicAccountUsage struct {
	Account      string `json:"account"`
	ActiveKeys   int64  `json:"active_keys"`
	RequestCount int64  `json:"request_count"`
}

type tokenSeriesRow struct {
	Name           string `db:"series_name"`
	BucketAt       int64  `db:"bucket_at"`
	Tokens         int64  `db:"total_tokens"`
	WeightedTokens int64  `db:"weighted_tokens"`
}

const effectiveWeightedTokensSQL = `CASE
	WHEN weight_policy_version = 'legacy-v1' AND weighted_tokens = 0 AND total_tokens > 0
	THEN total_tokens ELSE weighted_tokens END`

func (store *Store) TokenTimeSeries(
	ctx context.Context,
	accounts []string,
	knownUsers []string,
	selectedUsers []string,
	startAt int64,
	endAt int64,
	bucketSeconds int64,
	userLimit int,
	startAtByAccount map[string]int64,
) (TokenTrend, error) {
	if startAt < 0 || endAt <= startAt || bucketSeconds <= 0 {
		return TokenTrend{}, errors.New("token trend range is invalid")
	}
	accountIDs := normalizedStrings(accounts, false)
	normalizedStarts := make(map[string]int64)
	if startAtByAccount != nil {
		filteredAccounts := make([]string, 0, len(accountIDs))
		for _, account := range accountIDs {
			accountStart, found := startAtByAccount[account]
			if !found {
				continue
			}
			if accountStart < 0 || accountStart >= endAt {
				return TokenTrend{}, errors.New("token trend account range is invalid")
			}
			normalizedStarts[account] = accountStart
			filteredAccounts = append(filteredAccounts, account)
		}
		accountIDs = filteredAccounts
	}
	users := normalizedEmailSet(knownUsers)
	selected := normalizedEmailSet(selectedUsers)
	if userLimit < 1 {
		userLimit = 1
	}
	if userLimit > 50 {
		userLimit = 50
	}
	generatedAt := endAt - 1
	firstBucket := (startAt / bucketSeconds) * bucketSeconds
	lastBucket := (generatedAt / bucketSeconds) * bucketSeconds
	buckets := make([]int64, 0, (lastBucket-firstBucket)/bucketSeconds+1)
	for bucket := firstBucket; bucket <= lastBucket; bucket += bucketSeconds {
		buckets = append(buckets, bucket)
		if len(buckets) > 400 {
			return TokenTrend{}, errors.New("token trend exceeds 400 buckets")
		}
	}
	result := TokenTrend{
		GeneratedAt: generatedAt, WindowStartAt: startAt, WindowSeconds: endAt - startAt,
		BucketSeconds: bucketSeconds, Buckets: buckets,
		Accounts: make([]TokenSeries, 0, len(accountIDs)), Users: make([]TokenSeries, 0),
	}
	if len(accountIDs) == 0 {
		return result, nil
	}
	accountsJSON, err := json.Marshal(accountIDs)
	if err != nil {
		return result, fmt.Errorf("encode trend accounts: %w", err)
	}
	usersJSON, err := json.Marshal(users)
	if err != nil {
		return result, fmt.Errorf("encode trend users: %w", err)
	}
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, fmt.Errorf("begin token trend snapshot: %w", err)
	}
	defer transaction.Rollback()
	accountUserScope := users
	if len(selected) > 0 {
		accountUserScope = selected
	}
	accountUsersJSON, err := json.Marshal(accountUserScope)
	if err != nil {
		return result, fmt.Errorf("encode trend account users: %w", err)
	}
	rangeSQL := "occurred_at >= ? AND occurred_at < ?"
	rangeArguments := []any{startAt, endAt}
	if startAtByAccount != nil {
		encodedStarts, encodeErr := json.Marshal(normalizedStarts)
		if encodeErr != nil {
			return result, fmt.Errorf("encode token trend account starts: %w", encodeErr)
		}
		rangeSQL = `occurred_at < ? AND EXISTS (
			SELECT 1 FROM json_each(?) AS account_starts
			 WHERE CAST(account_starts.key AS TEXT) = usage_events.account
			   AND usage_events.occurred_at >= CAST(account_starts.value AS INTEGER)
		)`
		rangeArguments = []any{endAt, string(encodedStarts)}
	}
	accountRows := make([]tokenSeriesRow, 0)
	if len(accountUserScope) > 0 {
		query := `
			SELECT account AS series_name,
			       CAST(occurred_at / ? AS INTEGER) * ? AS bucket_at,
			       SUM(total_tokens) AS total_tokens,
			       SUM(` + effectiveWeightedTokensSQL + `) AS weighted_tokens
			  FROM usage_events
			 WHERE account IN (SELECT CAST(value AS TEXT) FROM json_each(?))
			   AND user_email IN (SELECT CAST(value AS TEXT) FROM json_each(?))
			   AND ` + rangeSQL + `
			 GROUP BY account, bucket_at
			 ORDER BY account, bucket_at`
		arguments := []any{bucketSeconds, bucketSeconds, string(accountsJSON), string(accountUsersJSON)}
		arguments = append(arguments, rangeArguments...)
		if err := transaction.SelectContext(ctx, &accountRows, query, arguments...); err != nil {
			return result, fmt.Errorf("query account token trend: %w", err)
		}
	}
	selectedForSeries := selected
	if len(selectedForSeries) == 0 && len(users) > 0 {
		rows := make([]struct {
			User           string `db:"user_email"`
			Tokens         int64  `db:"total_tokens"`
			WeightedTokens int64  `db:"weighted_tokens"`
		}, 0, userLimit)
		query := `
			SELECT user_email, SUM(total_tokens) AS total_tokens,
			       SUM(` + effectiveWeightedTokensSQL + `) AS weighted_tokens
			  FROM usage_events
			 WHERE account IN (SELECT CAST(value AS TEXT) FROM json_each(?))
			   AND user_email IN (SELECT CAST(value AS TEXT) FROM json_each(?))
			   AND ` + rangeSQL + `
			 GROUP BY user_email
			 ORDER BY weighted_tokens DESC, total_tokens DESC, user_email
			 LIMIT ?`
		arguments := []any{string(accountsJSON), string(usersJSON)}
		arguments = append(arguments, rangeArguments...)
		arguments = append(arguments, userLimit)
		if err := transaction.SelectContext(ctx, &rows, query, arguments...); err != nil {
			return result, fmt.Errorf("query token trend users: %w", err)
		}
		selectedForSeries = make([]string, 0, len(rows))
		for _, row := range rows {
			selectedForSeries = append(selectedForSeries, row.User)
		}
	}
	userRows := make([]tokenSeriesRow, 0)
	if len(selectedForSeries) > 0 {
		selectedJSON, encodeErr := json.Marshal(selectedForSeries)
		if encodeErr != nil {
			return result, fmt.Errorf("encode selected trend users: %w", encodeErr)
		}
		query := `
			SELECT user_email AS series_name,
			       CAST(occurred_at / ? AS INTEGER) * ? AS bucket_at,
			       SUM(total_tokens) AS total_tokens,
			       SUM(` + effectiveWeightedTokensSQL + `) AS weighted_tokens
			  FROM usage_events
			 WHERE account IN (SELECT CAST(value AS TEXT) FROM json_each(?))
			   AND user_email IN (SELECT CAST(value AS TEXT) FROM json_each(?))
			   AND ` + rangeSQL + `
			 GROUP BY user_email, bucket_at
			 ORDER BY user_email, bucket_at`
		arguments := []any{bucketSeconds, bucketSeconds, string(accountsJSON), string(selectedJSON)}
		arguments = append(arguments, rangeArguments...)
		if err := transaction.SelectContext(ctx, &userRows, query, arguments...); err != nil {
			return result, fmt.Errorf("query user token trend: %w", err)
		}
	}
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit token trend snapshot: %w", err)
	}
	result.Accounts = buildTokenSeries(accountIDs, buckets, accountRows, bucketSeconds, normalizedStarts)
	result.Users = buildTokenSeries(selectedForSeries, buckets, userRows, bucketSeconds, nil)
	return result, nil
}

func (store *Store) PublicGatewayUsage(
	ctx context.Context,
	accounts []string,
	startAt int64,
	endAt int64,
) (map[string]PublicAccountUsage, error) {
	accountIDs := normalizedStrings(accounts, false)
	result := make(map[string]PublicAccountUsage, len(accountIDs))
	for _, account := range accountIDs {
		result[account] = PublicAccountUsage{Account: account}
	}
	if len(accountIDs) == 0 {
		return result, nil
	}
	encoded, err := json.Marshal(accountIDs)
	if err != nil {
		return nil, fmt.Errorf("encode public usage accounts: %w", err)
	}
	rows := make([]struct {
		Account      string `db:"account"`
		ActiveKeys   int64  `db:"active_keys"`
		RequestCount int64  `db:"request_count"`
	}, 0, len(accountIDs))
	if err := store.db.SelectContext(ctx, &rows, `
		SELECT account, COUNT(DISTINCT user_email) AS active_keys,
		       COUNT(*) AS request_count
		  FROM usage_events
		 WHERE account IN (SELECT CAST(value AS TEXT) FROM json_each(?))
		   AND occurred_at >= ? AND occurred_at < ?
		 GROUP BY account
		 ORDER BY account`, string(encoded), startAt, endAt); err != nil {
		return nil, fmt.Errorf("query public gateway usage: %w", err)
	}
	for _, row := range rows {
		result[row.Account] = PublicAccountUsage{
			Account: row.Account, ActiveKeys: row.ActiveKeys, RequestCount: row.RequestCount,
		}
	}
	return result, nil
}

func normalizedStrings(values []string, lower bool) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		value := strings.TrimSpace(raw)
		if lower {
			value = strings.ToLower(value)
		}
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func buildTokenSeries(
	names []string,
	buckets []int64,
	rows []tokenSeriesRow,
	bucketSeconds int64,
	averageStartAt map[string]int64,
) []TokenSeries {
	type bucketTokens struct {
		raw      int64
		weighted int64
	}
	values := make(map[string]map[int64]bucketTokens, len(names))
	for _, name := range names {
		values[name] = make(map[int64]bucketTokens)
	}
	for _, row := range rows {
		if _, found := values[row.Name]; found {
			values[row.Name][row.BucketAt] = bucketTokens{raw: row.Tokens, weighted: row.WeightedTokens}
		}
	}
	result := make([]TokenSeries, 0, len(names))
	for _, name := range names {
		series := TokenSeries{
			Name: name, Values: make([]int64, len(buckets)), WeightedValues: make([]int64, len(buckets)),
		}
		for index, bucket := range buckets {
			series.Values[index] = values[name][bucket].raw
			series.WeightedValues[index] = values[name][bucket].weighted
			series.Total += series.Values[index]
			series.WeightedTotal += series.WeightedValues[index]
			series.Maximum = max(series.Maximum, series.Values[index])
			series.WeightedMaximum = max(series.WeightedMaximum, series.WeightedValues[index])
		}
		averageValues := series.Values
		weightedAverageValues := series.WeightedValues
		if startAt, found := averageStartAt[name]; found {
			startBucket := (startAt / bucketSeconds) * bucketSeconds
			for index, bucket := range buckets {
				if bucket >= startBucket {
					averageValues = series.Values[index:]
					weightedAverageValues = series.WeightedValues[index:]
					break
				}
			}
		}
		if len(series.Values) > 0 {
			series.Current = series.Values[len(series.Values)-1]
			series.WeightedCurrent = series.WeightedValues[len(series.WeightedValues)-1]
		}
		if len(averageValues) > 0 {
			var averageTotal, weightedAverageTotal int64
			for _, value := range averageValues {
				averageTotal += value
			}
			for _, value := range weightedAverageValues {
				weightedAverageTotal += value
			}
			series.Average = int64(math.Round(float64(averageTotal) / float64(len(averageValues))))
			series.WeightedAverage = int64(math.Round(float64(weightedAverageTotal) / float64(len(weightedAverageValues))))
		}
		result = append(result, series)
	}
	return result
}

func sortedKeys(values map[string]PublicAccountUsage) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}
