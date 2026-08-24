package failover

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/gateway"
)

func TestRuntimeStateProviderFailsClosedForStaleAndMissingAccounts(t *testing.T) {
	store, err := controlplane.Open(context.Background(), t.TempDir(), controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{
		{ID: "alpha", Email: "alpha@example.com", Port: 18319, GroupEnabled: true},
		{ID: "beta", Email: "beta@example.com", Port: 18320, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("write accounts: %v", err)
	}
	if err := store.WriteSettings(ctx, map[string]any{
		"account_failover.stale_after_seconds": 120,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := store.WriteRuntimeState(ctx, "account_failover", accountFailoverRuntimeState{
		Version: 1,
		Accounts: map[string]AccountState{
			"alpha": {Eligible: true, Headroom: 80, ObservedAt: 800, Reason: "available"},
		},
	}); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}
	provider := RuntimeStateProvider{Store: store, Now: func() time.Time { return time.Unix(1000, 0) }}
	states, err := provider.AccountStates(ctx)
	if err != nil {
		t.Fatalf("AccountStates: %v", err)
	}
	if states["alpha"].Eligible || states["alpha"].Reason != "quota_stale" || states["alpha"].Headroom != 0 {
		t.Fatalf("stale alpha state = %#v", states["alpha"])
	}
	if states["beta"].Eligible || states["beta"].Reason != "quota_unavailable" {
		t.Fatalf("missing beta state = %#v", states["beta"])
	}
}

func TestRuntimeStateProviderFailsClosedWhenControllerRecordedError(t *testing.T) {
	store, err := controlplane.Open(context.Background(), t.TempDir(), controlplane.Options{})
	if err != nil {
		t.Fatalf("open control-plane store: %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.WriteAccounts(ctx, []controlplane.Account{
		{ID: "alpha", Email: "alpha@example.com", Port: 18319, GroupEnabled: true},
	}); err != nil {
		t.Fatalf("write accounts: %v", err)
	}
	if err := store.WriteSettings(ctx, map[string]any{
		"account_failover.stale_after_seconds": 120,
	}); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if err := store.WriteRuntimeState(ctx, RuntimeStateName, RuntimeState{
		Version: RuntimeStateVersion, Mode: ModeActive, LastError: "quota stale",
		Accounts: map[string]AccountState{
			"alpha": {Account: "alpha", Eligible: true, Headroom: 80, ObservedAt: 990, Reason: "available"},
		},
	}); err != nil {
		t.Fatalf("write runtime state: %v", err)
	}
	provider := RuntimeStateProvider{Store: store, Now: func() time.Time { return time.Unix(1000, 0) }}
	states, err := provider.AccountStates(ctx)
	if err != nil {
		t.Fatalf("AccountStates: %v", err)
	}
	if states["alpha"].Eligible || states["alpha"].Reason != "quota_unavailable" {
		t.Fatalf("errored alpha state = %#v", states["alpha"])
	}
}

func TestUsageActivityReadsOneHourCountsFromExistingDatabase(t *testing.T) {
	root := t.TempDir()
	stateDirectory := filepath.Join(root, "state")
	if err := os.MkdirAll(stateDirectory, 0o700); err != nil {
		t.Fatalf("create state directory: %v", err)
	}
	database, err := sql.Open("sqlite", filepath.Join(stateDirectory, "usage.sqlite3"))
	if err != nil {
		t.Fatalf("open usage fixture: %v", err)
	}
	if _, err := database.Exec(`
        CREATE TABLE usage_meta (
            key TEXT PRIMARY KEY,
            value TEXT NOT NULL
        );
        CREATE TABLE usage_events (
            id INTEGER PRIMARY KEY,
            account TEXT NOT NULL,
            user_email TEXT NOT NULL,
            occurred_at INTEGER NOT NULL,
            model TEXT NOT NULL DEFAULT '',
            alias TEXT NOT NULL DEFAULT '',
            reasoning_effort TEXT NOT NULL DEFAULT '',
            failed INTEGER NOT NULL DEFAULT 0,
            input_tokens INTEGER NOT NULL DEFAULT 0,
            output_tokens INTEGER NOT NULL DEFAULT 0,
            reasoning_tokens INTEGER NOT NULL DEFAULT 0,
            cached_tokens INTEGER NOT NULL DEFAULT 0,
            total_tokens INTEGER NOT NULL DEFAULT 0,
            weighted_tokens INTEGER NOT NULL DEFAULT 0,
            weight_policy_version TEXT NOT NULL DEFAULT 'legacy-v1'
        );
        INSERT INTO usage_events(account, user_email, occurred_at) VALUES
            ('alpha', 'a@example.com', 990),
            ('alpha', 'a@example.com', 995),
            ('alpha', 'b@example.com', 999),
            ('beta', 'old@example.com', -3000);
        PRAGMA user_version = 10;`); err != nil {
		database.Close()
		t.Fatalf("create usage fixture: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("close usage fixture: %v", err)
	}
	activity, err := OpenUsageActivity(root, func() time.Time { return time.Unix(1000, 0) })
	if err != nil {
		t.Fatalf("OpenUsageActivity: %v", err)
	}
	defer activity.Close()
	counts, err := activity.RefreshActiveUsersLastHour(context.Background())
	if err != nil {
		t.Fatalf("RefreshActiveUsersLastHour: %v", err)
	}
	if !reflect.DeepEqual(counts, map[string]int{"alpha": 2}) {
		t.Fatalf("one-hour activity = %#v", counts)
	}
}

func TestAuthSnapshotPublisherWritesCompatibleSnapshotAndWaitsForActivation(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	path := filepath.Join(store.Root(), authSnapshotRelativePath)
	probe := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		raw, err := os.ReadFile(path)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusServiceUnavailable)
			return
		}
		var snapshot gateway.AuthSnapshot
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(writer).Encode(gateway.Status{Auth: gateway.SnapshotKindStatus{
			ActiveGeneration: snapshot.Generation,
		}})
	}))
	defer probe.Close()
	publisher := &AuthSnapshotPublisher{
		Root: store.Root(), Store: store, ProbeURLs: []string{probe.URL},
		HTTPClient: probe.Client(), WaitTimeout: time.Second, Fence: &testWriteFence{},
	}
	result, err := publisher.PublishAuthSnapshot(context.Background(), true)
	if err != nil {
		t.Fatalf("PublishAuthSnapshot: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open auth snapshot: %v", err)
	}
	snapshot, err := gateway.ParseAuthSnapshot(file)
	file.Close()
	if err != nil {
		t.Fatalf("parse auth snapshot: %v", err)
	}
	if snapshot.Generation != result.Generation || len(snapshot.Records) != 4 {
		t.Fatalf("published snapshot = %#v", snapshot)
	}
	for _, record := range snapshot.Records {
		if record.Account != "alpha" || record.Backend != "cliproxy-alpha:8317" || !validInternalKey(record.InternalKey) {
			t.Fatalf("published auth record = %#v", record)
		}
	}
}

func TestAuthSnapshotPublisherRejectsStaleLeaseBeforeAtomicReplace(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	sentinel := errors.New("lease generation lost")
	fence := &testWriteFence{err: sentinel}
	publisher := &AuthSnapshotPublisher{Root: store.Root(), Store: store, Fence: fence}
	if _, err := publisher.PublishAuthSnapshot(context.Background(), false); !errors.Is(err, sentinel) {
		t.Fatalf("stale auth snapshot error = %v", err)
	}
	if fence.calls != 1 {
		t.Fatalf("auth snapshot fence calls = %d", fence.calls)
	}
	if _, err := os.Stat(filepath.Join(store.Root(), authSnapshotRelativePath)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale auth publisher created snapshot: %v", err)
	}
}

func TestAuthSnapshotPublisherReadsAndReplacesUnderRealStoreFence(t *testing.T) {
	store := seedRebalanceStore(t)
	defer store.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	runtimeLease, err := store.TakeLease(ctx, "runtime-writer", "go-v2", 30*time.Second)
	if err != nil {
		t.Fatalf("take runtime lease: %v", err)
	}
	workerLease, err := store.TakeLease(ctx, "admin", "go-v2:admin", 30*time.Second)
	if err != nil {
		t.Fatalf("take worker lease: %v", err)
	}
	if err := store.InstallWriteFence(runtimeLease, workerLease); err != nil {
		t.Fatalf("install Store fence: %v", err)
	}
	publisher := &AuthSnapshotPublisher{Root: store.Root(), Store: store, Fence: store}
	result, err := publisher.PublishAuthSnapshot(ctx, false)
	if err != nil || len(result.Generation) != 32 {
		t.Fatalf("fenced PublishAuthSnapshot = (%#v, %v)", result, err)
	}
}
