package controlplane

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

// Reader exposes the control-plane database through a query-only connection.
// Health checks use it so probing a missing or damaged target cannot create a
// database, migration, WAL file, or encryption key as a side effect.
type Reader struct {
	root string
	path string
	db   *sqlx.DB
}

func OpenReadOnly(ctx context.Context, root string) (*Reader, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("control-plane root is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve control-plane root: %w", err)
	}
	path := filepath.Join(filepath.Clean(absoluteRoot), databaseRelativePath)
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("open existing control-plane database: %w", err)
	}
	dsn := &url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "query_only(1)")
	dsn.RawQuery = query.Encode()
	database, err := sqlx.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open control-plane database read-only: %w", err)
	}
	database.SetMaxOpenConns(2)
	database.SetMaxIdleConns(1)
	database.SetConnMaxIdleTime(time.Minute)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("connect to control-plane database read-only: %w", err)
	}
	return &Reader{root: filepath.Clean(absoluteRoot), path: path, db: database}, nil
}

func (reader *Reader) Root() string {
	return reader.root
}

func (reader *Reader) Path() string {
	return reader.path
}

func (reader *Reader) Close() error {
	if reader == nil || reader.db == nil {
		return nil
	}
	return reader.db.Close()
}

func (reader *Reader) ReadRuntimeState(
	ctx context.Context,
	name string,
	destination any,
) (bool, error) {
	return readRuntimeState(ctx, reader.db, name, destination)
}

func (reader *Reader) ReadSettings(ctx context.Context) (map[string]any, error) {
	return readSettings(ctx, reader.db)
}

// SecretStatuses exposes only integrity digests and timestamps. Health checks
// can therefore verify that a required encrypted secret exists without opening
// the encryption key or returning secret material.
func (reader *Reader) SecretStatuses(ctx context.Context) (map[string]SecretStatus, error) {
	return secretStatuses(ctx, reader.db)
}
