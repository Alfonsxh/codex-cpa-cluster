package runtimeops

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestJobManagerBoundsConcurrencyReusesDuplicatesAndSerializesAll(t *testing.T) {
	controller := &blockingController{started: make(chan string, 4), release: make(chan struct{})}
	manager, err := newJobManager(controller, 1, 2, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)

	first, err := manager.Submit("restart", "alpha")
	if err != nil {
		t.Fatalf("submit first: %v", err)
	}
	awaitRuntimeValue(t, controller.started, "restart:alpha")
	reused, err := manager.Submit("restart", "alpha")
	if err != nil || !reused.Reused || reused.Job.ID != first.Job.ID {
		t.Fatalf("duplicate submission = (%#v, %v)", reused, err)
	}
	if _, err := manager.Submit("stop", "alpha"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("conflicting account job error = %v", err)
	}
	if _, err := manager.Submit("stop", "all"); !errors.Is(err, ErrJobConflict) {
		t.Fatalf("all conflict error = %v", err)
	}

	second, err := manager.Submit("up", "beta")
	if err != nil || second.Job.Action != "start" || second.Job.Status != "queued" {
		t.Fatalf("queued second = (%#v, %v)", second, err)
	}
	close(controller.release)
	awaitJobStatus(t, manager, first.Job.ID, "succeeded")
	awaitJobStatus(t, manager, second.Job.ID, "succeeded")
	if controller.maxRunning != 1 {
		t.Fatalf("maximum controller concurrency = %d", controller.maxRunning)
	}
}

func TestJobManagerCancelsQueuedAndRunningJobs(t *testing.T) {
	controller := &blockingController{started: make(chan string, 4), release: make(chan struct{})}
	manager, err := newJobManager(controller, 1, 2, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)

	running, _ := manager.Submit("restart", "alpha")
	awaitRuntimeValue(t, controller.started, "restart:alpha")
	queued, _ := manager.Submit("stop", "beta")
	if cancelled, err := manager.Cancel(queued.Job.ID); err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cancel queued = (%#v, %v)", cancelled, err)
	}
	awaitJobStatus(t, manager, queued.Job.ID, "cancelled")
	if cancelled, err := manager.Cancel(running.Job.ID); err != nil || cancelled.Status != "cancelling" {
		t.Fatalf("cancel running = (%#v, %v)", cancelled, err)
	}
	awaitJobStatus(t, manager, running.Job.ID, "cancelled")
	if _, err := manager.Cancel(running.Job.ID); !errors.Is(err, ErrJobFinished) {
		t.Fatalf("cancel finished error = %v", err)
	}
}

func TestJobManagerReportsFailuresRedactsErrorsAndRejectsFullQueue(t *testing.T) {
	controller := &blockingController{
		started: make(chan string, 4), release: make(chan struct{}),
		errors: map[string]error{"stop:beta": errors.New("Authorization: Bearer secret-token")},
	}
	manager, err := newJobManager(controller, 1, 1, 10)
	if err != nil {
		t.Fatalf("new job manager: %v", err)
	}
	t.Cleanup(manager.Close)
	running, _ := manager.Submit("restart", "alpha")
	awaitRuntimeValue(t, controller.started, "restart:alpha")
	failed, _ := manager.Submit("stop", "beta")
	if _, err := manager.Submit("start", "gamma"); !errors.Is(err, ErrJobQueueFull) {
		t.Fatalf("full queue error = %v", err)
	}
	close(controller.release)
	awaitJobStatus(t, manager, running.Job.ID, "succeeded")
	result := awaitJobStatus(t, manager, failed.Job.ID, "failed")
	if result.Error != "Authorization: Bearer [REDACTED]" {
		t.Fatalf("redacted job error = %q", result.Error)
	}
	if _, err := manager.Get("missing"); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("missing job error = %v", err)
	}
}

type blockingController struct {
	started chan string
	release chan struct{}
	errors  map[string]error

	mu         sync.Mutex
	running    int
	maxRunning int
}

func (controller *blockingController) Start(ctx context.Context, target string) (OperationResult, error) {
	return controller.run(ctx, "start", target)
}

func (controller *blockingController) Stop(ctx context.Context, target string) (OperationResult, error) {
	return controller.run(ctx, "stop", target)
}

func (controller *blockingController) Restart(ctx context.Context, target string) (OperationResult, error) {
	return controller.run(ctx, "restart", target)
}

func (controller *blockingController) run(ctx context.Context, action string, target string) (OperationResult, error) {
	controller.mu.Lock()
	controller.running++
	if controller.running > controller.maxRunning {
		controller.maxRunning = controller.running
	}
	controller.mu.Unlock()
	defer func() {
		controller.mu.Lock()
		controller.running--
		controller.mu.Unlock()
	}()
	key := action + ":" + target
	controller.started <- key
	select {
	case <-ctx.Done():
		return OperationResult{}, ctx.Err()
	case <-controller.release:
	}
	return OperationResult{Action: action, Target: target}, controller.errors[key]
}

func awaitRuntimeValue(t *testing.T, values <-chan string, expected string) {
	t.Helper()
	select {
	case value := <-values:
		if value != expected {
			t.Fatalf("runtime value = %q, want %q", value, expected)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %q", expected)
	}
}

func awaitJobStatus(t *testing.T, manager *JobManager, id string, expected string) Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		job, err := manager.Get(id)
		if err != nil {
			t.Fatalf("get job: %v", err)
		}
		if job.Status == expected {
			return job
		}
		time.Sleep(time.Millisecond)
	}
	job, _ := manager.Get(id)
	t.Fatalf("job status = %q, want %q", job.Status, expected)
	return Job{}
}
