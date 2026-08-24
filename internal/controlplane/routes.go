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
	ErrRouteConflict       = errors.New("user route conflict")
	ErrRouteTargetNotFound = errors.New("route target account not found")
	ErrRouteUserNotFound   = errors.New("route user not found")
	ErrRouteUserUnsafe     = errors.New("route user is not safe to migrate")
)

type RouteConflictError struct {
	Users []string
}

func (conflict *RouteConflictError) Error() string {
	return fmt.Sprintf("%s: %s", ErrRouteConflict, strings.Join(conflict.Users, ", "))
}

func (conflict *RouteConflictError) Unwrap() error {
	return ErrRouteConflict
}

type RouteUpdateResult struct {
	MovedUsers   int            `json:"moved_users"`
	Destinations map[string]int `json:"destinations"`
}

func (store *Store) ApplyRoutesExpected(
	ctx context.Context,
	assignments map[string]string,
	expectedRoutes map[string]string,
) (RouteUpdateResult, error) {
	result := RouteUpdateResult{Destinations: make(map[string]int)}
	users := make([]string, 0, len(assignments))
	normalizedAssignments := make(map[string]string, len(assignments))
	normalizedExpected := make(map[string]string, len(assignments))
	for rawUser, rawTarget := range assignments {
		user := strings.ToLower(strings.TrimSpace(rawUser))
		target := strings.TrimSpace(rawTarget)
		if user == "" || target == "" {
			return result, fmt.Errorf("%w: route user and target are required", ErrInvalidCatalogInput)
		}
		expected, found := expectedRoutes[rawUser]
		if !found {
			expected, found = expectedRoutes[user]
		}
		if !found {
			return result, fmt.Errorf("%w: expected route is required for %s", ErrInvalidCatalogInput, user)
		}
		if _, duplicate := normalizedAssignments[user]; !duplicate {
			users = append(users, user)
		}
		normalizedAssignments[user] = target
		normalizedExpected[user] = strings.TrimSpace(expected)
	}
	sort.Strings(users)
	if len(users) == 0 {
		return result, nil
	}
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		targets := make(map[string]struct{})
		for _, target := range normalizedAssignments {
			targets[target] = struct{}{}
		}
		for target := range targets {
			var exists int
			err := transaction.GetContext(ctx, &exists, "SELECT 1 FROM accounts WHERE id = ?", target)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrRouteTargetNotFound, target)
			}
			if err != nil {
				return fmt.Errorf("validate route target %s: %w", target, err)
			}
		}
		conflicts := make([]string, 0)
		currentRoutes := make(map[string]string, len(users))
		for _, user := range users {
			rows := make([]struct {
				AccountID string `db:"account_id"`
				Secret    string `db:"secret"`
			}, 0)
			if err := transaction.SelectContext(ctx, &rows, `
                SELECT account_id, secret
                  FROM key_records
                 WHERE user_email = ? AND status = 'active'`, user); err != nil {
				return fmt.Errorf("validate route user %s: %w", user, err)
			}
			if len(rows) == 0 {
				return fmt.Errorf("%w: %s", ErrRouteUserNotFound, user)
			}
			secrets := make(map[string]struct{})
			targetAvailable := false
			for _, row := range rows {
				secrets[row.Secret] = struct{}{}
				if row.AccountID == normalizedAssignments[user] {
					targetAvailable = true
				}
			}
			if len(secrets) != 1 || !targetAvailable {
				return fmt.Errorf("%w: %s", ErrRouteUserUnsafe, user)
			}
			var current string
			err := transaction.GetContext(
				ctx,
				&current,
				"SELECT account_id FROM user_routes WHERE user_email = ?",
				user,
			)
			if errors.Is(err, sql.ErrNoRows) {
				current = ""
			} else if err != nil {
				return fmt.Errorf("read current route for %s: %w", user, err)
			}
			currentRoutes[user] = current
			if current != normalizedExpected[user] {
				conflicts = append(conflicts, user)
			}
		}
		if len(conflicts) > 0 {
			return &RouteConflictError{Users: conflicts}
		}
		for _, user := range users {
			target := normalizedAssignments[user]
			if currentRoutes[user] == target {
				continue
			}
			if _, err := transaction.ExecContext(ctx, `
                INSERT INTO user_routes(user_email, account_id) VALUES (?, ?)
                ON CONFLICT(user_email) DO UPDATE SET account_id = excluded.account_id`,
				user,
				target,
			); err != nil {
				return fmt.Errorf("update route for %s: %w", user, err)
			}
			result.MovedUsers++
			result.Destinations[target]++
		}
		return nil
	})
	return result, err
}

func (store *Store) RestoreRoutesExpected(
	ctx context.Context,
	originalRoutes map[string]string,
	expectedCurrentRoutes map[string]string,
) error {
	users := make([]string, 0, len(originalRoutes))
	normalizedOriginal := make(map[string]string, len(originalRoutes))
	normalizedCurrent := make(map[string]string, len(originalRoutes))
	for rawUser, original := range originalRoutes {
		user := strings.ToLower(strings.TrimSpace(rawUser))
		if user == "" {
			return fmt.Errorf("%w: rollback route user is required", ErrInvalidCatalogInput)
		}
		expected, found := expectedCurrentRoutes[rawUser]
		if !found {
			expected, found = expectedCurrentRoutes[user]
		}
		if !found || strings.TrimSpace(expected) == "" {
			return fmt.Errorf("%w: rollback expected route is required for %s", ErrInvalidCatalogInput, user)
		}
		if _, duplicate := normalizedOriginal[user]; !duplicate {
			users = append(users, user)
		}
		normalizedOriginal[user] = strings.TrimSpace(original)
		normalizedCurrent[user] = strings.TrimSpace(expected)
	}
	sort.Strings(users)
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		conflicts := make([]string, 0)
		for _, user := range users {
			var current string
			err := transaction.GetContext(ctx, &current, "SELECT account_id FROM user_routes WHERE user_email = ?", user)
			if errors.Is(err, sql.ErrNoRows) {
				current = ""
			} else if err != nil {
				return fmt.Errorf("read rollback route for %s: %w", user, err)
			}
			if current != normalizedCurrent[user] {
				conflicts = append(conflicts, user)
			}
		}
		if len(conflicts) > 0 {
			return &RouteConflictError{Users: conflicts}
		}
		for _, user := range users {
			original := normalizedOriginal[user]
			if original == "" {
				if _, err := transaction.ExecContext(ctx, "DELETE FROM user_routes WHERE user_email = ?", user); err != nil {
					return fmt.Errorf("delete rollback route for %s: %w", user, err)
				}
				continue
			}
			if _, err := transaction.ExecContext(ctx, `
                INSERT INTO user_routes(user_email, account_id) VALUES (?, ?)
                ON CONFLICT(user_email) DO UPDATE SET account_id = excluded.account_id`,
				user,
				original,
			); err != nil {
				return fmt.Errorf("restore route for %s: %w", user, err)
			}
		}
		return nil
	})
}
