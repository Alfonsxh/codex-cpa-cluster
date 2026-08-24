package migrationcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/gateway"
	"github.com/jmoiron/sqlx"
	_ "modernc.org/sqlite"
)

const StateSummaryVersion = 1

// These values select host-local bindings for each isolated comparison stack.
// v1 and v2 must use different ports while they coexist, so treating them as
// durable business state would make every valid paired deployment fail its
// state gate. They remain in the operational summary and are therefore still
// visible as an explicit, reviewable difference.
const deploymentLocalSettings = `
key IN (
  'accounts.listen_address',
  'management.listen_address',
  'gateway.listen_address',
  'gateway.port',
  'gateway.internal_port',
  'management.port'
)`

var controlDurableTables = []tableSpec{
	{name: "accounts"},
	{name: "branding_assets"},
	{name: "encrypted_secrets"},
	{name: "internal_keys"},
	{name: "key_records"},
	{name: "metadata"},
	{name: "schema_migrations"},
	{name: "settings", where: "NOT (" + deploymentLocalSettings + ")"},
	{name: "tags"},
	{name: "teams"},
	{name: "user_routes"},
	{name: "user_tags"},
	{name: "user_team_memberships"},
}

var controlOperationalTables = []tableSpec{
	{name: "runtime_state"},
	{name: "settings", where: deploymentLocalSettings},
}

var usageDurableTables = []tableSpec{
	{name: "key_identities"},
	{name: "portal_credentials"},
	{name: "usage_events"},
	{
		name:  "usage_meta",
		where: "key NOT IN ('collector_heartbeat_at', 'collector_last_error')",
	},
	{name: "user_quota_adjustments"},
	{name: "user_quota_policies"},
	{name: "user_weekly_usage"},
}

var usageOperationalTables = []tableSpec{{name: "portal_sessions"}}

type tableSpec struct {
	name  string
	where string
}

type stateQueryer interface {
	GetContext(context.Context, any, string, ...any) error
	SelectContext(context.Context, any, string, ...any) error
	QueryxContext(context.Context, string, ...any) (*sqlx.Rows, error)
}

// StateComparison is safe to archive as migration evidence: it contains only
// aggregate counts and one-way digests, never SQLite values, API Keys, OAuth
// material, encrypted secret payloads, or snapshot internal Keys.
type StateComparison struct {
	Version                int          `json:"version"`
	Passed                 bool         `json:"passed"`
	V1                     StateSummary `json:"v1"`
	V2                     StateSummary `json:"v2"`
	Differences            []string     `json:"differences"`
	OperationalDifferences []string     `json:"operational_differences"`
}

type StateSummary struct {
	Version          int             `json:"version"`
	Control          DatabaseSummary `json:"control"`
	Usage            DatabaseSummary `json:"usage"`
	AuthSnapshot     SnapshotSummary `json:"auth_snapshot"`
	QuotaSnapshot    SnapshotSummary `json:"quota_snapshot"`
	QuotaHeartbeat   SnapshotSummary `json:"quota_heartbeat"`
	CheckpointSHA256 string          `json:"checkpoint_sha256"`
}

type DatabaseSummary struct {
	UserVersion       int            `json:"user_version"`
	SchemaSHA256      string         `json:"schema_sha256"`
	Durable           []TableSummary `json:"durable_tables"`
	Operational       []TableSummary `json:"operational_tables"`
	DurableSHA256     string         `json:"durable_sha256"`
	OperationalSHA256 string         `json:"operational_sha256"`
}

type TableSummary struct {
	Name          string `json:"name"`
	Rows          int64  `json:"rows"`
	ContentSHA256 string `json:"content_sha256"`
}

type SnapshotSummary struct {
	Found         bool   `json:"found"`
	Records       int    `json:"records,omitempty"`
	ContentSHA256 string `json:"content_sha256,omitempty"`
	OperationalOK *bool  `json:"operational_ok,omitempty"`
}

func CompareStateRoots(ctx context.Context, v1Root string, v2Root string) (StateComparison, error) {
	var comparison StateComparison
	comparison.Version = StateSummaryVersion
	v1Path, v2Path, err := distinctRoots(v1Root, v2Root)
	if err != nil {
		return comparison, err
	}
	comparison.V1, err = SummarizeState(ctx, v1Path)
	if err != nil {
		return comparison, fmt.Errorf("summarize v1 state copy: %w", err)
	}
	comparison.V2, err = SummarizeState(ctx, v2Path)
	if err != nil {
		return comparison, fmt.Errorf("summarize v2 state copy: %w", err)
	}

	appendDifference := func(equal bool, message string) {
		if !equal {
			comparison.Differences = append(comparison.Differences, message)
		}
	}
	appendDifference(
		comparison.V1.Control.UserVersion == comparison.V2.Control.UserVersion,
		"control database user_version differs",
	)
	appendDifference(
		comparison.V1.Control.SchemaSHA256 == comparison.V2.Control.SchemaSHA256,
		"control database schema differs",
	)
	appendDifference(
		comparison.V1.Control.DurableSHA256 == comparison.V2.Control.DurableSHA256,
		"control durable state differs",
	)
	appendDifference(
		comparison.V1.Usage.UserVersion == comparison.V2.Usage.UserVersion,
		"usage database user_version differs",
	)
	appendDifference(
		comparison.V1.Usage.SchemaSHA256 == comparison.V2.Usage.SchemaSHA256,
		"usage database schema differs",
	)
	appendDifference(
		comparison.V1.Usage.DurableSHA256 == comparison.V2.Usage.DurableSHA256,
		"usage durable state differs",
	)
	appendDifference(
		equalSnapshot(comparison.V1.AuthSnapshot, comparison.V2.AuthSnapshot),
		"auth snapshot semantics differ",
	)
	appendDifference(
		equalSnapshot(comparison.V1.QuotaSnapshot, comparison.V2.QuotaSnapshot),
		"quota snapshot semantics differ",
	)

	if comparison.V1.Control.OperationalSHA256 != comparison.V2.Control.OperationalSHA256 {
		comparison.OperationalDifferences = append(
			comparison.OperationalDifferences,
			"control runtime_state differs (expected while owners or worker checkpoints differ)",
		)
	}
	if comparison.V1.Usage.OperationalSHA256 != comparison.V2.Usage.OperationalSHA256 {
		comparison.OperationalDifferences = append(
			comparison.OperationalDifferences,
			"usage sessions differ (review before a session-preserving cutover)",
		)
	}
	if !equalSnapshot(comparison.V1.QuotaHeartbeat, comparison.V2.QuotaHeartbeat) {
		comparison.OperationalDifferences = append(
			comparison.OperationalDifferences,
			"quota heartbeat semantics differ",
		)
	}
	comparison.Passed = len(comparison.Differences) == 0
	return comparison, nil
}

func SummarizeState(ctx context.Context, root string) (StateSummary, error) {
	var summary StateSummary
	summary.Version = StateSummaryVersion
	root, err := existingRoot(root)
	if err != nil {
		return summary, err
	}
	summary.Control, err = summarizeDatabase(
		ctx,
		filepath.Join(root, "state", "control-plane.sqlite3"),
		controlDurableTables,
		controlOperationalTables,
	)
	if err != nil {
		return summary, fmt.Errorf("summarize control database: %w", err)
	}
	summary.Usage, err = summarizeDatabase(
		ctx,
		filepath.Join(root, "state", "usage.sqlite3"),
		usageDurableTables,
		usageOperationalTables,
	)
	if err != nil {
		return summary, fmt.Errorf("summarize usage database: %w", err)
	}
	snapshotDirectory := filepath.Join(root, "state", "gateway")
	summary.AuthSnapshot, err = summarizeAuthSnapshot(filepath.Join(snapshotDirectory, "auth-snapshot.json"))
	if err != nil {
		return summary, err
	}
	summary.QuotaSnapshot, err = summarizeQuotaSnapshot(filepath.Join(snapshotDirectory, "quota-snapshot.json"))
	if err != nil {
		return summary, err
	}
	summary.QuotaHeartbeat, err = summarizeQuotaHeartbeat(filepath.Join(snapshotDirectory, "quota-heartbeat.json"))
	if err != nil {
		return summary, err
	}
	summary.CheckpointSHA256 = checkpointDigest(summary)
	return summary, nil
}

func summarizeDatabase(
	ctx context.Context,
	path string,
	durable []tableSpec,
	operational []tableSpec,
) (DatabaseSummary, error) {
	var summary DatabaseSummary
	database, err := openReadOnlyDatabase(ctx, path)
	if err != nil {
		return summary, err
	}
	defer database.Close()
	transaction, err := database.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return summary, fmt.Errorf("begin SQLite state snapshot: %w", err)
	}
	defer transaction.Rollback()
	if err := transaction.GetContext(ctx, &summary.UserVersion, "PRAGMA user_version"); err != nil {
		return summary, fmt.Errorf("read user_version: %w", err)
	}
	summary.SchemaSHA256, err = schemaDigest(ctx, transaction)
	if err != nil {
		return summary, err
	}
	summary.Durable, summary.DurableSHA256, err = summarizeTables(ctx, transaction, durable)
	if err != nil {
		return summary, err
	}
	summary.Operational, summary.OperationalSHA256, err = summarizeTables(ctx, transaction, operational)
	if err != nil {
		return summary, err
	}
	if err := transaction.Commit(); err != nil {
		return summary, fmt.Errorf("close SQLite state snapshot: %w", err)
	}
	return summary, nil
}

func openReadOnlyDatabase(ctx context.Context, path string) (*sqlx.DB, error) {
	if err := requireRegularFile(path); err != nil {
		return nil, err
	}
	dsn := &url.URL{Scheme: "file", Path: path}
	query := url.Values{}
	query.Set("mode", "ro")
	query.Add("_pragma", "query_only(1)")
	query.Add("_pragma", "busy_timeout(5000)")
	dsn.RawQuery = query.Encode()
	database, err := sqlx.Open("sqlite", dsn.String())
	if err != nil {
		return nil, fmt.Errorf("open read-only SQLite database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.PingContext(ctx); err != nil {
		database.Close()
		return nil, fmt.Errorf("connect to read-only SQLite database: %w", err)
	}
	return database, nil
}

func schemaDigest(ctx context.Context, database stateQueryer) (string, error) {
	rows := make([]struct {
		Type string `db:"type"`
		Name string `db:"name"`
		SQL  string `db:"sql"`
	}, 0)
	if err := database.SelectContext(ctx, &rows, `
        SELECT type, name, COALESCE(sql, '') AS sql
          FROM sqlite_master
         WHERE name NOT LIKE 'sqlite_%'
         ORDER BY type, name`); err != nil {
		return "", fmt.Errorf("read SQLite schema: %w", err)
	}
	hasher := sha256.New()
	for _, row := range rows {
		writeString(hasher, row.Type)
		writeString(hasher, row.Name)
		writeString(hasher, row.SQL)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func summarizeTables(
	ctx context.Context,
	database stateQueryer,
	specifications []tableSpec,
) ([]TableSummary, string, error) {
	summaries := make([]TableSummary, 0, len(specifications))
	for _, specification := range specifications {
		summary, err := summarizeTable(ctx, database, specification)
		if err != nil {
			return nil, "", err
		}
		summaries = append(summaries, summary)
	}
	hasher := sha256.New()
	for _, summary := range summaries {
		writeString(hasher, summary.Name)
		writeString(hasher, strconv.FormatInt(summary.Rows, 10))
		writeString(hasher, summary.ContentSHA256)
	}
	return summaries, hex.EncodeToString(hasher.Sum(nil)), nil
}

func summarizeTable(
	ctx context.Context,
	database stateQueryer,
	specification tableSpec,
) (TableSummary, error) {
	summary := TableSummary{Name: specification.name}
	columns, orderColumns, err := tableColumns(ctx, database, specification.name)
	if err != nil {
		return summary, err
	}
	if len(columns) == 0 {
		return summary, fmt.Errorf("required table %s has no columns", specification.name)
	}
	quotedColumns := make([]string, 0, len(columns))
	for _, column := range columns {
		quotedColumns = append(quotedColumns, quoteIdentifier(column))
	}
	query := "SELECT " + strings.Join(quotedColumns, ", ") +
		" FROM " + quoteIdentifier(specification.name)
	if specification.where != "" {
		query += " WHERE " + specification.where
	}
	quotedOrderColumns := make([]string, 0, len(orderColumns))
	for _, column := range orderColumns {
		quotedOrderColumns = append(quotedOrderColumns, quoteIdentifier(column))
	}
	query += " ORDER BY " + strings.Join(quotedOrderColumns, ", ")
	rows, err := database.QueryxContext(ctx, query)
	if err != nil {
		return summary, fmt.Errorf("read table %s: %w", specification.name, err)
	}
	defer rows.Close()
	hasher := sha256.New()
	for _, column := range columns {
		writeString(hasher, column)
	}
	values := make([]any, len(columns))
	pointers := make([]any, len(columns))
	for index := range pointers {
		pointers[index] = &values[index]
	}
	for rows.Next() {
		for index := range values {
			values[index] = nil
		}
		if err := rows.Scan(pointers...); err != nil {
			return summary, fmt.Errorf("scan table %s: %w", specification.name, err)
		}
		for _, value := range values {
			writeSQLiteValue(hasher, value)
		}
		summary.Rows++
	}
	if err := rows.Err(); err != nil {
		return summary, fmt.Errorf("iterate table %s: %w", specification.name, err)
	}
	summary.ContentSHA256 = hex.EncodeToString(hasher.Sum(nil))
	return summary, nil
}

func tableColumns(
	ctx context.Context,
	database stateQueryer,
	table string,
) ([]string, []string, error) {
	rows, err := database.QueryxContext(ctx, "PRAGMA table_info("+quoteIdentifier(table)+")")
	if err != nil {
		return nil, nil, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()
	type columnRow struct {
		cid        int
		name       string
		primaryKey int
	}
	columns := make([]columnRow, 0)
	for rows.Next() {
		var column columnRow
		var columnType string
		var notNull int
		var defaultValue any
		if err := rows.Scan(
			&column.cid,
			&column.name,
			&columnType,
			&notNull,
			&defaultValue,
			&column.primaryKey,
		); err != nil {
			return nil, nil, fmt.Errorf("scan table %s columns: %w", table, err)
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("iterate table %s columns: %w", table, err)
	}
	if len(columns) == 0 {
		return nil, nil, fmt.Errorf("required table %s is missing", table)
	}
	sort.Slice(columns, func(left, right int) bool { return columns[left].cid < columns[right].cid })
	result := make([]string, 0, len(columns))
	primaryColumns := make([]columnRow, 0)
	for _, column := range columns {
		result = append(result, column.name)
		if column.primaryKey > 0 {
			primaryColumns = append(primaryColumns, column)
		}
	}
	if len(primaryColumns) == 0 {
		return result, append([]string(nil), result...), nil
	}
	sort.Slice(primaryColumns, func(left, right int) bool {
		return primaryColumns[left].primaryKey < primaryColumns[right].primaryKey
	})
	order := make([]string, 0, len(primaryColumns))
	for _, column := range primaryColumns {
		order = append(order, column.name)
	}
	return result, order, nil
}

func summarizeAuthSnapshot(path string) (SnapshotSummary, error) {
	raw, found, err := readOptionalRegularFile(path, gateway.MaxSnapshotBytes)
	if err != nil || !found {
		return SnapshotSummary{Found: found}, wrapSnapshotError("auth", err)
	}
	snapshot, err := gateway.ParseAuthSnapshot(bytes.NewReader(raw))
	if err != nil {
		return SnapshotSummary{}, fmt.Errorf("parse auth snapshot: %w", err)
	}
	sort.Slice(snapshot.Records, func(left, right int) bool {
		return snapshot.Records[left].ExternalKeySHA256 < snapshot.Records[right].ExternalKeySHA256
	})
	semantic := struct {
		Version int                  `json:"version"`
		Records []gateway.AuthRecord `json:"records"`
	}{Version: snapshot.Version, Records: snapshot.Records}
	digest, err := jsonDigest(semantic)
	return SnapshotSummary{Found: true, Records: len(snapshot.Records), ContentSHA256: digest}, err
}

func summarizeQuotaSnapshot(path string) (SnapshotSummary, error) {
	raw, found, err := readOptionalRegularFile(path, gateway.MaxSnapshotBytes)
	if err != nil || !found {
		return SnapshotSummary{Found: found}, wrapSnapshotError("quota", err)
	}
	snapshot, err := gateway.ParseQuotaSnapshot(bytes.NewReader(raw))
	if err != nil {
		return SnapshotSummary{}, fmt.Errorf("parse quota snapshot: %w", err)
	}
	sort.Slice(snapshot.Records, func(left, right int) bool {
		return snapshot.Records[left].UserEmail < snapshot.Records[right].UserEmail
	})
	semantic := struct {
		Version int                   `json:"version"`
		Records []gateway.QuotaRecord `json:"records"`
	}{Version: snapshot.Version, Records: snapshot.Records}
	digest, err := jsonDigest(semantic)
	return SnapshotSummary{Found: true, Records: len(snapshot.Records), ContentSHA256: digest}, err
}

func summarizeQuotaHeartbeat(path string) (SnapshotSummary, error) {
	raw, found, err := readOptionalRegularFile(path, gateway.MaxSnapshotBytes)
	if err != nil || !found {
		return SnapshotSummary{Found: found}, wrapSnapshotError("quota heartbeat", err)
	}
	heartbeat, err := gateway.ParseQuotaHeartbeat(bytes.NewReader(raw))
	if err != nil {
		return SnapshotSummary{}, fmt.Errorf("parse quota heartbeat: %w", err)
	}
	semantic := struct {
		Version              int    `json:"version"`
		OK                   bool   `json:"ok"`
		Error                string `json:"error"`
		StaleAfterSeconds    int64  `json:"stale_after_seconds"`
		FailOpenAfterSeconds int64  `json:"fail_open_after_seconds"`
	}{
		Version: heartbeat.Version, OK: heartbeat.OK, Error: heartbeat.Error,
		StaleAfterSeconds:    heartbeat.StaleAfterSeconds,
		FailOpenAfterSeconds: heartbeat.FailOpenAfterSeconds,
	}
	digest, err := jsonDigest(semantic)
	return SnapshotSummary{
		Found: true, ContentSHA256: digest, OperationalOK: &heartbeat.OK,
	}, err
}

func readOptionalRegularFile(path string, maximum int64) ([]byte, bool, error) {
	information, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	if !information.Mode().IsRegular() {
		return nil, false, errors.New("snapshot must be a regular non-symlink file")
	}
	raw, _, err := readRegularFile(path, maximum)
	if err != nil {
		return nil, false, err
	}
	return raw, true, nil
}

func checkpointDigest(summary StateSummary) string {
	hasher := sha256.New()
	for _, value := range []string{
		strconv.Itoa(summary.Control.UserVersion),
		summary.Control.SchemaSHA256,
		summary.Control.DurableSHA256,
		strconv.Itoa(summary.Usage.UserVersion),
		summary.Usage.SchemaSHA256,
		summary.Usage.DurableSHA256,
		snapshotDigest(summary.AuthSnapshot),
		snapshotDigest(summary.QuotaSnapshot),
	} {
		writeString(hasher, value)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func snapshotDigest(summary SnapshotSummary) string {
	if !summary.Found {
		return "missing"
	}
	return strconv.Itoa(summary.Records) + ":" + summary.ContentSHA256
}

func equalSnapshot(left SnapshotSummary, right SnapshotSummary) bool {
	return left.Found == right.Found && left.Records == right.Records &&
		left.ContentSHA256 == right.ContentSHA256
}

func jsonDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode snapshot semantics: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func writeSQLiteValue(hasher hash.Hash, value any) {
	switch typed := value.(type) {
	case nil:
		writeString(hasher, "null")
	case int64:
		writeString(hasher, "integer:"+strconv.FormatInt(typed, 10))
	case float64:
		writeString(hasher, "real:"+strconv.FormatFloat(typed, 'g', -1, 64))
	case bool:
		writeString(hasher, "boolean:"+strconv.FormatBool(typed))
	case []byte:
		writeBytes(hasher, append([]byte("blob:"), typed...))
	case string:
		writeString(hasher, "text:"+typed)
	case sql.RawBytes:
		writeBytes(hasher, append([]byte("blob:"), typed...))
	default:
		writeString(hasher, fmt.Sprintf("%T:%v", typed, typed))
	}
}

func writeString(hasher hash.Hash, value string) {
	writeBytes(hasher, []byte(value))
}

func writeBytes(hasher hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = hasher.Write(length[:])
	_, _ = hasher.Write(value)
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func existingRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("state-copy root is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve state-copy root: %w", err)
	}
	information, err := os.Lstat(absolute)
	if err != nil {
		return "", fmt.Errorf("open state-copy root: %w", err)
	}
	if information.Mode()&os.ModeSymlink != 0 || !information.IsDir() {
		return "", errors.New("state-copy root must be a real directory, not a symlink")
	}
	return filepath.Clean(absolute), nil
}

func distinctRoots(left string, right string) (string, string, error) {
	leftRoot, err := existingRoot(left)
	if err != nil {
		return "", "", err
	}
	rightRoot, err := existingRoot(right)
	if err != nil {
		return "", "", err
	}
	if leftRoot == rightRoot {
		return "", "", errors.New("v1 and v2 state-copy roots must be different directories")
	}
	return leftRoot, rightRoot, nil
}

func requireRegularFile(path string) error {
	information, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("open existing state database: %w", err)
	}
	if !information.Mode().IsRegular() {
		return errors.New("state database must be a regular non-symlink file")
	}
	return nil
}

func wrapSnapshotError(name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("read %s snapshot: %w", name, err)
}
