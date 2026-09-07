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

// Only the control width varies; the shared configuration grid owns its position.
export function configurationControlWidth(field: ConfigurationField): number | "100%" | "fit-content" {
  if (field.unit === "Token") return "100%";
  if (["integer", "number", "nullable_integer", "time_list", "duration", "boolean"].includes(field.type)) return "fit-content";
  if (field.type === "color") return 144;
  if (field.type === "timezone") return 360;
  if (field.type === "choice") {
    const longestLabel = Math.max(0, ...(field.choices ?? []).map((choice) =>
      [...`${choice.label} · ${choice.value}`].reduce((width, character) => width + (character.charCodeAt(0) <= 127 ? 7.5 : 12), 0)
    ));
    return Math.min(360, Math.max(180, Math.ceil((longestLabel + 56) / 40) * 40));
  }
  return "100%";
}

export const configurationSections: Array<{
  id: string; category: ConfigurationCategory; title: string; description: string;
}> = [
  { id: "brand", category: "品牌与身份", title: "站点品牌", description: "页面名称、Logo 与公开地址" },
  { id: "identity", category: "品牌与身份", title: "组织身份", description: "企业邮箱后缀与新 API Key 前缀" },
  { id: "client", category: "品牌与身份", title: "客户端导出", description: "Provider、环境变量与默认模型" },
  { id: "general", category: "系统设置", title: "时间与登录", description: "统一业务时区与 Portal 登录有效期" },
  { id: "access", category: "系统设置", title: "访问凭据", description: "管理密钥与用户初始密码" },
  { id: "appearance", category: "系统设置", title: "显示偏好", description: "账号明细中的推理强度配色" },
  { id: "requests", category: "请求与账号", title: "请求与代理", description: "默认上游代理、重试与图片工具" },
  { id: "affinity", category: "请求与账号", title: "会话保持", description: "会话凭据复用与有效期" },
  { id: "failover", category: "请求与账号", title: "账号自动切换", description: "官方额度耗尽后的迁移策略" },
  { id: "provisioning", category: "请求与账号", title: "账号供应与运行", description: "新账号端口、监听地址与更新镜像" },
  { id: "logging", category: "请求与账号", title: "日志与排障", description: "调试开关、文件日志与保留上限" },
  { id: "quota", category: "用量与额度", title: "额度", description: "用户额度、官方额度查询与用量维护" },
  { id: "model-multipliers", category: "用量与额度", title: "模型倍率", description: "各模型与未匹配模型的用户额度倍率" },
  { id: "reasoning-multipliers", category: "用量与额度", title: "推理倍率", description: "各推理强度的用户额度倍率" },
  { id: "collection", category: "用量与额度", title: "用量采集", description: "采集开关、轮询、批次与事件保留" },
  { id: "notifications", category: "通知设置", title: "企业微信通知", description: "通知开关、Webhook 接入、发送计划与额度预警" },
  { id: "backups", category: "数据与审计", title: "安全归档", description: "归档数量与最近归档" },
  { id: "storage", category: "数据与审计", title: "本地数据", description: "持久化路径与权限状态" },
  { id: "audit", category: "数据与审计", title: "审计记录", description: "最近的配置与维护操作" }
];

const legacySections: Record<string, string> = {
  "品牌与身份": "brand", "系统设置": "general", "CPA 请求": "requests", "账号自动切换": "failover",
  "用户额度": "quota", "推理强度策略": "reasoning-multipliers", "用量与额度": "collection", "企业微信通知": "notifications",
  "multipliers": "model-multipliers", "模型与推理倍率": "model-multipliers",
  "quota-cache": "quota", "官方额度查询": "quota",
  "quota-reset": "quota", "用量维护": "quota",
  "schedule": "notifications", "发送计划": "notifications", "alerts": "notifications", "额度预警": "notifications",
  "会话与采集": "general", "账号供应": "provisioning", "账号与发布": "provisioning"
};

export function configurationSectionFor(field: Pick<ConfigurationField, "key">, originalGroup: string) {
  const key = field.key;
  const id = key.startsWith("branding.") ? "brand"
    : key.startsWith("identity.") ? "identity"
    : key === "portal.session_ttl_seconds" || key.startsWith("system.") ? "general"
    : key.startsWith("portal.") ? "client"
    : key.startsWith("admin.account_usage.reasoning_effort_color.") ? "appearance"
    : key.startsWith("user_quota.model_multiplier.") ? "model-multipliers"
    : key.startsWith("user_quota.reasoning_multiplier.") ? "reasoning-multipliers"
    : key.startsWith("user_quota.") ? "quota"
    : key.startsWith("collector.") || key.startsWith("cpa.usage_") ? "collection"
    : key.startsWith("usage.") ? "quota"
    : key.startsWith("account_failover.") ? "failover"
    : key.startsWith("accounts.") || key.startsWith("runtime.") ? "provisioning"
    : key.startsWith("cpa.session_affinity") ? "affinity"
    : ["cpa.debug", "cpa.logging_to_file", "cpa.logs_max_total_size_mb", "cpa.error_logs_max_files"].includes(key) ? "logging"
    : key.startsWith("cpa.") ? "requests"
    : key.startsWith("notification.") ? "notifications"
    : legacySections[originalGroup] ?? "general";
  return configurationSections.find((section) => section.id === id)!;
}

export function legacyConfigurationSection(group: string) {
  return configurationSections.find((section) => section.id === legacySections[group]);
}
