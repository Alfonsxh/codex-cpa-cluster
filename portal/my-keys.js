(() => {
  "use strict";

  const DEFAULT_REASONING_EFFORT = "xhigh";
  const DEFAULT_PLAN_REASONING_EFFORT = "xhigh";
  const browserOrigin = window.location.origin;
  const $ = (selector) => document.querySelector(selector);
  const state = {
    payload: null,
    windowSeconds: "today",
    sort: { field: "quota", direction: "asc", pinCurrent: true },
    expanded: new Set(),
    usageBreakdowns: new Map(),
    usageBreakdownLoading: new Set(),
    usageBreakdownErrors: new Map(),
    usageBreakdownControllers: new Map(),
    pendingGroup: "",
    activeConfig: "",
    controller: null,
    passwordChangeRequired: false,
    routeCheckInFlight: false,
    quotaRefreshTimer: null,
    siteConfig: {
      product_name: "Codex CPA Cluster",
      public_base_url: "",
      provider_name: "Codex CPA",
      api_key_env: "CPA_API_KEY",
      default_model: "gpt-5.6-sol"
    }
  };
  let loginRetryTimer = null;
  let loginRetryUntil = 0;

  const THEME_STORAGE_KEY = "cpa-ui-theme";
  const LEGACY_THEME_STORAGE_KEY = "cpa-admin-theme";
  const preferredTheme = () => {
    try {
      const stored = window.localStorage.getItem(THEME_STORAGE_KEY)
        || window.localStorage.getItem(LEGACY_THEME_STORAGE_KEY);
      if (stored === "light" || stored === "dark") return stored;
    } catch { /* storage may be unavailable */ }
    return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  };

  const applyTheme = (theme, persist = false) => {
    const resolved = theme === "dark" ? "dark" : "light";
    document.documentElement.dataset.theme = resolved;
    document.documentElement.style.colorScheme = resolved;
    const favicon = $("#usage-favicon");
    if (favicon) {
      favicon.href = `/portal/assets/codex-cpa-cluster-favicon${resolved === "dark" ? "-dark" : ""}.svg`;
    }
    const toggle = $("#usage-theme-toggle");
    if (toggle) {
      const nextTheme = resolved === "dark" ? "light" : "dark";
      toggle.setAttribute("aria-label", `切换为${nextTheme === "dark" ? "深色" : "浅色"}主题`);
      toggle.querySelector(".usage-theme-toggle-icon").textContent = resolved === "dark" ? "☀" : "☾";
      toggle.querySelector(".usage-theme-toggle-label").textContent = resolved === "dark" ? "浅色" : "深色";
    }
    if (persist) {
      try { window.localStorage.setItem(THEME_STORAGE_KEY, resolved); } catch { /* storage may be unavailable */ }
    }
    document.dispatchEvent(new CustomEvent("cpa-theme-change", { detail: { theme: resolved } }));
  };

  const escapeHTML = (value) => String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");

  const passwordVisibilityIcons = `
    <svg class="password-eye-show" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z"/><circle cx="12" cy="12" r="2.75"/>
    </svg>
    <svg class="password-eye-hide" viewBox="0 0 24 24" fill="none" aria-hidden="true">
      <path d="M3 3l18 18M10.6 6.2A10.7 10.7 0 0 1 12 6c6 0 9.5 6 9.5 6a17.6 17.6 0 0 1-2.5 3.2M6.2 6.2C3.8 8 2.5 12 2.5 12s3.5 6 9.5 6a9.9 9.9 0 0 0 3.2-.5M9.9 9.9a3 3 0 0 0 4.2 4.2"/>
    </svg>`;

  const setPasswordVisibility = (input, button, visible) => {
    input.type = visible ? "text" : "password";
    button.setAttribute("aria-pressed", String(visible));
    button.setAttribute("aria-label", visible ? "隐藏密码" : "显示密码");
    button.title = visible ? "隐藏密码" : "显示密码";
  };

  const enhancePasswordFields = (root = document) => {
    root.querySelectorAll('input[type="password"]:not([data-password-input])').forEach((input) => {
      const fieldLabel = input.closest("label")?.querySelector(":scope > span")?.textContent.trim();
      if (fieldLabel && !input.hasAttribute("aria-label")) input.setAttribute("aria-label", fieldLabel);
      const wrapper = document.createElement("span");
      wrapper.className = "password-input";
      input.before(wrapper);
      wrapper.append(input);
      input.dataset.passwordInput = "true";

      const button = document.createElement("button");
      button.className = "password-visibility-toggle";
      button.type = "button";
      if (input.id) button.setAttribute("aria-controls", input.id);
      button.innerHTML = passwordVisibilityIcons;
      button.addEventListener("click", () => {
        setPasswordVisibility(input, button, input.type === "password");
      });
      wrapper.append(button);
      setPasswordVisibility(input, button, false);
    });
  };

  const concealPasswordFields = (root = document) => {
    root.querySelectorAll("input[data-password-input]").forEach((input) => {
      const button = input.parentElement?.querySelector(".password-visibility-toggle");
      if (button) setPasswordVisibility(input, button, false);
    });
  };

  enhancePasswordFields();
  document.querySelectorAll("dialog").forEach((dialog) => {
    dialog.addEventListener("close", () => concealPasswordFields(dialog));
  });

  const tomlString = (value) => JSON.stringify(String(value ?? ""));
  const number = new Intl.NumberFormat("zh-CN");
  const compactNumber = new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 });
  const tableCollator = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });

  const compareTableValues = (left, right, direction) => {
    const leftMissing = left === null || left === undefined || left === "";
    const rightMissing = right === null || right === undefined || right === "";
    if (leftMissing !== rightMissing) return leftMissing ? 1 : -1;
    if (leftMissing) return 0;
    const comparison = typeof left === "string" || typeof right === "string"
      ? tableCollator.compare(String(left), String(right))
      : Number(left) - Number(right);
    return direction === "asc" ? comparison : -comparison;
  };

  const formatNumber = (value) => number.format(Number(value) || 0);
  const formatCompact = (value) => compactNumber.format(Number(value) || 0);
  const renderTokenUsage = (value) => TokenUsageFormatter.render(value);
  const formatTokenAmount = (value) => {
    const token = TokenUsageFormatter.format(value);
    return `${token.amount} ${token.unit}`;
  };
  const formatUsagePercent = (count, total) => {
    const denominator = Number(total) || 0;
    if (denominator <= 0) return "0%";
    return `${new Intl.NumberFormat("zh-CN", {
      maximumFractionDigits: 1
    }).format((Number(count) || 0) * 100 / denominator)}%`;
  };
  const formatTime = (value) => {
    if (!value) return "—";
    return new Intl.DateTimeFormat("zh-CN", {
      month: "2-digit",
      day: "2-digit",
      hour: "2-digit",
      minute: "2-digit",
      hour12: false
    }).format(new Date(Number(value) * 1000));
  };
  const resetUsageBreakdowns = () => {
    state.usageBreakdownControllers.forEach((controller) => controller.abort());
    state.usageBreakdownControllers.clear();
    state.usageBreakdowns.clear();
    state.usageBreakdownLoading.clear();
    state.usageBreakdownErrors.clear();
  };
  const usageWindowLabel = () => ({
    3600: "1 小时",
    today: "今日",
    86400: "24 小时",
    604800: "7 天"
  }[state.windowSeconds] || "当前周期");

  const showToast = (message) => {
    const toast = $("#usage-toast");
    toast.textContent = message;
    toast.hidden = false;
    window.clearTimeout(showToast.timer);
    showToast.timer = window.setTimeout(() => { toast.hidden = true; }, 2400);
  };

  const copyFallback = (value) => {
    const area = document.createElement("textarea");
    area.value = value;
    area.setAttribute("readonly", "");
    area.style.position = "fixed";
    area.style.left = "-9999px";
    document.body.append(area);
    area.select();
    let copied = false;
    try { copied = document.execCommand("copy"); } catch { copied = false; }
    area.remove();
    return copied;
  };

  const copyText = async (value) => {
    if (!value) return;
    if (copyFallback(value)) {
      showToast("已复制");
      return;
    }
    try {
      await navigator.clipboard.writeText(value);
      showToast("已复制");
    } catch {
      showToast("复制失败，请手动选择");
    }
  };

  const providerName = () => {
    const username = state.payload?.user?.split("@", 1)[0] || "user";
    return `${state.siteConfig.provider_name} · ${username}`;
  };

  const modelName = () => state.siteConfig.default_model || "gpt-5.6-sol";
  const apiKeyEnv = () => state.siteConfig.api_key_env || "CPA_API_KEY";
  const publicBaseUrl = () => {
    const configured = String(state.siteConfig.public_base_url || "").trim().replace(/\/+$/, "");
    try {
      const url = new URL(configured || browserOrigin);
      if (!["http:", "https:"].includes(url.protocol) || url.username || url.password) throw new Error("invalid URL");
      return url.href.replace(/\/+$/, "");
    } catch {
      return browserOrigin;
    }
  };
  const gatewayOrigin = () => publicBaseUrl();
  const gatewayBaseUrl = () => `${gatewayOrigin()}/v1`;

  const buildCodexConfig = (baseUrl = gatewayBaseUrl()) => [
    'model_provider = "custom"',
    `model = ${tomlString(modelName())}`,
    `model_reasoning_effort = ${tomlString(DEFAULT_REASONING_EFFORT)}`,
    `plan_mode_reasoning_effort = ${tomlString(DEFAULT_PLAN_REASONING_EFFORT)}`,
    "",
    "[model_providers.custom]",
    `name = ${tomlString(providerName())}`,
    `base_url = ${tomlString(baseUrl)}`,
    'wire_api = "responses"',
    'requires_openai_auth = false',
    'http_headers = { "X-OpenAI-Actor-Authorization" = "local-proxy" }',
    `experimental_bearer_token = ${tomlString(state.payload?.api_key || "")}`,
    ""
  ].join("\n");

  const shellQuote = (value) => `'${String(value ?? "").replaceAll("'", `'"'"'`)}'`;

  const buildClaudeCodeEnv = () => `${apiKeyEnv()}=${shellQuote(state.payload?.api_key || "")}\n`;

  const buildClaudeCodeLauncher = () => {
    const keyVariable = apiKeyEnv();
    const model = modelName();
    return [
    "claude_cpa() (",
    '  local env_file="${HOME}/.config/claude-cpa/env"',
    '  if [[ ! -r "$env_file" ]]; then',
    '    print -u2 "claude_cpa: missing $env_file"',
    "    return 1",
    "  fi",
    '  source "$env_file"',
    `  if [[ -z "\${${keyVariable}:-}" ]]; then`,
    `    print -u2 "claude_cpa: ${keyVariable} is empty"`,
    "    return 1",
    "  fi",
    "",
    "  unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY",
    "  unset all_proxy ALL_PROXY",
    "  unset ANTHROPIC_API_KEY",
    "  unset CLAUDE_CODE_OAUTH_TOKEN",
    "  unset ANTHROPIC_FOUNDRY_API_KEY",
    "  unset ANTHROPIC_FOUNDRY_BASE_URL",
    "  unset ANTHROPIC_FOUNDRY_RESOURCE",
    "  unset CLAUDE_CODE_USE_FOUNDRY",
    "  unset CLAUDE_CODE_USE_BEDROCK",
    "  unset CLAUDE_CODE_USE_VERTEX",
    "",
    `  export ANTHROPIC_AUTH_TOKEN="$${keyVariable}"`,
    `  unset ${keyVariable}`,
    `  export ANTHROPIC_BASE_URL=${shellQuote(gatewayOrigin())}`,
    `  export ANTHROPIC_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_SMALL_FAST_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_OPUS_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_SONNET_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_HAIKU_MODEL=${shellQuote(model)}`,
    `  export CLAUDE_CODE_SUBAGENT_MODEL=${shellQuote(model)}`,
    '  export CLAUDE_CODE_EFFORT_LEVEL="xhigh"',
    "  export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
    "  export CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1",
    "  export CLAUDE_CODE_DISABLE_1M_CONTEXT=1",
    "  export ENABLE_CLAUDEAI_MCP_SERVERS=0",
    "  export API_TIMEOUT_MS=600000",
    "  export CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR=1",
    "",
    "  command claude \\",
    "    --dangerously-skip-permissions \\",
    "    --verbose \\",
    "    --effort xhigh \\",
    '    "$@"',
    ")",
    ""
    ].join("\n");
  };

  const buildClaudeCodeSections = () => [
    {
      title: "准备配置目录",
      file: "终端",
      description: "先确认 Claude Code 已安装，再创建仅当前用户可访问的配置目录。",
      value: [
        "claude --version",
        'mkdir -p "$HOME/.config/claude-cpa"',
        'chmod 700 "$HOME/.config/claude-cpa"'
      ].join("\n"),
      copyLabel: "复制命令"
    },
    {
      title: "保存当前 API Key",
      file: "~/.config/claude-cpa/env",
      description: "新建此文件并粘贴下方内容。该文件包含完整 Key，不要提交到 Git。",
      value: buildClaudeCodeEnv(),
      hint: '保存后执行：chmod 600 "$HOME/.config/claude-cpa/env"',
      copyLabel: "复制文件内容"
    },
    {
      title: "创建 claude_cpa 启动脚本",
      file: "~/.config/claude-cpa/claude-cpa.zsh",
      description: `此函数仅影响 claude_cpa：通过当前网关使用 ${modelName()}，推理强度固定为 xhigh。`,
      value: buildClaudeCodeLauncher(),
      hint: '保存后执行：chmod 600 "$HOME/.config/claude-cpa/claude-cpa.zsh"',
      copyLabel: "复制启动脚本"
    },
    {
      title: "加载终端命令",
      file: "~/.zshrc",
      description: "将这一行追加到文件末尾，让每个新终端都能使用 claude_cpa。",
      value: 'source "$HOME/.config/claude-cpa/claude-cpa.zsh"\n',
      copyLabel: "复制加载配置"
    },
    {
      title: "加载并验证",
      file: "终端",
      description: "重新加载配置，检查函数存在，再发送一个最小请求验证网关。",
      value: [
        'source "$HOME/.zshrc"',
        "type claude_cpa",
        "claude_cpa -p 'Reply only: OK' --output-format text",
        "claude_cpa"
      ].join("\n"),
      copyLabel: "复制验证命令"
    }
  ];

  const buildClaudeCodeConfig = () => buildClaudeCodeLauncher();

  const buildCcSwitchLink = () => {
    const params = new URLSearchParams({
      resource: "provider",
      app: "codex",
      name: providerName(),
      endpoint: gatewayBaseUrl(),
      apiKey: state.payload?.api_key || "",
      homepage: `${gatewayOrigin()}/usage/`,
      model: modelName(),
      notes: `${state.siteConfig.product_name} · ${state.payload?.current_group || ""} · 导入后按使用中心提示粘贴完整 config.toml 以启用图片生成`
    });
    return `ccswitch://v1/import?${params.toString()}`;
  };

  const buildCodexHistoryAgentPrompt = () => "由于登录方式已从 OAuth 变为 API Key，请将 Codex 之前的会话迁移到当前 API Key 的会话历史中。";

  const weeklyPresentation = (weekly) => {
    const used = weekly.limit_reached
      ? 100
      : Math.max(0, Math.min(100, Number(weekly.used_percent) || 0));
    const reportedRemaining = Number(weekly.remaining_percent);
    const remaining = weekly.limit_reached
      ? 0
      : Number.isFinite(reportedRemaining)
        ? Math.max(0, Math.min(100, reportedRemaining))
        : Math.max(0, 100 - used);
    const level = used >= 100 ? "exhausted" : used >= 80 ? "warning" : "";
    return {
      used,
      remaining,
      usedCopy: used.toFixed(used % 1 ? 1 : 0),
      remainingCopy: remaining.toFixed(remaining % 1 ? 1 : 0),
      level
    };
  };

  const weeklyCell = (weekly) => {
    if (!weekly) return '<span class="usage-muted">—</span>';
    const { used, usedCopy, remainingCopy, level } = weeklyPresentation(weekly);
    return `<div class="usage-quota ${level}">
      <div><strong>${escapeHTML(usedCopy)}%</strong><span>剩余 ${escapeHTML(remainingCopy)}%</span></div>
      <progress class="usage-quota-track" max="100" value="${used}" aria-label="已使用 ${used}%"></progress>
      <small>${escapeHTML(formatTime(weekly.reset_at))} 重置</small>
    </div>`;
  };

  const statusPresentation = (group) => {
    const operational = group?.operational_status;
    if (operational && typeof operational === "object") {
      const className = {
        success: "available",
        warning: "warning",
        neutral: "degraded",
        danger: "unavailable"
      }[operational.tone] || "degraded";
      return {
        code: String(operational.code || "unknown"),
        label: String(operational.label || "状态未知"),
        className,
        reason: String(operational.reason || "账号状态暂不可确认"),
        selectable: operational.selectable !== false
      };
    }
    const legacy = {
      available: { label: "可用", className: "available", selectable: true },
      warning: { label: "注意额度", className: "warning", selectable: true },
      degraded: { label: "额度未知", className: "degraded", selectable: true },
      unavailable: { label: "不可用", className: "unavailable", selectable: false }
    }[group?.status];
    return {
      code: group?.status || "unknown",
      label: legacy?.label || group?.status || "状态未知",
      className: legacy?.className || "degraded",
      reason: "账号状态暂不可确认",
      selectable: legacy?.selectable ?? true
    };
  };

  const statusCell = (group) => {
    const status = statusPresentation(group);
    return `<span class="usage-status ${status.className}" title="${escapeHTML(status.reason)}">${escapeHTML(status.label)}</span>`;
  };

  const userTokenPair = (usage = {}) => {
    const rawTokens = Number(usage.total_tokens) || 0;
    const weightedTokens = Number(usage.weighted_tokens ?? rawTokens) || 0;
    return `<div class="usage-user-token-pair">
      <div><small>加权</small>${renderTokenUsage(weightedTokens)}</div>
      <div><small>未加权</small>${renderTokenUsage(rawTokens)}</div>
    </div>`;
  };

  const renderUserQuota = (quota = {}) => {
    const container = $("#user-quota");
    const source = ({
      default: "组织默认",
      user_unlimited: "单独不限额",
      user_custom: "用户自定义"
    })[quota.source] || "状态未知";
    $("#user-quota-source").textContent = source;
    if (!quota.period) {
      $("#user-quota-copy").textContent = "周额度暂不可用";
      $("#user-quota-value").textContent = "额度暂不可用";
      $("#user-quota-raw").textContent = "未加权用量暂不可用";
      $("#user-quota-remaining").textContent = "请稍后刷新";
      $("#user-quota-reset").textContent = "—";
      $("#user-quota-track").value = 0;
      $("#user-quota-track").setAttribute("aria-label", "个人周额度暂不可用");
      container.className = "usage-current-quota degraded";
      return;
    }
    const used = Number(quota.used_tokens) || 0;
    const weightedUsed = Number(quota.weighted_used_tokens ?? used) || 0;
    const rawUsed = Number(quota.raw_used_tokens) || 0;
    const limit = Number(quota.limit_tokens) || 0;
    const percent = quota.unlimited ? 0 : Math.max(0, Math.min(100, Number(quota.used_percent) || 0));
    const percentCopy = percent.toFixed(percent % 1 ? 1 : 0);
    const remainingPercent = quota.unlimited ? null : Math.max(0, 100 - percent);
    const remainingCopy = remainingPercent == null
      ? ""
      : remainingPercent.toFixed(remainingPercent % 1 ? 1 : 0);
    const value = $("#user-quota-value");
    $("#user-quota-copy").textContent = quota.unlimited ? "周额度不限额" : `周额度 ${percentCopy}%`;
    value.textContent = quota.unlimited
      ? `加权已用 ${formatTokenAmount(weightedUsed)}`
      : `加权已用 ${formatTokenAmount(weightedUsed)} / ${formatTokenAmount(limit)}`;
    value.title = quota.unlimited
      ? `加权已用 ${formatNumber(weightedUsed)} Token，不限额`
      : `加权已用 ${formatNumber(weightedUsed)} / ${formatNumber(limit)} Token`;
    $("#user-quota-raw").textContent = `未加权累计 ${formatTokenAmount(rawUsed)}`;
    $("#user-quota-raw").title = `未加权累计 ${formatNumber(rawUsed)} Token`;
    $("#user-quota-remaining").textContent = quota.unlimited
      ? "剩余不限额"
      : `剩余 ${remainingCopy}%`;
    $("#user-quota-reset").textContent = `${formatTime(quota.week_end_at)} 重置`;
    $("#user-quota-track").value = percent;
    $("#user-quota-track").setAttribute("aria-label", quota.unlimited
      ? `个人周额度不限额，已使用 ${formatNumber(used)} Token`
      : `个人周额度已使用 ${percentCopy}%`);
    container.className = `usage-current-quota ${quota.limit_reached ? "exhausted" : percent >= 90 ? "warning" : ""}`.trim();
  };

  const renderCurrentAccount = (payload) => {
    const groups = payload.groups || [];
    const current = groups.find((group) => group.current)
      || groups.find((group) => group.account === payload.current_group)
      || null;
    const status = $("#current-account-status");
    const quota = $("#current-account-quota");
    const quotaTrack = $("#current-account-quota-track");

    if (!current) {
      $("#current-account-name").textContent = "尚未选择";
      status.textContent = "待选择";
      status.removeAttribute("title");
      status.className = "usage-status degraded";
      $("#current-account-quota-copy").textContent = "选择可用账号后显示";
      $("#current-account-quota-remaining").textContent = "—";
      quota.className = "usage-current-quota";
      quotaTrack.value = 0;
      quotaTrack.setAttribute("aria-label", "尚未选择当前账号");
      return;
    }

    $("#current-account-name").textContent = current.account;
    const presentation = statusPresentation(current);
    status.textContent = presentation.label;
    status.title = presentation.reason;
    status.className = `usage-status ${presentation.className}`;
    const weekly = current.weekly;
    if (!weekly) {
      $("#current-account-quota-copy").textContent = "周额度未知";
      $("#current-account-quota-remaining").textContent = "—";
      quota.className = "usage-current-quota";
      quotaTrack.value = 0;
      quotaTrack.setAttribute("aria-label", "当前账号周额度未知");
      return;
    }

    const { used, usedCopy, remainingCopy, level } = weeklyPresentation(weekly);
    $("#current-account-quota-copy").textContent = `周额度 ${usedCopy}%`;
    $("#current-account-quota-remaining").textContent = `剩余 ${remainingCopy}%`;
    quota.className = `usage-current-quota ${level}`.trim();
    quotaTrack.value = used;
    quotaTrack.setAttribute("aria-label", `当前账号周额度已使用 ${usedCopy}%`);
  };

  const renderUsageSummary = (usage) => {
    const rangeLabel = usageWindowLabel();
    $("#usage-summary-label").textContent = `${rangeLabel} Token`;
    if (!usage) {
      $("#range-raw-tokens").textContent = "—";
      $("#range-weighted-tokens").textContent = "—";
      $("#range-request-count").textContent = `${rangeLabel}请求 —`;
      return;
    }
    const rawTokens = Number(usage.total_tokens) || 0;
    const weightedTokens = Number(usage.weighted_tokens ?? rawTokens) || 0;
    const raw = $("#range-raw-tokens");
    const weighted = $("#range-weighted-tokens");
    raw.innerHTML = renderTokenUsage(rawTokens);
    raw.title = `${formatNumber(rawTokens)} Token`;
    weighted.innerHTML = renderTokenUsage(weightedTokens);
    weighted.title = `${formatNumber(weightedTokens)} 加权 Token`;
    $("#range-request-count").textContent = `${rangeLabel}请求 ${formatNumber(usage.request_count)}`;
  };

  const summarizeGroupUsage = (groups = []) => groups.reduce((summary, group) => {
    const usage = group.usage || {};
    summary.request_count += Number(usage.request_count) || 0;
    summary.total_tokens += Number(usage.total_tokens) || 0;
    summary.weighted_tokens += Number(usage.weighted_tokens ?? usage.total_tokens) || 0;
    return summary;
  }, { request_count: 0, total_tokens: 0, weighted_tokens: 0 });

  const windowUsageForPayload = (payload) => (
    payload.window_usage && typeof payload.window_usage === "object"
      ? payload.window_usage
      : summarizeGroupUsage(payload.groups)
  );

  const usageBreakdownKey = (group) => [group.account, state.windowSeconds].join("\u0000");

  const usageBreakdownTooltip = (model, effort) => [
    `${model} · ${effort.reasoning_effort}`,
    `调用：${formatNumber(effort.request_count)}`,
    `输入：${formatNumber(effort.input_tokens)}`,
    `输出：${formatNumber(effort.output_tokens)}`,
    `推理：${formatNumber(effort.reasoning_tokens)}`,
    `缓存：${formatNumber(effort.cached_tokens)}`,
    `总 Token：${formatNumber(effort.total_tokens)}`,
    `加权 Token：${formatNumber(effort.weighted_tokens ?? effort.total_tokens)}`
  ];

  const renderUsageBreakdown = (group) => {
    const key = usageBreakdownKey(group);
    const cached = state.usageBreakdowns.get(key);
    const payload = cached?.payload;
    const error = state.usageBreakdownErrors.get(key);
    const loading = state.usageBreakdownLoading.has(key) || (!payload && !error);
    let content = "";
    if (loading && !payload) {
      content = '<div class="account-model-usage-skeleton" aria-label="正在加载我的模型 Token 明细"><span></span><span></span></div>';
    } else if (error && !payload) {
      content = `<div class="account-model-usage-message error" role="alert">
        <span>${escapeHTML(error)}</span>
        <button class="usage-breakdown-retry" type="button" data-usage-breakdown-retry="${escapeHTML(group.id)}">重试</button>
      </div>`;
    } else {
      const models = globalThis.MonitorUtils.groupAccountModelUsage(payload?.combinations || []);
      content = models.length
        ? `<div class="account-model-usage-list">${models.map((model) => `
          <div class="account-model-usage-row">
            <div class="account-model-usage-head">
              <strong title="${escapeHTML(model.model)}">${escapeHTML(model.model)}</strong>
              ${renderTokenUsage(model.total_tokens)}
            </div>
            <div class="account-model-progress" role="group" aria-label="${escapeHTML(`${model.model} 各推理强度 Token 占比`)}">
              ${model.efforts.map((effort) => {
                const tooltip = usageBreakdownTooltip(model.model, effort);
                const share = `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(effort.share_percent)}%`;
                const compact = effort.share_percent < 18 ? " compact" : "";
                const shareUnits = Math.max(1, Math.min(100, Math.round(effort.share_percent)));
                const shareClasses = `account-model-share-tens-${Math.floor(shareUnits / 10)} account-model-share-ones-${shareUnits % 10}`;
                const effortColor = globalThis.MonitorUtils.accountModelEffortColorKey(effort.reasoning_effort);
                return `<button class="account-model-progress-segment account-model-effort-${effortColor} ${shareClasses}${compact}" type="button" data-tooltip="${escapeHTML(tooltip.join("\n"))}" aria-label="${escapeHTML(tooltip.join("，"))}"><span>${escapeHTML(effort.reasoning_effort)}</span><em>${escapeHTML(share)}</em></button>`;
              }).join("")}
            </div>
          </div>`).join("")}</div>`
        : '<div class="account-model-usage-message">当前范围暂无我的模型与推理强度 Token 数据。</div>';
    }
    const partial = payload
      && Number(payload.collection_started_at) > Number(payload.window_start_at)
      ? `<small class="usage-breakdown-history">模型维度数据自 ${escapeHTML(formatTime(payload.collection_started_at))} 起采集，本范围更早的数据未包含在图中。</small>`
      : "";
    return `<section class="account-model-usage" aria-label="我的模型与推理强度 Token 明细">
      <div class="account-model-usage-title"><span>我的模型 × 推理强度 Token 明细</span><small>${escapeHTML(usageWindowLabel())}</small></div>
      ${content}
      ${partial}
    </section>`;
  };

  const detailRow = (group) => {
    const usage = group.usage || {};
    const open = state.expanded.has(group.id);
    return `<tr class="usage-detail-row" data-detail-for="${escapeHTML(group.id)}" ${open ? "" : "hidden"}>
      <td colspan="10">
        <div class="usage-account-detail">
          <div class="usage-detail-panel">
            <div class="usage-detail-heading">
              <strong>我的使用明细</strong>
              <span>${escapeHTML(usageWindowLabel())}</span>
            </div>
            <div class="usage-token-grid">
              <div><span>成功请求</span><strong>${formatNumber(usage.success_count)}</strong></div>
              <div><span>失败请求</span><strong>${formatNumber(usage.failed_count)}</strong></div>
              <div><span>输入 Token</span>${renderTokenUsage(usage.input_tokens)}</div>
              <div><span>输出 Token</span>${renderTokenUsage(usage.output_tokens)}</div>
              <div><span>推理 Token</span>${renderTokenUsage(usage.reasoning_tokens)}</div>
              <div class="usage-cache-fact"><div class="usage-cache-head"><span>缓存 Token</span><small class="usage-cache-rate" title="缓存 Token ÷ 输入 Token">缓存率 ${escapeHTML(formatUsagePercent(usage.cached_tokens, usage.input_tokens))}</small></div>${renderTokenUsage(usage.cached_tokens)}</div>
              <div><span>未加权 Token</span>${renderTokenUsage(usage.total_tokens)}</div>
              <div><span>加权 Token</span>${renderTokenUsage(usage.weighted_tokens ?? usage.total_tokens)}</div>
            </div>
          </div>
          ${renderUsageBreakdown(group)}
        </div>
      </td>
    </tr>`;
  };

  const loadUsageBreakdown = async (groupId, force = false) => {
    const group = state.payload?.groups?.find((item) => item.id === groupId);
    if (!group) return;
    const key = usageBreakdownKey(group);
    const cached = state.usageBreakdowns.get(key);
    if (!force && cached && Date.now() - cached.fetchedAt < 30_000) return;
    if (state.usageBreakdownLoading.has(key)) return;
    const controller = new AbortController();
    state.usageBreakdownControllers.set(key, controller);
    state.usageBreakdownLoading.add(key);
    state.usageBreakdownErrors.delete(key);
    if (state.expanded.has(groupId)) renderRows();
    try {
      const query = new URLSearchParams({ account: group.account, window: state.windowSeconds });
      const response = await fetch(`/usage/me/usage-breakdown?${query.toString()}`, {
        cache: "no-store",
        signal: controller.signal
      });
      let payload = {};
      try { payload = await response.json(); } catch { payload = {}; }
      if (response.status === 401) {
        showLogin();
        return;
      }
      if (!response.ok) throw new Error(payload.error?.message || `明细加载失败（HTTP ${response.status}）`);
      state.usageBreakdowns.set(key, { payload, fetchedAt: Date.now() });
    } catch (requestError) {
      if (requestError.name === "AbortError") return;
      state.usageBreakdownErrors.set(key, requestError.message || "模型 Token 明细加载失败");
    } finally {
      if (state.usageBreakdownControllers.get(key) === controller) {
        state.usageBreakdownControllers.delete(key);
        state.usageBreakdownLoading.delete(key);
        if (state.expanded.has(groupId)) renderRows();
      }
    }
  };

  const renderRows = () => {
    const groups = [...(state.payload?.groups || [])];
    const hasCurrentGroup = Boolean(state.payload?.current_group);
    const statusOrder = {
      available: 0,
      quota_warning: 1,
      transient_cooldown: 2,
      rate_limited: 3,
      degraded: 4,
      quota_unknown: 5,
      unknown: 6,
      quota_exhausted: 7,
      credential_unavailable: 8,
      auth_missing: 9,
      stopped: 10,
      disabled: 11
    };
    const quotaSortValue = (group) => {
      if (!statusPresentation(group).selectable || !group.weekly) return null;
      if (group.weekly.limit_reached) return 100;
      const rawUsedPercent = group.weekly.used_percent;
      if (rawUsedPercent === null || rawUsedPercent === undefined || rawUsedPercent === "") return null;
      const usedPercent = Number(rawUsedPercent);
      return Number.isFinite(usedPercent)
        ? Math.max(0, Math.min(usedPercent, 100))
        : null;
    };
    const sortValue = (group) => ({
      current: group.current ? 0 : 1,
      account: group.account,
      quota: quotaSortValue(group),
      active_users: group.active_users_1h ?? 0,
      status: statusOrder[statusPresentation(group).code] ?? 11,
      requests: group.usage?.request_count ?? 0,
      tokens: group.usage?.weighted_tokens ?? group.usage?.total_tokens ?? 0,
      last_used: group.usage?.last_used_at || null
    }[state.sort.field]);
    groups.sort((left, right) => (
      (state.sort.pinCurrent && left.current !== right.current
        ? (left.current ? -1 : 1)
        : 0)
      || compareTableValues(sortValue(left), sortValue(right), state.sort.direction)
      || tableCollator.compare(left.account, right.account)
    ));
    document.querySelectorAll("[data-usage-sort]").forEach((button) => {
      const active = button.dataset.usageSort === state.sort.field;
      button.classList.toggle("active", active);
      button.dataset.direction = active ? state.sort.direction : "";
      button.closest("th")?.setAttribute("aria-sort", active
        ? (state.sort.direction === "asc" ? "ascending" : "descending")
        : "none");
      const label = button.querySelector(".usage-sort-copy > span")?.textContent || "此列";
      const pinnedCurrent = active && state.sort.pinCurrent && state.sort.field === "quota"
        ? "，当前账号固定在第一行"
        : "";
      button.setAttribute("aria-label", active
        ? `${label}，当前${state.sort.direction === "asc" ? "升序" : "降序"}${pinnedCurrent}，点击切换排序方向`
        : `${label}，点击排序`);
    });
    $("#empty-state").hidden = groups.length > 0;
    $("#account-rows").innerHTML = groups.map((group, index) => {
      const usage = group.usage || {};
      const expanded = state.expanded.has(group.id);
      const groupStatus = statusPresentation(group);
      const selectable = groupStatus.selectable && !group.current;
      const selection = group.current
        ? '<span class="usage-current-mark" title="当前账号">✓<span class="sr-only">当前账号</span></span>'
        : `<button class="usage-select-button" type="button" data-switch-group="${escapeHTML(group.id)}" title="${escapeHTML(groupStatus.reason)}" ${selectable ? "" : "disabled"}>${hasCurrentGroup ? "切换" : "选择"}</button>`;
      return `<tr class="usage-summary-row ${group.current ? "current" : ""}" data-expand-group="${escapeHTML(group.id)}" aria-expanded="${expanded}">
        <td class="table-index-cell" data-label="序号">${index + 1}</td>
        <td data-label="当前账号">${selection}</td>
        <td data-label="CPA 账号"><strong class="usage-account-id">${escapeHTML(group.account)}</strong></td>
        <td data-label="账号周额度">${weeklyCell(group.weekly)}</td>
        <td data-label="活跃用户（近 1 小时）"><strong class="usage-cell-number">${formatNumber(group.active_users_1h)}</strong></td>
        <td data-label="账号状态">${statusCell(group)}</td>
        <td data-label="我的请求（${escapeHTML(usageWindowLabel())}）"><strong class="usage-cell-number" title="${formatNumber(usage.request_count)}">${formatCompact(usage.request_count)}</strong></td>
        <td class="usage-token-cell" data-label="我的 Token（${escapeHTML(usageWindowLabel())}）"><div class="usage-token-content">${userTokenPair(usage)}</div></td>
        <td data-label="我的最后使用"><time class="usage-last-used">${escapeHTML(formatTime(usage.last_used_at))}</time></td>
        <td><button class="usage-expand-button" type="button" data-expand-button="${escapeHTML(group.id)}" aria-label="${expanded ? "收起" : "展开"}">${expanded ? "−" : "+"}</button></td>
      </tr>${detailRow(group)}`;
    }).join("");
  };

  const renderDashboard = (payload) => {
    if (state.payload?.user && state.payload.user !== payload.user) {
      state.expanded.clear();
      resetUsageBreakdowns();
    }
    state.payload = payload;
    if (payload.route_assignment?.status === "assigned") {
      showToast(`已自动分配至 ${payload.route_assignment.account}`);
    }
    $("#user-badge").textContent = payload.user;
    $("#user-badge").hidden = false;
    $("#change-password-button").hidden = false;
    $("#logout-button").hidden = false;
    $("#api-key").textContent = payload.api_key;
    renderCurrentAccount(payload);
    renderUserQuota(payload.weekly_quota);
    renderUsageSummary(windowUsageForPayload(payload));
    const routeNotice = $("#route-notice");
    routeNotice.hidden = Boolean(payload.current_group);
    if (!payload.current_group) {
      $("#route-notice-title").textContent = "暂时无法自动分配 CPA";
      $("#route-notice-message").textContent = payload.route_assignment?.message
        || "当前没有可用账号；系统会在下次刷新时自动重试，也可以在下方手动选择。";
    }
    $("#ccswitch-link").href = buildCcSwitchLink();
    $("#key-card").hidden = false;
    $("#codex-history-card").hidden = false;
    $("#dashboard").hidden = false;
    $("#page-error").hidden = true;
    const quotaUpdatedAt = payload.quota_generated_at || payload.generated_at;
    const quotaState = payload.quota_refreshing ? "（后台更新中）" : payload.quota_cached ? "（缓存）" : "";
    $("#updated-at").textContent = `额度更新 ${formatTime(quotaUpdatedAt)}${quotaState}`;
    $("#request-window-label").textContent = usageWindowLabel();
    $("#token-window-label").textContent = usageWindowLabel();
    renderRows();
    state.expanded.forEach((groupId) => loadUsageBreakdown(groupId));
    window.clearTimeout(state.quotaRefreshTimer);
    if (payload.quota_refreshing) {
      state.quotaRefreshTimer = window.setTimeout(() => loadDashboard({ quiet: true }), 5000);
    }
  };

  const showLogin = () => {
    state.payload = null;
    state.expanded.clear();
    resetUsageBreakdowns();
    $("#api-key").textContent = "—";
    $("#key-card").hidden = true;
    $("#codex-history-card").hidden = true;
    $("#dashboard").hidden = true;
    $("#user-badge").hidden = true;
    $("#change-password-button").hidden = true;
    $("#logout-button").hidden = true;
    state.passwordChangeRequired = false;
    const dialog = $("#login-dialog");
    if ($("#codex-history-dialog").open) $("#codex-history-dialog").close();
    if ($("#password-dialog").open) $("#password-dialog").close();
    if ($("#rotate-key-dialog").open) $("#rotate-key-dialog").close();
    if (!dialog.open) dialog.showModal();
    window.setTimeout(() => $("#login-email").focus(), 50);
  };

  const resetPasswordForm = (currentPassword = "") => {
    $("#current-password").value = currentPassword;
    $("#new-password").value = "";
    $("#confirm-password").value = "";
    $("#password-error").textContent = "";
    $("#password-error").hidden = true;
  };

  const closeOptionalPasswordChange = () => {
    if (state.passwordChangeRequired) return;
    const dialog = $("#password-dialog");
    if (dialog.open) dialog.close();
    resetPasswordForm();
  };

  const showPasswordChange = ({ required = true, currentPassword = "" } = {}) => {
    if ($("#login-dialog").open) $("#login-dialog").close();
    const dialog = $("#password-dialog");
    state.passwordChangeRequired = Boolean(required);
    $("#password-title").textContent = required ? "首次登录请修改密码" : "修改登录密码";
    $("#password-notice").textContent = required
      ? "初始密码仅用于首次登录。设置至少 8 位的新密码后，才能查看 API Key 和用量。"
      : "输入当前密码并设置至少 8 位的新密码。修改成功后，其他已登录会话将失效。";
    $("#cancel-password").hidden = required;
    if (!dialog.open) {
      resetPasswordForm(currentPassword);
      dialog.showModal();
    } else if (currentPassword && !$("#current-password").value) {
      $("#current-password").value = currentPassword;
    }
    window.setTimeout(() => $("#current-password").focus(), 50);
  };

  $("#password-dialog").addEventListener("cancel", (event) => {
    event.preventDefault();
    closeOptionalPasswordChange();
  });
  $("#cancel-password").addEventListener("click", closeOptionalPasswordChange);
  $("#change-password-button").addEventListener("click", () => {
    showPasswordChange({ required: false });
  });

  const loadDashboard = async ({ quiet = false, fresh = false } = {}) => {
    if (state.controller) state.controller.abort();
    state.controller = new AbortController();
    const button = $("#refresh-button");
    if (!quiet) button.disabled = true;
    try {
      const freshQuery = fresh ? "&fresh=1" : "";
      const response = await fetch(`/usage/me?window=${state.windowSeconds}&lifetime=0${freshQuery}`, {
        cache: "no-store",
        signal: state.controller.signal
      });
      let payload = {};
      try { payload = await response.json(); } catch { payload = {}; }
      if (response.status === 401) {
        showLogin();
        return;
      }
      if (response.status === 403 && payload.error?.code === "password_change_required") {
        showPasswordChange({ required: true });
        return;
      }
      if (!response.ok) throw new Error(payload.error?.message || `加载失败（HTTP ${response.status}）`);
      if ($("#login-dialog").open) $("#login-dialog").close();
      renderDashboard(payload);
    } catch (error) {
      if (error.name === "AbortError") return;
      $("#page-error").textContent = error.message;
      $("#page-error").hidden = false;
    } finally {
      button.disabled = false;
    }
  };

  const checkCurrentRoute = async () => {
    if (
      !state.payload
      || document.visibilityState !== "visible"
      || state.routeCheckInFlight
    ) return;
    state.routeCheckInFlight = true;
    try {
      const response = await fetch("/usage/me/route", { cache: "no-store" });
      let payload = {};
      try { payload = await response.json(); } catch { payload = {}; }
      if (response.status === 401) {
        showLogin();
        return;
      }
      if (response.status === 403 && payload.error?.code === "password_change_required") {
        showPasswordChange({ required: true });
        return;
      }
      if (!response.ok) return;
      const currentGroup = payload.current_group || "";
      const displayedGroup = state.payload?.current_group || "";
      if (currentGroup !== displayedGroup) {
        await loadDashboard({ quiet: true });
      }
    } catch {
      // The full dashboard remains usable; the normal refresh can recover later.
    } finally {
      state.routeCheckInFlight = false;
    }
  };

  const normalizeEmail = () => {
    const input = $("#login-email");
    input.value = input.value.trim().toLowerCase();
    return input.value;
  };

  const startLoginRetryCountdown = (retryAfter) => {
    const seconds = Math.max(1, Number.parseInt(retryAfter, 10) || 1);
    loginRetryUntil = Date.now() + seconds * 1000;
    window.clearInterval(loginRetryTimer);
    const update = () => {
      const remaining = Math.max(0, Math.ceil((loginRetryUntil - Date.now()) / 1000));
      const button = $("#login-submit");
      const error = $("#login-error");
      if (remaining > 0) {
        button.disabled = true;
        button.textContent = `${remaining} 秒后重试`;
        error.textContent = `登录尝试过于频繁，请 ${remaining} 秒后重试`;
        error.hidden = false;
        return;
      }
      window.clearInterval(loginRetryTimer);
      loginRetryTimer = null;
      loginRetryUntil = 0;
      button.disabled = false;
      button.textContent = "登录";
    };
    update();
    loginRetryTimer = window.setInterval(update, 250);
  };

  $("#login-email").addEventListener("blur", normalizeEmail);
  $("#login-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    if (loginRetryUntil > Date.now()) return;
    const email = normalizeEmail();
    const password = $("#login-password").value;
    const error = $("#login-error");
    const button = $("#login-submit");
    error.hidden = true;
    button.disabled = true;
    try {
      const response = await fetch("/usage/session", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, password }),
        cache: "no-store"
      });
      let payload = {};
      try { payload = await response.json(); } catch { payload = {}; }
      if (response.status === 429) {
        startLoginRetryCountdown(response.headers.get("Retry-After"));
        return;
      }
      if (!response.ok) throw new Error(payload.error?.message || "无法进入使用中心");
      if (payload.password_change_required) {
        showPasswordChange({ required: true, currentPassword: password });
      } else {
        $("#login-dialog").close();
        await loadDashboard({ fresh: true });
      }
    } catch (requestError) {
      error.textContent = requestError.message;
      error.hidden = false;
    } finally {
      button.disabled = loginRetryUntil > Date.now();
      if (!button.disabled) button.textContent = "登录";
    }
  });

  $("#password-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const currentPassword = $("#current-password").value;
    const newPassword = $("#new-password").value;
    const confirmPassword = $("#confirm-password").value;
    const error = $("#password-error");
    const button = $("#password-submit");
    error.hidden = true;
    if (newPassword !== confirmPassword) {
      error.textContent = "两次输入的新密码不一致";
      error.hidden = false;
      return;
    }
    button.disabled = true;
    try {
      const response = await fetch("/usage/me/password", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          current_password: currentPassword,
          new_password: newPassword
        }),
        cache: "no-store"
      });
      let payload = {};
      try { payload = await response.json(); } catch { payload = {}; }
      if (response.status === 401 && payload.error?.code === "session_required") {
        $("#current-password").value = "";
        $("#new-password").value = "";
        $("#confirm-password").value = "";
        showLogin();
        return;
      }
      if (!response.ok) throw new Error(payload.error?.message || "密码修改失败");
      state.passwordChangeRequired = false;
      $("#password-dialog").close();
      $("#login-password").value = "";
      $("#current-password").value = "";
      $("#new-password").value = "";
      $("#confirm-password").value = "";
      showToast(payload.message || "密码已修改");
      await loadDashboard({ fresh: true });
    } catch (requestError) {
      error.textContent = requestError.message;
      error.hidden = false;
    } finally {
      button.disabled = false;
    }
  });

  $("#logout-button").addEventListener("click", async () => {
    await fetch("/usage/session", { method: "DELETE", cache: "no-store" });
    state.payload = null;
    state.expanded.clear();
    resetUsageBreakdowns();
    $("#login-email").value = "";
    $("#login-password").value = "";
    showLogin();
  });

  $("#account-rows").addEventListener("click", (event) => {
    const retryButton = event.target.closest("[data-usage-breakdown-retry]");
    if (retryButton) {
      event.stopPropagation();
      loadUsageBreakdown(retryButton.dataset.usageBreakdownRetry, true);
      return;
    }
    const switchButton = event.target.closest("[data-switch-group]");
    if (switchButton) {
      event.stopPropagation();
      const group = state.payload.groups.find((item) => item.id === switchButton.dataset.switchGroup);
      if (!group) return;
      state.pendingGroup = group.id;
      const hasCurrentGroup = Boolean(state.payload?.current_group);
      $("#switch-title").textContent = hasCurrentGroup ? "切换账号" : "选择账号";
      $("#switch-message").textContent = hasCurrentGroup
        ? `将当前 API Key 切换到 ${group.account}？`
        : `选择 ${group.account} 作为当前账号？选择后 API Key 将立即生效。`;
      $("#confirm-switch").textContent = hasCurrentGroup ? "确认切换" : "确认选择";
      $("#switch-dialog").showModal();
      return;
    }
    const row = event.target.closest("[data-expand-group]");
    if (!row) return;
    const groupId = row.dataset.expandGroup;
    const opening = !state.expanded.has(groupId);
    if (opening) state.expanded.add(groupId);
    else state.expanded.delete(groupId);
    renderRows();
    if (opening) loadUsageBreakdown(groupId);
  });

  const closeSwitch = () => {
    state.pendingGroup = "";
    if ($("#switch-dialog").open) $("#switch-dialog").close();
  };
  $("#close-switch").addEventListener("click", closeSwitch);
  $("#cancel-switch").addEventListener("click", closeSwitch);
  $("#confirm-switch").addEventListener("click", async () => {
    const groupId = state.pendingGroup;
    if (!groupId) return;
    const button = $("#confirm-switch");
    button.disabled = true;
    try {
      const response = await fetch("/usage/me/group", {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ group_id: groupId }),
        cache: "no-store"
      });
      let payload = {};
      try { payload = await response.json(); } catch { payload = {}; }
      if (!response.ok) throw new Error(payload.error?.message || "切换失败");
      closeSwitch();
      showToast("账号已切换");
      await loadDashboard();
    } catch (error) {
      showToast(error.message);
    } finally {
      button.disabled = false;
    }
  });

  const closeRotateKey = () => {
    if ($("#rotate-key-dialog").open) $("#rotate-key-dialog").close();
  };
  $("#rotate-key").addEventListener("click", () => {
    $("#rotate-key-dialog").showModal();
  });
  $("#close-rotate-key").addEventListener("click", closeRotateKey);
  $("#cancel-rotate-key").addEventListener("click", closeRotateKey);
  $("#confirm-rotate-key").addEventListener("click", async () => {
    const button = $("#confirm-rotate-key");
    button.disabled = true;
    button.textContent = "刷新中…";
    try {
      const response = await fetch("/usage/me/key/rotate", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ confirm: true }),
        cache: "no-store"
      });
      let payload = {};
      try { payload = await response.json(); } catch { payload = {}; }
      if (response.status === 401) {
        closeRotateKey();
        showLogin();
        return;
      }
      if (!response.ok) throw new Error(payload.error?.message || "API Key 刷新失败");
      if (!payload.api_key) throw new Error("API Key 刷新响应无效");
      state.payload = { ...state.payload, api_key: payload.api_key };
      $("#api-key").textContent = payload.api_key;
      $("#ccswitch-link").href = buildCcSwitchLink();
      closeRotateKey();
      showToast(payload.message || "API Key 已刷新");
      $("#copy-key").focus();
    } catch (error) {
      showToast(error.message);
    } finally {
      button.disabled = false;
      button.textContent = "确认刷新";
    }
  });

  const renderConfigSections = (sections) => {
    const workflow = $("#config-workflow");
    workflow.replaceChildren();
    sections.forEach((section, index) => {
      const article = document.createElement("article");
      article.className = "usage-config-step";

      const marker = document.createElement("span");
      marker.className = "usage-config-step-number";
      marker.textContent = String(index + 1).padStart(2, "0");

      const content = document.createElement("div");
      content.className = "usage-config-step-content";

      const head = document.createElement("div");
      head.className = "usage-config-step-head";
      const heading = document.createElement("div");
      const title = document.createElement("h3");
      title.textContent = section.title;
      const file = document.createElement("code");
      file.className = "usage-config-step-file";
      file.textContent = section.file;
      heading.append(title, file);

      const copyButton = document.createElement("button");
      copyButton.className = "usage-config-copy-button";
      copyButton.type = "button";
      copyButton.textContent = section.copyLabel || "复制内容";
      copyButton.addEventListener("click", () => copyText(section.value));
      head.append(heading, copyButton);

      const description = document.createElement("p");
      description.className = "usage-config-step-description";
      description.textContent = section.description;

      const preview = document.createElement("pre");
      preview.className = "usage-config-preview usage-config-step-preview";
      const code = document.createElement("code");
      code.textContent = section.value;
      preview.append(code);

      content.append(head, description, preview);
      if (section.hint) {
        const hint = document.createElement("p");
        hint.className = "usage-config-step-hint";
        hint.textContent = section.hint;
        content.append(hint);
      }
      article.append(marker, content);
      workflow.append(article);
    });
  };

  const openConfig = ({
    title,
    file,
    steps,
    value,
    notice = "",
    sections = [],
    copyLabel = "复制配置",
    externalLink = "",
    externalLabel = "继续"
  }) => {
    state.activeConfig = value;
    $("#config-title").textContent = title;
    $("#config-file").textContent = file;
    $("#config-steps").innerHTML = steps
      .map((step) => `<li>${escapeHTML(step)}</li>`)
      .join("");
    $("#config-content").textContent = value;
    const noticeElement = $("#config-notice");
    noticeElement.textContent = notice;
    noticeElement.hidden = !notice;
    const hasWorkflow = sections.length > 0;
    $("#config-simple-section").hidden = hasWorkflow;
    $("#config-workflow").hidden = !hasWorkflow;
    if (hasWorkflow) renderConfigSections(sections);
    const copyButton = $("#copy-config");
    copyButton.textContent = copyLabel;
    copyButton.className = externalLink ? "usage-secondary-button" : "usage-primary-button";
    const externalAction = $("#config-external-link");
    externalAction.href = externalLink || "#";
    externalAction.textContent = externalLabel;
    externalAction.hidden = !externalLink;
    if (!$("#config-dialog").open) $("#config-dialog").showModal();
  };
  const closeConfig = () => {
    if ($("#config-dialog").open) $("#config-dialog").close();
  };
  $("#open-codex-config").addEventListener("click", () => openConfig({
    title: "Codex 配置",
    file: "~/.codex/config.toml",
    steps: ["打开或创建上述文件", "合并下方配置并保存", "重新启动 Codex"],
    value: buildCodexConfig()
  }));
  $("#open-claude-config").addEventListener("click", () => openConfig({
    title: "Claude Code 终端配置",
    file: "~/.config/claude-cpa/",
    steps: ["准备目录", "保存 Key", "创建启动脚本", "加载并验证"],
    value: buildClaudeCodeConfig(),
    notice: "以下内容已包含你的完整 API Key。仅在可信设备保存，不要粘贴到聊天、Issue 或 Git 仓库。",
    sections: buildClaudeCodeSections(),
    copyLabel: "复制启动脚本"
  }));
  $("#close-config").addEventListener("click", closeConfig);
  $("#cancel-config").addEventListener("click", closeConfig);
  $("#copy-config").addEventListener("click", () => copyText(state.activeConfig));
  $("#config-external-link").addEventListener("click", () => {
    copyText(state.activeConfig);
  });
  const closeCodexHistory = () => {
    if ($("#codex-history-dialog").open) $("#codex-history-dialog").close();
  };
  $("#open-codex-history").addEventListener("click", () => {
    $("#codex-history-agent-prompt").textContent = buildCodexHistoryAgentPrompt();
    $("#codex-history-dialog").showModal();
  });
  $("#close-codex-history").addEventListener("click", closeCodexHistory);
  $("#cancel-codex-history").addEventListener("click", closeCodexHistory);
  $("#copy-codex-history").addEventListener("click", () => copyText(buildCodexHistoryAgentPrompt()));
  $("#copy-key").addEventListener("click", () => copyText(state.payload?.api_key || ""));
  $("#ccswitch-link").addEventListener("click", (event) => {
    event.preventDefault();
    openConfig({
      title: "完成 CC Switch 图片配置",
      file: "CC Switch → Codex → CPA Provider → 编辑 → config.toml",
      steps: [
        "点击“复制配置并继续导入”，在 CC Switch 中确认导入",
        "编辑刚导入的 CPA Provider，将已复制的内容完整替换到 config.toml",
        "保存并切换到该 Provider；无需开启 CC Switch 本地路由",
        "完全退出并重新启动 Codex，然后新建任务"
      ],
      value: buildCodexConfig(),
      notice: "一键导入会先带入地址、模型和 Key。为了让 Codex 加载图片生成工具，还需在 CC Switch 中粘贴下方完整配置。配置含完整 API Key，仅在自己的设备使用。",
      copyLabel: "仅复制图片配置",
      externalLink: buildCcSwitchLink(),
      externalLabel: "复制配置并继续导入"
    });
  });

  document.querySelectorAll("[data-window]").forEach((button) => {
    button.addEventListener("click", () => {
      state.windowSeconds = button.dataset.window;
      document.querySelectorAll("[data-window]").forEach((item) => {
        item.setAttribute("aria-pressed", String(item === button));
      });
      renderUsageSummary(null);
      loadDashboard();
    });
  });
  document.querySelectorAll("[data-usage-sort]").forEach((button) => {
    button.addEventListener("click", () => {
      const field = button.dataset.usageSort;
      if (state.sort.field === field) {
        state.sort.direction = state.sort.direction === "asc" ? "desc" : "asc";
        state.sort.pinCurrent = false;
      } else {
        state.sort = {
          field,
          direction: ["account", "current", "quota", "status"].includes(field) ? "asc" : "desc",
          pinCurrent: false
        };
      }
      renderRows();
    });
  });
  $("#refresh-button").addEventListener("click", () => {
    resetUsageBreakdowns();
    loadDashboard({ fresh: true });
  });
  applyTheme(preferredTheme());
  $("#usage-theme-toggle").addEventListener("click", () => {
    applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark", true);
  });

  Promise.resolve(window.cpaBrandingReady).then((config) => {
    if (config) state.siteConfig = { ...state.siteConfig, ...config };
    return loadDashboard({ fresh: true });
  });
  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible") checkCurrentRoute();
  });
  window.addEventListener("pageshow", checkCurrentRoute);
  window.addEventListener("focus", checkCurrentRoute);
  window.setInterval(checkCurrentRoute, 5_000);
  window.setInterval(() => {
    if (state.payload && document.visibilityState === "visible") loadDashboard({ quiet: true });
  }, 30_000);
})();
