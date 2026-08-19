"use strict";

(() => {
  const uniqueViews = (views) => [...new Set(views)];

  const mutationAffectedViews = (path, method = "GET") => {
    const verb = String(method || "GET").trim().toUpperCase();
    if (["GET", "HEAD"].includes(verb)) return [];
    const pathname = String(path || "").split("?", 1)[0];
    if (pathname.startsWith("/accounts")) {
      return ["overview", "accounts", "users", "operations"];
    }
    if (pathname.startsWith("/users") || pathname.startsWith("/keys")) {
      return ["overview", "accounts", "users", "organization", "settings"];
    }
    if (pathname.startsWith("/teams") || pathname.startsWith("/tags")) {
      return ["users", "organization"];
    }
    if (pathname === "/operations" || pathname === "/jobs/cancel") {
      return ["overview", "accounts", "operations"];
    }
    if (pathname.startsWith("/settings")) {
      return ["overview", "accounts", "users", "organization", "operations", "settings"];
    }
    if (pathname.startsWith("/notifications")) return ["settings"];
    return [];
  };

  const catalogEntries = (catalog, kind) => {
    const key = kind === "account" ? "accounts" : kind === "user" ? "users" : "";
    return key && Array.isArray(catalog?.[key]) ? catalog[key] : [];
  };

  const catalogOptions = (catalog, kind) => {
    const valueKey = kind === "account" ? "id" : "email";
    return catalogEntries(catalog, kind)
      .map((item) => String(item?.[valueKey] || "").trim())
      .filter(Boolean)
      .map((value) => ({ value, label: value }));
  };

  const monitorSeriesStatus = (catalog, name, kind) => {
    const normalizedName = String(name || "").trim();
    if (kind === "account") {
      const account = catalogEntries(catalog, kind)
        .find((item) => String(item?.id || "") === normalizedName);
      const operational = account?.operational_status;
      if (operational && typeof operational === "object") {
        return {
          label: String(operational.label || "状态未知"),
          tone: String(operational.tone || "neutral")
        };
      }
      return { label: "状态未知", tone: "neutral" };
    }
    const user = catalogEntries(catalog, kind)
      .find((item) => String(item?.email || "") === normalizedName);
    if (user?.status === "active") return { label: "活跃", tone: "success" };
    if (user?.status === "inactive") return { label: "停用", tone: "neutral" };
    return { label: "状态未知", tone: "neutral" };
  };

  const allUserQuotaImpact = (summary) => {
    const rawTotal = summary?.total_users;
    const total = Number(rawTotal);
    const hasTotal = rawTotal !== null
      && rawTotal !== undefined
      && rawTotal !== ""
      && Number.isInteger(total)
      && total >= 0;
    return {
      available: hasTotal,
      totalUsers: hasTotal ? total : 0,
      usersWithUsage: Math.max(0, Number(summary?.users_with_usage) || 0),
      totalUsedTokens: Math.max(0, Number(summary?.total_used_tokens) || 0),
      totalRawUsedTokens: Math.max(0, Number(summary?.total_raw_used_tokens) || 0)
    };
  };

  const stopOperationMessage = (target, impact, impactAvailable = true) => {
    const normalizedTarget = String(target || "").trim();
    if (normalizedTarget === "all") {
      return "将停止全部业务 CPA 和插件资源服务；网关与本管理界面会保留，方便恢复。";
    }
    if (!impactAvailable) return `将停止 ${normalizedTarget}；影响范围暂不可确认。`;
    if (impact?.target_type !== "account") return `将停止 ${normalizedTarget}。`;
    const rawRoutedUsers = impact.routed_users;
    const routedUsers = Number(rawRoutedUsers);
    if (
      rawRoutedUsers === null
      || rawRoutedUsers === undefined
      || rawRoutedUsers === ""
      || !Number.isInteger(routedUsers)
      || routedUsers < 0
    ) {
      return `将停止 ${normalizedTarget}；影响范围暂不可确认。`;
    }
    return routedUsers
      ? `将停止 ${normalizedTarget}，当前有 ${routedUsers} 个用户路由到该账号。`
      : `将停止 ${normalizedTarget}，当前没有用户路由到该账号。`;
  };

  const viewStateUtils = {
    allUserQuotaImpact,
    catalogOptions,
    monitorSeriesStatus,
    mutationAffectedViews,
    stopOperationMessage,
    uniqueViews
  };
  if (typeof module !== "undefined" && module.exports) module.exports = viewStateUtils;
  globalThis.AdminViewStateUtils = viewStateUtils;
})();
