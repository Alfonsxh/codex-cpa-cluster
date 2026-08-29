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
		Version: 1, GeneratedAt: time.Now().Unix(), FieldCount: len(configurationDefinitions), Groups: groups,
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
	"品牌与身份":  "统一管理公开名称、邮箱域名、新 Key 前缀和客户端导出参数。",
	"CPA 请求": "统一作用于所有继承系统默认设置的业务 CPA。",
	"用量与额度":  "管理官方额度读取、用量事件保留和采集策略。",
	"账号自动切换": "官方周额度不足后，按剩余资源迁移用户路由；仅支持关闭或自动执行。",
	"用户额度":   "设置系统默认周额度、个人策略恢复和网关故障策略。",
	"推理强度策略": "管理用户额度倍率和账号 Token 明细配色；两类配置独立生效。",
	"企业微信通知": "配置定时额度报告、发送时区和阈值预警。",
	"会话与采集":  "控制使用中心会话有效期与用量采集器吞吐。",
	"账号供应":   "只影响之后创建的业务 CPA，现有账号保持原端口。",
	"账号与发布":  "管理业务 CPA 的本机监听、镜像通道和应用版本提醒；正式控制面参数只来自 target.env。",
}

var configurationPresentationByKey = map[string]configurationPresentation{
	"branding.product_name":                              {Group: "品牌与身份", Description: "Portal、使用中心、管理中心和通知中显示的完整名称。"},
	"branding.short_name":                                {Group: "品牌与身份", Description: "客户端 Provider、紧凑导航和导出配置中使用的名称。"},
	"branding.environment_label":                         {Group: "品牌与身份", Description: "入口页显示的环境或访问范围说明；可以留空。"},
	"branding.public_base_url":                           {Group: "品牌与身份", Description: "通知及 Codex、Claude Code、CC Switch 导出使用的 HTTP(S) 根地址；协议按此值原样使用，留空时使用浏览器当前来源。"},
	"identity.allowed_email_domains":                     {Group: "品牌与身份", Description: "支持多个域名，使用逗号分隔；创建用户前必须至少配置一个。"},
	"identity.key_prefix":                                {Group: "品牌与身份", Description: "必须以下划线结尾；只影响之后创建或轮换的 Key，既有 Key 保持有效。"},
	"portal.provider_name":                               {Group: "品牌与身份", Description: "Codex、CC Switch 等客户端配置中显示的 Provider 名称。"},
	"portal.api_key_env":                                 {Group: "品牌与身份", Description: "使用中心生成的 Shell 配置所使用的环境变量名。"},
	"portal.default_model":                               {Group: "品牌与身份", Description: "使用中心生成的 Codex 和 Claude Code 配置默认模型。"},
	"cpa.proxy_enabled":                                  {Group: "CPA 请求", Description: "启用后，所有选择“继承默认”的 CPA 使用默认代理；账号自定义代理或强制直连优先。"},
	"cpa.proxy_url":                                      {Group: "CPA 请求", Description: "支持 HTTP、HTTPS、SOCKS5；加密保存。仅作用于选择“继承默认”的 CPA。"},
	"cpa.request_retry":                                  {Group: "CPA 请求", Description: "单次上游请求失败后的重试次数。"},
	"cpa.disable_image_generation":                       {Group: "CPA 请求", Description: "推荐仅在普通对话中禁用 CPA hosted 图片工具，避免与 Codex image_gen 命名空间冲突，同时保留专用图片生成接口。", ChoiceLabels: map[string]string{"chat": "仅普通对话禁用（推荐）", "true": "全部禁用", "false": "全部启用"}},
	"cpa.max_retry_credentials":                          {Group: "CPA 请求", Description: "一次请求最多切换尝试的 OAuth 凭据数量。"},
	"cpa.max_retry_interval":                             {Group: "CPA 请求", Description: "等待临时冷却凭据后再次重试的最长时间。", Unit: "秒"},
	"cpa.transient_error_cooldown_seconds":               {Group: "CPA 请求", Description: "上游 408/500/502/503/504 后暂停使用当前凭据的时间。", Unit: "秒"},
	"cpa.session_affinity":                               {Group: "CPA 请求", Description: "同一会话优先沿用原有上游凭据。"},
	"cpa.session_affinity_ttl":                           {Group: "CPA 请求", Description: "支持 30s、5m、1h、7d 等格式。"},
	"cpa.debug":                                          {Group: "CPA 请求", Description: "仅排障时启用，可能显著增加日志量。"},
	"cpa.logging_to_file":                                {Group: "CPA 请求", Description: "让业务 CPA 将运行日志写入各自日志目录，并由容量上限自动清理。"},
	"cpa.logs_max_total_size_mb":                         {Group: "CPA 请求", Description: "每个业务 CPA 日志目录的总容量上限，超出后由 CPA 删除最旧日志。", Unit: "MiB"},
	"cpa.error_logs_max_files":                           {Group: "CPA 请求", Description: "每个业务 CPA 最多保留的请求错误日志文件数量。", Unit: "个"},
	"cpa.usage_statistics_enabled":                       {Group: "用量与额度", Description: "关闭后用户 Token 用量采集将停止获得新事件。"},
	"cpa.usage_queue_retention_seconds":                  {Group: "用量与额度", Description: "采集器中断时，CPA 最长保留用量事件的秒数。", Unit: "秒"},
	"usage.quota_cache_seconds":                          {Group: "用量与额度", Description: "管理中心缓存各 CPA 官方额度的时间。", Unit: "秒"},
	"usage.upstream_timeout_seconds":                     {Group: "用量与额度", Description: "读取额度及执行 Full reset 时的上游等待上限。", Unit: "秒"},
	"account_failover.mode":                              {Group: "账号自动切换", Description: "关闭时不检查；自动模式在账号官方周额度耗尽后批量迁移路由。", ChoiceLabels: map[string]string{"off": "关闭", "active": "自动执行"}},
	"account_failover.poll_seconds":                      {Group: "账号自动切换", Description: "自动切换独立检查官方账号周额度的周期；不依赖企业微信通知开关。", Unit: "秒"},
	"account_failover.reserve_percent":                   {Group: "账号自动切换", Description: "剩余额度不高于该比例的账号不接收自动迁入用户。", Unit: "%"},
	"account_failover.stale_after_seconds":               {Group: "账号自动切换", Description: "超过该时间未成功刷新官方额度时停止自动迁移，保留现有路由。", Unit: "秒"},
	"user_quota.default_weekly_tokens":                   {Group: "用户额度", Description: "按用户邮箱汇总全部 CPA 的自然周加权 Token；留空表示默认不限额，用户单独策略优先。", Unit: "Token"},
	"user_quota.reset_personal_weekly_on_new_week":       {Group: "用户额度", Description: "开启后，所有用户的单独不限额或自定义额度只在当前自然周生效，下周开始时自动恢复继承系统默认值。"},
	"user_quota.timezone":                                {Group: "用户额度", Description: "用户周额度和今日用量的日期边界；修改后会按新时区重建周聚合。"},
	"user_quota.fail_open_after_seconds":                 {Group: "用户额度", Description: "采集异常时继续使用最后有效快照；超过该时长后放行新请求并记录告警。", Unit: "秒"},
	"user_quota.reasoning_multiplier.none":               {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.minimal":            {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.low":                {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.medium":             {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.high":               {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.xhigh":              {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.max":                {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.ultra":              {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.auto":               {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"user_quota.reasoning_multiplier.unknown":            {Group: "推理强度策略", Description: "按请求原始总 Token 计算加权额度；保存后仅作用于新采集事件，历史事件不追溯重算。", Unit: "倍"},
	"admin.account_usage.reasoning_effort_color.none":    {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.minimal": {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.low":     {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.medium":  {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.high":    {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.xhigh":   {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.max":     {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.ultra":   {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.auto":    {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"admin.account_usage.reasoning_effort_color.unknown": {Group: "推理强度策略", Description: "账号管理模型 Token 明细中使用；同一推理强度在所有模型中保持一致。"},
	"notification.enabled":                               {Group: "企业微信通知", Description: "启用后按设定时间发送账号额度表格，并执行周额度阈值检查。"},
	"notification.timezone":                              {Group: "企业微信通知", Description: "定时发送和额度刷新时间使用的 IANA 时区。"},
	"notification.daily_times":                           {Group: "企业微信通知", Description: "多个 HH:MM 使用逗号分隔，保存时自动去重排序。"},
	"notification.schedule_grace_minutes":                {Group: "企业微信通知", Description: "服务在发送时刻后恢复时，允许补发报告的分钟数。", Unit: "分钟"},
	"notification.quota_alert_enabled":                   {Group: "企业微信通知", Description: "每个账号额度窗口首次达到阈值时发送一次；恢复到阈值以下后重新布防。"},
	"notification.weekly_threshold_percent":              {Group: "企业微信通知", Description: "各账号、各周额度窗口独立判断的已使用百分比。", Unit: "%"},
	"portal.session_ttl_seconds":                         {Group: "会话与采集", Description: "只影响保存后新创建的用户登录会话。", Unit: "秒"},
	"collector.interval_seconds":                         {Group: "会话与采集", Description: "用量采集器两轮扫描之间的等待时间。", Unit: "秒"},
	"collector.batch_size":                               {Group: "会话与采集", Description: "每次从单个 CPA 用量队列读取的最大事件数。"},
	"accounts.port_start":                                {Group: "账号供应", Description: "只影响后续新建 CPA，现有账号端口保持不变。"},
	"accounts.port_end":                                  {Group: "账号供应", Description: "只影响后续新建 CPA，必须不小于端口起点。"},
	"accounts.listen_address":                            {Group: "账号与发布", Description: "固定为宿主机回环地址；业务 CPA 只能由本机发布检查或 Docker 内网访问。"},
	"runtime.cliproxy_image":                             {Group: "账号与发布", Description: "作为业务 CPA 更新通道；账号管理拉取后识别真实版本，验证通过才固定为不可变镜像。"},
	"delivery.release_metadata_image":                    {Group: "账号与发布", Description: "Admin 只读检查项目新版本所使用的 metadata 镜像；可留空关闭提醒。"},
}
