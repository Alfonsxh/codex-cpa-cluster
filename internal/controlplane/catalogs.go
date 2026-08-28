package controlplane

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var (
	ErrInvalidCatalogInput    = errors.New("invalid catalog input")
	ErrTeamNameExists         = errors.New("team name already exists")
	ErrTeamNotFound           = errors.New("team not found")
	ErrTeamNotEmpty           = errors.New("team is not empty")
	ErrTeamMembershipConflict = errors.New("team membership conflict")
)

const maximumTeamBatchSize = 500

const userListQuery = `
WITH user_summaries AS (
    SELECT lower(trim(user_email)) AS email,
           CASE
               WHEN COUNT(DISTINCT CASE WHEN status = 'active' THEN secret END) > 0
               THEN 'active'
               ELSE 'inactive'
           END AS status,
           COUNT(DISTINCT CASE WHEN status = 'active' THEN secret END) AS active_keys,
           COUNT(DISTINCT CASE WHEN status = 'active' THEN account_id END) AS active_accounts,
           COUNT(*) AS total_records,
           MIN(created_at) AS created_at,
           MAX(updated_at) AS updated_at
      FROM key_records
     WHERE trim(user_email) <> ''
     GROUP BY lower(trim(user_email))
), catalog AS (
    SELECT u.email, u.status, u.active_keys, u.active_accounts, u.total_records,
           u.created_at, u.updated_at, r.account_id AS route_account_id,
           m.team_id, COALESCE(m.membership_version, 0) AS team_membership_version,
           t.name AS team_name, t.description AS team_description
      FROM user_summaries AS u
      LEFT JOIN user_routes AS r ON lower(trim(r.user_email)) = u.email
      LEFT JOIN user_team_memberships AS m ON lower(trim(m.user_email)) = u.email
      LEFT JOIN teams AS t ON t.id = m.team_id
)
`

type TeamMembershipConflictError struct {
	Users []string
}

func (conflict *TeamMembershipConflictError) Error() string {
	return fmt.Sprintf("%s: %s", ErrTeamMembershipConflict, strings.Join(conflict.Users, ", "))
}

func (conflict *TeamMembershipConflictError) Unwrap() error {
	return ErrTeamMembershipConflict
}

func normalizeCatalogText(value string, maximum int, field string) (string, error) {
	value = strings.Join(strings.Fields(value), " ")
	if value == "" {
		return "", fmt.Errorf("%w: %s is required", ErrInvalidCatalogInput, field)
	}
	if utf8.RuneCountInString(value) > maximum {
		return "", fmt.Errorf("%w: %s must not exceed %d characters", ErrInvalidCatalogInput, field, maximum)
	}
	return value, nil
}

func normalizeTeamDescription(value string) (string, error) {
	value = strings.Join(strings.Fields(value), " ")
	if utf8.RuneCountInString(value) > 200 {
		return "", fmt.Errorf("%w: team description must not exceed 200 characters", ErrInvalidCatalogInput)
	}
	return value, nil
}

func (store *Store) ListTeams(ctx context.Context) ([]Team, error) {
	result := make([]Team, 0)
	if err := store.db.SelectContext(ctx, &result, `
        SELECT t.id, t.name, t.description, COUNT(m.user_email) AS user_count,
               t.created_at, t.updated_at
          FROM teams AS t
          LEFT JOIN user_team_memberships AS m ON m.team_id = t.id
         GROUP BY t.id
         ORDER BY t.name COLLATE NOCASE, t.id`); err != nil {
		return nil, fmt.Errorf("list control-plane teams: %w", err)
	}
	return result, nil
}

func (store *Store) CreateTeam(ctx context.Context, name string, description string) (Team, error) {
	name, err := normalizeCatalogText(name, 64, "team name")
	if err != nil {
		return Team{}, err
	}
	description, err = normalizeTeamDescription(description)
	if err != nil {
		return Team{}, err
	}
	now := store.now().Unix()
	team := Team{
		ID:          "team_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16],
		Name:        name,
		Description: description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	err = store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, `
            INSERT INTO teams(id, name, description, created_at, updated_at)
            VALUES (?, ?, ?, ?, ?)`, team.ID, team.Name, team.Description, now, now); err != nil {
			if isTeamNameConflict(err) {
				return fmt.Errorf("%w: %s", ErrTeamNameExists, team.Name)
			}
			return fmt.Errorf("create control-plane team: %w", err)
		}
		return nil
	})
	return team, err
}

func (store *Store) UpdateTeam(
	ctx context.Context,
	teamID string,
	name string,
	description string,
) (Team, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return Team{}, fmt.Errorf("%w: team id is required", ErrInvalidCatalogInput)
	}
	name, err := normalizeCatalogText(name, 64, "team name")
	if err != nil {
		return Team{}, err
	}
	description, err = normalizeTeamDescription(description)
	if err != nil {
		return Team{}, err
	}
	err = store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		result, err := transaction.ExecContext(ctx, `
            UPDATE teams
               SET name = ?, description = ?, updated_at = ?
             WHERE id = ?`, name, description, store.now().Unix(), teamID)
		if err != nil {
			if isTeamNameConflict(err) {
				return fmt.Errorf("%w: %s", ErrTeamNameExists, name)
			}
			return fmt.Errorf("update control-plane team %s: %w", teamID, err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read updated control-plane team count: %w", err)
		}
		if count == 0 {
			return fmt.Errorf("%w: %s", ErrTeamNotFound, teamID)
		}
		return nil
	})
	if err != nil {
		return Team{}, err
	}
	return store.teamByID(ctx, teamID)
}

func (store *Store) DeleteTeam(ctx context.Context, teamID string) (DeletedTeam, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return DeletedTeam{}, fmt.Errorf("%w: team id is required", ErrInvalidCatalogInput)
	}
	deleted := DeletedTeam{ID: teamID}
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if err := transaction.GetContext(ctx, &deleted.Name, "SELECT name FROM teams WHERE id = ?", teamID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrTeamNotFound, teamID)
			}
			return fmt.Errorf("read control-plane team %s: %w", teamID, err)
		}
		var assigned int
		if err := transaction.GetContext(
			ctx,
			&assigned,
			"SELECT COUNT(*) FROM user_team_memberships WHERE team_id = ?",
			teamID,
		); err != nil {
			return fmt.Errorf("count control-plane team %s members: %w", teamID, err)
		}
		if assigned > 0 {
			return fmt.Errorf("%w: %s has %d users", ErrTeamNotEmpty, teamID, assigned)
		}
		if _, err := transaction.ExecContext(ctx, "DELETE FROM teams WHERE id = ?", teamID); err != nil {
			return fmt.Errorf("delete control-plane team %s: %w", teamID, err)
		}
		deleted.Deleted = true
		return nil
	})
	return deleted, err
}

func (store *Store) SetUserTeams(
	ctx context.Context,
	userEmails []string,
	teamID *string,
) ([]TeamAssignment, error) {
	return store.SetUserTeamsExpected(ctx, userEmails, teamID, TeamExpectation{})
}

func (store *Store) SetUserTeamsExpected(
	ctx context.Context,
	userEmails []string,
	teamID *string,
	expected TeamExpectation,
) ([]TeamAssignment, error) {
	users := normalizedUsers(userEmails)
	if len(users) == 0 {
		return nil, fmt.Errorf("%w: at least one user is required", ErrInvalidCatalogInput)
	}
	if len(users) > maximumTeamBatchSize {
		return nil, fmt.Errorf(
			"%w: a team batch must not exceed %d users",
			ErrInvalidCatalogInput,
			maximumTeamBatchSize,
		)
	}
	var normalizedTeamID *string
	if teamID != nil {
		value := strings.TrimSpace(*teamID)
		if value != "" {
			normalizedTeamID = &value
		}
	}
	if expected.TeamID != nil {
		value := strings.TrimSpace(*expected.TeamID)
		if value == "" {
			expected.TeamID = nil
		} else {
			expected.TeamID = &value
		}
	}
	assignments := make([]TeamAssignment, 0, len(users))
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if normalizedTeamID != nil {
			var exists int
			err := transaction.GetContext(ctx, &exists, "SELECT 1 FROM teams WHERE id = ?", *normalizedTeamID)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrTeamNotFound, *normalizedTeamID)
			}
			if err != nil {
				return fmt.Errorf("validate control-plane team %s: %w", *normalizedTeamID, err)
			}
		}
		if expected.Provided {
			conflicts := make([]string, 0)
			for _, user := range users {
				var currentTeamID *string
				err := transaction.GetContext(
					ctx,
					&currentTeamID,
					"SELECT team_id FROM user_team_memberships WHERE user_email = ?",
					user,
				)
				if err != nil && !errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("read expected team assignment for %s: %w", user, err)
				}
				if errors.Is(err, sql.ErrNoRows) {
					currentTeamID = nil
				}
				if !equalOptionalStrings(currentTeamID, expected.TeamID) {
					conflicts = append(conflicts, user)
				}
			}
			if len(conflicts) > 0 {
				return &TeamMembershipConflictError{Users: conflicts}
			}
		}
		for _, user := range users {
			var current struct {
				TeamID            *string `db:"team_id"`
				MembershipVersion int64   `db:"membership_version"`
				UpdatedAt         int64   `db:"updated_at"`
			}
			err := transaction.GetContext(ctx, &current, `
                SELECT team_id, membership_version, updated_at
                  FROM user_team_memberships
                 WHERE user_email = ?`, user)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("read team assignment for %s: %w", user, err)
			}
			found := err == nil
			assignment := TeamAssignment{
				User:              user,
				TeamID:            cloneStringPointer(normalizedTeamID),
				MembershipVersion: current.MembershipVersion,
				UpdatedAt:         current.UpdatedAt,
			}
			if (found && equalOptionalStrings(current.TeamID, normalizedTeamID)) ||
				(!found && normalizedTeamID == nil) {
				assignments = append(assignments, assignment)
				continue
			}
			assignment.Changed = true
			assignment.MembershipVersion++
			assignment.UpdatedAt = store.now().Unix()
			if _, err := transaction.ExecContext(ctx, `
                INSERT INTO user_team_memberships(
                    user_email, team_id, membership_version, updated_at
                ) VALUES (?, ?, ?, ?)
                ON CONFLICT(user_email) DO UPDATE SET
                    team_id = excluded.team_id,
                    membership_version = excluded.membership_version,
                    updated_at = excluded.updated_at`,
				user,
				normalizedTeamID,
				assignment.MembershipVersion,
				assignment.UpdatedAt,
			); err != nil {
				return fmt.Errorf("write team assignment for %s: %w", user, err)
			}
			assignments = append(assignments, assignment)
		}
		return nil
	})
	return assignments, err
}

func (store *Store) KnownUsers(ctx context.Context) ([]string, error) {
	users := make([]string, 0)
	if err := store.db.SelectContext(
		ctx,
		&users,
		"SELECT DISTINCT user_email FROM key_records ORDER BY user_email",
	); err != nil {
		return nil, fmt.Errorf("list control-plane users: %w", err)
	}
	return users, nil
}

func (store *Store) UserExists(ctx context.Context, userEmail string) (bool, error) {
	user := strings.ToLower(strings.TrimSpace(userEmail))
	if user == "" {
		return false, nil
	}
	var exists int
	if err := store.db.GetContext(ctx, &exists, `
        SELECT EXISTS(
            SELECT 1
              FROM key_records
             WHERE lower(trim(user_email)) = ?
             LIMIT 1
        )`, user); err != nil {
		return false, fmt.Errorf("check control-plane user: %w", err)
	}
	return exists == 1, nil
}

// ListUserSummaries returns the complete secret-free control-plane catalog in
// one read snapshot. The Admin user page joins these rows with one batched
// usage summary before applying usage-aware sorting and pagination; raw Key
// material is never selected by this query.
func (store *Store) ListUserSummaries(ctx context.Context) ([]UserSummary, error) {
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin control-plane user summary snapshot: %w", err)
	}
	defer transaction.Rollback()
	rows := make([]UserSummary, 0)
	if err := transaction.SelectContext(ctx, &rows, userListQuery+`
SELECT email, status, active_keys, active_accounts, total_records,
       created_at, updated_at, route_account_id, team_id,
       team_membership_version, team_name, team_description
  FROM catalog
 ORDER BY email COLLATE NOCASE`); err != nil {
		return nil, fmt.Errorf("list control-plane user summaries: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit control-plane user summary snapshot: %w", err)
	}
	hydrateUserSummaryTeams(rows)
	return rows, nil
}

func (store *Store) ListUsers(ctx context.Context, options UserListOptions) (UserPage, error) {
	query := strings.ToLower(strings.TrimSpace(options.Query))
	if utf8.RuneCountInString(query) > 200 {
		return UserPage{}, fmt.Errorf("%w: user search must not exceed 200 characters", ErrInvalidCatalogInput)
	}
	teamID := strings.TrimSpace(options.TeamID)
	page := options.Page
	if page <= 0 {
		page = 1
	}
	pageSize := options.PageSize
	if pageSize == 0 {
		pageSize = 50
	}
	if pageSize != 25 && pageSize != 50 && pageSize != 100 {
		return UserPage{}, fmt.Errorf("%w: user page size must be 25, 50, or 100", ErrInvalidCatalogInput)
	}

	where, arguments := userListWhere(query, teamID)
	transaction, err := store.db.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return UserPage{}, fmt.Errorf("begin control-plane user list snapshot: %w", err)
	}
	defer transaction.Rollback()
	var total int
	if err := transaction.GetContext(
		ctx,
		&total,
		userListQuery+"SELECT COUNT(*) FROM catalog"+where,
		arguments...,
	); err != nil {
		return UserPage{}, fmt.Errorf("count control-plane users: %w", err)
	}
	totalPages := max(1, (total+pageSize-1)/pageSize)
	page = min(page, totalPages)
	rows := make([]UserSummary, 0)
	listArguments := append(append([]any{}, arguments...), pageSize, (page-1)*pageSize)
	if err := transaction.SelectContext(
		ctx,
		&rows,
		userListQuery+`
SELECT email, status, active_keys, active_accounts, total_records,
       created_at, updated_at, route_account_id, team_id,
       team_membership_version, team_name, team_description
  FROM catalog`+where+`
 ORDER BY email COLLATE NOCASE
 LIMIT ? OFFSET ?`,
		listArguments...,
	); err != nil {
		return UserPage{}, fmt.Errorf("list control-plane users: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return UserPage{}, fmt.Errorf("commit control-plane user list snapshot: %w", err)
	}
	hydrateUserSummaryTeams(rows)
	return UserPage{
		Users: rows, Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages,
	}, nil
}

func hydrateUserSummaryTeams(rows []UserSummary) {
	for index := range rows {
		if rows[index].TeamID == nil || rows[index].TeamName == nil {
			continue
		}
		rows[index].Team = &TeamSummary{
			ID:          *rows[index].TeamID,
			Name:        *rows[index].TeamName,
			Description: optionalStringValue(rows[index].TeamDescription),
		}
	}
}

func userListWhere(query string, teamID string) (string, []any) {
	clauses := make([]string, 0, 2)
	arguments := make([]any, 0, 2)
	if query != "" {
		clauses = append(clauses, "(instr(lower(email), ?) > 0 OR instr(lower(COALESCE(team_name, '')), ?) > 0)")
		arguments = append(arguments, query, query)
	}
	if teamID == "unassigned" {
		clauses = append(clauses, "team_id IS NULL")
	} else if teamID != "" {
		clauses = append(clauses, "team_id = ?")
		arguments = append(arguments, teamID)
	}
	if len(clauses) == 0 {
		return "", arguments
	}
	return " WHERE " + strings.Join(clauses, " AND "), arguments
}

func optionalStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (store *Store) ReadUserTeams(
	ctx context.Context,
	userEmails []string,
) (map[string]UserTeamClassification, error) {
	users := normalizedUsers(userEmails)
	result := make(map[string]UserTeamClassification, len(users))
	for _, user := range users {
		result[user] = UserTeamClassification{}
	}
	for offset := 0; offset < len(users); offset += 500 {
		end := min(offset+500, len(users))
		query, arguments, err := sqlx.In(`
            SELECT m.user_email, m.team_id, m.membership_version,
                   t.name AS team_name, t.description AS team_description
              FROM user_team_memberships AS m
              LEFT JOIN teams AS t ON t.id = m.team_id
             WHERE m.user_email IN (?)`, users[offset:end])
		if err != nil {
			return nil, fmt.Errorf("build control-plane team membership lookup: %w", err)
		}
		rows := make([]struct {
			UserEmail         string  `db:"user_email"`
			TeamID            *string `db:"team_id"`
			MembershipVersion int64   `db:"membership_version"`
			TeamName          *string `db:"team_name"`
			TeamDescription   *string `db:"team_description"`
		}, 0)
		if err := store.db.SelectContext(ctx, &rows, store.db.Rebind(query), arguments...); err != nil {
			return nil, fmt.Errorf("read control-plane team memberships: %w", err)
		}
		for _, row := range rows {
			classification := UserTeamClassification{
				TeamID:                cloneStringPointer(row.TeamID),
				TeamMembershipVersion: row.MembershipVersion,
			}
			if row.TeamID != nil {
				classification.Team = &TeamSummary{
					ID:          *row.TeamID,
					Name:        optionalString(row.TeamName),
					Description: optionalString(row.TeamDescription),
				}
			}
			result[row.UserEmail] = classification
		}
	}
	return result, nil
}

func (store *Store) DeleteUserClassifications(ctx context.Context, userEmail string) (int64, int64, error) {
	user := strings.ToLower(strings.TrimSpace(userEmail))
	if user == "" {
		return 0, 0, fmt.Errorf("%w: user email is required", ErrInvalidCatalogInput)
	}
	var teamCount, legacyTagCount int64
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		result, err := transaction.ExecContext(ctx, "DELETE FROM user_tags WHERE user_email = ?", user)
		if err != nil {
			return fmt.Errorf("delete legacy tag assignments for %s: %w", user, err)
		}
		legacyTagCount, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read deleted legacy tag assignment count: %w", err)
		}
		result, err = transaction.ExecContext(ctx, "DELETE FROM user_team_memberships WHERE user_email = ?", user)
		if err != nil {
			return fmt.Errorf("delete team assignment for %s: %w", user, err)
		}
		teamCount, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read deleted team assignment count: %w", err)
		}
		return nil
	})
	return teamCount, legacyTagCount, err
}

func (store *Store) ReadBrandingAsset(ctx context.Context, name string) (BrandingAsset, bool, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return BrandingAsset{}, false, fmt.Errorf("%w: branding asset name is required", ErrInvalidCatalogInput)
	}
	var asset BrandingAsset
	err := store.db.GetContext(ctx, &asset, `
        SELECT name, filename, content_type, content, sha256, updated_at
          FROM branding_assets
         WHERE name = ?`, name)
	if errors.Is(err, sql.ErrNoRows) {
		return BrandingAsset{}, false, nil
	}
	if err != nil {
		return BrandingAsset{}, false, fmt.Errorf("read control-plane branding asset %s: %w", name, err)
	}
	asset.Content = append([]byte(nil), asset.Content...)
	return asset, true, nil
}

func (store *Store) WriteBrandingAsset(
	ctx context.Context,
	name string,
	filename string,
	contentType string,
	content []byte,
) (BrandingAsset, error) {
	name = strings.TrimSpace(name)
	filename = strings.TrimSpace(filename)
	contentType = strings.TrimSpace(contentType)
	if name == "" || filename == "" || contentType == "" || len(content) == 0 {
		return BrandingAsset{}, fmt.Errorf("%w: branding asset fields and content are required", ErrInvalidCatalogInput)
	}
	digest := sha256.Sum256(content)
	asset := BrandingAsset{
		Name:        name,
		Filename:    filename,
		ContentType: contentType,
		Content:     append([]byte(nil), content...),
		SHA256:      hex.EncodeToString(digest[:]),
		UpdatedAt:   store.now().Unix(),
	}
	err := store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, `
            INSERT INTO branding_assets(name, filename, content_type, content, sha256, updated_at)
            VALUES (?, ?, ?, ?, ?, ?)
            ON CONFLICT(name) DO UPDATE SET
                filename = excluded.filename,
                content_type = excluded.content_type,
                content = excluded.content,
                sha256 = excluded.sha256,
                updated_at = excluded.updated_at`,
			asset.Name,
			asset.Filename,
			asset.ContentType,
			asset.Content,
			asset.SHA256,
			asset.UpdatedAt,
		); err != nil {
			return fmt.Errorf("write control-plane branding asset %s: %w", name, err)
		}
		return nil
	})
	return asset, err
}

func (store *Store) DeleteBrandingAsset(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: branding asset name is required", ErrInvalidCatalogInput)
	}
	return store.writeTransaction(ctx, func(transaction *sqlx.Tx) error {
		if _, err := transaction.ExecContext(ctx, "DELETE FROM branding_assets WHERE name = ?", name); err != nil {
			return fmt.Errorf("delete control-plane branding asset %s: %w", name, err)
		}
		return nil
	})
}

func (store *Store) teamByID(ctx context.Context, teamID string) (Team, error) {
	var team Team
	err := store.db.GetContext(ctx, &team, `
        SELECT t.id, t.name, t.description, COUNT(m.user_email) AS user_count,
               t.created_at, t.updated_at
          FROM teams AS t
          LEFT JOIN user_team_memberships AS m ON m.team_id = t.id
         WHERE t.id = ?
         GROUP BY t.id`, teamID)
	if errors.Is(err, sql.ErrNoRows) {
		return Team{}, fmt.Errorf("%w: %s", ErrTeamNotFound, teamID)
	}
	if err != nil {
		return Team{}, fmt.Errorf("read control-plane team %s: %w", teamID, err)
	}
	return team, nil
}

func isTeamNameConflict(err error) bool {
	return strings.Contains(err.Error(), "UNIQUE constraint failed: teams.name")
}

func equalOptionalStrings(left *string, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
