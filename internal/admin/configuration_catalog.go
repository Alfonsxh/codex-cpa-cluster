package admin

import (
	"net/http"
	"sort"
	"strings"
	"time"

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
	"品牌与身份":  "品牌、域名和客户端配置。",
	"CPA 请求": "CPA 请求与代理配置。",
	"用量与额度":  "额度与用量采集。",
	"账号自动切换": "额度不足时自动迁移。",
	"用户额度":   "用户额度和故障策略。",
	"推理强度策略": "Token 倍率和颜色。",
	"企业微信通知": "额度报告和预警。",
	"会话与采集":  "会话和采集设置。",
	"账号供应":   "新 CPA 端口范围。",
	"账号与发布":  "CPA 监听、镜像和版本。",
}

var configurationPresentationByKey = map[string]configurationPresentation{
	"branding.product_name":                              {Group: "品牌与身份", Description: "页面产品名称。"},
	"branding.short_name":                                {Group: "品牌与身份", Description: "客户端显示的简称。"},
	"branding.environment_label":                         {Group: "品牌与身份", Description: "入口页环境说明，可留空。"},
	"branding.public_base_url":                           {Group: "品牌与身份", Description: "通知和导出的访问地址。"},
	"identity.allowed_email_domains":                     {Group: "品牌与身份", Description: "允许的邮箱域名，逗号分隔。"},
	"identity.key_prefix":                                {Group: "品牌与身份", Description: "新 Key 前缀，以下划线结尾。"},
	"portal.provider_name":                               {Group: "品牌与身份", Description: "客户端 Provider 名称。"},
	"portal.api_key_env":                                 {Group: "品牌与身份", Description: "Shell Key 变量名。"},
	"portal.default_model":                               {Group: "品牌与身份", Description: "客户端默认模型。"},
	"cpa.proxy_enabled":                                  {Group: "CPA 请求", Description: "是否使用默认代理。"},
	"cpa.proxy_url":                                      {Group: "CPA 请求", Description: "默认代理地址（HTTP/HTTPS/SOCKS5）。"},
	"cpa.request_retry":                                  {Group: "CPA 请求", Description: "上游失败重试次数。"},
	"cpa.disable_image_generation":                       {Group: "CPA 请求", Description: "图片工具启用策略。", ChoiceLabels: map[string]string{"chat": "仅普通对话禁用（推荐）", "true": "全部禁用", "false": "全部启用"}},
	"cpa.max_retry_credentials":                          {Group: "CPA 请求", Description: "单次切换凭据上限。"},
	"cpa.max_retry_interval":                             {Group: "CPA 请求", Description: "冷却凭据最长等待时间。", Unit: "秒"},
	"cpa.transient_error_cooldown_seconds":               {Group: "CPA 请求", Description: "临时错误冷却时间。", Unit: "秒"},
	"cpa.session_affinity":                               {Group: "CPA 请求", Description: "是否复用会话凭据。"},
	"cpa.session_affinity_ttl":                           {Group: "CPA 请求", Description: "凭据复用时长，如 30s。"},
	"cpa.debug":                                          {Group: "CPA 请求", Description: "是否开启调试日志。"},
	"cpa.logging_to_file":                                {Group: "CPA 请求", Description: "是否保存 CPA 日志。"},
	"cpa.logs_max_total_size_mb":                         {Group: "CPA 请求", Description: "单个 CPA 日志容量上限。", Unit: "MiB"},
	"cpa.error_logs_max_files":                           {Group: "CPA 请求", Description: "单个 CPA 错误日志数量。", Unit: "个"},
	"cpa.usage_statistics_enabled":                       {Group: "用量与额度", Description: "是否采集用户 Token。"},
	"cpa.usage_queue_retention_seconds":                  {Group: "用量与额度", Description: "中断时事件保留时间。", Unit: "秒"},
	"usage.quota_cache_seconds":                          {Group: "用量与额度", Description: "官方额度缓存时间。", Unit: "秒"},
	"usage.upstream_timeout_seconds":                     {Group: "用量与额度", Description: "官方接口超时时间。", Unit: "秒"},
	"account_failover.mode":                              {Group: "账号自动切换", Description: "额度耗尽后是否迁移。", ChoiceLabels: map[string]string{"off": "关闭", "active": "自动执行"}},
	"account_failover.poll_seconds":                      {Group: "账号自动切换", Description: "官方额度检查周期。", Unit: "秒"},
	"account_failover.reserve_percent":                   {Group: "账号自动切换", Description: "迁入账号最低余量。", Unit: "%"},
	"account_failover.stale_after_seconds":               {Group: "账号自动切换", Description: "额度过期后停止迁移。", Unit: "秒"},
	"user_quota.default_weekly_tokens":                   {Group: "用户额度", Description: "每周 Token 上限，留空不限额。", Unit: "Token"},
	"user_quota.reset_personal_weekly_on_new_week":       {Group: "用户额度", Description: "新周恢复系统默认额度。"},
	"user_quota.timezone":                                {Group: "用户额度", Description: "周额度和今日用量时区。"},
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
	"notification.timezone":                              {Group: "企业微信通知", Description: "通知和额度刷新时区。"},
	"notification.daily_times":                           {Group: "企业微信通知", Description: "每日发送时间，逗号分隔。"},
	"notification.schedule_grace_minutes":                {Group: "企业微信通知", Description: "服务恢复后的补发分钟数。", Unit: "分钟"},
	"notification.quota_alert_enabled":                   {Group: "企业微信通知", Description: "是否发送周额度预警。"},
	"notification.weekly_threshold_percent":              {Group: "企业微信通知", Description: "周额度预警百分比。", Unit: "%"},
	"portal.session_ttl_seconds":                         {Group: "会话与采集", Description: "用户会话有效期。", Unit: "秒"},
	"collector.interval_seconds":                         {Group: "会话与采集", Description: "采集轮询间隔。", Unit: "秒"},
	"collector.batch_size":                               {Group: "会话与采集", Description: "每批采集事件数。"},
	"accounts.port_start":                                {Group: "账号供应", Description: "新 CPA 端口起点。"},
	"accounts.port_end":                                  {Group: "账号供应", Description: "新 CPA 端口终点。"},
	"accounts.listen_address":                            {Group: "账号与发布", Description: "CPA 宿主机监听地址。"},
	"runtime.cliproxy_image":                             {Group: "账号与发布", Description: "业务 CPA 更新镜像。"},
}
