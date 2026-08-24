package controlplane

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

func (store *Store) ReadAccounts(ctx context.Context) ([]Account, error) {
	rows := make([]struct {
		ID           string `db:"id"`
		Email        string `db:"email"`
		Port         int    `db:"port"`
		ProxyMode    string `db:"proxy_mode"`
		CreatedAt    int64  `db:"created_at"`
		GroupEnabled int    `db:"group_enabled"`
		DefaultGroup int    `db:"default_group"`
	}, 0)
	if err := store.db.SelectContext(ctx, &rows, `
        SELECT id, email, port, proxy_mode, created_at, group_enabled, default_group
          FROM accounts
         ORDER BY position, id`); err != nil {
		return nil, fmt.Errorf("read control-plane accounts: %w", err)
	}
	result := make([]Account, 0, len(rows))
	for _, row := range rows {
		proxyMode := row.ProxyMode
		if proxyMode == "" {
			proxyMode = "inherit"
		}
		result = append(result, Account{
			ID:           row.ID,
			Email:        row.Email,
			Port:         row.Port,
			ProxyMode:    proxyMode,
			CreatedAt:    row.CreatedAt,
			GroupEnabled: row.GroupEnabled != 0,
			DefaultGroup: row.DefaultGroup != 0,
		})
	}
	return result, nil
}

func (store *Store) WriteAccounts(ctx context.Context, accounts []Account) error {
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM accounts"); err != nil {
			return fmt.Errorf("clear control-plane accounts: %w", err)
		}
		for position, account := range accounts {
			proxyMode := strings.TrimSpace(account.ProxyMode)
			if proxyMode == "" {
				proxyMode = "inherit"
			}
			if _, err := transaction.ExecContext(ctx, `
                INSERT INTO accounts(
                    id, email, port, proxy_mode, created_at,
                    group_enabled, default_group, position
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
				account.ID,
				account.Email,
				account.Port,
				proxyMode,
				account.CreatedAt,
				boolInteger(account.GroupEnabled),
				boolInteger(account.DefaultGroup),
				position,
			); err != nil {
				return fmt.Errorf("write control-plane account %s: %w", account.ID, err)
			}
		}
		return nil
	})
}

func (store *Store) ReadRoutes(ctx context.Context) (map[string]string, error) {
	rows := make([]struct {
		UserEmail string `db:"user_email"`
		AccountID string `db:"account_id"`
	}, 0)
	if err := store.db.SelectContext(
		ctx,
		&rows,
		"SELECT user_email, account_id FROM user_routes ORDER BY user_email",
	); err != nil {
		return nil, fmt.Errorf("read control-plane user routes: %w", err)
	}
	result := make(map[string]string, len(rows))
	for _, row := range rows {
		result[row.UserEmail] = row.AccountID
	}
	return result, nil
}

func (store *Store) WriteRoutes(ctx context.Context, routes map[string]string) error {
	users := make([]string, 0, len(routes))
	for user := range routes {
		users = append(users, user)
	}
	sort.Strings(users)
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM user_routes"); err != nil {
			return fmt.Errorf("clear control-plane user routes: %w", err)
		}
		for _, user := range users {
			if _, err := transaction.ExecContext(
				ctx,
				"INSERT INTO user_routes(user_email, account_id) VALUES (?, ?)",
				user,
				routes[user],
			); err != nil {
				return fmt.Errorf("write control-plane route for %s: %w", user, err)
			}
		}
		return nil
	})
}

func (store *Store) ReadKeyRecords(ctx context.Context) ([]KeyRecord, error) {
	return store.readKeyRecords(ctx, "", nil)
}

func (store *Store) ReadKeyRecordsForUsers(ctx context.Context, userEmails []string) ([]KeyRecord, error) {
	users := normalizedUsers(userEmails)
	if len(users) == 0 {
		return []KeyRecord{}, nil
	}
	result := make([]KeyRecord, 0)
	for offset := 0; offset < len(users); offset += 500 {
		end := min(offset+500, len(users))
		query, arguments, err := sqlx.In(`
            SELECT label, account_id, account_email, user_email, status,
                   secret, created_at, updated_at, sequence
              FROM key_records
             WHERE user_email IN (?)
             ORDER BY sequence`, users[offset:end])
		if err != nil {
			return nil, fmt.Errorf("build control-plane key lookup: %w", err)
		}
		batch, err := store.readKeyRecords(ctx, store.db.Rebind(query), arguments)
		if err != nil {
			return nil, err
		}
		result = append(result, batch...)
	}
	return result, nil
}

func (store *Store) readKeyRecords(ctx context.Context, query string, arguments []any) ([]KeyRecord, error) {
	if query == "" {
		query = `
            SELECT label, account_id, account_email, user_email, status,
                   secret, created_at, updated_at, sequence
              FROM key_records
             ORDER BY sequence`
	}
	rows := make([]struct {
		Label        string `db:"label"`
		Account      string `db:"account_id"`
		AccountEmail string `db:"account_email"`
		User         string `db:"user_email"`
		Status       string `db:"status"`
		Key          string `db:"secret"`
		CreatedAt    int64  `db:"created_at"`
		UpdatedAt    int64  `db:"updated_at"`
		Sequence     int    `db:"sequence"`
	}, 0)
	if err := store.db.SelectContext(ctx, &rows, query, arguments...); err != nil {
		return nil, fmt.Errorf("read control-plane key records: %w", err)
	}
	result := make([]KeyRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, KeyRecord{
			Label:        row.Label,
			Account:      row.Account,
			AccountEmail: row.AccountEmail,
			User:         row.User,
			Status:       row.Status,
			Key:          row.Key,
			CreatedAt:    row.CreatedAt,
			UpdatedAt:    row.UpdatedAt,
		})
	}
	return result, nil
}

func (store *Store) WriteKeyRecords(ctx context.Context, records []KeyRecord) error {
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM key_records"); err != nil {
			return fmt.Errorf("clear control-plane key records: %w", err)
		}
		for sequence, record := range records {
			if _, err := transaction.ExecContext(ctx, `
                INSERT INTO key_records(
                    sequence, label, account_id, account_email, user_email,
                    status, secret, created_at, updated_at
                ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				sequence,
				record.Label,
				record.Account,
				record.AccountEmail,
				record.User,
				record.Status,
				record.Key,
				record.CreatedAt,
				record.UpdatedAt,
			); err != nil {
				return fmt.Errorf("write control-plane key record %d: %w", sequence, err)
			}
		}
		return nil
	})
}

type UserKeyRotation struct {
	User        string
	OldKey      string
	NewKey      string
	RotatedRows []RotatedKeyRow
	CreatedRows []int
}

type RotatedKeyRow struct {
	Sequence  int
	UpdatedAt int64
}

var (
	ErrUserKeyRotationConflict = errors.New("user key rotation conflict")
	ErrUserKeyRotationUnsafe   = errors.New("user key rotation is unsafe")
)

// ApplyUserKeyRotationExpected marks every active row for one unified user key
// as rotated and appends a replacement row for every account in one SQLite
// transaction. The expected key prevents a stale browser request from rotating
// a newer credential.
func (store *Store) ApplyUserKeyRotationExpected(
	ctx context.Context,
	user string,
	expectedKey string,
	newKey string,
) (UserKeyRotation, error) {
	rotation := UserKeyRotation{
		User: strings.ToLower(strings.TrimSpace(user)), OldKey: strings.TrimSpace(expectedKey),
		NewKey: strings.TrimSpace(newKey), RotatedRows: make([]RotatedKeyRow, 0), CreatedRows: make([]int, 0),
	}
	if rotation.User == "" || rotation.OldKey == "" || rotation.NewKey == "" || rotation.OldKey == rotation.NewKey {
		return rotation, fmt.Errorf("%w: user, expected key, and distinct new key are required", ErrInvalidCatalogInput)
	}
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		var duplicate int
		if err := transaction.GetContext(ctx, &duplicate, "SELECT COUNT(*) FROM key_records WHERE secret = ?", rotation.NewKey); err != nil {
			return fmt.Errorf("check new user key uniqueness: %w", err)
		}
		if duplicate != 0 {
			return ErrUserKeyRotationConflict
		}
		rows := make([]struct {
			Sequence     int    `db:"sequence"`
			Label        string `db:"label"`
			Account      string `db:"account_id"`
			AccountEmail string `db:"account_email"`
			UpdatedAt    int64  `db:"updated_at"`
			Secret       string `db:"secret"`
		}, 0)
		if err := transaction.SelectContext(ctx, &rows, `
			SELECT sequence, label, account_id, account_email, updated_at, secret
			  FROM key_records
			 WHERE lower(trim(user_email)) = ? AND status = 'active'
			 ORDER BY sequence`, rotation.User); err != nil {
			return fmt.Errorf("read active rows for user key rotation: %w", err)
		}
		if len(rows) == 0 {
			return ErrUserKeyRotationUnsafe
		}
		accounts := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			if row.Secret != rotation.OldKey {
				return ErrUserKeyRotationConflict
			}
			if _, duplicateAccount := accounts[row.Account]; duplicateAccount {
				return ErrUserKeyRotationUnsafe
			}
			accounts[row.Account] = struct{}{}
			rotation.RotatedRows = append(rotation.RotatedRows, RotatedKeyRow{
				Sequence: row.Sequence, UpdatedAt: row.UpdatedAt,
			})
		}
		currentAccounts := make([]string, 0)
		if err := transaction.SelectContext(ctx, &currentAccounts, "SELECT id FROM accounts ORDER BY position, id"); err != nil {
			return fmt.Errorf("read account matrix for user key rotation: %w", err)
		}
		if len(currentAccounts) == 0 || len(accounts) != len(currentAccounts) {
			return ErrUserKeyRotationUnsafe
		}
		for _, account := range currentAccounts {
			if _, found := accounts[account]; !found {
				return ErrUserKeyRotationUnsafe
			}
		}
		var nextSequence int
		if err := transaction.GetContext(ctx, &nextSequence, "SELECT COALESCE(MAX(sequence), -1) + 1 FROM key_records"); err != nil {
			return fmt.Errorf("allocate user key rotation sequence: %w", err)
		}
		now := store.now().Unix()
		for _, row := range rows {
			if _, err := transaction.ExecContext(ctx, `
				UPDATE key_records SET status = 'rotated', updated_at = ? WHERE sequence = ? AND status = 'active'`,
				now, row.Sequence,
			); err != nil {
				return fmt.Errorf("mark user key row %d rotated: %w", row.Sequence, err)
			}
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO key_records(
					sequence, label, account_id, account_email, user_email,
					status, secret, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?)`,
				nextSequence, row.Label, row.Account, row.AccountEmail, rotation.User,
				rotation.NewKey, now, now,
			); err != nil {
				return fmt.Errorf("append replacement user key row %d: %w", nextSequence, err)
			}
			rotation.CreatedRows = append(rotation.CreatedRows, nextSequence)
			nextSequence++
		}
		return nil
	})
	return rotation, err
}

func (store *Store) RestoreUserKeyRotation(ctx context.Context, rotation UserKeyRotation) error {
	if rotation.User == "" || rotation.OldKey == "" || rotation.NewKey == "" ||
		len(rotation.RotatedRows) == 0 || len(rotation.RotatedRows) != len(rotation.CreatedRows) {
		return fmt.Errorf("%w: incomplete user key rotation rollback", ErrInvalidCatalogInput)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		for _, sequence := range rotation.CreatedRows {
			var row struct {
				User   string `db:"user_email"`
				Status string `db:"status"`
				Secret string `db:"secret"`
			}
			if err := transaction.GetContext(ctx, &row, `
				SELECT user_email, status, secret FROM key_records WHERE sequence = ?`, sequence); err != nil {
				return fmt.Errorf("read replacement user key row %d for rollback: %w", sequence, err)
			}
			if !strings.EqualFold(row.User, rotation.User) || row.Status != "active" || row.Secret != rotation.NewKey {
				return ErrUserKeyRotationConflict
			}
		}
		for _, row := range rotation.RotatedRows {
			var current struct {
				User   string `db:"user_email"`
				Status string `db:"status"`
				Secret string `db:"secret"`
			}
			if err := transaction.GetContext(ctx, &current, `
				SELECT user_email, status, secret FROM key_records WHERE sequence = ?`, row.Sequence); err != nil {
				return fmt.Errorf("read rotated user key row %d for rollback: %w", row.Sequence, err)
			}
			if !strings.EqualFold(current.User, rotation.User) || current.Status != "rotated" || current.Secret != rotation.OldKey {
				return ErrUserKeyRotationConflict
			}
		}
		for _, sequence := range rotation.CreatedRows {
			if _, err := transaction.ExecContext(ctx, "DELETE FROM key_records WHERE sequence = ?", sequence); err != nil {
				return fmt.Errorf("delete replacement user key row %d: %w", sequence, err)
			}
		}
		for _, row := range rotation.RotatedRows {
			if _, err := transaction.ExecContext(ctx, `
				UPDATE key_records SET status = 'active', updated_at = ? WHERE sequence = ?`,
				row.UpdatedAt, row.Sequence,
			); err != nil {
				return fmt.Errorf("restore original user key row %d: %w", row.Sequence, err)
			}
		}
		return nil
	})
}

func (store *Store) ReadInternalKeys(ctx context.Context) (map[string]InternalKey, error) {
	rows := make([]struct {
		UserEmail string `db:"user_email"`
		Key       string `db:"secret"`
		CreatedAt int64  `db:"created_at"`
		Status    string `db:"status"`
	}, 0)
	if err := store.db.SelectContext(
		ctx,
		&rows,
		"SELECT user_email, secret, created_at, status FROM internal_keys ORDER BY user_email",
	); err != nil {
		return nil, fmt.Errorf("read control-plane internal keys: %w", err)
	}
	result := make(map[string]InternalKey, len(rows))
	for _, row := range rows {
		result[row.UserEmail] = InternalKey{Key: row.Key, CreatedAt: row.CreatedAt, Status: row.Status}
	}
	return result, nil
}

func (store *Store) WriteInternalKeys(ctx context.Context, users map[string]InternalKey) error {
	emails := make([]string, 0, len(users))
	for email := range users {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM internal_keys"); err != nil {
			return fmt.Errorf("clear control-plane internal keys: %w", err)
		}
		for _, email := range emails {
			record := users[email]
			status := record.Status
			if status == "" {
				status = "active"
			}
			if _, err := transaction.ExecContext(ctx, `
                INSERT INTO internal_keys(user_email, secret, created_at, status)
                VALUES (?, ?, ?, ?)`, email, record.Key, record.CreatedAt, status); err != nil {
				return fmt.Errorf("write control-plane internal key for %s: %w", email, err)
			}
		}
		return nil
	})
}

// EnsureInternalKeys keeps the stable per-user credentials used between the
// Gateway and every CPA. It deliberately updates the catalog in one
// cross-process transaction so an auth snapshot can never observe a partially
// synchronized set of internal credentials.
func (store *Store) EnsureInternalKeys(
	ctx context.Context,
	activeUsers []string,
) (map[string]InternalKey, error) {
	users := normalizedUsers(activeUsers)
	active := make(map[string]struct{}, len(users))
	for _, user := range users {
		active[user] = struct{}{}
	}
	result := make(map[string]InternalKey, len(users))
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		rows := make([]struct {
			UserEmail string `db:"user_email"`
			Key       string `db:"secret"`
			CreatedAt int64  `db:"created_at"`
			Status    string `db:"status"`
		}, 0)
		if err := transaction.SelectContext(
			ctx,
			&rows,
			"SELECT user_email, secret, created_at, status FROM internal_keys ORDER BY user_email",
		); err != nil {
			return fmt.Errorf("read internal keys for synchronization: %w", err)
		}
		existing := make(map[string]InternalKey, len(rows))
		for _, row := range rows {
			existing[row.UserEmail] = InternalKey{
				Key: row.Key, CreatedAt: row.CreatedAt, Status: row.Status,
			}
		}

		for _, user := range users {
			record, found := existing[user]
			if !found {
				key, err := newInternalKey()
				if err != nil {
					return err
				}
				record = InternalKey{Key: key, CreatedAt: store.now().Unix(), Status: "active"}
				if _, err := transaction.ExecContext(ctx, `
                    INSERT INTO internal_keys(user_email, secret, created_at, status)
                    VALUES (?, ?, ?, 'active')`, user, record.Key, record.CreatedAt); err != nil {
					return fmt.Errorf("create internal key for %s: %w", user, err)
				}
			} else if record.Status != "active" {
				if _, err := transaction.ExecContext(
					ctx,
					"UPDATE internal_keys SET status = 'active' WHERE user_email = ?",
					user,
				); err != nil {
					return fmt.Errorf("activate internal key for %s: %w", user, err)
				}
				record.Status = "active"
			}
			result[user] = record
		}
		for _, row := range rows {
			if _, found := active[row.UserEmail]; found || row.Status == "inactive" {
				continue
			}
			if _, err := transaction.ExecContext(
				ctx,
				"UPDATE internal_keys SET status = 'inactive' WHERE user_email = ?",
				row.UserEmail,
			); err != nil {
				return fmt.Errorf("deactivate internal key for %s: %w", row.UserEmail, err)
			}
		}
		return nil
	})
	return result, err
}

func newInternalKey() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("generate internal key: %w", err)
	}
	return "cpa_internal_" + hex.EncodeToString(raw), nil
}

func normalizedUsers(values []string) []string {
	unique := make(map[string]struct{})
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
