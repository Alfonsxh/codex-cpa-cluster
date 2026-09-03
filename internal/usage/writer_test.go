package usage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWriterIngestsSchemaV10WithoutPersistingKeys(t *testing.T) {
	path := createWriterFixture(t, 10)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	writer, err := OpenWriterPath(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenWriterPath: %v", err)
	}
	defer writer.Close()

	const externalKey = "cpa_external_alice_0123456789abcdef"
	if count, err := writer.SyncIdentities(context.Background(), []Identity{
		{Key: externalKey, Label: "alice@example.com:alpha", UserEmail: "Alice@Example.com", Account: "alpha"},
		{Key: "", Label: "ignored", UserEmail: "ignored@example.com", Account: "alpha"},
	}); err != nil || count != 1 {
		t.Fatalf("SyncIdentities = %d, %v", count, err)
	}
	if count, err := writer.SyncUserTeams(context.Background(), map[string]TeamIdentity{
		"alice@example.com": {TeamID: "team-platform", MembershipVersion: 7},
	}); err != nil || count != 1 {
		t.Fatalf("SyncUserTeams = %d, %v", count, err)
	}
	startedAt, err := writer.EnsureUsageBreakdownStarted(context.Background())
	if err != nil || startedAt != now.Unix() {
		t.Fatalf("EnsureUsageBreakdownStarted = %d, %v", startedAt, err)
	}

	maxEvent := usageEvent(externalKey, "request-max", now.Unix()+10)
	duplicate := usageEvent(externalKey, "request-max", now.Unix()+10)
	highEvent := usageEvent(externalKey, "request-high", now.Unix()+20)
	highEvent["reasoning_effort"] = "high"
	highEvent["alias"] = ""
	highEvent["tokens"] = map[string]any{
		"input_tokens": 2, "output_tokens": 3, "reasoning_tokens": 1, "cached_tokens": 1,
	}
	ignored := usageEvent(externalKey, "models", now.Unix()+30)
	ignored["endpoint"] = "GET /v1/models"
	unmapped := usageEvent("missing-key", "missing", now.Unix()+40)
	missingAPIKey := usageEvent("", "missing-api-key", now.Unix()+50)

	counters, err := writer.IngestEvents(
		context.Background(),
		"alpha",
		[]Event{maxEvent, duplicate, highEvent, ignored, unmapped, missingAPIKey},
		map[string]float64{"high": 1.5},
	)
	if err != nil {
		t.Fatalf("IngestEvents: %v", err)
	}
	if counters != (IngestCounters{
		Received: 6, Inserted: 2, Duplicate: 1, Unmapped: 2,
		MissingAPIKey: 1, UnknownAPIKey: 1, Ignored: 1,
	}) {
		t.Fatalf("counters = %#v", counters)
	}

	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer database.Close()
	rows, err := database.Query(`
        SELECT request_id, user_email, alias, reasoning_effort, quota_multiplier,
               weighted_tokens, team_id, team_membership_version, weight_policy_version
          FROM usage_events ORDER BY id`)
	if err != nil {
		t.Fatalf("query events: %v", err)
	}
	defer rows.Close()
	type storedEvent struct {
		requestID, userEmail, alias, effort, teamID, policy string
		multiplier                                          float64
		weighted, membership                                int64
	}
	stored := make([]storedEvent, 0)
	for rows.Next() {
		var event storedEvent
		if err := rows.Scan(
			&event.requestID, &event.userEmail, &event.alias, &event.effort, &event.multiplier,
			&event.weighted, &event.teamID, &event.membership, &event.policy,
		); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		stored = append(stored, event)
	}
	if len(stored) != 2 {
		t.Fatalf("stored events = %#v", stored)
	}
	if stored[0].requestID != "request-max" || stored[0].userEmail != "alice@example.com" ||
		stored[0].effort != "max" || stored[0].multiplier != 2 || stored[0].weighted != 30 ||
		stored[0].teamID != "team-platform" || stored[0].membership != 7 {
		t.Fatalf("max event = %#v", stored[0])
	}
	if stored[1].alias != "gpt-5.6-sol" || stored[1].multiplier != 1.5 || stored[1].weighted != 8 {
		t.Fatalf("high event = %#v", stored[1])
	}
	if stored[0].policy != "reasoning-ac55ce6669f3" || stored[0].policy != stored[1].policy {
		t.Fatalf("policy versions = %q, %q", stored[0].policy, stored[1].policy)
	}

	var totalTokens, weightedTokens, requestCount int64
	if err := database.QueryRow(`
        SELECT total_tokens, weighted_tokens, request_count
          FROM user_weekly_usage WHERE user_email = 'alice@example.com'`,
	).Scan(&totalTokens, &weightedTokens, &requestCount); err != nil {
		t.Fatalf("query weekly usage: %v", err)
	}
	if totalTokens != 20 || weightedTokens != 38 || requestCount != 2 {
		t.Fatalf("weekly usage = raw %d, weighted %d, requests %d", totalTokens, weightedTokens, requestCount)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read usage database: %v", err)
	}
	if strings.Contains(string(raw), externalKey) {
		t.Fatal("usage database contains the raw API key")
	}

	reader, err := OpenReadOnlyPath(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenReadOnlyPath after write: %v", err)
	}
	defer reader.Close()
	breakdown, err := reader.UserBreakdown(context.Background(), "alice@example.com", "alpha", 0, nil)
	if err != nil {
		t.Fatalf("UserBreakdown: %v", err)
	}
	if breakdown.Totals.RequestCount != 2 || breakdown.Totals.TotalTokens != 20 || breakdown.Totals.WeightedTokens != 38 {
		t.Fatalf("reader totals = %#v", breakdown.Totals)
	}
}

func TestWriterDeduplicatesDigestEventsAndNormalizesBearerKeys(t *testing.T) {
	path := createWriterFixture(t, 10)
	now := time.Unix(1_800_000_000, 0)
	writer, err := OpenWriterPath(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenWriterPath: %v", err)
	}
	defer writer.Close()

	if _, err := writer.SyncIdentities(context.Background(), []Identity{
		{Key: "secret-key", Label: "alice", UserEmail: "alice@example.com", Account: "alpha"},
	}); err != nil {
		t.Fatalf("SyncIdentities: %v", err)
	}
	event := usageEvent("Bearer secret-key", "", now.Unix())
	if got := usageEventKey("alpha", "", keyHash("Bearer secret-key"), event); got !=
		"alpha:sha256:e551adaac06d32d6c54b49cf64ffa17529db3cf85ba526b111204f75fdef239e" {
		t.Fatalf("stable digest event key = %s", got)
	}
	first, err := writer.IngestEvents(context.Background(), "alpha", []Event{event}, nil)
	if err != nil || first.Inserted != 1 {
		t.Fatalf("first ingest = %#v, %v", first, err)
	}
	second, err := writer.IngestEvents(context.Background(), "alpha", []Event{event}, nil)
	if err != nil || second.Duplicate != 1 {
		t.Fatalf("second ingest = %#v, %v", second, err)
	}
}

func TestWriterRequiresExactSchemaWithoutMigrating(t *testing.T) {
	path := createWriterFixture(t, 9)
	if _, err := OpenWriterPath(path, time.Now); err == nil || !strings.Contains(err.Error(), "migration is not automatic") {
		t.Fatalf("old schema error = %v", err)
	}
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer database.Close()
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read schema version: %v", err)
	}
	if version != 9 {
		t.Fatalf("writer migrated schema to %d", version)
	}
}

func TestWriterWaitsForSQLiteBusyBeforeReadThenWriteIngest(t *testing.T) {
	path := createWriterFixture(t, 10)
	now := time.Unix(1_800_000_000, 0)
	writer, err := OpenWriterPath(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenWriterPath: %v", err)
	}
	defer writer.Close()
	if _, err := writer.SyncIdentities(context.Background(), []Identity{
		{Key: "alice-key", Label: "alice", UserEmail: "alice@example.com", Account: "alpha"},
	}); err != nil {
		t.Fatalf("seed usage identity: %v", err)
	}
	blocker, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open external usage blocker: %v", err)
	}
	defer blocker.Close()
	if _, err := blocker.Exec("PRAGMA busy_timeout = 1000"); err != nil {
		t.Fatalf("configure external usage blocker: %v", err)
	}
	if _, err := blocker.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("begin external usage write: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	type ingestResult struct {
		counters IngestCounters
		err      error
	}
	done := make(chan ingestResult, 1)
	go func() {
		close(started)
		counters, err := writer.IngestEvents(
			ctx,
			"alpha",
			[]Event{usageEvent("alice-key", "busy-ingest", now.Unix())},
			nil,
		)
		done <- ingestResult{counters: counters, err: err}
	}()
	<-started
	select {
	case result := <-done:
		t.Fatalf("usage ingest did not wait for SQLite busy state: %#v", result)
	case <-time.After(100 * time.Millisecond):
	}
	if _, err := writer.Status(ctx); err != nil {
		t.Fatalf("read-only status while external writer holds WAL lock: %v", err)
	}
	if _, err := blocker.Exec("COMMIT"); err != nil {
		t.Fatalf("release external usage write: %v", err)
	}
	result := <-done
	if result.err != nil || result.counters.Inserted != 1 {
		t.Fatalf("usage ingest after SQLite busy recovery = (%#v, %v)", result.counters, result.err)
	}
	status, err := writer.Status(ctx)
	if err != nil || status.EventCount != 1 || status.LastEventAt != now.Unix() {
		t.Fatalf("usage status after busy recovery = (%#v, %v)", status, err)
	}
}

func TestWriterBuildsWeightedWeeklyQuotaAndCollectorStatus(t *testing.T) {
	path := createWriterFixture(t, 10)
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	writer, err := OpenWriterPath(path, func() time.Time { return now })
	if err != nil {
		t.Fatalf("OpenWriterPath: %v", err)
	}
	defer writer.Close()
	if _, err := writer.SyncIdentities(context.Background(), []Identity{
		{Key: "alice-key", Label: "alice", UserEmail: "alice@example.com", Account: "alpha"},
	}); err != nil {
		t.Fatalf("SyncIdentities: %v", err)
	}
	if _, err := writer.EnsureUsageBreakdownStarted(context.Background()); err != nil {
		t.Fatalf("EnsureUsageBreakdownStarted: %v", err)
	}
	event := usageEvent("alice-key", "quota", now.Unix())
	if _, err := writer.IngestEvents(context.Background(), "alpha", []Event{event}, nil); err != nil {
		t.Fatalf("IngestEvents: %v", err)
	}
	weekStart, weekEnd := naturalWeekBounds(now.Unix(), time.UTC)
	if _, err := writer.db.Exec(`
        INSERT INTO user_quota_policies(
            user_email, weekly_tokens, created_at, updated_at, created_by, reset_at
        ) VALUES ('alice@example.com', 500, ?, ?, 'admin', ?),
                 ('expired@example.com', 100, ?, ?, 'admin', ?)`,
		now.Unix(), now.Unix(), weekEnd,
		now.Unix()-100, now.Unix()-100, now.Unix()-1,
	); err != nil {
		t.Fatalf("seed policies: %v", err)
	}
	if _, err := writer.db.Exec(`
        INSERT INTO user_quota_adjustments(
            user_email, week_start_at, action, token_amount, reason, created_at, created_by
        ) VALUES ('alice@example.com', ?, 'bonus', 100, 'bonus', ?, 'admin'),
                 ('alice@example.com', ?, 'usage_reset', 5, 'reset', ?, 'admin')`,
		weekStart, now.Unix(), weekStart, now.Unix(),
	); err != nil {
		t.Fatalf("seed adjustments: %v", err)
	}
	configuration, err := writer.ConfigurePersonalQuotaReset(context.Background(), true, false)
	if err != nil {
		t.Fatalf("ConfigurePersonalQuotaReset: %v", err)
	}
	if configuration.ExpiredPolicies != 1 || configuration.ScheduledPolicies != 0 || configuration.WeekEndAt != weekEnd {
		t.Fatalf("quota reset configuration = %#v", configuration)
	}
	defaultLimit := int64(1000)
	quotas, err := writer.WeeklyQuotas(
		context.Background(),
		[]string{"Alice@Example.com", "bob@example.com", "alice@example.com"},
		&defaultLimit,
	)
	if err != nil {
		t.Fatalf("WeeklyQuotas: %v", err)
	}
	alice := quotas["alice@example.com"]
	if alice.PolicyMode != "custom" || alice.LimitTokens == nil || *alice.LimitTokens != 600 ||
		alice.RawUsedTokens != 15 || alice.WeightedRawUsedTokens != 30 || alice.UsedTokens != 25 ||
		alice.RemainingTokens == nil || *alice.RemainingTokens != 575 || alice.AdjustmentCount != 2 {
		t.Fatalf("alice quota = %#v", alice)
	}
	bob := quotas["bob@example.com"]
	if bob.PolicyMode != "inherit" || bob.LimitTokens == nil || *bob.LimitTokens != 1000 || bob.UsedTokens != 0 {
		t.Fatalf("bob quota = %#v", bob)
	}

	if err := writer.UpdateCollectorStatus(context.Background(), ""); err != nil {
		t.Fatalf("UpdateCollectorStatus: %v", err)
	}
	status, err := writer.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Status != "healthy" || status.HeartbeatAt != now.Unix() || status.EventCount != 1 ||
		status.UsageBreakdownStartedAt != now.Unix() || status.LastEventAt != now.Unix() {
		t.Fatalf("collector status = %#v", status)
	}
}

func TestNaturalWeekBoundsUseConfiguredTimezone(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	sunday := time.Date(2026, 8, 2, 23, 59, 0, 0, location).Unix()
	start, end := naturalWeekBounds(sunday, location)
	if want := time.Date(2026, 7, 27, 0, 0, 0, 0, location).Unix(); start != want {
		t.Fatalf("week start = %d, want %d", start, want)
	}
	if want := time.Date(2026, 8, 3, 0, 0, 0, 0, location).Unix(); end != want {
		t.Fatalf("week end = %d, want %d", end, want)
	}
}

func TestWriterRebuildsLegacyWeightAndRebucketsTimezone(t *testing.T) {
	path := createWriterFixture(t, 10)
	eventAt := time.Date(2026, 8, 2, 18, 0, 0, 0, time.UTC)
	writer, err := OpenWriterPath(path, func() time.Time { return eventAt })
	if err != nil {
		t.Fatalf("OpenWriterPath: %v", err)
	}
	defer writer.Close()
	if _, err := writer.SyncIdentities(context.Background(), []Identity{
		{Key: "legacy-key", Label: "legacy", UserEmail: "alice@example.com", Account: "alpha"},
	}); err != nil {
		t.Fatalf("SyncIdentities: %v", err)
	}
	if _, err := writer.IngestEvents(
		context.Background(), "alpha", []Event{usageEvent("legacy-key", "legacy", eventAt.Unix())}, nil,
	); err != nil {
		t.Fatalf("IngestEvents: %v", err)
	}
	if _, err := writer.db.Exec(
		"UPDATE usage_events SET weighted_tokens = 0, weight_policy_version = 'legacy-v1'",
	); err != nil {
		t.Fatalf("mark legacy event: %v", err)
	}
	result, err := writer.RebuildWeeklyUsage(context.Background())
	if err != nil {
		t.Fatalf("RebuildWeeklyUsage: %v", err)
	}
	if !result.Backfilled || result.Events != 1 || result.Counters != 1 {
		t.Fatalf("rebuild result = %#v", result)
	}
	var utcWeekStart, weighted int64
	if err := writer.db.QueryRow(
		"SELECT week_start_at, weighted_tokens FROM user_weekly_usage",
	).Scan(&utcWeekStart, &weighted); err != nil {
		t.Fatalf("read rebuilt usage: %v", err)
	}
	if weighted != 15 {
		t.Fatalf("legacy weighted usage = %d", weighted)
	}
	changed, err := writer.EnsureWeekTimezone(context.Background(), "Asia/Shanghai")
	if err != nil {
		t.Fatalf("EnsureWeekTimezone: %v", err)
	}
	if !changed {
		t.Fatal("timezone change was not applied")
	}
	var shanghaiWeekStart int64
	if err := writer.db.QueryRow(
		"SELECT week_start_at FROM user_weekly_usage",
	).Scan(&shanghaiWeekStart); err != nil {
		t.Fatalf("read rebucketed usage: %v", err)
	}
	if shanghaiWeekStart == utcWeekStart {
		t.Fatalf("week start did not change: %d", shanghaiWeekStart)
	}
	changed, err = writer.EnsureWeekTimezone(context.Background(), "Asia/Shanghai")
	if err != nil || changed {
		t.Fatalf("unchanged timezone = %v, %v", changed, err)
	}
}

func usageEvent(key, requestID string, timestamp int64) Event {
	return Event{
		"timestamp": timestamp, "latency_ms": 123, "provider": "openai",
		"model": "gpt-5.6-sol", "alias": "gpt-5.6-sol", "reasoning_effort": "max",
		"endpoint": "POST /v1/responses", "api_key": key, "request_id": requestID,
		"failed": false,
		"tokens": map[string]any{
			"input_tokens": 10, "output_tokens": 5, "reasoning_tokens": 2,
			"cached_tokens": 3, "total_tokens": 15,
		},
	}
}

func createWriterFixture(t *testing.T, version int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage.sqlite3")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open usage fixture: %v", err)
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
		`CREATE TABLE usage_events (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            event_key TEXT NOT NULL UNIQUE,
            account TEXT NOT NULL,
            user_email TEXT NOT NULL,
            key_label TEXT NOT NULL,
            occurred_at INTEGER NOT NULL,
            request_id TEXT NOT NULL DEFAULT '',
            provider TEXT NOT NULL DEFAULT '',
            model TEXT NOT NULL DEFAULT '',
            alias TEXT NOT NULL DEFAULT '',
            reasoning_effort TEXT NOT NULL DEFAULT '',
            endpoint TEXT NOT NULL DEFAULT '',
            failed INTEGER NOT NULL DEFAULT 0,
            latency_ms INTEGER NOT NULL DEFAULT 0,
            input_tokens INTEGER NOT NULL DEFAULT 0,
            output_tokens INTEGER NOT NULL DEFAULT 0,
            reasoning_tokens INTEGER NOT NULL DEFAULT 0,
            cached_tokens INTEGER NOT NULL DEFAULT 0,
            total_tokens INTEGER NOT NULL DEFAULT 0,
            quota_multiplier REAL NOT NULL DEFAULT 1.0,
            weighted_tokens INTEGER NOT NULL DEFAULT 0,
            weight_policy_version TEXT NOT NULL DEFAULT 'legacy-v1',
            team_id TEXT NOT NULL DEFAULT '',
            team_membership_version INTEGER NOT NULL DEFAULT 0
        )`,
		`CREATE INDEX usage_events_user_time ON usage_events(user_email, occurred_at)`,
		`CREATE TABLE usage_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`INSERT INTO usage_meta(key, value) VALUES ('weekly_usage_timezone', 'UTC')`,
		`CREATE TABLE user_weekly_usage (
            user_email TEXT NOT NULL,
            week_start_at INTEGER NOT NULL,
            total_tokens INTEGER NOT NULL DEFAULT 0,
            weighted_tokens INTEGER NOT NULL DEFAULT 0,
            request_count INTEGER NOT NULL DEFAULT 0,
            updated_at INTEGER NOT NULL,
            PRIMARY KEY(user_email, week_start_at)
        )`,
		`CREATE TABLE user_quota_policies (
            user_email TEXT PRIMARY KEY,
            weekly_tokens INTEGER CHECK(weekly_tokens IS NULL OR weekly_tokens > 0),
            created_at INTEGER NOT NULL,
            updated_at INTEGER NOT NULL,
            created_by TEXT NOT NULL DEFAULT 'admin',
            reset_at INTEGER
        )`,
		`CREATE TABLE user_quota_adjustments (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            user_email TEXT NOT NULL,
            week_start_at INTEGER NOT NULL,
            action TEXT NOT NULL CHECK(action IN ('bonus', 'usage_reset')),
            token_amount INTEGER NOT NULL CHECK(token_amount > 0),
            reason TEXT NOT NULL,
            created_at INTEGER NOT NULL,
            created_by TEXT NOT NULL DEFAULT 'admin'
        )`,
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
	return path
}
