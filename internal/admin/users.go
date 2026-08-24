package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/identity"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/gin-gonic/gin"
)

type createUserPayload struct {
	Email  string  `json:"email" binding:"required,max=254"`
	TeamID *string `json:"team_id"`
}

type userActionPayload struct {
	Email      string `json:"email" binding:"required,max=254"`
	Confirm    string `json:"confirm" binding:"required,max=254"`
	RevokeKeys bool   `json:"revoke_keys"`
}

type userQuotaPayload struct {
	Email        string `json:"email" binding:"required,max=254"`
	Mode         string `json:"mode" binding:"required,max=16"`
	WeeklyTokens *int64 `json:"weekly_tokens"`
}

func (server *Server) listUsers(c *gin.Context) {
	page, err := positiveQueryInteger(c, "page", 1)
	if err != nil {
		writeError(c, http.StatusBadRequest, "用户分页参数无效", "invalid_request")
		return
	}
	pageSize, err := positiveQueryInteger(c, "page_size", 50)
	if err != nil {
		writeError(c, http.StatusBadRequest, "用户分页参数无效", "invalid_request")
		return
	}
	result, err := server.store.ListUsers(c.Request.Context(), controlplane.UserListOptions{
		Query:    c.Query("q"),
		TeamID:   c.Query("team_id"),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		if errors.Is(err, controlplane.ErrInvalidCatalogInput) {
			writeError(c, http.StatusBadRequest, "用户查询参数无效", "invalid_request")
			return
		}
		server.internalError(c, "list users", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"users": result.Users,
		"pagination": gin.H{
			"page":        result.Page,
			"page_size":   result.PageSize,
			"total":       result.Total,
			"total_pages": result.TotalPages,
		},
		"generated_at": server.now().Unix(),
	})
}

func (server *Server) createUser(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body createUserPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "用户参数无效", "invalid_request")
		return
	}
	result, err := server.users.CreateUser(c.Request.Context(), body.Email, body.TeamID)
	if err != nil {
		server.writeUserLifecycleError(c, "create user", err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{
		"message": "用户已创建；API Key 和初始密码仅显示本次，首次登录必须修改密码",
		"user":    result,
	})
}

func (server *Server) rotateUserKey(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userActionPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "rotate" {
		writeError(c, http.StatusBadRequest, "请确认轮换 API Key", "invalid_request")
		return
	}
	result, err := server.users.RotateUserKey(c.Request.Context(), body.Email)
	if err != nil {
		server.writeUserLifecycleError(c, "rotate user API key", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "API Key 已轮换；旧 Key 已失效，新 Key 仅显示本次",
		"key":     result,
	})
}

func (server *Server) revokeUser(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userActionPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "revoke" {
		writeError(c, http.StatusBadRequest, "请确认停用用户 API Key", "invalid_request")
		return
	}
	result, err := server.users.RevokeUser(c.Request.Context(), body.Email)
	if err != nil {
		server.writeUserLifecycleError(c, "revoke user", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "用户 API Key 已停用", "revocation": result})
}

func (server *Server) resetUserPassword(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userActionPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "reset" {
		writeError(c, http.StatusBadRequest, "请确认重置使用中心密码", "invalid_request")
		return
	}
	result, err := server.users.ResetUserPassword(c.Request.Context(), body.Email)
	if err != nil {
		server.writeUserLifecycleError(c, "reset user password", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":  "密码已重置；现有会话已失效，初始密码仅显示本次",
		"password": result,
	})
}

func (server *Server) deleteUser(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userActionPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != body.Email {
		writeError(c, http.StatusBadRequest, "确认内容必须与用户邮箱完全一致", "invalid_request")
		return
	}
	result, err := server.users.DeleteUser(c.Request.Context(), body.Email, body.RevokeKeys)
	if err != nil {
		server.writeUserLifecycleError(c, "delete user", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "用户、路由、登录凭据和当前配额策略已删除；历史用量继续保留",
		"user":    result,
	})
}

func (server *Server) readUserQuota(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	email := strings.TrimSpace(c.Query("email"))
	if email == "" {
		writeError(c, http.StatusBadRequest, "请指定用户邮箱", "invalid_request")
		return
	}
	result, err := server.users.ReadUserQuota(c.Request.Context(), email)
	if err != nil {
		server.writeUserLifecycleError(c, "read user quota", err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (server *Server) updateUserQuota(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	var body userQuotaPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "用户周额度参数无效", "invalid_request")
		return
	}
	body.Mode = strings.ToLower(strings.TrimSpace(body.Mode))
	if !validUserQuotaPolicy(body.Mode, body.WeeklyTokens) {
		writeError(c, http.StatusBadRequest, "额度模式或自定义周额度无效", "invalid_request")
		return
	}
	result, err := server.users.UpdateUserQuota(
		c.Request.Context(), body.Email, body.Mode, body.WeeklyTokens,
	)
	if err != nil {
		server.writeUserLifecycleError(c, "update user quota", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":      "用户周额度策略已保存，将由额度采集器发布到 Gateway",
		"user":         result.User,
		"weekly_quota": result.WeeklyQuota,
		"adjustments":  result.Adjustments,
	})
}

func (server *Server) clearUserQuota(c *gin.Context) {
	if !server.requireUserLifecycle(c) {
		return
	}
	email := strings.TrimSpace(c.Query("email"))
	if email == "" {
		writeError(c, http.StatusBadRequest, "请指定用户邮箱", "invalid_request")
		return
	}
	result, err := server.users.ClearUserQuota(c.Request.Context(), email)
	if err != nil {
		server.writeUserLifecycleError(c, "clear user quota", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":      "已恢复继承组织默认周额度，将由额度采集器发布到 Gateway",
		"user":         result.User,
		"weekly_quota": result.WeeklyQuota,
		"adjustments":  result.Adjustments,
	})
}

func validUserQuotaPolicy(mode string, weeklyTokens *int64) bool {
	switch mode {
	case "inherit", "unlimited":
		return weeklyTokens == nil
	case "custom":
		return weeklyTokens != nil && *weeklyTokens > 0 && *weeklyTokens <= 1_000_000_000_000
	default:
		return false
	}
}

func (server *Server) requireUserLifecycle(c *gin.Context) bool {
	if server.users != nil {
		return true
	}
	writeError(c, http.StatusServiceUnavailable, "用户生命周期服务尚未就绪", "user_lifecycle_unavailable")
	return false
}

func (server *Server) writeUserLifecycleError(c *gin.Context, operation string, err error) {
	switch {
	case errors.Is(err, identity.ErrInvalidUser):
		writeError(c, http.StatusBadRequest, "用户邮箱不属于允许的域名", "invalid_user")
	case errors.Is(err, ErrInitialPasswordMissing):
		writeError(c, http.StatusConflict, "请先配置使用中心初始密码", "initial_password_unavailable")
	case errors.Is(err, controlplane.ErrUserAlreadyActive):
		writeError(c, http.StatusConflict, "用户已有启用中的 API Key", "user_exists")
	case errors.Is(err, controlplane.ErrUserLifecycleNotFound):
		writeError(c, http.StatusNotFound, "用户不存在或没有启用中的 API Key", "user_not_found")
	case errors.Is(err, controlplane.ErrTeamNotFound):
		writeError(c, http.StatusNotFound, "团队不存在", "team_not_found")
	case errors.Is(err, controlplane.ErrUserDeleteRequiresRevoke):
		writeError(c, http.StatusConflict, "用户仍有有效 API Key，请确认同时停用后再删除", "active_keys_present")
	case errors.Is(err, controlplane.ErrUserLifecycleConflict), errors.Is(err, identity.ErrRotationConflict):
		writeError(c, http.StatusConflict, "用户状态已变化，请刷新后重试", "user_lifecycle_conflict")
	case errors.Is(err, identity.ErrRotationUnsafe), errors.Is(err, controlplane.ErrInvalidCatalogInput):
		writeError(c, http.StatusBadRequest, "用户或 API Key 状态不安全，拒绝操作", "invalid_request")
	case isQuotaActionInputError(err):
		writeError(c, http.StatusBadRequest, "额度操作参数或目标状态无效", "invalid_request")
	default:
		server.internalError(c, operation, err)
	}
}

func positiveQueryInteger(c *gin.Context, name string, fallback int) (int, error) {
	raw := strings.TrimSpace(c.Query(name))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 0, errors.New("expected a positive integer")
	}
	return value, nil
}

type optionalNullableString struct {
	Set   bool
	Value *string
}

func (value *optionalNullableString) UnmarshalJSON(raw []byte) error {
	value.Set = true
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		value.Value = nil
		return nil
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return errors.New("expected a string or null")
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		value.Value = nil
	} else {
		value.Value = &decoded
	}
	return nil
}

type userTeamPayload struct {
	Email          string                 `json:"email"`
	Users          []string               `json:"users"`
	TeamID         *string                `json:"team_id"`
	ExpectedTeamID optionalNullableString `json:"expected_team_id"`
}

func (server *Server) updateUserTeam(c *gin.Context) {
	var body userTeamPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "用户团队参数无效", "invalid_request")
		return
	}
	server.applyUserTeamUpdate(c, []string{body.Email}, body)
}

func (server *Server) updateUserTeams(c *gin.Context) {
	var body userTeamPayload
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "用户团队参数无效", "invalid_request")
		return
	}
	server.applyUserTeamUpdate(c, body.Users, body)
}

func (server *Server) applyUserTeamUpdate(c *gin.Context, requested []string, body userTeamPayload) {
	users := normalizeUserEmails(requested)
	if len(users) == 0 || len(users) > 500 {
		writeError(c, http.StatusBadRequest, "请选择 1 到 500 位用户", "invalid_request")
		return
	}
	knownUsers, err := server.store.KnownUsers(c.Request.Context())
	if err != nil {
		server.internalError(c, "list known users for team assignment", err)
		return
	}
	known := make(map[string]struct{}, len(knownUsers))
	for _, user := range knownUsers {
		known[strings.ToLower(strings.TrimSpace(user))] = struct{}{}
	}
	missing := make([]string, 0)
	for _, user := range users {
		if _, found := known[user]; !found {
			missing = append(missing, user)
		}
	}
	if len(missing) > 0 {
		message := "用户不存在：" + strings.Join(missing[:min(len(missing), 3)], "、")
		writeError(c, http.StatusNotFound, message, "user_not_found")
		return
	}
	if server.teamIdentities == nil {
		writeError(c, http.StatusServiceUnavailable, "用量身份同步服务尚未就绪", "usage_not_ready")
		return
	}
	teamID := normalizeOptionalString(body.TeamID)
	expectation := controlplane.TeamExpectation{
		Provided: body.ExpectedTeamID.Set,
		TeamID:   normalizeOptionalString(body.ExpectedTeamID.Value),
	}
	assignments, err := server.store.SetUserTeamsExpected(
		c.Request.Context(),
		users,
		teamID,
		expectation,
	)
	if err != nil {
		server.writeControlPlaneError(c, err)
		return
	}
	classifications, err := server.store.ReadUserTeams(c.Request.Context(), users)
	if err != nil {
		server.internalError(c, "read updated user team assignments", err)
		return
	}
	identities := make(map[string]usage.TeamIdentity, len(classifications))
	for user, classification := range classifications {
		identity := usage.TeamIdentity{MembershipVersion: classification.TeamMembershipVersion}
		if classification.TeamID != nil {
			identity.TeamID = *classification.TeamID
		}
		identities[user] = identity
	}
	if _, err := server.teamIdentities.SyncUserTeams(c.Request.Context(), identities); err != nil {
		server.internalError(c, "synchronize updated user team identities", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":         fmt.Sprintf("已更新 %d 位用户的团队归属；团队用量按当前成员动态统计", len(users)),
		"assignments":     assignments,
		"classifications": classifications,
	})
}

func normalizeUserEmails(values []string) []string {
	unique := make(map[string]struct{})
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value != "" {
			unique[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeOptionalString(value *string) *string {
	if value == nil {
		return nil
	}
	normalized := strings.TrimSpace(*value)
	if normalized == "" {
		return nil
	}
	return &normalized
}
