package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

var (
	ErrAccountAlreadyExists        = errors.New("account already exists")
	ErrAccountEmailAlreadyExists   = errors.New("account email already exists")
	ErrAccountPortAlreadyExists    = errors.New("account port already exists")
	ErrAccountLifecycleNotFound    = errors.New("account lifecycle not found")
	ErrAccountLifecycleConflict    = errors.New("account lifecycle conflict")
	ErrAccountDeleteLast           = errors.New("cannot delete the last account")
	ErrAccountDeleteNeedsFallback  = errors.New("account deletion requires an enabled fallback")
	ErrAccountDeleteRequiresRevoke = errors.New(
		"account has exclusive active keys that require revocation",
	)
)

var accountIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)

var reservedAccountIDs = map[string]struct{}{
	"admin": {}, "all": {}, "gateway": {}, "management": {},
}

type AccountCreation struct {
	Account     Account
	CreatedRows []StoredKeyRecord
}

type StoredAccount struct {
	Position int
	Account
}

type AccountRouteChange struct {
	UserEmail string
	Before    string
	After     string
}

type AccountDefaultState struct {
	AccountID string
	Default   bool
}

type AccountUpdateRequest struct {
	AccountID       string
	NewAccountID    string
	Email           string
	ProxyMode       string
	GroupEnabled    *bool
	DefaultGroup    *bool
	FallbackAccount string
}

type AccountUpdate struct {
	Before         StoredAccount
	After          StoredAccount
	RowsBefore     []StoredKeyRecord
	RowsAfter      []StoredKeyRecord
	Routes         []AccountRouteChange
	DefaultsBefore []AccountDefaultState
	DefaultsAfter  []AccountDefaultState
}

type AccountDeletion struct {
	Account              StoredAccount
	Rows                 []StoredKeyRecord
	Routes               []AccountRouteChange
	Defaults             []AccountDefaultState
	FallbackAccount      string
	RevokedExclusiveKeys int
}

// ReadAccountLifecycle returns one account and its sequence-preserving Key
// rows from a single read transaction. Lifecycle orchestration uses this to
// create a recoverable filesystem backup before the authoritative mutation.
func (store *Store) ReadAccountLifecycle(
	ctx context.Context,
	accountID string,
) (StoredAccount, []StoredKeyRecord, error) {
	accountID, err := NormalizeAccountID(accountID)
	if err != nil {
		return StoredAccount{}, nil, err
	}
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return StoredAccount{}, nil, fmt.Errorf("begin account lifecycle read: %w", err)
	}
	defer transaction.Rollback()
	account, found, err := readStoredAccount(ctx, transaction, accountID)
	if err != nil {
		return StoredAccount{}, nil, err
	}
	if !found {
		return StoredAccount{}, nil, ErrAccountLifecycleNotFound
	}
	rows, err := readAccountKeyRows(ctx, transaction, accountID)
	if err != nil {
		return StoredAccount{}, nil, err
	}
	if err := transaction.Commit(); err != nil {
		return StoredAccount{}, nil, fmt.Errorf("commit account lifecycle read: %w", err)
	}
	return account, rows, nil
}

func NormalizeAccountID(value string) (string, error) {
	accountID := strings.ToLower(strings.TrimSpace(value))
	if !accountIDPattern.MatchString(accountID) {
		return "", fmt.Errorf("%w: account ID must be 2-32 lowercase letters, digits, or hyphens and start with a letter", ErrInvalidCatalogInput)
	}
	if _, reserved := reservedAccountIDs[accountID]; reserved {
		return "", fmt.Errorf("%w: account ID %s is reserved", ErrInvalidCatalogInput, accountID)
	}
	return accountID, nil
}

func NormalizeAccountEmail(value string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(value))
	if len(email) == 0 || len(email) > 254 || strings.ContainsAny(email, "\r\n\t ") {
		return "", fmt.Errorf("%w: invalid upstream account email", ErrInvalidCatalogInput)
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsed.Address, email) || strings.Count(email, "@") != 1 {
		return "", fmt.Errorf("%w: invalid upstream account email", ErrInvalidCatalogInput)
	}
	localAndDomain := strings.SplitN(email, "@", 2)
	if localAndDomain[0] == "" || !strings.Contains(localAndDomain[1], ".") {
		return "", fmt.Errorf("%w: invalid upstream account email", ErrInvalidCatalogInput)
	}
	return email, nil
}

func NormalizeProxyMode(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		mode = "inherit"
	}
	if mode != "inherit" && mode != "direct" && mode != "custom" {
		return "", fmt.Errorf("%w: invalid account proxy mode", ErrInvalidCatalogInput)
	}
	return mode, nil
}

// ApplyAccountCreation appends an account and one row for every active unified
// user Key in the same transaction. Existing Key bytes are copied unchanged;
// this is the compatibility invariant that lets the new account join the
// current API Key matrix without rotating any client credential.
func (store *Store) ApplyAccountCreation(ctx context.Context, candidate Account) (AccountCreation, error) {
	creation := AccountCreation{CreatedRows: make([]StoredKeyRecord, 0)}
	accountID, err := NormalizeAccountID(candidate.ID)
	if err != nil {
		return creation, err
	}
	email, err := NormalizeAccountEmail(candidate.Email)
	if err != nil {
		return creation, err
	}
	proxyMode, err := NormalizeProxyMode(candidate.ProxyMode)
	if err != nil {
		return creation, err
	}
	if candidate.Port < 1 || candidate.Port > 65535 {
		return creation, fmt.Errorf("%w: account port is invalid", ErrInvalidCatalogInput)
	}
	candidate.ID = accountID
	candidate.Email = email
	candidate.ProxyMode = proxyMode
	if candidate.CreatedAt <= 0 {
		candidate.CreatedAt = store.now().Unix()
	}
	candidate.GroupEnabled = true
	creation.Account = candidate

	err = store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		var count int
		if err := transaction.GetContext(ctx, &count, "SELECT COUNT(*) FROM accounts WHERE id = ?", candidate.ID); err != nil {
			return fmt.Errorf("check account ID uniqueness: %w", err)
		}
		if count != 0 {
			return ErrAccountAlreadyExists
		}
		if err := transaction.GetContext(ctx, &count, "SELECT COUNT(*) FROM accounts WHERE lower(email) = ?", candidate.Email); err != nil {
			return fmt.Errorf("check account email uniqueness: %w", err)
		}
		if count != 0 {
			return ErrAccountEmailAlreadyExists
		}
		if err := transaction.GetContext(ctx, &count, "SELECT COUNT(*) FROM accounts WHERE port = ?", candidate.Port); err != nil {
			return fmt.Errorf("check account port uniqueness: %w", err)
		}
		if count != 0 {
			return ErrAccountPortAlreadyExists
		}
		var accountCount, position int
		if err := transaction.GetContext(ctx, &accountCount, "SELECT COUNT(*) FROM accounts"); err != nil {
			return fmt.Errorf("count accounts for creation: %w", err)
		}
		if err := transaction.GetContext(ctx, &position, "SELECT COALESCE(MAX(position), -1) + 1 FROM accounts"); err != nil {
			return fmt.Errorf("allocate account position: %w", err)
		}
		candidate.DefaultGroup = accountCount == 0
		creation.Account.DefaultGroup = candidate.DefaultGroup
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO accounts(
				id, email, port, proxy_mode, created_at,
				group_enabled, default_group, position
			) VALUES (?, ?, ?, ?, ?, 1, ?, ?)`,
			candidate.ID, candidate.Email, candidate.Port, candidate.ProxyMode,
			candidate.CreatedAt, boolInteger(candidate.DefaultGroup), position,
		); err != nil {
			return fmt.Errorf("create account: %w", err)
		}

		users := make([]struct {
			User     string `db:"user_email"`
			Secret   string `db:"secret"`
			KeyCount int    `db:"key_count"`
		}, 0)
		if err := transaction.SelectContext(ctx, &users, `
			SELECT lower(trim(user_email)) AS user_email,
			       MIN(secret) AS secret,
			       COUNT(DISTINCT secret) AS key_count
			  FROM key_records
			 WHERE status = 'active'
			 GROUP BY lower(trim(user_email))
			 ORDER BY lower(trim(user_email))`); err != nil {
			return fmt.Errorf("read active users for account creation: %w", err)
		}
		var sequence int
		if err := transaction.GetContext(ctx, &sequence, "SELECT COALESCE(MAX(sequence), -1) + 1 FROM key_records"); err != nil {
			return fmt.Errorf("allocate account Key sequence: %w", err)
		}
		now := store.now().Unix()
		for _, user := range users {
			if user.User == "" || user.KeyCount != 1 || user.Secret == "" {
				return fmt.Errorf("%w: active user %s does not have one unified API Key", ErrAccountLifecycleConflict, user.User)
			}
			record := StoredKeyRecord{Sequence: sequence, KeyRecord: KeyRecord{
				Label: user.User + ":" + candidate.ID, Account: candidate.ID,
				AccountEmail: candidate.Email, User: user.User, Status: "active",
				Key: user.Secret, CreatedAt: now, UpdatedAt: now,
			}}
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO key_records(
					sequence, label, account_id, account_email, user_email,
					status, secret, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?)`,
				record.Sequence, record.Label, record.Account, record.AccountEmail,
				record.User, record.Key, record.CreatedAt, record.UpdatedAt,
			); err != nil {
				return fmt.Errorf("create account Key row for %s: %w", user.User, err)
			}
			creation.CreatedRows = append(creation.CreatedRows, record)
			sequence++
		}
		return nil
	})
	return creation, err
}

func (store *Store) RestoreAccountCreation(ctx context.Context, creation AccountCreation) error {
	if creation.Account.ID == "" {
		return fmt.Errorf("%w: incomplete account creation rollback", ErrInvalidCatalogInput)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		current, found, err := readStoredAccount(ctx, transaction, creation.Account.ID)
		if err != nil {
			return err
		}
		if !found || !sameAccount(current.Account, creation.Account) {
			return ErrAccountLifecycleConflict
		}
		var routes int
		if err := transaction.GetContext(ctx, &routes, "SELECT COUNT(*) FROM user_routes WHERE account_id = ?", creation.Account.ID); err != nil {
			return fmt.Errorf("check created account routes: %w", err)
		}
		if routes != 0 {
			return ErrAccountLifecycleConflict
		}
		for _, expected := range creation.CreatedRows {
			currentRow, found, err := readStoredKeyRecord(ctx, transaction, expected.Sequence)
			if err != nil {
				return err
			}
			if !found || !sameStoredKeyRecord(currentRow, expected) {
				return ErrAccountLifecycleConflict
			}
		}
		for _, row := range creation.CreatedRows {
			if _, err := transaction.ExecContext(ctx, "DELETE FROM key_records WHERE sequence = ?", row.Sequence); err != nil {
				return fmt.Errorf("delete created account Key row %d: %w", row.Sequence, err)
			}
		}
		if _, err := transaction.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", creation.Account.ID); err != nil {
			return fmt.Errorf("delete created account: %w", err)
		}
		return nil
	})
}

// ApplyAccountUpdate changes account identity, email, proxy policy and routing
// policy as one expected-state transaction. Renames update every account-scoped
// Key row and explicit route without changing external API Key bytes.
func (store *Store) ApplyAccountUpdate(ctx context.Context, request AccountUpdateRequest) (AccountUpdate, error) {
	update := AccountUpdate{
		RowsBefore: make([]StoredKeyRecord, 0), RowsAfter: make([]StoredKeyRecord, 0),
		Routes: make([]AccountRouteChange, 0), DefaultsBefore: make([]AccountDefaultState, 0),
		DefaultsAfter: make([]AccountDefaultState, 0),
	}
	accountID, err := NormalizeAccountID(request.AccountID)
	if err != nil {
		return update, err
	}
	newAccountID := strings.TrimSpace(request.NewAccountID)
	if newAccountID == "" {
		newAccountID = accountID
	}
	newAccountID, err = NormalizeAccountID(newAccountID)
	if err != nil {
		return update, err
	}
	email, err := NormalizeAccountEmail(request.Email)
	if err != nil {
		return update, err
	}
	proxyMode, err := NormalizeProxyMode(request.ProxyMode)
	if err != nil {
		return update, err
	}
	fallbackID := strings.TrimSpace(request.FallbackAccount)
	if fallbackID != "" {
		fallbackID, err = NormalizeAccountID(fallbackID)
		if err != nil {
			return update, err
		}
	}

	err = store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		before, found, err := readStoredAccount(ctx, transaction, accountID)
		if err != nil {
			return err
		}
		if !found {
			return ErrAccountLifecycleNotFound
		}
		update.Before = before
		if newAccountID != accountID {
			var duplicate int
			if err := transaction.GetContext(ctx, &duplicate, "SELECT COUNT(*) FROM accounts WHERE id = ?", newAccountID); err != nil {
				return fmt.Errorf("check renamed account ID: %w", err)
			}
			if duplicate != 0 {
				return ErrAccountAlreadyExists
			}
		}
		var duplicateEmail int
		if err := transaction.GetContext(ctx, &duplicateEmail, `
			SELECT COUNT(*) FROM accounts WHERE lower(email) = ? AND id <> ?`, email, accountID); err != nil {
			return fmt.Errorf("check updated account email: %w", err)
		}
		if duplicateEmail != 0 {
			return ErrAccountEmailAlreadyExists
		}

		accounts := make([]struct {
			ID      string `db:"id"`
			Enabled int    `db:"group_enabled"`
			Default int    `db:"default_group"`
		}, 0)
		if err := transaction.SelectContext(ctx, &accounts, `
			SELECT id, group_enabled, default_group FROM accounts ORDER BY position, id`); err != nil {
			return fmt.Errorf("read accounts for update: %w", err)
		}
		for _, candidate := range accounts {
			update.DefaultsBefore = append(update.DefaultsBefore, AccountDefaultState{
				AccountID: candidate.ID, Default: candidate.Default != 0,
			})
		}
		desiredEnabled := before.Account.GroupEnabled
		if request.GroupEnabled != nil {
			desiredEnabled = *request.GroupEnabled
		}
		desiredDefault := before.Account.DefaultGroup
		if request.DefaultGroup != nil {
			desiredDefault = *request.DefaultGroup
		}
		enabledOthers := make([]string, 0)
		for _, candidate := range accounts {
			if candidate.ID != accountID && candidate.Enabled != 0 {
				enabledOthers = append(enabledOthers, candidate.ID)
			}
		}
		if !desiredEnabled && len(enabledOthers) == 0 {
			return ErrAccountDeleteNeedsFallback
		}
		if fallbackID != "" && (fallbackID == accountID || !containsString(enabledOthers, fallbackID)) {
			return ErrAccountDeleteNeedsFallback
		}
		if fallbackID == "" && (!desiredEnabled || (before.Account.DefaultGroup && !desiredDefault)) {
			for _, candidate := range accounts {
				if candidate.ID != accountID && candidate.Enabled != 0 && candidate.Default != 0 {
					fallbackID = candidate.ID
					break
				}
			}
			if fallbackID == "" && len(enabledOthers) > 0 {
				fallbackID = enabledOthers[0]
			}
		}
		if !desiredEnabled && desiredDefault {
			return fmt.Errorf("%w: a disabled account cannot be the default", ErrInvalidCatalogInput)
		}

		rowsBefore, err := readAccountKeyRows(ctx, transaction, accountID)
		if err != nil {
			return err
		}
		update.RowsBefore = rowsBefore
		if _, err := transaction.ExecContext(ctx, `
			UPDATE accounts
			   SET id = ?, email = ?, proxy_mode = ?, group_enabled = ?, default_group = ?
			 WHERE id = ?`,
			newAccountID, email, proxyMode, boolInteger(desiredEnabled), boolInteger(desiredDefault), accountID,
		); err != nil {
			return fmt.Errorf("update account row: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE key_records
			   SET account_id = ?, account_email = ?, label = lower(trim(user_email)) || ':' || ?
			 WHERE account_id = ?`, newAccountID, email, newAccountID, accountID); err != nil {
			return fmt.Errorf("update account Key rows: %w", err)
		}

		if newAccountID != accountID {
			routes := make([]struct {
				User string `db:"user_email"`
			}, 0)
			if err := transaction.SelectContext(ctx, &routes, `
				SELECT user_email FROM user_routes WHERE account_id = ? ORDER BY user_email`, accountID); err != nil {
				return fmt.Errorf("read renamed account routes: %w", err)
			}
			for _, route := range routes {
				update.Routes = append(update.Routes, AccountRouteChange{
					UserEmail: route.User, Before: accountID, After: newAccountID,
				})
			}
			if _, err := transaction.ExecContext(ctx, `
				UPDATE user_routes SET account_id = ? WHERE account_id = ?`, newAccountID, accountID); err != nil {
				return fmt.Errorf("rename account routes: %w", err)
			}
		}
		if !desiredEnabled {
			routes := make([]struct {
				User string `db:"user_email"`
			}, 0)
			if err := transaction.SelectContext(ctx, &routes, `
				SELECT user_email FROM user_routes WHERE account_id = ? ORDER BY user_email`, newAccountID); err != nil {
				return fmt.Errorf("read disabled account routes: %w", err)
			}
			for _, route := range routes {
				updatedExisting := false
				for index := range update.Routes {
					if update.Routes[index].UserEmail == route.User {
						update.Routes[index].After = fallbackID
						updatedExisting = true
						break
					}
				}
				if !updatedExisting {
					update.Routes = append(update.Routes, AccountRouteChange{
						UserEmail: route.User, Before: accountID, After: fallbackID,
					})
				}
			}
			if _, err := transaction.ExecContext(ctx, `
				UPDATE user_routes SET account_id = ? WHERE account_id = ?`, fallbackID, newAccountID); err != nil {
				return fmt.Errorf("reroute disabled account users: %w", err)
			}
		}
		if desiredDefault {
			if _, err := transaction.ExecContext(ctx, `
				UPDATE accounts SET default_group = CASE WHEN id = ? THEN 1 ELSE 0 END`, newAccountID); err != nil {
				return fmt.Errorf("set updated default account: %w", err)
			}
		} else if before.Account.DefaultGroup {
			if fallbackID == "" {
				return ErrAccountDeleteNeedsFallback
			}
			if _, err := transaction.ExecContext(ctx, `
				UPDATE accounts SET default_group = CASE WHEN id = ? THEN 1 ELSE 0 END`, fallbackID); err != nil {
				return fmt.Errorf("replace updated default account: %w", err)
			}
		}

		after, found, err := readStoredAccount(ctx, transaction, newAccountID)
		if err != nil {
			return err
		}
		if !found {
			return ErrAccountLifecycleConflict
		}
		update.After = after
		rowsAfter, err := readAccountKeyRows(ctx, transaction, newAccountID)
		if err != nil {
			return err
		}
		update.RowsAfter = rowsAfter
		updatedDefaults := make([]struct {
			ID      string `db:"id"`
			Default int    `db:"default_group"`
		}, 0)
		if err := transaction.SelectContext(ctx, &updatedDefaults, `
			SELECT id, default_group FROM accounts ORDER BY position, id`); err != nil {
			return fmt.Errorf("read updated default accounts: %w", err)
		}
		for _, candidate := range updatedDefaults {
			update.DefaultsAfter = append(update.DefaultsAfter, AccountDefaultState{
				AccountID: candidate.ID, Default: candidate.Default != 0,
			})
		}
		return nil
	})
	return update, err
}

func (store *Store) RestoreAccountUpdate(ctx context.Context, update AccountUpdate) error {
	if update.Before.ID == "" || update.After.ID == "" {
		return fmt.Errorf("%w: incomplete account update rollback", ErrInvalidCatalogInput)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		current, found, err := readStoredAccount(ctx, transaction, update.After.ID)
		if err != nil {
			return err
		}
		if !found || !sameStoredAccount(current, update.After) {
			return ErrAccountLifecycleConflict
		}
		if update.Before.ID != update.After.ID {
			if _, found, err := readStoredAccount(ctx, transaction, update.Before.ID); err != nil {
				return err
			} else if found {
				return ErrAccountLifecycleConflict
			}
		}
		currentRows, err := readAccountKeyRows(ctx, transaction, update.After.ID)
		if err != nil {
			return err
		}
		if !sameStoredKeyRecords(currentRows, update.RowsAfter) {
			return ErrAccountLifecycleConflict
		}
		for _, route := range update.Routes {
			var currentRoute string
			if err := transaction.GetContext(ctx, &currentRoute, `
				SELECT account_id FROM user_routes WHERE user_email = ?`, route.UserEmail); err != nil {
				return fmt.Errorf("read account update rollback route: %w", err)
			}
			if currentRoute != route.After {
				return ErrAccountLifecycleConflict
			}
		}
		for _, expected := range update.DefaultsAfter {
			var currentDefault int
			if err := transaction.GetContext(ctx, &currentDefault, `
				SELECT default_group FROM accounts WHERE id = ?`, expected.AccountID); err != nil {
				return fmt.Errorf("read account update rollback default: %w", err)
			}
			if (currentDefault != 0) != expected.Default {
				return ErrAccountLifecycleConflict
			}
		}
		before := update.Before
		if _, err := transaction.ExecContext(ctx, `
			UPDATE accounts
			   SET id = ?, email = ?, port = ?, proxy_mode = ?, created_at = ?,
			       group_enabled = ?, default_group = ?, position = ?
			 WHERE id = ?`,
			before.ID, before.Email, before.Port, before.ProxyMode, before.CreatedAt,
			boolInteger(before.GroupEnabled), boolInteger(before.DefaultGroup), before.Position,
			update.After.ID,
		); err != nil {
			return fmt.Errorf("restore updated account: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, "DELETE FROM key_records WHERE account_id = ?", update.After.ID); err != nil {
			return fmt.Errorf("clear updated account Key rows: %w", err)
		}
		for _, row := range update.RowsBefore {
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO key_records(
					sequence, label, account_id, account_email, user_email,
					status, secret, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				row.Sequence, row.Label, row.Account, row.AccountEmail, row.User,
				row.Status, row.Key, row.CreatedAt, row.UpdatedAt,
			); err != nil {
				return fmt.Errorf("restore updated account Key row %d: %w", row.Sequence, err)
			}
		}
		for _, route := range update.Routes {
			if _, err := transaction.ExecContext(ctx, `
				UPDATE user_routes SET account_id = ? WHERE user_email = ? AND account_id = ?`,
				route.Before, route.UserEmail, route.After,
			); err != nil {
				return fmt.Errorf("restore updated account route: %w", err)
			}
		}
		for _, state := range update.DefaultsBefore {
			accountID := state.AccountID
			if accountID == update.Before.ID && update.Before.ID != update.After.ID {
				accountID = update.Before.ID
			}
			if _, err := transaction.ExecContext(ctx, `
				UPDATE accounts SET default_group = ? WHERE id = ?`, boolInteger(state.Default), accountID); err != nil {
				return fmt.Errorf("restore updated account default state: %w", err)
			}
		}
		return nil
	})
}

// ApplyAccountDeletion removes one account, preserves all shared user API Keys
// on the remaining account rows, and moves explicit routes to one enabled
// fallback. Exclusive active Keys require an explicit revoke confirmation.
func (store *Store) ApplyAccountDeletion(
	ctx context.Context,
	rawAccountID string,
	rawFallback string,
	revokeExclusive bool,
) (AccountDeletion, error) {
	deletion := AccountDeletion{
		Rows: make([]StoredKeyRecord, 0), Routes: make([]AccountRouteChange, 0),
		Defaults: make([]AccountDefaultState, 0),
	}
	accountID, err := NormalizeAccountID(rawAccountID)
	if err != nil {
		return deletion, err
	}
	fallbackID := strings.TrimSpace(rawFallback)
	if fallbackID != "" {
		fallbackID, err = NormalizeAccountID(fallbackID)
		if err != nil {
			return deletion, err
		}
	}
	err = store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		account, found, err := readStoredAccount(ctx, transaction, accountID)
		if err != nil {
			return err
		}
		if !found {
			return ErrAccountLifecycleNotFound
		}
		deletion.Account = account
		accounts := make([]struct {
			ID      string `db:"id"`
			Enabled int    `db:"group_enabled"`
			Default int    `db:"default_group"`
		}, 0)
		if err := transaction.SelectContext(ctx, &accounts, `
			SELECT id, group_enabled, default_group FROM accounts ORDER BY position, id`); err != nil {
			return fmt.Errorf("read accounts for deletion: %w", err)
		}
		if len(accounts) <= 1 {
			return ErrAccountDeleteLast
		}
		enabled := make([]string, 0)
		for _, candidate := range accounts {
			deletion.Defaults = append(deletion.Defaults, AccountDefaultState{
				AccountID: candidate.ID, Default: candidate.Default != 0,
			})
			if candidate.ID != accountID && candidate.Enabled != 0 {
				enabled = append(enabled, candidate.ID)
			}
		}
		if fallbackID != "" {
			if fallbackID == accountID || !containsString(enabled, fallbackID) {
				return ErrAccountDeleteNeedsFallback
			}
		} else {
			for _, candidate := range accounts {
				if candidate.ID != accountID && candidate.Enabled != 0 && candidate.Default != 0 {
					fallbackID = candidate.ID
					break
				}
			}
			if fallbackID == "" && len(enabled) > 0 {
				fallbackID = enabled[0]
			}
		}
		if fallbackID == "" {
			return ErrAccountDeleteNeedsFallback
		}
		deletion.FallbackAccount = fallbackID

		rows, err := readAccountKeyRows(ctx, transaction, accountID)
		if err != nil {
			return err
		}
		deletion.Rows = rows
		exclusive := make(map[string]struct{})
		for _, row := range rows {
			if row.Status != "active" {
				continue
			}
			var elsewhere int
			if err := transaction.GetContext(ctx, &elsewhere, `
				SELECT COUNT(*) FROM key_records
				 WHERE lower(trim(user_email)) = lower(trim(?))
				   AND secret = ? AND status = 'active' AND account_id <> ?`,
				row.User, row.Key, accountID,
			); err != nil {
				return fmt.Errorf("check account Key exclusivity: %w", err)
			}
			if elsewhere == 0 {
				exclusive[row.Key] = struct{}{}
			}
		}
		if len(exclusive) > 0 && !revokeExclusive {
			return ErrAccountDeleteRequiresRevoke
		}
		deletion.RevokedExclusiveKeys = len(exclusive)

		routes := make([]struct {
			User string `db:"user_email"`
		}, 0)
		if err := transaction.SelectContext(ctx, &routes, `
			SELECT user_email FROM user_routes WHERE account_id = ? ORDER BY user_email`, accountID); err != nil {
			return fmt.Errorf("read account routes for deletion: %w", err)
		}
		for _, route := range routes {
			deletion.Routes = append(deletion.Routes, AccountRouteChange{
				UserEmail: route.User, Before: accountID, After: fallbackID,
			})
		}
		if _, err := transaction.ExecContext(ctx, `
			UPDATE user_routes SET account_id = ? WHERE account_id = ?`, fallbackID, accountID); err != nil {
			return fmt.Errorf("reroute deleted account users: %w", err)
		}
		if account.Account.DefaultGroup {
			if _, err := transaction.ExecContext(ctx, `
				UPDATE accounts SET default_group = CASE WHEN id = ? THEN 1 ELSE 0 END`, fallbackID); err != nil {
				return fmt.Errorf("replace deleted default account: %w", err)
			}
		}
		if _, err := transaction.ExecContext(ctx, "DELETE FROM key_records WHERE account_id = ?", accountID); err != nil {
			return fmt.Errorf("delete account Key rows: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, "DELETE FROM accounts WHERE id = ?", accountID); err != nil {
			return fmt.Errorf("delete account row: %w", err)
		}
		return nil
	})
	return deletion, err
}

func (store *Store) RestoreAccountDeletion(ctx context.Context, deletion AccountDeletion) error {
	if deletion.Account.ID == "" || deletion.FallbackAccount == "" {
		return fmt.Errorf("%w: incomplete account deletion rollback", ErrInvalidCatalogInput)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		_, found, err := readStoredAccount(ctx, transaction, deletion.Account.ID)
		if err != nil {
			return err
		}
		if found {
			return ErrAccountLifecycleConflict
		}
		for _, route := range deletion.Routes {
			var current string
			if err := transaction.GetContext(ctx, &current, `
				SELECT account_id FROM user_routes WHERE user_email = ?`, route.UserEmail); err != nil {
				return fmt.Errorf("read deleted account route rollback target: %w", err)
			}
			if current != route.After {
				return ErrAccountLifecycleConflict
			}
		}
		for _, row := range deletion.Rows {
			_, found, err := readStoredKeyRecord(ctx, transaction, row.Sequence)
			if err != nil {
				return err
			}
			if found {
				return ErrAccountLifecycleConflict
			}
		}
		account := deletion.Account
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO accounts(
				id, email, port, proxy_mode, created_at,
				group_enabled, default_group, position
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			account.ID, account.Email, account.Port, account.ProxyMode, account.CreatedAt,
			boolInteger(account.GroupEnabled), boolInteger(account.DefaultGroup), account.Position,
		); err != nil {
			return fmt.Errorf("restore deleted account: %w", err)
		}
		rows := append([]StoredKeyRecord(nil), deletion.Rows...)
		sort.Slice(rows, func(left, right int) bool { return rows[left].Sequence < rows[right].Sequence })
		for _, row := range rows {
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO key_records(
					sequence, label, account_id, account_email, user_email,
					status, secret, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				row.Sequence, row.Label, row.Account, row.AccountEmail, row.User,
				row.Status, row.Key, row.CreatedAt, row.UpdatedAt,
			); err != nil {
				return fmt.Errorf("restore deleted account Key row %d: %w", row.Sequence, err)
			}
		}
		for _, route := range deletion.Routes {
			if _, err := transaction.ExecContext(ctx, `
				UPDATE user_routes SET account_id = ? WHERE user_email = ? AND account_id = ?`,
				route.Before, route.UserEmail, route.After,
			); err != nil {
				return fmt.Errorf("restore deleted account route: %w", err)
			}
		}
		for _, state := range deletion.Defaults {
			if _, err := transaction.ExecContext(ctx, `
				UPDATE accounts SET default_group = ? WHERE id = ?`, boolInteger(state.Default), state.AccountID); err != nil {
				return fmt.Errorf("restore account default state: %w", err)
			}
		}
		return nil
	})
}

func readStoredAccount(ctx context.Context, transaction *sqlx.Tx, accountID string) (StoredAccount, bool, error) {
	var row struct {
		ID           string `db:"id"`
		Email        string `db:"email"`
		Port         int    `db:"port"`
		ProxyMode    string `db:"proxy_mode"`
		CreatedAt    int64  `db:"created_at"`
		GroupEnabled int    `db:"group_enabled"`
		DefaultGroup int    `db:"default_group"`
		Position     int    `db:"position"`
	}
	err := transaction.GetContext(ctx, &row, `
		SELECT id, email, port, proxy_mode, created_at,
		       group_enabled, default_group, position
		  FROM accounts WHERE id = ?`, accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredAccount{}, false, nil
	}
	if err != nil {
		return StoredAccount{}, false, fmt.Errorf("read account lifecycle row: %w", err)
	}
	return StoredAccount{Position: row.Position, Account: Account{
		ID: row.ID, Email: row.Email, Port: row.Port, ProxyMode: row.ProxyMode,
		CreatedAt: row.CreatedAt, GroupEnabled: row.GroupEnabled != 0,
		DefaultGroup: row.DefaultGroup != 0,
	}}, true, nil
}

func readAccountKeyRows(ctx context.Context, transaction *sqlx.Tx, accountID string) ([]StoredKeyRecord, error) {
	rows := make([]struct {
		Sequence     int    `db:"sequence"`
		Label        string `db:"label"`
		Account      string `db:"account_id"`
		AccountEmail string `db:"account_email"`
		User         string `db:"user_email"`
		Status       string `db:"status"`
		Key          string `db:"secret"`
		CreatedAt    int64  `db:"created_at"`
		UpdatedAt    int64  `db:"updated_at"`
	}, 0)
	if err := transaction.SelectContext(ctx, &rows, `
		SELECT sequence, label, account_id, account_email, user_email,
		       status, secret, created_at, updated_at
		  FROM key_records WHERE account_id = ? ORDER BY sequence`, accountID); err != nil {
		return nil, fmt.Errorf("read account Key rows: %w", err)
	}
	result := make([]StoredKeyRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, StoredKeyRecord{Sequence: row.Sequence, KeyRecord: KeyRecord{
			Label: row.Label, Account: row.Account, AccountEmail: row.AccountEmail,
			User: row.User, Status: row.Status, Key: row.Key,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}})
	}
	return result, nil
}

func readStoredKeyRecord(ctx context.Context, transaction *sqlx.Tx, sequence int) (StoredKeyRecord, bool, error) {
	rows, err := readAccountKeyRowsByQuery(ctx, transaction, "sequence = ?", sequence)
	if err != nil {
		return StoredKeyRecord{}, false, err
	}
	if len(rows) == 0 {
		return StoredKeyRecord{}, false, nil
	}
	return rows[0], true, nil
}

func readAccountKeyRowsByQuery(ctx context.Context, transaction *sqlx.Tx, where string, argument any) ([]StoredKeyRecord, error) {
	query := `SELECT sequence, label, account_id, account_email, user_email,
	                 status, secret, created_at, updated_at
	            FROM key_records WHERE ` + where + ` ORDER BY sequence`
	rows := make([]struct {
		Sequence     int    `db:"sequence"`
		Label        string `db:"label"`
		Account      string `db:"account_id"`
		AccountEmail string `db:"account_email"`
		User         string `db:"user_email"`
		Status       string `db:"status"`
		Key          string `db:"secret"`
		CreatedAt    int64  `db:"created_at"`
		UpdatedAt    int64  `db:"updated_at"`
	}, 0)
	if err := transaction.SelectContext(ctx, &rows, query, argument); err != nil {
		return nil, fmt.Errorf("read stored Key record: %w", err)
	}
	result := make([]StoredKeyRecord, 0, len(rows))
	for _, row := range rows {
		result = append(result, StoredKeyRecord{Sequence: row.Sequence, KeyRecord: KeyRecord{
			Label: row.Label, Account: row.Account, AccountEmail: row.AccountEmail,
			User: row.User, Status: row.Status, Key: row.Key,
			CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
		}})
	}
	return result, nil
}

func sameAccount(left Account, right Account) bool {
	return left.ID == right.ID && left.Email == right.Email && left.Port == right.Port &&
		left.ProxyMode == right.ProxyMode && left.CreatedAt == right.CreatedAt &&
		left.GroupEnabled == right.GroupEnabled && left.DefaultGroup == right.DefaultGroup
}

func sameStoredAccount(left StoredAccount, right StoredAccount) bool {
	return left.Position == right.Position && sameAccount(left.Account, right.Account)
}

func sameStoredKeyRecord(left StoredKeyRecord, right StoredKeyRecord) bool {
	return left.Sequence == right.Sequence && left.Label == right.Label &&
		left.Account == right.Account && left.AccountEmail == right.AccountEmail &&
		strings.EqualFold(left.User, right.User) && left.Status == right.Status &&
		left.Key == right.Key && left.CreatedAt == right.CreatedAt && left.UpdatedAt == right.UpdatedAt
}

func sameStoredKeyRecords(left []StoredKeyRecord, right []StoredKeyRecord) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !sameStoredKeyRecord(left[index], right[index]) {
			return false
		}
	}
	return true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
