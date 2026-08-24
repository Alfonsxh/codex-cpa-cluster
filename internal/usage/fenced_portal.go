package usage

import (
	"context"
	"errors"
	"time"
)

type WriteFence interface {
	WithWriteFence(context.Context, func() error) error
}

// FencedPortalStore applies the Admin runtime/worker lease to every mutable
// portal/session/quota operation in usage.sqlite3. Read-only queries do not
// hold the cross-process control lock.
type FencedPortalStore struct {
	store *PortalStore
	fence WriteFence
}

func NewFencedPortalStore(store *PortalStore, fence WriteFence) (*FencedPortalStore, error) {
	if store == nil || fence == nil {
		return nil, errors.New("fenced portal store requires a store and ownership fence")
	}
	return &FencedPortalStore{store: store, fence: fence}, nil
}

func (store *FencedPortalStore) CreateSession(
	ctx context.Context,
	user string,
	ttl time.Duration,
) (string, PortalSession, error) {
	type result struct {
		token   string
		session PortalSession
	}
	value, err := withPortalFence(ctx, store.fence, func() (result, error) {
		token, session, err := store.store.CreateSession(ctx, user, ttl)
		return result{token: token, session: session}, err
	})
	return value.token, value.session, err
}

func (store *FencedPortalStore) ResolveSession(ctx context.Context, token string) (PortalSession, error) {
	return store.store.ResolveSession(ctx, token)
}

func (store *FencedPortalStore) SyncUserTeams(
	ctx context.Context,
	classifications map[string]TeamIdentity,
) (int, error) {
	return withPortalFence(ctx, store.fence, func() (int, error) {
		return store.store.SyncUserTeams(ctx, classifications)
	})
}

func (store *FencedPortalStore) RevokeSession(ctx context.Context, token string) error {
	return store.fence.WithWriteFence(ctx, func() error {
		return store.store.RevokeSession(ctx, token)
	})
}

func (store *FencedPortalStore) Credential(ctx context.Context, user string) (PortalCredential, error) {
	return store.store.Credential(ctx, user)
}

func (store *FencedPortalStore) SetCredential(
	ctx context.Context,
	user string,
	passwordHash string,
	mustChange bool,
	keepSessionToken string,
) (PortalCredential, error) {
	return withPortalFence(ctx, store.fence, func() (PortalCredential, error) {
		return store.store.SetCredential(ctx, user, passwordHash, mustChange, keepSessionToken)
	})
}

func (store *FencedPortalStore) DeleteIdentity(ctx context.Context, user string) error {
	return store.fence.WithWriteFence(ctx, func() error {
		return store.store.DeleteIdentity(ctx, user)
	})
}

func (store *FencedPortalStore) DeleteUserState(ctx context.Context, user string) error {
	return store.fence.WithWriteFence(ctx, func() error {
		return store.store.DeleteUserState(ctx, user)
	})
}

func (store *FencedPortalStore) WeeklyQuota(
	ctx context.Context,
	user string,
	defaultLimit *int64,
) (WeeklyQuota, error) {
	return store.store.WeeklyQuota(ctx, user, defaultLimit)
}

func (store *FencedPortalStore) WeeklyQuotas(
	ctx context.Context,
	users []string,
	defaultLimit *int64,
) (map[string]WeeklyQuota, error) {
	return store.store.WeeklyQuotas(ctx, users, defaultLimit)
}

func (store *FencedPortalStore) QuotaAdjustmentHistory(
	ctx context.Context,
	user string,
	limit int,
) ([]QuotaAdjustment, error) {
	return store.store.QuotaAdjustmentHistory(ctx, user, limit)
}

func (store *FencedPortalStore) SetQuotaPolicy(
	ctx context.Context,
	user string,
	mode string,
	weeklyTokens *int64,
	resetOnNewWeek bool,
	createdBy string,
) error {
	return store.fence.WithWriteFence(ctx, func() error {
		return store.store.SetQuotaPolicy(ctx, user, mode, weeklyTokens, resetOnNewWeek, createdBy)
	})
}

func (store *FencedPortalStore) ClearQuotaPolicy(ctx context.Context, user string) error {
	return store.fence.WithWriteFence(ctx, func() error {
		return store.store.ClearQuotaPolicy(ctx, user)
	})
}

func (store *FencedPortalStore) ApplyQuotaAction(
	ctx context.Context,
	request QuotaActionRequest,
) (QuotaActionResult, error) {
	return withPortalFence(ctx, store.fence, func() (QuotaActionResult, error) {
		return store.store.ApplyQuotaAction(ctx, request)
	})
}

func withPortalFence[T any](
	ctx context.Context,
	fence WriteFence,
	operation func() (T, error),
) (T, error) {
	var result T
	err := fence.WithWriteFence(ctx, func() error {
		var err error
		result, err = operation()
		return err
	})
	return result, err
}
