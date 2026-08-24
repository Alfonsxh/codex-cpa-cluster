package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/accountlifecycle"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/runtimeops"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

type AccountCatalog interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
}

type AccountLifecycleService interface {
	Create(context.Context, accountlifecycle.CreateRequest) (accountlifecycle.CreateResult, error)
	Update(context.Context, accountlifecycle.UpdateRequest) (accountlifecycle.UpdateResult, error)
	Delete(context.Context, accountlifecycle.DeleteRequest) (accountlifecycle.DeleteResult, error)
	ClearAuth(context.Context, string) (accountlifecycle.AuthClearResult, error)
}

type accountListItem struct {
	ID              string                `json:"id"`
	Email           string                `json:"email"`
	Port            int                   `json:"port"`
	ProxyMode       string                `json:"proxy_mode"`
	Enabled         bool                  `json:"enabled"`
	Default         bool                  `json:"default"`
	RoutedUsers     int                   `json:"routed_users"`
	ActiveUsers1H   *int                  `json:"active_users_1h"`
	AccountState    failover.AccountState `json:"account_state"`
	StateAvailable  bool                  `json:"state_available"`
	ProxyConfigured bool                  `json:"proxy_configured"`
}

func (server *Server) listAccounts(c *gin.Context) {
	if server.accounts == nil {
		writeError(c, http.StatusServiceUnavailable, "账号目录服务尚未就绪", "accounts_not_ready")
		return
	}
	var (
		accounts       []controlplane.Account
		routes         map[string]string
		states         map[string]failover.AccountState
		activity       map[string]int
		stateError     error
		activityError  error
		secretStatuses map[string]controlplane.SecretStatus
	)
	group, groupContext := errgroup.WithContext(c.Request.Context())
	group.Go(func() error {
		var err error
		accounts, err = server.accounts.ReadAccounts(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		secretStatuses, err = server.store.SecretStatuses(groupContext)
		return err
	})
	group.Go(func() error {
		var err error
		routes, err = server.accounts.ReadRoutes(groupContext)
		return err
	})
	if server.accountStates != nil {
		group.Go(func() error {
			states, stateError = server.accountStates.AccountStates(groupContext)
			return nil
		})
	}
	if server.activity != nil {
		group.Go(func() error {
			activity, activityError = server.activity.RefreshActiveUsersLastHour(groupContext)
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		server.internalError(c, "read account catalog", err)
		return
	}
	warnings := make([]string, 0, 2)
	if stateError != nil {
		server.logger.Warn("account operational state is unavailable", zap.Error(stateError))
		warnings = append(warnings, "账号额度状态暂不可用，已按状态未知展示")
	}
	if activityError != nil {
		server.logger.Warn("one-hour account activity is unavailable", zap.Error(activityError))
		warnings = append(warnings, "近 1 小时活跃用户数暂不可用")
	}
	routedCounts := make(map[string]int)
	for _, account := range routes {
		routedCounts[account]++
	}
	items := make([]accountListItem, 0, len(accounts))
	for _, account := range accounts {
		state, stateAvailable := states[account.ID]
		if !stateAvailable {
			state = failover.AccountState{Account: account.ID, Reason: "quota_unavailable"}
		}
		var activeUsers *int
		if activityError == nil && server.activity != nil {
			count := activity[account.ID]
			activeUsers = &count
		}
		items = append(items, accountListItem{
			ID: account.ID, Email: account.Email, Port: account.Port,
			ProxyMode: account.ProxyMode, Enabled: account.GroupEnabled,
			Default: account.DefaultGroup, RoutedUsers: routedCounts[account.ID],
			ActiveUsers1H: activeUsers, AccountState: state,
			StateAvailable:  stateAvailable && stateError == nil,
			ProxyConfigured: secretStatuses["cpa_account_proxy_url:"+account.ID].SHA256 != "",
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"accounts":     items,
		"generated_at": server.now().Unix(),
		"warnings":     warnings,
	})
}

func (server *Server) createAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
		return
	}
	var body accountlifecycle.CreateRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "账号创建参数无效", "invalid_request")
		return
	}
	result, err := server.accountLifecycle.Create(c.Request.Context(), body)
	if err != nil {
		server.writeAccountLifecycleError(c, "create account", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"message": "业务 CPA 已创建并通过运行探针", "account": result})
}

func (server *Server) updateAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID              string  `json:"id" binding:"required"`
		NewID           string  `json:"new_id"`
		Email           string  `json:"email"`
		ProxyMode       string  `json:"proxy_mode"`
		ProxyURL        *string `json:"proxy_url"`
		GroupEnabled    *bool   `json:"group_enabled"`
		DefaultGroup    *bool   `json:"default_group"`
		FallbackAccount string  `json:"fallback_account"`
		Confirm         string  `json:"confirm"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "账号更新参数无效", "invalid_request")
		return
	}
	currentID := strings.ToLower(strings.TrimSpace(body.ID))
	newID := strings.ToLower(strings.TrimSpace(body.NewID))
	if newID != "" && newID != currentID && strings.TrimSpace(body.Confirm) != currentID {
		writeError(c, http.StatusBadRequest, "重命名确认内容必须与当前 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	result, err := server.accountLifecycle.Update(c.Request.Context(), accountlifecycle.UpdateRequest{
		AccountID: body.ID, NewAccountID: body.NewID, Email: body.Email,
		ProxyMode: body.ProxyMode, ProxyURL: body.ProxyURL, Enabled: body.GroupEnabled,
		Default: body.DefaultGroup, FallbackAccount: body.FallbackAccount,
	})
	if err != nil {
		server.writeAccountLifecycleError(c, "update account", err)
		return
	}
	message := "CPA 账号已更新并通过运行探针"
	if result.RenamedFrom != "" {
		message = "CPA 已重命名、重建并通过运行探针"
	}
	c.JSON(http.StatusOK, gin.H{"message": message, "account": result})
}

func (server *Server) clearAccountAuth(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID      string `json:"id" binding:"required"`
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Confirm) != strings.TrimSpace(body.ID) {
		writeError(c, http.StatusBadRequest, "确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	result, err := server.accountLifecycle.ClearAuth(c.Request.Context(), body.ID)
	if err != nil {
		server.writeAccountLifecycleError(c, "clear account OAuth", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "OAuth 授权已清除，原文件已安全归档", "account": result})
}

func (server *Server) deleteAccount(c *gin.Context) {
	if server.accountLifecycle == nil {
		writeError(c, http.StatusServiceUnavailable, "账号生命周期服务尚未就绪", "account_lifecycle_not_ready")
		return
	}
	var body struct {
		ID              string `json:"id" binding:"required"`
		Confirm         string `json:"confirm" binding:"required"`
		RevokeKeys      bool   `json:"revoke_keys"`
		FallbackAccount string `json:"fallback_account"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || strings.TrimSpace(body.Confirm) != strings.TrimSpace(body.ID) {
		writeError(c, http.StatusBadRequest, "确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	result, err := server.accountLifecycle.Delete(c.Request.Context(), accountlifecycle.DeleteRequest{
		AccountID: body.ID, FallbackAccount: body.FallbackAccount, RevokeExclusive: body.RevokeKeys,
	})
	if err != nil {
		server.writeAccountLifecycleError(c, "delete account", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "业务 CPA 已删除，配置、授权和日志已安全归档", "account": result})
}

func (server *Server) writeAccountLifecycleError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, "账号参数无效，请检查标识、邮箱、代理和确认字段", "invalid_request")
	case errors.Is(err, controlplane.ErrAccountLifecycleNotFound):
		writeError(c, http.StatusNotFound, "CPA 账号不存在", "account_not_found")
	case errors.Is(err, controlplane.ErrAccountAlreadyExists),
		errors.Is(err, controlplane.ErrAccountEmailAlreadyExists),
		errors.Is(err, controlplane.ErrAccountPortAlreadyExists):
		writeError(c, http.StatusConflict, "CPA 标识、邮箱或端口已被占用", "account_exists")
	case errors.Is(err, controlplane.ErrAccountDeleteLast):
		writeError(c, http.StatusConflict, "至少保留一个业务 CPA，不能删除最后一个账号", "account_last")
	case errors.Is(err, controlplane.ErrAccountDeleteRequiresRevoke):
		writeError(c, http.StatusConflict, "该 CPA 仍有独占有效 Key，请确认同时停用后再删除", "account_revoke_required")
	case errors.Is(err, controlplane.ErrAccountDeleteNeedsFallback):
		writeError(c, http.StatusConflict, "请选择其他已启用 CPA 作为安全迁移目标", "account_fallback_required")
	case errors.Is(err, controlplane.ErrAccountLifecycleConflict),
		errors.Is(err, accountlifecycle.ErrNoAccountPort),
		errors.Is(err, runtimeops.ErrRuntimeConflict):
		writeError(c, http.StatusConflict, "账号状态已变化或没有安全运行资源，未执行切换", "account_lifecycle_conflict")
	case errors.Is(err, controlplane.ErrLeaseLost):
		writeError(c, http.StatusServiceUnavailable, "控制面所有权已变化，操作已停止并回滚", "ownership_lost")
	case errors.Is(err, accountlifecycle.ErrRouteEvacuationUnavailable),
		errors.Is(err, accountlifecycle.ErrLifecycleRecoveryRequired):
		writeError(c, http.StatusServiceUnavailable, "账号安全迁移与恢复服务尚未就绪", "account_lifecycle_not_ready")
	case errors.Is(err, accountlifecycle.ErrAccountDrainTimeout):
		writeError(c, http.StatusConflict, "该 CPA 仍有进行中的 Codex 请求，账号未重建或删除，请稍后重试", "account_requests_active")
	default:
		server.internalError(c, operation, err)
	}
}

func (server *Server) rebalanceAllAccounts(c *gin.Context) {
	var body struct {
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "rebalance-all" {
		writeError(c, http.StatusBadRequest, "请确认一键负载均衡全部账号", "invalid_request")
		return
	}
	if server.rebalancer == nil {
		writeError(c, http.StatusServiceUnavailable, "负载均衡服务尚未就绪", "rebalance_not_ready")
		return
	}
	result, err := server.rebalancer.RebalanceAll(c.Request.Context())
	if err != nil {
		switch {
		case errors.Is(err, failover.ErrRebalanceUnsafe),
			errors.Is(err, failover.ErrRebalanceUnavailable),
			errors.Is(err, controlplane.ErrRouteConflict),
			errors.Is(err, controlplane.ErrRouteUserUnsafe):
			writeError(c, http.StatusConflict, "当前用户或账号状态不满足安全迁移条件，未执行任何迁移", "account_rebalance_unavailable")
		default:
			server.internalError(c, "rebalance all accounts", err)
		}
		return
	}
	message := "账号已处于目标分布，无需迁移"
	if result.MovedUsers > 0 && result.ActivityRefreshed {
		message = "账号用户负载均衡已完成，近 1 小时活跃用户数已刷新"
	} else if result.MovedUsers > 0 {
		message = "账号用户负载均衡已完成，但近 1 小时活跃用户数刷新失败"
	}
	c.JSON(http.StatusOK, gin.H{
		"message":   message,
		"rebalance": result,
	})
}

func (server *Server) rebalanceAccount(c *gin.Context) {
	var body struct {
		ID      string `json:"id" binding:"required"`
		Confirm string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "账号迁移参数无效", "invalid_request")
		return
	}
	account := strings.ToLower(strings.TrimSpace(body.ID))
	if account == "" || strings.ToLower(strings.TrimSpace(body.Confirm)) != account {
		writeError(c, http.StatusBadRequest, "确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	if server.accounts == nil {
		writeError(c, http.StatusServiceUnavailable, "账号目录服务尚未就绪", "accounts_not_ready")
		return
	}
	accounts, err := server.accounts.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read account before rebalance", err)
		return
	}
	found := false
	for _, item := range accounts {
		if item.ID == account {
			found = true
			break
		}
	}
	if !found {
		writeError(c, http.StatusNotFound, "CPA 账号不存在", "account_not_found")
		return
	}
	if server.rebalancer == nil {
		writeError(c, http.StatusServiceUnavailable, "负载均衡服务尚未就绪", "rebalance_not_ready")
		return
	}
	result, err := server.rebalancer.EvacuateAccount(c.Request.Context(), account)
	if err != nil {
		switch {
		case errors.Is(err, failover.ErrRebalanceUnsafe),
			errors.Is(err, failover.ErrRebalanceUnavailable),
			errors.Is(err, controlplane.ErrRouteConflict),
			errors.Is(err, controlplane.ErrRouteUserUnsafe):
			writeError(c, http.StatusConflict, "当前用户或账号状态不满足安全迁移条件，未执行任何迁移", "account_rebalance_unavailable")
		default:
			server.internalError(c, "rebalance account", err)
		}
		return
	}
	message := "该账号当前没有需要迁移的用户"
	if result.MovedUsers > 0 && result.ActivityRefreshed {
		message = "账号用户已全部安全迁移，近 1 小时活跃用户数已刷新"
	} else if result.MovedUsers > 0 {
		message = "账号用户已全部安全迁移，但近 1 小时活跃用户数刷新失败"
	}
	c.JSON(http.StatusOK, gin.H{
		"message":   message,
		"rebalance": result,
	})
}

func (server *Server) resetAccountQuota(c *gin.Context) {
	var body struct {
		Account  string `json:"account" binding:"required"`
		CreditID string `json:"credit_id" binding:"required"`
		Confirm  string `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "周限额重置参数无效", "invalid_request")
		return
	}
	account := strings.ToLower(strings.TrimSpace(body.Account))
	if account == "" || strings.ToLower(strings.TrimSpace(body.Confirm)) != account {
		writeError(c, http.StatusBadRequest, "确认内容必须与 CPA 标识完全一致", "invalid_confirmation")
		return
	}
	creditID := strings.TrimSpace(body.CreditID)
	if creditID == "" || len(creditID) > 512 {
		writeError(c, http.StatusBadRequest, "请选择要使用的重置额度", "invalid_request")
		return
	}
	if server.quotaResetter == nil {
		writeError(c, http.StatusServiceUnavailable, "周限额重置服务尚未就绪", "quota_reset_not_ready")
		return
	}
	result, err := server.quotaResetter.Reset(c.Request.Context(), account, creditID)
	if err != nil {
		switch {
		case errors.Is(err, controlplane.ErrInvalidCatalogInput):
			writeError(c, http.StatusBadRequest, "周限额重置参数无效", "invalid_request")
		case errors.Is(err, quota.ErrResetAccountNotFound):
			writeError(c, http.StatusNotFound, "CPA 账号不存在", "account_not_found")
		case errors.Is(err, quota.ErrOAuthMissing):
			writeError(c, http.StatusConflict, "该 CPA 尚未完成 OAuth 授权", "quota_auth_missing")
		case errors.Is(err, quota.ErrAuthExpired):
			writeError(c, http.StatusConflict, "上游 OAuth 授权已失效，请重新完成 OAuth 后再重试", "quota_auth_expired")
		case errors.Is(err, quota.ErrResetCreditChanged):
			writeError(c, http.StatusConflict, "所选重置额度已使用、过期或不可用，请刷新列表后重新选择", "quota_reset_credit_changed")
		case errors.Is(err, quota.ErrResetUnavailable):
			writeError(c, http.StatusConflict, "当前没有已耗尽且可重置的周限额，请等待额度耗尽或刷新列表", "quota_reset_unavailable")
		case errors.Is(err, quota.ErrResetRejected):
			writeError(c, http.StatusConflict, "上游已拒绝本次重置周限额，请刷新周限额后重试", "quota_reset_rejected")
		case errors.Is(err, controlplane.ErrLeaseLost):
			writeError(c, http.StatusServiceUnavailable, "控制面所有权已变化，操作已停止", "ownership_lost")
		default:
			writeError(c, http.StatusBadGateway, "无法连接上游完成重置周限额，请稍后重试", "quota_upstream_unavailable")
		}
		return
	}
	message := "重置请求已处理，请刷新确认最新周限额"
	if result.WindowsReset > 0 {
		message = fmt.Sprintf("周限额已重置，共刷新 %d 个窗口", result.WindowsReset)
	}
	c.JSON(http.StatusOK, gin.H{
		"message":       message,
		"account":       result.Account,
		"windows":       result.Windows,
		"windows_reset": result.WindowsReset,
		"code":          result.Code,
		"credit":        result.Credit,
	})
}
