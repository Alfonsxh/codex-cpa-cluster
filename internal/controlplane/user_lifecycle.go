package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jmoiron/sqlx"
)

var (
	ErrUserAlreadyActive        = errors.New("user already has an active API key")
	ErrUserLifecycleNotFound    = errors.New("user lifecycle not found")
	ErrUserLifecycleConflict    = errors.New("user lifecycle conflict")
	ErrUserDeleteRequiresRevoke = errors.New("active user deletion requires key revocation")
)

type UserCreation struct {
	User               string
	APIKey             string
	CreatedRows        []int
	PreviousRoute      *string
	TeamChanged        bool
	PreviousMembership *UserMembershipState
}

type UserRevocation struct {
	User string
	Rows []UserStatusRow
}

type UserDeletion struct {
	User              string
	Rows              []StoredKeyRecord
	Route             *string
	Membership        *UserMembershipState
	Tags              []UserTagState
	InternalKey       *InternalKey
	RevokedActiveKeys int
}

type UserStatusRow struct {
	Sequence  int    `db:"sequence"`
	Secret    string `db:"secret"`
	UpdatedAt int64  `db:"updated_at"`
}

type StoredKeyRecord struct {
	Sequence int
	KeyRecord
}

type UserMembershipState struct {
	TeamID            *string
	MembershipVersion int64
	UpdatedAt         int64
}

type UserTagState struct {
	TagID      string `db:"tag_id"`
	AssignedAt int64  `db:"assigned_at"`
}

// ActiveUserKey returns the unified active key only when every active row for
// the user agrees. Callers deliberately keep the raw value inside the server;
// list APIs must never expose it or its digest.
func (store *Store) ActiveUserKey(ctx context.Context, user string) (string, error) {
	user = strings.ToLower(strings.TrimSpace(user))
	if user == "" {
		return "", fmt.Errorf("%w: user is required", ErrInvalidCatalogInput)
	}
	keys := make([]string, 0)
	if err := store.db.SelectContext(ctx, &keys, `
		SELECT DISTINCT secret
		  FROM key_records
		 WHERE lower(trim(user_email)) = ? AND status = 'active'
		 ORDER BY secret`, user); err != nil {
		return "", fmt.Errorf("read active user key: %w", err)
	}
	if len(keys) == 0 {
		return "", ErrUserLifecycleNotFound
	}
	if len(keys) != 1 {
		return "", ErrUserLifecycleConflict
	}
	return keys[0], nil
}

// ApplyUserCreation appends one shared external key row per current account in
// one transaction. It clears a stale route so first self-service login can run
// the normal load-aware assignment. Team assignment is optional and expected
// to be selected from the live catalog by the Admin UI.
func (store *Store) ApplyUserCreation(
	ctx context.Context,
	user string,
	apiKey string,
	teamID *string,
) (UserCreation, error) {
	creation := UserCreation{
		User: strings.ToLower(strings.TrimSpace(user)), APIKey: strings.TrimSpace(apiKey),
		CreatedRows: make([]int, 0),
	}
	if creation.User == "" || creation.APIKey == "" {
		return creation, fmt.Errorf("%w: user and API key are required", ErrInvalidCatalogInput)
	}
	normalizedTeamID := normalizeOptionalCatalogID(teamID)
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		var active int
		if err := transaction.GetContext(ctx, &active, `
			SELECT COUNT(*) FROM key_records
			 WHERE lower(trim(user_email)) = ? AND status = 'active'`, creation.User); err != nil {
			return fmt.Errorf("check active user rows: %w", err)
		}
		if active != 0 {
			return ErrUserAlreadyActive
		}
		var duplicate int
		if err := transaction.GetContext(ctx, &duplicate, "SELECT COUNT(*) FROM key_records WHERE secret = ?", creation.APIKey); err != nil {
			return fmt.Errorf("check new user key uniqueness: %w", err)
		}
		if duplicate != 0 {
			return ErrUserLifecycleConflict
		}
		accounts := make([]struct {
			ID    string `db:"id"`
			Email string `db:"email"`
		}, 0)
		if err := transaction.SelectContext(ctx, &accounts, "SELECT id, email FROM accounts ORDER BY position, id"); err != nil {
			return fmt.Errorf("read accounts for user creation: %w", err)
		}
		if len(accounts) == 0 {
			return fmt.Errorf("%w: at least one account is required", ErrInvalidCatalogInput)
		}
		if normalizedTeamID != nil {
			var teamExists int
			if err := transaction.GetContext(ctx, &teamExists, "SELECT COUNT(*) FROM teams WHERE id = ?", *normalizedTeamID); err != nil {
				return fmt.Errorf("validate user team: %w", err)
			}
			if teamExists != 1 {
				return ErrTeamNotFound
			}
			membership, found, err := readUserMembership(ctx, transaction, creation.User)
			if err != nil {
				return err
			}
			if found {
				creation.PreviousMembership = &membership
			}
			version := int64(1)
			if creation.PreviousMembership != nil {
				version = creation.PreviousMembership.MembershipVersion + 1
			}
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO user_team_memberships(user_email, team_id, membership_version, updated_at)
				VALUES (?, ?, ?, ?)
				ON CONFLICT(user_email) DO UPDATE SET
					team_id = excluded.team_id,
					membership_version = excluded.membership_version,
					updated_at = excluded.updated_at`,
				creation.User, *normalizedTeamID, version, store.now().Unix(),
			); err != nil {
				return fmt.Errorf("assign new user team: %w", err)
			}
			creation.TeamChanged = true
		}
		var route string
		routeError := transaction.GetContext(
			ctx, &route, "SELECT account_id FROM user_routes WHERE lower(trim(user_email)) = ?", creation.User,
		)
		if routeError == nil {
			creation.PreviousRoute = &route
		} else if !errors.Is(routeError, sql.ErrNoRows) {
			return fmt.Errorf("read stale user route: %w", routeError)
		}
		if _, err := transaction.ExecContext(ctx, "DELETE FROM user_routes WHERE lower(trim(user_email)) = ?", creation.User); err != nil {
			return fmt.Errorf("clear stale user route: %w", err)
		}
		var sequence int
		if err := transaction.GetContext(ctx, &sequence, "SELECT COALESCE(MAX(sequence), -1) + 1 FROM key_records"); err != nil {
			return fmt.Errorf("allocate user key sequence: %w", err)
		}
		now := store.now().Unix()
		for _, account := range accounts {
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO key_records(
					sequence, label, account_id, account_email, user_email,
					status, secret, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?)`,
				sequence, creation.User+":"+account.ID, account.ID, account.Email,
				creation.User, creation.APIKey, now, now,
			); err != nil {
				return fmt.Errorf("create user key row for %s: %w", account.ID, err)
			}
			creation.CreatedRows = append(creation.CreatedRows, sequence)
			sequence++
		}
		return nil
	})
	return creation, err
}

func (store *Store) RestoreUserCreation(ctx context.Context, creation UserCreation) error {
	if creation.User == "" || creation.APIKey == "" || len(creation.CreatedRows) == 0 {
		return fmt.Errorf("%w: incomplete user creation rollback", ErrInvalidCatalogInput)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		for _, sequence := range creation.CreatedRows {
			var row struct {
				User   string `db:"user_email"`
				Status string `db:"status"`
				Secret string `db:"secret"`
			}
			if err := transaction.GetContext(ctx, &row, `
				SELECT user_email, status, secret FROM key_records WHERE sequence = ?`, sequence); err != nil {
				return fmt.Errorf("read created user row %d for rollback: %w", sequence, err)
			}
			if !strings.EqualFold(row.User, creation.User) || row.Status != "active" || row.Secret != creation.APIKey {
				return ErrUserLifecycleConflict
			}
		}
		for _, sequence := range creation.CreatedRows {
			if _, err := transaction.ExecContext(ctx, "DELETE FROM key_records WHERE sequence = ?", sequence); err != nil {
				return fmt.Errorf("delete created user row %d: %w", sequence, err)
			}
		}
		if creation.PreviousRoute == nil {
			if _, err := transaction.ExecContext(ctx, "DELETE FROM user_routes WHERE user_email = ?", creation.User); err != nil {
				return fmt.Errorf("clear created user route: %w", err)
			}
		} else if _, err := transaction.ExecContext(ctx, `
			INSERT INTO user_routes(user_email, account_id) VALUES (?, ?)
			ON CONFLICT(user_email) DO UPDATE SET account_id = excluded.account_id`,
			creation.User, *creation.PreviousRoute,
		); err != nil {
			return fmt.Errorf("restore previous user route: %w", err)
		}
		if creation.TeamChanged {
			if err := restoreUserMembership(ctx, transaction, creation.User, creation.PreviousMembership); err != nil {
				return err
			}
		}
		return nil
	})
}

func (store *Store) ApplyUserRevocation(ctx context.Context, user string) (UserRevocation, error) {
	revocation := UserRevocation{User: strings.ToLower(strings.TrimSpace(user)), Rows: make([]UserStatusRow, 0)}
	if revocation.User == "" {
		return revocation, fmt.Errorf("%w: user is required", ErrInvalidCatalogInput)
	}
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if err := transaction.SelectContext(ctx, &revocation.Rows, `
			SELECT sequence, secret, updated_at
			  FROM key_records
			 WHERE lower(trim(user_email)) = ? AND status = 'active'
			 ORDER BY sequence`, revocation.User); err != nil {
			return fmt.Errorf("read active user rows for revocation: %w", err)
		}
		if len(revocation.Rows) == 0 {
			return ErrUserLifecycleNotFound
		}
		now := store.now().Unix()
		for _, row := range revocation.Rows {
			result, err := transaction.ExecContext(ctx, `
				UPDATE key_records SET status = 'revoked', updated_at = ?
				 WHERE sequence = ? AND status = 'active'`, now, row.Sequence)
			if err != nil {
				return fmt.Errorf("revoke user row %d: %w", row.Sequence, err)
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return ErrUserLifecycleConflict
			}
		}
		return nil
	})
	return revocation, err
}

func (store *Store) RestoreUserRevocation(ctx context.Context, revocation UserRevocation) error {
	if revocation.User == "" || len(revocation.Rows) == 0 {
		return fmt.Errorf("%w: incomplete user revocation rollback", ErrInvalidCatalogInput)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		for _, row := range revocation.Rows {
			var current struct {
				User   string `db:"user_email"`
				Status string `db:"status"`
				Secret string `db:"secret"`
			}
			if err := transaction.GetContext(ctx, &current, `
				SELECT user_email, status, secret FROM key_records WHERE sequence = ?`, row.Sequence); err != nil {
				return fmt.Errorf("read revoked user row %d for rollback: %w", row.Sequence, err)
			}
			if !strings.EqualFold(current.User, revocation.User) || current.Status != "revoked" || current.Secret != row.Secret {
				return ErrUserLifecycleConflict
			}
		}
		for _, row := range revocation.Rows {
			if _, err := transaction.ExecContext(ctx, `
				UPDATE key_records SET status = 'active', updated_at = ? WHERE sequence = ?`,
				row.UpdatedAt, row.Sequence,
			); err != nil {
				return fmt.Errorf("restore revoked user row %d: %w", row.Sequence, err)
			}
		}
		return nil
	})
}

func (store *Store) ApplyUserDeletion(ctx context.Context, user string, revokeActive bool) (UserDeletion, error) {
	deletion := UserDeletion{User: strings.ToLower(strings.TrimSpace(user)), Rows: make([]StoredKeyRecord, 0), Tags: make([]UserTagState, 0)}
	if deletion.User == "" {
		return deletion, fmt.Errorf("%w: user is required", ErrInvalidCatalogInput)
	}
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
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
			  FROM key_records
			 WHERE lower(trim(user_email)) = ? ORDER BY sequence`, deletion.User); err != nil {
			return fmt.Errorf("read user rows for deletion: %w", err)
		}
		if len(rows) == 0 {
			return ErrUserLifecycleNotFound
		}
		activeKeys := make(map[string]struct{})
		for _, row := range rows {
			deletion.Rows = append(deletion.Rows, StoredKeyRecord{Sequence: row.Sequence, KeyRecord: KeyRecord{
				Label: row.Label, Account: row.Account, AccountEmail: row.AccountEmail, User: row.User,
				Status: row.Status, Key: row.Key, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
			}})
			if row.Status == "active" {
				activeKeys[row.Key] = struct{}{}
			}
		}
		if len(activeKeys) > 0 && !revokeActive {
			return ErrUserDeleteRequiresRevoke
		}
		deletion.RevokedActiveKeys = len(activeKeys)
		var route string
		err := transaction.GetContext(ctx, &route, "SELECT account_id FROM user_routes WHERE lower(trim(user_email)) = ?", deletion.User)
		if err == nil {
			deletion.Route = &route
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read user route for deletion: %w", err)
		}
		membership, found, err := readUserMembership(ctx, transaction, deletion.User)
		if err != nil {
			return err
		}
		if found {
			deletion.Membership = &membership
		}
		if err := transaction.SelectContext(ctx, &deletion.Tags, `
			SELECT tag_id, assigned_at FROM user_tags WHERE lower(trim(user_email)) = ? ORDER BY tag_id`, deletion.User); err != nil {
			return fmt.Errorf("read user tags for deletion: %w", err)
		}
		var internal struct {
			Key       string `db:"secret"`
			CreatedAt int64  `db:"created_at"`
			Status    string `db:"status"`
		}
		err = transaction.GetContext(ctx, &internal, `
			SELECT secret, created_at, status FROM internal_keys WHERE lower(trim(user_email)) = ?`, deletion.User)
		if err == nil {
			deletion.InternalKey = &InternalKey{Key: internal.Key, CreatedAt: internal.CreatedAt, Status: internal.Status}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read internal user key for deletion: %w", err)
		}
		for _, statement := range []string{
			"DELETE FROM key_records WHERE lower(trim(user_email)) = ?",
			"DELETE FROM user_routes WHERE lower(trim(user_email)) = ?",
			"DELETE FROM user_tags WHERE lower(trim(user_email)) = ?",
			"DELETE FROM user_team_memberships WHERE lower(trim(user_email)) = ?",
			"DELETE FROM internal_keys WHERE lower(trim(user_email)) = ?",
		} {
			if _, err := transaction.ExecContext(ctx, statement, deletion.User); err != nil {
				return fmt.Errorf("delete user lifecycle state: %w", err)
			}
		}
		return nil
	})
	return deletion, err
}

func (store *Store) RestoreUserDeletion(ctx context.Context, deletion UserDeletion) error {
	if deletion.User == "" || len(deletion.Rows) == 0 {
		return fmt.Errorf("%w: incomplete user deletion rollback", ErrInvalidCatalogInput)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		var existing int
		if err := transaction.GetContext(ctx, &existing, `
			SELECT COUNT(*) FROM key_records WHERE lower(trim(user_email)) = ?`, deletion.User); err != nil {
			return fmt.Errorf("check deleted user rollback target: %w", err)
		}
		if existing != 0 {
			return ErrUserLifecycleConflict
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
				return fmt.Errorf("restore deleted user row %d: %w", row.Sequence, err)
			}
		}
		if deletion.Route != nil {
			if _, err := transaction.ExecContext(ctx, "INSERT INTO user_routes(user_email, account_id) VALUES (?, ?)", deletion.User, *deletion.Route); err != nil {
				return fmt.Errorf("restore deleted user route: %w", err)
			}
		}
		if err := restoreUserMembership(ctx, transaction, deletion.User, deletion.Membership); err != nil {
			return err
		}
		for _, tag := range deletion.Tags {
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO user_tags(user_email, tag_id, assigned_at) VALUES (?, ?, ?)`,
				deletion.User, tag.TagID, tag.AssignedAt,
			); err != nil {
				return fmt.Errorf("restore deleted user tag: %w", err)
			}
		}
		if deletion.InternalKey != nil {
			if _, err := transaction.ExecContext(ctx, `
				INSERT INTO internal_keys(user_email, secret, created_at, status) VALUES (?, ?, ?, ?)`,
				deletion.User, deletion.InternalKey.Key, deletion.InternalKey.CreatedAt, deletion.InternalKey.Status,
			); err != nil {
				return fmt.Errorf("restore deleted internal user key: %w", err)
			}
		}
		return nil
	})
}

func readUserMembership(ctx context.Context, transaction *sqlx.Tx, user string) (UserMembershipState, bool, error) {
	var row struct {
		TeamID            sql.NullString `db:"team_id"`
		MembershipVersion int64          `db:"membership_version"`
		UpdatedAt         int64          `db:"updated_at"`
	}
	err := transaction.GetContext(ctx, &row, `
		SELECT team_id, membership_version, updated_at
		  FROM user_team_memberships WHERE lower(trim(user_email)) = ?`, user)
	if errors.Is(err, sql.ErrNoRows) {
		return UserMembershipState{}, false, nil
	}
	if err != nil {
		return UserMembershipState{}, false, fmt.Errorf("read user membership: %w", err)
	}
	state := UserMembershipState{MembershipVersion: row.MembershipVersion, UpdatedAt: row.UpdatedAt}
	if row.TeamID.Valid {
		state.TeamID = &row.TeamID.String
	}
	return state, true, nil
}

func restoreUserMembership(
	ctx context.Context,
	transaction *sqlx.Tx,
	user string,
	membership *UserMembershipState,
) error {
	if membership == nil {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM user_team_memberships WHERE user_email = ?", user); err != nil {
			return fmt.Errorf("clear user membership during rollback: %w", err)
		}
		return nil
	}
	if _, err := transaction.ExecContext(ctx, `
		INSERT INTO user_team_memberships(user_email, team_id, membership_version, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_email) DO UPDATE SET
			team_id = excluded.team_id,
			membership_version = excluded.membership_version,
			updated_at = excluded.updated_at`,
		user, membership.TeamID, membership.MembershipVersion, membership.UpdatedAt,
	); err != nil {
		return fmt.Errorf("restore user membership: %w", err)
	}
	return nil
}

func normalizeOptionalCatalogID(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}
