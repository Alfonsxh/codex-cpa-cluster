package admin

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Alfonsxh/codex-cpa-cluster/internal/controlplane"
	"github.com/gin-gonic/gin"
)

const generalSettingsVersion = 1

var (
	domainPattern    = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	keyPrefixPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,30}_$`)
	envNamePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
)

type generalSettingsValues struct {
	ProductName         string   `json:"product_name"`
	ShortName           string   `json:"short_name"`
	EnvironmentLabel    string   `json:"environment_label"`
	PublicBaseURL       string   `json:"public_base_url"`
	AllowedEmailDomains []string `json:"allowed_email_domains"`
	KeyPrefix           string   `json:"key_prefix"`
	ProviderName        string   `json:"provider_name"`
	APIKeyEnv           string   `json:"api_key_env"`
	DefaultModel        string   `json:"default_model"`
}

type generalSettingsSecurity struct {
	ManagementKeyConfigured   bool `json:"management_key_configured"`
	InitialPasswordConfigured bool `json:"initial_password_configured"`
}

type generalSettingsBranding struct {
	CustomLogo bool   `json:"custom_logo"`
	LogoSHA256 string `json:"logo_sha256,omitempty"`
	UpdatedAt  int64  `json:"updated_at,omitempty"`
}

type generalSettingsResponse struct {
	Version     int                     `json:"version"`
	ApplyMode   string                  `json:"apply_mode"`
	GeneratedAt int64                   `json:"generated_at"`
	Values      generalSettingsValues   `json:"values"`
	Security    generalSettingsSecurity `json:"security"`
	Branding    generalSettingsBranding `json:"branding"`
}

func (server *Server) readGeneralSettings(c *gin.Context) {
	payload, err := server.generalSettings(c)
	if err != nil {
		server.internalError(c, "read general settings", err)
		return
	}
	c.JSON(http.StatusOK, payload)
}

func (server *Server) updateGeneralSettings(c *gin.Context) {
	var body struct {
		Confirm string                `json:"confirm" binding:"required"`
		Values  generalSettingsValues `json:"values" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, "请确认保存通用设置", "invalid_request")
		return
	}
	values, err := normalizeGeneralSettings(body.Values)
	if err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_settings")
		return
	}
	if err := server.store.UpdateSettings(c.Request.Context(), values.settingsMap()); err != nil {
		server.internalError(c, "update general settings", err)
		return
	}
	payload, err := server.generalSettings(c)
	if err != nil {
		server.internalError(c, "read updated general settings", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message":  "通用设置已保存并实时生效",
		"settings": payload,
	})
}

func (server *Server) generalSettings(c *gin.Context) (generalSettingsResponse, error) {
	stored, err := server.store.ReadSettings(c.Request.Context())
	if err != nil {
		return generalSettingsResponse{}, err
	}
	values, err := generalSettingsFromMap(stored)
	if err != nil {
		return generalSettingsResponse{}, err
	}
	statuses, err := server.store.SecretStatuses(c.Request.Context())
	if err != nil {
		return generalSettingsResponse{}, err
	}
	logo, customLogo, err := server.store.ReadBrandingAsset(c.Request.Context(), "logo")
	if err != nil {
		return generalSettingsResponse{}, err
	}
	return generalSettingsResponse{
		Version: generalSettingsVersion, ApplyMode: "live", GeneratedAt: server.now().Unix(), Values: values,
		Security: generalSettingsSecurity{
			ManagementKeyConfigured:   hasSecretStatus(statuses, "cpa_management_key"),
			InitialPasswordConfigured: hasSecretStatus(statuses, "portal_initial_password"),
		},
		Branding: generalSettingsBranding{
			CustomLogo: customLogo, LogoSHA256: logo.SHA256, UpdatedAt: logo.UpdatedAt,
		},
	}, nil
}

func hasSecretStatus(statuses map[string]controlplane.SecretStatus, name string) bool {
	status, found := statuses[name]
	return found && status.SHA256 != ""
}

func generalSettingsFromMap(settings map[string]any) (generalSettingsValues, error) {
	values := defaultGeneralSettings()
	var err error
	if values.ProductName, err = stringSettingValue(settings, "branding.product_name", values.ProductName); err != nil {
		return values, err
	}
	if values.ShortName, err = stringSettingValue(settings, "branding.short_name", values.ShortName); err != nil {
		return values, err
	}
	if values.EnvironmentLabel, err = stringSettingValue(settings, "branding.environment_label", values.EnvironmentLabel); err != nil {
		return values, err
	}
	if values.PublicBaseURL, err = stringSettingValue(settings, "branding.public_base_url", values.PublicBaseURL); err != nil {
		return values, err
	}
	if values.AllowedEmailDomains, err = stringListSettingValue(settings, "identity.allowed_email_domains", values.AllowedEmailDomains); err != nil {
		return values, err
	}
	if values.KeyPrefix, err = stringSettingValue(settings, "identity.key_prefix", values.KeyPrefix); err != nil {
		return values, err
	}
	if values.ProviderName, err = stringSettingValue(settings, "portal.provider_name", values.ProviderName); err != nil {
		return values, err
	}
	if values.APIKeyEnv, err = stringSettingValue(settings, "portal.api_key_env", values.APIKeyEnv); err != nil {
		return values, err
	}
	if values.DefaultModel, err = stringSettingValue(settings, "portal.default_model", values.DefaultModel); err != nil {
		return values, err
	}
	return normalizeGeneralSettings(values)
}

func defaultGeneralSettings() generalSettingsValues {
	return generalSettingsValues{
		ProductName: "Codex CPA Cluster", ShortName: "Codex CPA",
		EnvironmentLabel: "Self-hosted service", PublicBaseURL: "",
		AllowedEmailDomains: []string{}, KeyPrefix: "cpa_", ProviderName: "Codex CPA",
		APIKeyEnv: "CPA_API_KEY", DefaultModel: "gpt-5.6-sol",
	}
}

func normalizeGeneralSettings(values generalSettingsValues) (generalSettingsValues, error) {
	var err error
	if values.ProductName, err = normalizeText(values.ProductName, "产品名称", 2, 64, true); err != nil {
		return values, err
	}
	if values.ShortName, err = normalizeText(values.ShortName, "产品简称", 2, 32, true); err != nil {
		return values, err
	}
	if values.EnvironmentLabel, err = normalizeText(values.EnvironmentLabel, "环境说明", 0, 64, false); err != nil {
		return values, err
	}
	if values.PublicBaseURL, err = normalizeBaseURL(values.PublicBaseURL); err != nil {
		return values, err
	}
	values.AllowedEmailDomains, err = normalizeDomains(values.AllowedEmailDomains)
	if err != nil {
		return values, err
	}
	values.KeyPrefix = strings.ToLower(strings.TrimSpace(values.KeyPrefix))
	if !keyPrefixPattern.MatchString(values.KeyPrefix) {
		return values, errors.New("新 Key 前缀必须为 3-32 位小写字母、数字或下划线，并以下划线结尾")
	}
	if values.ProviderName, err = normalizeText(values.ProviderName, "客户端 Provider 名称", 2, 48, true); err != nil {
		return values, err
	}
	values.APIKeyEnv = strings.TrimSpace(values.APIKeyEnv)
	if !envNamePattern.MatchString(values.APIKeyEnv) {
		return values, errors.New("客户端 Key 环境变量必须为有效的大写环境变量名")
	}
	if values.DefaultModel, err = normalizeText(values.DefaultModel, "客户端默认模型", 1, 128, true); err != nil {
		return values, err
	}
	return values, nil
}

func normalizeText(value string, label string, minimum int, maximum int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", fmt.Errorf("%s不能为空", label)
	}
	length := utf8.RuneCountInString(value)
	if length < minimum {
		return "", fmt.Errorf("%s至少需要 %d 个字符", label, minimum)
	}
	if length > maximum {
		return "", fmt.Errorf("%s不能超过 %d 个字符", label, maximum)
	}
	for _, character := range value {
		if character < 32 {
			return "", fmt.Errorf("%s不能包含控制字符", label)
		}
	}
	return value, nil
}

func normalizeBaseURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	for _, character := range value {
		if character < 32 || character == ' ' || character == '\t' || character == '\n' || character == '\r' {
			return "", errors.New("公开访问地址不得包含空白或控制字符")
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed == nil {
		return "", errors.New("公开访问地址必须为有效的 HTTP(S) URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Hostname() == "" {
		return "", errors.New("公开访问地址必须为有效的 HTTP(S) URL")
	}
	if port := parsed.Port(); port != "" {
		value, portError := strconv.Atoi(port)
		if portError != nil || value < 1 || value > 65535 {
			return "", errors.New("公开访问地址包含无效端口")
		}
	}
	if parsed.User != nil {
		return "", errors.New("公开访问地址不得包含账号或密码")
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("公开访问地址只能使用根路径，且不得包含查询参数或片段")
	}
	return strings.TrimRight(value, "/"), nil
}

func normalizeDomains(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		domain := strings.ToLower(strings.TrimLeft(strings.TrimSpace(value), "@"))
		if domain == "" {
			continue
		}
		if len(domain) > 253 || strings.Contains(domain, "..") || !domainPattern.MatchString(domain) {
			return nil, fmt.Errorf("允许的邮箱域名包含无效域名：%s", domain)
		}
		if _, found := seen[domain]; found {
			continue
		}
		seen[domain] = struct{}{}
		result = append(result, domain)
	}
	return result, nil
}

func stringSettingValue(settings map[string]any, key string, fallback string) (string, error) {
	value, found := settings[key]
	if !found || value == nil {
		return fallback, nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("设置 %s 的数据类型无效", key)
	}
	return text, nil
}

func stringListSettingValue(settings map[string]any, key string, fallback []string) ([]string, error) {
	value, found := settings[key]
	if !found || value == nil {
		return append([]string(nil), fallback...), nil
	}
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...), nil
	case []any:
		result := make([]string, 0, len(values))
		for _, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("设置 %s 的数据类型无效", key)
			}
			result = append(result, text)
		}
		return result, nil
	default:
		return nil, fmt.Errorf("设置 %s 的数据类型无效", key)
	}
}

func (values generalSettingsValues) settingsMap() map[string]any {
	return map[string]any{
		"branding.product_name":          values.ProductName,
		"branding.short_name":            values.ShortName,
		"branding.environment_label":     values.EnvironmentLabel,
		"branding.public_base_url":       values.PublicBaseURL,
		"identity.allowed_email_domains": values.AllowedEmailDomains,
		"identity.key_prefix":            values.KeyPrefix,
		"portal.provider_name":           values.ProviderName,
		"portal.api_key_env":             values.APIKeyEnv,
		"portal.default_model":           values.DefaultModel,
	}
}
