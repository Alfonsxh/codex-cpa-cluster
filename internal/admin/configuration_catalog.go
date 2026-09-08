package admin

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Alfonsxh/codex-cpa-pool/internal/usage"
	"github.com/gin-gonic/gin"
)

type configurationPresentation struct {
	Group        string
	Description  string
	Unit         string
	ChoiceLabels map[string]string
}

type configurationCatalogChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

type configurationCatalogField struct {
	Key            string                       `json:"key"`
	Label          string                       `json:"label"`
	Description    string                       `json:"description"`
	ValueType      string                       `json:"type"`
	Value          any                          `json:"value"`
	Default        any                          `json:"default"`
	ApplyMode      string                       `json:"apply_mode"`
	Editable       bool                         `json:"editable"`
	Unit           string                       `json:"unit,omitempty"`
	Minimum        *float64                     `json:"min,omitempty"`
	Maximum        *float64                     `json:"max,omitempty"`
	MinimumLength  *int                         `json:"min_length,omitempty"`
	MaximumLength  *int                         `json:"max_length,omitempty"`
	Choices        []configurationCatalogChoice `json:"choices,omitempty"`
	Configured     *bool                        `json:"configured,omitempty"`
	DigestRequired bool                         `json:"digest_required,omitempty"`
}

type configurationCatalogGroup struct {
	Name        string                      `json:"name"`
	Description string                      `json:"description"`
	Fields      []configurationCatalogField `json:"fields"`
}

type configurationCatalogResponse struct {
	Version     int                         `json:"version"`
	GeneratedAt int64                       `json:"generated_at"`
	FieldCount  int                         `json:"field_count"`
	Groups      []configurationCatalogGroup `json:"groups"`
}

func (server *Server) readConfiguration(c *gin.Context) {
	server.configurationLock.Lock()
	defer server.configurationLock.Unlock()

	_, values, proxy, proxyFound, err := server.currentConfiguration(c.Request.Context())
	if err != nil {
		server.internalError(c, "read configuration", err)
		return
	}

	groups := make([]configurationCatalogGroup, 0, len(configurationGroupDescriptions))
	groupIndexes := make(map[string]int, len(configurationGroupDescriptions))
	for _, definition := range configurationDefinitions {
		presentation, found := configurationPresentationByKey[definition.Key]
		if !found {
			server.internalError(c, "read configuration", errMissingConfigurationPresentation(definition.Key))
			return
		}
		groupIndex, found := groupIndexes[presentation.Group]
		if !found {
			groupIndex = len(groups)
			groupIndexes[presentation.Group] = groupIndex
			groups = append(groups, configurationCatalogGroup{
				Name: presentation.Group, Description: configurationGroupDescriptions[presentation.Group],
				Fields: make([]configurationCatalogField, 0),
			})
		}

		field := configurationCatalogField{
			Key: definition.Key, Label: definition.Label, Description: presentation.Description,
			ValueType: definition.ValueType, Value: values[definition.Key], Default: definition.Default,
			ApplyMode: definition.ApplyMode, Editable: true, Unit: presentation.Unit,
			DigestRequired: definition.DigestRequired,
		}
		if definition.Key == quotaResetSettingKey {
			field.Key = quotaRetentionFieldKey
			field.Value = !values[definition.Key].(bool)
			field.Default = !definition.Default.(bool)
		}
		if definition.HasMinimum {
			minimum := definition.Minimum
			field.Minimum = &minimum
		}
		if definition.HasMaximum {
			maximum := definition.Maximum
			field.Maximum = &maximum
		}
		if definition.MinimumLength > 0 {
			minimumLength := definition.MinimumLength
			field.MinimumLength = &minimumLength
		}
		if definition.MaximumLength > 0 {
			maximumLength := definition.MaximumLength
			field.MaximumLength = &maximumLength
		}
		if definition.ValueType == "choice" {
			order := append([]string(nil), definition.ChoiceOrder...)
			if len(order) == 0 {
				for value := range definition.Choices {
					order = append(order, value)
				}
				sort.Strings(order)
			}
			field.Choices = make([]configurationCatalogChoice, 0, len(order))
			for _, value := range order {
				label := presentation.ChoiceLabels[value]
				if label == "" {
					label = value
				}
				field.Choices = append(field.Choices, configurationCatalogChoice{Value: value, Label: label})
			}
		}
		if definition.Key == "cpa.proxy_url" {
			configured := proxyFound && strings.TrimSpace(proxy) != ""
			field.Value = ""
			field.Configured = &configured
		}
		groups[groupIndex].Fields = append(groups[groupIndex].Fields, field)
	}

	c.JSON(http.StatusOK, configurationCatalogResponse{
		Version: 2, GeneratedAt: time.Now().Unix(), FieldCount: len(configurationDefinitions), Groups: groups,
	})
}

type missingConfigurationPresentationError string

func (err missingConfigurationPresentationError) Error() string {
	return "missing configuration presentation: " + string(err)
}

func errMissingConfigurationPresentation(key string) error {
	return missingConfigurationPresentationError(key)
}

var configurationGroupDescriptions = map[string]string{
	"系统设置":   "站点统一的时间与日期边界。",
	"品牌与身份":  "品牌、域名和客户端配置。",
	"CPA 请求": "CPA 请求与代理配置。",
	"用量与额度":  "额度与用量采集。",
	"账号自动切换": "额度不足时自动迁移。",
	"用户额度":   "用户额度和故障策略。",
	"推理强度策略": "模型与推理强度共同决定用户额度 Token 倍率；颜色只影响展示。",
	"企业微信通知": "额度报告和预警。",
	"会话与采集":  "会话和采集设置。",
	"账号供应":   "新 CPA 端口范围。",
	"账号与发布":  "CPA 监听与更新镜像。",
}

var configurationPresentationByKey = map[string]configurationPresentation{
	"branding.product_name":                              {Group: "品牌与身份", Description: "页面产品名称。"},
	"branding.short_name":                                {Group: "品牌与身份", Description: "客户端显示的简称。"},
	"branding.environment_label":                         {Group: "品牌与身份", Description: "入口页环境说明，可留空。"},
	"branding.public_base_url":                           {Group: "品牌与身份", Description: "通知与导出地址；留空用当前站点。"},
	"identity.allowed_email_domains":                     {Group: "品牌与身份", Description: "逗号分隔；创建用户前至少填一个。"},
	"identity.key_prefix":                                {Group: "品牌与身份", Description: "新 Key 前缀，以下划线结尾。"},
	"portal.provider_name":                               {Group: "品牌与身份", Description: "客户端 Provider 名称。"},
	"portal.api_key_env":                                 {Group: "品牌与身份", Description: "Shell Key 变量名。"},
	"portal.default_model":                               {Group: "品牌与身份", Description: "客户端默认模型。"},
	"cpa.proxy_enabled":                                  {Group: "CPA 请求", Description: "仅用于继承默认代理的 CPA。"},
	"cpa.proxy_url":                                      {Group: "CPA 请求", Description: "默认代理地址（HTTP/HTTPS/SOCKS5）。"},
	"cpa.request_retry":                                  {Group: "CPA 请求", Description: "上游失败重试次数。"},
	"cpa.disable_image_generation":                       {Group: "CPA 请求", Description: "图片工具启用策略。", ChoiceLabels: map[string]string{"chat": "仅普通对话禁用（推荐）", "true": "全部禁用", "false": "全部启用"}},
	"cpa.max_retry_credentials":                          {Group: "CPA 请求", Description: "单次切换凭据上限。"},
	"cpa.max_retry_interval":                             {Group: "CPA 请求", Description: "冷却凭据最长等待时间。", Unit: "秒"},
	"cpa.transient_error_cooldown_seconds":               {Group: "CPA 请求", Description: "临时错误冷却时间。", Unit: "秒"},
	"cpa.session_affinity":                               {Group: "CPA 请求", Description: "是否复用会话凭据。"},
	"cpa.session_affinity_ttl":                           {Group: "CPA 请求", Description: "凭据复用时长，如 30s。"},
	"cpa.debug":                                          {Group: "CPA 请求", Description: "排障时开启，会增加日志量。"},
	"cpa.logging_to_file":                                {Group: "CPA 请求", Description: "是否保存 CPA 日志。"},
	"cpa.logs_max_total_size_mb":                         {Group: "CPA 请求", Description: "单个 CPA 上限，超出删除最旧日志。", Unit: "MiB"},
	"cpa.error_logs_max_files":                           {Group: "CPA 请求", Description: "单个 CPA 错误日志文件上限。", Unit: "个"},
	"cpa.usage_statistics_enabled":                       {Group: "用量与额度", Description: "关闭后停止采集新增 Token 用量。"},
	"cpa.usage_queue_retention_seconds":                  {Group: "用量与额度", Description: "中断时事件保留时间。", Unit: "秒"},
	"usage.quota_cache_seconds":                          {Group: "用量与额度", Description: "官方额度缓存时间。", Unit: "秒"},
	"usage.upstream_timeout_seconds":                     {Group: "用量与额度", Description: "官方接口超时时间。", Unit: "秒"},
	"account_failover.mode":                              {Group: "账号自动切换", Description: "官方周额度耗尽后自动迁移用户。", ChoiceLabels: map[string]string{"off": "关闭", "active": "自动执行"}},
	"account_failover.poll_seconds":                      {Group: "账号自动切换", Description: "官方额度检查周期。", Unit: "秒"},
	"account_failover.reserve_percent":                   {Group: "账号自动切换", Description: "剩余额度不高于此值时不接收迁入。", Unit: "%"},
	"account_failover.stale_after_seconds":               {Group: "账号自动切换", Description: "额度过期后停止迁移。", Unit: "秒"},
	"user_quota.default_weekly_tokens":                   {Group: "用户额度", Description: "每人自然周加权上限；留空不限额，个人策略优先。", Unit: "Token"},
	quotaResetSettingKey:                                 {Group: "用户额度", Description: "对所有用户生效。关闭时，本周额度不变，下周一 00:00 按系统时区恢复组织默认额度；开启时持续保留个人额度。每周用量仍重新累计，临时追加额度仍在换周后失效。"},
	"system.timezone":                                    {Group: "系统设置", Description: "统一用于页面时间、今日用量、自然周额度与通知调度。修改后将重新归集本周用量。"},
	"user_quota.fail_open_after_seconds":                 {Group: "用户额度", Description: "采集异常超时后放行并告警。", Unit: "秒"},
	"user_quota.reasoning_multiplier.none":               {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.minimal":            {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.low":                {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.medium":             {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.high":               {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.xhigh":              {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.max":                {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.ultra":              {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.auto":               {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"user_quota.reasoning_multiplier.unknown":            {Group: "推理强度策略", Description: "新采集事件的 Token 倍率。", Unit: "倍"},
	"admin.account_usage.reasoning_effort_color.none":    {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.minimal": {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.low":     {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.medium":  {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.high":    {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.xhigh":   {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.max":     {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.ultra":   {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.auto":    {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"admin.account_usage.reasoning_effort_color.unknown": {Group: "推理强度策略", Description: "账号明细显示颜色。"},
	"notification.enabled":                               {Group: "企业微信通知", Description: "是否发送企业微信通知。"},
	"notification.daily_times":                           {Group: "企业微信通知", Description: "HH:MM 格式，多个时间用逗号分隔。"},
	"notification.schedule_grace_minutes":                {Group: "企业微信通知", Description: "计划发送时间后允许补发的时长。", Unit: "分钟"},
	"notification.quota_alert_enabled":                   {Group: "企业微信通知", Description: "是否发送周额度预警。"},
	"notification.weekly_threshold_percent":              {Group: "企业微信通知", Description: "账号周额度已用比例达到此值时预警。", Unit: "%"},
	"portal.session_ttl_seconds":                         {Group: "会话与采集", Description: "仅影响保存后的新登录会话。", Unit: "秒"},
	"collector.interval_seconds":                         {Group: "会话与采集", Description: "采集轮询间隔。", Unit: "秒"},
	"collector.batch_size":                               {Group: "会话与采集", Description: "单个 CPA 每批采集事件上限。"},
	"accounts.port_start":                                {Group: "账号供应", Description: "新 CPA 端口起点。"},
	"accounts.port_end":                                  {Group: "账号供应", Description: "新 CPA 端口终点，不小于起点。"},
	"accounts.listen_address":                            {Group: "账号与发布", Description: "仅允许宿主机回环地址。"},
	"runtime.cliproxy_image":                             {Group: "账号与发布", Description: "在账号管理中拉取并验证更新。"},
}

func init() {
	for _, model := range usage.ModelMultiplierDefinitions() {
		description := "新采集事件的模型 Token 倍率。"
		if model.Model == "unknown" {
			description = "模型未匹配时用于新采集事件的 Token 倍率。"
		}
		configurationPresentationByKey[usage.ModelMultiplierSettingKey(model.Model)] = configurationPresentation{
			Group: "推理强度策略", Description: description, Unit: "倍",
		}
	}
}
