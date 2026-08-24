package ownership

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/google/uuid"
)

const RuntimeScope = "runtime-writer"

type LeaseStore interface {
	JoinLease(context.Context, string, string, time.Duration) (controlplane.Lease, error)
	TakeLease(context.Context, string, string, time.Duration) (controlplane.Lease, error)
	RenewLease(context.Context, controlplane.Lease, time.Duration) (controlplane.Lease, error)
	ReleaseLease(context.Context, controlplane.Lease) error
}

type writeFenceInstaller interface {
	InstallWriteFence(controlplane.Lease, controlplane.Lease) error
}

type Config struct {
	RuntimeOwner  string
	WorkerScope   string
	WorkerOwner   string
	TTL           time.Duration
	RenewInterval time.Duration
	ReleaseWait   time.Duration
}

type heartbeatResult struct {
	workerLease controlplane.Lease
	err         error
}

// RunWithExistingStore opens only an already-initialized deployment target,
// acquires the shared runtime and exclusive worker leases, and only then lets
// the control-plane store perform schema validation/migration and secret
// compatibility work. This ordering prevents a standby Go process from
// changing shared state before ownership has been explicitly transferred.
func RunWithExistingStore(
	ctx context.Context,
	root string,
	options controlplane.Options,
	config Config,
	operation func(context.Context, context.Context, *controlplane.Store) error,
) error {
	if operation == nil {
		return errors.New("ownership operation is required")
	}
	store, err := controlplane.OpenExisting(ctx, root, options)
	if err != nil {
		return err
	}
	defer store.Close()
	return Run(ctx, store, config, func(runContext context.Context, fenceContext context.Context) error {
		if err := store.InitializeExisting(runContext); err != nil {
			return fmt.Errorf("initialize owned control-plane store: %w", err)
		}
		return operation(runContext, fenceContext, store)
	})
}

// Run holds both the explicitly transferred runtime-wide ownership and one
// exclusive worker lease for the duration of operation. Losing either fence
// cancels operation immediately. The runtime lease is deliberately not
// released by an individual worker because other workers from the same runtime
// may still be active; it expires only after the final member stops renewing.
func Run(
	ctx context.Context,
	store LeaseStore,
	config Config,
	operation func(context.Context, context.Context) error,
) error {
	if err := config.normalizeAndValidate(); err != nil {
		return err
	}
	if store == nil {
		return errors.New("ownership lease store is required")
	}
	if operation == nil {
		return errors.New("ownership operation is required")
	}

	runtimeLease, err := store.JoinLease(
		ctx,
		RuntimeScope,
		config.RuntimeOwner,
		config.TTL,
	)
	if err != nil {
		return fmt.Errorf("join runtime ownership: %w", err)
	}
	workerLease, err := store.TakeLease(
		ctx,
		config.WorkerScope,
		config.WorkerOwner,
		config.TTL,
	)
	if err != nil {
		return fmt.Errorf("take worker ownership: %w", err)
	}
	if installer, ok := store.(writeFenceInstaller); ok {
		if err := installer.InstallWriteFence(runtimeLease, workerLease); err != nil {
			releaseContext, cancelRelease := context.WithTimeout(
				context.WithoutCancel(ctx),
				config.ReleaseWait,
			)
			releaseError := store.ReleaseLease(releaseContext, workerLease)
			cancelRelease()
			return errors.Join(fmt.Errorf("install ownership write fence: %w", err), releaseError)
		}
	}

	operationContext, cancelOperation := context.WithCancel(ctx)
	defer cancelOperation()
	fenceContext, cancelFence := context.WithCancel(context.Background())
	defer cancelFence()
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan heartbeatResult, 1)
	go heartbeat(
		operationContext,
		store,
		config,
		runtimeLease,
		workerLease,
		stopHeartbeat,
		heartbeatDone,
		cancelOperation,
		cancelFence,
	)

	operationError := operation(operationContext, fenceContext)
	close(stopHeartbeat)
	heartbeatState := <-heartbeatDone

	var releaseError error
	if heartbeatState.err == nil {
		releaseContext, cancelRelease := context.WithTimeout(
			context.WithoutCancel(ctx),
			config.ReleaseWait,
		)
		releaseError = store.ReleaseLease(releaseContext, heartbeatState.workerLease)
		cancelRelease()
		if releaseError != nil {
			releaseError = fmt.Errorf("release worker ownership: %w", releaseError)
		}
	}
	return errors.Join(operationError, heartbeatState.err, releaseError)
}

func NewProcessOwner(runtime string) (string, error) {
	runtime = strings.TrimSpace(runtime)
	if runtime == "" || len(runtime) > 64 {
		return "", errors.New("runtime owner prefix must contain 1 to 64 characters")
	}
	return runtime + ":" + uuid.NewString(), nil
}

func WorkerConfig(runtimeOwner string, workerScope string, ttl time.Duration) (Config, error) {
	runtimeOwner = strings.TrimSpace(runtimeOwner)
	if runtimeOwner == "" {
		runtimeOwner = "go-v2"
	}
	if ttl == 0 {
		ttl = 30 * time.Second
	}
	workerOwner, err := NewProcessOwner(runtimeOwner + "-" + strings.TrimSpace(workerScope))
	if err != nil {
		return Config{}, err
	}
	config := Config{
		RuntimeOwner: runtimeOwner,
		WorkerScope:  workerScope,
		WorkerOwner:  workerOwner,
		TTL:          ttl,
	}
	if err := config.normalizeAndValidate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func heartbeat(
	ctx context.Context,
	store LeaseStore,
	config Config,
	runtimeLease controlplane.Lease,
	workerLease controlplane.Lease,
	stop <-chan struct{},
	done chan<- heartbeatResult,
	cancelOperation context.CancelFunc,
	cancelFence context.CancelFunc,
) {
	ticker := time.NewTicker(config.RenewInterval)
	defer ticker.Stop()
	result := heartbeatResult{workerLease: workerLease}
	defer func() { done <- result }()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			renewContext, cancelRenew := context.WithTimeout(
				context.WithoutCancel(ctx),
				config.RenewInterval,
			)
			var err error
			runtimeLease, err = store.RenewLease(renewContext, runtimeLease, config.TTL)
			if err == nil {
				workerLease, err = store.RenewLease(renewContext, workerLease, config.TTL)
				result.workerLease = workerLease
			}
			cancelRenew()
			if err != nil {
				result.err = fmt.Errorf("ownership heartbeat failed: %w", err)
				cancelFence()
				cancelOperation()
				return
			}
		}
	}
}

func (config *Config) normalizeAndValidate() error {
	config.RuntimeOwner = strings.TrimSpace(config.RuntimeOwner)
	config.WorkerScope = strings.TrimSpace(config.WorkerScope)
	config.WorkerOwner = strings.TrimSpace(config.WorkerOwner)
	if config.RuntimeOwner == "" {
		return errors.New("runtime owner is required")
	}
	if config.WorkerScope == "" {
		return errors.New("worker lease scope is required")
	}
	if config.WorkerOwner == "" {
		return errors.New("worker lease owner is required")
	}
	if config.TTL == 0 {
		config.TTL = 30 * time.Second
	}
	if config.RenewInterval == 0 {
		config.RenewInterval = config.TTL / 3
	}
	if config.ReleaseWait == 0 {
		config.ReleaseWait = 5 * time.Second
	}
	if config.TTL < 5*time.Second || config.TTL > 5*time.Minute {
		return errors.New("ownership TTL must be between five seconds and five minutes")
	}
	if config.RenewInterval < time.Second || config.RenewInterval >= config.TTL/2 {
		return errors.New("ownership renew interval must be at least one second and less than half the TTL")
	}
	if config.ReleaseWait < time.Second || config.ReleaseWait > 30*time.Second {
		return errors.New("ownership release wait must be between one and thirty seconds")
	}
	return nil
}
