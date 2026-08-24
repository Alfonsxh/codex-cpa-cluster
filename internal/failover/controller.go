package failover

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
	"golang.org/x/sync/errgroup"
)

const (
	RuntimeStateName    = "account_failover"
	RuntimeStateVersion = 1
)

var ErrQuotaStateUnavailable = errors.New("official quota state is unavailable or stale")

type RuntimeState struct {
	Version         int                     `json:"version"`
	Mode            Mode                    `json:"mode"`
	HeartbeatAt     *int64                  `json:"heartbeat_at"`
	LastCheckAt     *int64                  `json:"last_check_at"`
	LastSuccessAt   *int64                  `json:"last_success_at"`
	NextCheckAt     *int64                  `json:"next_check_at"`
	QuotaRefreshing bool                    `json:"quota_refreshing"`
	LastError       string                  `json:"last_error"`
	Accounts        map[string]AccountState `json:"accounts"`
	LastPlan        *PlanSummary            `json:"last_plan"`
	LastAction      *ActionSummary          `json:"last_action"`
}

type ActionSummary struct {
	At                 int64          `json:"at"`
	Trigger            string         `json:"trigger"`
	Sources            map[string]int `json:"sources"`
	Destinations       map[string]int `json:"destinations"`
	MovedUsers         int            `json:"moved_users"`
	SnapshotGeneration string         `json:"snapshot_generation"`
}

type ControllerResult struct {
	Mode                Mode         `json:"mode"`
	Checked             bool         `json:"checked"`
	MovedUsers          int          `json:"moved_users"`
	Plan                *PlanSummary `json:"plan,omitempty"`
	CapacityUnavailable bool         `json:"capacity_unavailable,omitempty"`
	Warning             string       `json:"warning,omitempty"`
}

type ControllerStore interface {
	RouteStore
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadRuntimeState(context.Context, string, any) (bool, error)
	WriteRuntimeState(context.Context, string, any) error
}

type AccountRuntimeProbe interface {
	ProbeAccounts(context.Context, []controlplane.Account) (map[string]bool, error)
}

type AuditRecorder interface {
	Record(context.Context, string, string, string) error
}

type Controller struct {
	Store     ControllerStore
	Probe     AccountRuntimeProbe
	Activity  ActivityProvider
	Snapshots SnapshotPublisher
	Audit     AuditRecorder
	Now       func() time.Time
}

func DefaultRuntimeState() RuntimeState {
	return RuntimeState{
		Version: RuntimeStateVersion, Mode: ModeOff,
		Accounts: make(map[string]AccountState),
	}
}

func HealthyRuntimeState(state RuntimeState, found bool, now time.Time, maxAge time.Duration) bool {
	if !found || state.Version != RuntimeStateVersion || state.HeartbeatAt == nil ||
		*state.HeartbeatAt <= 0 || state.LastError != "" || maxAge <= 0 {
		return false
	}
	age := now.Unix() - *state.HeartbeatAt
	if age < 0 || age > int64(maxAge/time.Second) {
		return false
	}
	if state.Mode == ModeOff {
		return true
	}
	return state.Mode == ModeActive && state.LastSuccessAt != nil && *state.LastSuccessAt > 0
}

func (controller *Controller) RunOnce(ctx context.Context) (ControllerResult, error) {
	return controller.run(ctx, false)
}

func (controller *Controller) RunForced(ctx context.Context) (ControllerResult, error) {
	return controller.run(ctx, true)
}

func (controller *Controller) run(ctx context.Context, force bool) (ControllerResult, error) {
	if controller.Store == nil {
		return ControllerResult{}, errors.New("account failover controller requires a control-plane store")
	}
	settings, err := controller.Store.ReadSettings(ctx)
	if err != nil {
		return ControllerResult{}, fmt.Errorf("read account failover settings: %w", err)
	}
	mode := modeSetting(settings["account_failover.mode"])
	pollInterval := durationSetting(settings["account_failover.poll_seconds"], time.Minute)
	staleAfter := durationSetting(settings["account_failover.stale_after_seconds"], defaultAccountStateStaleAfter)
	reservePercent := percentageSetting(settings["account_failover.reserve_percent"], 5)
	now := time.Now
	if controller.Now != nil {
		now = controller.Now
	}
	nowTime := now()
	nowUnix := nowTime.Unix()
	state, _, err := ReadRuntimeState(ctx, controller.Store)
	if err != nil {
		return ControllerResult{}, err
	}
	previousMode := state.Mode
	previousHeartbeatAge := timestampAge(state.HeartbeatAt, nowUnix)
	previousPlan := state.LastPlan
	state.Mode = mode
	state.HeartbeatAt = int64Pointer(nowUnix)
	state.QuotaRefreshing = false
	result := ControllerResult{Mode: mode}
	if mode == ModeOff {
		state.NextCheckAt = nil
		state.LastError = ""
		if force || previousMode != mode || previousHeartbeatAge >= 60 {
			if err := controller.writeState(ctx, state); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	if !force && previousMode == mode && state.LastCheckAt != nil &&
		nowUnix < *state.LastCheckAt+int64(pollInterval/time.Second) {
		if previousHeartbeatAge >= 60 {
			if err := controller.writeState(ctx, state); err != nil {
				return result, err
			}
		}
		return result, nil
	}
	if controller.Probe == nil || controller.Activity == nil || controller.Snapshots == nil {
		return result, controller.recordError(
			ctx, state, nowUnix, pollInterval,
			errors.New("active account failover dependencies are incomplete"),
		)
	}

	var (
		accounts   []controlplane.Account
		quotaState quota.RuntimeState
		quotaFound bool
	)
	group, groupContext := errgroup.WithContext(ctx)
	group.Go(func() error {
		var readError error
		accounts, readError = controller.Store.ReadAccounts(groupContext)
		return readError
	})
	group.Go(func() error {
		var readError error
		quotaState, quotaFound, readError = quota.ReadState(groupContext, controller.Store)
		return readError
	})
	if err := group.Wait(); err != nil {
		return result, controller.recordError(ctx, state, nowUnix, pollInterval, fmt.Errorf("collect account failover inputs: %w", err))
	}
	running, err := controller.Probe.ProbeAccounts(ctx, accounts)
	if err != nil {
		return result, controller.recordError(ctx, state, nowUnix, pollInterval, fmt.Errorf("probe account runtimes: %w", err))
	}
	states := BuildAccountStates(
		accounts, quotaState.Snapshot, running, nowTime, staleAfter, reservePercent,
	)
	state.Accounts = states
	state.LastCheckAt = int64Pointer(nowUnix)
	state.NextCheckAt = int64Pointer(nowUnix + int64(pollInterval/time.Second))
	result.Checked = true
	if !quota.Healthy(quotaState, quotaFound, nowTime, staleAfter) {
		return result, controller.recordError(ctx, state, nowUnix, pollInterval, ErrQuotaStateUnavailable)
	}

	service := Service{
		Routes: controller.Store, States: fixedAccountStates(states),
		Activity: controller.Activity, Snapshots: controller.Snapshots,
	}
	evacuation, evacuationError := service.EvacuateExhausted(ctx)
	plan := evacuation.Plan
	result.Plan = &plan
	state.LastPlan = &plan
	capacityUnavailable := errors.Is(evacuationError, ErrRebalanceUnavailable) ||
		errors.Is(evacuationError, ErrRebalanceUnsafe)
	if evacuationError != nil && !capacityUnavailable {
		return result, controller.recordError(ctx, state, nowUnix, pollInterval, evacuationError)
	}
	state.LastSuccessAt = int64Pointer(nowUnix)
	state.LastError = ""
	finalizeContext, cancelFinalize := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelFinalize()
	if capacityUnavailable {
		result.CapacityUnavailable = true
		result.Warning = "exhausted accounts have users that cannot be migrated safely"
		if previousPlanChanged(previousPlan, plan) {
			result.Warning = joinWarnings(result.Warning, controller.audit(
				finalizeContext, "account.failover.capacity_unavailable", formatCapacityTarget(plan), "accepted",
			))
		}
	} else {
		result.MovedUsers = evacuation.MovedUsers
		result.Warning = evacuation.Warning
		if evacuation.MovedUsers > 0 {
			state.LastAction = &ActionSummary{
				At: nowUnix, Trigger: "automatic", Sources: plan.Sources,
				Destinations: evacuation.Destinations, MovedUsers: evacuation.MovedUsers,
				SnapshotGeneration: evacuation.SnapshotGeneration,
			}
			result.Warning = joinWarnings(result.Warning, controller.audit(
				finalizeContext, "account.failover.rebalance", formatMigrationTarget(plan.Sources, evacuation.Destinations), "accepted",
			))
		}
	}
	if err := controller.writeState(finalizeContext, state); err != nil {
		return result, err
	}
	return result, nil
}

func previousPlanChanged(previous *PlanSummary, current PlanSummary) bool {
	return previous == nil || !reflect.DeepEqual(*previous, current)
}

func (controller *Controller) recordError(
	ctx context.Context,
	state RuntimeState,
	now int64,
	pollInterval time.Duration,
	runError error,
) error {
	state.HeartbeatAt = int64Pointer(now)
	state.LastCheckAt = int64Pointer(now)
	state.NextCheckAt = int64Pointer(now + int64(pollInterval/time.Second))
	state.QuotaRefreshing = false
	state.LastError = boundedControllerError(runError, 500)
	finalizeContext, cancelFinalize := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancelFinalize()
	if writeError := controller.writeState(finalizeContext, state); writeError != nil {
		return errors.Join(runError, writeError)
	}
	return runError
}

func (controller *Controller) writeState(ctx context.Context, state RuntimeState) error {
	state.Version = RuntimeStateVersion
	if state.Mode != ModeOff && state.Mode != ModeActive {
		state.Mode = ModeOff
	}
	if state.Accounts == nil {
		state.Accounts = make(map[string]AccountState)
	}
	if err := controller.Store.WriteRuntimeState(ctx, RuntimeStateName, state); err != nil {
		return fmt.Errorf("write account failover runtime state: %w", err)
	}
	return nil
}

func (controller *Controller) audit(ctx context.Context, action string, target string, outcome string) string {
	if controller.Audit == nil {
		return ""
	}
	if err := controller.Audit.Record(ctx, action, target, outcome); err != nil {
		return "audit write failed: " + err.Error()
	}
	return ""
}

type fixedAccountStates map[string]AccountState

func (states fixedAccountStates) AccountStates(context.Context) (map[string]AccountState, error) {
	return states, nil
}

func modeSetting(value any) Mode {
	raw, found := value.(string)
	if !found || strings.TrimSpace(raw) == "" {
		return ModeActive
	}
	mode, err := ParseMode(raw)
	if err != nil {
		return ModeOff
	}
	return mode
}

func percentageSetting(value any, fallback float64) float64 {
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
	if math.IsNaN(result) || math.IsInf(result, 0) || result < 0 || result > 50 {
		return fallback
	}
	return result
}

func timestampAge(timestamp *int64, now int64) int64 {
	if timestamp == nil || *timestamp > now {
		return math.MaxInt64
	}
	return now - *timestamp
}

func int64Pointer(value int64) *int64 {
	return &value
}

func boundedControllerError(err error, maximum int) string {
	if err == nil {
		return ""
	}
	runes := []rune(err.Error())
	if len(runes) > maximum {
		runes = runes[:maximum]
	}
	return string(runes)
}

func formatMigrationTarget(sources map[string]int, destinations map[string]int) string {
	return formatCounts(sources) + " -> " + formatCounts(destinations)
}

func formatCapacityTarget(plan PlanSummary) string {
	return formatCounts(plan.Sources) + fmt.Sprintf(" unassigned:%d", plan.UnassignedUsers)
}

func formatCounts(values map[string]int) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s:%d", key, values[key]))
	}
	return strings.Join(parts, ",")
}

func joinWarnings(values ...string) string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return strings.Join(result, "; ")
}
