package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/failover"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/notifications"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/quota"
	"github.com/Alfonsxh/codex-cpa-cluster/internal/usage"
	"github.com/alexedwards/scs/v2"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const (
	adminSessionCookie = "cpa_admin_session"
	defaultSessionTTL  = 15 * time.Minute
	defaultBodyLimit   = int64(1 << 20)
	brandingBodyLimit  = int64(3 << 20)
)

type ControlPlaneStore interface {
	WithWriteFence(context.Context, func() error) error
	ReadSecret(context.Context, string) (string, bool, error)
	WriteSecret(context.Context, string, string) error
	DeleteSecret(context.Context, string) error
	SecretStatuses(context.Context) (map[string]controlplane.SecretStatus, error)
	ReadAccounts(context.Context) ([]controlplane.Account, error)
	ReadRoutes(context.Context) (map[string]string, error)
	ReadOverviewSummary(context.Context) (controlplane.OverviewSummary, error)
	ListTeams(context.Context) ([]controlplane.Team, error)
	CreateTeam(context.Context, string, string) (controlplane.Team, error)
	UpdateTeam(context.Context, string, string, string) (controlplane.Team, error)
	DeleteTeam(context.Context, string) (controlplane.DeletedTeam, error)
	ReadBrandingAsset(context.Context, string) (controlplane.BrandingAsset, bool, error)
	WriteBrandingAsset(context.Context, string, string, string, []byte) (controlplane.BrandingAsset, error)
	DeleteBrandingAsset(context.Context, string) error
	ReadSettings(context.Context) (map[string]any, error)
	UpdateSettings(context.Context, map[string]any) error
	ReplaceSettingsAndSecret(context.Context, map[string]any, string, *string) error
	ReadRuntimeState(context.Context, string, any) (bool, error)
	WriteRuntimeState(context.Context, string, any) error
	PatchRuntimeState(context.Context, string, map[string]any) error
	KnownUsers(context.Context) ([]string, error)
	UserExists(context.Context, string) (bool, error)
	ListUsers(context.Context, controlplane.UserListOptions) (controlplane.UserPage, error)
	ListUserSummaries(context.Context) ([]controlplane.UserSummary, error)
	ReadKeyRecordsForUsers(context.Context, []string) ([]controlplane.KeyRecord, error)
	ReadUserTeams(context.Context, []string) (map[string]controlplane.UserTeamClassification, error)
	SetUserTeamsExpected(context.Context, []string, *string, controlplane.TeamExpectation) ([]controlplane.TeamAssignment, error)
}

type Config struct {
	Root                 string
	Store                ControlPlaneStore
	Accounts             AccountCatalog
	AccountStates        failover.AccountStateProvider
	AccountRuntime       AccountRuntimeReader
	Activity             failover.ActivityProvider
	OAuth                AccountOAuthReader
	Logger               *zap.Logger
	Now                  func() time.Time
	SessionTTL           time.Duration
	SecureCookies        bool
	Rebalancer           AccountRebalancer
	Usage                UsageReader
	TeamIdentities       TeamIdentitySynchronizer
	NotificationSender   notifications.ContentSender
	Portal               RouteRegistrar
	Users                UserLifecycleService
	Runtime              RuntimeCatalog
	Images               ImageCatalog
	Release              ReleaseCatalog
	RuntimeJobs          RuntimeJobService
	AccountLifecycle     AccountLifecycleService
	QuotaResetter        QuotaResetter
	ConfigurationApplier ConfigurationApplier
	OperationLock        sync.Locker
}

type UsageReader interface {
	UserBreakdown(context.Context, string, string, int64, *int64) (usage.UserBreakdown, error)
	AccountBreakdown(context.Context, string, int64, *int64) (usage.AccountBreakdown, error)
	TeamUsage(context.Context, []string, map[string]string, *int64, *int64) (map[string]usage.TeamUsageMetrics, error)
	TeamBreakdown(context.Context, string, []string, *int64, *int64) (usage.TeamBreakdown, error)
	TokenTimeSeries(context.Context, []string, []string, []string, int64, int64, int64, int, map[string]int64) (usage.TokenTrend, error)
	Status(context.Context) (usage.CollectorStatus, error)
}

type AccountUsageSummaryReader interface {
	AccountSummaries(context.Context, []string, int64, *int64) (map[string]usage.AccountUsageSummary, error)
}

type UserUsageSummaryReader interface {
	UserSummaries(context.Context, int64, *int64) (map[string]usage.WeightedMetrics, error)
	UserSummariesForUsers(context.Context, []string, int64, *int64) (map[string]usage.WeightedMetrics, error)
	UserAccounts(context.Context, string, int64, *int64) (usage.UserAccountSummary, error)
}

// AccountUsageSummaryByAccountReader preserves the legacy account-page
// since-reset contract without issuing one SQLite query per CPA. Each account
// may have a different official quota-period start, while all rows share the
// same end-exclusive snapshot boundary.
type AccountUsageSummaryByAccountReader interface {
	AccountSummariesByStart(context.Context, map[string]int64, *int64) (map[string]usage.AccountUsageSummary, error)
}

type AccountOAuthReader interface {
	Load(string) (quota.OAuthRecord, error)
}

// TeamIdentitySynchronizer keeps the mutable identity catalog in usage.sqlite3
// aligned with control-plane team assignments. Usage events retain the
// membership version that was current when they were ingested, so this write
// must complete before the Admin reports a team change as successful.
type TeamIdentitySynchronizer interface {
	SyncUserTeams(context.Context, map[string]usage.TeamIdentity) (int, error)
}

type AccountRebalancer interface {
	RebalanceAll(context.Context) (failover.RebalanceResult, error)
	EvacuateAccount(context.Context, string) (failover.EvacuationResult, error)
}

type QuotaResetter interface {
	Reset(context.Context, string, string) (quota.ResetResult, error)
}

type quotaResetInspector interface {
	Inspect(context.Context, string) (quota.ResetInspection, error)
}

// RouteRegistrar lets the shared Gin listener host independently owned route
// groups without coupling Admin handlers to the self-service implementation.
type RouteRegistrar interface {
	Register(gin.IRouter)
}

type Server struct {
	root                    string
	store                   ControlPlaneStore
	logger                  *zap.Logger
	now                     func() time.Time
	sessionTTL              time.Duration
	secureCookies           bool
	sessions                *scs.SessionManager
	rebalancer              AccountRebalancer
	accounts                AccountCatalog
	accountStates           failover.AccountStateProvider
	accountRuntime          AccountRuntimeReader
	activity                failover.ActivityProvider
	oauth                   AccountOAuthReader
	usage                   UsageReader
	teamIdentities          TeamIdentitySynchronizer
	notificationSender      notifications.ContentSender
	portal                  RouteRegistrar
	users                   UserLifecycleService
	runtime                 RuntimeCatalog
	images                  ImageCatalog
	release                 ReleaseCatalog
	runtimeJobs             RuntimeJobService
	accountLifecycle        AccountLifecycleService
	quotaResetter           QuotaResetter
	configurationApplier    ConfigurationApplier
	configurationLock       sync.Locker
	releaseStatusMu         sync.Mutex
	releaseStatusCache      *releaseLookupStatus
	releaseStatusCacheUntil time.Time
	sessionGeneration       atomic.Int64
	router                  *gin.Engine
}

type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

type APIError struct {
	Message string `json:"message"`
	Type    string `json:"type,omitempty"`
	Code    string `json:"code"`
}

type adminContext struct {
	Kind      string
	CSRFToken string
	ExpiresAt int64
}

const adminContextKey = "admin.authentication"

var configureGinMode sync.Once

func New(config Config) (*Server, error) {
	if config.Store == nil {
		return nil, errors.New("admin server requires a control-plane store")
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
	if config.OperationLock == nil {
		config.OperationLock = &sync.Mutex{}
	}
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false
	router.HandleMethodNotAllowed = false
	router.RemoveExtraSlash = false
	if err := router.SetTrustedProxies(nil); err != nil {
		return nil, fmt.Errorf("disable trusted proxies: %w", err)
	}

	sessionManager := scs.New()
	sessionManager.Lifetime = config.SessionTTL
	sessionManager.HashTokenInStore = true
	sessionManager.Cookie.Name = adminSessionCookie
	sessionManager.Cookie.Path = "/admin"
	sessionManager.Cookie.HttpOnly = true
	sessionManager.Cookie.SameSite = http.SameSiteStrictMode
	sessionManager.Cookie.Secure = config.SecureCookies
	sessionManager.Cookie.Persist = true
	server := &Server{
		root:                 strings.TrimSpace(config.Root),
		store:                config.Store,
		logger:               config.Logger,
		now:                  config.Now,
		sessionTTL:           config.SessionTTL,
		secureCookies:        config.SecureCookies,
		sessions:             sessionManager,
		rebalancer:           config.Rebalancer,
		accounts:             config.Accounts,
		accountStates:        config.AccountStates,
		accountRuntime:       config.AccountRuntime,
		activity:             config.Activity,
		oauth:                config.OAuth,
		usage:                config.Usage,
		teamIdentities:       config.TeamIdentities,
		notificationSender:   config.NotificationSender,
		portal:               config.Portal,
		users:                config.Users,
		runtime:              config.Runtime,
		images:               config.Images,
		release:              config.Release,
		runtimeJobs:          config.RuntimeJobs,
		accountLifecycle:     config.AccountLifecycle,
		quotaResetter:        config.QuotaResetter,
		configurationApplier: config.ConfigurationApplier,
		configurationLock:    config.OperationLock,
		router:               router,
	}
	server.sessionGeneration.Store(1)
	if server.notificationSender == nil {
		sender, err := notifications.NewFencedSender(
			&notifications.WebhookSender{Store: config.Store},
			config.Store,
		)
		if err != nil {
			return nil, err
		}
		server.notificationSender = sender
	}
	if server.accounts == nil {
		server.accounts, _ = config.Store.(AccountCatalog)
	}
	router.Use(server.recovery(), server.securityHeaders(), server.loadSession())
	server.registerRoutes()
	return server, nil
}

func (server *Server) Handler() http.Handler {
	return server.router
}

func (server *Server) Close() {
	// SCS's in-memory store intentionally owns a process-lifetime cleanup
	// goroutine. Stopping it immediately after construction races with its
	// initialization in SCS v2.9, so service shutdown lets the process reclaim
	// it instead of calling MemStore.StopCleanup.
}

func (server *Server) registerRoutes() {
	server.router.GET("/healthz", server.health)
	server.router.GET("/admin/healthz", server.health)
	server.router.GET("/site-config.json", server.publicSiteConfiguration)
	server.router.GET("/branding/logo", server.brandingLogo)

	api := server.router.Group("/admin/api")
	api.POST("/session", server.limitBody(defaultBodyLimit), server.createSession)
	api.GET("/session", server.requireAdmin(), server.readSession)
	api.DELETE("/session", server.requireAdmin(), server.deleteSession)

	authenticated := api.Group("")
	authenticated.Use(server.requireAdmin())
	authenticated.GET("/teams", server.listTeams)
	authenticated.GET("/teams/usage", server.readTeamUsage)
	authenticated.GET("/teams/usage-breakdown", server.readTeamUsageBreakdown)
	authenticated.POST("/teams", server.limitBody(defaultBodyLimit), server.createTeam)
	authenticated.PUT("/teams", server.limitBody(defaultBodyLimit), server.updateTeam)
	authenticated.DELETE("/teams", server.deleteTeam)
	authenticated.POST("/settings/logo", server.limitBody(brandingBodyLimit), server.updateBrandingLogo)
	authenticated.DELETE("/settings/logo", server.limitBody(defaultBodyLimit), server.deleteBrandingLogo)
	authenticated.GET("/settings/notifications", server.readNotificationSettings)
	authenticated.PUT("/settings/notifications", server.limitBody(defaultBodyLimit), server.updateNotificationSettings)
	authenticated.POST("/settings/notification-webhook", server.limitBody(defaultBodyLimit), server.updateNotificationWebhook)
	authenticated.POST("/settings/notification-webhook/clear", server.limitBody(defaultBodyLimit), server.clearNotificationWebhook)
	authenticated.POST("/notifications/send", server.limitBody(defaultBodyLimit), server.sendNotification)
	authenticated.POST("/notifications/test", server.limitBody(defaultBodyLimit), server.testNotification)
	authenticated.GET("/accounts", server.listAccounts)
	authenticated.POST("/accounts", server.limitBody(defaultBodyLimit), server.createAccount)
	authenticated.POST("/accounts/update", server.limitBody(defaultBodyLimit), server.updateAccount)
	authenticated.POST("/accounts/policy", server.limitBody(defaultBodyLimit), server.updateAccount)
	authenticated.POST("/accounts/repair-proxy", server.limitBody(defaultBodyLimit), server.repairUnavailableAccountProxy)
	authenticated.POST("/accounts/clear-auth", server.limitBody(defaultBodyLimit), server.clearAccountAuth)
	authenticated.POST("/accounts/delete", server.limitBody(defaultBodyLimit), server.deleteAccount)
	authenticated.GET("/native-accounts", server.nativeAccounts)
	authenticated.GET("/accounts/usage-breakdown", server.accountUsageBreakdown)
	authenticated.GET("/users", server.listUsers)
	authenticated.GET("/users/detail", server.userDetail)
	authenticated.POST("/users", server.limitBody(defaultBodyLimit), server.createUser)
	authenticated.GET("/users/quota", server.readUserQuota)
	authenticated.PUT("/users/quota", server.limitBody(defaultBodyLimit), server.updateUserQuota)
	authenticated.DELETE("/users/quota", server.clearUserQuota)
	authenticated.GET("/users/quota-actions", server.readUserQuotaOperations)
	authenticated.POST("/users/quota-actions", server.limitBody(defaultBodyLimit), server.applyUserQuotaAction)
	authenticated.GET("/users/usage-breakdown", server.userUsageBreakdown)
	authenticated.POST("/users/revoke", server.limitBody(defaultBodyLimit), server.revokeUser)
	authenticated.POST("/users/reset-password", server.limitBody(defaultBodyLimit), server.resetUserPassword)
	authenticated.POST("/users/delete", server.limitBody(defaultBodyLimit), server.deleteUser)
	authenticated.POST("/keys/rotate", server.limitBody(defaultBodyLimit), server.rotateUserKey)
	authenticated.PUT("/users/team", server.limitBody(defaultBodyLimit), server.updateUserTeam)
	authenticated.POST("/users/team/batch", server.limitBody(defaultBodyLimit), server.updateUserTeams)
	authenticated.POST("/accounts/rebalance-all", server.limitBody(defaultBodyLimit), server.rebalanceAllAccounts)
	authenticated.POST("/accounts/rebalance", server.limitBody(defaultBodyLimit), server.rebalanceAccount)
	authenticated.GET("/accounts/quota-reset", server.inspectAccountQuotaReset)
	authenticated.POST("/accounts/reset-quota", server.limitBody(defaultBodyLimit), server.resetAccountQuota)
	authenticated.GET("/runtime/services", server.listRuntimeServices)
	authenticated.GET("/runtime/logs", server.readRuntimeLogs)
	authenticated.GET("/runtime/jobs", server.listRuntimeJobs)
	authenticated.GET("/runtime/jobs/:id", server.readRuntimeJob)
	authenticated.POST("/runtime/jobs", server.limitBody(defaultBodyLimit), server.submitConfirmedRuntimeJob)
	authenticated.POST("/runtime/jobs/:id/cancel", server.limitBody(defaultBodyLimit), server.cancelRuntimeJob)
	// Keep the current v1 operational paths while React migrates to the finer
	// runtime namespace. Both route families share the same bounded job pool.
	authenticated.GET("/logs", server.readRuntimeLogs)
	authenticated.GET("/jobs", server.listLegacyRuntimeJobs)
	authenticated.GET("/jobs/:id", server.readLegacyRuntimeJob)
	authenticated.POST("/jobs/cancel", server.limitBody(defaultBodyLimit), server.cancelLegacyRuntimeJob)
	authenticated.POST("/operations", server.limitBody(defaultBodyLimit), server.submitLegacyRuntimeJob)
	authenticated.GET("/overview/summary", server.readOverviewSummary)
	authenticated.GET("/overview/catalog", server.readOverviewCatalog)
	authenticated.GET("/overview/status", server.readOverviewStatus)
	authenticated.GET("/overview/usage", server.readOverviewUsage)
	authenticated.GET("/onboarding", server.readOnboarding)
	authenticated.PUT("/onboarding/preferences", server.limitBody(defaultBodyLimit), server.updateOnboardingPreferences)
	authenticated.GET("/images/cliproxy", server.readCPAImageStatus)
	authenticated.GET("/release", server.readReleaseStatus)
	authenticated.GET("/settings/general", server.readGeneralSettings)
	authenticated.PUT("/settings/general", server.limitBody(defaultBodyLimit), server.updateGeneralSettings)
	authenticated.GET("/settings/workspace", server.readSettingsWorkspace)
	authenticated.GET("/settings/configuration", server.readConfiguration)
	authenticated.POST("/settings/configuration", server.limitBody(defaultBodyLimit), server.updateConfiguration)
	authenticated.POST("/settings/initial-password", server.limitBody(defaultBodyLimit), server.updateInitialPassword)
	authenticated.POST("/settings/management-key", server.limitBody(defaultBodyLimit), server.rotateManagementKey)
	authenticated.GET("/operations/impact", server.readOperationImpact)
	if server.portal != nil {
		server.portal.Register(server.router)
	}

	server.router.NoRoute(func(c *gin.Context) {
		writeError(c, http.StatusNotFound, "接口不存在", "not_found")
	})
}

func (server *Server) clearConfigurationCaches() {
	server.releaseStatusMu.Lock()
	server.releaseStatusCache = nil
	server.releaseStatusCacheUntil = time.Time{}
	server.releaseStatusMu.Unlock()
}

func (server *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (server *Server) createSession(c *gin.Context) {
	provided := strings.TrimSpace(c.GetHeader("X-Management-Key"))
	authenticated, err := server.authenticateManagementKey(c.Request.Context(), provided)
	if err != nil {
		server.internalError(c, "read management credential", err)
		return
	}
	if !authenticated {
		writeError(c, http.StatusUnauthorized, "管理密钥无效", "unauthorized")
		return
	}
	csrfToken, err := randomToken()
	if err != nil {
		server.internalError(c, "generate CSRF token", err)
		return
	}
	expiresAt := server.now().Add(server.sessionTTL).Unix()
	if err := server.sessions.RenewToken(c.Request.Context()); err != nil {
		server.internalError(c, "renew admin session", err)
		return
	}
	server.sessions.Put(c.Request.Context(), "authenticated", true)
	server.sessions.Put(c.Request.Context(), "csrf_token", csrfToken)
	server.sessions.Put(c.Request.Context(), "expires_at", expiresAt)
	server.sessions.Put(c.Request.Context(), "session_generation", server.sessionGeneration.Load())
	token, expiry, err := server.sessions.Commit(c.Request.Context())
	if err != nil {
		server.internalError(c, "save admin session", err)
		return
	}
	server.writeSessionCookie(c, token, expiry)
	c.JSON(http.StatusCreated, gin.H{
		"authenticated": true,
		"csrf_token":    csrfToken,
	})
}

func (server *Server) readSession(c *gin.Context) {
	context := currentAdminContext(c)
	payload := gin.H{"authenticated": true}
	if context.Kind == "session" {
		payload["csrf_token"] = context.CSRFToken
	}
	c.JSON(http.StatusOK, payload)
}

func (server *Server) deleteSession(c *gin.Context) {
	if err := server.sessions.Destroy(c.Request.Context()); err != nil {
		server.internalError(c, "delete admin session", err)
		return
	}
	server.writeSessionCookie(c, "", time.Time{})
	c.JSON(http.StatusOK, gin.H{"logged_out": true})
}

func (server *Server) authenticateManagementKey(ctx context.Context, provided string) (bool, error) {
	if provided == "" {
		return false, nil
	}
	expected, found, err := server.store.ReadSecret(ctx, "cpa_management_key")
	if err != nil || !found {
		return false, err
	}
	providedDigest := sha256.Sum256([]byte(provided))
	expectedDigest := sha256.Sum256([]byte(strings.TrimSpace(expected)))
	return subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) == 1, nil
}

func (server *Server) requireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		provided := strings.TrimSpace(c.GetHeader("X-Management-Key"))
		if provided != "" {
			authenticated, err := server.authenticateManagementKey(c.Request.Context(), provided)
			if err != nil {
				server.internalError(c, "read management credential", err)
				c.Abort()
				return
			}
			if authenticated {
				c.Set(adminContextKey, adminContext{Kind: "management_key"})
				c.Next()
				return
			}
			writeError(c, http.StatusUnauthorized, "管理密钥无效", "unauthorized")
			c.Abort()
			return
		}

		authenticated := server.sessions.GetBool(c.Request.Context(), "authenticated")
		csrfToken := server.sessions.GetString(c.Request.Context(), "csrf_token")
		expiresAt := server.sessions.GetInt64(c.Request.Context(), "expires_at")
		generation := server.sessions.GetInt64(c.Request.Context(), "session_generation")
		if !authenticated || csrfToken == "" || expiresAt <= server.now().Unix() ||
			generation != server.sessionGeneration.Load() {
			writeError(c, http.StatusUnauthorized, "管理密钥无效", "unauthorized")
			c.Abort()
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			providedCSRF := c.GetHeader("X-CSRF-Token")
			providedDigest := sha256.Sum256([]byte(providedCSRF))
			expectedDigest := sha256.Sum256([]byte(csrfToken))
			if providedCSRF == "" || subtle.ConstantTimeCompare(providedDigest[:], expectedDigest[:]) != 1 {
				writeError(c, http.StatusForbidden, "管理会话校验失败", "csrf_required")
				c.Abort()
				return
			}
		}
		c.Set(adminContextKey, adminContext{Kind: "session", CSRFToken: csrfToken, ExpiresAt: expiresAt})
		c.Next()
	}
}

func currentAdminContext(c *gin.Context) adminContext {
	value, _ := c.Get(adminContextKey)
	context, _ := value.(adminContext)
	return context
}

func (server *Server) loadSession() gin.HandlerFunc {
	return func(c *gin.Context) {
		token := ""
		if cookie, err := c.Request.Cookie(adminSessionCookie); err == nil {
			token = cookie.Value
		}
		ctx, err := server.sessions.Load(c.Request.Context(), token)
		if err != nil {
			server.internalError(c, "load admin session", err)
			c.Abort()
			return
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

func (server *Server) writeSessionCookie(c *gin.Context, token string, expiry time.Time) {
	forwardedProtocol := strings.ToLower(strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0]))
	cookie := &http.Cookie{
		Name:     adminSessionCookie,
		Value:    token,
		Path:     "/admin",
		HttpOnly: true,
		Secure:   server.secureCookies || forwardedProtocol == "https",
		SameSite: http.SameSiteStrictMode,
	}
	if expiry.IsZero() {
		cookie.Expires = time.Unix(1, 0).UTC()
		cookie.MaxAge = -1
	} else {
		cookie.Expires = expiry
		cookie.MaxAge = int(server.sessionTTL.Seconds())
	}
	http.SetCookie(c.Writer, cookie)
}

func (server *Server) securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("Referrer-Policy", "no-referrer")
		if strings.ToLower(strings.TrimSpace(strings.Split(c.GetHeader("X-Forwarded-Proto"), ",")[0])) == "https" {
			c.Header("Strict-Transport-Security", "max-age=0")
		}
		if strings.HasPrefix(c.Request.URL.Path, "/admin/api/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	}
}

func (server *Server) limitBody(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
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
					"admin request panic",
					zap.String("method", c.Request.Method),
					zap.String("path", c.Request.URL.Path),
					zap.String("panic_type", fmt.Sprintf("%T", recovered)),
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
	server.logger.Error(
		"admin request failed",
		zap.String("operation", operation),
		zap.String("method", c.Request.Method),
		zap.String("path", c.Request.URL.Path),
		zap.Error(err),
	)
	writeError(c, http.StatusInternalServerError, "服务内部错误", "internal_error")
}

func writeError(c *gin.Context, status int, message string, code string) {
	c.AbortWithStatusJSON(status, ErrorEnvelope{Error: APIError{
		Message: message,
		Type:    "request_error",
		Code:    code,
	}})
}

func randomToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("read secure random source: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
