package usage

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestUserDailyTrendUsesNaturalDaysAndBreakdownSemantics(t *testing.T) {
	path := createUsageFixture(t, 10)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open daily trend fixture: %v", err)
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		database.Close()
		t.Fatalf("load usage timezone: %v", err)
	}
	at := func(year int, month time.Month, day, hour int) int64 {
		return time.Date(year, month, day, hour, 0, 0, 0, location).Unix()
	}
	statements := []string{
		`DELETE FROM usage_events`,
		`UPDATE usage_meta SET value = ? WHERE key = 'usage_breakdown_started_at'`,
		`CREATE INDEX usage_events_user_time_test ON usage_events(user_email, occurred_at)`,
	}
	if _, err := database.Exec(statements[0]); err != nil {
		database.Close()
		t.Fatalf("clear usage events: %v", err)
	}
	if _, err := database.Exec(statements[1], at(2026, time.August, 27, 12)); err != nil {
		database.Close()
		t.Fatalf("set collection start: %v", err)
	}
	if _, err := database.Exec(statements[2]); err != nil {
		database.Close()
		t.Fatalf("create usage test index: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO usage_events(
			account, user_email, occurred_at, model, alias, reasoning_effort, failed,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			weighted_tokens, weight_policy_version
		) VALUES
			('alpha', 'alice@example.com', ?, 'gpt-5.4', 'gpt-5.4', 'high', 0, 60, 30, 10, 5, 100, 125, 'v2'),
			('alpha', 'alice@example.com', ?, 'gpt-5.4', 'gpt-5.4', 'high', 1, 20, 20, 10, 0, 50, 75, 'v2'),
			('beta', 'alice@example.com', ?, 'gpt-5.4-mini', 'gpt-5.4-mini', 'medium', 0, 40, 20, 20, 0, 80, 0, 'legacy-v1'),
			('alpha', 'alice@example.com', ?, 'gpt-5.4', 'gpt-5.4', 'high', 0, 10, 5, 5, 0, 20, 30, 'v2'),
			('alpha', 'alice@example.com', ?, 'hidden', '', 'low', 0, 5, 5, 0, 0, 10, 10, 'v2'),
			('alpha', 'bob@example.com', ?, 'gpt-5.4', 'gpt-5.4', 'high', 0, 500, 500, 0, 0, 1000, 2000, 'v2')`,
		at(2026, time.August, 27, 13),
		at(2026, time.August, 27, 14),
		at(2026, time.August, 28, 9),
		at(2026, time.August, 28, 10),
		at(2026, time.August, 28, 11),
		at(2026, time.August, 28, 12),
	); err != nil {
		database.Close()
		t.Fatalf("seed daily trend usage: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close daily trend fixture: %v", err)
	}

	store, err := OpenReadOnlyPath(path, time.Now)
	if err != nil {
		t.Fatalf("OpenReadOnlyPath: %v", err)
	}
	defer store.Close()
	endAt := time.Date(2026, time.August, 29, 12, 0, 0, 0, location).Unix()

	total, err := store.UserDailyTrend(
		context.Background(), " Alice@Example.com ", 3, "Asia/Shanghai", endAt, UserTrendTotal,
	)
	if err != nil {
		t.Fatalf("UserDailyTrend total: %v", err)
	}
	if total.Timezone != "Asia/Shanghai" || total.WindowDays != 3 || len(total.Days) != 3 {
		t.Fatalf("total trend envelope = %#v", total)
	}
	if total.Days[0].Date != "2026-08-27" || total.Days[0].CollectionState != DailyCollectionPartial ||
		total.Days[0].RequestCount != 2 || total.Days[0].TotalTokens != 100 || total.Days[0].WeightedTokens != 125 {
		t.Fatalf("first total day = %#v", total.Days[0])
	}
	if total.Days[1].Date != "2026-08-28" || total.Days[1].CollectionState != DailyCollectionComplete ||
		total.Days[1].RequestCount != 2 || total.Days[1].TotalTokens != 100 || total.Days[1].WeightedTokens != 110 {
		t.Fatalf("second total day = %#v", total.Days[1])
	}
	if total.Days[2].Date != "2026-08-29" || total.Days[2].RequestCount != 0 ||
		total.Days[2].TotalTokens != 0 || total.Days[2].WeightedTokens != 0 {
		t.Fatalf("zero-filled current day = %#v", total.Days[2])
	}
	if len(total.Days[0].Combinations) != 0 {
		t.Fatalf("total trend unexpectedly returned combinations: %#v", total.Days[0].Combinations)
	}

	combined, err := store.UserDailyTrend(
		context.Background(), "alice@example.com", 3, "Asia/Shanghai", endAt, UserTrendModelReasoning,
	)
	if err != nil {
		t.Fatalf("UserDailyTrend model_reasoning: %v", err)
	}
	if len(combined.Days[1].Combinations) != 2 {
		t.Fatalf("combined second day = %#v", combined.Days[1])
	}
	first := combined.Days[1].Combinations[0]
	second := combined.Days[1].Combinations[1]
	if first.Model != "gpt-5.4-mini" || first.ReasoningEffort != "medium" ||
		first.TotalTokens != 80 || first.WeightedTokens != 80 {
		t.Fatalf("legacy weighted fallback combination = %#v", first)
	}
	if second.Model != "gpt-5.4" || second.ReasoningEffort != "high" ||
		second.TotalTokens != 20 || second.WeightedTokens != 30 {
		t.Fatalf("weighted combination = %#v", second)
	}
	if combined.Days[1].TotalTokens != 100 || combined.Days[1].WeightedTokens != 110 {
		t.Fatalf("combined day total = %#v", combined.Days[1])
	}
}

func TestUserDailyTrendPreservesDSTNaturalDayBounds(t *testing.T) {
	path := createUsageFixture(t, 10)
	store, err := OpenReadOnlyPath(path, time.Now)
	if err != nil {
		t.Fatalf("OpenReadOnlyPath: %v", err)
	}
	defer store.Close()
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load DST timezone: %v", err)
	}
	endAt := time.Date(2026, time.March, 9, 12, 0, 0, 0, location).Unix()
	trend, err := store.UserDailyTrend(
		context.Background(), "alice@example.com", 3, "America/New_York", endAt, UserTrendTotal,
	)
	if err != nil {
		t.Fatalf("UserDailyTrend DST: %v", err)
	}
	if len(trend.Days) != 3 || trend.Days[1].Date != "2026-03-08" {
		t.Fatalf("DST day list = %#v", trend.Days)
	}
	if seconds := trend.Days[1].EndAt - trend.Days[1].StartAt; seconds != 23*60*60 {
		t.Fatalf("spring-forward day length = %d, want %d", seconds, 23*60*60)
	}
	if trend.Days[2].EndAt != endAt {
		t.Fatalf("current partial day end = %d, want %d", trend.Days[2].EndAt, endAt)
	}
}

func TestUserDailyTrendRejectsUnsafeBounds(t *testing.T) {
	path := createUsageFixture(t, 10)
	store, err := OpenReadOnlyPath(path, time.Now)
	if err != nil {
		t.Fatalf("OpenReadOnlyPath: %v", err)
	}
	defer store.Close()
	for _, testCase := range []struct {
		name      string
		days      int
		timezone  string
		dimension UserTrendDimension
	}{
		{name: "zero days", days: 0, timezone: "UTC", dimension: UserTrendTotal},
		{name: "too many days", days: 91, timezone: "UTC", dimension: UserTrendTotal},
		{name: "bad timezone", days: 7, timezone: "Mars/Olympus", dimension: UserTrendTotal},
		{name: "bad dimension", days: 7, timezone: "UTC", dimension: UserTrendDimension("model")},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := store.UserDailyTrend(
				context.Background(), "alice@example.com", testCase.days, testCase.timezone,
				time.Now().Unix(), testCase.dimension,
			); err == nil {
				t.Fatal("invalid daily trend bounds were accepted")
			}
		})
	}
}

func TestDailyCombinationQueryUsesBoundedUserTimeIndex(t *testing.T) {
	path := createUsageFixture(t, 10)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open daily trend plan fixture: %v", err)
	}
	defer database.Close()
	if _, err := database.Exec(
		`CREATE INDEX usage_events_user_time_test ON usage_events(user_email, occurred_at)`,
	); err != nil {
		t.Fatalf("create daily trend plan index: %v", err)
	}
	rows, err := database.Query(
		"EXPLAIN QUERY PLAN "+dailyCombinationQuery,
		"alice@example.com", int64(1), int64(2),
	)
	if err != nil {
		t.Fatalf("explain daily combination query: %v", err)
	}
	defer rows.Close()
	details := make([]string, 0)
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatalf("scan daily combination query plan: %v", err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate daily combination query plan: %v", err)
	}
	if !strings.Contains(strings.Join(details, "\n"), "usage_events_user_time_test") {
		t.Fatalf("daily combination query does not use the user/time index: %v", details)
	}
}

func TestUserDailyTrendKeepsSuccessfulZeroTokenCombination(t *testing.T) {
	path := createUsageFixture(t, 10)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open zero-token daily trend fixture: %v", err)
	}
	now := time.Now().Unix()
	if _, err := database.Exec(`DELETE FROM usage_events`); err != nil {
		database.Close()
		t.Fatalf("clear zero-token usage events: %v", err)
	}
	if _, err := database.Exec(
		`UPDATE usage_meta SET value = ? WHERE key = 'usage_breakdown_started_at'`, now-3600,
	); err != nil {
		database.Close()
		t.Fatalf("set zero-token collection start: %v", err)
	}
	if _, err := database.Exec(
		`CREATE INDEX usage_events_user_time_test ON usage_events(user_email, occurred_at)`,
	); err != nil {
		database.Close()
		t.Fatalf("create zero-token usage index: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO usage_events(
			account, user_email, occurred_at, model, alias, reasoning_effort, failed,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			weighted_tokens, weight_policy_version
		) VALUES
			('alpha', 'alice@example.com', ?, 'gpt-zero', 'gpt-zero', 'none', 0, 0, 0, 0, 0, 0, 0, 'v2'),
			('alpha', 'alice@example.com', ?, 'gpt-zero', 'gpt-zero', 'none', 1, 0, 0, 0, 0, 0, 0, 'v2')`,
		now-120, now-60,
	); err != nil {
		database.Close()
		t.Fatalf("seed zero-token usage: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close zero-token daily trend fixture: %v", err)
	}

	store, err := OpenReadOnlyPath(path, time.Now)
	if err != nil {
		t.Fatalf("open zero-token daily trend store: %v", err)
	}
	defer store.Close()
	trend, err := store.UserDailyTrend(
		context.Background(), "alice@example.com", 1, "UTC", now,
		UserTrendModelReasoning,
	)
	if err != nil {
		t.Fatalf("read zero-token daily trend: %v", err)
	}
	if len(trend.Days) != 1 || trend.Days[0].RequestCount != 2 ||
		len(trend.Days[0].Combinations) != 1 ||
		trend.Days[0].Combinations[0].RequestCount != 2 {
		t.Fatalf("zero-token daily trend = %#v", trend)
	}
}
