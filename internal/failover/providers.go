package failover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
)

const defaultAccountStateStaleAfter = 120 * time.Second

type RuntimeStateStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
}

// RuntimeStateProvider consumes the last complete account-capacity state
// published by the failover controller. Missing, failed, or stale state is
// represented as unavailable capacity, never as a healthy account.
type RuntimeStateProvider struct {
	Store RuntimeStateStore
	Now   func() time.Time
}

// Keep the package-local name while older focused tests and migration code are
// converted to the complete Python-compatible runtime state.
type accountFailoverRuntimeState = RuntimeState

func (provider RuntimeStateProvider) AccountStates(ctx context.Context) (map[string]AccountState, error) {
	if provider.Store == nil {
		return nil, errors.New("account state provider requires a control-plane store")
	}
	accounts, err := provider.Store.ReadAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("read account state catalog: %w", err)
	}
	settings, err := provider.Store.ReadSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("read account state settings: %w", err)
	}
	staleAfter := durationSetting(
		settings["account_failover.stale_after_seconds"],
		defaultAccountStateStaleAfter,
	)
	persisted, found, err := ReadRuntimeState(ctx, provider.Store)
	if err != nil {
		return nil, err
	}
	if provider.Now == nil {
		provider.Now = time.Now
	}
	now := provider.Now().Unix()
	result := make(map[string]AccountState, len(accounts))
	for _, account := range accounts {
		state, stateFound := persisted.Accounts[account.ID]
		if !found || !stateFound || persisted.LastError != "" {
			result[account.ID] = unavailableAccountState(account.ID, "quota_unavailable")
			continue
		}
		state.Account = account.ID
		if err := validateAccountState(state); err != nil {
			return nil, fmt.Errorf("invalid persisted account state for %s: %w", account.ID, err)
		}
		if !account.GroupEnabled {
			state.Eligible = false
			state.Headroom = 0
			state.Reason = "account_disabled"
		} else if state.ObservedAt <= 0 || state.ObservedAt > now+30 || now-state.ObservedAt > int64(staleAfter/time.Second) {
			state.Eligible = false
			state.Headroom = 0
			state.Reason = "quota_stale"
		}
		result[account.ID] = state
	}
	return result, nil
}

// ReadRuntimeState accepts both the Go controller payload and the current
// Python v1 payload. Invalid or future payloads fail closed to the default
// state instead of turning unknown quota into migration capacity.
func ReadRuntimeState(ctx context.Context, store interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
}) (RuntimeState, bool, error) {
	state := DefaultRuntimeState()
	var raw json.RawMessage
	found, err := store.ReadRuntimeState(ctx, RuntimeStateName, &raw)
	if err != nil {
		return state, false, fmt.Errorf("read account failover runtime state: %w", err)
	}
	if !found {
		return state, false, nil
	}
	var persisted RuntimeState
	if len(raw) == 0 || raw[0] != '{' || json.Unmarshal(raw, &persisted) != nil ||
		persisted.Version != RuntimeStateVersion {
		return state, true, nil
	}
	if persisted.Mode != ModeOff && persisted.Mode != ModeActive {
		persisted.Mode = ModeOff
	}
	if persisted.Accounts == nil {
		persisted.Accounts = make(map[string]AccountState)
	}
	return persisted, true, nil
}

func unavailableAccountState(account string, reason string) AccountState {
	return AccountState{Account: account, Reason: reason}
}

func validateAccountState(state AccountState) error {
	if state.Headroom < 0 || math.IsNaN(state.Headroom) || math.IsInf(state.Headroom, 0) {
		return errors.New("headroom is invalid")
	}
	for name, value := range map[string]*float64{
		"used_percent": state.UsedPercent, "remaining_percent": state.RemainingPercent,
	} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 || *value > 100) {
			return fmt.Errorf("%s is invalid", name)
		}
	}
	return nil
}

func durationSetting(value any, fallback time.Duration) time.Duration {
	var seconds float64
	switch typed := value.(type) {
	case float64:
		seconds = typed
	case int:
		seconds = float64(typed)
	case int64:
		seconds = float64(typed)
	}
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 5 || seconds > 24*60*60 {
		return fallback
	}
	return time.Duration(seconds * float64(time.Second))
}

// UsageActivity remains as a source-compatible alias while the Go migration
// moves all read-only usage queries into one shared package and connection
// pool. New callers should open internal/usage.Store directly.
type UsageActivity = usage.Store

func OpenUsageActivity(root string, now func() time.Time) (*UsageActivity, error) {
	return usage.OpenReadOnly(root, now)
}
