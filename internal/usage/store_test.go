package usage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyStoreMatchesExistingBreakdownSemantics(t *testing.T) {
	path := createUsageFixture(t, 10)
	store, err := OpenReadOnlyPath(path, func() time.Time { return time.Unix(7000, 0) })
	if err != nil {
		t.Fatalf("OpenReadOnlyPath: %v", err)
	}
	defer store.Close()
	endAt := int64(6300)
	user, err := store.UserBreakdown(
		context.Background(), "Alice@Example.com", "", 5500, &endAt,
	)
	if err != nil {
		t.Fatalf("UserBreakdown: %v", err)
	}
	if user.CollectionStartedAt != 5000 || user.EffectiveStartAt != 5500 {
		t.Fatalf("user usage bounds = %#v", user)
	}
	if user.Totals.RequestCount != 3 || user.Totals.SuccessCount != 2 ||
		user.Totals.FailedCount != 1 || user.Totals.TotalTokens != 180 ||
		user.Totals.WeightedTokens != 205 || user.Totals.KnownEffortCount != 1 ||
		user.Totals.LastUsedAt != 6200 {
		t.Fatalf("user totals = %#v", user.Totals)
	}
	if len(user.Models) != 2 || len(user.ReasoningEfforts) != 2 || len(user.Combinations) != 2 {
		t.Fatalf("user breakdown = %#v", user)
	}
	if user.Models[0].Model != "gpt-5.6-sol" || user.Models[1].Model != "gpt-5.6-terra" {
		t.Fatalf("user model ordering = %#v", user.Models)
	}

	account, err := store.AccountBreakdown(context.Background(), "alpha", 5500, &endAt)
	if err != nil {
		t.Fatalf("AccountBreakdown: %v", err)
	}
	if account.Totals.RequestCount != 2 || account.Totals.SuccessCount != 1 ||
		account.Totals.FailedCount != 1 || account.Totals.TotalTokens != 150 ||
		account.Totals.LastUsedAt != 6100 {
		t.Fatalf("account totals = %#v", account.Totals)
	}
	raw, err := json.Marshal(account)
	if err != nil {
		t.Fatalf("marshal account breakdown: %v", err)
	}
	if strings.Contains(string(raw), "weighted") || strings.Contains(string(raw), "user_email") {
		t.Fatalf("account response leaks weighted or user data: %s", raw)
	}

	activity, err := store.RefreshActiveUsersLastHour(context.Background())
	if err != nil {
		t.Fatalf("RefreshActiveUsersLastHour: %v", err)
	}
	if activity["alpha"] != 2 || activity["beta"] != 1 {
		t.Fatalf("one-hour activity = %#v", activity)
	}
	activeEmails, err := store.ActiveUserEmailsLastHour(context.Background())
	if err != nil {
		t.Fatalf("ActiveUserEmailsLastHour: %v", err)
	}
	if len(activeEmails["alpha"]) != 2 || activeEmails["alpha"][0] != "alice@example.com" ||
		activeEmails["alpha"][1] != "bob@example.com" || len(activeEmails["beta"]) != 1 ||
		activeEmails["beta"][0] != "alice@example.com" {
		t.Fatalf("one-hour activity emails = %#v", activeEmails)
	}
	if _, err := store.db.Exec("INSERT INTO usage_meta(key, value) VALUES ('forbidden', '1')"); err == nil {
		t.Fatal("read-only usage store accepted a write")
	}

	accounts, err := store.UserAccounts(context.Background(), "Alice@Example.com", 5500, &endAt)
	if err != nil {
		t.Fatalf("UserAccounts: %v", err)
	}
	if len(accounts.Accounts) != 2 || accounts.Totals.RequestCount != 3 ||
		accounts.Totals.FailedCount != 1 || accounts.Totals.TotalTokens != 230 ||
		accounts.Totals.WeightedTokens != 280 {
		t.Fatalf("user account summary = %#v", accounts)
	}
	userSummaries, err := store.UserSummaries(context.Background(), 5500, &endAt)
	if err != nil {
		t.Fatalf("UserSummaries: %v", err)
	}
	if len(userSummaries) != 1 || userSummaries["alice@example.com"].RequestCount != 3 ||
		userSummaries["alice@example.com"].FailedCount != 1 ||
		userSummaries["alice@example.com"].TotalTokens != 230 ||
		userSummaries["alice@example.com"].WeightedTokens != 280 ||
		userSummaries["alice@example.com"].LastUsedAt != 6200 {
		t.Fatalf("user usage summaries = %#v", userSummaries)
	}
	selectedSummaries, err := store.UserSummariesForUsers(
		context.Background(), []string{" ALICE@example.com ", "alice@example.com", "missing@example.com"}, 5500, &endAt,
	)
	if err != nil {
		t.Fatalf("UserSummariesForUsers: %v", err)
	}
	if len(selectedSummaries) != 1 || selectedSummaries["alice@example.com"].WeightedTokens != 280 {
		t.Fatalf("selected user usage summaries = %#v", selectedSummaries)
	}
	emptySummaries, err := store.UserSummariesForUsers(context.Background(), nil, 5500, &endAt)
	if err != nil || len(emptySummaries) != 0 {
		t.Fatalf("empty selected user usage summaries = (%#v, %v)", emptySummaries, err)
	}

	summaries, err := store.AccountSummaries(
		context.Background(), []string{"alpha", "beta", "gamma"}, 5500, &endAt,
	)
	if err != nil {
		t.Fatalf("AccountSummaries: %v", err)
	}
	if summaries["alpha"].ActiveUsers != 1 || summaries["alpha"].RequestCount != 2 ||
		summaries["alpha"].FailedCount != 1 || summaries["alpha"].TotalTokens != 150 ||
		summaries["alpha"].LastUsedAt != 6100 || summaries["beta"].RequestCount != 1 ||
		summaries["beta"].TotalTokens != 80 || summaries["gamma"].RequestCount != 0 {
		t.Fatalf("account usage summaries = %#v", summaries)
	}

	perAccountEndAt := int64(6300)
	perAccount, err := store.AccountSummariesByStart(
		context.Background(), map[string]int64{"alpha": 6100, "beta": 6000, "gamma": 6000}, &perAccountEndAt,
	)
	if err != nil {
		t.Fatalf("AccountSummariesByStart: %v", err)
	}
	if perAccount["alpha"].RequestCount != 1 || perAccount["alpha"].FailedCount != 1 ||
		perAccount["alpha"].TotalTokens != 50 || perAccount["alpha"].LastUsedAt != 6100 ||
		perAccount["beta"].RequestCount != 1 || perAccount["beta"].TotalTokens != 80 ||
		perAccount["gamma"].RequestCount != 0 {
		t.Fatalf("per-account usage summaries = %#v", perAccount)
	}
}

func TestReadOnlyStoreAggregatesTeamsByCurrentMembershipWithBoundedBreakdown(t *testing.T) {
	path := createUsageFixture(t, 10)
	store, err := OpenReadOnlyPath(path, func() time.Time { return time.Unix(7000, 0) })
	if err != nil {
		t.Fatalf("OpenReadOnlyPath: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	endAt := int64(6300)
	teamUsage, err := store.TeamUsage(
		ctx,
		[]string{"team_alpha", "team_beta"},
		map[string]string{"alice@example.com": "team_beta", "bob@example.com": ""},
		func() *int64 { value := int64(5500); return &value }(),
		&endAt,
	)
	if err != nil {
		t.Fatalf("TeamUsage: %v", err)
	}
	if teamUsage["team_alpha"].TotalTokens != 0 ||
		teamUsage["team_beta"].TotalTokens != 230 || teamUsage["team_beta"].WeightedTokens != 280 ||
		teamUsage["team_beta"].RequestCount != 3 || teamUsage["team_beta"].FailedCount != 1 ||
		teamUsage["team_beta"].ActiveUsers != 1 || teamUsage["unassigned"].TotalTokens != 0 {
		t.Fatalf("team usage = %#v", teamUsage)
	}

	breakdown, err := store.TeamBreakdown(
		ctx, "team_beta", []string{"alice@example.com"}, func() *int64 { value := int64(5500); return &value }(), &endAt,
	)
	if err != nil {
		t.Fatalf("TeamBreakdown: %v", err)
	}
	if breakdown.TeamID != "team_beta" || breakdown.Attribution != "current_membership" ||
		breakdown.Totals.TotalTokens != 230 || breakdown.Totals.WeightedTokens != 280 ||
		len(breakdown.Users) != 1 || breakdown.Users[0].User != "alice@example.com" ||
		len(breakdown.Accounts) != 2 || len(breakdown.Models) != 2 || len(breakdown.Combinations) != 2 ||
		breakdown.Series.StartAt != 5500 || breakdown.Series.EndAt != endAt ||
		breakdown.Series.BucketSeconds != 300 || sumInt64(breakdown.Series.Values) != 280 {
		t.Fatalf("team breakdown = %#v", breakdown)
	}

	largeUsers := make([]string, 1_100)
	for index := range largeUsers {
		largeUsers[index] = fmt.Sprintf("member%04d@example.com", index)
	}
	large, err := store.TeamBreakdown(ctx, "team_large", largeUsers, nil, nil)
	if err != nil || large.Totals.TotalTokens != 0 || len(large.Users) != 0 {
		t.Fatalf("large team breakdown = (%#v, %v)", large, err)
	}
}

func TestReadOnlyStoreBuildsBoundedTokenTrendsAndPublicUsage(t *testing.T) {
	path := createUsageFixture(t, 10)
	store, err := OpenReadOnlyPath(path, func() time.Time { return time.Unix(7000, 0) })
	if err != nil {
		t.Fatalf("OpenReadOnlyPath: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	trend, err := store.TokenTimeSeries(
		ctx,
		[]string{"alpha", "beta"},
		[]string{"alice@example.com", "bob@example.com"},
		[]string{"alice@example.com"},
		5500,
		6300,
		300,
		10,
		TokenModeUnweighted,
		nil,
	)
	if err != nil {
		t.Fatalf("TokenTimeSeries: %v", err)
	}
	if trend.GeneratedAt != 6299 || trend.WindowStartAt != 5500 || trend.WindowSeconds != 800 ||
		len(trend.Buckets) != 3 || len(trend.Accounts) != 2 || len(trend.Users) != 1 ||
		trend.Accounts[0].Name != "alpha" || trend.Accounts[0].Total != 150 || trend.Accounts[0].WeightedTotal != 200 ||
		trend.Accounts[1].Name != "beta" || trend.Accounts[1].Total != 80 || trend.Accounts[1].WeightedTotal != 80 ||
		trend.Users[0].Name != "alice@example.com" || trend.Users[0].Total != 230 || trend.Users[0].WeightedTotal != 280 {
		t.Fatalf("token trend = %#v", trend)
	}
	periodTrend, err := store.TokenTimeSeries(
		ctx,
		[]string{"alpha", "beta"},
		[]string{"alice@example.com", "bob@example.com"},
		[]string{"alice@example.com"},
		5500,
		6300,
		300,
		10,
		TokenModeUnweighted,
		map[string]int64{"alpha": 6050, "beta": 5500},
	)
	if err != nil {
		t.Fatalf("TokenTimeSeries by account period: %v", err)
	}
	if len(periodTrend.Accounts) != 2 || periodTrend.Accounts[0].Total != 50 ||
		periodTrend.Accounts[0].Average != 50 || periodTrend.Accounts[0].WeightedTotal != 75 ||
		periodTrend.Accounts[1].Total != 80 || periodTrend.Accounts[1].WeightedTotal != 80 ||
		len(periodTrend.Users) != 1 || periodTrend.Users[0].Total != 130 || periodTrend.Users[0].WeightedTotal != 155 {
		t.Fatalf("period token trend = %#v", periodTrend)
	}
	public, err := store.PublicGatewayUsage(ctx, []string{"alpha", "beta"}, 6000, 7001)
	if err != nil {
		t.Fatalf("PublicGatewayUsage: %v", err)
	}
	if public["alpha"].ActiveKeys != 2 || public["alpha"].RequestCount != 5 ||
		public["beta"].ActiveKeys != 1 || public["beta"].RequestCount != 1 {
		t.Fatalf("public gateway usage = %#v", public)
	}
}

func TestTokenTimeSeriesRanksUsersByWeightedTokens(t *testing.T) {
	path := createUsageFixture(t, 10)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open usage ranking fixture: %v", err)
	}
	if _, err := database.Exec(`
		INSERT INTO usage_events(
			account, user_email, occurred_at, total_tokens, weighted_tokens, weight_policy_version
		) VALUES
			('alpha', 'raw-leader@example.com', 6001, 200, 200, 'v2'),
			('alpha', 'weighted-leader@example.com', 6002, 100, 500, 'v2')`); err != nil {
		database.Close()
		t.Fatalf("write usage ranking fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close usage ranking fixture: %v", err)
	}

	store, err := OpenReadOnlyPath(path, func() time.Time { return time.Unix(7000, 0) })
	if err != nil {
		t.Fatalf("OpenReadOnlyPath: %v", err)
	}
	defer store.Close()
	trend, err := store.TokenTimeSeries(
		context.Background(),
		[]string{"alpha"},
		[]string{"raw-leader@example.com", "weighted-leader@example.com"},
		nil,
		5500,
		6300,
		300,
		10,
		TokenModeWeighted,
		nil,
	)
	if err != nil {
		t.Fatalf("TokenTimeSeries: %v", err)
	}
	if len(trend.Users) != 2 || trend.Users[0].Name != "weighted-leader@example.com" ||
		trend.Users[0].Total != 100 || trend.Users[0].WeightedTotal != 500 ||
		trend.Users[1].Name != "raw-leader@example.com" || trend.Users[1].Total != 200 ||
		trend.Users[1].WeightedTotal != 200 {
		t.Fatalf("weighted user ranking = %#v", trend.Users)
	}
	rawTrend, err := store.TokenTimeSeries(
		context.Background(),
		[]string{"alpha"},
		[]string{"raw-leader@example.com", "weighted-leader@example.com"},
		nil,
		5500,
		6300,
		300,
		1,
		TokenModeUnweighted,
		nil,
	)
	if err != nil {
		t.Fatalf("unweighted TokenTimeSeries: %v", err)
	}
	if len(rawTrend.Users) != 1 || rawTrend.Users[0].Name != "raw-leader@example.com" ||
		rawTrend.Users[0].Total != 200 {
		t.Fatalf("unweighted user ranking = %#v", rawTrend.Users)
	}
}

func sumInt64(values []int64) int64 {
	var total int64
	for _, value := range values {
		total += value
	}
	return total
}

func TestReadOnlyStoreRejectsOlderSchemaWithoutMigratingIt(t *testing.T) {
	path := createUsageFixture(t, 9)
	if _, err := OpenReadOnlyPath(path, time.Now); err == nil || !strings.Contains(err.Error(), "older") {
		t.Fatalf("old schema error = %v", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("reopen old fixture: %v", err)
	}
	defer database.Close()
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 9 {
		t.Fatalf("reader migrated schema to %d", version)
	}
}

func createUsageFixture(t *testing.T, version int) string {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "usage.sqlite3")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open usage fixture: %v", err)
	}
	statements := []string{
		`CREATE TABLE usage_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE usage_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            account TEXT NOT NULL,
            user_email TEXT NOT NULL,
            occurred_at INTEGER NOT NULL,
            model TEXT NOT NULL DEFAULT '',
            alias TEXT NOT NULL DEFAULT '',
            reasoning_effort TEXT NOT NULL DEFAULT '',
            failed INTEGER NOT NULL DEFAULT 0,
            input_tokens INTEGER NOT NULL DEFAULT 0,
            output_tokens INTEGER NOT NULL DEFAULT 0,
            reasoning_tokens INTEGER NOT NULL DEFAULT 0,
            cached_tokens INTEGER NOT NULL DEFAULT 0,
            total_tokens INTEGER NOT NULL DEFAULT 0,
            weighted_tokens INTEGER NOT NULL DEFAULT 0,
            weight_policy_version TEXT NOT NULL DEFAULT 'legacy-v1'
        )`,
		`INSERT INTO usage_meta(key, value) VALUES ('usage_breakdown_started_at', '5000')`,
		`INSERT INTO usage_events(
            account, user_email, occurred_at, model, alias, reasoning_effort, failed,
            input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
            weighted_tokens, weight_policy_version
        ) VALUES
            ('alpha', 'alice@example.com', 4999, 'old', 'old', 'low', 0, 1, 1, 0, 0, 2, 2, 'v2'),
            ('alpha', 'alice@example.com', 6000, 'gpt-5.6-sol', 'sol', 'high', 0, 60, 30, 10, 5, 100, 125, 'v2'),
            ('alpha', 'alice@example.com', 6100, 'gpt-5.6-sol', 'sol', 'high', 1, 20, 20, 10, 0, 50, 75, 'v2'),
            ('beta', 'alice@example.com', 6200, 'gpt-5.6-terra', 'terra', '', 0, 50, 20, 10, 0, 80, 0, 'legacy-v1'),
            ('alpha', 'alice@example.com', 6300, 'boundary', 'boundary', 'low', 0, 5, 5, 0, 0, 10, 10, 'v2'),
            ('alpha', 'alice@example.com', 6400, 'hidden', '', 'low', 0, 5, 5, 0, 0, 10, 10, 'v2'),
            ('alpha', 'bob@example.com', 6999, 'gpt-5.6-sol', 'sol', 'low', 0, 1, 1, 0, 0, 2, 2, 'v2')`,
		"PRAGMA user_version = " + strconv.Itoa(version),
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			database.Close()
			t.Fatalf("create usage fixture: %v\n%s", err, statement)
		}
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close usage fixture: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("secure usage fixture: %v", err)
	}
	return path
}
