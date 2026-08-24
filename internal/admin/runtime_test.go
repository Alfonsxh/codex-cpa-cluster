package admin

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/runtimeops"
)

func TestRuntimeAPIsRequireAdminAndUseFineGrainedCatalogAndLogs(t *testing.T) {
	server, _ := newTestAdmin(t)
	runtime := &fakeRuntimeCatalog{
		services: []runtimeops.Service{{Service: "cliproxy-alpha", ContainerID: "aaaaaaaaaaaa", State: "running"}},
		logs:     runtimeops.LogsResult{Target: "alpha", Output: "Bearer [REDACTED]", Truncated: true},
	}
	server.runtime = runtime
	server.runtimeJobs = &fakeRuntimeJobs{}

	response := performAdminRequest(server, http.MethodGet, "/admin/api/runtime/services", nil, nil, nil)
	assertAdminError(t, response, http.StatusUnauthorized, "unauthorized")
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/runtime/services", nil, headers, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"service":"cliproxy-alpha"`) ||
		strings.Contains(response.Body.String(), strings.Repeat("a", 64)) {
		t.Fatalf("runtime services = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/runtime/logs?target=alpha", nil, headers, nil)
	if response.Code != http.StatusOK || runtime.logTarget != "alpha" ||
		!strings.Contains(response.Body.String(), "[REDACTED]") || !strings.Contains(response.Body.String(), `"truncated":true`) ||
		!strings.Contains(response.Body.String(), `"exit_code":0`) {
		t.Fatalf("runtime logs = %d %s, target=%q", response.Code, response.Body.String(), runtime.logTarget)
	}
}

func TestRuntimeJobAPIsRequireExactConfirmationAndKeepLegacyAliases(t *testing.T) {
	server, _ := newTestAdmin(t)
	jobs := &fakeRuntimeJobs{submission: runtimeops.JobSubmission{Job: runtimeops.Job{
		ID: "job-1", Name: "重启服务", Action: "restart", Target: "alpha", Status: "queued", CreatedAt: 100,
	}}}
	server.runtime = &fakeRuntimeCatalog{}
	server.runtimeJobs = jobs
	headers := map[string]string{"X-Management-Key": "test-management-key"}

	response := performAdminRequest(server, http.MethodPost, "/admin/api/runtime/jobs", map[string]any{
		"action": "restart", "target": "alpha", "confirm": "wrong",
	}, headers, nil)
	assertAdminError(t, response, http.StatusBadRequest, "confirmation_required")
	if jobs.submitCalls != 0 {
		t.Fatal("runtime job was submitted without exact confirmation")
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/runtime/jobs", map[string]any{
		"action": "restart", "target": "alpha", "confirm": "restart:alpha",
	}, headers, nil)
	if response.Code != http.StatusAccepted || jobs.submitCalls != 1 || jobs.action != "restart" || jobs.target != "alpha" ||
		!strings.Contains(response.Body.String(), `"id":"job-1"`) {
		t.Fatalf("submit runtime job = %d %s, jobs=%#v", response.Code, response.Body.String(), jobs)
	}

	response = performAdminRequest(server, http.MethodPost, "/admin/api/operations", map[string]any{
		"action": "up", "target": "alpha",
	}, headers, nil)
	if response.Code != http.StatusAccepted || jobs.action != "up" {
		t.Fatalf("legacy operation alias = %d %s", response.Code, response.Body.String())
	}

	jobs.recent = []runtimeops.Job{jobs.submission.Job}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/runtime/jobs?limit=1", nil, headers, nil)
	if response.Code != http.StatusOK || jobs.recentLimit != 1 || !strings.Contains(response.Body.String(), `"job-1"`) {
		t.Fatalf("runtime job list = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/jobs/job-1", nil, headers, nil)
	if response.Code != http.StatusOK || jobs.getID != "job-1" {
		t.Fatalf("legacy job read = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/runtime/jobs/job-1/cancel", map[string]any{}, headers, nil)
	if response.Code != http.StatusOK || jobs.cancelID != "job-1" {
		t.Fatalf("runtime cancel = %d %s", response.Code, response.Body.String())
	}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/jobs/cancel", map[string]any{"id": "job-1"}, headers, nil)
	if response.Code != http.StatusOK || jobs.cancelID != "job-1" {
		t.Fatalf("legacy cancel = %d %s", response.Code, response.Body.String())
	}
}

func TestRuntimeAPIsFailClosedWhenUnavailableAndMapBoundedQueueErrors(t *testing.T) {
	server, _ := newTestAdmin(t)
	headers := map[string]string{"X-Management-Key": "test-management-key"}
	response := performAdminRequest(server, http.MethodGet, "/admin/api/runtime/services", nil, headers, nil)
	assertAdminError(t, response, http.StatusServiceUnavailable, "runtime_not_ready")

	server.runtime = &fakeRuntimeCatalog{}
	server.runtimeJobs = &fakeRuntimeJobs{submitError: runtimeops.ErrJobQueueFull}
	response = performAdminRequest(server, http.MethodPost, "/admin/api/runtime/jobs", map[string]any{
		"action": "stop", "target": "alpha", "confirm": "stop:alpha",
	}, headers, nil)
	assertAdminError(t, response, http.StatusTooManyRequests, "job_queue_full")
	if response.Header().Get("Retry-After") != "1" {
		t.Fatalf("runtime queue Retry-After = %q", response.Header().Get("Retry-After"))
	}

	server.runtimeJobs = &fakeRuntimeJobs{getError: runtimeops.ErrJobNotFound}
	response = performAdminRequest(server, http.MethodGet, "/admin/api/runtime/jobs/missing", nil, headers, nil)
	assertAdminError(t, response, http.StatusNotFound, "job_not_found")
}

type fakeRuntimeCatalog struct {
	services  []runtimeops.Service
	logs      runtimeops.LogsResult
	listError error
	logsError error
	logTarget string
}

func (runtime *fakeRuntimeCatalog) List(context.Context) ([]runtimeops.Service, error) {
	return append([]runtimeops.Service(nil), runtime.services...), runtime.listError
}

func (runtime *fakeRuntimeCatalog) Logs(_ context.Context, target string) (runtimeops.LogsResult, error) {
	runtime.logTarget = target
	return runtime.logs, runtime.logsError
}

type fakeRuntimeJobs struct {
	submission  runtimeops.JobSubmission
	submitError error
	getError    error
	cancelError error
	recent      []runtimeops.Job
	submitCalls int
	action      string
	target      string
	getID       string
	cancelID    string
	recentLimit int
}

func (jobs *fakeRuntimeJobs) Submit(action string, target string) (runtimeops.JobSubmission, error) {
	jobs.submitCalls++
	jobs.action = action
	jobs.target = target
	return jobs.submission, jobs.submitError
}

func (jobs *fakeRuntimeJobs) Get(id string) (runtimeops.Job, error) {
	jobs.getID = id
	return jobs.submission.Job, jobs.getError
}

func (jobs *fakeRuntimeJobs) Recent(limit int) []runtimeops.Job {
	jobs.recentLimit = limit
	return append([]runtimeops.Job(nil), jobs.recent...)
}

func (jobs *fakeRuntimeJobs) Cancel(id string) (runtimeops.Job, error) {
	jobs.cancelID = id
	return jobs.submission.Job, jobs.cancelError
}

var _ RuntimeCatalog = (*fakeRuntimeCatalog)(nil)
var _ RuntimeJobService = (*fakeRuntimeJobs)(nil)
