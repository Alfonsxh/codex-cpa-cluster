package quota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"golang.org/x/sync/errgroup"
)

type Store interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadSecret(context.Context, string) (string, bool, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
	WriteRuntimeState(context.Context, string, any) error
}

func (refresher *Refresher) RecordError(ctx context.Context, runError error) error {
	if refresher.Store == nil {
		return errors.New("official quota refresher requires a control-plane store")
	}
	if refresher.Now == nil {
		refresher.Now = time.Now
	}
	state, _, err := ReadState(ctx, refresher.Store)
	if err != nil {
		return err
	}
	state.Version = runtimeStateVersion
	state.HeartbeatAt = refresher.Now().Unix()
	state.LastError = boundedError(runError, 500)
	if err := refresher.Store.WriteRuntimeState(ctx, RuntimeStateName, state); err != nil {
		return fmt.Errorf("write official quota error state: %w", err)
	}
	return nil
}

func ReadState(ctx context.Context, store interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
}) (RuntimeState, bool, error) {
	var raw json.RawMessage
	found, err := store.ReadRuntimeState(ctx, RuntimeStateName, &raw)
	if err != nil {
		return RuntimeState{}, false, fmt.Errorf("read official quota runtime state: %w", err)
	}
	if !found {
		return RuntimeState{}, false, nil
	}
	var state RuntimeState
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &state) != nil || state.Version != runtimeStateVersion {
		return RuntimeState{}, true, nil
	}
	if state.Snapshot.Accounts == nil {
		state.Snapshot.Accounts = []AccountQuota{}
	}
	return state, true, nil
}

func Healthy(state RuntimeState, found bool, now time.Time, maxAge time.Duration) bool {
	if !found || state.Version != runtimeStateVersion || state.HeartbeatAt <= 0 ||
		state.LastSuccessAt <= 0 || state.LastError != "" || maxAge <= 0 {
		return false
	}
	age := now.Unix() - state.HeartbeatAt
	return age >= 0 && age <= int64(maxAge/time.Second)
}

func PollInterval(settings map[string]any) time.Duration {
	return time.Duration(intSetting(settings["account_failover.poll_seconds"], 60, 30, 3600)) * time.Second
}

type Refresher struct {
	Root           string
	Store          Store
	Endpoint       string
	Now            func() time.Time
	MaxConcurrency int
}

func (refresher *Refresher) RunOnce(ctx context.Context) (Snapshot, error) {
	if refresher.Store == nil {
		return Snapshot{}, errors.New("official quota refresher requires a control-plane store")
	}
	accounts, err := refresher.Store.ReadAccounts(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read official quota accounts: %w", err)
	}
	settings, err := refresher.Store.ReadSettings(ctx)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read official quota settings: %w", err)
	}
	if refresher.Now == nil {
		refresher.Now = time.Now
	}
	generatedAt := refresher.Now().Unix()
	cacheTTL := intSetting(settings["usage.quota_cache_seconds"], 60, 30, 3600)
	timeout := time.Duration(intSetting(settings["usage.upstream_timeout_seconds"], 20, 5, 120)) * time.Second
	result := Snapshot{
		GeneratedAt: generatedAt, CacheTTLSeconds: cacheTTL,
		Accounts: make([]AccountQuota, len(accounts)),
	}
	loader := OAuthLoader{Root: refresher.Root}
	resolver := ProxyResolver{Store: refresher.Store, Settings: settings}
	client := Client{Endpoint: refresher.Endpoint, Timeout: timeout}
	limit := refresher.MaxConcurrency
	if limit <= 0 || limit > 16 {
		limit = 4
	}
	group, groupContext := errgroup.WithContext(ctx)
	group.SetLimit(limit)
	for index, account := range accounts {
		index, account := index, account
		group.Go(func() error {
			auth, err := loader.Load(account.ID)
			if errors.Is(err, ErrOAuthMissing) {
				result.Accounts[index] = unavailableAccount(account.ID, "auth_missing")
				return nil
			}
			if err != nil {
				result.Accounts[index] = unavailableAccount(account.ID, "unavailable")
				return nil
			}
			proxyURL, err := resolver.Resolve(groupContext, account)
			if err != nil {
				result.Accounts[index] = unavailableAccount(account.ID, "unavailable")
				return nil
			}
			payload, err := client.Fetch(groupContext, auth, proxyURL)
			switch {
			case errors.Is(err, ErrAuthExpired):
				result.Accounts[index] = unavailableAccount(account.ID, "auth_expired")
			case err != nil:
				result.Accounts[index] = unavailableAccount(account.ID, "unavailable")
			default:
				result.Accounts[index] = Normalize(account.ID, payload)
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return Snapshot{}, fmt.Errorf("refresh official quotas: %w", err)
	}
	state := RuntimeState{
		Version: runtimeStateVersion, HeartbeatAt: generatedAt,
		LastSuccessAt: generatedAt, LastError: "", Snapshot: result,
	}
	if err := refresher.Store.WriteRuntimeState(ctx, RuntimeStateName, state); err != nil {
		return result, fmt.Errorf("write official quota runtime state: %w", err)
	}
	return result, nil
}

func intSetting(value any, fallback int64, minimum int64, maximum int64) int64 {
	var result float64
	switch typed := value.(type) {
	case float64:
		result = typed
	case int:
		result = float64(typed)
	case int64:
		result = float64(typed)
	default:
		return fallback
	}
	if math.IsNaN(result) || math.IsInf(result, 0) || result < float64(minimum) || result > float64(maximum) {
		return fallback
	}
	return int64(result)
}

func boundedError(err error, maximum int) string {
	if err == nil {
		return ""
	}
	runes := []rune(err.Error())
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes)
}
