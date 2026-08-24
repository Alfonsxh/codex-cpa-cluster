package usage

import (
	"net/url"
	"path/filepath"
	"time"

	"github.com/jmoiron/sqlx"
)

// openUsageSQLiteHandle keeps read snapshots and write transactions on
// separate single-connection pools. Write transactions start with
// BEGIN IMMEDIATE so SQLite waits for an existing writer before establishing
// a read snapshot; a deferred read-to-write upgrade cannot honor busy_timeout.
func openUsageSQLiteHandle(path string, immediateWrites bool) (*sqlx.DB, error) {
	dsn := &url.URL{Scheme: "file", Path: filepath.Clean(path)}
	query := url.Values{}
	query.Set("mode", "rw")
	query.Add("_pragma", "busy_timeout(10000)")
	query.Add("_pragma", "foreign_keys(1)")
	if immediateWrites {
		query.Set("_txlock", "immediate")
	}
	dsn.RawQuery = query.Encode()

	database, err := sqlx.Open("sqlite", dsn.String())
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	database.SetConnMaxIdleTime(time.Minute)
	return database, nil
}
