package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/identity"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/portal"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

var (
	ErrUserLifecycleUnavailable = errors.New("user lifecycle service is unavailable")
	ErrInitialPasswordMissing   = errors.New("initial portal password is not configured")
)

type UserLifecycleStore interface {
	identity.RotationStore
	ReadSecret(context.Context, string) (string, bool, error)
	UserExists(context.Context, string) (bool, error)
	KnownUsers(context.Context) ([]string, error)
	ActiveUserKey(context.Context, string) (string, error)
	ReadInternalKey(context.Context, string) (controlplane.InternalKey, bool, error)
	RestoreInternalKey(context.Context, string, *controlplane.InternalKey) error
	ApplyUserCreation(context.Context, string, string, *string) (controlplane.UserCreation, error)
	RestoreUserCreation(context.Context, controlplane.UserCreation) error
	ApplyUserRevocation(context.Context, string) (controlplane.UserRevocation, error)
	RestoreUserRevocation(context.Context, controlplane.UserRevocation) error
	ApplyUserDeletion(context.Context, string, bool) (controlplane.UserDeletion, error)
	RestoreUserDeletion(context.Context, controlplane.UserDeletion) error
}

type UserCredentialStore interface {
	Credential(context.Context, string) (usage.PortalCredential, error)
	SetCredential(context.Context, string, string, bool, string) (usage.PortalCredential, error)
	DeleteIdentity(context.Context, string) error
	DeleteUserState(context.Context, string) error
	WeeklyQuota(context.Context, string, *int64) (usage.WeeklyQuota, error)
	WeeklyQuotas(context.Context, []string, *int64) (map[string]usage.WeeklyQuota, error)
	QuotaAdjustmentHistory(context.Context, string, int) ([]usage.QuotaAdjustment, error)
	SetQuotaPolicy(context.Context, string, string, *int64, bool, string) error
	ClearQuotaPolicy(context.Context, string) error
	ApplyQuotaAction(context.Context, usage.QuotaActionRequest) (usage.QuotaActionResult, error)
}

// UserLifecycleProjection refreshes the CPA internal-Key configuration from
// authoritative control-plane state. The projection is part of the same
// compensated lifecycle operation as the control mutation and activated
// Gateway snapshot.
type UserLifecycleProjection interface {
	RefreshAccounts(context.Context) error
}

type UserLifecycleService interface {
	CreateUser(context.Context, string, *string) (UserCreateResult, error)
	RotateUserKey(context.Context, string) (identity.RotationResult, error)
	RevokeUser(context.Context, string) (UserRevokeResult, error)
	ResetUserPassword(context.Context, string) (PasswordResetResult, error)
	DeleteUser(context.Context, string, bool) (UserDeleteResult, error)
	ReadUserQuota(context.Context, string) (UserQuotaResult, error)
	ReadUserQuotas(context.Context, []string) (map[string]UserWeeklyQuota, error)
	UpdateUserQuota(context.Context, string, string, *int64) (UserQuotaResult, error)
	ClearUserQuota(context.Context, string) (UserQuotaResult, error)
	ReadQuotaOperations(context.Context) (UserQuotaOperationSummary, error)
	ApplyUserQuotaAction(context.Context, UserQuotaActionRequest) (UserQuotaActionResponse, error)
}

type UserLifecycleConfig struct {
	Store       UserLifecycleStore
	Credentials UserCredentialStore
	Projection  UserLifecycleProjection
	Snapshots   identity.SnapshotPublisher
	Lock        sync.Locker
}

type UserManager struct {
	store       UserLifecycleStore
	credentials UserCredentialStore
	projection  UserLifecycleProjection
	snapshots   identity.SnapshotPublisher
	keys        *identity.Service
	mu          sync.Locker
}

type UserCreateResult struct {
	User               string  `json:"user"`
	APIKey             string  `json:"api_key"`
	InitialPassword    string  `json:"initial_password"`
	TeamID             *string `json:"team_id"`
	Accounts           int     `json:"accounts"`
	SnapshotGeneration string  `json:"snapshot_generation"`
}

type UserRevokeResult struct {
	User               string `json:"user"`
	RevokedKeys        int    `json:"revoked_keys"`
	SnapshotGeneration string `json:"snapshot_generation"`
}

type PasswordResetResult struct {
	User                   string `json:"user"`
	InitialPassword        string `json:"initial_password"`
	PasswordChangeRequired bool   `json:"password_change_required"`
}

type UserDeleteResult struct {
	User               string `json:"user"`
	RemovedRecords     int    `json:"removed_records"`
	RevokedActiveKeys  int    `json:"revoked_active_keys"`
	SnapshotGeneration string `json:"snapshot_generation"`
}

type UserQuotaResult struct {
	User        string                  `json:"user"`
	WeeklyQuota UserWeeklyQuota         `json:"weekly_quota"`
	Adjustments []usage.QuotaAdjustment `json:"adjustments"`
}

type UserWeeklyQuota struct {
	usage.WeeklyQuota
	PersonalPolicyResetEnabled bool `json:"personal_policy_reset_enabled"`
}

func NewUserManager(config UserLifecycleConfig) (*UserManager, error) {
	if config.Store == nil || config.Credentials == nil || config.Projection == nil || config.Snapshots == nil {
		return nil, ErrUserLifecycleUnavailable
	}
	lock := config.Lock
	if lock == nil {
		lock = &sync.Mutex{}
	}
	return &UserManager{
		store: config.Store, credentials: config.Credentials, projection: config.Projection, snapshots: config.Snapshots,
		keys: &identity.Service{Store: config.Store, Snapshots: config.Snapshots}, mu: lock,
	}, nil
}

func (manager *UserManager) CreateUser(
	ctx context.Context,
	rawUser string,
	teamID *string,
) (UserCreateResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	settings, user, err := manager.settingsAndUser(ctx, rawUser)
	if err != nil {
		return UserCreateResult{}, err
	}
	initialPassword, found, err := manager.store.ReadSecret(ctx, "portal_initial_password")
	if err != nil {
		return UserCreateResult{}, fmt.Errorf("read initial portal password: %w", err)
	}
	if !found || strings.TrimSpace(initialPassword) == "" {
		return UserCreateResult{}, ErrInitialPasswordMissing
	}
	passwordHash, err := portal.HashPassword(initialPassword)
	if err != nil {
		return UserCreateResult{}, fmt.Errorf("hash initial portal password: %w", err)
	}
	apiKey, err := identity.NewUserKey(settings, user)
	if err != nil {
		return UserCreateResult{}, err
	}
	previousCredential, credentialError := manager.credentials.Credential(ctx, user)
	credentialFound := credentialError == nil
	if credentialError != nil && !errors.Is(credentialError, usage.ErrPortalCredentialNotFound) {
		return UserCreateResult{}, fmt.Errorf("read previous portal credential: %w", credentialError)
	}
	previousInternalKey, err := manager.readPreviousInternalKey(ctx, user)
	if err != nil {
		return UserCreateResult{}, fmt.Errorf("read internal Keys before user creation: %w", err)
	}
	creation, err := manager.store.ApplyUserCreation(ctx, user, apiKey, teamID)
	if err != nil {
		return UserCreateResult{}, err
	}
	if _, err := manager.credentials.SetCredential(ctx, user, passwordHash, true, ""); err != nil {
		rollbackContext, cancel := lifecycleRollbackContext(ctx)
		defer cancel()
		rollbackError := manager.store.RestoreUserCreation(rollbackContext, creation)
		return UserCreateResult{}, errors.Join(
			fmt.Errorf("create portal credential: %w", err),
			wrapLifecycleError("rollback user creation", rollbackError),
		)
	}
	if err := manager.projection.RefreshAccounts(ctx); err != nil {
		return UserCreateResult{}, manager.rollbackCreation(
			ctx, creation, user, previousCredential, credentialFound, previousInternalKey,
			fmt.Errorf("refresh user creation account projection: %w", err),
		)
	}
	snapshot, err := manager.snapshots.PublishAuthSnapshot(ctx, true)
	if err != nil {
		return UserCreateResult{}, manager.rollbackCreation(
			ctx, creation, user, previousCredential, credentialFound, previousInternalKey,
			fmt.Errorf("publish user creation snapshot: %w", err),
		)
	}
	return UserCreateResult{
		User: user, APIKey: apiKey, InitialPassword: initialPassword,
		TeamID: normalizeOptionalString(teamID), Accounts: len(creation.CreatedRows),
		SnapshotGeneration: snapshot.Generation,
	}, nil
}

func (manager *UserManager) RotateUserKey(ctx context.Context, rawUser string) (identity.RotationResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, user, err := manager.settingsAndUser(ctx, rawUser)
	if err != nil {
		return identity.RotationResult{}, err
	}
	expectedKey, err := manager.store.ActiveUserKey(ctx, user)
	if err != nil {
		return identity.RotationResult{}, err
	}
	// External API Key rotation changes only Gateway-facing Key records. CPA
	// configs contain the stable per-user internal Key, so refreshing their
	// projection here would add failure surface without changing any output.
	return manager.keys.RotateUserKey(ctx, user, expectedKey)
}

func (manager *UserManager) RevokeUser(ctx context.Context, rawUser string) (UserRevokeResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, user, err := manager.settingsAndUser(ctx, rawUser)
	if err != nil {
		return UserRevokeResult{}, err
	}
	previousInternalKey, err := manager.readPreviousInternalKey(ctx, user)
	if err != nil {
		return UserRevokeResult{}, fmt.Errorf("read internal Keys before user revocation: %w", err)
	}
	revocation, err := manager.store.ApplyUserRevocation(ctx, user)
	if err != nil {
		return UserRevokeResult{}, err
	}
	snapshot, err := manager.snapshots.PublishAuthSnapshot(ctx, true)
	if err != nil {
		return UserRevokeResult{}, manager.rollbackRevocation(
			ctx, revocation, previousInternalKey, fmt.Errorf("publish user revocation snapshot: %w", err),
		)
	}
	if err := manager.projection.RefreshAccounts(ctx); err != nil {
		return UserRevokeResult{}, manager.rollbackRevocation(
			ctx, revocation, previousInternalKey, fmt.Errorf("refresh user revocation account projection: %w", err),
		)
	}
	unique := make(map[string]struct{})
	for _, row := range revocation.Rows {
		unique[row.Secret] = struct{}{}
	}
	return UserRevokeResult{
		User: user, RevokedKeys: len(unique), SnapshotGeneration: snapshot.Generation,
	}, nil
}

func (manager *UserManager) ResetUserPassword(ctx context.Context, rawUser string) (PasswordResetResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, user, err := manager.settingsAndUser(ctx, rawUser)
	if err != nil {
		return PasswordResetResult{}, err
	}
	exists, err := manager.store.UserExists(ctx, user)
	if err != nil {
		return PasswordResetResult{}, fmt.Errorf("check password reset user: %w", err)
	}
	if !exists {
		return PasswordResetResult{}, controlplane.ErrUserLifecycleNotFound
	}
	initialPassword, found, err := manager.store.ReadSecret(ctx, "portal_initial_password")
	if err != nil {
		return PasswordResetResult{}, fmt.Errorf("read initial portal password: %w", err)
	}
	if !found || strings.TrimSpace(initialPassword) == "" {
		return PasswordResetResult{}, ErrInitialPasswordMissing
	}
	encoded, err := portal.HashPassword(initialPassword)
	if err != nil {
		return PasswordResetResult{}, fmt.Errorf("hash reset portal password: %w", err)
	}
	if _, err := manager.credentials.SetCredential(ctx, user, encoded, true, ""); err != nil {
		return PasswordResetResult{}, fmt.Errorf("reset portal credential: %w", err)
	}
	return PasswordResetResult{
		User: user, InitialPassword: initialPassword, PasswordChangeRequired: true,
	}, nil
}

func (manager *UserManager) DeleteUser(
	ctx context.Context,
	rawUser string,
	revokeActive bool,
) (UserDeleteResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	_, user, err := manager.settingsAndUser(ctx, rawUser)
	if err != nil {
		return UserDeleteResult{}, err
	}
	deletion, err := manager.store.ApplyUserDeletion(ctx, user, revokeActive)
	if err != nil {
		return UserDeleteResult{}, err
	}
	snapshot, err := manager.snapshots.PublishAuthSnapshot(ctx, true)
	if err != nil {
		return UserDeleteResult{}, manager.rollbackDeletion(
			ctx, deletion, fmt.Errorf("publish user deletion snapshot: %w", err),
		)
	}
	if err := manager.projection.RefreshAccounts(ctx); err != nil {
		return UserDeleteResult{}, manager.rollbackDeletion(
			ctx, deletion, fmt.Errorf("refresh user deletion account projection: %w", err),
		)
	}
	if err := manager.credentials.DeleteUserState(ctx, user); err != nil {
		return UserDeleteResult{}, manager.rollbackDeletion(
			ctx, deletion, fmt.Errorf("delete portal user state: %w", err),
		)
	}
	return UserDeleteResult{
		User: user, RemovedRecords: len(deletion.Rows), RevokedActiveKeys: deletion.RevokedActiveKeys,
		SnapshotGeneration: snapshot.Generation,
	}, nil
}

func (manager *UserManager) ReadUserQuota(ctx context.Context, rawUser string) (UserQuotaResult, error) {
	settings, user, err := manager.settingsAndUser(ctx, rawUser)
	if err != nil {
		return UserQuotaResult{}, err
	}
	if err := manager.requireKnownUser(ctx, user); err != nil {
		return UserQuotaResult{}, err
	}
	defaultLimit, resetOnNewWeek, err := userQuotaConfiguration(settings)
	if err != nil {
		return UserQuotaResult{}, err
	}
	return manager.readUserQuota(ctx, user, defaultLimit, resetOnNewWeek, "read user weekly quota")
}

func (manager *UserManager) ReadUserQuotas(
	ctx context.Context,
	rawUsers []string,
) (map[string]UserWeeklyQuota, error) {
	settings, err := manager.store.ReadSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read user quota catalog settings: %w", err)
	}
	defaultLimit, resetOnNewWeek, err := userQuotaConfiguration(settings)
	if err != nil {
		return nil, err
	}
	users := make([]string, 0, len(rawUsers))
	seen := make(map[string]struct{}, len(rawUsers))
	for _, rawUser := range rawUsers {
		user := strings.ToLower(strings.TrimSpace(rawUser))
		if user == "" {
			continue
		}
		if _, found := seen[user]; found {
			continue
		}
		seen[user] = struct{}{}
		users = append(users, user)
	}
	quotas, err := manager.credentials.WeeklyQuotas(ctx, users, defaultLimit)
	if err != nil {
		return nil, fmt.Errorf("read user weekly quota catalog: %w", err)
	}
	result := make(map[string]UserWeeklyQuota, len(users))
	for _, user := range users {
		quota, found := quotas[user]
		if !found {
			return nil, fmt.Errorf("read user weekly quota catalog: missing %s", user)
		}
		result[user] = UserWeeklyQuota{
			WeeklyQuota: quota, PersonalPolicyResetEnabled: resetOnNewWeek,
		}
	}
	return result, nil
}

func (manager *UserManager) UpdateUserQuota(
	ctx context.Context,
	rawUser string,
	mode string,
	weeklyTokens *int64,
) (UserQuotaResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	settings, user, err := manager.settingsAndUser(ctx, rawUser)
	if err != nil {
		return UserQuotaResult{}, err
	}
	if err := manager.requireKnownUser(ctx, user); err != nil {
		return UserQuotaResult{}, err
	}
	_, resetOnNewWeek, err := userQuotaConfiguration(settings)
	if err != nil {
		return UserQuotaResult{}, err
	}
	if err := manager.credentials.SetQuotaPolicy(
		ctx, user, mode, weeklyTokens, resetOnNewWeek, "admin",
	); err != nil {
		return UserQuotaResult{}, err
	}
	return manager.readUserQuotaWithSettings(ctx, user, settings)
}

func (manager *UserManager) ClearUserQuota(ctx context.Context, rawUser string) (UserQuotaResult, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	settings, user, err := manager.settingsAndUser(ctx, rawUser)
	if err != nil {
		return UserQuotaResult{}, err
	}
	if err := manager.requireKnownUser(ctx, user); err != nil {
		return UserQuotaResult{}, err
	}
	if err := manager.credentials.ClearQuotaPolicy(ctx, user); err != nil {
		return UserQuotaResult{}, err
	}
	return manager.readUserQuotaWithSettings(ctx, user, settings)
}

func (manager *UserManager) rollbackDeletion(
	ctx context.Context,
	deletion controlplane.UserDeletion,
	cause error,
) error {
	rollbackContext, cancel := lifecycleRollbackContext(ctx)
	defer cancel()
	controlError := manager.store.RestoreUserDeletion(rollbackContext, deletion)
	projectionError, snapshotError := manager.restorePublishedUserState(rollbackContext, controlError)
	return errors.Join(
		cause,
		wrapLifecycleError("rollback user deletion", controlError),
		wrapLifecycleError("restore user deletion account projection", projectionError),
		wrapLifecycleError("publish user deletion rollback snapshot", snapshotError),
	)
}

func (manager *UserManager) rollbackCreation(
	ctx context.Context,
	creation controlplane.UserCreation,
	user string,
	previousCredential usage.PortalCredential,
	credentialFound bool,
	previousInternalKey *controlplane.InternalKey,
	cause error,
) error {
	rollbackContext, cancel := lifecycleRollbackContext(ctx)
	defer cancel()
	controlError := manager.store.RestoreUserCreation(rollbackContext, creation)
	credentialError := manager.restoreCredential(rollbackContext, user, previousCredential, credentialFound)
	internalKeyError := manager.restoreInternalKey(rollbackContext, controlError, user, previousInternalKey)
	projectionError, snapshotError := manager.restorePublishedUserState(
		rollbackContext, errors.Join(controlError, internalKeyError),
	)
	return errors.Join(
		cause,
		wrapLifecycleError("rollback user creation", controlError),
		wrapLifecycleError("rollback portal credential", credentialError),
		wrapLifecycleError("rollback user creation internal Keys", internalKeyError),
		wrapLifecycleError("restore user creation account projection", projectionError),
		wrapLifecycleError("publish user creation rollback snapshot", snapshotError),
	)
}

func (manager *UserManager) rollbackRevocation(
	ctx context.Context,
	revocation controlplane.UserRevocation,
	previousInternalKey *controlplane.InternalKey,
	cause error,
) error {
	rollbackContext, cancel := lifecycleRollbackContext(ctx)
	defer cancel()
	controlError := manager.store.RestoreUserRevocation(rollbackContext, revocation)
	internalKeyError := manager.restoreInternalKey(rollbackContext, controlError, revocation.User, previousInternalKey)
	projectionError, snapshotError := manager.restorePublishedUserState(
		rollbackContext, errors.Join(controlError, internalKeyError),
	)
	return errors.Join(
		cause,
		wrapLifecycleError("rollback user revocation", controlError),
		wrapLifecycleError("rollback user revocation internal Keys", internalKeyError),
		wrapLifecycleError("restore user revocation account projection", projectionError),
		wrapLifecycleError("publish user revocation rollback snapshot", snapshotError),
	)
}

func (manager *UserManager) restoreInternalKey(
	ctx context.Context,
	controlError error,
	user string,
	previous *controlplane.InternalKey,
) error {
	if controlError != nil {
		return nil
	}
	return manager.store.RestoreInternalKey(ctx, user, previous)
}

func (manager *UserManager) readPreviousInternalKey(
	ctx context.Context,
	user string,
) (*controlplane.InternalKey, error) {
	key, found, err := manager.store.ReadInternalKey(ctx, user)
	if err != nil || !found {
		return nil, err
	}
	return &key, nil
}

func (manager *UserManager) restorePublishedUserState(
	ctx context.Context,
	prerequisiteError error,
) (error, error) {
	if prerequisiteError != nil {
		return nil, nil
	}
	projectionError := manager.projection.RefreshAccounts(ctx)
	if projectionError != nil {
		return projectionError, nil
	}
	_, snapshotError := manager.snapshots.PublishAuthSnapshot(ctx, true)
	return nil, snapshotError
}

func (manager *UserManager) settingsAndUser(
	ctx context.Context,
	rawUser string,
) (map[string]any, string, error) {
	settings, err := manager.store.ReadSettings(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("read user identity settings: %w", err)
	}
	user, err := identity.NormalizeUser(settings, rawUser)
	if err != nil {
		return nil, "", err
	}
	return settings, user, nil
}

func (manager *UserManager) requireKnownUser(ctx context.Context, user string) error {
	exists, err := manager.store.UserExists(ctx, user)
	if err != nil {
		return fmt.Errorf("check quota policy user: %w", err)
	}
	if !exists {
		return controlplane.ErrUserLifecycleNotFound
	}
	return nil
}

func (manager *UserManager) readUserQuotaWithSettings(
	ctx context.Context,
	user string,
	settings map[string]any,
) (UserQuotaResult, error) {
	defaultLimit, resetOnNewWeek, err := userQuotaConfiguration(settings)
	if err != nil {
		return UserQuotaResult{}, err
	}
	return manager.readUserQuota(ctx, user, defaultLimit, resetOnNewWeek, "read updated user weekly quota")
}

func (manager *UserManager) readUserQuota(
	ctx context.Context,
	user string,
	defaultLimit *int64,
	resetOnNewWeek bool,
	operation string,
) (UserQuotaResult, error) {
	quota, err := manager.credentials.WeeklyQuota(ctx, user, defaultLimit)
	if err != nil {
		return UserQuotaResult{}, fmt.Errorf("%s: %w", operation, err)
	}
	adjustments, err := manager.credentials.QuotaAdjustmentHistory(ctx, user, 20)
	if err != nil {
		return UserQuotaResult{}, fmt.Errorf("read user quota adjustments: %w", err)
	}
	return UserQuotaResult{
		User: user,
		WeeklyQuota: UserWeeklyQuota{
			WeeklyQuota: quota, PersonalPolicyResetEnabled: resetOnNewWeek,
		},
		Adjustments: adjustments,
	}, nil
}

func userQuotaConfiguration(settings map[string]any) (*int64, bool, error) {
	resetOnNewWeek := true
	if raw, found := settings["user_quota.reset_personal_weekly_on_new_week"]; found {
		value, ok := raw.(bool)
		if !ok {
			return nil, false, errors.New("user quota reset policy must be a boolean")
		}
		resetOnNewWeek = value
	}
	var defaultLimit *int64
	if raw, found := settings["user_quota.default_weekly_tokens"]; found && raw != nil {
		var value int64
		switch typed := raw.(type) {
		case int:
			value = int64(typed)
		case int64:
			value = typed
		case float64:
			value = int64(typed)
			if float64(value) != typed {
				return nil, false, errors.New("default weekly quota must be a positive integer or null")
			}
		default:
			return nil, false, errors.New("default weekly quota must be a positive integer or null")
		}
		if value <= 0 || value > 1_000_000_000_000 {
			return nil, false, errors.New("default weekly quota is outside the supported range")
		}
		defaultLimit = &value
	}
	return defaultLimit, resetOnNewWeek, nil
}

func (manager *UserManager) restoreCredential(
	ctx context.Context,
	user string,
	previous usage.PortalCredential,
	found bool,
) error {
	if !found {
		return manager.credentials.DeleteIdentity(ctx, user)
	}
	_, err := manager.credentials.SetCredential(ctx, user, previous.PasswordHash, previous.MustChange, "")
	return err
}

func lifecycleRollbackContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
}

func wrapLifecycleError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

var _ identity.SnapshotPublisher = (*failover.AuthSnapshotPublisher)(nil)
