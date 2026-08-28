package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/runtimeops"
	"github.com/alitto/pond/v2"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const maxRuntimeJobsResponse = 60

type RuntimeCatalog interface {
	List(context.Context) ([]runtimeops.Service, error)
	Logs(context.Context, string) (runtimeops.LogsResult, error)
}

type ImageCatalog interface {
	CPAImageStatus(context.Context) (runtimeops.CPAImageStatus, error)
}

type RuntimeJobService interface {
	Submit(string, string) (runtimeops.JobSubmission, error)
	Get(string) (runtimeops.Job, error)
	Recent(int) []runtimeops.Job
	Cancel(string) (runtimeops.Job, error)
}

type runtimeJobRequest struct {
	Action  string `json:"action"`
	Target  string `json:"target"`
	Confirm string `json:"confirm"`
}

type cancelLegacyRuntimeJobRequest struct {
	ID string `json:"id"`
}

// legacyRuntimeJob preserves the stable compatibility job shape. Output is an
// array of complete lines, action/result are absent, and timestamps/exit code
// remain explicit nullable fields. The namespaced /runtime/jobs API exposes
// the native Go job shape with one bounded output string.
type legacyRuntimeJob struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Target     string    `json:"target"`
	Status     string    `json:"status"`
	CreatedAt  int64     `json:"created_at"`
	StartedAt  *int64    `json:"started_at"`
	FinishedAt *int64    `json:"finished_at"`
	ExitCode   *int      `json:"exit_code"`
	Output     *[]string `json:"output,omitempty"`
}

type operationImpactResponse struct {
	Action      string `json:"action"`
	Target      string `json:"target"`
	TargetType  string `json:"target_type"`
	RoutedUsers *int   `json:"routed_users"`
}

var stoppableServiceTargets = map[string]struct{}{
	"web": {}, "management": {}, "usage-collector": {}, "log-maintenance": {},
}

func (server *Server) readOperationImpact(c *gin.Context) {
	action := strings.ToLower(strings.TrimSpace(c.Query("action")))
	target := strings.ToLower(strings.TrimSpace(c.Query("target")))
	if target == "" {
		target = "all"
	}
	if action != "stop" {
		writeError(c, http.StatusBadRequest, "只支持查询停止操作影响", "invalid_runtime_target")
		return
	}
	accounts, err := server.store.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read account catalog for operation impact", err)
		return
	}
	accountIDs := make(map[string]struct{}, len(accounts))
	for _, account := range accounts {
		accountIDs[account.ID] = struct{}{}
	}
	targetType := ""
	switch {
	case target == "all":
		targetType = "all"
	case func() bool { _, found := accountIDs[target]; return found }():
		targetType = "account"
	case func() bool { _, found := stoppableServiceTargets[target]; return found }():
		targetType = "service"
	default:
		writeError(c, http.StatusBadRequest, "未知操作目标", "invalid_runtime_target")
		return
	}
	response := operationImpactResponse{Action: action, Target: target, TargetType: targetType}
	if targetType == "service" {
		c.JSON(http.StatusOK, response)
		return
	}
	routes, err := server.store.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read routes for operation impact", err)
		return
	}
	count := 0
	for _, accountID := range routes {
		if targetType == "all" {
			if _, found := accountIDs[accountID]; found {
				count++
			}
		} else if accountID == target {
			count++
		}
	}
	response.RoutedUsers = &count
	c.JSON(http.StatusOK, response)
}

func (server *Server) listRuntimeServices(c *gin.Context) {
	if !server.requireRuntime(c, false) {
		return
	}
	services, err := server.runtime.List(c.Request.Context())
	if err != nil {
		server.runtimeUnavailable(c, "list runtime services", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"services": services})
}

func (server *Server) readCPAImageStatus(c *gin.Context) {
	if server.images == nil {
		writeError(c, http.StatusServiceUnavailable, "镜像查询服务尚未就绪", "runtime_not_ready")
		return
	}
	status, err := server.images.CPAImageStatus(c.Request.Context())
	if err != nil {
		server.runtimeUnavailable(c, "read CPA image status", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"target_image": status.TargetImage, "update_channel": status.UpdateChannel,
		"candidate": status.Candidate, "applied": status.Applied, "local_image": status.LocalImage,
		"accounts": status.Accounts, "running_count": status.RunningCount,
		"current_count": status.CurrentCount, "outdated_count": status.OutdatedCount, "cached": false,
	})
}

func (server *Server) readRuntimeLogs(c *gin.Context) {
	if !server.requireRuntime(c, false) {
		return
	}
	result, err := server.runtime.Logs(c.Request.Context(), c.Query("target"))
	if err != nil {
		server.writeRuntimeError(c, "read runtime logs", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (server *Server) listRuntimeJobs(c *gin.Context) {
	server.listRuntimeJobsWithShape(c, false)
}

func (server *Server) listLegacyRuntimeJobs(c *gin.Context) {
	server.listRuntimeJobsWithShape(c, true)
}

func (server *Server) listRuntimeJobsWithShape(c *gin.Context, legacy bool) {
	if !server.requireRuntime(c, true) {
		return
	}
	limit := 30
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxRuntimeJobsResponse {
			writeError(c, http.StatusBadRequest, "任务数量范围无效", "invalid_request")
			return
		}
		limit = parsed
	}
	jobs := server.runtimeJobs.Recent(limit)
	if legacy {
		legacyJobs := make([]legacyRuntimeJob, 0, len(jobs))
		for _, job := range jobs {
			legacyJobs = append(legacyJobs, toLegacyRuntimeJob(job, false))
		}
		c.JSON(http.StatusOK, gin.H{"jobs": legacyJobs})
		return
	}
	c.JSON(http.StatusOK, gin.H{"jobs": jobs})
}

func (server *Server) readRuntimeJob(c *gin.Context) {
	server.readRuntimeJobWithShape(c, false)
}

func (server *Server) readLegacyRuntimeJob(c *gin.Context) {
	server.readRuntimeJobWithShape(c, true)
}

func (server *Server) readRuntimeJobWithShape(c *gin.Context, legacy bool) {
	if !server.requireRuntime(c, true) {
		return
	}
	job, err := server.runtimeJobs.Get(c.Param("id"))
	if err != nil {
		server.writeRuntimeError(c, "read runtime job", err)
		return
	}
	if legacy {
		c.JSON(http.StatusOK, gin.H{"job": toLegacyRuntimeJob(job, true)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"job": job})
}

func (server *Server) submitConfirmedRuntimeJob(c *gin.Context) {
	server.submitRuntimeJob(c, true, false)
}

func (server *Server) submitLegacyRuntimeJob(c *gin.Context) {
	server.submitRuntimeJob(c, false, true)
}

func (server *Server) submitRuntimeJob(c *gin.Context, requireConfirmation bool, legacy bool) {
	if !server.requireRuntime(c, true) {
		return
	}
	var body runtimeJobRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "运行操作请求无效", "invalid_request")
		return
	}
	action := strings.ToLower(strings.TrimSpace(body.Action))
	target := strings.ToLower(strings.TrimSpace(body.Target))
	if target == "" {
		target = "all"
	}
	if action == "image-pull" {
		target = "all"
	}
	if requireConfirmation && body.Confirm != action+":"+target {
		writeError(c, http.StatusBadRequest, "请确认运行操作及目标", "confirmation_required")
		return
	}
	submission, err := server.runtimeJobs.Submit(action, target)
	if err != nil {
		server.writeRuntimeError(c, "submit runtime job", err)
		return
	}
	message := "任务已提交"
	status := http.StatusAccepted
	if submission.Reused {
		message = "已有相同任务，已直接打开"
		status = http.StatusOK
	}
	job := any(submission.Job)
	if legacy {
		job = toLegacyRuntimeJob(submission.Job, true)
	}
	c.JSON(status, gin.H{"message": message, "job": job, "reused": submission.Reused})
}

func (server *Server) cancelRuntimeJob(c *gin.Context) {
	if !server.requireRuntime(c, true) {
		return
	}
	server.cancelRuntimeJobID(c, c.Param("id"))
}

func (server *Server) cancelLegacyRuntimeJob(c *gin.Context) {
	if !server.requireRuntime(c, true) {
		return
	}
	var body cancelLegacyRuntimeJobRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "取消任务请求无效", "invalid_request")
		return
	}
	server.cancelRuntimeJobID(c, body.ID, true)
}

func (server *Server) cancelRuntimeJobID(c *gin.Context, id string, legacy ...bool) {
	job, err := server.runtimeJobs.Cancel(id)
	if err != nil {
		server.writeRuntimeError(c, "cancel runtime job", err)
		return
	}
	responseJob := any(job)
	if len(legacy) > 0 && legacy[0] {
		responseJob = toLegacyRuntimeJob(job, true)
	}
	c.JSON(http.StatusOK, gin.H{"message": "任务取消请求已提交", "job": responseJob})
}

func toLegacyRuntimeJob(job runtimeops.Job, includeOutput bool) legacyRuntimeJob {
	var exitCode *int
	switch job.Status {
	case "succeeded":
		value := 0
		exitCode = &value
	case "failed":
		value := 1
		exitCode = &value
	case "cancelled":
		value := -15
		exitCode = &value
	}
	result := legacyRuntimeJob{
		ID: job.ID, Name: job.Name, Target: job.Target, Status: job.Status,
		CreatedAt: job.CreatedAt, StartedAt: job.StartedAt, FinishedAt: job.FinishedAt,
		ExitCode: exitCode,
	}
	if includeOutput {
		lines := make([]string, 0)
		if output := strings.TrimRight(strings.ReplaceAll(job.Output, "\r\n", "\n"), "\r\n"); output != "" {
			lines = strings.Split(output, "\n")
		}
		result.Output = &lines
	}
	return result
}

func (server *Server) requireRuntime(c *gin.Context, jobs bool) bool {
	if server.runtime == nil || (jobs && server.runtimeJobs == nil) {
		writeError(c, http.StatusServiceUnavailable, "运行维护服务尚未就绪", "runtime_not_ready")
		return false
	}
	return true
}

func (server *Server) runtimeUnavailable(c *gin.Context, operation string, err error) {
	server.logger.Warn(operation, zap.String("error", runtimeops.Sanitize(err.Error())))
	writeError(c, http.StatusServiceUnavailable, "Docker 运行时暂时不可用", "runtime_unavailable")
}

func (server *Server) writeRuntimeError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, runtimeops.ErrJobNotFound):
		writeError(c, http.StatusNotFound, "运行任务不存在", "job_not_found")
	case errors.Is(err, runtimeops.ErrJobFinished):
		writeError(c, http.StatusConflict, "运行任务已经结束", "job_finished")
	case errors.Is(err, runtimeops.ErrJobConflict):
		writeError(c, http.StatusConflict, "已有冲突的运行任务", "job_conflict")
	case errors.Is(err, runtimeops.ErrJobQueueFull), errors.Is(err, pond.ErrQueueFull):
		c.Header("Retry-After", "1")
		writeError(c, http.StatusTooManyRequests, "运行任务队列已满", "job_queue_full")
	case errors.Is(err, pond.ErrPoolStopped):
		writeError(c, http.StatusServiceUnavailable, "运行任务服务正在关闭", "runtime_not_ready")
	case errors.Is(err, runtimeops.ErrRuntimeTarget):
		writeError(c, http.StatusBadRequest, "运行操作或目标无效", "invalid_runtime_target")
	default:
		server.runtimeUnavailable(c, operation, err)
	}
}
