package ownership

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
)

func TestRunJoinsRuntimeTakesWorkerAndReleasesWorker(t *testing.T) {
	store := &fakeLeaseStore{}
	operationCalled := false
	err := Run(context.Background(), store, Config{
		RuntimeOwner: "go-v2", WorkerScope: "quota", WorkerOwner: "go-v2:test",
		TTL: 5 * time.Second, RenewInterval: time.Second,
	}, func(context.Context, context.Context) error {
		operationCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !operationCalled || store.joined != 1 || store.taken != 1 || store.released != 1 {
		t.Fatalf(
			"calls = operation %v joined %d taken %d released %d",
			operationCalled,
			store.joined,
			store.taken,
			store.released,
		)
	}
}

func TestRunCancelsOperationWhenHeartbeatLosesLease(t *testing.T) {
	store := &fakeLeaseStore{renewError: controlplane.ErrLeaseLost}
	started := time.Now()
	err := Run(context.Background(), store, Config{
		RuntimeOwner: "go-v2", WorkerScope: "collector", WorkerOwner: "go-v2:test",
		TTL: 5 * time.Second, RenewInterval: time.Second,
	}, func(ctx context.Context, fence context.Context) error {
		<-ctx.Done()
		if fence.Err() == nil {
			t.Fatal("fence context remained active after lease loss")
		}
		return ctx.Err()
	})
	if !errors.Is(err, controlplane.ErrLeaseLost) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want lease loss and cancellation", err)
	}
	if time.Since(started) > 3*time.Second {
		t.Fatalf("lease loss cancellation took %s", time.Since(started))
	}
	if store.released != 0 {
		t.Fatalf("lost worker lease was released %d times", store.released)
	}
}

func TestRunDoesNotStartWithoutTransferredRuntimeOwnership(t *testing.T) {
	store := &fakeLeaseStore{joinError: controlplane.ErrLeaseMissing}
	called := false
	err := Run(context.Background(), store, Config{
		RuntimeOwner: "go-v2", WorkerScope: "notifications", WorkerOwner: "go-v2:test",
		TTL: 5 * time.Second, RenewInterval: time.Second,
	}, func(context.Context, context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, controlplane.ErrLeaseMissing) || called || store.taken != 0 {
		t.Fatalf("Run = error %v called %v taken %d", err, called, store.taken)
	}
}

func TestRunWithExistingStoreRequiresActivatedRuntimeBeforeOperation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seed, err := controlplane.Open(ctx, root, controlplane.Options{})
	if err != nil {
		t.Fatalf("seed control-plane target: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close seed target: %v", err)
	}
	config, err := WorkerConfig("go-v2", "admin", 30*time.Second)
	if err != nil {
		t.Fatalf("WorkerConfig: %v", err)
	}
	called := false
	err = RunWithExistingStore(
		ctx,
		root,
		controlplane.Options{},
		config,
		func(context.Context, context.Context, *controlplane.Store) error {
			called = true
			return nil
		},
	)
	if !errors.Is(err, controlplane.ErrLeaseMissing) {
		t.Fatalf("RunWithExistingStore error = %v, want ErrLeaseMissing", err)
	}
	if called {
		t.Fatal("operation ran before runtime ownership was activated")
	}

	activator, err := controlplane.OpenExisting(ctx, root, controlplane.Options{})
	if err != nil {
		t.Fatalf("open ownership activator: %v", err)
	}
	if _, err := activator.TakeLease(ctx, RuntimeScope, "go-v2", 30*time.Second); err != nil {
		t.Fatalf("activate runtime ownership: %v", err)
	}
	if err := activator.Close(); err != nil {
		t.Fatalf("close ownership activator: %v", err)
	}

	err = RunWithExistingStore(
		ctx,
		root,
		controlplane.Options{},
		config,
		func(runContext context.Context, _ context.Context, store *controlplane.Store) error {
			called = true
			return store.WriteRuntimeState(runContext, "owned-operation", map[string]any{"ok": true})
		},
	)
	if err != nil {
		t.Fatalf("RunWithExistingStore after activation: %v", err)
	}
	if !called {
		t.Fatal("owned operation did not run")
	}
}

func TestGoWorkerLeaseGroupTransfersAllScopesAndRejectsDuplicate(t *testing.T) {
	workerScopes := []string{
		"admin", "usage-collector", "quota", "account-failover", "notifications", "log-maintenance",
	}
	root := t.TempDir()
	var unixTime atomic.Int64
	unixTime.Store(1_000)
	now := func() time.Time { return time.Unix(unixTime.Load(), 0) }
	seed, err := controlplane.Open(context.Background(), root, controlplane.Options{Now: now})
	if err != nil {
		t.Fatalf("seed worker-group target: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close worker-group seed: %v", err)
	}
	activator, err := controlplane.OpenExisting(
		context.Background(), root, controlplane.Options{Now: now},
	)
	if err != nil {
		t.Fatalf("open worker-group activator: %v", err)
	}
	runtimeLease, err := activator.TakeLease(
		context.Background(), RuntimeScope, "go-v2", 5*time.Second,
	)
	if err != nil {
		t.Fatalf("activate worker-group runtime: %v", err)
	}
	if runtimeLease.Generation != 1 {
		t.Fatalf("initial runtime generation = %d", runtimeLease.Generation)
	}
	if err := activator.Close(); err != nil {
		t.Fatalf("close worker-group activator: %v", err)
	}

	groupContext, cancelGroup := context.WithCancel(context.Background())
	ready := make(chan string, len(workerScopes))
	results := make(chan error, len(workerScopes))
	for _, scope := range workerScopes {
		scope := scope
		config, err := WorkerConfig("go-v2", scope, 5*time.Second)
		if err != nil {
			t.Fatalf("worker config %s: %v", scope, err)
		}
		go func() {
			results <- RunWithExistingStore(
				groupContext,
				root,
				controlplane.Options{Now: now},
				config,
				func(runContext context.Context, _ context.Context, store *controlplane.Store) error {
					if err := store.WriteRuntimeState(
						runContext,
						"worker-rehearsal:"+scope,
						map[string]any{"scope": scope},
					); err != nil {
						return err
					}
					ready <- scope
					<-runContext.Done()
					return nil
				},
			)
		}()
	}
	seen := make(map[string]struct{}, len(workerScopes))
	for range workerScopes {
		select {
		case scope := <-ready:
			seen[scope] = struct{}{}
		case err := <-results:
			cancelGroup()
			t.Fatalf("worker exited before group became ready: %v", err)
		case <-time.After(5 * time.Second):
			cancelGroup()
			t.Fatal("worker lease group did not become ready")
		}
	}
	if len(seen) != len(workerScopes) {
		cancelGroup()
		t.Fatalf("ready worker scopes = %#v", seen)
	}

	duplicateCalled := false
	duplicateConfig, err := WorkerConfig("go-v2", "admin", 5*time.Second)
	if err != nil {
		cancelGroup()
		t.Fatalf("duplicate worker config: %v", err)
	}
	err = RunWithExistingStore(
		context.Background(),
		root,
		controlplane.Options{Now: now},
		duplicateConfig,
		func(context.Context, context.Context, *controlplane.Store) error {
			duplicateCalled = true
			return nil
		},
	)
	if !errors.Is(err, controlplane.ErrLeaseHeld) || duplicateCalled {
		cancelGroup()
		t.Fatalf("duplicate Admin worker = error %v called %v", err, duplicateCalled)
	}

	cancelGroup()
	for range workerScopes {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("worker-group shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("worker lease group did not stop")
		}
	}

	unixTime.Store(1_005)
	rollback, err := controlplane.OpenExisting(
		context.Background(), root, controlplane.Options{Now: now},
	)
	if err != nil {
		t.Fatalf("open worker-group rollback: %v", err)
	}
	defer rollback.Close()
	rolledRuntime, err := rollback.TakeLease(
		context.Background(), RuntimeScope, "python-v1", 5*time.Second,
	)
	if err != nil || rolledRuntime.Generation != 2 {
		t.Fatalf("rollback runtime lease = (%#v, %v)", rolledRuntime, err)
	}
	for _, scope := range workerScopes {
		lease, err := rollback.TakeLease(
			context.Background(), scope, "python-v1:"+scope, 5*time.Second,
		)
		if err != nil || lease.Generation != 2 {
			t.Fatalf("rollback worker %s = (%#v, %v)", scope, lease, err)
		}
	}
}

type fakeLeaseStore struct {
	mu         sync.Mutex
	joined     int
	taken      int
	renewed    int
	released   int
	joinError  error
	renewError error
}

func (store *fakeLeaseStore) JoinLease(
	_ context.Context,
	scope string,
	owner string,
	_ time.Duration,
) (controlplane.Lease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.joined++
	if store.joinError != nil {
		return controlplane.Lease{}, store.joinError
	}
	return testLease(scope, owner, 1), nil
}

func (store *fakeLeaseStore) TakeLease(
	_ context.Context,
	scope string,
	owner string,
	_ time.Duration,
) (controlplane.Lease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.taken++
	return testLease(scope, owner, 1), nil
}

func (store *fakeLeaseStore) RenewLease(
	_ context.Context,
	lease controlplane.Lease,
	_ time.Duration,
) (controlplane.Lease, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.renewed++
	if store.renewError != nil {
		return controlplane.Lease{}, store.renewError
	}
	return lease, nil
}

func (store *fakeLeaseStore) ReleaseLease(_ context.Context, _ controlplane.Lease) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.released++
	return nil
}

func testLease(scope string, owner string, generation int64) controlplane.Lease {
	return controlplane.Lease{
		Version: 1, Scope: scope, Owner: owner, Generation: generation,
		Token: "test-token", AcquiredAt: 1, RenewedAt: 1, ExpiresAt: 100,
	}
}
