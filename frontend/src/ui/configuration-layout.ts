import type { ConfigurationField } from "../api/configuration";

// Presentation only: the catalog remains authoritative for values, validation and effects.
export const configurationCategories = [
  { name: "品牌与身份", eyebrow: "BRAND & IDENTITY", description: "站点品牌、组织身份与客户端导出" },
  { name: "系统设置", eyebrow: "SYSTEM SETTINGS", description: "时间、登录安全与显示偏好" },
  { name: "请求与账号", eyebrow: "REQUESTS & ACCOUNTS", description: "请求行为、账号切换与运行参数" },
  { name: "用量与额度", eyebrow: "USAGE & QUOTAS", description: "用户额度、计费倍率与用量采集" },
  { name: "通知设置", eyebrow: "NOTIFICATIONS", description: "企业微信通知、发送计划与额度预警" },
  { name: "数据与审计", eyebrow: "DATA & AUDIT", description: "安全归档、存储状态与管理操作记录" }
] as const;
export type ConfigurationCategory = typeof configurationCategories[number]["name"];

export const configurationSections: Array<{
  id: string; category: ConfigurationCategory; title: string; description: string; expanded?: boolean; independent?: boolean;
}> = [
  { id: "brand", category: "品牌与身份", title: "站点品牌", description: "页面名称、Logo 与公开地址", expanded: true },
  { id: "identity", category: "品牌与身份", title: "组织身份", description: "企业邮箱后缀与新 API Key 前缀", expanded: true },
  { id: "client", category: "品牌与身份", title: "客户端导出", description: "Provider、环境变量与默认模型" },
  { id: "general", category: "系统设置", title: "时间与登录", description: "统一业务时区与 Portal 登录有效期", expanded: true },
  { id: "access", category: "系统设置", title: "访问凭据", description: "管理密钥与用户初始密码", expanded: true, independent: true },
  { id: "appearance", category: "系统设置", title: "显示偏好", description: "账号明细中的推理强度配色" },
  { id: "requests", category: "请求与账号", title: "请求与代理", description: "默认上游代理、重试与图片工具", expanded: true },
  { id: "affinity", category: "请求与账号", title: "会话保持", description: "会话凭据复用与有效期" },
  { id: "failover", category: "请求与账号", title: "账号自动切换", description: "官方额度耗尽后的迁移策略", expanded: true },
  { id: "provisioning", category: "请求与账号", title: "账号供应与运行", description: "新账号端口、监听地址与更新镜像" },
  { id: "logging", category: "请求与账号", title: "日志与排障", description: "调试开关、文件日志与保留上限" },
  { id: "quota", category: "用量与额度", title: "用户额度", description: "系统默认周额度与异常放行策略", expanded: true },
  { id: "multipliers", category: "用量与额度", title: "模型与推理倍率", description: "模型倍率 × 推理强度倍率" },
  { id: "collection", category: "用量与额度", title: "用量采集", description: "采集开关、轮询、批次与事件保留" },
  { id: "quota-cache", category: "用量与额度", title: "官方额度查询", description: "缓存时长与接口超时" },
  { id: "quota-reset", category: "用量与额度", title: "用量维护", description: "异常补偿时清零全员本周已用量", independent: true },
  { id: "notifications", category: "通知设置", title: "企业微信通知", description: "通知开关与 Webhook 接入", expanded: true },
  { id: "schedule", category: "通知设置", title: "发送计划", description: "按系统时区发送定时报表", expanded: true },
  { id: "alerts", category: "通知设置", title: "额度预警", description: "周额度预警开关与阈值" },
  { id: "backups", category: "数据与审计", title: "安全归档", description: "归档数量与最近归档", expanded: true },
  { id: "storage", category: "数据与审计", title: "本地数据", description: "持久化路径与权限状态" },
  { id: "audit", category: "数据与审计", title: "审计记录", description: "最近的配置与维护操作" }
];

const legacySections: Record<string, string> = {
  "品牌与身份": "brand", "系统设置": "general", "CPA 请求": "requests", "账号自动切换": "failover",
  "用户额度": "quota", "推理强度策略": "multipliers", "用量与额度": "collection", "企业微信通知": "notifications",
  "会话与采集": "general", "账号供应": "provisioning", "账号与发布": "provisioning"
};

export function configurationSectionFor(field: Pick<ConfigurationField, "key">, originalGroup: string) {
  const key = field.key;
  const id = key.startsWith("branding.") ? "brand"
    : key.startsWith("identity.") ? "identity"
    : key === "portal.session_ttl_seconds" || key.startsWith("system.") ? "general"
    : key.startsWith("portal.") ? "client"
    : key.startsWith("admin.account_usage.reasoning_effort_color.") ? "appearance"
    : key.startsWith("user_quota.model_multiplier.") || key.startsWith("user_quota.reasoning_multiplier.") ? "multipliers"
    : key.startsWith("user_quota.") ? "quota"
    : key.startsWith("collector.") || key.startsWith("cpa.usage_") ? "collection"
    : key.startsWith("usage.") ? "quota-cache"
    : key.startsWith("account_failover.") ? "failover"
    : key.startsWith("accounts.") || key.startsWith("runtime.") ? "provisioning"
    : key.startsWith("cpa.session_affinity") ? "affinity"
    : ["cpa.debug", "cpa.logging_to_file", "cpa.logs_max_total_size_mb", "cpa.error_logs_max_files"].includes(key) ? "logging"
    : key.startsWith("cpa.") ? "requests"
    : ["notification.daily_times", "notification.schedule_grace_minutes"].includes(key) ? "schedule"
    : ["notification.quota_alert_enabled", "notification.weekly_threshold_percent"].includes(key) ? "alerts"
    : key.startsWith("notification.") ? "notifications"
    : legacySections[originalGroup] ?? "general";
  return configurationSections.find((section) => section.id === id)!;
}

export function legacyConfigurationSection(group: string) {
  return configurationSections.find((section) => section.id === legacySections[group]);
}
