package portal

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/identity"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	sessionCookieName = "cpa_user_session"
	defaultSessionTTL = 12 * time.Hour
	maximumBodySize   = int64(1 << 20)
)

type IdentityStore interface {
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
	ReadKeyRecordsForUsers(context.Context, []string) ([]controlplane.KeyRecord, error)
	ReadSettings(context.Context) (map[string]any, error)
	ReadSecret(context.Context, string) (string, bool, error)
}

type SessionStore interface {
	CreateSession(context.Context, string, time.Duration) (string, usage.PortalSession, error)
	ResolveSession(context.Context, string) (usage.PortalSession, error)
	RevokeSession(context.Context, string) error
	Credential(context.Context, string) (usage.PortalCredential, error)
	SetCredential(context.Context, string, string, bool, string) (usage.PortalCredential, error)
}

type UsageReader interface {
	UserAccounts(context.Context, string, int64, *int64) (usage.UserAccountSummary, error)
	UserBreakdown(context.Context, string, string, int64, *int64) (usage.UserBreakdown, error)
	UserDailyTrend(
		context.Context, string, int, string, int64, usage.UserTrendDimension,
	) (usage.UserDailyTrend, error)
}

// QuotaReader deliberately keeps the natural-week quota read separate from
// session/credential ownership. Portal pages can therefore request the
// summary only when it is visible without widening the session store API.
type QuotaReader interface {
	WeeklyQuota(context.Context, string, *int64) (usage.WeeklyQuota, error)
}

type RouteChanger interface {
	MoveUser(context.Context, string, string, string) (failover.RebalanceResult, error)
}

type KeyRotator interface {
	RotateUserKey(context.Context, string, string) (identity.RotationResult, error)
}

type QuotaStateStore interface {
	ReadRuntimeState(context.Context, string, any) (bool, error)
}

type PublicUsageReader interface {
	PublicGatewayUsage(context.Context, []string, int64, int64) (map[string]usage.PublicAccountUsage, error)
}

type InflightKeyReader interface {
	InflightKeyCounts(context.Context) (map[string]int64, error)
}

type Config struct {
	Identity      IdentityStore
	Sessions      SessionStore
	Usage         UsageReader
	Quotas        QuotaReader
	States        failover.AccountStateProvider
	Activity      failover.ActivityProvider
	Routes        RouteChanger
	Keys          KeyRotator
	QuotaStore    QuotaStateStore
	PublicUsage   PublicUsageReader
	Inflight      InflightKeyReader
	Logger        *zap.Logger
	Now           func() time.Time
	SessionTTL    time.Duration
	SecureCookies bool
	LoginLimiter  *LoginLimiter
}

type Server struct {
	identity      IdentityStore
	sessions      SessionStore
	usage         UsageReader
	quotas        QuotaReader
	states        failover.AccountStateProvider
	activity      failover.ActivityProvider
	routes        RouteChanger
	keys          KeyRotator
	quotaStore    QuotaStateStore
	publicUsage   PublicUsageReader
	inflight      InflightKeyReader
	logger        *zap.Logger
	now           func() time.Time
	sessionTTL    time.Duration
	secureCookies bool
	loginLimiter  *LoginLimiter
}

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code"`
}

type portalAuth struct {
	Session    usage.PortalSession
	Credential usage.PortalCredential
	Token      string
	Records    []controlplane.KeyRecord
	APIKey     string
}

type usageWindow struct {
	Name     any    `json:"window"`
	Seconds  *int64 `json:"window_seconds"`
	StartAt  int64  `json:"window_start_at"`
	EndAt    int64  `json:"window_end_at"`
	Timezone string `json:"window_timezone,omitempty"`
}

func New(config Config) (*Server, error) {
	if config.Identity == nil || config.Sessions == nil {
		return nil, errors.New("portal server requires identity and session stores")
	}
	if config.Logger == nil {
		config.Logger = zap.NewNop()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.SessionTTL <= 0 {
		config.SessionTTL = defaultSessionTTL
	}
	if config.SessionTTL > 30*24*time.Hour {
		return nil, errors.New("portal session TTL must not exceed 30 days")
	}
	if config.LoginLimiter == nil {
		config.LoginLimiter = NewLoginLimiter(config.Now)
	}
	return &Server{
		identity: config.Identity, sessions: config.Sessions, usage: config.Usage, quotas: config.Quotas,
		states: config.States, activity: config.Activity, routes: config.Routes, keys: config.Keys,
		quotaStore:  config.QuotaStore,
		publicUsage: config.PublicUsage, inflight: config.Inflight,
		logger: config.Logger, now: config.Now, sessionTTL: config.SessionTTL,
		secureCookies: config.SecureCookies, loginLimiter: config.LoginLimiter,
	}, nil
}

func (server *Server) Register(router gin.IRouter) {
	usageRoutes := router.Group("/usage")
	usageRoutes.Use(server.noStore(), server.recovery())
	usageRoutes.POST("/session", server.limitBody(), server.createSession)
	usageRoutes.GET("/session", server.readSession)
	usageRoutes.DELETE("/session", server.deleteSession)
	usageRoutes.GET("/me/profile", server.readProfile)
	usageRoutes.GET("/me/key", server.readKey)
	usageRoutes.GET("/me/quota", server.readQuota)
	usageRoutes.GET("/me/accounts", server.readAccounts)
	usageRoutes.GET("/me/route", server.readRoute)
	usageRoutes.GET("/me/usage-breakdown", server.readUsageBreakdown)
	usageRoutes.GET("/me/usage-trend", server.readUsageTrend)
	usageRoutes.PUT("/me/password", server.limitBody(), server.changePassword)
	usageRoutes.PUT("/me/group", server.limitBody(), server.changeRoute)
	usageRoutes.POST("/me/key/rotate", server.limitBody(), server.rotateKey)
	usageRoutes.GET("/limits", server.readUsageLimits)
	usageRoutes.GET("/api", server.readPublicGatewayUsage)
}

type publicGatewayUsageRow struct {
	Account      string `json:"account"`
	InflightKeys int64  `json:"inflight_keys"`
	ActiveKeys   int64  `json:"active_keys"`
	RequestCount int64  `json:"request_count"`
}

func (server *Server) readPublicGatewayUsage(c *gin.Context) {
	if server.publicUsage == nil {
		writeError(c, http.StatusServiceUnavailable, "公开用量服务尚未就绪", "public_usage_not_ready")
		return
	}
	window := int64(300)
	if raw := strings.TrimSpace(c.Query("window")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			writeError(c, http.StatusBadRequest, "统计范围无效", "invalid_request")
			return
		}
		window = parsed
	}
	if window != 300 && window != 3600 && window != 86400 {
		writeError(c, http.StatusBadRequest, "统计范围无效", "invalid_request")
		return
	}
	accounts, err := server.identity.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read public usage accounts", err)
		return
	}
	accountIDs := make([]string, 0, len(accounts))
	for _, account := range accounts {
		accountIDs = append(accountIDs, account.ID)
	}
	now := server.now().Unix()
	usageByAccount, err := server.publicUsage.PublicGatewayUsage(
		c.Request.Context(), accountIDs, now-window, now+1,
	)
	if err != nil {
		server.internalError(c, "read public gateway usage", err)
		return
	}
	inflight := make(map[string]int64)
	if server.inflight != nil {
		if values, readErr := server.inflight.InflightKeyCounts(c.Request.Context()); readErr == nil {
			inflight = values
		} else {
			server.logger.Warn("public in-flight usage unavailable", zap.Error(readErr))
		}
	}
	rows := make([]publicGatewayUsageRow, 0, len(accountIDs))
	totals := gin.H{"inflight_keys": int64(0), "active_keys": int64(0), "requests": int64(0)}
	for _, accountID := range accountIDs {
		metrics := usageByAccount[accountID]
		row := publicGatewayUsageRow{
			Account: accountID, InflightKeys: inflight[accountID],
			ActiveKeys: metrics.ActiveKeys, RequestCount: metrics.RequestCount,
		}
		rows = append(rows, row)
		totals["inflight_keys"] = totals["inflight_keys"].(int64) + row.InflightKeys
		totals["active_keys"] = totals["active_keys"].(int64) + row.ActiveKeys
		totals["requests"] = totals["requests"].(int64) + row.RequestCount
	}
	c.JSON(http.StatusOK, gin.H{
		"generated_at": now, "window_seconds": window, "truncated": false,
		"cached": false, "totals": totals, "accounts": rows,
	})
}

type publicWeeklyWindow struct {
	Key                 string  `json:"key"`
	Label               string  `json:"label"`
	MeteredFeature      *string `json:"metered_feature"`
	WindowSlot          string  `json:"window_slot"`
	UsedPercent         float64 `json:"used_percent"`
	RemainingPercent    float64 `json:"remaining_percent"`
	ReportedUsedPercent float64 `json:"reported_used_percent"`
	ResetAt             *int64  `json:"reset_at"`
	ResetAfterSeconds   *int64  `json:"reset_after_seconds"`
	WindowSeconds       int64   `json:"window_seconds"`
	LimitReached        bool    `json:"limit_reached"`
}

type publicAccountQuota struct {
	Account       string               `json:"account"`
	Status        string               `json:"status"`
	PlanType      *string              `json:"plan_type"`
	Allowed       *bool                `json:"allowed"`
	LimitReached  *bool                `json:"limit_reached"`
	Weekly        *publicWeeklyWindow  `json:"weekly"`
	WeeklyWindows []publicWeeklyWindow `json:"weekly_windows"`
}

func (server *Server) readUsageLimits(c *gin.Context) {
	if server.quotaStore == nil {
		writeError(c, http.StatusServiceUnavailable, "账号周额度服务尚未就绪", "usage_limits_not_ready")
		return
	}
	state, found, err := quota.ReadState(c.Request.Context(), server.quotaStore)
	if err != nil {
		server.internalError(c, "read public usage limits", err)
		return
	}
	snapshot := state.Snapshot
	if !found || state.Version == 0 {
		snapshot = quota.Snapshot{Accounts: []quota.AccountQuota{}}
	}
	accounts := make([]publicAccountQuota, 0, len(snapshot.Accounts))
	for _, account := range snapshot.Accounts {
		windows := make([]publicWeeklyWindow, 0, len(account.WeeklyWindows))
		for _, window := range account.WeeklyWindows {
			windows = append(windows, sanitizeWeeklyWindow(window))
		}
		var weekly *publicWeeklyWindow
		if account.Weekly != nil {
			value := sanitizeWeeklyWindow(*account.Weekly)
			weekly = &value
		}
		accounts = append(accounts, publicAccountQuota{
			Account: account.Account, Status: account.Status, PlanType: account.PlanType,
			Allowed: account.Allowed, LimitReached: account.LimitReached,
			Weekly: weekly, WeeklyWindows: windows,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"generated_at": snapshot.GeneratedAt, "cache_ttl_seconds": snapshot.CacheTTLSeconds,
		"cached": snapshot.Cached, "refreshing": snapshot.Refreshing, "accounts": accounts,
	})
}

func sanitizeWeeklyWindow(window quota.WeeklyWindow) publicWeeklyWindow {
	return publicWeeklyWindow{
		Key: window.Key, Label: window.Label, MeteredFeature: window.MeteredFeature,
		WindowSlot: window.WindowSlot, UsedPercent: window.UsedPercent,
		RemainingPercent: window.RemainingPercent, ReportedUsedPercent: window.ReportedUsedPercent,
		ResetAt: window.ResetAt, ResetAfterSeconds: window.ResetAfterSeconds,
		WindowSeconds: window.WindowSeconds, LimitReached: window.LimitReached,
	}
}

func (server *Server) createSession(c *gin.Context) {
	var body struct {
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || ValidateCurrentPassword(body.Password) != nil {
		writeError(c, http.StatusBadRequest, "邮箱或密码格式无效", "invalid_request")
		return
	}
	user := normalizeEmail(body.Email)
	identityDigest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(body.Email))))
	limitKeys := []string{
		"portal-ip:" + c.ClientIP(),
		"portal-account:" + hex.EncodeToString(identityDigest[:8]),
	}
	if allowed, retry := server.loginLimiter.Allow(limitKeys...); !allowed {
		seconds := max(int(math.Ceil(retry.Seconds())), 1)
		c.Header("Retry-After", strconv.Itoa(seconds))
		writeError(c, http.StatusTooManyRequests, "登录尝试过于频繁，请稍后重试", "rate_limited")
		return
	}
	records, recordError := server.activeRecords(c.Request.Context(), user)
	credential, credentialError := server.sessions.Credential(c.Request.Context(), user)
	encoded := DummyPasswordHash()
	if credentialError == nil {
		encoded = credential.PasswordHash
	}
	passwordMatches := VerifyPassword(body.Password, encoded)
	if recordError != nil || credentialError != nil || !passwordMatches {
		if credentialError != nil && !errors.Is(credentialError, usage.ErrPortalCredentialNotFound) {
			server.internalError(c, "read portal credential", credentialError)
			return
		}
		if recordError != nil && !errors.Is(recordError, errPortalUserUnavailable) &&
			!errors.Is(recordError, errPortalKeyMigrating) {
			server.internalError(c, "read portal identity", recordError)
			return
		}
		writeError(c, http.StatusUnauthorized, "邮箱或密码错误", "invalid_credentials")
		return
	}
	_ = records
	server.loginLimiter.Forget(limitKeys...)
	token, session, err := server.sessions.CreateSession(c.Request.Context(), user, server.sessionTTL)
	if err != nil {
		server.internalError(c, "create portal session", err)
		return
	}
	server.writeSessionCookie(c, token, session.ExpiresAt)
	c.JSON(http.StatusCreated, gin.H{
		"user": user, "expires_at": session.ExpiresAt,
		"password_change_required": credential.MustChange,
	})
}

func (server *Server) readSession(c *gin.Context) {
	auth, ok := server.requireAuth(c, true)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"authenticated": true, "user": auth.Session.User,
		"expires_at":               auth.Session.ExpiresAt,
		"password_change_required": auth.Credential.MustChange,
	})
}

func (server *Server) deleteSession(c *gin.Context) {
	token := server.sessionToken(c)
	if err := server.sessions.RevokeSession(c.Request.Context(), token); err != nil {
		server.internalError(c, "revoke portal session", err)
		return
	}
	server.writeSessionCookie(c, "", 0)
	c.JSON(http.StatusOK, gin.H{"logged_out": true})
}

func (server *Server) readProfile(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal route", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"user":          auth.Session.User,
		"current_group": routes[auth.Session.User], "generated_at": server.now().Unix(),
	})
}

func (server *Server) readKey(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"api_key": auth.APIKey, "generated_at": server.now().Unix(),
	})
}

type portalWeeklyQuota struct {
	usage.WeeklyQuota
	PersonalPolicyResetEnabled bool `json:"personal_policy_reset_enabled"`
}

func (server *Server) readQuota(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	if server.quotas == nil {
		writeError(c, http.StatusServiceUnavailable, "个人周额度服务尚未就绪", "quota_not_ready")
		return
	}
	settings, err := server.identity.ReadSettings(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal quota settings", err)
		return
	}
	defaultLimit, resetOnNewWeek, err := portalQuotaConfiguration(settings)
	if err != nil {
		server.internalError(c, "parse portal quota settings", err)
		return
	}
	weekly, err := server.quotas.WeeklyQuota(c.Request.Context(), auth.Session.User, defaultLimit)
	if err != nil {
		server.internalError(c, "read portal weekly quota", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"generated_at": server.now().Unix(),
		"weekly_quota": portalWeeklyQuota{
			WeeklyQuota: weekly, PersonalPolicyResetEnabled: resetOnNewWeek,
		},
	})
}

func portalQuotaConfiguration(settings map[string]any) (*int64, bool, error) {
	resetOnNewWeek := true
	if raw, found := settings["user_quota.reset_personal_weekly_on_new_week"]; found {
		value, valid := raw.(bool)
		if !valid {
			return nil, false, errors.New("user quota reset policy must be a boolean")
		}
		resetOnNewWeek = value
	}
	var defaultLimit *int64
	if raw, found := settings["user_quota.default_weekly_tokens"]; found && raw != nil {
		var value int64
		switch typed := raw.(type) {
		case int:
			value = int64(typed)
		case int64:
			value = typed
		case float64:
			value = int64(typed)
			if float64(value) != typed {
				return nil, false, errors.New("default weekly quota must be a positive integer or null")
			}
		default:
			return nil, false, errors.New("default weekly quota must be a positive integer or null")
		}
		if value <= 0 || value > 1_000_000_000_000 {
			return nil, false, errors.New("default weekly quota is outside the supported range")
		}
		defaultLimit = &value
	}
	return defaultLimit, resetOnNewWeek, nil
}

func (server *Server) readRoute(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal route", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"current_group": routes[auth.Session.User], "generated_at": server.now().Unix()})
}

func (server *Server) readAccounts(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, "用量查询服务尚未就绪", "usage_not_ready")
		return
	}
	window, err := server.parseUsageWindow(c.Request.Context(), c.Query("window"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	accounts, err := server.identity.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal accounts", err)
		return
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal routes", err)
		return
	}
	accountUsage, err := server.usage.UserAccounts(
		c.Request.Context(), auth.Session.User, window.StartAt, &window.EndAt,
	)
	if err != nil {
		server.internalError(c, "read portal account usage", err)
		return
	}
	usageByAccount := make(map[string]usage.WeightedMetrics, len(accountUsage.Accounts))
	for _, item := range accountUsage.Accounts {
		usageByAccount[item.Account] = item.WeightedMetrics
	}
	states := make(map[string]failover.AccountState)
	warnings := make([]string, 0, 2)
	if server.states != nil {
		if loaded, stateError := server.states.AccountStates(c.Request.Context()); stateError == nil {
			states = loaded
		} else {
			warnings = append(warnings, "账号额度状态暂不可用，已按状态未知展示")
			server.logger.Warn("portal account state unavailable", zap.Error(stateError))
		}
	}
	activity := make(map[string]int)
	if server.activity != nil {
		if loaded, activityError := server.activity.RefreshActiveUsersLastHour(c.Request.Context()); activityError == nil {
			activity = loaded
		} else {
			warnings = append(warnings, "近 1 小时活跃用户数暂不可用")
			server.logger.Warn("portal account activity unavailable", zap.Error(activityError))
		}
	}
	items := make([]gin.H, 0, len(accounts))
	for index, account := range accounts {
		state, stateFound := states[account.ID]
		presentation := presentAccountState(account, state, stateFound)
		items = append(items, gin.H{
			"id": account.ID, "display_name": fmt.Sprintf("CPA %d", index+1),
			"current": routes[auth.Session.User] == account.ID,
			"enabled": account.GroupEnabled, "selectable": presentation.Selectable,
			"status": presentation, "active_users_1h": activity[account.ID],
			"usage": usageByAccount[account.ID],
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"generated_at": server.now().Unix(), "window": window,
		"current_group": routes[auth.Session.User], "accounts": items,
		"totals": accountUsage.Totals, "warnings": warnings,
	})
}

func (server *Server) readUsageBreakdown(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, "用量查询服务尚未就绪", "usage_not_ready")
		return
	}
	account := strings.TrimSpace(c.Query("account"))
	if account != "" && !recordHasAccount(auth.Records, account) {
		writeError(c, http.StatusNotFound, "账号不存在", "account_not_found")
		return
	}
	window, err := server.parseUsageWindow(c.Request.Context(), c.Query("window"))
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	breakdown, err := server.usage.UserBreakdown(
		c.Request.Context(), auth.Session.User, account, window.StartAt, &window.EndAt,
	)
	if err != nil {
		server.internalError(c, "read portal usage breakdown", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"generated_at": server.now().Unix(), "window": window.Name,
		"window_seconds": window.Seconds, "window_start_at": window.StartAt,
		"window_end_at": window.EndAt, "window_timezone": window.Timezone,
		"account": optionalAccount(account), "user": auth.Session.User,
		"definition":            "仅统计 Collector 已持久化的业务请求；加权 Token 使用事件写入时冻结的倍率",
		"collection_started_at": breakdown.CollectionStartedAt,
		"effective_start_at":    breakdown.EffectiveStartAt,
		"totals":                breakdown.Totals, "models": breakdown.Models,
		"reasoning_efforts": breakdown.ReasoningEfforts, "combinations": breakdown.Combinations,
	})
}

func (server *Server) readUsageTrend(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	if server.usage == nil {
		writeError(c, http.StatusServiceUnavailable, "用量查询服务尚未就绪", "usage_not_ready")
		return
	}
	if _, supplied := c.Request.URL.Query()["user"]; supplied {
		writeError(c, http.StatusBadRequest, "个人趋势不接受用户参数", "invalid_request")
		return
	}
	window := strings.ToLower(strings.TrimSpace(c.Query("window")))
	if window == "" {
		window = "30d"
	}
	windowDays, found := map[string]int{"7d": 7, "30d": 30, "90d": 90}[window]
	if !found {
		writeError(c, http.StatusBadRequest, "趋势统计范围无效", "invalid_request")
		return
	}
	dimension := usage.UserTrendDimension(strings.ToLower(strings.TrimSpace(c.Query("dimension"))))
	if dimension == "" {
		dimension = usage.UserTrendTotal
	}
	if dimension != usage.UserTrendTotal && dimension != usage.UserTrendModelReasoning {
		writeError(c, http.StatusBadRequest, "趋势统计维度无效", "invalid_request")
		return
	}
	timezone, err := server.usageTimezone(c.Request.Context())
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	trend, err := server.usage.UserDailyTrend(
		c.Request.Context(), auth.Session.User, windowDays, timezone, server.now().Unix(), dimension,
	)
	if err != nil {
		server.internalError(c, "read portal daily usage trend", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"generated_at": server.now().Unix(), "window": window, "window_days": trend.WindowDays,
		"window_start_at": trend.WindowStartAt, "window_end_at": trend.WindowEndAt,
		"window_timezone": trend.Timezone, "dimension": trend.Dimension,
		"definition":            "仅统计 Collector 已持久化的业务请求；按配置时区自然日聚合，加权 Token 使用事件写入时冻结的倍率",
		"collection_started_at": trend.CollectionStartedAt,
		"effective_start_at":    trend.EffectiveStartAt,
		"days":                  trend.Days,
	})
}

func (server *Server) changePassword(c *gin.Context) {
	auth, ok := server.requireAuth(c, true)
	if !ok {
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password" binding:"required"`
		NewPassword     string `json:"new_password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "密码格式无效", "invalid_password")
		return
	}
	if err := ValidateCurrentPassword(body.CurrentPassword); err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_password")
		return
	}
	if err := ValidateNewPassword(body.NewPassword); err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "weak_password")
		return
	}
	if !VerifyPassword(body.CurrentPassword, auth.Credential.PasswordHash) {
		writeError(c, http.StatusUnauthorized, "当前密码错误", "invalid_current_password")
		return
	}
	if auth.Credential.MustChange && constantTimeEqual(body.NewPassword, body.CurrentPassword) {
		writeError(c, http.StatusBadRequest, "新密码不能与初始密码相同", "weak_password")
		return
	}
	initialPassword, found, err := server.identity.ReadSecret(c.Request.Context(), "portal_initial_password")
	if err != nil {
		server.internalError(c, "read initial portal password", err)
		return
	}
	if found && constantTimeEqual(body.NewPassword, initialPassword) {
		writeError(c, http.StatusBadRequest, "新密码不能与初始密码相同", "weak_password")
		return
	}
	encoded, err := HashPassword(body.NewPassword)
	if err != nil {
		server.internalError(c, "hash portal password", err)
		return
	}
	if _, err := server.sessions.SetCredential(
		c.Request.Context(), auth.Session.User, encoded, false, auth.Token,
	); err != nil {
		server.internalError(c, "update portal password", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "密码已修改", "password_change_required": false})
}

func (server *Server) changeRoute(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	var body struct {
		GroupID string `json:"group_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		writeError(c, http.StatusBadRequest, "目标账号不能为空", "invalid_request")
		return
	}
	target := strings.TrimSpace(body.GroupID)
	if !recordHasAccount(auth.Records, target) {
		writeError(c, http.StatusNotFound, "目标账号不存在", "account_not_found")
		return
	}
	accounts, err := server.identity.ReadAccounts(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal accounts", err)
		return
	}
	account, found := accountByID(accounts, target)
	if !found || !account.GroupEnabled {
		writeError(c, http.StatusConflict, "目标账号当前不可选择", "account_unavailable")
		return
	}
	if server.states != nil {
		states, stateError := server.states.AccountStates(c.Request.Context())
		if stateError != nil {
			server.internalError(c, "read portal account state", stateError)
			return
		}
		state, stateFound := states[target]
		if !presentAccountState(account, state, stateFound).Selectable {
			writeError(c, http.StatusConflict, "目标账号当前不可选择", "account_unavailable")
			return
		}
	}
	routes, err := server.identity.ReadRoutes(c.Request.Context())
	if err != nil {
		server.internalError(c, "read portal route", err)
		return
	}
	current := routes[auth.Session.User]
	if current == target {
		c.JSON(http.StatusOK, gin.H{"message": "当前已使用该账号", "current_group": target, "changed": false})
		return
	}
	if server.routes == nil {
		writeError(c, http.StatusServiceUnavailable, "路由切换服务尚未就绪", "route_change_not_ready")
		return
	}
	result, err := server.routes.MoveUser(c.Request.Context(), auth.Session.User, target, current)
	if err != nil {
		switch {
		case errors.Is(err, controlplane.ErrRouteConflict):
			writeError(c, http.StatusConflict, "当前账号已变化，请刷新后重试", "route_conflict")
		case errors.Is(err, controlplane.ErrRouteUserUnsafe), errors.Is(err, controlplane.ErrRouteTargetNotFound):
			writeError(c, http.StatusConflict, "当前用户或目标账号不满足安全切换条件", "route_unavailable")
		default:
			server.internalError(c, "change portal route", err)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "当前账号已切换", "current_group": target, "changed": result.MovedUsers > 0,
		"snapshot_generation": result.SnapshotGeneration,
	})
}

func (server *Server) rotateKey(c *gin.Context) {
	auth, ok := server.requireAuth(c, false)
	if !ok {
		return
	}
	var body struct {
		Confirm bool `json:"confirm" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || !body.Confirm {
		writeError(c, http.StatusBadRequest, "请确认刷新 API Key", "confirmation_required")
		return
	}
	if server.keys == nil {
		writeError(c, http.StatusServiceUnavailable, "API Key 刷新服务尚未就绪", "key_rotation_not_ready")
		return
	}
	result, err := server.keys.RotateUserKey(c.Request.Context(), auth.Session.User, auth.APIKey)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrRotationConflict), errors.Is(err, identity.ErrRotationUnsafe):
			writeError(c, http.StatusConflict, "API Key 状态已变化，请刷新后重试", "key_rotation_conflict")
		default:
			server.internalError(c, "rotate portal API key", err)
		}
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "API Key 已刷新，旧 Key 已失效，请更新客户端配置",
		"api_key": result.APIKey, "snapshot_generation": result.SnapshotGeneration,
	})
}

var (
	errPortalUserUnavailable = errors.New("portal user unavailable")
	errPortalKeyMigrating    = errors.New("portal key migration required")
)

func (server *Server) requireAuth(c *gin.Context, allowPasswordChange bool) (portalAuth, bool) {
	token := server.sessionToken(c)
	session, err := server.sessions.ResolveSession(c.Request.Context(), token)
	if err != nil {
		if errors.Is(err, usage.ErrPortalSessionNotFound) {
			writeError(c, http.StatusUnauthorized, "用户会话已失效", "session_required")
		} else {
			server.internalError(c, "resolve portal session", err)
		}
		return portalAuth{}, false
	}
	records, err := server.activeRecords(c.Request.Context(), session.User)
	if err != nil {
		if errors.Is(err, errPortalUserUnavailable) || errors.Is(err, errPortalKeyMigrating) {
			_ = server.sessions.RevokeSession(c.Request.Context(), token)
			writeError(c, http.StatusUnauthorized, "用户已停用、删除或 Key 正在迁移", "session_required")
		} else {
			server.internalError(c, "validate portal identity", err)
		}
		return portalAuth{}, false
	}
	credential, err := server.sessions.Credential(c.Request.Context(), session.User)
	if err != nil {
		if errors.Is(err, usage.ErrPortalCredentialNotFound) {
			_ = server.sessions.RevokeSession(c.Request.Context(), token)
			writeError(c, http.StatusUnauthorized, "用户凭据未初始化或已失效", "session_required")
		} else {
			server.internalError(c, "read portal credential", err)
		}
		return portalAuth{}, false
	}
	if credential.MustChange && !allowPasswordChange {
		writeError(c, http.StatusForbidden, "首次登录请先修改初始密码", "password_change_required")
		return portalAuth{}, false
	}
	return portalAuth{
		Session: session, Credential: credential, Token: token,
		Records: records, APIKey: records[0].Key,
	}, true
}

func (server *Server) activeRecords(ctx context.Context, user string) ([]controlplane.KeyRecord, error) {
	if user == "" {
		return nil, errPortalUserUnavailable
	}
	records, err := server.identity.ReadKeyRecordsForUsers(ctx, []string{user})
	if err != nil {
		return nil, err
	}
	active := make([]controlplane.KeyRecord, 0, len(records))
	keys := make(map[string]struct{})
	for _, record := range records {
		if record.Status != "active" || !strings.EqualFold(strings.TrimSpace(record.User), user) {
			continue
		}
		active = append(active, record)
		keys[record.Key] = struct{}{}
	}
	if len(active) == 0 {
		return nil, errPortalUserUnavailable
	}
	if len(keys) != 1 {
		return nil, errPortalKeyMigrating
	}
	sort.Slice(active, func(left, right int) bool { return active[left].Label < active[right].Label })
	return active, nil
}

func (server *Server) parseUsageWindow(ctx context.Context, raw string) (usageWindow, error) {
	now := server.now()
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		value = "today"
	}
	if value == "today" {
		timezone, err := server.usageTimezone(ctx)
		if err != nil {
			return usageWindow{}, err
		}
		location, err := time.LoadLocation(timezone)
		if err != nil {
			return usageWindow{}, errors.New("用量时区配置无效")
		}
		local := now.In(location)
		start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).Unix()
		return usageWindow{Name: "today", StartAt: start, EndAt: now.Unix(), Timezone: timezone}, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return usageWindow{}, errors.New("统计范围无效")
	}
	allowed := map[int64]struct{}{3600: {}, 86400: {}, 604800: {}, 2592000: {}}
	if _, found := allowed[seconds]; !found {
		return usageWindow{}, errors.New("统计范围无效")
	}
	return usageWindow{Name: seconds, Seconds: &seconds, StartAt: now.Unix() - seconds, EndAt: now.Unix()}, nil
}

func (server *Server) usageTimezone(ctx context.Context) (string, error) {
	settings, err := server.identity.ReadSettings(ctx)
	if err != nil {
		return "", fmt.Errorf("读取用量时区失败")
	}
	timezone := stringSetting(settings["user_quota.timezone"], "Asia/Shanghai")
	if _, err := time.LoadLocation(timezone); err != nil {
		return "", errors.New("用量时区配置无效")
	}
	return timezone, nil
}

type accountStatus struct {
	Code             string   `json:"code"`
	Label            string   `json:"label"`
	Tone             string   `json:"tone"`
	Reason           string   `json:"reason"`
	Selectable       bool     `json:"selectable"`
	UsedPercent      *float64 `json:"used_percent,omitempty"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
	ResetAt          int64    `json:"reset_at,omitempty"`
}

func presentAccountState(account controlplane.Account, state failover.AccountState, found bool) accountStatus {
	if !account.GroupEnabled {
		return accountStatus{Code: "disabled", Label: "已停用", Tone: "neutral", Reason: "账号已被管理员停用"}
	}
	if !found {
		return accountStatus{Code: "unknown", Label: "状态未知", Tone: "neutral", Reason: "账号运行状态暂不可确认", Selectable: false}
	}
	status := accountStatus{
		Code: "unknown", Label: "状态未知", Tone: "neutral", Reason: "账号运行状态暂不可确认", Selectable: true,
		UsedPercent:      state.UsedPercent,
		RemainingPercent: state.RemainingPercent, ResetAt: state.ResetAt,
	}
	switch state.Reason {
	case "available":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"available", "可用", "success", "账号当前可用", true
	case "quota_exhausted":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_exhausted", "额度耗尽", "danger", "账号周额度已耗尽", false
	case "account_disabled":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"disabled", "已停用", "neutral", "账号已被管理员停用", false
	case "container_not_running":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"stopped", "已停止", "danger", "CPA 服务未运行", false
	case "oauth_missing":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"auth_missing", "未授权", "danger", "OAuth 尚未授权", false
	case "credential_unavailable":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"credential_unavailable", "凭据不可用", "danger", "OAuth 凭据已失效，需要重新授权", false
	case "transient_cooldown":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"transient_cooldown", "临时冷却", "warning", "上游请求临时失败，CPA 正在等待凭据冷却恢复", true
	case "rate_limited":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"rate_limited", "限流中", "warning", "账号近期出现 429，仍可选择并稍后重试", true
	case "degraded":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"degraded", "近期异常", "warning", "账号近期出现请求异常", true
	case "runtime_unknown":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"unknown", "状态未知", "neutral", "CPA 原生状态暂不可查询", true
	case "reserve_reached":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_warning", "额度预留", "warning", "账号已达到预留额度", true
	case "quota_stale":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"unknown", "状态未知", "neutral", "账号实时状态暂不可确认", true
	case "quota_unavailable", "upstream_disallowed":
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_unknown", "额度未知", "neutral", "额度状态暂不可确认", true
	}
	if state.Exhausted {
		status.Code, status.Label, status.Tone, status.Reason, status.Selectable =
			"quota_exhausted", "额度耗尽", "danger", "账号周额度已耗尽", false
	}
	return status
}

func (server *Server) sessionToken(c *gin.Context) string {
	cookie, err := c.Request.Cookie(sessionCookieName)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cookie.Value)
}

func (server *Server) writeSessionCookie(c *gin.Context, token string, expiresAt int64) {
	forwardedProtocol := strings.ToLower(strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]))
	cookie := &http.Cookie{
		Name: sessionCookieName, Value: token, Path: "/usage", HttpOnly: true,
		Secure: server.secureCookies || forwardedProtocol == "https", SameSite: http.SameSiteLaxMode,
	}
	if expiresAt <= 0 {
		cookie.Expires = time.Unix(1, 0).UTC()
		cookie.MaxAge = -1
	} else {
		cookie.Expires = time.Unix(expiresAt, 0).UTC()
		cookie.MaxAge = max(int(cookie.Expires.Sub(server.now()).Seconds()), 1)
	}
	http.SetCookie(c.Writer, cookie)
}

func (server *Server) limitBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maximumBodySize)
		c.Next()
	}
}

func (server *Server) noStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	}
}

func (server *Server) recovery() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				server.logger.Error(
					"portal request panic", zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path), zap.String("panic_type", fmt.Sprintf("%T", recovered)),
					zap.Stack("stack"),
				)
				writeError(c, http.StatusInternalServerError, "服务内部错误", "internal_error")
				c.Abort()
			}
		}()
		c.Next()
	}
}

func (server *Server) internalError(c *gin.Context, operation string, err error) {
	server.logger.Error(operation, zap.String("method", c.Request.Method), zap.String("path", c.Request.URL.Path), zap.Error(err))
	writeError(c, http.StatusInternalServerError, "服务内部错误", "internal_error")
}

func writeError(c *gin.Context, status int, message string, code string) {
	c.AbortWithStatusJSON(status, ErrorEnvelope{Error: APIError{Message: message, Type: code, Code: code}})
}

func normalizeEmail(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	parsed, err := mail.ParseAddress(value)
	if err != nil || parsed.Address != value || strings.Count(value, "@") != 1 {
		return ""
	}
	return value
}

func constantTimeEqual(left string, right string) bool {
	leftDigest := sha256.Sum256([]byte(left))
	rightDigest := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftDigest[:], rightDigest[:]) == 1
}

func recordHasAccount(records []controlplane.KeyRecord, account string) bool {
	for _, record := range records {
		if record.Account == account {
			return true
		}
	}
	return false
}

func accountByID(accounts []controlplane.Account, target string) (controlplane.Account, bool) {
	for _, account := range accounts {
		if account.ID == target {
			return account, true
		}
	}
	return controlplane.Account{}, false
}

func optionalAccount(account string) any {
	if account == "" {
		return nil
	}
	return account
}

func stringSetting(value any, fallback string) string {
	if typed, ok := value.(string); ok && strings.TrimSpace(typed) != "" {
		return strings.TrimSpace(typed)
	}
	return fallback
}
