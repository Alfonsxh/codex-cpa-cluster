package usage

import (
	"context"
	"database/sql"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestActiveUserQueryBoundsHistoryBeforeAccountGrouping(t *testing.T) {
	path := createUsageFixture(t, 10)
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	for _, statement := range []string{
		`CREATE INDEX usage_events_account_time ON usage_events(account, occurred_at)`,
		`CREATE INDEX usage_events_time_user ON usage_events(occurred_at, user_email)`,
		`CREATE INDEX usage_events_user_time ON usage_events(user_email, occurred_at)`,
		`WITH RECURSIVE old(n) AS (SELECT 1 UNION ALL SELECT n+1 FROM old WHERE n<10000)
		 INSERT INTO usage_events(account,user_email,occurred_at)
		 SELECT 'alpha','old@example.com',n%3000 FROM old`,
		`INSERT INTO usage_events(account,user_email,occurred_at) VALUES
		 ('alpha',' ALICE@EXAMPLE.COM ',7000), ('alpha','   ',7000),
		 ('gamma','boundary@example.com',3400), ('gamma','expired@example.com',3399)`,
	} {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := database.Query("EXPLAIN QUERY PLAN "+activeUserEmailsLastHourQuery, 3400)
	if err != nil {
		t.Fatal(err)
	}
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	plan := strings.Join(details, "\n")
	if !strings.Contains(plan, "SEARCH usage_events USING INDEX usage_events_time_user (occurred_at>?)") ||
		strings.Contains(plan, "SCAN usage_events") {
		t.Fatalf("activity must search the time range, not scan historical events:\n%s", plan)
	}
	store, err := OpenReadOnlyPath(path, func() time.Time { return time.Unix(7000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	emails, err := store.ActiveUserEmailsLastHour(context.Background())
	want := map[string][]string{
		"alpha": {"alice@example.com", "bob@example.com"},
		"beta":  {"alice@example.com"}, "gamma": {"boundary@example.com"},
	}
	if err != nil || !reflect.DeepEqual(emails, want) {
		t.Fatalf("normalization, deduplication or rolling boundary changed: %v, %v", emails, err)
	}
}
