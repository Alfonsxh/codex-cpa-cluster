package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/Alfonsxh/codex-cpa-pool/internal/runtimeops"
	"github.com/gin-gonic/gin"
)

type AccountModelProbe interface {
	Models(context.Context, string) (runtimeops.AccountModels, error)
	Test(context.Context, string, string) (runtimeops.ModelProbeResult, error)
}

func (server *Server) readAccountModels(c *gin.Context) {
	if server.modelProbe == nil {
		writeError(c, http.StatusServiceUnavailable, "模型测试服务尚未就绪", "model_test_unavailable")
		return
	}
	result, err := server.modelProbe.Models(c.Request.Context(), c.Query("account"))
	if err != nil {
		server.writeModelProbeError(c, err)
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (server *Server) testAccountModel(c *gin.Context) {
	if server.modelProbe == nil {
		writeError(c, http.StatusServiceUnavailable, "模型测试服务尚未就绪", "model_test_unavailable")
		return
	}
	var body struct {
		Account string `json:"account" binding:"required"`
		Model   string `json:"model" binding:"required"`
	}
	if c.ShouldBindJSON(&body) != nil {
		writeError(c, http.StatusBadRequest, "请选择账号和模型", "invalid_request")
		return
	}
	result, err := server.modelProbe.Test(c.Request.Context(), body.Account, body.Model)
	if err != nil {
		server.writeModelProbeError(c, err)
		return
	}
	// Upstream authentication failures are test results, not Admin session failures.
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, result)
}

func (server *Server) writeModelProbeError(c *gin.Context, err error) {
	var probeError *runtimeops.ModelProbeError
	if !errors.As(err, &probeError) {
		writeError(c, http.StatusServiceUnavailable, "模型测试暂不可用，请稍后重试", "model_test_unavailable")
		return
	}
	status := http.StatusBadGateway
	switch probeError.Code {
	case "invalid_account", "invalid_model":
		status = http.StatusBadRequest
	case "account_not_found":
		status = http.StatusNotFound
	case "account_not_running", "model_test_no_credentials":
		status = http.StatusConflict
	case "model_test_busy":
		status = http.StatusTooManyRequests
		c.Header("Retry-After", "3")
	case "model_test_unavailable":
		status = http.StatusServiceUnavailable
	}
	writeError(c, status, probeError.Message, probeError.Code)
}
