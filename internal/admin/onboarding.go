package admin

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

const (
	onboardingVersion          = 1
	onboardingSkippedSetting   = "onboarding.skipped_recommended"
	onboardingRequiredKind     = "required"
	onboardingRecommendedKind  = "recommended"
	onboardingCompleteStatus   = "complete"
	onboardingIncompleteStatus = "incomplete"
	onboardingSkippedStatus    = "skipped"
)

var onboardingRecommendedIDs = []string{
	"public_base_url",
	"quota_timezone",
	"weekly_quota",
	"notifications",
	"branding",
	"proxy",
}

type onboardingStep struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Status      string   `json:"status"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	ActionPath  string   `json:"action_path"`
	Blockers    []string `json:"blockers"`
}

type onboardingRequiredProgress struct {
	Complete int `json:"complete"`
	Total    int `json:"total"`
}

type onboardingRecommendedProgress struct {
	Complete int `json:"complete"`
	Skipped  int `json:"skipped"`
	Total    int `json:"total"`
}

type onboardingStatusResponse struct {
	Version            int                           `json:"version"`
	GeneratedAt        int64                         `json:"generated_at"`
	RequiredComplete   bool                          `json:"required_complete"`
	Required           onboardingRequiredProgress    `json:"required"`
	Recommended        onboardingRecommendedProgress `json:"recommended"`
	SkippedRecommended []string                      `json:"skipped_recommended"`
	Steps              []onboardingStep              `json:"steps"`
}

type onboardingPreferencesPayload struct {
	Confirm            string   `json:"confirm"`
	SkippedRecommended []string `json:"skipped_recommended"`
}

func (server *Server) readOnboarding(c *gin.Context) {
	payload, err := server.onboardingStatus(c.Request.Context())
	if err != nil {
		server.internalError(c, "read onboarding status", err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (server *Server) updateOnboardingPreferences(c *gin.Context) {
	var body onboardingPreferencesPayload
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, "请确认保存初始化偏好", "invalid_request")
		return
	}
	skipped, err := normalizeSkippedRecommended(body.SkippedRecommended)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if err := server.store.UpdateSettings(c.Request.Context(), map[string]any{
		onboardingSkippedSetting: skipped,
	}); err != nil {
		server.internalError(c, "update onboarding preferences", err)
		return
	}
	payload, err := server.onboardingStatus(c.Request.Context())
	if err != nil {
		server.internalError(c, "read updated onboarding status", err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (server *Server) onboardingStatus(ctx context.Context) (onboardingStatusResponse, error) {
	settings, err := server.store.ReadSettings(ctx)
	if err != nil {
		return onboardingStatusResponse{}, fmt.Errorf("read onboarding settings: %w", err)
	}
	secretStatuses, err := server.store.SecretStatuses(ctx)
	if err != nil {
		return onboardingStatusResponse{}, fmt.Errorf("read onboarding secret statuses: %w", err)
	}
	values, err := generalSettingsFromMap(settings)
	if err != nil {
		return onboardingStatusResponse{}, fmt.Errorf("read onboarding general settings: %w", err)
	}
	skipped, err := skippedRecommendedFromSettings(settings)
	if err != nil {
		return onboardingStatusResponse{}, err
	}
	_, customLogo, err := server.store.ReadBrandingAsset(ctx, "logo")
	if err != nil {
		return onboardingStatusResponse{}, fmt.Errorf("read onboarding branding status: %w", err)
	}

	required := make([]onboardingStep, 0, 2)
	emailDomainsComplete := len(values.AllowedEmailDomains) > 0
	required = append(required, onboardingRequiredStep(
		"email_domains", emailDomainsComplete, "组织与访问", "配置允许创建和登录用户的邮箱域名。",
		"/configuration?group=品牌与身份&key=identity.allowed_email_domains", nil,
	))
	initialPasswordComplete := hasSecretStatus(secretStatuses, portalInitialPasswordSecret)
	required = append(required, onboardingRequiredStep(
		"initial_password", initialPasswordComplete, "用户初始密码", "新用户首次登录使用，登录后必须立即修改。",
		"/configuration?section=access", nil,
	))

	recommendedConfigured := map[string]bool{
		"public_base_url": strings.TrimSpace(values.PublicBaseURL) != "",
		"quota_timezone":  configuredNonEmptyString(settings, "user_quota.timezone"),
		"weekly_quota":    configuredPositiveNumber(settings, "user_quota.default_weekly_tokens"),
		"notifications":   hasSecretStatus(secretStatuses, "wecom_webhook"),
		"branding":        customLogo || brandingCustomized(values),
		"proxy":           settingBool(settings, "cpa.proxy_enabled") && hasSecretStatus(secretStatuses, defaultProxySecretName),
	}
	recommendedDefinitions := []onboardingStep{
		{ID: "public_base_url", Kind: onboardingRecommendedKind, Title: "公开访问地址", Description: "用于通知和客户端配置导出；留空时浏览器仍使用当前来源。", ActionPath: "/configuration?group=品牌与身份&key=branding.public_base_url"},
		{ID: "quota_timezone", Kind: onboardingRecommendedKind, Title: "用户额度时区", Description: "决定自然周额度和今日用量的日期边界。", ActionPath: "/configuration?group=用户额度&key=user_quota.timezone"},
		{ID: "weekly_quota", Kind: onboardingRecommendedKind, Title: "默认周额度", Description: "为新用户设置组织级默认 Token 上限；留空表示默认不限额。", ActionPath: "/configuration?group=用户额度&key=user_quota.default_weekly_tokens"},
		{ID: "notifications", Kind: onboardingRecommendedKind, Title: "企业微信通知", Description: "配置额度报告和异常提醒 Webhook。", ActionPath: "/configuration?group=企业微信通知"},
		{ID: "branding", Kind: onboardingRecommendedKind, Title: "品牌信息", Description: "按需设置产品名称、环境说明和 Logo。", ActionPath: "/configuration?group=品牌与身份"},
		{ID: "proxy", Kind: onboardingRecommendedKind, Title: "默认上游代理", Description: "仅在访问上游必须经过代理时配置。", ActionPath: "/configuration?group=CPA%20请求&key=cpa.proxy_url"},
	}
	skippedSet := make(map[string]struct{}, len(skipped))
	for _, id := range skipped {
		skippedSet[id] = struct{}{}
	}
	recommendedProgress := onboardingRecommendedProgress{Total: len(recommendedDefinitions)}
	for index := range recommendedDefinitions {
		step := &recommendedDefinitions[index]
		step.Blockers = make([]string, 0)
		switch {
		case recommendedConfigured[step.ID]:
			step.Status = onboardingCompleteStatus
			recommendedProgress.Complete++
		case hasString(skippedSet, step.ID):
			step.Status = onboardingSkippedStatus
			recommendedProgress.Skipped++
		default:
			step.Status = onboardingIncompleteStatus
		}
	}

	requiredProgress := onboardingRequiredProgress{Total: len(required)}
	for _, step := range required {
		if step.Status == onboardingCompleteStatus {
			requiredProgress.Complete++
		}
	}
	requiredComplete := requiredProgress.Complete == requiredProgress.Total
	steps := append(required, recommendedDefinitions...)
	return onboardingStatusResponse{
		Version: onboardingVersion, GeneratedAt: server.now().Unix(), RequiredComplete: requiredComplete,
		Required: requiredProgress, Recommended: recommendedProgress,
		SkippedRecommended: skipped, Steps: steps,
	}, nil
}

func onboardingRequiredStep(
	id string,
	complete bool,
	title string,
	description string,
	actionPath string,
	blockers []string,
) onboardingStep {
	status := onboardingIncompleteStatus
	if complete {
		status = onboardingCompleteStatus
	}
	if blockers == nil {
		blockers = make([]string, 0)
	}
	return onboardingStep{
		ID: id, Kind: onboardingRequiredKind, Status: status, Title: title,
		Description: description, ActionPath: actionPath, Blockers: blockers,
	}
}

func normalizeSkippedRecommended(values []string) ([]string, error) {
	allowed := make(map[string]struct{}, len(onboardingRecommendedIDs))
	for _, id := range onboardingRecommendedIDs {
		allowed[id] = struct{}{}
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		id := strings.TrimSpace(raw)
		if _, found := allowed[id]; !found {
			return nil, fmt.Errorf("不支持跳过初始化项目：%s", id)
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Strings(result)
	return result, nil
}

func skippedRecommendedFromSettings(settings map[string]any) ([]string, error) {
	values, err := stringListSettingValue(settings, onboardingSkippedSetting, []string{})
	if err != nil {
		return nil, fmt.Errorf("read skipped onboarding recommendations: %w", err)
	}
	allowed := make(map[string]struct{}, len(onboardingRecommendedIDs))
	for _, id := range onboardingRecommendedIDs {
		allowed[id] = struct{}{}
	}
	filtered := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, id := range values {
		if _, found := allowed[id]; !found {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		filtered = append(filtered, id)
	}
	sort.Strings(filtered)
	return filtered, nil
}

func configuredNonEmptyString(settings map[string]any, key string) bool {
	value, found := settings[key].(string)
	return found && strings.TrimSpace(value) != ""
}

func configuredPositiveNumber(settings map[string]any, key string) bool {
	value, found := settings[key]
	if !found || value == nil {
		return false
	}
	switch typed := value.(type) {
	case float64:
		return typed > 0
	case int:
		return typed > 0
	case int64:
		return typed > 0
	default:
		return false
	}
}

func settingBool(settings map[string]any, key string) bool {
	value, _ := settings[key].(bool)
	return value
}

func brandingCustomized(values generalSettingsValues) bool {
	defaults := defaultGeneralSettings()
	return values.ProductName != defaults.ProductName || values.ShortName != defaults.ShortName ||
		values.EnvironmentLabel != defaults.EnvironmentLabel
}

func hasString(values map[string]struct{}, value string) bool {
	_, found := values[value]
	return found
}
