package usage

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestReportUsagePreservesWeightsAndHistoricalIdentities(t *testing.T) {
	path := createUsageFixture(t, 10)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`INSERT INTO usage_events(account, user_email, occurred_at, total_tokens, weighted_tokens, weight_policy_version)
		VALUES ('removed-account', ' DELETED@example.com ', 6200, 7, 0, 'current-policy'),
		('alpha', ' ALICE@EXAMPLE.COM ', 6000, 1, 4, 'current-policy')`)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenReadOnlyPath(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.ReportUsage(context.Background(), []ReportWindow{{5500, 6100}, {6100, 6300}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 {
		t.Fatalf("rows = %+v", rows)
	}
	var total, weighted, requests int64
	var removed, legacy, normalized bool
	for _, row := range rows {
		total += row.Usage.TotalTokens
		weighted += row.Usage.WeightedTokens
		requests += row.Usage.RequestCount
		if row.Account == "removed-account" {
			removed = row.User == "deleted@example.com" && row.Usage.WeightedTokens == 0
		}
		if row.Account == "beta" {
			legacy = row.Usage.WeightedTokens == 80
		}
		if row.Window == 0 {
			normalized = row.User == "alice@example.com" && row.Usage.RequestCount == 2 && row.Usage.WeightedTokens == 129
		}
	}
	if total != 238 || weighted != 284 || requests != 5 || !removed || !legacy || !normalized {
		t.Fatalf("total=%d weighted=%d requests=%d rows=%+v", total, weighted, requests, rows)
	}
	if _, err := store.ReportUsage(context.Background(), []ReportWindow{{6000, 6100}, {6050, 6150}}); err == nil {
		t.Fatal("overlapping windows accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.ReportUsage(ctx, []ReportWindow{{5500, 6100}}); err == nil {
		t.Fatal("cancellation ignored")
	}
}

func TestReportUsageRejectsOversizedResultInsteadOfTruncating(t *testing.T) {
	path := createUsageFixture(t, 10)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<100001)
		INSERT INTO usage_events(account, user_email, occurred_at) SELECT 'alpha', 'user-' || x, 8000 FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenReadOnlyPath(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	rows, err := store.ReportUsage(context.Background(), []ReportWindow{{8000, 8001}})
	if !errors.Is(err, ErrReportTooLarge) || rows != nil {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
}
