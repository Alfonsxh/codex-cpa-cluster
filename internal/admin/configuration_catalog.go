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
	"品牌与身份":  "名称、域名、Key 前缀和导出配置。",
	"CPA 请求": "业务 CPA 的请求和代理设置。",
	"用量与额度":  "额度读取和用量采集设置。",
	"账号自动切换": "额度不足时自动迁移用户。",
	"用户额度":   "系统默认额度和故障策略。",
	"推理强度策略": "Token 倍率和明细配色。",
	"企业微信通知": "额度报告和阈值提醒。",
	"会话与采集":  "会话有效期和采集性能。",
	"账号供应":   "新建 CPA 的端口范围。",
	"账号与发布":  "CPA 监听、镜像和版本提醒。",
}

var configurationPresentationByKey = map[string]configurationPresentation{
	"branding.product_name":                              {Group: "品牌与身份", Description: "各页面显示的产品名。"},
	"branding.short_name":                                {Group: "品牌与身份", Description: "客户端显示的简称。"},
	"branding.environment_label":                         {Group: "品牌与身份", Description: "入口页环境说明，可留空。"},
	"branding.public_base_url":                           {Group: "品牌与身份", Description: "通知和导出的访问地址。"},
	"identity.allowed_email_domains":                     {Group: "品牌与身份", Description: "域名用逗号分隔，至少一个。"},
	"identity.key_prefix":                                {Group: "品牌与身份", Description: "以下划线结尾，仅影响新 Key。"},
	"portal.provider_name":                               {Group: "品牌与身份", Description: "客户端 Provider 名称。"},
	"portal.api_key_env":                                 {Group: "品牌与身份", Description: "Shell 配置变量名。"},
	"portal.default_model":                               {Group: "品牌与身份", Description: "客户端默认模型。"},
	"cpa.proxy_enabled":                                  {Group: "CPA 请求", Description: "是否启用默认代理。"},
	"cpa.proxy_url":                                      {Group: "CPA 请求", Description: "默认代理地址，支持 HTTP/HTTPS/SOCKS5。"},
	"cpa.request_retry":                                  {Group: "CPA 请求", Description: "上游失败重试次数。"},
	"cpa.disable_image_generation":                       {Group: "CPA 请求", Description: "图片工具开关，避免 image_gen 冲突。", ChoiceLabels: map[string]string{"chat": "仅普通对话禁用（推荐）", "true": "全部禁用", "false": "全部启用"}},
	"cpa.max_retry_credentials":                          {Group: "CPA 请求", Description: "单次最多切换凭据数。"},
	"cpa.max_retry_interval":                             {Group: "CPA 请求", Description: "冷却凭据最长等待时间。", Unit: "秒"},
	"cpa.transient_error_cooldown_seconds":               {Group: "CPA 请求", Description: "凭据错误冷却时间。", Unit: "秒"},
	"cpa.session_affinity":                               {Group: "CPA 请求", Description: "会话优先复用原凭据。"},
	"cpa.session_affinity_ttl":                           {Group: "CPA 请求", Description: "凭据保持时间，如 30s、5m。"},
	"cpa.debug":                                          {Group: "CPA 请求", Description: "排障开关，可能增加日志。"},
	"cpa.logging_to_file":                                {Group: "CPA 请求", Description: "保存 CPA 日志并自动清理。"},
	"cpa.logs_max_total_size_mb":                         {Group: "CPA 请求", Description: "单个 CPA 日志容量上限。", Unit: "MiB"},
	"cpa.error_logs_max_files":                           {Group: "CPA 请求", Description: "单个 CPA 错误日志数量。", Unit: "个"},
	"cpa.usage_statistics_enabled":                       {Group: "用量与额度", Description: "是否采集用户 Token。"},
	"cpa.usage_queue_retention_seconds":                  {Group: "用量与额度", Description: "中断时事件保留时间。", Unit: "秒"},
	"usage.quota_cache_seconds":                          {Group: "用量与额度", Description: "官方额度缓存时间。", Unit: "秒"},
	"usage.upstream_timeout_seconds":                     {Group: "用量与额度", Description: "额度读取超时时间。", Unit: "秒"},
	"account_failover.mode":                              {Group: "账号自动切换", Description: "额度耗尽后自动迁移用户。", ChoiceLabels: map[string]string{"off": "关闭", "active": "自动执行"}},
	"account_failover.poll_seconds":                      {Group: "账号自动切换", Description: "官方额度检查周期。", Unit: "秒"},
	"account_failover.reserve_percent":                   {Group: "账号自动切换", Description: "低额度账号不接收迁入。", Unit: "%"},
	"account_failover.stale_after_seconds":               {Group: "账号自动切换", Description: "额度刷新超时即停止迁移。", Unit: "秒"},
	"user_quota.default_weekly_tokens":                   {Group: "用户额度", Description: "每周 Token 上限，留空不限额。", Unit: "Token"},
	"user_quota.reset_personal_weekly_on_new_week":       {Group: "用户额度", Description: "个人额度下周恢复默认。"},
	"user_quota.timezone":                                {Group: "用户额度", Description: "周额度和今日用量时区。"},
	"user_quota.fail_open_after_seconds":                 {Group: "用户额度", Description: "采集异常超时后放行并告警。", Unit: "秒"},
	"user_quota.reasoning_multiplier.none":               {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.minimal":            {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.low":                {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.medium":             {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.high":               {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.xhigh":              {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.max":                {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.ultra":              {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.auto":               {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"user_quota.reasoning_multiplier.unknown":            {Group: "推理强度策略", Description: "新事件按原始 Token 加权。", Unit: "倍"},
	"admin.account_usage.reasoning_effort_color.none":    {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.minimal": {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.low":     {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.medium":  {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.high":    {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.xhigh":   {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.max":     {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.ultra":   {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.auto":    {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"admin.account_usage.reasoning_effort_color.unknown": {Group: "推理强度策略", Description: "账号模型 Token 明细使用的颜色。"},
	"notification.enabled":                               {Group: "企业微信通知", Description: "按设定时间发送额度报告并检查阈值。"},
	"notification.timezone":                              {Group: "企业微信通知", Description: "报告发送和额度刷新的 IANA 时区。"},
	"notification.daily_times":                           {Group: "企业微信通知", Description: "多个 HH:MM 用逗号分隔。"},
	"notification.schedule_grace_minutes":                {Group: "企业微信通知", Description: "服务恢复后允许补发报告的分钟数。", Unit: "分钟"},
	"notification.quota_alert_enabled":                   {Group: "企业微信通知", Description: "账号首次达到阈值时发送一次提醒。"},
	"notification.weekly_threshold_percent":              {Group: "企业微信通知", Description: "账号额度窗口的已使用百分比阈值。", Unit: "%"},
	"portal.session_ttl_seconds":                         {Group: "会话与采集", Description: "仅影响新创建的用户会话。", Unit: "秒"},
	"collector.interval_seconds":                         {Group: "会话与采集", Description: "采集器两次扫描之间的等待时间。", Unit: "秒"},
	"collector.batch_size":                               {Group: "会话与采集", Description: "单个 CPA 每次读取的最大事件数。"},
	"accounts.port_start":                                {Group: "账号供应", Description: "仅影响新建 CPA，现有端口不变。"},
	"accounts.port_end":                                  {Group: "账号供应", Description: "仅影响新建 CPA，必须不小于端口起点。"},
	"accounts.listen_address":                            {Group: "账号与发布", Description: "固定宿主机回环地址，仅本机和 Docker 内网可访问。"},
	"runtime.cliproxy_image":                             {Group: "账号与发布", Description: "业务 CPA 更新镜像，验证后固定为不可变版本。"},
}
