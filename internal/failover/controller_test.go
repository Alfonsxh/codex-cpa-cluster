package failover

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
)

func TestControllerActiveModeEvacuatesAndPersistsCompatibleState(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	now := time.Unix(1_800_000_000, 0)
	writeControllerSettings(t, store, "active")
	writeQuotaState(t, store, now, map[string]quota.AccountQuota{
		"alpha": quotaAccount("alpha", 100, true),
		"beta":  quotaAccount("beta", 20, false),
	})
	probe := &staticRuntimeProbe{running: map[string]bool{"alpha": true, "beta": true}}
	activity := &fakeActivity{counts: map[string]int{"beta": 4}}
	publisher := &fakePublisher{events: &activity.events}
	audit := &memoryAudit{}
	controller := Controller{
		Store: store, Probe: probe, Activity: activity, Snapshots: publisher,
		Audit: audit, Now: func() time.Time { return now },
	}

	result, err := controller.RunForced(context.Background())
	if err != nil {
		t.Fatalf("RunForced: %v", err)
	}
	if !result.Checked || result.MovedUsers != 4 || result.Plan == nil || result.Plan.Sources["alpha"] != 4 {
		t.Fatalf("controller result = %#v", result)
	}
	if !reflect.DeepEqual(activity.events, []string{"publish-wait", "refresh-1h"}) {
		t.Fatalf("controller event order = %#v", activity.events)
	}
	state, found, err := ReadRuntimeState(context.Background(), store)
	if err != nil || !found {
		t.Fatalf("ReadRuntimeState = (%#v, %v, %v)", state, found, err)
	}
	if state.Mode != ModeActive || state.LastSuccessAt == nil || *state.LastSuccessAt != now.Unix() ||
		state.LastAction == nil || state.LastAction.MovedUsers != 4 || state.LastError != "" {
		t.Fatalf("runtime state = %#v", state)
	}
	if len(audit.records) != 1 || audit.records[0].action != "account.failover.rebalance" ||
		strings.Contains(audit.records[0].target, "@") {
		t.Fatalf("audit records = %#v", audit.records)
	}
}

func TestControllerRetiredObserveModeFailsClosedWithoutProbes(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	writeControllerSettings(t, store, "observe")
	probe := &staticRuntimeProbe{running: map[string]bool{"alpha": true, "beta": true}}
	controller := Controller{Store: store, Probe: probe, Now: func() time.Time { return time.Unix(50, 0) }}

	result, err := controller.RunForced(context.Background())
	if err != nil {
		t.Fatalf("RunForced: %v", err)
	}
	if result.Mode != ModeOff || result.Checked || probe.calls != 0 {
		t.Fatalf("observe fail-closed result = %#v, probe calls = %d", result, probe.calls)
	}
	state, _, _ := ReadRuntimeState(context.Background(), store)
	if state.Mode != ModeOff || state.NextCheckAt != nil {
		t.Fatalf("observe runtime state = %#v", state)
	}
}

func TestControllerMissingQuotaFailsClosedAndPreservesRoutes(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	writeControllerSettings(t, store, "active")
	before, _ := store.ReadRoutes(context.Background())
	controller := Controller{
		Store:    store,
		Probe:    &staticRuntimeProbe{running: map[string]bool{"alpha": true, "beta": true}},
		Activity: &fakeActivity{}, Snapshots: &fakePublisher{},
		Now: func() time.Time { return time.Unix(100, 0) },
	}

	result, err := controller.RunForced(context.Background())
	if !errors.Is(err, ErrQuotaStateUnavailable) || !result.Checked || result.MovedUsers != 0 {
		t.Fatalf("missing quota result = (%#v, %v)", result, err)
	}
	after, _ := store.ReadRoutes(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("missing quota changed routes: before=%#v after=%#v", before, after)
	}
	state, _, _ := ReadRuntimeState(context.Background(), store)
	if state.LastError == "" || state.Accounts["alpha"].Eligible || state.Accounts["beta"].Eligible {
		t.Fatalf("missing quota state = %#v", state)
	}
}

func TestControllerCapacityAlertIsDeduplicated(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	now := time.Unix(200, 0)
	writeControllerSettings(t, store, "active")
	writeQuotaState(t, store, now, map[string]quota.AccountQuota{
		"alpha": quotaAccount("alpha", 100, true),
		"beta":  quotaAccount("beta", 100, true),
	})
	audit := &memoryAudit{}
	controller := Controller{
		Store:    store,
		Probe:    &staticRuntimeProbe{running: map[string]bool{"alpha": true, "beta": true}},
		Activity: &fakeActivity{}, Snapshots: &fakePublisher{}, Audit: audit,
		Now: func() time.Time { return now },
	}

	first, firstError := controller.RunForced(context.Background())
	second, secondError := controller.RunForced(context.Background())
	if firstError != nil || secondError != nil || !first.CapacityUnavailable || !second.CapacityUnavailable {
		t.Fatalf("capacity results = (%#v, %v), (%#v, %v)", first, firstError, second, secondError)
	}
	if len(audit.records) != 1 || audit.records[0].action != "account.failover.capacity_unavailable" {
		t.Fatalf("capacity audit records = %#v", audit.records)
	}
}

func TestControllerFinalizesCommittedMigrationAfterRequestCancellation(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	now := time.Unix(300, 0)
	writeControllerSettings(t, store, "active")
	writeQuotaState(t, store, now, map[string]quota.AccountQuota{
		"alpha": quotaAccount("alpha", 100, true),
		"beta":  quotaAccount("beta", 20, false),
	})
	requestContext, cancel := context.WithCancel(context.Background())
	controller := Controller{
		Store:    store,
		Probe:    &staticRuntimeProbe{running: map[string]bool{"alpha": true, "beta": true}},
		Activity: &fakeActivity{}, Snapshots: &successfulCancelPublisher{cancel: cancel},
		Now: func() time.Time { return now },
	}

	result, err := controller.RunForced(requestContext)
	if err != nil || result.MovedUsers != 4 {
		t.Fatalf("canceled committed result = (%#v, %v)", result, err)
	}
	state, _, readError := ReadRuntimeState(context.Background(), store)
	if readError != nil || state.LastAction == nil || state.LastAction.MovedUsers != 4 {
		t.Fatalf("canceled committed state = (%#v, %v)", state, readError)
	}
}

func TestControllerSkipsRoundUntilConfiguredPollInterval(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	writeControllerSettings(t, store, "active")
	now := time.Unix(500, 0)
	state := DefaultRuntimeState()
	state.Mode = ModeActive
	state.HeartbeatAt = int64Pointer(now.Unix() - 10)
	state.LastCheckAt = int64Pointer(now.Unix() - 10)
	if err := store.WriteRuntimeState(context.Background(), RuntimeStateName, state); err != nil {
		t.Fatalf("WriteRuntimeState: %v", err)
	}
	probe := &staticRuntimeProbe{}
	controller := Controller{Store: store, Probe: probe, Now: func() time.Time { return now }}

	result, err := controller.RunOnce(context.Background())
	if err != nil || result.Checked || probe.calls != 0 {
		t.Fatalf("due result = (%#v, %v), probe calls = %d", result, err, probe.calls)
	}
}

func TestHealthyRuntimeStateRequiresSuccessfulActiveCheck(t *testing.T) {
	now := time.Unix(1_000, 0)
	state := DefaultRuntimeState()
	state.Mode = ModeOff
	state.HeartbeatAt = int64Pointer(990)
	if !HealthyRuntimeState(state, true, now, time.Minute) {
		t.Fatal("fresh off state should be healthy")
	}
	state.Mode = ModeActive
	if HealthyRuntimeState(state, true, now, time.Minute) {
		t.Fatal("active state without a successful check should be unhealthy")
	}
	state.LastSuccessAt = int64Pointer(995)
	if !HealthyRuntimeState(state, true, now, time.Minute) {
		t.Fatal("fresh successful active state should be healthy")
	}
	state.LastError = "quota stale"
	if HealthyRuntimeState(state, true, now, time.Minute) {
		t.Fatal("state with an error should be unhealthy")
	}
}

func writeControllerSettings(t *testing.T, store *controlplane.Store, mode string) {
	t.Helper()
	err := store.WriteSettings(context.Background(), map[string]any{
		"account_failover.mode":                mode,
		"account_failover.poll_seconds":        60,
		"account_failover.reserve_percent":     5.0,
		"account_failover.stale_after_seconds": 120,
	})
	if err != nil {
		t.Fatalf("WriteSettings: %v", err)
	}
}

func writeQuotaState(
	t *testing.T,
	store *controlplane.Store,
	now time.Time,
	values map[string]quota.AccountQuota,
) {
	t.Helper()
	accounts := make([]quota.AccountQuota, 0, len(values))
	for _, account := range []string{"alpha", "beta"} {
		accounts = append(accounts, values[account])
	}
	state := quota.RuntimeState{
		Version: 1, HeartbeatAt: now.Unix(), LastSuccessAt: now.Unix(),
		Snapshot: quota.Snapshot{GeneratedAt: now.Unix(), Accounts: accounts},
	}
	if err := store.WriteRuntimeState(context.Background(), quota.RuntimeStateName, state); err != nil {
		t.Fatalf("write quota state: %v", err)
	}
}

func quotaAccount(account string, used float64, exhausted bool) quota.AccountQuota {
	remaining := 100 - used
	allowed := true
	limitReached := exhausted
	return quota.AccountQuota{
		Account: account, Status: "ok", Allowed: &allowed, LimitReached: &limitReached,
		Weekly: &quota.WeeklyWindow{
			Key: "default:test", UsedPercent: used, RemainingPercent: remaining,
			LimitReached: exhausted, WindowSeconds: quota.WeeklyWindowSeconds,
		},
	}
}

type staticRuntimeProbe struct {
	running map[string]bool
	err     error
	calls   int
}

func (probe *staticRuntimeProbe) ProbeAccounts(
	context.Context,
	[]controlplane.Account,
) (map[string]bool, error) {
	probe.calls++
	return probe.running, probe.err
}

type auditRecord struct {
	action  string
	target  string
	outcome string
}

type memoryAudit struct {
	records []auditRecord
	err     error
}

type successfulCancelPublisher struct {
	cancel context.CancelFunc
}

func (publisher *successfulCancelPublisher) PublishAuthSnapshot(context.Context, bool) (Snapshot, error) {
	publisher.cancel()
	return Snapshot{Generation: "committed-generation"}, nil
}

func (audit *memoryAudit) Record(_ context.Context, action string, target string, outcome string) error {
	audit.records = append(audit.records, auditRecord{action: action, target: target, outcome: outcome})
	return audit.err
}
