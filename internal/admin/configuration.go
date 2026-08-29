package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const defaultProxySecretName = "cpa_default_proxy_url"

var (
	configurationDurationPattern = regexp.MustCompile(`^([1-9][0-9]*)([smhd])$`)
	configurationColorPattern    = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)
	configurationImagePattern    = regexp.MustCompile(`^[A-Za-z0-9._:/@-]+$`)
	configurationDigestImage     = regexp.MustCompile(`^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$`)
	configurationTimePattern     = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
)

type configurationDefinition struct {
	Key            string
	Label          string
	ValueType      string
	ApplyMode      string
	Default        any
	Minimum        float64
	Maximum        float64
	HasMinimum     bool
	HasMaximum     bool
	MinimumLength  int
	MaximumLength  int
	Choices        map[string]struct{}
	ChoiceOrder    []string
	DigestRequired bool
}

type ConfigurationChange struct {
	Before   map[string]any
	After    map[string]any
	Changed  []string
	Modes    []string
	Rollback bool
}

// ConfigurationApplier owns side effects that cannot be represented by the
// settings rows alone: account projection/runtime refresh, Collector restart,
// quota snapshot refresh and deployment-environment projection.
type ConfigurationApplier interface {
	ApplyConfiguration(context.Context, ConfigurationChange) error
}

type configurationUpdateRequest struct {
	Confirm string         `json:"confirm"`
	Values  map[string]any `json:"values"`
}

type configurationUpdateResponse struct {
	Message           string   `json:"message"`
	Changed           []string `json:"changed"`
	Applied           []string `json:"applied"`
	PendingDeployment bool     `json:"pending_deployment"`
}

var configurationDefinitions = buildConfigurationDefinitions()

var configurationDefinitionByKey = func() map[string]configurationDefinition {
	result := make(map[string]configurationDefinition, len(configurationDefinitions))
	for _, definition := range configurationDefinitions {
		result[definition.Key] = definition
	}
	return result
}()

var retiredConfigurationKeys = map[string]struct{}{
	"gost.enabled": {}, "gost.remote_hosts": {}, "gost.remote_host": {},
	"gost.port_start": {}, "gost.port_end": {}, "runtime.gost_image": {},
	"runtime.admin_base_image": {}, "runtime.gateway_image": {},
	"gateway.listen_address": {}, "gateway.port": {}, "gateway.internal_port": {},
	"management.listen_address": {}, "management.port": {},
	"delivery.gateway_drain_timeout_seconds": {},
}

func buildConfigurationDefinitions() []configurationDefinition {
	text := func(key, label, fallback string, minimum, maximum int, mode string, optional bool) configurationDefinition {
		valueType := "text"
		if optional {
			valueType = "optional_text"
		}
		return configurationDefinition{
			Key: key, Label: label, ValueType: valueType, ApplyMode: mode, Default: fallback,
			MinimumLength: minimum, MaximumLength: maximum,
		}
	}
	boolean := func(key, label string, fallback bool, mode string) configurationDefinition {
		return configurationDefinition{Key: key, Label: label, ValueType: "boolean", ApplyMode: mode, Default: fallback}
	}
	integer := func(key, label string, fallback int64, minimum, maximum int64, mode string) configurationDefinition {
		return configurationDefinition{
			Key: key, Label: label, ValueType: "integer", ApplyMode: mode, Default: fallback,
			Minimum: float64(minimum), Maximum: float64(maximum), HasMinimum: true, HasMaximum: true,
		}
	}
	number := func(key, label string, fallback, minimum, maximum float64, mode string) configurationDefinition {
		return configurationDefinition{
			Key: key, Label: label, ValueType: "number", ApplyMode: mode, Default: fallback,
			Minimum: minimum, Maximum: maximum, HasMinimum: true, HasMaximum: true,
		}
	}
	choice := func(key, label, fallback, mode string, values ...string) configurationDefinition {
		choices := make(map[string]struct{}, len(values))
		for _, value := range values {
			choices[value] = struct{}{}
		}
		return configurationDefinition{
			Key: key, Label: label, ValueType: "choice", ApplyMode: mode, Default: fallback,
			Choices: choices, ChoiceOrder: append([]string(nil), values...),
		}
	}
	simple := func(key, label, valueType string, fallback any, mode string) configurationDefinition {
		return configurationDefinition{Key: key, Label: label, ValueType: valueType, ApplyMode: mode, Default: fallback}
	}

	definitions := []configurationDefinition{
		text("branding.product_name", "产品名称", "Codex CPA Cluster", 2, 64, "live", false),
		text("branding.short_name", "产品简称", "Codex CPA", 2, 32, "live", false),
		text("branding.environment_label", "环境说明", "Self-hosted service", 0, 64, "live", true),
		simple("branding.public_base_url", "公开访问地址", "base_url", "", "live"),
		simple("identity.allowed_email_domains", "允许的邮箱域名", "domain_list", []string{}, "live"),
		simple("identity.key_prefix", "新 Key 前缀", "key_prefix", "cpa_", "live"),
		text("portal.provider_name", "客户端 Provider 名称", "Codex CPA", 2, 48, "live", false),
		simple("portal.api_key_env", "客户端 Key 环境变量", "env_name", "CPA_API_KEY", "live"),
		text("portal.default_model", "客户端默认模型", "gpt-5.6-sol", 1, 128, "live", false),
		boolean("cpa.proxy_enabled", "启用默认上游代理", false, "accounts"),
		simple("cpa.proxy_url", "默认上游代理 URL", "proxy_url_secret", "", "accounts"),
		integer("cpa.request_retry", "请求重试次数", 2, 0, 10, "accounts"),
		choice("cpa.disable_image_generation", "图片工具策略", "chat", "accounts", "chat", "true", "false"),
		integer("cpa.max_retry_credentials", "最大重试凭据数", 1, 1, 10, "accounts"),
		integer("cpa.max_retry_interval", "最大重试等待", 12, 1, 300, "accounts"),
		integer("cpa.transient_error_cooldown_seconds", "临时错误冷却", 10, 1, 300, "accounts"),
		boolean("cpa.session_affinity", "会话亲和", true, "accounts"),
		simple("cpa.session_affinity_ttl", "会话亲和有效期", "duration", "1h", "accounts"),
		boolean("cpa.debug", "调试日志", false, "accounts"),
		boolean("cpa.logging_to_file", "写入 CPA 日志文件", true, "accounts"),
		integer("cpa.logs_max_total_size_mb", "单 CPA 日志容量上限", 64, 16, 1024, "accounts"),
		integer("cpa.error_logs_max_files", "单 CPA 错误文件上限", 10, 1, 100, "accounts"),
		boolean("cpa.usage_statistics_enabled", "官方用量事件", true, "accounts"),
		integer("cpa.usage_queue_retention_seconds", "用量队列保留时间", 3600, 60, 604800, "accounts"),
		integer("usage.quota_cache_seconds", "官方额度缓存", 60, 30, 3600, "live"),
		integer("usage.upstream_timeout_seconds", "官方接口超时", 20, 5, 120, "live"),
		choice("account_failover.mode", "自动切换模式", "active", "live", "off", "active"),
		integer("account_failover.poll_seconds", "额度检查间隔", 60, 30, 3600, "live"),
		number("account_failover.reserve_percent", "目标账号安全余量", 5, 0, 50, "live"),
		integer("account_failover.stale_after_seconds", "额度数据失效时间", 120, 60, 7200, "live"),
		{Key: "user_quota.default_weekly_tokens", Label: "用户周额度系统默认值", ValueType: "nullable_integer", ApplyMode: "quota", Default: nil, Minimum: 1, Maximum: 1_000_000_000_000, HasMinimum: true, HasMaximum: true},
		boolean("user_quota.reset_personal_weekly_on_new_week", "新周恢复默认个人额度", true, "quota"),
		simple("user_quota.timezone", "用户自然周时区", "timezone", "UTC", "collector"),
		integer("user_quota.fail_open_after_seconds", "额度故障放行等待", 300, 30, 3600, "quota"),
	}
	for _, effort := range []struct {
		name     string
		fallback float64
	}{
		{"none", 1}, {"minimal", 1}, {"low", 1}, {"medium", 1}, {"high", 1},
		{"xhigh", 1}, {"max", 2}, {"ultra", 1}, {"auto", 1}, {"unknown", 1},
	} {
		definitions = append(definitions, number(
			"user_quota.reasoning_multiplier."+effort.name,
			strings.ToUpper(effort.name[:1])+effort.name[1:]+" 推理强度倍率",
			effort.fallback, 0.1, 10, "quota",
		))
	}
	for _, effort := range []struct {
		name, fallback string
	}{
		{"none", "#7d8490"}, {"minimal", "#84929a"}, {"low", "#4b8ccf"},
		{"medium", "#7653a6"}, {"high", "#2f73d9"}, {"xhigh", "#5965c7"},
		{"max", "#b2731e"}, {"ultra", "#9b5f9d"}, {"auto", "#5e708a"},
		{"unknown", "#687287"},
	} {
		definitions = append(definitions, simple(
			"admin.account_usage.reasoning_effort_color."+effort.name,
			strings.ToUpper(effort.name[:1])+effort.name[1:]+" 推理强度颜色",
			"color", effort.fallback, "live",
		))
	}
	definitions = append(definitions,
		boolean("notification.enabled", "启用企业微信通知", false, "live"),
		simple("notification.timezone", "通知时区", "timezone", "UTC", "live"),
		simple("notification.daily_times", "每日发送时间", "time_list", "09:00,14:00,18:00", "live"),
		integer("notification.schedule_grace_minutes", "定时补发窗口", 15, 0, 120, "live"),
		boolean("notification.quota_alert_enabled", "启用周额度预警", true, "live"),
		number("notification.weekly_threshold_percent", "周额度预警阈值", 90, 1, 100, "live"),
		integer("portal.session_ttl_seconds", "使用中心登录有效期", 43200, 3600, 43200, "live"),
		number("collector.interval_seconds", "采集轮询间隔", 2, 0.5, 60, "collector"),
		integer("collector.batch_size", "单批采集数量", 100, 1, 500, "collector"),
		integer("accounts.port_start", "新账号端口起点", 18319, 1024, 65535, "future"),
		integer("accounts.port_end", "新账号端口终点", 18999, 1024, 65535, "future"),
		simple("accounts.listen_address", "业务 CPA 监听地址", "ip", "127.0.0.1", "deployment"),
		simple("runtime.cliproxy_image", "CLIProxyAPI 镜像", "image", "docker.m.daocloud.io/eceasy/cli-proxy-api:v7.1.23", "deployment"),
	)
	definitions = append(definitions,
		simple("delivery.release_metadata_image", "发布更新通道", "optional_image", "", "live"),
	)
	return definitions
}

func (server *Server) updateConfiguration(c *gin.Context) {
	var body configurationUpdateRequest
	if err := c.ShouldBindJSON(&body); err != nil || body.Confirm != "save" {
		writeError(c, http.StatusBadRequest, "请确认保存配置", "invalid_request")
		return
	}
	if body.Values == nil {
		writeError(c, http.StatusBadRequest, "配置值必须为对象", "invalid_request")
		return
	}
	changes := make(map[string]any, len(body.Values))
	for key, value := range body.Values {
		changes[key] = value
	}
	// v1 deliberately treats an empty proxy field as "leave unchanged" so a
	// masked secret rendered by the browser cannot accidentally clear it.
	if raw, found := changes["cpa.proxy_url"]; found && strings.TrimSpace(valueString(raw)) == "" {
		delete(changes, "cpa.proxy_url")
	}
	if len(changes) == 0 {
		c.JSON(http.StatusOK, noConfigurationChanges())
		return
	}

	server.configurationLock.Lock()
	defer server.configurationLock.Unlock()
	ctx := c.Request.Context()
	storedBefore, current, proxyBefore, proxyFound, err := server.currentConfiguration(ctx)
	if err != nil {
		server.internalError(c, "read configuration", err)
		return
	}
	updated := cloneConfiguration(current)
	unknown := make([]string, 0)
	for key, raw := range changes {
		definition, found := configurationDefinitionByKey[key]
		if !found {
			unknown = append(unknown, key)
			continue
		}
		value, normalizeError := normalizeConfigurationValue(definition, raw)
		if normalizeError != nil {
			writeError(c, http.StatusBadRequest, normalizeError.Error(), "invalid_request")
			return
		}
		updated[key] = value
	}
	if len(unknown) != 0 {
		sort.Strings(unknown)
		writeError(c, http.StatusBadRequest, "不支持的配置项："+strings.Join(unknown, ", "), "invalid_request")
		return
	}
	if err := validateConfiguration(updated); err != nil {
		writeError(c, http.StatusBadRequest, err.Error(), "invalid_request")
		return
	}
	if enabled, _ := updated["notification.enabled"].(bool); enabled {
		statuses, statusError := server.store.SecretStatuses(ctx)
		if statusError != nil {
			server.internalError(c, "read notification webhook status", statusError)
			return
		}
		if !hasSecretStatus(statuses, "wecom_webhook") {
			writeError(c, http.StatusBadRequest, "启用企业微信通知前必须先配置 Webhook", "invalid_request")
			return
		}
	}

	changed := changedConfigurationKeys(current, updated)
	if len(changed) == 0 {
		c.JSON(http.StatusOK, noConfigurationChanges())
		return
	}
	storedAfter := cloneConfiguration(storedBefore)
	for _, key := range changed {
		if key != "cpa.proxy_url" {
			storedAfter[key] = updated[key]
		}
	}
	for key := range retiredConfigurationKeys {
		delete(storedAfter, key)
	}
	proxyAfter := optionalSecretValue(updated["cpa.proxy_url"])
	if err := server.store.ReplaceSettingsAndSecret(ctx, storedAfter, defaultProxySecretName, proxyAfter); err != nil {
		server.internalError(c, "save configuration", err)
		return
	}
	modes := configurationModes(changed)
	change := ConfigurationChange{Before: current, After: updated, Changed: changed, Modes: modes}
	if err := server.applyConfiguration(ctx, change); err != nil {
		proxyRestore := (*string)(nil)
		if proxyFound {
			value := proxyBefore
			proxyRestore = &value
		}
		storeRollbackError := server.store.ReplaceSettingsAndSecret(
			context.WithoutCancel(ctx), storedBefore, defaultProxySecretName, proxyRestore,
		)
		rollback := ConfigurationChange{
			Before: updated, After: current, Changed: changed, Modes: modes, Rollback: true,
		}
		applyRollbackError := server.applyConfiguration(context.WithoutCancel(ctx), rollback)
		server.logger.Error("configuration apply failed and rollback was attempted",
			zapError(err), zapError(storeRollbackError), zapError(applyRollbackError))
		writeError(c, http.StatusBadGateway, "配置应用失败，已尝试恢复原配置", "configuration_apply_failed")
		return
	}

	server.clearConfigurationCaches()
	applied := make([]string, 0, len(modes))
	pendingDeployment := false
	for _, mode := range modes {
		switch mode {
		case "deployment":
			pendingDeployment = true
		case "future":
		default:
			applied = append(applied, mode)
		}
	}
	message := fmt.Sprintf("已保存 %d 项配置", len(changed))
	if pendingDeployment {
		message += "；业务 CPA 参数已写入私有 Compose 投影，重建对应账号后生效"
	}
	c.JSON(http.StatusOK, configurationUpdateResponse{
		Message: message, Changed: changed, Applied: applied, PendingDeployment: pendingDeployment,
	})
}

func (server *Server) applyConfiguration(ctx context.Context, change ConfigurationChange) error {
	if server.configurationApplier != nil {
		return server.configurationApplier.ApplyConfiguration(ctx, change)
	}
	for _, mode := range change.Modes {
		if mode == "accounts" || mode == "collector" || mode == "deployment" {
			return errors.New("configuration runtime applier is unavailable")
		}
	}
	return nil
}

func (server *Server) currentConfiguration(
	ctx context.Context,
) (stored map[string]any, effective map[string]any, proxy string, proxyFound bool, err error) {
	stored, err = server.store.ReadSettings(ctx)
	if err != nil {
		return nil, nil, "", false, err
	}
	originalStored := cloneConfiguration(stored)
	legacyProxy := ""
	cleaned := make(map[string]any, len(stored))
	for key, value := range stored {
		if _, retired := retiredConfigurationKeys[key]; retired {
			continue
		}
		if _, found := configurationDefinitionByKey[key]; !found {
			return nil, nil, "", false, fmt.Errorf("配置中心包含未知参数：%s", key)
		}
		if key == "cpa.proxy_url" {
			legacyProxy = strings.TrimSpace(valueString(value))
			continue
		}
		cleaned[key] = value
	}
	stored = cleaned
	if stored["account_failover.mode"] == "observe" {
		stored["account_failover.mode"] = "off"
	}
	for _, key := range []string{"accounts.listen_address"} {
		if stored[key] == "0.0.0.0" || stored[key] == "::" {
			stored[key] = "127.0.0.1"
		}
	}
	proxy, proxyFound, err = server.store.ReadSecret(ctx, defaultProxySecretName)
	if err != nil {
		return nil, nil, "", false, err
	}
	if !proxyFound && legacyProxy != "" {
		proxy = legacyProxy
		proxyFound = true
	}
	effective = make(map[string]any, len(configurationDefinitions))
	for _, definition := range configurationDefinitions {
		raw := definition.Default
		if definition.Key == "cpa.proxy_url" {
			raw = proxy
		} else if value, found := stored[definition.Key]; found {
			raw = value
		}
		value, normalizeError := normalizeConfigurationValue(definition, raw)
		if normalizeError != nil {
			return nil, nil, "", false, fmt.Errorf("stored setting %s: %w", definition.Key, normalizeError)
		}
		effective[definition.Key] = value
	}
	if validationError := validateConfiguration(effective); validationError != nil {
		return nil, nil, "", false, validationError
	}
	if !reflect.DeepEqual(originalStored, stored) {
		var proxyValue *string
		if proxyFound {
			value := proxy
			proxyValue = &value
		}
		if err := server.store.ReplaceSettingsAndSecret(
			ctx,
			stored,
			defaultProxySecretName,
			proxyValue,
		); err != nil {
			return nil, nil, "", false, fmt.Errorf("normalize stored configuration: %w", err)
		}
	}
	return stored, effective, proxy, proxyFound, nil
}

func normalizeConfigurationValue(definition configurationDefinition, raw any) (any, error) {
	switch definition.ValueType {
	case "boolean":
		if value, ok := raw.(bool); ok {
			return value, nil
		}
		if value, ok := raw.(string); ok {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "true", "1", "yes", "on":
				return true, nil
			case "false", "0", "no", "off":
				return false, nil
			}
		}
		return nil, fmt.Errorf("%s 必须为布尔值", definition.Label)
	case "integer", "nullable_integer":
		if definition.ValueType == "nullable_integer" && (raw == nil || strings.TrimSpace(valueString(raw)) == "") {
			return nil, nil
		}
		value, err := configurationInteger(raw)
		if err != nil {
			return nil, fmt.Errorf("%s 必须为整数", definition.Label)
		}
		if definition.HasMinimum && float64(value) < definition.Minimum ||
			definition.HasMaximum && float64(value) > definition.Maximum {
			return nil, fmt.Errorf("%s 必须在 %s 至 %s 之间", definition.Label, numberLabel(definition.Minimum), numberLabel(definition.Maximum))
		}
		return value, nil
	case "number":
		value, err := configurationNumber(raw)
		if err != nil {
			return nil, fmt.Errorf("%s 必须为数字", definition.Label)
		}
		if !math.IsInf(value, 0) && !math.IsNaN(value) {
			if definition.HasMinimum && value < definition.Minimum || definition.HasMaximum && value > definition.Maximum {
				return nil, fmt.Errorf("%s 必须在 %s 至 %s 之间", definition.Label, numberLabel(definition.Minimum), numberLabel(definition.Maximum))
			}
			return value, nil
		}
		return nil, fmt.Errorf("%s 必须为有限数字", definition.Label)
	}

	value := strings.TrimSpace(valueString(raw))
	switch definition.ValueType {
	case "text", "optional_text":
		if definition.ValueType == "text" && value == "" {
			return nil, fmt.Errorf("%s不能为空", definition.Label)
		}
		length := utf8.RuneCountInString(value)
		if length < definition.MinimumLength {
			return nil, fmt.Errorf("%s至少需要 %d 个字符", definition.Label, definition.MinimumLength)
		}
		if definition.MaximumLength > 0 && length > definition.MaximumLength {
			return nil, fmt.Errorf("%s不能超过 %d 个字符", definition.Label, definition.MaximumLength)
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return nil, fmt.Errorf("%s不能包含控制字符", definition.Label)
			}
		}
		return value, nil
	case "domain_list":
		values := make([]string, 0)
		switch typed := raw.(type) {
		case []string:
			values = append(values, typed...)
		case []any:
			for _, item := range typed {
				values = append(values, valueString(item))
			}
		default:
			values = strings.FieldsFunc(value, func(character rune) bool {
				return character == ',' || character == '，' || unicode.IsSpace(character)
			})
		}
		return normalizeDomains(values)
	case "key_prefix":
		value = strings.ToLower(value)
		if !keyPrefixPattern.MatchString(value) {
			return nil, fmt.Errorf("%s必须为 3-32 位小写字母、数字或下划线，并以下划线结尾", definition.Label)
		}
		return value, nil
	case "env_name":
		if !envNamePattern.MatchString(value) {
			return nil, fmt.Errorf("%s必须为有效的大写环境变量名", definition.Label)
		}
		return value, nil
	case "choice":
		if _, found := definition.Choices[value]; !found {
			allowed := make([]string, 0, len(definition.Choices))
			for choice := range definition.Choices {
				allowed = append(allowed, choice)
			}
			sort.Strings(allowed)
			return nil, fmt.Errorf("%s 必须选择以下值之一：%s", definition.Label, strings.Join(allowed, ", "))
		}
		return value, nil
	case "color":
		if !configurationColorPattern.MatchString(value) {
			return nil, fmt.Errorf("%s 必须使用 #RRGGBB 颜色格式", definition.Label)
		}
		return strings.ToLower(value), nil
	case "base_url", "proxy_url_secret":
		return normalizeConfigurationURL(definition, value)
	case "duration":
		match := configurationDurationPattern.FindStringSubmatch(strings.ToLower(value))
		if match == nil {
			return nil, errors.New("时间窗口格式应为 30s、5m、1h 或 7d")
		}
		amount, _ := strconv.ParseInt(match[1], 10, 64)
		scale := map[string]int64{"s": 1, "m": 60, "h": 3600, "d": 86400}[match[2]]
		seconds := amount * scale
		if amount <= 0 || seconds < 30 || seconds > 30*24*60*60 {
			return nil, fmt.Errorf("%s 必须在 30 秒至 30 天之间", definition.Label)
		}
		return strings.ToLower(value), nil
	case "timezone":
		if value == "" || len(value) > 64 {
			return nil, fmt.Errorf("%s 必须为有效 IANA 时区", definition.Label)
		}
		if _, err := time.LoadLocation(value); err != nil {
			return nil, fmt.Errorf("%s 必须为有效 IANA 时区", definition.Label)
		}
		return value, nil
	case "time_list":
		return normalizeConfigurationTimes(definition, value)
	case "image", "optional_image":
		if definition.ValueType == "optional_image" && value == "" {
			return "", nil
		}
		if value == "" || len(value) > 255 || !configurationImagePattern.MatchString(value) {
			return nil, fmt.Errorf("%s 的镜像名称无效", definition.Label)
		}
		if definition.DigestRequired && !configurationDigestImage.MatchString(value) {
			return nil, fmt.Errorf("%s 必须使用 name:tag@sha256:digest 固定镜像", definition.Label)
		}
		return value, nil
	case "ip":
		address := net.ParseIP(value)
		if address == nil || address.To4() == nil {
			return nil, fmt.Errorf("%s 必须为有效 IPv4 地址", definition.Label)
		}
		return address.To4().String(), nil
	default:
		return nil, fmt.Errorf("未知配置类型：%s (%s)", definition.ValueType, definition.Key)
	}
}

func normalizeConfigurationURL(definition configurationDefinition, value string) (string, error) {
	if value == "" {
		return "", nil
	}
	for _, character := range value {
		if unicode.IsSpace(character) || unicode.IsControl(character) {
			return "", fmt.Errorf("%s 不得包含空白或控制字符", definition.Label)
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Hostname() == "" {
		return "", fmt.Errorf("%s 必须为有效的 HTTP(S) URL", definition.Label)
	}
	scheme := strings.ToLower(parsed.Scheme)
	allowed := scheme == "http" || scheme == "https"
	if definition.ValueType == "proxy_url_secret" {
		allowed = allowed || scheme == "socks5"
	}
	if !allowed {
		return "", fmt.Errorf("%s 必须为有效的 HTTP(S) URL", definition.Label)
	}
	if definition.ValueType != "proxy_url_secret" && parsed.User != nil {
		return "", fmt.Errorf("%s 不得包含账号或密码", definition.Label)
	}
	if port := parsed.Port(); port != "" {
		value, parseError := strconv.Atoi(port)
		if parseError != nil || value < 1 || value > 65535 {
			return "", fmt.Errorf("%s 包含无效端口", definition.Label)
		}
	}
	if parsed.Path != "" && parsed.Path != "/" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("%s 不得包含查询参数或片段", definition.Label)
	}
	return strings.TrimRight(value, "/"), nil
}

func normalizeConfigurationTimes(definition configurationDefinition, value string) (string, error) {
	parts := strings.FieldsFunc(value, func(character rune) bool {
		return character == ',' || character == '，' || unicode.IsSpace(character)
	})
	if len(parts) == 0 || len(parts) > 12 {
		return "", fmt.Errorf("%s 必须包含 1 至 12 个时间", definition.Label)
	}
	times := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		match := configurationTimePattern.FindStringSubmatch(part)
		if match == nil {
			return "", fmt.Errorf("%s 必须使用 HH:MM 格式", definition.Label)
		}
		hour, _ := strconv.Atoi(match[1])
		minute, _ := strconv.Atoi(match[2])
		if hour > 23 || minute > 59 {
			return "", fmt.Errorf("%s 包含无效时间", definition.Label)
		}
		times[fmt.Sprintf("%02d:%02d", hour, minute)] = struct{}{}
	}
	result := make([]string, 0, len(times))
	for value := range times {
		result = append(result, value)
	}
	sort.Strings(result)
	return strings.Join(result, ","), nil
}

func validateConfiguration(values map[string]any) error {
	for _, key := range []string{"accounts.listen_address"} {
		address, _ := values[key].(string)
		parsed := net.ParseIP(address)
		if parsed == nil || !parsed.IsLoopback() {
			return errors.New("业务 CPA 监听地址必须使用宿主机回环地址")
		}
	}
	portStart := values["accounts.port_start"].(int64)
	portEnd := values["accounts.port_end"].(int64)
	if portStart > portEnd {
		return errors.New("新账号端口起点不能大于终点")
	}
	if values["account_failover.stale_after_seconds"].(int64) < values["account_failover.poll_seconds"].(int64) {
		return errors.New("账号自动切换额度数据失效时间不能小于检查间隔")
	}
	if values["cpa.proxy_enabled"].(bool) && strings.TrimSpace(values["cpa.proxy_url"].(string)) == "" {
		return errors.New("启用默认上游代理前必须配置默认代理 URL")
	}
	return nil
}

func changedConfigurationKeys(before, after map[string]any) []string {
	result := make([]string, 0)
	for _, definition := range configurationDefinitions {
		if !reflect.DeepEqual(before[definition.Key], after[definition.Key]) {
			result = append(result, definition.Key)
		}
	}
	return result
}

func configurationModes(changed []string) []string {
	seen := make(map[string]struct{})
	for _, key := range changed {
		seen[configurationDefinitionByKey[key].ApplyMode] = struct{}{}
	}
	result := make([]string, 0, len(seen))
	for mode := range seen {
		result = append(result, mode)
	}
	sort.Strings(result)
	return result
}

func noConfigurationChanges() configurationUpdateResponse {
	return configurationUpdateResponse{
		Message: "配置没有变化", Changed: []string{}, Applied: []string{}, PendingDeployment: false,
	}
}

func optionalSecretValue(raw any) *string {
	value := strings.TrimSpace(valueString(raw))
	if value == "" {
		return nil
	}
	return &value
}

func cloneConfiguration(values map[string]any) map[string]any {
	result := make(map[string]any, len(values))
	for key, value := range values {
		switch typed := value.(type) {
		case []string:
			result[key] = append([]string(nil), typed...)
		default:
			result[key] = value
		}
	}
	return result
}

func configurationInteger(raw any) (int64, error) {
	switch value := raw.(type) {
	case int:
		return int64(value), nil
	case int64:
		return value, nil
	case float64:
		if math.IsInf(value, 0) || math.IsNaN(value) || math.Trunc(value) != value || value < math.MinInt64 || value > math.MaxInt64 {
			return 0, errors.New("not an integer")
		}
		return int64(value), nil
	case json.Number:
		return value.Int64()
	case string:
		return strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	default:
		return 0, errors.New("not an integer")
	}
}

func configurationNumber(raw any) (float64, error) {
	switch value := raw.(type) {
	case int:
		return float64(value), nil
	case int64:
		return float64(value), nil
	case float64:
		return value, nil
	case json.Number:
		return value.Float64()
	case string:
		return strconv.ParseFloat(strings.TrimSpace(value), 64)
	default:
		return 0, errors.New("not numeric")
	}
}

func valueString(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	return fmt.Sprint(value)
}

func numberLabel(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// zapError keeps configuration rollback logging concise without letting any
// submitted value or encrypted proxy URL enter the structured log.
func zapError(err error) zap.Field {
	if err == nil {
		return zap.Skip()
	}
	return zap.String("error_type", fmt.Sprintf("%T", err))
}

var _ sync.Locker = (*sync.Mutex)(nil)
