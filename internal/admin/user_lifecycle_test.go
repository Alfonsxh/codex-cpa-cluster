package admin

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/accountprojection"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/portal"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

func TestUserManagerRunsCreateRotateResetRevokeAndDeleteLifecycle(t *testing.T) {
	store := newUserLifecycleStore(t)
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	publisher := &lifecyclePublisherFake{}
	projection := &lifecycleProjectionFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}
	ctx := context.Background()
	created, err := manager.CreateUser(ctx, "Alice@Example.com", nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if created.User != "alice@example.com" || created.InitialPassword != "initial-password" ||
		created.Accounts != 2 || !strings.HasPrefix(created.APIKey, "custom_alice_") {
		t.Fatalf("created user = %#v", created)
	}
	credential := credentials.values[created.User]
	if !credential.MustChange || !portal.VerifyPassword("initial-password", credential.PasswordHash) {
		t.Fatalf("created credential = %#v", credential)
	}
	activeKey, err := store.ActiveUserKey(ctx, created.User)
	if err != nil || activeKey != created.APIKey {
		t.Fatalf("active created key = (%q, %v)", activeKey, err)
	}
	if projection.calls != 1 {
		t.Fatalf("creation projection refreshes = %d, want 1", projection.calls)
	}

	rotated, err := manager.RotateUserKey(ctx, created.User)
	if err != nil || rotated.APIKey == created.APIKey || !strings.HasPrefix(rotated.APIKey, "custom_alice_") {
		t.Fatalf("RotateUserKey = (%#v, %v)", rotated, err)
	}
	activeKey, err = store.ActiveUserKey(ctx, created.User)
	if err != nil || activeKey != rotated.APIKey {
		t.Fatalf("active rotated key = (%q, %v)", activeKey, err)
	}
	if projection.calls != 1 {
		t.Fatalf("external Key rotation unexpectedly refreshed CPA projection %d times", projection.calls)
	}
	reset, err := manager.ResetUserPassword(ctx, created.User)
	if err != nil || reset.InitialPassword != "initial-password" || !reset.PasswordChangeRequired {
		t.Fatalf("ResetUserPassword = (%#v, %v)", reset, err)
	}
	revoked, err := manager.RevokeUser(ctx, created.User)
	if err != nil || revoked.RevokedKeys != 1 {
		t.Fatalf("RevokeUser = (%#v, %v)", revoked, err)
	}
	deleted, err := manager.DeleteUser(ctx, created.User, false)
	if err != nil || deleted.RemovedRecords != 4 || deleted.RevokedActiveKeys != 0 {
		t.Fatalf("DeleteUser = (%#v, %v)", deleted, err)
	}
	if _, found := credentials.values[created.User]; found || credentials.deletedUsers != 1 {
		t.Fatalf("credentials after delete = %#v", credentials)
	}
	if exists, err := store.UserExists(ctx, created.User); err != nil || exists {
		t.Fatalf("deleted user exists = (%v, %v)", exists, err)
	}
	if publisher.calls != 4 {
		t.Fatalf("snapshot publishes = %d, want 4", publisher.calls)
	}
	if projection.calls != 3 {
		t.Fatalf("lifecycle projection refreshes = %d, want create/revoke/delete", projection.calls)
	}
}

func TestUserManagerRollsBackCreateAndCredentialWhenSnapshotActivationFails(t *testing.T) {
	store := newUserLifecycleStore(t)
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	publisher := &lifecyclePublisherFake{failCalls: map[int]error{1: errors.New("activation failed")}}
	projection := &lifecycleProjectionFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}
	_, err = manager.CreateUser(context.Background(), "alice@example.com", nil)
	if err == nil || !strings.Contains(err.Error(), "publish user creation snapshot") {
		t.Fatalf("CreateUser activation error = %v", err)
	}
	if exists, readError := store.UserExists(context.Background(), "alice@example.com"); readError != nil || exists {
		t.Fatalf("rolled back user exists = (%v, %v)", exists, readError)
	}
	if _, found := credentials.values["alice@example.com"]; found {
		t.Fatalf("rolled back credential remains = %#v", credentials.values)
	}
	if publisher.calls != 2 {
		t.Fatalf("snapshot publishes = %d, want activation plus rollback", publisher.calls)
	}
	if projection.calls != 2 {
		t.Fatalf("projection refreshes = %d, want activation plus rollback", projection.calls)
	}
}

func TestUserManagerRollsBackCreateCredentialProjectionAndSnapshotWhenProjectionFails(t *testing.T) {
	store := newUserLifecycleStore(t)
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	projection := &lifecycleProjectionFake{failCalls: map[int]error{1: errors.New("projection write failed")}}
	publisher := &lifecyclePublisherFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}

	_, err = manager.CreateUser(context.Background(), "alice@example.com", nil)
	if err == nil || !strings.Contains(err.Error(), "refresh user creation account projection") {
		t.Fatalf("CreateUser projection error = %v", err)
	}
	if exists, readError := store.UserExists(context.Background(), "alice@example.com"); readError != nil || exists {
		t.Fatalf("rolled back projected user exists = (%v, %v)", exists, readError)
	}
	if _, found := credentials.values["alice@example.com"]; found {
		t.Fatalf("rolled back projected credential remains = %#v", credentials.values)
	}
	if projection.calls != 2 || publisher.calls != 1 {
		t.Fatalf("projection/snapshot rollback calls = (%d, %d), want (2, 1)", projection.calls, publisher.calls)
	}
}

func TestUserManagerDoesNotRepublishGatewayWhenRollbackProjectionCannotBeRestored(t *testing.T) {
	store := newUserLifecycleStore(t)
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	projection := &lifecycleProjectionFake{failCalls: map[int]error{
		1: errors.New("new projection failed"),
		2: errors.New("rollback projection failed"),
	}}
	publisher := &lifecyclePublisherFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}

	_, err = manager.CreateUser(context.Background(), "alice@example.com", nil)
	if err == nil || !strings.Contains(err.Error(), "rollback projection failed") {
		t.Fatalf("CreateUser rollback projection error = %v", err)
	}
	if publisher.calls != 0 {
		t.Fatalf("Gateway snapshot published without a restored CPA projection %d times", publisher.calls)
	}
}

func TestUserManagerDoesNotRestoreProjectionOrGatewayWhenControlRollbackFails(t *testing.T) {
	baseStore := newUserLifecycleStore(t)
	store := &lifecycleStoreFailure{
		Store: baseStore, restoreCreationError: errors.New("control rollback failed"),
	}
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	projection := &lifecycleProjectionFake{failCalls: map[int]error{1: errors.New("new projection failed")}}
	publisher := &lifecyclePublisherFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}

	_, err = manager.CreateUser(context.Background(), "alice@example.com", nil)
	if err == nil || !strings.Contains(err.Error(), "control rollback failed") {
		t.Fatalf("CreateUser control rollback error = %v", err)
	}
	if projection.calls != 1 || publisher.calls != 0 {
		t.Fatalf(
			"unsafe control rollback side effects = projection %d, snapshot %d; want (1, 0)",
			projection.calls, publisher.calls,
		)
	}
}

func TestUserManagerDoesNotRestoreProjectionOrGatewayWhenInternalKeyRollbackFails(t *testing.T) {
	baseStore := newUserLifecycleStore(t)
	store := &lifecycleStoreFailure{
		Store: baseStore, restoreInternalKeyError: errors.New("internal Key rollback failed"),
	}
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	projection := &lifecycleProjectionFake{failCalls: map[int]error{1: errors.New("new projection failed")}}
	publisher := &lifecyclePublisherFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}

	_, err = manager.CreateUser(context.Background(), "alice@example.com", nil)
	if err == nil || !strings.Contains(err.Error(), "internal Key rollback failed") {
		t.Fatalf("CreateUser internal Key rollback error = %v", err)
	}
	if projection.calls != 1 || publisher.calls != 0 {
		t.Fatalf(
			"unsafe internal Key rollback side effects = projection %d, snapshot %d; want (1, 0)",
			projection.calls, publisher.calls,
		)
	}
}

func TestUserManagerTargetedInternalKeyRollbackPreservesConcurrentUser(t *testing.T) {
	store := newUserLifecycleStore(t)
	ctx := context.Background()
	originalBob := controlplane.InternalKey{
		Key: "cpa_internal_bob_original", CreatedAt: 10, Status: "active",
	}
	if err := store.RestoreInternalKey(ctx, "bob@example.com", &originalBob); err != nil {
		t.Fatalf("seed Bob internal Key: %v", err)
	}
	concurrentBob := controlplane.InternalKey{
		Key: "cpa_internal_bob_concurrent", CreatedAt: 20, Status: "active",
	}
	refreshCalls := 0
	projection := &lifecycleProjectionFake{
		refresh: func(ctx context.Context) error {
			refreshCalls++
			if refreshCalls == 1 {
				return store.RestoreInternalKey(ctx, "bob@example.com", &concurrentBob)
			}
			return nil
		},
		failCalls: map[int]error{1: errors.New("new projection failed")},
	}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store:       store,
		Credentials: &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)},
		Projection:  projection,
		Snapshots:   &lifecyclePublisherFake{},
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}

	if _, err := manager.CreateUser(ctx, "alice@example.com", nil); err == nil {
		t.Fatal("CreateUser unexpectedly accepted failed projection")
	}
	bob, found, err := store.ReadInternalKey(ctx, "bob@example.com")
	if err != nil || !found || bob != concurrentBob {
		t.Fatalf("concurrent Bob internal Key = (%#v, %v, %v)", bob, found, err)
	}
	if _, found, err := store.ReadInternalKey(ctx, "alice@example.com"); err != nil || found {
		t.Fatalf("rolled back Alice internal Key remains = (%v, %v)", found, err)
	}
}

func TestUserManagerProjectsStableCPAInternalKeysAcrossCreateRotateRevokeAndDelete(t *testing.T) {
	root := t.TempDir()
	store := newUserLifecycleStoreAt(t, root)
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	renderer := &accountprojection.Renderer{Root: root, Store: store}
	projection := &lifecycleProjectionFake{refresh: func(ctx context.Context) error {
		_, err := renderer.Render(ctx)
		return err
	}}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: &lifecyclePublisherFake{},
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}
	ctx := context.Background()
	created, err := manager.CreateUser(ctx, "alice@example.com", nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	configPath := filepath.Join(root, "configs", "alpha", "config.yaml")
	createdConfig, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(createdConfig), "cpa_internal_") {
		t.Fatalf("created CPA config = (%q, %v)", createdConfig, err)
	}
	createdKeys, err := store.ReadInternalKeys(ctx)
	if err != nil || createdKeys[created.User].Status != "active" ||
		!strings.HasPrefix(createdKeys[created.User].Key, "cpa_internal_") {
		t.Fatalf("created internal Keys = (%#v, %v)", createdKeys, err)
	}

	if _, err := manager.RotateUserKey(ctx, created.User); err != nil {
		t.Fatalf("RotateUserKey: %v", err)
	}
	rotatedConfig, err := os.ReadFile(configPath)
	if err != nil || string(rotatedConfig) != string(createdConfig) {
		t.Fatalf("external Key rotation rewrote CPA config = (%q, %v)", rotatedConfig, err)
	}

	if _, err := manager.RevokeUser(ctx, created.User); err != nil {
		t.Fatalf("RevokeUser: %v", err)
	}
	revokedConfig, err := os.ReadFile(configPath)
	if err != nil || strings.Contains(string(revokedConfig), "cpa_internal_") {
		t.Fatalf("revoked CPA config = (%q, %v)", revokedConfig, err)
	}
	revokedKeys, err := store.ReadInternalKeys(ctx)
	if err != nil || revokedKeys[created.User].Status != "inactive" ||
		revokedKeys[created.User].Key != createdKeys[created.User].Key {
		t.Fatalf("revoked internal Keys = (%#v, %v)", revokedKeys, err)
	}

	if _, err := manager.DeleteUser(ctx, created.User, false); err != nil {
		t.Fatalf("DeleteUser: %v", err)
	}
	deletedKeys, err := store.ReadInternalKeys(ctx)
	if err != nil || len(deletedKeys) != 0 {
		t.Fatalf("deleted internal Keys = (%#v, %v)", deletedKeys, err)
	}
}

func TestUserManagerExactlyRestoresInternalKeysAfterPartiallyAppliedProjectionFailure(t *testing.T) {
	root := t.TempDir()
	store := newUserLifecycleStoreAt(t, root)
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	renderer := &accountprojection.Renderer{Root: root, Store: store}
	projection := &lifecycleProjectionFake{
		refresh: func(ctx context.Context) error {
			_, err := renderer.Render(ctx)
			return err
		},
		failCalls: map[int]error{1: errors.New("failure after projection replacement")},
	}
	publisher := &lifecyclePublisherFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}

	_, err = manager.CreateUser(context.Background(), "alice@example.com", nil)
	if err == nil || !strings.Contains(err.Error(), "refresh user creation account projection") {
		t.Fatalf("CreateUser partial projection error = %v", err)
	}
	keys, readError := store.ReadInternalKeys(context.Background())
	if readError != nil || len(keys) != 0 {
		t.Fatalf("partial projection rollback internal Keys = (%#v, %v)", keys, readError)
	}
	config, readError := os.ReadFile(filepath.Join(root, "configs", "alpha", "config.yaml"))
	if readError != nil || strings.Contains(string(config), "cpa_internal_") {
		t.Fatalf("partial projection rollback config = (%q, %v)", config, readError)
	}
	if projection.calls != 2 || publisher.calls != 1 {
		t.Fatalf("partial projection rollback calls = (%d, %d), want (2, 1)", projection.calls, publisher.calls)
	}
}

func TestUserManagerRollsBackRevocationProjectionAndSnapshotWhenProjectionFails(t *testing.T) {
	store := newUserLifecycleStore(t)
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	projection := &lifecycleProjectionFake{}
	publisher := &lifecyclePublisherFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}
	created, err := manager.CreateUser(context.Background(), "alice@example.com", nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	projection.failCalls = map[int]error{2: errors.New("projection write failed")}

	_, err = manager.RevokeUser(context.Background(), created.User)
	if err == nil || !strings.Contains(err.Error(), "refresh user revocation account projection") {
		t.Fatalf("RevokeUser projection error = %v", err)
	}
	activeKey, readError := store.ActiveUserKey(context.Background(), created.User)
	if readError != nil || activeKey != created.APIKey {
		t.Fatalf("restored revoked user key = (%q, %v)", activeKey, readError)
	}
	if projection.calls != 3 || publisher.calls != 3 {
		t.Fatalf("revocation projection/snapshot calls = (%d, %d), want (3, 3)", projection.calls, publisher.calls)
	}
}

func TestUserManagerRollsBackDeletionProjectionAndSnapshotWhenProjectionFails(t *testing.T) {
	store := newUserLifecycleStore(t)
	credentials := &lifecycleCredentialFake{values: make(map[string]usage.PortalCredential)}
	projection := &lifecycleProjectionFake{}
	publisher := &lifecyclePublisherFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}
	created, err := manager.CreateUser(context.Background(), "alice@example.com", nil)
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	projection.failCalls = map[int]error{2: errors.New("projection write failed")}

	_, err = manager.DeleteUser(context.Background(), created.User, true)
	if err == nil || !strings.Contains(err.Error(), "refresh user deletion account projection") {
		t.Fatalf("DeleteUser projection error = %v", err)
	}
	activeKey, readError := store.ActiveUserKey(context.Background(), created.User)
	if readError != nil || activeKey != created.APIKey {
		t.Fatalf("restored deleted user key = (%q, %v)", activeKey, readError)
	}
	if _, found := credentials.values[created.User]; !found || credentials.deletedUsers != 0 {
		t.Fatalf("deletion projection failure changed credentials = %#v", credentials)
	}
	if projection.calls != 3 || publisher.calls != 3 {
		t.Fatalf("deletion projection/snapshot calls = (%d, %d), want (3, 3)", projection.calls, publisher.calls)
	}
}

func TestUserManagerRollsBackDeletionWhenPortalCleanupFails(t *testing.T) {
	store := newUserLifecycleStore(t)
	ctx := context.Background()
	if err := store.WriteKeyRecords(ctx, []controlplane.KeyRecord{{
		Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com",
		User: "alice@example.com", Status: "active", Key: "shared-key", CreatedAt: 1, UpdatedAt: 2,
	}}); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	credentials := &lifecycleCredentialFake{
		values:          map[string]usage.PortalCredential{"alice@example.com": {PasswordHash: "scrypt$fixture", MustChange: true}},
		deleteUserError: errors.New("usage database busy"),
	}
	publisher := &lifecyclePublisherFake{}
	projection := &lifecycleProjectionFake{}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: projection, Snapshots: publisher,
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}
	_, err = manager.DeleteUser(ctx, "alice@example.com", true)
	if err == nil || !strings.Contains(err.Error(), "delete portal user state") {
		t.Fatalf("DeleteUser cleanup error = %v", err)
	}
	key, readError := store.ActiveUserKey(ctx, "alice@example.com")
	if readError != nil || key != "shared-key" {
		t.Fatalf("restored active key = (%q, %v)", key, readError)
	}
	if publisher.calls != 2 {
		t.Fatalf("snapshot publishes = %d, want delete plus rollback", publisher.calls)
	}
	if projection.calls != 2 {
		t.Fatalf("projection refreshes = %d, want delete plus rollback", projection.calls)
	}
}

func TestUserManagerReadsUpdatesAndClearsFineGrainedWeeklyQuota(t *testing.T) {
	store := newUserLifecycleStore(t)
	ctx := context.Background()
	if err := store.WriteKeyRecords(ctx, []controlplane.KeyRecord{{
		Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com",
		User: "alice@example.com", Status: "active", Key: "shared-key", CreatedAt: 1, UpdatedAt: 2,
	}}); err != nil {
		t.Fatalf("seed quota user: %v", err)
	}
	credentials := &lifecycleCredentialFake{
		values: make(map[string]usage.PortalCredential),
		quotas: make(map[string]usage.WeeklyQuota),
	}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: &lifecycleProjectionFake{},
		Snapshots: &lifecyclePublisherFake{},
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}

	inherited, err := manager.ReadUserQuota(ctx, "Alice@Example.com")
	if err != nil || inherited.User != "alice@example.com" ||
		inherited.WeeklyQuota.PolicyMode != "inherit" || inherited.WeeklyQuota.LimitTokens == nil ||
		*inherited.WeeklyQuota.LimitTokens != 1_000 || !inherited.WeeklyQuota.PersonalPolicyResetEnabled {
		t.Fatalf("inherited quota = (%#v, %v)", inherited, err)
	}
	catalog, err := manager.ReadUserQuotas(ctx, []string{"alice@example.com", "ALICE@example.com"})
	if err != nil || len(catalog) != 1 || catalog["alice@example.com"].LimitTokens == nil ||
		*catalog["alice@example.com"].LimitTokens != 1_000 ||
		!catalog["alice@example.com"].PersonalPolicyResetEnabled {
		t.Fatalf("quota catalog = (%#v, %v)", catalog, err)
	}
	customTokens := int64(500)
	custom, err := manager.UpdateUserQuota(ctx, "alice@example.com", "custom", &customTokens)
	if err != nil || custom.WeeklyQuota.PolicyMode != "custom" || custom.WeeklyQuota.LimitTokens == nil ||
		*custom.WeeklyQuota.LimitTokens != 500 {
		t.Fatalf("custom quota = (%#v, %v)", custom, err)
	}
	cleared, err := manager.ClearUserQuota(ctx, "alice@example.com")
	if err != nil || cleared.WeeklyQuota.PolicyMode != "inherit" || cleared.WeeklyQuota.LimitTokens == nil ||
		*cleared.WeeklyQuota.LimitTokens != 1_000 {
		t.Fatalf("cleared quota = (%#v, %v)", cleared, err)
	}
	if _, err := manager.ReadUserQuota(ctx, "missing@example.com"); !errors.Is(err, controlplane.ErrUserLifecycleNotFound) {
		t.Fatalf("missing user quota error = %v", err)
	}
}

func TestUserManagerAppliesBulkQuotaActionsAndReturnsFreshSummary(t *testing.T) {
	store := newUserLifecycleStore(t)
	ctx := context.Background()
	if err := store.WriteKeyRecords(ctx, []controlplane.KeyRecord{
		{Label: "alice@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com", User: "alice@example.com", Status: "active", Key: "alice-key", CreatedAt: 1, UpdatedAt: 2},
		{Label: "bob@example.com:alpha", Account: "alpha", AccountEmail: "alpha@example.com", User: "bob@example.com", Status: "active", Key: "bob-key", CreatedAt: 1, UpdatedAt: 2},
	}); err != nil {
		t.Fatalf("seed quota action users: %v", err)
	}
	limit := int64(1_000)
	credentials := &lifecycleCredentialFake{
		values: make(map[string]usage.PortalCredential),
		quotas: map[string]usage.WeeklyQuota{
			"alice@example.com": {PolicyMode: "custom", LimitTokens: &limit, UsedTokens: 300, RawUsedTokens: 150, BonusTokens: 200, WeekStartAt: 100, WeekEndAt: 200},
			"bob@example.com":   {PolicyMode: "inherit", LimitTokens: &limit, UsedTokens: 0, RawUsedTokens: 0, UsageResetTokens: 300, WeekStartAt: 100, WeekEndAt: 200},
		},
	}
	manager, err := NewUserManager(UserLifecycleConfig{
		Store: store, Credentials: credentials, Projection: &lifecycleProjectionFake{},
		Snapshots: &lifecyclePublisherFake{},
	})
	if err != nil {
		t.Fatalf("NewUserManager: %v", err)
	}
	before, err := manager.ReadQuotaOperations(ctx)
	if err != nil || before.TotalUsers != 2 || before.UsersWithUsage != 1 ||
		before.TotalUsedTokens != 300 || before.TotalRawUsedTokens != 150 {
		t.Fatalf("quota operation summary = (%#v, %v)", before, err)
	}
	result, err := manager.ApplyUserQuotaAction(ctx, UserQuotaActionRequest{
		Action: "add_bonus", Scope: "selected",
		Users: []string{"Alice@Example.com", "alice@example.com"}, TokenAmount: 200,
		Reason: "temporary capacity",
	})
	if err != nil || credentials.quotaActionCalls != 1 ||
		len(credentials.quotaAction.Users) != 1 || credentials.quotaAction.Users[0] != "alice@example.com" ||
		credentials.quotaAction.DefaultLimit == nil || *credentials.quotaAction.DefaultLimit != 1_000 ||
		result.QuotaOperations.TotalUsers != 2 || result.QuotaOperations.UsersWithUsage != 1 ||
		result.QuotaOperations.TotalUsedTokens != 300 || result.QuotaOperations.TotalRawUsedTokens != 150 ||
		result.QuotaOperations.UsersWithPersonalPolicy != 1 || result.QuotaOperations.UsersWithBonus != 1 ||
		result.QuotaOperations.UsersWithUsageReset != 1 {
		t.Fatalf("bulk quota action = (%#v, %v), credentials=%#v", result, err, credentials)
	}
	if _, err := manager.ApplyUserQuotaAction(ctx, UserQuotaActionRequest{
		Action: "restore_default", Scope: "all", Users: nil,
	}); !errors.Is(err, usage.ErrInvalidQuotaAction) {
		t.Fatalf("unsafe all-user restore error = %v", err)
	}
}

func newUserLifecycleStore(t *testing.T) *controlplane.Store {
	t.Helper()
	return newUserLifecycleStoreAt(t, t.TempDir())
}

func newUserLifecycleStoreAt(t *testing.T, root string) *controlplane.Store {
	t.Helper()
	store, err := controlplane.Open(context.Background(), root, controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{
		{ID: "alpha", Email: "alpha@example.com", Port: 18318, GroupEnabled: true},
		{ID: "beta", Email: "beta@example.com", Port: 18319, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("seed lifecycle accounts: %v", err)
	}
	if err := store.WriteSettings(ctx, map[string]any{
		"identity.allowed_email_domains":               []string{"example.com"},
		"identity.key_prefix":                          "custom_",
		"user_quota.default_weekly_tokens":             int64(1_000),
		"user_quota.reset_personal_weekly_on_new_week": true,
	}); err != nil {
		t.Fatalf("seed lifecycle settings: %v", err)
	}
	if err := store.WriteSecret(ctx, "portal_initial_password", "initial-password"); err != nil {
		t.Fatalf("seed initial password: %v", err)
	}
	if err := store.WriteSecret(ctx, "cpa_management_key", "management-key-for-tests"); err != nil {
		t.Fatalf("seed CPA management key: %v", err)
	}
	return store
}

type lifecycleCredentialFake struct {
	values           map[string]usage.PortalCredential
	quotas           map[string]usage.WeeklyQuota
	deletedUsers     int
	deleteUserError  error
	quotaActionCalls int
	quotaAction      usage.QuotaActionRequest
}

type lifecycleStoreFailure struct {
	*controlplane.Store
	restoreCreationError    error
	restoreInternalKeyError error
}

func (store *lifecycleStoreFailure) RestoreUserCreation(
	ctx context.Context,
	creation controlplane.UserCreation,
) error {
	if store.restoreCreationError != nil {
		return store.restoreCreationError
	}
	return store.Store.RestoreUserCreation(ctx, creation)
}

func (store *lifecycleStoreFailure) RestoreInternalKey(
	ctx context.Context,
	user string,
	previous *controlplane.InternalKey,
) error {
	if store.restoreInternalKeyError != nil {
		return store.restoreInternalKeyError
	}
	return store.Store.RestoreInternalKey(ctx, user, previous)
}

func (store *lifecycleCredentialFake) Credential(_ context.Context, user string) (usage.PortalCredential, error) {
	credential, found := store.values[user]
	if !found {
		return usage.PortalCredential{}, usage.ErrPortalCredentialNotFound
	}
	return credential, nil
}

func (store *lifecycleCredentialFake) SetCredential(
	_ context.Context,
	user string,
	passwordHash string,
	mustChange bool,
	_ string,
) (usage.PortalCredential, error) {
	credential := usage.PortalCredential{PasswordHash: passwordHash, MustChange: mustChange}
	store.values[user] = credential
	return credential, nil
}

func (store *lifecycleCredentialFake) DeleteIdentity(_ context.Context, user string) error {
	delete(store.values, user)
	return nil
}

func (store *lifecycleCredentialFake) DeleteUserState(_ context.Context, user string) error {
	if store.deleteUserError != nil {
		return store.deleteUserError
	}
	delete(store.values, user)
	store.deletedUsers++
	return nil
}

func (store *lifecycleCredentialFake) WeeklyQuota(
	_ context.Context,
	user string,
	defaultWeeklyTokens *int64,
) (usage.WeeklyQuota, error) {
	if quota, found := store.quotas[user]; found {
		return quota, nil
	}
	return usage.WeeklyQuota{
		PolicyMode: "inherit", LimitTokens: defaultWeeklyTokens,
		DefaultLimitTokens: defaultWeeklyTokens, QuotaUnit: "weighted_tokens",
	}, nil
}

func (store *lifecycleCredentialFake) WeeklyQuotas(
	_ context.Context,
	users []string,
	defaultWeeklyTokens *int64,
) (map[string]usage.WeeklyQuota, error) {
	result := make(map[string]usage.WeeklyQuota, len(users))
	for _, user := range users {
		quota, err := store.WeeklyQuota(context.Background(), user, defaultWeeklyTokens)
		if err != nil {
			return nil, err
		}
		result[user] = quota
	}
	return result, nil
}

func (store *lifecycleCredentialFake) QuotaAdjustmentHistory(
	_ context.Context,
	_ string,
	_ int,
) ([]usage.QuotaAdjustment, error) {
	return []usage.QuotaAdjustment{}, nil
}

func (store *lifecycleCredentialFake) SetQuotaPolicy(
	_ context.Context,
	user string,
	mode string,
	weeklyTokens *int64,
	_ bool,
	_ string,
) error {
	if store.quotas == nil {
		store.quotas = make(map[string]usage.WeeklyQuota)
	}
	store.quotas[user] = usage.WeeklyQuota{
		PolicyMode: mode, PolicyTokens: weeklyTokens, LimitTokens: weeklyTokens,
		Unlimited: mode == "unlimited", QuotaUnit: "weighted_tokens",
	}
	return nil
}

func (store *lifecycleCredentialFake) ClearQuotaPolicy(_ context.Context, user string) error {
	delete(store.quotas, user)
	return nil
}

func (store *lifecycleCredentialFake) ApplyQuotaAction(
	_ context.Context,
	request usage.QuotaActionRequest,
) (usage.QuotaActionResult, error) {
	store.quotaActionCalls++
	store.quotaAction = request
	return usage.QuotaActionResult{
		Action: request.Action, AppliedUsers: append([]string(nil), request.Users...), SkippedUsers: []string{},
		TokenAmount: func() *int64 { value := request.TokenAmount; return &value }(),
	}, nil
}

type lifecyclePublisherFake struct {
	calls     int
	failCalls map[int]error
}

type lifecycleProjectionFake struct {
	calls     int
	failCalls map[int]error
	refresh   func(context.Context) error
}

func (projection *lifecycleProjectionFake) RefreshAccounts(ctx context.Context) error {
	projection.calls++
	var refreshError error
	if projection.refresh != nil {
		refreshError = projection.refresh(ctx)
	}
	return errors.Join(refreshError, projection.failCalls[projection.calls])
}

func (publisher *lifecyclePublisherFake) PublishAuthSnapshot(
	_ context.Context,
	wait bool,
) (failover.Snapshot, error) {
	publisher.calls++
	if !wait {
		return failover.Snapshot{}, errors.New("lifecycle snapshot must wait for activation")
	}
	if err := publisher.failCalls[publisher.calls]; err != nil {
		return failover.Snapshot{}, err
	}
	return failover.Snapshot{Generation: "generation-test"}, nil
}
