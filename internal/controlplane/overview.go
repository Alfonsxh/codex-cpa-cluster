package controlplane

import (
	"context"
	"database/sql"
	"fmt"
)

// OverviewSummary is a bounded control-plane catalog projection. It is kept
// separate from runtime state and usage data so opening the Admin overview does
// not fan out to Docker, OAuth, Gateway, or the high-write usage database.
type OverviewSummary struct {
	Accounts           int `db:"accounts" json:"accounts"`
	EnabledAccounts    int `db:"enabled_accounts" json:"enabled_accounts"`
	Users              int `db:"users" json:"users"`
	ActiveUsers        int `db:"active_users" json:"active_users"`
	ActiveKeys         int `db:"active_keys" json:"active_keys"`
	RoutedUsers        int `db:"routed_users" json:"routed_users"`
	UnassignedUsers    int `db:"unassigned_users" json:"unassigned_users"`
	Teams              int `db:"teams" json:"teams"`
	IncompleteMatrices int `db:"incomplete_matrices" json:"incomplete_key_matrices"`
}

// ReadOverviewSummary returns all overview counters from one read-only SQLite
// snapshot. No raw Key, email address, secret digest, or account identity is
// selected by this query.
func (store *Store) ReadOverviewSummary(ctx context.Context) (OverviewSummary, error) {
	result := OverviewSummary{}
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return result, fmt.Errorf("begin control-plane overview snapshot: %w", err)
	}
	defer transaction.Rollback()
	if err := transaction.GetContext(ctx, &result, `
WITH user_catalog AS (
    SELECT lower(trim(user_email)) AS email,
           MAX(CASE WHEN status = 'active' THEN 1 ELSE 0 END) AS active,
           COUNT(DISTINCT CASE WHEN status = 'active' THEN account_id END) AS active_accounts
      FROM key_records
     WHERE trim(user_email) <> ''
     GROUP BY lower(trim(user_email))
), account_catalog AS (
    SELECT COUNT(*) AS total,
           COALESCE(SUM(CASE WHEN group_enabled = 1 THEN 1 ELSE 0 END), 0) AS enabled
      FROM accounts
), active_key_catalog AS (
    SELECT COUNT(DISTINCT secret) AS total
      FROM key_records
     WHERE status = 'active'
), routed_catalog AS (
    SELECT COUNT(*) AS total FROM user_routes
), team_catalog AS (
    SELECT COUNT(*) AS total FROM teams
)
SELECT account_catalog.total AS accounts,
       account_catalog.enabled AS enabled_accounts,
       (SELECT COUNT(*) FROM user_catalog) AS users,
       (SELECT COUNT(*) FROM user_catalog WHERE active = 1) AS active_users,
       active_key_catalog.total AS active_keys,
       routed_catalog.total AS routed_users,
       (
           SELECT COUNT(*)
             FROM user_catalog AS users
             LEFT JOIN user_team_memberships AS memberships
               ON lower(trim(memberships.user_email)) = users.email
            WHERE memberships.team_id IS NULL
       ) AS unassigned_users,
       team_catalog.total AS teams,
       (
           SELECT COUNT(*)
             FROM user_catalog
            WHERE active = 1 AND active_accounts < account_catalog.enabled
       ) AS incomplete_matrices
  FROM account_catalog, active_key_catalog, routed_catalog, team_catalog`); err != nil {
		return result, fmt.Errorf("read control-plane overview: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return result, fmt.Errorf("commit control-plane overview snapshot: %w", err)
	}
	return result, nil
}
