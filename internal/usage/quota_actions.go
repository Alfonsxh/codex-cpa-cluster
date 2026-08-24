package usage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

const maxWeeklyQuotaTokens = int64(1_000_000_000_000)

var (
	ErrInvalidQuotaAction       = errors.New("invalid quota action")
	ErrQuotaActionUnlimited     = errors.New("quota action targets an unlimited user")
	ErrQuotaActionLimitExceeded = errors.New("quota action exceeds the weekly quota limit")
)

type QuotaActionRequest struct {
	Action       string
	Users        []string
	TokenAmount  int64
	Reason       string
	CreatedBy    string
	DefaultLimit *int64
}

type QuotaActionApplied struct {
	User        string `json:"user"`
	TokenAmount int64  `json:"token_amount"`
}

type QuotaActionResult struct {
	Action          string                `json:"action"`
	Applied         *[]QuotaActionApplied `json:"applied,omitempty"`
	AppliedUsers    []string              `json:"applied_users"`
	SkippedUsers    []string              `json:"skipped_users"`
	TokenAmount     *int64                `json:"token_amount,omitempty"`
	WeekStartAt     int64                 `json:"week_start_at,omitempty"`
	Reason          string                `json:"reason,omitempty"`
	CreatedAt       int64                 `json:"created_at,omitempty"`
	ChangedPolicies *int64                `json:"changed_policies,omitempty"`
}

// WeeklyQuotas returns one read-only natural-week snapshot for a bounded user
// set. It does not acquire the Writer Lease because it cannot mutate the usage
// database.
func (store *PortalStore) WeeklyQuotas(
	ctx context.Context,
	users []string,
	defaultLimit *int64,
) (map[string]WeeklyQuota, error) {
	return (&Writer{db: store.db, writeDB: store.writeDB, now: store.now}).WeeklyQuotas(
		ctx, users, defaultLimit,
	)
}

// ApplyQuotaAction performs the selected-user bulk quota mutation in one
// BEGIN IMMEDIATE transaction. Validation happens before any row is changed,
// so an unlimited user, an oversized bonus, or a malformed reason rejects the
// complete batch.
func (store *PortalStore) ApplyQuotaAction(
	ctx context.Context,
	request QuotaActionRequest,
) (QuotaActionResult, error) {
	request.Action = strings.ToLower(strings.TrimSpace(request.Action))
	request.CreatedBy = strings.TrimSpace(request.CreatedBy)
	request.Users = normalizedEmailSet(request.Users)
	if len(request.Users) == 0 || request.CreatedBy == "" {
		return QuotaActionResult{}, ErrInvalidQuotaAction
	}
	sort.Strings(request.Users)
	if request.DefaultLimit != nil && (*request.DefaultLimit <= 0 || *request.DefaultLimit > maxWeeklyQuotaTokens) {
		return QuotaActionResult{}, ErrInvalidQuotaAction
	}
	if request.Action != "restore_default" && request.Action != "add_bonus" && request.Action != "reset_usage" {
		return QuotaActionResult{}, ErrInvalidQuotaAction
	}
	if request.Action == "add_bonus" && (request.TokenAmount <= 0 || request.TokenAmount > maxWeeklyQuotaTokens) {
		return QuotaActionResult{}, ErrInvalidQuotaAction
	}
	if request.Action != "restore_default" {
		request.Reason = strings.Join(strings.Fields(request.Reason), " ")
		if request.Reason == "" || utf8.RuneCountInString(request.Reason) > 200 {
			return QuotaActionResult{}, ErrInvalidQuotaAction
		}
	}

	now := store.now().Unix()
	transaction, err := store.writeDB.BeginTxx(ctx, nil)
	if err != nil {
		return QuotaActionResult{}, fmt.Errorf("begin quota action: %w", err)
	}
	defer transaction.Rollback()
	result := QuotaActionResult{
		Action: request.Action, AppliedUsers: make([]string, 0, len(request.Users)),
		SkippedUsers: make([]string, 0),
	}
	switch request.Action {
	case "restore_default":
		changed := int64(0)
		changed, err = clearQuotaPolicies(ctx, transaction, request.Users)
		result.ChangedPolicies = &changed
		result.AppliedUsers = append(result.AppliedUsers, request.Users...)
	case "add_bonus":
		result, err = addQuotaBonus(ctx, transaction, request, now)
	case "reset_usage":
		result, err = resetQuotaUsage(ctx, transaction, request, now)
	}
	if err != nil {
		return QuotaActionResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return QuotaActionResult{}, fmt.Errorf("commit quota action: %w", err)
	}
	return result, nil
}

func clearQuotaPolicies(ctx context.Context, transaction *sqlx.Tx, users []string) (int64, error) {
	query, arguments, err := sqlx.In("DELETE FROM user_quota_policies WHERE user_email IN (?)", users)
	if err != nil {
		return 0, fmt.Errorf("build quota policy clear: %w", err)
	}
	write, err := transaction.ExecContext(ctx, transaction.Rebind(query), arguments...)
	if err != nil {
		return 0, fmt.Errorf("clear quota policies: %w", err)
	}
	changed, err := write.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("count cleared quota policies: %w", err)
	}
	return changed, nil
}

func addQuotaBonus(
	ctx context.Context,
	transaction *sqlx.Tx,
	request QuotaActionRequest,
	now int64,
) (QuotaActionResult, error) {
	location, err := loadWeekTimezone(ctx, transaction)
	if err != nil {
		return QuotaActionResult{}, err
	}
	weekStart, _ := naturalWeekBounds(now, location)
	type policyRow struct {
		UserEmail    string        `db:"user_email"`
		WeeklyTokens sql.NullInt64 `db:"weekly_tokens"`
	}
	query, arguments, err := sqlx.In(`
		SELECT user_email, weekly_tokens
		  FROM user_quota_policies
		 WHERE user_email IN (?) AND (reset_at IS NULL OR reset_at > ?)`, request.Users, now)
	if err != nil {
		return QuotaActionResult{}, fmt.Errorf("build quota action policy lookup: %w", err)
	}
	policies := make([]policyRow, 0, len(request.Users))
	if err := transaction.SelectContext(ctx, &policies, transaction.Rebind(query), arguments...); err != nil {
		return QuotaActionResult{}, fmt.Errorf("read quota action policies: %w", err)
	}
	policyByUser := make(map[string]policyRow, len(policies))
	for _, policy := range policies {
		policyByUser[policy.UserEmail] = policy
	}
	type bonusRow struct {
		UserEmail string `db:"user_email"`
		Tokens    int64  `db:"tokens"`
	}
	query, arguments, err = sqlx.In(`
		SELECT user_email, COALESCE(SUM(token_amount), 0) AS tokens
		  FROM user_quota_adjustments
		 WHERE week_start_at = ? AND action = 'bonus' AND user_email IN (?)
		 GROUP BY user_email`, weekStart, request.Users)
	if err != nil {
		return QuotaActionResult{}, fmt.Errorf("build quota bonus lookup: %w", err)
	}
	bonuses := make([]bonusRow, 0, len(request.Users))
	if err := transaction.SelectContext(ctx, &bonuses, transaction.Rebind(query), arguments...); err != nil {
		return QuotaActionResult{}, fmt.Errorf("read quota bonuses: %w", err)
	}
	bonusByUser := make(map[string]int64, len(bonuses))
	for _, bonus := range bonuses {
		bonusByUser[bonus.UserEmail] = bonus.Tokens
	}
	for _, user := range request.Users {
		baseLimit := request.DefaultLimit
		if policy, found := policyByUser[user]; found {
			if !policy.WeeklyTokens.Valid {
				return QuotaActionResult{}, fmt.Errorf("%w: %s", ErrQuotaActionUnlimited, user)
			}
			value := policy.WeeklyTokens.Int64
			baseLimit = &value
		}
		if baseLimit == nil {
			return QuotaActionResult{}, fmt.Errorf("%w: %s", ErrQuotaActionUnlimited, user)
		}
		existing := bonusByUser[user]
		if existing < 0 || existing > maxWeeklyQuotaTokens ||
			request.TokenAmount > maxWeeklyQuotaTokens-existing ||
			*baseLimit > maxWeeklyQuotaTokens-existing-request.TokenAmount {
			return QuotaActionResult{}, fmt.Errorf("%w: %s", ErrQuotaActionLimitExceeded, user)
		}
	}
	for _, user := range request.Users {
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO user_quota_adjustments(
				id, user_email, week_start_at, action, token_amount, reason, created_at, created_by
			) VALUES (?, ?, ?, 'bonus', ?, ?, ?, ?)`,
			uuid.NewString(), user, weekStart, request.TokenAmount, request.Reason, now, request.CreatedBy,
		); err != nil {
			return QuotaActionResult{}, fmt.Errorf("insert quota bonus: %w", err)
		}
	}
	tokenAmount := request.TokenAmount
	return QuotaActionResult{
		Action: "bonus", AppliedUsers: append([]string(nil), request.Users...), SkippedUsers: []string{},
		TokenAmount: &tokenAmount, WeekStartAt: weekStart, Reason: request.Reason, CreatedAt: now,
	}, nil
}

func resetQuotaUsage(
	ctx context.Context,
	transaction *sqlx.Tx,
	request QuotaActionRequest,
	now int64,
) (QuotaActionResult, error) {
	location, err := loadWeekTimezone(ctx, transaction)
	if err != nil {
		return QuotaActionResult{}, err
	}
	weekStart, _ := naturalWeekBounds(now, location)
	type usageRow struct {
		UserEmail string `db:"user_email"`
		Tokens    int64  `db:"tokens"`
	}
	query, arguments, err := sqlx.In(`
		SELECT user_email, weighted_tokens AS tokens
		  FROM user_weekly_usage
		 WHERE week_start_at = ? AND user_email IN (?)`, weekStart, request.Users)
	if err != nil {
		return QuotaActionResult{}, fmt.Errorf("build weekly usage reset lookup: %w", err)
	}
	usageRows := make([]usageRow, 0, len(request.Users))
	if err := transaction.SelectContext(ctx, &usageRows, transaction.Rebind(query), arguments...); err != nil {
		return QuotaActionResult{}, fmt.Errorf("read weekly usage for reset: %w", err)
	}
	usageByUser := make(map[string]int64, len(usageRows))
	for _, row := range usageRows {
		usageByUser[row.UserEmail] = row.Tokens
	}
	query, arguments, err = sqlx.In(`
		SELECT user_email, COALESCE(SUM(token_amount), 0) AS tokens
		  FROM user_quota_adjustments
		 WHERE week_start_at = ? AND action = 'usage_reset' AND user_email IN (?)
		 GROUP BY user_email`, weekStart, request.Users)
	if err != nil {
		return QuotaActionResult{}, fmt.Errorf("build previous usage reset lookup: %w", err)
	}
	resetRows := make([]usageRow, 0, len(request.Users))
	if err := transaction.SelectContext(ctx, &resetRows, transaction.Rebind(query), arguments...); err != nil {
		return QuotaActionResult{}, fmt.Errorf("read previous usage resets: %w", err)
	}
	resetByUser := make(map[string]int64, len(resetRows))
	for _, row := range resetRows {
		resetByUser[row.UserEmail] = row.Tokens
	}
	applied := make([]QuotaActionApplied, 0, len(request.Users))
	tokenAmount := int64(0)
	result := QuotaActionResult{
		Action: "usage_reset", Applied: &applied, TokenAmount: &tokenAmount,
		AppliedUsers: make([]string, 0, len(request.Users)), SkippedUsers: make([]string, 0),
		WeekStartAt: weekStart, Reason: request.Reason, CreatedAt: now,
	}
	for _, user := range request.Users {
		effective := max(usageByUser[user]-resetByUser[user], 0)
		if effective == 0 {
			result.SkippedUsers = append(result.SkippedUsers, user)
			continue
		}
		if _, err := transaction.ExecContext(ctx, `
			INSERT INTO user_quota_adjustments(
				id, user_email, week_start_at, action, token_amount, reason, created_at, created_by
			) VALUES (?, ?, ?, 'usage_reset', ?, ?, ?, ?)`,
			uuid.NewString(), user, weekStart, effective, request.Reason, now, request.CreatedBy,
		); err != nil {
			return QuotaActionResult{}, fmt.Errorf("insert weekly usage reset: %w", err)
		}
		*result.Applied = append(*result.Applied, QuotaActionApplied{User: user, TokenAmount: effective})
		result.AppliedUsers = append(result.AppliedUsers, user)
		*result.TokenAmount += effective
	}
	return result, nil
}
