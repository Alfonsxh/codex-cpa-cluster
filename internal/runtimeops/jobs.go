package runtimeops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/alitto/pond/v2"
	"github.com/google/uuid"
)

const (
	defaultJobConcurrency = 1
	defaultJobQueueSize   = 16
	defaultJobHistory     = 60
)

var (
	ErrJobQueueFull = errors.New("runtime job queue is full")
	ErrJobConflict  = errors.New("a conflicting runtime job is already active")
	ErrJobNotFound  = errors.New("runtime job was not found")
	ErrJobFinished  = errors.New("runtime job is already finished")
)

type Controller interface {
	Start(context.Context, string) (OperationResult, error)
	Stop(context.Context, string) (OperationResult, error)
	Restart(context.Context, string) (OperationResult, error)
}

type Job struct {
	ID         string           `json:"id"`
	Name       string           `json:"name"`
	Action     string           `json:"action"`
	Target     string           `json:"target"`
	Status     string           `json:"status"`
	CreatedAt  int64            `json:"created_at"`
	StartedAt  *int64           `json:"started_at,omitempty"`
	FinishedAt *int64           `json:"finished_at,omitempty"`
	Result     *OperationResult `json:"result,omitempty"`
	Error      string           `json:"error,omitempty"`
	cancel     context.CancelFunc
}

type JobSubmission struct {
	Job    Job  `json:"job"`
	Reused bool `json:"reused"`
}

type JobManager struct {
	controller Controller
	pool       pond.Pool
	ctx        context.Context
	cancel     context.CancelFunc
	now        func() time.Time
	newID      func() string
	history    int

	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string
	active map[string]string
}

func NewJobManager(controller Controller) (*JobManager, error) {
	return newJobManager(controller, defaultJobConcurrency, defaultJobQueueSize, defaultJobHistory)
}

func newJobManager(controller Controller, concurrency int, queueSize int, history int) (*JobManager, error) {
	if controller == nil {
		return nil, errors.New("runtime jobs require a controller")
	}
	if concurrency <= 0 || queueSize <= 0 || history <= 0 {
		return nil, errors.New("runtime job limits must be positive")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &JobManager{
		controller: controller,
		pool: pond.NewPool(
			concurrency,
			pond.WithContext(ctx),
			pond.WithQueueSize(queueSize),
			pond.WithNonBlocking(true),
		),
		ctx: ctx, cancel: cancel, now: time.Now,
		newID: uuid.NewString, history: history,
		jobs: make(map[string]*Job), active: make(map[string]string),
	}, nil
}

func (manager *JobManager) Submit(action string, target string) (JobSubmission, error) {
	action = strings.ToLower(strings.TrimSpace(action))
	target = normalizeTarget(target)
	if action != "start" && action != "up" && action != "stop" && action != "restart" {
		return JobSubmission{}, fmt.Errorf("%w: unsupported action %s", ErrRuntimeTarget, action)
	}
	if action == "up" {
		action = "start"
	}

	manager.mu.Lock()
	if manager.pool.Stopped() {
		manager.mu.Unlock()
		return JobSubmission{}, pond.ErrPoolStopped
	}
	if existingID, conflict := manager.findConflictLocked(action, target); conflict {
		existing := cloneJob(manager.jobs[existingID])
		manager.mu.Unlock()
		if existing.Action == action && existing.Target == target {
			return JobSubmission{Job: existing, Reused: true}, nil
		}
		return JobSubmission{}, fmt.Errorf("%w: job %s is %s %s", ErrJobConflict, existing.ID, existing.Action, existing.Target)
	}

	jobContext, cancel := context.WithCancel(manager.ctx)
	job := &Job{
		ID: manager.newID(), Name: jobName(action), Action: action, Target: target,
		Status: "queued", CreatedAt: manager.now().Unix(), cancel: cancel,
	}
	manager.jobs[job.ID] = job
	manager.order = append(manager.order, job.ID)
	manager.active[target] = job.ID
	task, accepted := manager.pool.TrySubmitErr(func() error {
		return manager.execute(jobContext, job.ID)
	})
	if !accepted {
		delete(manager.jobs, job.ID)
		manager.order = manager.order[:len(manager.order)-1]
		delete(manager.active, target)
		cancel()
		manager.mu.Unlock()
		return JobSubmission{}, ErrJobQueueFull
	}
	created := cloneJob(job)
	manager.mu.Unlock()

	go func() {
		manager.finish(job.ID, task.Wait())
	}()
	return JobSubmission{Job: created}, nil
}

func (manager *JobManager) Get(id string) (Job, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job, found := manager.jobs[strings.TrimSpace(id)]
	if !found {
		return Job{}, ErrJobNotFound
	}
	return cloneJob(job), nil
}

func (manager *JobManager) Recent(limit int) []Job {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if limit <= 0 || limit > manager.history {
		limit = manager.history
	}
	result := make([]Job, 0, min(limit, len(manager.order)))
	for index := len(manager.order) - 1; index >= 0 && len(result) < limit; index-- {
		if job := manager.jobs[manager.order[index]]; job != nil {
			result = append(result, cloneJob(job))
		}
	}
	return result
}

func (manager *JobManager) Cancel(id string) (Job, error) {
	manager.mu.Lock()
	job, found := manager.jobs[strings.TrimSpace(id)]
	if !found {
		manager.mu.Unlock()
		return Job{}, ErrJobNotFound
	}
	if isTerminalJobStatus(job.Status) {
		finished := cloneJob(job)
		manager.mu.Unlock()
		return finished, ErrJobFinished
	}
	if job.Status == "queued" {
		finishedAt := manager.now().Unix()
		job.Status = "cancelled"
		job.FinishedAt = &finishedAt
		delete(manager.active, job.Target)
		job.cancel()
		manager.trimLocked()
	} else if job.Status != "cancelling" {
		job.Status = "cancelling"
		job.cancel()
	}
	result := cloneJob(job)
	manager.mu.Unlock()
	return result, nil
}

func (manager *JobManager) Close() {
	if manager == nil {
		return
	}
	manager.cancel()
	manager.pool.StopAndWait()
}

func (manager *JobManager) execute(ctx context.Context, id string) error {
	manager.mu.Lock()
	job := manager.jobs[id]
	if job == nil {
		manager.mu.Unlock()
		return ErrJobNotFound
	}
	if err := ctx.Err(); err != nil {
		manager.mu.Unlock()
		return err
	}
	startedAt := manager.now().Unix()
	job.StartedAt = &startedAt
	job.Status = "running"
	action, target := job.Action, job.Target
	manager.mu.Unlock()

	var (
		result OperationResult
		err    error
	)
	switch action {
	case "start":
		result, err = manager.controller.Start(ctx, target)
	case "stop":
		result, err = manager.controller.Stop(ctx, target)
	case "restart":
		result, err = manager.controller.Restart(ctx, target)
	default:
		err = fmt.Errorf("%w: unsupported action %s", ErrRuntimeTarget, action)
	}
	if err == nil {
		manager.mu.Lock()
		if job = manager.jobs[id]; job != nil {
			cloned := result
			job.Result = &cloned
		}
		manager.mu.Unlock()
	}
	return err
}

func (manager *JobManager) finish(id string, err error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	job := manager.jobs[id]
	if job == nil || isTerminalJobStatus(job.Status) {
		return
	}
	finishedAt := manager.now().Unix()
	job.FinishedAt = &finishedAt
	if errors.Is(err, context.Canceled) {
		job.Status = "cancelled"
	} else if err != nil {
		job.Status = "failed"
		job.Error = Sanitize(err.Error())
	} else {
		job.Status = "succeeded"
	}
	delete(manager.active, job.Target)
	manager.trimLocked()
}

func (manager *JobManager) findConflictLocked(action string, target string) (string, bool) {
	if existing, found := manager.active[target]; found {
		return existing, true
	}
	if target == "all" {
		ids := make([]string, 0, len(manager.active))
		for _, id := range manager.active {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) > 0 {
			return ids[0], true
		}
		return "", false
	}
	if existing, found := manager.active["all"]; found {
		return existing, true
	}
	return "", false
}

func (manager *JobManager) trimLocked() {
	for len(manager.order) > manager.history {
		oldest := manager.order[0]
		manager.order = manager.order[1:]
		job := manager.jobs[oldest]
		if job != nil && !isTerminalJobStatus(job.Status) {
			manager.order = append(manager.order, oldest)
			continue
		}
		delete(manager.jobs, oldest)
	}
}

func jobName(action string) string {
	switch action {
	case "start":
		return "启动服务"
	case "stop":
		return "停止服务"
	case "restart":
		return "重启服务"
	default:
		return "运行维护"
	}
}

func cloneJob(job *Job) Job {
	if job == nil {
		return Job{}
	}
	cloned := *job
	cloned.cancel = nil
	if job.StartedAt != nil {
		value := *job.StartedAt
		cloned.StartedAt = &value
	}
	if job.FinishedAt != nil {
		value := *job.FinishedAt
		cloned.FinishedAt = &value
	}
	if job.Result != nil {
		result := *job.Result
		result.Services = append([]Service(nil), job.Result.Services...)
		cloned.Result = &result
	}
	return cloned
}

func isTerminalJobStatus(status string) bool {
	return status == "succeeded" || status == "failed" || status == "cancelled"
}
