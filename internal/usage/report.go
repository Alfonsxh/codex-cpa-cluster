package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ReportWindow is a calendar-day interval, start inclusive and end exclusive.
// Calendar boundaries are resolved in the business timezone by the caller.
type ReportWindow struct {
	StartAt int64
	EndAt   int64
}

type ReportUsageRow struct {
	Window  int
	Account string
	User    string
	Usage   WeightedMetrics
}

type reportMetricRow struct {
	Window  int    `db:"report_window"`
	Account string `db:"account"`
	User    string `db:"user_email"`
	breakdownRow
}

var ErrReportTooLarge = errors.New("weekly usage report exceeds its size limit")

const maxReportUsageRows = 100_000

// ReportUsage reads both weeks in one SQLite snapshot. Grouping happens in SQL,
// so no request-level records or credentials leave the store. It deliberately
// includes historical identities absent from the current control-plane catalog.
func (store *Store) ReportUsage(ctx context.Context, windows []ReportWindow) ([]ReportUsageRow, error) {
	if len(windows) == 0 || len(windows) > 14 {
		return nil, errors.New("report requires 1 to 14 day windows")
	}
	values := make([]string, len(windows))
	args := make([]any, 0, 3*len(windows))
	for index, window := range windows {
		if window.StartAt >= window.EndAt || window.EndAt-window.StartAt > 26*3600 ||
			(index > 0 && window.StartAt < windows[index-1].EndAt) ||
			window.EndAt-windows[0].StartAt > 15*86400 {
			return nil, errors.New("invalid report day windows")
		}
		values[index] = "(?, ?, ?)"
		args = append(args, index, window.StartAt, window.EndAt)
	}
	tx, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin report snapshot: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryxContext(ctx, `WITH report_days(report_window, start_at, end_at) AS (VALUES `+
		strings.Join(values, ",")+`)
		SELECT report_window, TRIM(account) AS account, LOWER(TRIM(user_email)) AS user_email, `+teamMetricsSQL+`
		FROM report_days JOIN usage_events ON occurred_at >= start_at AND occurred_at < end_at
		GROUP BY report_window, TRIM(account), LOWER(TRIM(user_email))
		LIMIT 100001`, args...)
	if err != nil {
		return nil, fmt.Errorf("query report usage: %w", err)
	}
	defer rows.Close()
	result := make([]ReportUsageRow, 0)
	for rows.Next() {
		if len(result) >= maxReportUsageRows {
			return nil, ErrReportTooLarge
		}
		var row reportMetricRow
		if err := rows.StructScan(&row); err != nil {
			return nil, fmt.Errorf("read report usage: %w", err)
		}
		result = append(result, ReportUsageRow{Window: row.Window, Account: row.Account, User: row.User, Usage: row.weightedMetrics()})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read report rows: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish report snapshot: %w", err)
	}
	return result, nil
}
