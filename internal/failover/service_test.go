package failover

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/gateway"
)

func TestMoveUserUsesExpectedRouteAndRefreshesActivityAfterActivation(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	activity := &fakeActivity{counts: map[string]int{"alpha": 3, "beta": 1}}
	publisher := &fakePublisher{events: &activity.events}
	service := Service{Routes: store, Activity: activity, Snapshots: publisher}

	result, err := service.MoveUser(context.Background(), " A@Example.com ", "beta", "alpha")
	if err != nil {
		t.Fatalf("MoveUser: %v", err)
	}
	if result.MovedUsers != 1 || result.Destinations["beta"] != 1 ||
		!result.ActivityRefreshed || !reflect.DeepEqual(result.ActiveUsers1H, activity.counts) {
		t.Fatalf("move result = %#v", result)
	}
	if !reflect.DeepEqual(activity.events, []string{"publish-wait", "refresh-1h"}) {
		t.Fatalf("move event order = %#v", activity.events)
	}
	routes, readError := store.ReadRoutes(context.Background())
	if readError != nil || routes["a@example.com"] != "beta" {
		t.Fatalf("moved route = %q, error=%v", routes["a@example.com"], readError)
	}
}

func TestMoveUserRejectsStaleExpectedRouteWithoutPublishing(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	publisher := &fakePublisher{}
	service := Service{Routes: store, Activity: &fakeActivity{}, Snapshots: publisher}

	_, err := service.MoveUser(context.Background(), "a@example.com", "beta", "stale")
	if !errors.Is(err, controlplane.ErrRouteConflict) || publisher.count != 0 {
		t.Fatalf("stale expected route = %v, publish count=%d", err, publisher.count)
	}
	routes, _ := store.ReadRoutes(context.Background())
	if routes["a@example.com"] != "alpha" {
		t.Fatalf("conflicting move changed route to %q", routes["a@example.com"])
	}
}

func TestMoveUserRollsBackExactRouteWhenActivationFails(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	publisher := &fakePublisher{failFirst: true}
	service := Service{Routes: store, Activity: &fakeActivity{}, Snapshots: publisher}

	_, err := service.MoveUser(context.Background(), "a@example.com", "beta", "alpha")
	if err == nil || !strings.Contains(err.Error(), "publish account rebalance snapshot") {
		t.Fatalf("activation failure = %v", err)
	}
	routes, _ := store.ReadRoutes(context.Background())
	if routes["a@example.com"] != "alpha" || publisher.count != 2 {
		t.Fatalf("rollback route=%q publisher=%d", routes["a@example.com"], publisher.count)
	}
}

func TestRebalanceAllPublishesOnceThenImmediatelyRefreshesActivity(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	activity := &fakeActivity{
		users:  []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"},
		counts: map[string]int{"alpha": 2, "beta": 2},
	}
	publisher := &fakePublisher{events: &activity.events}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Eligible: true, Headroom: 100},
			"beta":  {Eligible: true, Headroom: 100},
		}},
		Activity:  activity,
		Snapshots: publisher,
	}
	result, err := service.RebalanceAll(context.Background())
	if err != nil {
		t.Fatalf("RebalanceAll: %v", err)
	}
	if result.MovedUsers != 2 || !result.ActivityRefreshed || result.SnapshotGeneration != "generation-1" {
		t.Fatalf("rebalance result = %#v", result)
	}
	if !reflect.DeepEqual(activity.events, []string{"publish-wait", "refresh-1h"}) {
		t.Fatalf("rebalance event order = %#v", activity.events)
	}
	routes, err := store.ReadRoutes(context.Background())
	if err != nil {
		t.Fatalf("ReadRoutes: %v", err)
	}
	counts := map[string]int{}
	for _, account := range routes {
		counts[account]++
	}
	if !reflect.DeepEqual(counts, map[string]int{"alpha": 2, "beta": 2}) {
		t.Fatalf("balanced routes = %#v", routes)
	}
}

func TestRebalanceAllRejectsWholeBatchBeforeWriteForUnsafeUser(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	if err := store.WriteKeyRecords(context.Background(), []controlplane.KeyRecord{
		{Label: "a:alpha", Account: "alpha", AccountEmail: "alpha@accounts.example.com", User: "a@example.com", Status: "active", Key: "key-a", CreatedAt: 1, UpdatedAt: 1},
	}); err != nil {
		t.Fatalf("replace records: %v", err)
	}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Eligible: true, Headroom: 100},
			"beta":  {Eligible: true, Headroom: 100},
		}},
		Activity:  &fakeActivity{users: []string{"a@example.com", "b@example.com"}},
		Snapshots: &fakePublisher{},
	}
	before, _ := store.ReadRoutes(context.Background())
	_, err := service.RebalanceAll(context.Background())
	if !errors.Is(err, ErrRebalanceUnsafe) {
		t.Fatalf("unsafe RebalanceAll error = %v", err)
	}
	after, _ := store.ReadRoutes(context.Background())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("unsafe rebalance changed routes: before=%#v after=%#v", before, after)
	}
}

func TestRebalanceAllRollsBackRoutesWhenSnapshotPublicationFails(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	activity := &fakeActivity{users: []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"}}
	publisher := &fakePublisher{failFirst: true, events: &activity.events}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Eligible: true, Headroom: 100},
			"beta":  {Eligible: true, Headroom: 100},
		}},
		Activity:  activity,
		Snapshots: publisher,
	}
	before, _ := store.ReadRoutes(context.Background())
	_, err := service.RebalanceAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "publish account rebalance snapshot") {
		t.Fatalf("snapshot failure error = %v", err)
	}
	after, _ := store.ReadRoutes(context.Background())
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("snapshot failure did not restore routes: before=%#v after=%#v", before, after)
	}
	if !reflect.DeepEqual(activity.events, []string{"publish-wait", "publish-wait"}) {
		t.Fatalf("rollback event order = %#v", activity.events)
	}
}

func TestRebalanceAllUsesIndependentRollbackContextAfterRequestCancellation(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	requestContext, cancelRequest := context.WithCancel(context.Background())
	publisher := &cancelingPublisher{cancel: cancelRequest}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Eligible: true, Headroom: 100},
			"beta":  {Eligible: true, Headroom: 100},
		}},
		Activity:  &fakeActivity{},
		Snapshots: publisher,
	}
	before, _ := store.ReadRoutes(context.Background())
	_, err := service.RebalanceAll(requestContext)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot error = %v", err)
	}
	after, readError := store.ReadRoutes(context.Background())
	if readError != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("request cancellation did not restore routes: before=%#v after=%#v error=%v", before, after, readError)
	}
	if publisher.calls != 2 || !publisher.rollbackContextActive {
		t.Fatalf("rollback publisher = %#v", publisher)
	}
}

func TestRebalanceAllRestoresRoutesAndSnapshotWhenActivationProbeNeverAdvances(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	probe := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(writer, `{"auth":{"active_generation":"previous"}}`)
	}))
	defer probe.Close()
	publisher := &AuthSnapshotPublisher{
		Root: store.Root(), Store: store, ProbeURLs: []string{probe.URL}, HTTPClient: probe.Client(),
		WaitTimeout: 25 * time.Millisecond, PollInterval: 2 * time.Millisecond, Fence: &testWriteFence{},
	}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Eligible: true, Headroom: 100},
			"beta":  {Eligible: true, Headroom: 100},
		}},
		Activity:  &fakeActivity{},
		Snapshots: publisher,
	}
	before, _ := store.ReadRoutes(context.Background())
	_, err := service.RebalanceAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "did not activate auth snapshot") {
		t.Fatalf("stale probe error = %v", err)
	}
	after, readError := store.ReadRoutes(context.Background())
	if readError != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("stale probe did not restore routes: before=%#v after=%#v error=%v", before, after, readError)
	}
	file, openError := os.Open(filepath.Join(store.Root(), authSnapshotRelativePath))
	if openError != nil {
		t.Fatalf("open rollback auth snapshot: %v", openError)
	}
	snapshot, parseError := gateway.ParseAuthSnapshot(file)
	_ = file.Close()
	if parseError != nil {
		t.Fatalf("parse rollback auth snapshot: %v", parseError)
	}
	if len(snapshot.Records) != 4 {
		t.Fatalf("rollback auth snapshot records = %#v", snapshot.Records)
	}
	for _, record := range snapshot.Records {
		if record.Account != "alpha" {
			t.Fatalf("rollback auth snapshot retained candidate route: %#v", record)
		}
	}
}

func TestEvacuateExhaustedMovesWholeSafeBatchAndRefreshesActivity(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	activity := &fakeActivity{counts: map[string]int{"beta": 4}}
	publisher := &fakePublisher{events: &activity.events}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Account: "alpha", Exhausted: true, Reason: "quota_exhausted"},
			"beta":  {Account: "beta", Eligible: true, Headroom: 80, Reason: "available"},
		}},
		Activity: activity, Snapshots: publisher,
	}
	result, err := service.EvacuateExhausted(context.Background())
	if err != nil {
		t.Fatalf("EvacuateExhausted: %v", err)
	}
	if result.MovedUsers != 4 || result.Plan.AffectedUsers != 4 || result.Plan.PlannedUsers != 4 ||
		result.Plan.Sources["alpha"] != 4 || result.Destinations["beta"] != 4 || !result.ActivityRefreshed {
		t.Fatalf("evacuation result = %#v", result)
	}
	if !reflect.DeepEqual(activity.events, []string{"publish-wait", "refresh-1h"}) {
		t.Fatalf("evacuation event order = %#v", activity.events)
	}
	routes, err := store.ReadRoutes(context.Background())
	if err != nil {
		t.Fatalf("ReadRoutes: %v", err)
	}
	for user, account := range routes {
		if account != "beta" {
			t.Fatalf("route %s = %s", user, account)
		}
	}
}

func TestEvacuateExhaustedRejectsWholeBatchForUnroutableUser(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	if err := store.WriteKeyRecords(context.Background(), []controlplane.KeyRecord{
		{Label: "a:alpha", Account: "alpha", User: "a@example.com", Status: "active", Key: "key-a"},
		{Label: "a:beta", Account: "beta", User: "a@example.com", Status: "active", Key: "key-a"},
		{Label: "b:alpha", Account: "alpha", User: "b@example.com", Status: "active", Key: "key-b"},
	}); err != nil {
		t.Fatalf("WriteKeyRecords: %v", err)
	}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Account: "alpha", Exhausted: true},
			"beta":  {Account: "beta", Eligible: true, Headroom: 80},
		}},
		Activity: &fakeActivity{}, Snapshots: &fakePublisher{},
	}
	before, _ := store.ReadRoutes(context.Background())
	result, err := service.EvacuateExhausted(context.Background())
	if !errors.Is(err, ErrRebalanceUnsafe) || result.Plan.SkippedUsers != 1 {
		t.Fatalf("unsafe evacuation = (%#v, %v)", result, err)
	}
	after, _ := store.ReadRoutes(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("unsafe evacuation changed routes: before=%#v after=%#v", before, after)
	}
}

func TestEvacuateExhaustedPreservesRoutesWithoutCapacity(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Account: "alpha", Exhausted: true},
			"beta":  {Account: "beta", Eligible: false, Headroom: 0},
		}},
		Activity: &fakeActivity{}, Snapshots: &fakePublisher{},
	}
	before, _ := store.ReadRoutes(context.Background())
	result, err := service.EvacuateExhausted(context.Background())
	if !errors.Is(err, ErrRebalanceUnavailable) || result.Plan.UnassignedUsers != 4 {
		t.Fatalf("capacity evacuation = (%#v, %v)", result, err)
	}
	after, _ := store.ReadRoutes(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("capacity evacuation changed routes: before=%#v after=%#v", before, after)
	}
}

func TestEvacuateAccountMovesWholeSafeBatchAndRefreshesActivity(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	activity := &fakeActivity{counts: map[string]int{"beta": 4}}
	publisher := &fakePublisher{events: &activity.events}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Account: "alpha", Eligible: true, Headroom: 100},
			"beta":  {Account: "beta", Eligible: true, Headroom: 100},
		}},
		Activity: activity, Snapshots: publisher,
	}

	result, err := service.EvacuateAccount(context.Background(), " alpha ")
	if err != nil {
		t.Fatalf("EvacuateAccount: %v", err)
	}
	if result.MovedUsers != 4 || result.Plan.AffectedUsers != 4 || result.Plan.PlannedUsers != 4 ||
		result.Plan.Sources["alpha"] != 4 || result.Destinations["beta"] != 4 || !result.ActivityRefreshed {
		t.Fatalf("account evacuation result = %#v", result)
	}
	if !reflect.DeepEqual(activity.events, []string{"publish-wait", "refresh-1h"}) {
		t.Fatalf("account evacuation event order = %#v", activity.events)
	}
	routes, readError := store.ReadRoutes(context.Background())
	if readError != nil {
		t.Fatalf("ReadRoutes: %v", readError)
	}
	for user, account := range routes {
		if account != "beta" {
			t.Fatalf("route %s = %s", user, account)
		}
	}
}

func TestEvacuateAccountRejectsWholeBatchForUnsafeUser(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	if err := store.WriteKeyRecords(context.Background(), []controlplane.KeyRecord{
		{Label: "a:alpha", Account: "alpha", User: "a@example.com", Status: "active", Key: "key-a"},
		{Label: "a:beta", Account: "beta", User: "a@example.com", Status: "active", Key: "key-a"},
		{Label: "b:alpha", Account: "alpha", User: "b@example.com", Status: "active", Key: "key-b"},
	}); err != nil {
		t.Fatalf("WriteKeyRecords: %v", err)
	}
	service := Service{
		Routes: store,
		States: staticStates{states: map[string]AccountState{
			"alpha": {Account: "alpha", Eligible: true, Headroom: 100},
			"beta":  {Account: "beta", Eligible: true, Headroom: 100},
		}},
		Activity: &fakeActivity{}, Snapshots: &fakePublisher{},
	}
	before, _ := store.ReadRoutes(context.Background())
	result, err := service.EvacuateAccount(context.Background(), "alpha")
	if !errors.Is(err, ErrRebalanceUnsafe) || result.Plan.SkippedUsers != 1 {
		t.Fatalf("unsafe account evacuation = (%#v, %v)", result, err)
	}
	after, _ := store.ReadRoutes(context.Background())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("unsafe account evacuation changed routes: before=%#v after=%#v", before, after)
	}
}

type staticStates struct {
	states map[string]AccountState
	err    error
}

func (provider staticStates) AccountStates(context.Context) (map[string]AccountState, error) {
	return provider.states, provider.err
}

type fakeActivity struct {
	users  []string
	counts map[string]int
	err    error
	events []string
}

func (activity *fakeActivity) ActiveUsersLastHour(context.Context) ([]string, error) {
	return activity.users, activity.err
}

func (activity *fakeActivity) RefreshActiveUsersLastHour(context.Context) (map[string]int, error) {
	activity.events = append(activity.events, "refresh-1h")
	return activity.counts, activity.err
}

type fakePublisher struct {
	count     int
	failFirst bool
	events    *[]string
}

type cancelingPublisher struct {
	cancel                context.CancelFunc
	calls                 int
	rollbackContextActive bool
}

func (publisher *cancelingPublisher) PublishAuthSnapshot(ctx context.Context, _ bool) (Snapshot, error) {
	publisher.calls++
	if publisher.calls == 1 {
		publisher.cancel()
		return Snapshot{}, context.Canceled
	}
	publisher.rollbackContextActive = ctx.Err() == nil
	return Snapshot{Generation: "rollback-generation"}, ctx.Err()
}

func (publisher *fakePublisher) PublishAuthSnapshot(_ context.Context, wait bool) (Snapshot, error) {
	publisher.count++
	if publisher.events != nil {
		name := "publish-rollback"
		if wait {
			name = "publish-wait"
		}
		*publisher.events = append(*publisher.events, name)
	}
	if publisher.failFirst && publisher.count == 1 {
		return Snapshot{}, errors.New("test snapshot failure")
	}
	return Snapshot{Generation: fmt.Sprintf("generation-%d", publisher.count)}, nil
}

func seedRebalanceStore(t *testing.T) *controlplane.Store {
	t.Helper()
	store, err := controlplane.Open(context.Background(), t.TempDir(), controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	accounts := []controlplane.Account{
		{ID: "alpha", Email: "alpha@accounts.example.com", Port: 18318, CreatedAt: 1, GroupEnabled: true},
		{ID: "beta", Email: "beta@accounts.example.com", Port: 18319, CreatedAt: 1, GroupEnabled: true},
	}
	if err := store.WriteAccounts(context.Background(), accounts); err != nil {
		store.Close()
		t.Fatalf("write accounts: %v", err)
	}
	records := make([]controlplane.KeyRecord, 0)
	routes := make(map[string]string)
	for _, user := range []string{"a@example.com", "b@example.com", "c@example.com", "d@example.com"} {
		secret := "key-" + user[:1]
		for _, account := range accounts {
			records = append(records, controlplane.KeyRecord{
				Label: user + ":" + account.ID, Account: account.ID, AccountEmail: account.Email,
				User: user, Status: "active", Key: secret, CreatedAt: 1, UpdatedAt: 1,
			})
		}
		routes[user] = "alpha"
	}
	if err := store.WriteKeyRecords(context.Background(), records); err != nil {
		store.Close()
		t.Fatalf("write key records: %v", err)
	}
	if err := store.WriteRoutes(context.Background(), routes); err != nil {
		store.Close()
		t.Fatalf("write routes: %v", err)
	}
	return store
}
