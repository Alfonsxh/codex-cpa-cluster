(() => {
  "use strict";

  const RELEASE_POLL_INTERVAL_MS = 15 * 60 * 1000;

  const REASONING_MULTIPLIER_PREFIX = "user_quota.reasoning_multiplier.";
  const REASONING_COLOR_PREFIX = "admin.account_usage.reasoning_effort_color.";
  const REASONING_EFFORTS = [
    ["none", "None"],
    ["minimal", "Minimal"],
    ["low", "Low"],
    ["medium", "Medium"],
    ["high", "High"],
    ["xhigh", "XHigh"],
    ["max", "Max"],
    ["ultra", "Ultra"],
    ["auto", "Auto"],
    ["unknown", "Unknown"]
  ];
  const state = {
    key: "",
    authenticated: false,
    csrfToken: "",
    overview: null,
    overviewCatalog: null,
    overviewCatalogLoading: false,
    overviewCatalogError: "",
    overviewCatalogRequestId: 0,
    overviewUsage: null,
    overviewUsageWindow: "today",
    overviewUsageCustomRange: null,
    overviewUsageAccounts: [],
    overviewUsageUsers: [],
    overviewUsageUserLimit: "10",
    overviewUsageRefresh: "30",
    overviewUsageLoading: false,
    overviewUsageError: "",
    overviewUsageRequestId: 0,
    overviewUsageSort: {
      account: { field: "total", direction: "desc" },
      user: { field: "total", direction: "desc" }
    },
    settings: null,
    accounts: [],
    accountCollector: null,
    imageStatus: null,
    releaseStatus: null,
    accountUsageWindow: "today",
    accountCustomRange: null,
    accountSort: { field: "quota", direction: "asc" },
    expandedAccount: "",
    accountUsageBreakdowns: new Map(),
    accountUsageBreakdownLoading: new Set(),
    accountUsageBreakdownErrors: new Map(),
    users: [],
    teams: [],
    teamUsage: [],
    teamUsageLoading: false,
    teamUsageError: "",
    userTeamFilter: "",
    classificationUsers: [],
    userCollector: null,
    userUsageWindow: "today",
    userCustomRange: null,
    customUsageRangeTarget: "",
    expandedUser: "",
    userUsageBreakdowns: new Map(),
    userUsageBreakdownLoading: new Set(),
    userUsageBreakdownErrors: new Map(),
    userUsageAccountFilters: new Map(),
    userUsageBreakdownSort: { field: "total_tokens", direction: "desc" },
    userAccountSort: { field: "total_tokens", direction: "desc" },
    userSort: { field: "tokens", direction: "desc" },
    userPage: 1,
    userPageSize: 50,
    userPagination: { page: 1, page_size: 50, total: 0, total_pages: 1 },
    userDetails: new Map(),
    userDetailLoading: new Set(),
    userDetailErrors: new Map(),
    selectedUsers: new Set(),
    organizationTeamId: "",
    organizationUsers: [],
    organizationPagination: { page: 1, page_size: 50, total: 0, total_pages: 1 },
    organizationPage: 1,
    organizationSelectedUsers: new Map(),
    organizationAllMatches: false,
    selectedUser: "",
    selectedUserQuota: null,
    quotaAction: null,
    selectedAccount: "",
    accountPolicyAccount: "",
    quotaResetAccount: "",
    quotaResetCredits: [],
    configurationOriginal: {},
    configurationDraft: {},
    configurationDirty: false,
    configurationGroup: "",
    configurationSearch: "",
    configurationFocusKey: "",
    settingsSection: "configuration",
    notificationWebhookDraft: "",
    notificationWebhookDirty: false,
    view: "overview",
    viewLoadedAt: new Map(),
    viewRequestIds: new Map(),
    activeJob: "",
    jobTimer: null,
    refreshTimer: null,
    overviewUsageTimer: null,
    secrets: [],
    oauthUrl: "",
    oauthCode: ""
  };

  const $ = (selector, root = document) => root.querySelector(selector);
  const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];
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
  const THEME_STORAGE_KEY = "cpa-ui-theme";
  const LEGACY_THEME_STORAGE_KEY = "cpa-admin-theme";
  const preferredTheme = () => {
    try {
      const stored = window.localStorage.getItem(THEME_STORAGE_KEY)
        || window.localStorage.getItem(LEGACY_THEME_STORAGE_KEY);
      if (stored === "light" || stored === "dark") return stored;
    } catch { /* storage may be unavailable in private contexts */ }
    return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  };

  const bootstrapTheme = preferredTheme();
  document.documentElement.dataset.theme = bootstrapTheme;
  document.documentElement.style.colorScheme = bootstrapTheme;

  const applyTheme = (theme, persist = false) => {
    const resolved = theme === "dark" ? "dark" : "light";
    document.documentElement.dataset.theme = resolved;
    document.documentElement.style.colorScheme = resolved;
    const favicon = $("#app-favicon");
    if (favicon) {
      favicon.href = `/portal/assets/codex-cpa-cluster-favicon${resolved === "dark" ? "-dark" : ""}.svg`;
    }
    const sidebarMark = $("#sidebar-brand-mark");
    if (sidebarMark) {
      sidebarMark.src = `/portal/assets/codex-cpa-cluster-mark${resolved === "dark" ? "-dark" : ""}.svg`;
    }
    const toggle = $("#theme-toggle");
    if (toggle) {
      const nextTheme = resolved === "dark" ? "light" : "dark";
      toggle.setAttribute("aria-label", `切换为${nextTheme === "dark" ? "深色" : "浅色"}主题`);
      toggle.querySelector(".theme-toggle-icon").textContent = resolved === "dark" ? "☀" : "☾";
      toggle.querySelector(".theme-toggle-label").textContent = resolved === "dark" ? "浅色" : "深色";
    }
    if (persist) {
      try { window.localStorage.setItem(THEME_STORAGE_KEY, resolved); } catch { /* ignore */ }
    }
    document.dispatchEvent(new CustomEvent("cpa-theme-change", { detail: { theme: resolved } }));
  };

  const escapeHTML = (value) => String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");

  const formatTime = (timestamp) => {
    if (!timestamp) return "—";
    return new Intl.DateTimeFormat("zh-CN", {
      month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false
    }).format(new Date(timestamp * 1000));
  };

  const formatClockTime = (timestamp) => {
    if (!timestamp) return "—";
    return new Intl.DateTimeFormat("zh-CN", {
      hour: "2-digit", minute: "2-digit", hour12: false
    }).format(new Date(timestamp * 1000));
  };

  const formatFullTime = (timestamp) => {
    const value = Number(timestamp);
    if (!Number.isFinite(value) || value <= 0) return "—";
    return new Intl.DateTimeFormat("zh-CN", {
      year: "numeric", month: "2-digit", day: "2-digit",
      hour: "2-digit", minute: "2-digit", hour12: false
    }).format(new Date(value * 1000));
  };

  const customUsageRangeLabel = (range) => {
    if (!range?.startAt || !range?.endAt) return "选择时间范围";
    const start = new Date(Number(range.startAt) * 1000);
    const end = new Date(Number(range.endAt) * 1000);
    const sameYear = start.getFullYear() === end.getFullYear();
    const sameDay = sameYear
      && start.getMonth() === end.getMonth()
      && start.getDate() === end.getDate();
    const dayPart = (date, includeYear = false) => [
      ...(includeYear ? [date.getFullYear()] : []),
      String(date.getMonth() + 1).padStart(2, "0"),
      String(date.getDate()).padStart(2, "0")
    ].join("/");
    const part = (date, includeYear = false) => `${dayPart(date, includeYear)} ${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
    if (sameDay) {
      return `${dayPart(start)} ${String(start.getHours()).padStart(2, "0")}:${String(start.getMinutes()).padStart(2, "0")}–${String(end.getHours()).padStart(2, "0")}:${String(end.getMinutes()).padStart(2, "0")}`;
    }
    return `${part(start, !sameYear)} → ${part(end, !sameYear)}`;
  };

  const fullCustomUsageRangeLabel = (range) => range?.startAt && range?.endAt
    ? `${formatFullTime(range.startAt)} → ${formatFullTime(range.endAt)}`
    : "选择时间范围";

  const usageRangeQuery = (windowValue, customRange, initial = {}) => {
    const query = new URLSearchParams({ ...initial, window: windowValue });
    if (windowValue === "custom" && customRange) {
      query.set("start_at", String(customRange.startAt));
      query.set("end_at", String(customRange.endAt));
    }
    return query;
  };

  const usageRangeTarget = (target) => {
    if (target === "overview") {
      return {
        label: "Token 趋势",
        windowKey: "overviewUsageWindow",
        rangeKey: "overviewUsageCustomRange",
        select: "#overview-usage-window"
      };
    }
    return target === "account" ? {
        label: "账号信息",
        windowKey: "accountUsageWindow",
        rangeKey: "accountCustomRange",
        select: "#account-usage-window"
      }
    : {
        label: "用户信息",
        windowKey: "userUsageWindow",
        rangeKey: "userCustomRange",
        select: "#user-usage-window"
      };
  };

  const renderCustomUsageRangeControl = (target) => {
    const config = usageRangeTarget(target);
    const active = state[config.windowKey] === "custom";
    const label = customUsageRangeLabel(state[config.rangeKey]);
    if (target === "overview") {
      const button = $('[data-overview-window="custom"]');
      if (!button) return;
      const value = button.querySelector("[data-overview-custom-range-label]");
      if (value) value.textContent = active ? label : "自定义";
      button.title = active
        ? `修改${config.label}统计范围：${fullCustomUsageRangeLabel(state[config.rangeKey])}`
        : "选择自定义时间范围";
      return;
    }
    const select = $(config.select);
    if (!select) return;
    if (active && select.value !== "custom") select.value = "custom";
    const customOption = select.querySelector('option[value="custom"]');
    if (customOption) customOption.textContent = active ? label : "自定义…";
    select.title = active ? fullCustomUsageRangeLabel(state[config.rangeKey]) : "";
    syncEnhancedSelect(select);
  };

  const customRangeDraft = {
    start: { date: null, month: null },
    end: { date: null, month: null }
  };

  const dateKey = (date) => [
    date.getFullYear(),
    String(date.getMonth() + 1).padStart(2, "0"),
    String(date.getDate()).padStart(2, "0")
  ].join("-");

  const parseTimeValue = (value) => {
    const match = /^(\d{2}):(\d{2})$/.exec(String(value || "").trim());
    if (!match) return null;
    const hour = Number(match[1]);
    const minute = Number(match[2]);
    return hour <= 23 && minute <= 59 ? { hour, minute } : null;
  };

  const boundaryTimestamp = (boundary) => {
    const date = customRangeDraft[boundary].date;
    const time = parseTimeValue($(`#custom-usage-${boundary}-time`).value);
    if (!date || !time) return NaN;
    return Math.floor(new Date(
      date.getFullYear(), date.getMonth(), date.getDate(), time.hour, time.minute
    ).getTime() / 1000);
  };

  const renderCustomRangePreview = () => {
    const startAt = boundaryTimestamp("start");
    const endAt = boundaryTimestamp("end");
    $("#custom-usage-range-preview").textContent = Number.isFinite(startAt) && Number.isFinite(endAt)
      ? fullCustomUsageRangeLabel({ startAt, endAt })
      : "请选择完整的日期和时间";
  };

  const renderCustomCalendar = (boundary) => {
    const draft = customRangeDraft[boundary];
    const calendar = $(`[data-custom-calendar="${boundary}"]`);
    if (!calendar || !draft.month) return;
    const year = draft.month.getFullYear();
    const month = draft.month.getMonth();
    calendar.querySelector("[data-calendar-month]").textContent = `${year} 年 ${month + 1} 月`;
    const firstWeekday = new Date(year, month, 1).getDay();
    const dayCount = new Date(year, month + 1, 0).getDate();
    const previousCount = new Date(year, month, 0).getDate();
    const today = new Date();
    const selectedKey = draft.date ? dateKey(draft.date) : "";
    const cells = Array.from({ length: 42 }, (_, index) => {
      const offset = index - firstWeekday + 1;
      const cellDate = offset < 1
        ? new Date(year, month - 1, previousCount + offset)
        : offset > dayCount
          ? new Date(year, month + 1, offset - dayCount)
          : new Date(year, month, offset);
      const outside = cellDate.getMonth() !== month;
      const key = dateKey(cellDate);
      const future = cellDate > new Date(today.getFullYear(), today.getMonth(), today.getDate());
      return `<button type="button" data-calendar-date="${key}" ${future ? "disabled" : ""} class="${outside ? "outside" : ""} ${key === selectedKey ? "selected" : ""} ${key === dateKey(today) ? "today" : ""}" aria-label="${cellDate.getFullYear()}年${cellDate.getMonth() + 1}月${cellDate.getDate()}日">${cellDate.getDate()}</button>`;
    });
    calendar.querySelector("[data-calendar-days]").innerHTML = cells.join("");
    const nextButton = calendar.querySelector(`[data-calendar-nav="${boundary}:1"]`);
    if (nextButton) {
      nextButton.disabled = year > today.getFullYear()
        || (year === today.getFullYear() && month >= today.getMonth());
    }
    const dateButton = $(`#custom-usage-${boundary}-date strong`);
    if (dateButton) dateButton.textContent = draft.date
      ? `${draft.date.getFullYear()}/${String(draft.date.getMonth() + 1).padStart(2, "0")}/${String(draft.date.getDate()).padStart(2, "0")}`
      : "选择日期";
  };

  const renderCustomCalendars = () => {
    renderCustomCalendar("start");
    renderCustomCalendar("end");
    renderCustomRangePreview();
  };

  const openCustomUsageRange = (target) => {
    const config = usageRangeTarget(target);
    const now = Math.floor(Date.now() / 60000) * 60;
    const range = state[config.rangeKey] || {
      startAt: now - 24 * 60 * 60,
      endAt: now
    };
    const start = new Date(range.startAt * 1000);
    const end = new Date(range.endAt * 1000);
    customRangeDraft.start.date = new Date(start.getFullYear(), start.getMonth(), start.getDate());
    customRangeDraft.start.month = new Date(start.getFullYear(), start.getMonth(), 1);
    customRangeDraft.end.date = new Date(end.getFullYear(), end.getMonth(), end.getDate());
    customRangeDraft.end.month = new Date(end.getFullYear(), end.getMonth(), 1);
    state.customUsageRangeTarget = target;
    $("#custom-usage-range-title").textContent = `${config.label}自定义统计范围`;
    $("#custom-usage-range-subtitle").textContent = "按本地时间选择历史用量的起止边界";
    $("#custom-usage-start-time").value = `${String(start.getHours()).padStart(2, "0")}:${String(start.getMinutes()).padStart(2, "0")}`;
    $("#custom-usage-end-time").value = `${String(end.getHours()).padStart(2, "0")}:${String(end.getMinutes()).padStart(2, "0")}`;
    $("#custom-usage-range-error").textContent = "";
    renderCustomCalendars();
    $("#custom-usage-range-dialog").showModal();
    $("#custom-usage-start-date").focus();
  };

  const clearUsageRangeCaches = (target) => {
    if (target === "overview") return;
    if (target === "account") {
      state.accountUsageBreakdowns.clear();
      state.accountUsageBreakdownLoading.clear();
      state.accountUsageBreakdownErrors.clear();
      return;
    }
    state.userUsageBreakdowns.clear();
    state.userUsageBreakdownLoading.clear();
    state.userUsageBreakdownErrors.clear();
    state.userDetails.clear();
    state.userDetailLoading.clear();
    state.userDetailErrors.clear();
  };

  const applyCustomUsageRange = async () => {
    const target = state.customUsageRangeTarget;
    if (!target) return;
    const startAt = boundaryTimestamp("start");
    const endAt = boundaryTimestamp("end");
    const error = $("#custom-usage-range-error");
    if (!Number.isFinite(startAt) || !Number.isFinite(endAt)) {
      error.textContent = "请选择有效的开始和结束时间";
      return;
    }
    if (startAt >= endAt) {
      error.textContent = "开始时间必须早于结束时间";
      return;
    }
    if (endAt > Math.floor(Date.now() / 1000)) {
      error.textContent = "结束时间不能晚于当前时间";
      return;
    }
    const config = usageRangeTarget(target);
    state[config.rangeKey] = { startAt, endAt };
    state[config.windowKey] = "custom";
    const windowControl = $(config.select);
    if (windowControl?.matches("select")) {
      windowControl.value = "custom";
      syncEnhancedSelect(windowControl);
    }
    clearUsageRangeCaches(target);
    $("#custom-usage-range-dialog").close();
    state.customUsageRangeTarget = "";
    renderCustomUsageRangeControl(target);
    if (target === "overview") {
      state.overviewUsage = null;
      await loadOverviewUsage(false);
    } else {
      await refreshAll(false);
    }
  };

  const enhancedSelects = new Map();

  const closeEnhancedSelects = (except = null) => {
    enhancedSelects.forEach((control) => {
      if (control === except) return;
      control.menu.hidden = true;
      control.trigger.setAttribute("aria-expanded", "false");
    });
  };

  const syncEnhancedSelect = (select) => {
    const control = enhancedSelects.get(select);
    if (!control) return;
    const options = [...select.options];
    const selected = options.find((option) => option.value === select.value) || options[0];
    control.value.textContent = selected?.textContent || "请选择";
    const fieldLabel = select.closest(".filter-field")?.querySelector(":scope > span")?.textContent?.trim() || "筛选条件";
    control.trigger.setAttribute("aria-label", `${fieldLabel}：${selected?.textContent || "请选择"}`);
    control.trigger.title = select.title || selected?.textContent || "";
    control.menu.innerHTML = options.map((option) => `
      <button type="button" role="option" data-select-value="${escapeHTML(option.value)}" aria-selected="${option.value === select.value}">
        <span class="enhanced-select-check" aria-hidden="true">${option.value === select.value ? "✓" : ""}</span>
        <span>${escapeHTML(option.textContent)}</span>
      </button>`).join("");
  };

  const enhanceSelect = (select) => {
    if (!select || enhancedSelects.has(select)) return;
    const wrapper = document.createElement("span");
    wrapper.className = "enhanced-select";
    const menuId = `${select.id}-menu`;
    const fieldLabel = select.closest(".filter-field")?.querySelector(":scope > span")?.textContent?.trim() || "筛选条件";
    wrapper.innerHTML = `<button class="enhanced-select-trigger" type="button" aria-label="${escapeHTML(fieldLabel)}" aria-haspopup="listbox" aria-expanded="false" aria-controls="${escapeHTML(menuId)}"><span data-enhanced-select-value></span><span class="enhanced-select-caret" aria-hidden="true">⌄</span></button><span class="enhanced-select-menu" id="${escapeHTML(menuId)}" role="listbox" aria-label="${escapeHTML(fieldLabel)}" hidden></span>`;
    select.insertAdjacentElement("afterend", wrapper);
    select.classList.add("enhanced-select-native");
    select.tabIndex = -1;
    select.setAttribute("aria-hidden", "true");
    const control = {
      select,
      wrapper,
      trigger: wrapper.querySelector(".enhanced-select-trigger"),
      value: wrapper.querySelector("[data-enhanced-select-value]"),
      menu: wrapper.querySelector(".enhanced-select-menu")
    };
    enhancedSelects.set(select, control);
    syncEnhancedSelect(select);
    control.trigger.addEventListener("click", () => {
      const opening = control.menu.hidden;
      closeEnhancedSelects(opening ? control : null);
      control.menu.hidden = !opening;
      control.trigger.setAttribute("aria-expanded", String(opening));
      if (opening) control.menu.querySelector('[aria-selected="true"]')?.focus();
    });
    control.menu.addEventListener("click", (event) => {
      const option = event.target.closest("[data-select-value]");
      if (!option) return;
      const previous = select.value;
      select.value = option.dataset.selectValue;
      control.menu.hidden = true;
      control.trigger.setAttribute("aria-expanded", "false");
      syncEnhancedSelect(select);
      control.trigger.focus();
      if (select.value !== previous || select.value === "custom") {
        select.dispatchEvent(new Event("change", { bubbles: true }));
      }
    });
    control.wrapper.addEventListener("keydown", (event) => {
      const options = [...control.menu.querySelectorAll("[data-select-value]")];
      if (event.key === "Escape") {
        control.menu.hidden = true;
        control.trigger.setAttribute("aria-expanded", "false");
        control.trigger.focus();
        return;
      }
      if (!["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      if (control.menu.hidden) {
        control.trigger.click();
        return;
      }
      const current = Math.max(0, options.indexOf(document.activeElement));
      const next = event.key === "Home" ? 0
        : event.key === "End" ? options.length - 1
          : Math.min(options.length - 1, Math.max(0, current + (event.key === "ArrowDown" ? 1 : -1)));
      options[next]?.focus();
    });
    control.wrapper.addEventListener("focusout", (event) => {
      if (control.wrapper.contains(event.relatedTarget)) return;
      control.menu.hidden = true;
      control.trigger.setAttribute("aria-expanded", "false");
    });
  };

  const enhanceSelects = (root = document) => {
    $$('[data-enhance-select]', root).forEach(enhanceSelect);
  };

  const enhanceFilterSelects = () => {
    enhanceSelects();
  };

  const formatNumber = (value) => new Intl.NumberFormat("zh-CN").format(Number(value) || 0);
  const tokenMagnitudeFormatter = new Intl.NumberFormat("zh-CN", {
    maximumFractionDigits: 2
  });
  const tokenReadableParts = (value, { allowZero = false } = {}) => {
    const raw = String(value ?? "").trim();
    if (!raw) return { state: "empty" };
    if (!/^\d+$/.test(raw)) return { state: "invalid" };
    const tokens = Number(raw);
    if (!Number.isSafeInteger(tokens) || tokens < 0 || (!allowZero && tokens === 0)) {
      return { state: "invalid" };
    }
    const formatted = TokenUsageFormatter.format(tokens);
    const compact = formatted.unit === "Token"
      ? formatted.label
      : `${formatted.amount} ${formatted.unit} Token`;
    let localized = "";
    if (tokens >= 1_000_000_000_000) {
      localized = `${tokenMagnitudeFormatter.format(tokens / 1_000_000_000_000)} 万亿 Token`;
    } else if (tokens >= 100_000_000) {
      localized = `${tokenMagnitudeFormatter.format(tokens / 100_000_000)} 亿 Token`;
    } else if (tokens >= 10_000) {
      localized = `${tokenMagnitudeFormatter.format(tokens / 10_000)} 万 Token`;
    }
    return {
      state: "ready",
      compact,
      localized,
      exact: formatted.label,
      compacted: formatted.compacted
    };
  };
  const tokenReadableText = (value) => {
    const details = tokenReadableParts(value, { allowZero: true });
    if (details.state !== "ready") return "—";
    return details.localized
      ? `${details.compact}（${details.localized}）`
      : details.compact;
  };
  const tokenInputPresentation = (value, emptyLabel = "请输入 Token 数量") => {
    const details = tokenReadableParts(value);
    if (details.state === "empty") {
      return {
        state: "empty",
        html: `<small>${escapeHTML(emptyLabel)}</small>`
      };
    }
    if (details.state === "invalid") {
      return {
        state: "invalid",
        html: "<small>请输入正整数 Token</small>"
      };
    }
    return {
      state: "ready",
      html: `<strong>${escapeHTML(details.compact)}</strong>
        ${details.localized ? `<span>${escapeHTML(details.localized)}</span>` : ""}
        ${details.compacted ? `<small>精确值 ${escapeHTML(details.exact)}</small>` : ""}`
    };
  };
  const updateTokenInputPreview = (input) => {
    if (!input) return;
    const preview = input.closest(".token-input-control")?.querySelector("[data-token-input-preview]");
    if (!preview) return;
    const presentation = tokenInputPresentation(
      input.value,
      input.dataset.tokenEmptyLabel || "请输入 Token 数量"
    );
    preview.dataset.state = presentation.state;
    preview.innerHTML = presentation.html;
  };
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
  const sortableTableHeader = ({ attribute, field, label, sortState, className = "" }) => {
    const active = sortState.field === field;
    const direction = active ? sortState.direction : "";
    const ariaSort = active ? (direction === "asc" ? "ascending" : "descending") : "none";
    const ariaLabel = active
      ? `${label}，当前${direction === "asc" ? "升序" : "降序"}，点击切换排序方向`
      : `${label}，点击排序`;
    return `<th${className ? ` class="${className}"` : ""} aria-sort="${ariaSort}"><button class="sort-button${active ? " active" : ""}" type="button" ${attribute}="${field}"${active ? ` data-direction="${direction}"` : ""} aria-label="${escapeHTML(ariaLabel)}">${escapeHTML(label)}</button></th>`;
  };
  const renderTokenUsage = (value) => TokenUsageFormatter.render(value);
  const userUsageWindowLabel = () => ({
    "3600": "1 小时",
    today: "今日",
    "86400": "24 小时",
    "604800": "7 天",
    "2592000": "30 天",
    all: "累计",
    custom: "自定义范围"
  })[state.userUsageWindow] || "当前范围";
  const monitorSortDefaultDirection = (field) => (
    ["name", "status"].includes(field) ? "asc" : "desc"
  );
  const monitorSeriesStatusRank = (item, kind) => {
    const tone = monitorSeriesStatus(item.name, kind).tone;
    return ({ success: 0, warning: 1, danger: 2, neutral: 3 }[tone] ?? 9);
  };
  const formatLastUsed = (timestamp) => timestamp ? formatTime(timestamp) : "从未使用";
  const formatUsagePercent = (count, total) => {
    const denominator = Number(total) || 0;
    if (denominator <= 0) return "0%";
    return `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format((Number(count) || 0) * 100 / denominator)}%`;
  };
  const usageCombinationLabel = (item) => {
    const model = item?.model === "unknown" ? "未上报模型" : (item?.model || "未上报模型");
    const effort = item?.reasoning_effort === "unknown" ? "未上报" : (item?.reasoning_effort || "未上报");
    return `${model}-${effort}`;
  };
  const paginationItems = (current, total) => {
    if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1);
    const pages = new Set([1, total, current - 1, current, current + 1]);
    if (current <= 3) pages.add(2).add(3).add(4);
    if (current >= total - 2) pages.add(total - 1).add(total - 2).add(total - 3);
    const ordered = [...pages].filter((page) => page > 0 && page <= total).sort((a, b) => a - b);
    return ordered.flatMap((page, index) => index && page - ordered[index - 1] > 1 ? ["…", page] : [page]);
  };

  const statusClass = (status) => {
    if (["active", "configured", "running", "succeeded"].includes(status)) return "success";
    if (["pending", "queued", "cancelling", "running-job", "restarting"].includes(status)) return "warning";
    if (["failed", "exited", "dead"].includes(status)) return "danger";
    return "neutral";
  };

  const statusLabel = (status) => ({
    active: "启用", inactive: "已停用", configured: "已授权", pending: "待授权",
    running: "运行中", exited: "已停止", missing: "未创建", rotated: "已轮换",
    revoked: "已吊销", succeeded: "成功", failed: "失败", queued: "排队中",
    cancelling: "取消中", cancelled: "已取消"
  }[status] || status || "未知");

  const showToast = (message, type = "success") => {
    const toast = document.createElement("div");
    toast.className = `toast ${type === "error" ? "error" : ""}`;
    toast.textContent = message;
    $("#toast-region").append(toast);
    window.setTimeout(() => toast.remove(), 4200);
  };

  const legacyCopyText = (value) => {
    const area = document.createElement("textarea");
    area.value = value;
    area.setAttribute("readonly", "");
    area.setAttribute("aria-hidden", "true");
    area.tabIndex = -1;
    area.style.position = "fixed";
    area.style.left = "-9999px";
    area.style.top = "0";
    // HTTP 内网环境会使用 execCommand 回退。模态 dialog 打开时，body 下的
    // 临时输入框处于 inert 区域，无法获得选区，因此必须挂到当前 dialog 内。
    const copyHost = document.querySelector("dialog[open]") || document.body;
    copyHost.append(area);
    let copied = false;
    try {
      area.focus();
      area.select();
      area.setSelectionRange(0, area.value.length);
      copied = Boolean(document.execCommand("copy"));
    } catch {
      copied = false;
    } finally {
      area.remove();
    }
    return copied;
  };

  const copyText = async (value) => {
    const text = String(value ?? "");
    if (!text) {
      showToast("暂无可复制内容", "error");
      return false;
    }
    let copied = false;
    if (!window.isSecureContext || !navigator.clipboard?.writeText) {
      copied = legacyCopyText(text);
    } else {
      try {
        await navigator.clipboard.writeText(text);
        copied = true;
      } catch {
        copied = legacyCopyText(text);
      }
    }
    showToast(copied ? "已复制到剪贴板" : "浏览器拒绝复制，请手动选择文本", copied ? "success" : "error");
    return copied;
  };

  const selectionInside = (element) => {
    const selection = window.getSelection();
    if (!selection || selection.isCollapsed) return false;
    return Boolean(
      (selection.anchorNode && element.contains(selection.anchorNode))
      || (selection.focusNode && element.contains(selection.focusNode))
    );
  };

  const setOutputText = (value, force = false) => {
    const content = $("#output-content");
    const next = String(value || "");
    if (content.textContent === next) return true;
    if (!force && selectionInside(content)) return false;
    const previousTop = content.scrollTop;
    const nearBottom = content.scrollHeight - content.scrollTop - content.clientHeight < 40;
    content.textContent = next;
    content.scrollTop = nearBottom ? content.scrollHeight : previousTop;
    return true;
  };

  const updateOAuthCopyPanel = (output = "") => {
    const url = output.match(/^Codex device URL:\s*(\S+)/m)?.[1] || "";
    const code = output.match(/^Codex device code:\s*(\S+)/m)?.[1] || "";
    let safeUrl = "";
    if (url) {
      try {
        const parsed = new URL(url);
        if (parsed.protocol === "https:") safeUrl = parsed.href;
      } catch { safeUrl = ""; }
    }
    state.oauthUrl = safeUrl;
    state.oauthCode = code;
    $("#oauth-copy-panel").hidden = !(safeUrl || code);
    const urlValue = safeUrl || "—";
    const codeValue = code || "—";
    if ($("#oauth-url-value").textContent !== urlValue) $("#oauth-url-value").textContent = urlValue;
    if ($("#oauth-code-value").textContent !== codeValue) $("#oauth-code-value").textContent = codeValue;
    const link = $("#oauth-open-link");
    link.hidden = !safeUrl;
    link.href = safeUrl || "#";
  };

  const updateScrollableView = (view) => {
    const usersActive = view === "users";
    const accountsActive = view === "accounts";
    [document.documentElement, document.body].forEach((element) => {
      element.classList.toggle("users-view-active", usersActive);
      element.classList.toggle("accounts-view-active", accountsActive);
    });
    if (usersActive || accountsActive) window.scrollTo(0, 0);
  };

  const resetToAuth = (message = "") => {
    state.key = "";
    state.authenticated = false;
    state.csrfToken = "";
    state.selectedUsers.clear();
    state.selectedUserQuota = null;
    state.quotaAction = null;
    state.accountUsageBreakdowns.clear();
    state.accountUsageBreakdownLoading.clear();
    state.accountUsageBreakdownErrors.clear();
    state.userUsageBreakdowns.clear();
    state.userUsageBreakdownLoading.clear();
    state.userUsageBreakdownErrors.clear();
    state.userUsageAccountFilters.clear();
    state.userDetails.clear();
    state.userDetailLoading.clear();
    state.userDetailErrors.clear();
    state.overviewCatalog = null;
    state.overviewCatalogLoading = false;
    state.overviewCatalogError = "";
    state.overviewCatalogRequestId += 1;
    state.overviewUsage = null;
    state.overviewUsageRequestId += 1;
    state.viewLoadedAt.clear();
    state.viewRequestIds.clear();
    updateScrollableView("");
    $("#app-shell").hidden = true;
    $("#auth-screen").hidden = false;
    $("#auth-error").textContent = message;
    $("#management-key").value = "";
    $("#management-key").focus();
    window.clearTimeout(state.refreshTimer);
    window.clearTimeout(state.overviewUsageTimer);
  };

  const invalidateViews = (views = []) => {
    const affected = AdminViewStateUtils.uniqueViews(views);
    affected.forEach((view) => state.viewLoadedAt.delete(view));
    if (affected.includes("overview")) {
      state.overviewCatalogRequestId += 1;
      state.overviewCatalog = null;
      state.overviewCatalogLoading = false;
      state.overviewCatalogError = "";
      state.overviewUsageRequestId += 1;
      state.overviewUsage = null;
      state.overviewUsageLoading = false;
      state.overviewUsageError = "";
    }
  };

  const api = async (path, options = {}) => {
    const { skipAuthReset = false, ...requestOptions } = options;
    const headers = { ...(requestOptions.headers || {}) };
    if (state.key) headers["X-Management-Key"] = state.key;
    if (state.csrfToken && !["GET", "HEAD"].includes(requestOptions.method || "GET")) {
      headers["X-CSRF-Token"] = state.csrfToken;
    }
    if (requestOptions.body && !headers["Content-Type"]) headers["Content-Type"] = "application/json";
    const response = await fetch(`/admin/api${path}`, {
      ...requestOptions,
      headers,
      cache: "no-store"
    });
    let payload = {};
    try { payload = await response.json(); } catch { payload = {}; }
    if (response.status === 401) {
      if (!skipAuthReset) resetToAuth("管理会话已失效，请重新输入管理密钥");
      throw new Error("管理密钥无效");
    }
    if (!response.ok) throw new Error(payload.error?.message || `HTTP ${response.status}`);
    invalidateViews(AdminViewStateUtils.mutationAffectedViews(
      path,
      requestOptions.method || "GET"
    ));
    return payload;
  };

  const overviewUsagePath = (fresh = false) => {
    const query = usageRangeQuery(
      state.overviewUsageWindow,
      state.overviewUsageCustomRange,
      {
      user_limit: state.overviewUsageUserLimit
      }
    );
    state.overviewUsageAccounts.forEach((account) => query.append("account", account));
    state.overviewUsageUsers.forEach((user) => query.append("user", user));
    if (fresh) query.set("fresh", "1");
    return `/overview/usage?${query.toString()}`;
  };

  const renderReleaseStatus = () => {
    const status = state.releaseStatus;
    const version = $("#release-version");
    const notice = $("#release-notice");
    version.hidden = !status?.current_version;
    version.textContent = status?.current_version ? `当前版本 ${status.current_version}` : "";
    notice.hidden = !status?.available;
    if (!status?.available) return;
    $("#release-notice-title").textContent = `发现新版本 ${status.latest_version}`;
    $("#release-notice-copy").textContent = `当前为 ${status.current_version}。请在部署环境执行镜像拉取部署，运行数据不会被镜像覆盖。`;
  };

  const loadReleaseStatus = async (fresh = false) => {
    const button = $("#release-check-button");
    if (fresh) button.disabled = true;
    try {
      state.releaseStatus = await api(`/release${fresh ? "?fresh=1" : ""}`);
      renderReleaseStatus();
      if (fresh) showToast(state.releaseStatus.available ? "检测到可用新版本" : "当前已是最新版本");
    } catch (error) {
      if (fresh) showToast(error.message, "error");
    } finally {
      if (fresh) button.disabled = false;
    }
  };

  window.setInterval(() => {
    if (!$("#app-shell").hidden) loadReleaseStatus(false);
  }, RELEASE_POLL_INTERVAL_MS);

  const enterApp = async (createSession = false) => {
    const session = await api(
      "/session",
      createSession ? { method: "POST" } : { skipAuthReset: true }
    );
    state.csrfToken = session.csrf_token || "";
    state.authenticated = true;
    state.key = "";
    $("#management-key").value = "";
    $("#auth-screen").hidden = true;
    $("#app-shell").hidden = false;
    updateScrollableView(state.view);
    loadReleaseStatus(false);
    await refreshView(state.view, true);
    if (new URLSearchParams(window.location.search).get("action") === "add-account") {
      openAddAccount();
      window.history.replaceState({}, "", "/admin/");
    }
  };

  const nextViewRequestId = (view) => {
    const requestId = (state.viewRequestIds.get(view) || 0) + 1;
    state.viewRequestIds.set(view, requestId);
    return requestId;
  };

  const userSummaryPath = () => {
    const query = usageRangeQuery(
      state.userUsageWindow,
      state.userCustomRange,
      {
        view: "summary",
        page: String(state.userPage),
        page_size: String(state.userPageSize),
        q: $("#user-search").value.trim(),
        sort: state.userSort.field,
        direction: state.userSort.direction
      }
    );
    if (state.userTeamFilter) query.set("team_id", state.userTeamFilter);
    return `/users?${query.toString()}`;
  };

  const organizationTeamScope = () => $("#organization-user-scope").value;
  const organizationTeamQuery = (page = state.organizationPage, pageSize = 50) => {
    const query = new URLSearchParams({
      view: "summary",
      window: $("#organization-usage-window").value,
      page: String(page),
      page_size: String(pageSize),
      q: $("#organization-user-search").value.trim(),
      sort: "tokens",
      direction: "desc",
      usage_state: $("#organization-usage-state").value
    });
    const scope = organizationTeamScope();
    if (scope === "unassigned") query.set("team_id", "unassigned");
    if (scope === "current" && state.organizationTeamId) query.set("team_id", state.organizationTeamId);
    return query;
  };

  const scheduleViewRefresh = (quotaRefreshing = false) => {
    window.clearTimeout(state.refreshTimer);
    if (!state.authenticated || document.hidden) return;
    state.refreshTimer = window.setTimeout(
      () => refreshView(state.view, false),
      quotaRefreshing && state.view === "accounts" ? 5000 : 30000
    );
  };

  const refreshView = async (view = state.view, showFeedback = false) => {
    $("#refresh-state").textContent = "正在刷新";
    let quotaRefreshing = false;
    const requestId = nextViewRequestId(view);
    try {
      if (view === "overview") {
        loadOverviewCatalog(showFeedback);
        const overview = await api(`/overview${showFeedback ? "?fresh=1" : ""}`);
        if (state.viewRequestIds.get(view) !== requestId) return;
        state.overview = overview;
        renderOverview();
        if (showFeedback || !state.overviewUsage) loadOverviewUsage(showFeedback);
        $("#refresh-state").textContent = `总览更新于 ${formatTime(overview.generated_at)}`;
      } else if (view === "accounts") {
        const accountQuery = usageRangeQuery(
          state.accountUsageWindow,
          state.accountCustomRange
        );
        if (showFeedback) accountQuery.set("fresh", "1");
        const imageRequest = api("/images/cliproxy").catch(() => null);
        const accounts = await api(`/accounts?${accountQuery.toString()}`);
        if (state.viewRequestIds.get(view) !== requestId) return;
        state.accounts = accounts.accounts || [];
        state.accountCollector = accounts.collector || null;
        quotaRefreshing = Boolean(accounts.quota_refreshing);
        renderAccounts();
        imageRequest.then((imageStatus) => {
          if (!imageStatus || state.viewRequestIds.get(view) !== requestId) return;
          state.imageStatus = imageStatus;
          renderImageManager();
          if (state.view === "accounts") renderAccounts();
        });
        if (state.expandedAccount) loadAccountUsageBreakdown(state.expandedAccount, showFeedback);
        const quotaState = quotaRefreshing ? "（后台更新中）" : accounts.quota_cached ? "（缓存）" : "";
        $("#refresh-state").textContent = `额度更新于 ${formatTime(accounts.quota_generated_at || accounts.generated_at)}${quotaState}`;
      } else if (view === "users") {
        state.teamUsageLoading = true;
        state.teamUsageError = "";
        renderTeamUsageTrigger();
        const teamUsageQuery = usageRangeQuery(
          state.userUsageWindow,
          state.userCustomRange
        );
        const [users, teamUsageResult] = await Promise.all([
          api(userSummaryPath()),
          api(`/teams/usage?${teamUsageQuery.toString()}`)
            .then((payload) => ({ payload }))
            .catch((error) => ({ error }))
        ]);
        if (state.viewRequestIds.get(view) !== requestId) return;
        state.users = users.users || [];
        state.teams = users.teams || [];
        state.userCollector = users.collector || null;
        state.teamUsageLoading = false;
        if (teamUsageResult.error) {
          state.teamUsageError = teamUsageResult.error.message;
        } else {
          state.teamUsage = teamUsageResult.payload.teams || [];
        }
        state.userPagination = users.pagination || {
          page: state.userPage,
          page_size: state.userPageSize,
          total: state.users.length,
          total_pages: 1
        };
        state.userPage = state.userPagination.page;
        renderOrganizationFilters();
        renderTeamUsageTrigger();
        renderUsers();
        if (state.expandedUser) {
          loadUserDetail(state.expandedUser, true);
          loadUserUsageBreakdown(state.expandedUser);
        }
        $("#refresh-state").textContent = `用户数据更新于 ${formatTime(users.summary_generated_at || users.generated_at)}${users.summary_cached ? "（缓存）" : ""}`;
      } else if (view === "organization") {
        await loadOrganizationWorkspace();
        if (state.viewRequestIds.get(view) !== requestId) return;
        $("#refresh-state").textContent = "团队数据已刷新";
      } else if (view === "operations") {
        const overview = await api(`/overview${showFeedback ? "?fresh=1" : ""}`);
        if (state.viewRequestIds.get(view) !== requestId) return;
        state.overview = overview;
        renderOperations();
        loadJobs();
        $("#refresh-state").textContent = `运行状态更新于 ${formatTime(overview.generated_at)}`;
      } else if (view === "settings") {
        const settings = await api("/settings");
        if (state.viewRequestIds.get(view) !== requestId) return;
        state.settings = settings;
        renderSettings();
        $("#refresh-state").textContent = "配置已刷新";
      }
      state.viewLoadedAt.set(view, Date.now());
      if (showFeedback && view !== "overview") {
        showToast(quotaRefreshing ? "表格已刷新，额度正在后台更新" : "数据已刷新");
      }
    } catch (error) {
      $("#refresh-state").textContent = "刷新失败";
      if (state.authenticated) showToast(error.message, "error");
    } finally {
      scheduleViewRefresh(quotaRefreshing);
    }
  };

  const refreshAll = async (showFeedback = false) => refreshView(state.view, showFeedback);

  const loadOverviewCatalog = async (fresh = false) => {
    const requestId = ++state.overviewCatalogRequestId;
    state.overviewCatalogLoading = true;
    state.overviewCatalogError = "";
    if (state.view === "overview") renderOverviewUsageFilters();
    try {
      const payload = await api(`/overview/catalog${fresh ? "?fresh=1" : ""}`);
      if (requestId !== state.overviewCatalogRequestId) return;
      state.overviewCatalog = payload;
      if (reconcileOverviewUsageSelections() && state.view === "overview") {
        state.overviewUsage = null;
        loadOverviewUsage(false);
      }
    } catch (error) {
      if (requestId !== state.overviewCatalogRequestId) return;
      state.overviewCatalogError = error.message;
    } finally {
      if (requestId === state.overviewCatalogRequestId) {
        state.overviewCatalogLoading = false;
        if (state.view === "overview") renderOverviewUsage();
      }
    }
  };

  const scheduleOverviewUsageRefresh = () => {
    window.clearTimeout(state.overviewUsageTimer);
    const seconds = Number(state.overviewUsageRefresh);
    if (!state.authenticated || !seconds || state.view !== "overview" || document.hidden) return;
    state.overviewUsageTimer = window.setTimeout(() => loadOverviewUsage(false), seconds * 1000);
  };

  const loadOverviewUsage = async (showFeedback = false) => {
    const requestId = ++state.overviewUsageRequestId;
    const usagePath = overviewUsagePath(showFeedback);
    state.overviewUsageLoading = true;
    state.overviewUsageError = "";
    renderOverviewUsage();
    try {
      const payload = await api(usagePath);
      if (requestId !== state.overviewUsageRequestId) return;
      state.overviewUsage = payload;
      if (showFeedback) showToast("Token 趋势已刷新");
    } catch (error) {
      if (requestId !== state.overviewUsageRequestId) return;
      state.overviewUsageError = error.message;
      if (showFeedback) showToast(error.message, "error");
    } finally {
      if (requestId === state.overviewUsageRequestId) {
        state.overviewUsageLoading = false;
        renderOverviewUsage();
        scheduleOverviewUsageRefresh();
      }
    }
  };

  const VIEW_HEADING_META = {
    overview: ["运行总览", "OPERATIONS OVERVIEW"],
    accounts: ["账号管理", "ACCOUNT MANAGEMENT"],
    users: ["用户管理", "USER MANAGEMENT"],
    organization: ["团队管理", "TEAM MANAGEMENT"],
    operations: ["运行维护", "RUNTIME OPERATIONS"],
    settings: ["系统设置", "CONTROL PLANE SETTINGS"]
  };

  const CONFIGURATION_HEADING_META = {
    "品牌与身份": "BRAND & IDENTITY",
    "CPA 请求": "CPA REQUESTS",
    "用量与额度": "USAGE & QUOTAS",
    "账号自动切换": "ACCOUNT FAILOVER",
    "用户额度": "USER QUOTAS",
    "推理强度策略": "REASONING EFFORT",
    "企业微信通知": "WECOM NOTIFICATIONS",
    "会话与采集": "SESSIONS & COLLECTION",
    "账号供应": "ACCOUNT PROVISIONING",
    "部署环境": "DEPLOYMENT ENVIRONMENT",
    "系统约束": "SYSTEM CONSTRAINTS"
  };

  const SETTINGS_SECTION_HEADING_META = {
    access: ["访问凭据", "ACCESS CONTROL"],
    backups: ["安全归档", "RECOVERY"],
    storage: ["本地数据", "LOCAL STORAGE"],
    audit: ["审计记录", "AUDIT TRAIL"]
  };

  const settingsHeadingDetail = () => {
    if (state.settingsSection !== "configuration") {
      return SETTINGS_SECTION_HEADING_META[state.settingsSection] || ["", ""];
    }
    const group = state.configurationGroup || configurationGroups()[0]?.name || "";
    return [group, CONFIGURATION_HEADING_META[group] || "CONFIGURATION GROUP"];
  };

  const updatePageHeading = () => {
    const [title, eyebrow] = VIEW_HEADING_META[state.view] || VIEW_HEADING_META.overview;
    const [detailTitle, detailEyebrow] = state.view === "settings" ? settingsHeadingDetail() : ["", ""];
    $("#page-title-base").textContent = title;
    $("#page-eyebrow-base").textContent = eyebrow;
    $("#page-title-detail").textContent = detailTitle;
    $("#page-eyebrow-detail").textContent = detailEyebrow;
    $("#page-title-path").hidden = !detailTitle;
    $("#page-eyebrow-path").hidden = !detailEyebrow;
  };

  const setView = (view) => {
    if (!$( `[data-view-panel="${view}"]` )) return;
    state.view = view;
    updateScrollableView(view);
    $$("[data-view-panel]").forEach((panel) => panel.classList.toggle("active", panel.dataset.viewPanel === view));
    $$(".nav-item").forEach((item) => item.classList.toggle("active", item.dataset.view === view));
    updatePageHeading();
    const loadedAt = state.viewLoadedAt.get(view) || 0;
    if (state.authenticated && Date.now() - loadedAt > 15000) refreshView(view, false);
    else scheduleViewRefresh(false);
    if (view === "overview") scheduleOverviewUsageRefresh();
    else window.clearTimeout(state.overviewUsageTimer);
  };

  const accountAvailabilityStatus = (account = {}) => {
    const operational = account.operational_status;
    if (operational && typeof operational === "object") {
      return {
        value: String(operational.code || "unknown"),
        label: String(operational.label || "状态未知"),
        tone: String(operational.tone || "neutral"),
        reason: String(operational.reason || "账号状态暂不可确认"),
        selectable: operational.selectable !== false
      };
    }
    const quota = account.quota || {};
    const weekly = quota.weekly;
    const runtime = account.runtime || {};
    if (account.group_enabled === false) {
      return { value: "unavailable", label: "不可用", tone: "danger", reason: "账号已停用" };
    }
    if (account.container_state !== "running") {
      return { value: "unavailable", label: "不可用", tone: "danger", reason: "容器未运行" };
    }
    if (account.auth_state !== "configured") {
      return { value: "unavailable", label: "不可用", tone: "danger", reason: "OAuth 未授权" };
    }
    if (runtime.state === "unavailable" && globalThis.MonitorUtils.runtimeUnavailableDueToQuota(runtime)) {
      return { value: "quota_exhausted", label: "额度耗尽", tone: "danger", reason: "账号周额度已耗尽", selectable: false };
    }
    if (runtime.state === "unavailable") {
      return { value: "unavailable", label: "凭据不可用", tone: "danger", reason: runtime.status_message || "CPA 原生接口报告凭据不可用" };
    }
    if (runtime.state === "rate_limited") {
      return { value: "rate_limited", label: "限流中", tone: "warning", reason: `近 1 小时发生 ${Number(runtime.rate_429_count) || 0} 次 429`, selectable: true };
    }
    if (runtime.state === "degraded") {
      return { value: "warning", label: "近期异常", tone: "warning", reason: `近 1 小时发生 ${Number(runtime.error_count) || 0} 次错误` };
    }
    if (quota.allowed === false || quota.limit_reached === true) {
      return { value: "unavailable", label: "不可用", tone: "danger", reason: "账号额度已耗尽" };
    }
    if (quota.status !== "ok" || !weekly) {
      return { value: "degraded", label: "额度未知", tone: "neutral", reason: "额度状态暂不可确认" };
    }
    const remaining = Number(weekly.remaining_percent);
    if (Number.isFinite(remaining) && remaining <= 10) {
      return { value: "warning", label: "注意额度", tone: "warning", reason: "周额度剩余不高于 10%" };
    }
    return { value: "available", label: "可用", tone: "success", reason: "容器、OAuth 与额度均正常" };
  };

  const monitorWindowLabel = () => ({
    "3600": "最近 1 小时",
    "21600": "最近 6 小时",
    today: "今日",
    "86400": "最近 24 小时",
    "604800": "最近 7 天",
    "2592000": "最近 30 天",
    since_reset: "本周期",
    custom: customUsageRangeLabel(state.overviewUsageCustomRange)
  })[state.overviewUsageWindow] || "当前范围";

  const monitorWindowUsesDateLabels = () => (
    state.overviewUsageWindow === "since_reset"
    || state.overviewUsageWindow === "custom"
    || Number(state.overviewUsageWindow) > 86400
  );

  const monitorIntervalLabel = (seconds) => ({
    60: "1 分钟",
    300: "5 分钟",
    900: "15 分钟",
    3600: "1 小时",
    21600: "6 小时"
  })[Number(seconds)] || `${formatNumber(seconds)} 秒`;

  const monitorTokenLabel = (value) => {
    const formatted = TokenUsageFormatter.format(value);
    return `${formatted.amount} ${formatted.unit}`;
  };

  const monitorTimeLabel = (timestamp, includeDate = true) => {
    const options = includeDate
      ? { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }
      : { hour: "2-digit", minute: "2-digit", hour12: false };
    return new Intl.DateTimeFormat("zh-CN", options).format(new Date(Number(timestamp) * 1000));
  };

  const monitorVariableConfig = {
    account: {
      stateKey: "overviewUsageAccounts",
      label: "CPA",
      allLabel: "全部 CPA",
      searchPlaceholder: "搜索 CPA",
      options: () => AdminViewStateUtils.catalogOptions(state.overviewCatalog, "account")
    },
    user: {
      stateKey: "overviewUsageUsers",
      label: "用户",
      allLabel: "全部用户",
      searchPlaceholder: "搜索用户邮箱",
      options: () => AdminViewStateUtils.catalogOptions(state.overviewCatalog, "user")
    }
  };

  const reconcileOverviewUsageSelections = () => {
    if (!state.overviewCatalog) return false;
    let changed = false;
    Object.values(monitorVariableConfig).forEach((config) => {
      const available = new Set(config.options().map((option) => option.value));
      const selected = state[config.stateKey].filter((value) => available.has(value));
      if (selected.length !== state[config.stateKey].length) changed = true;
      state[config.stateKey] = selected;
    });
    return changed;
  };

  const monitorVariableSummary = (config, selected) => {
    if (!selected.length) return config.allLabel;
    if (selected.length <= 2) return selected.join("、");
    return `${selected.length} 个已选`;
  };

  const renderMonitorVariable = (kind) => {
    const config = monitorVariableConfig[kind];
    const root = $(`#overview-usage-${kind}`);
    if (!config || !root) return;
    const options = config.options();
    if (state.overviewCatalog) {
      const available = new Set(options.map((option) => option.value));
      state[config.stateKey] = state[config.stateKey].filter((value) => available.has(value));
    }
    const selected = state[config.stateKey];
    const value = root.querySelector("[data-variable-value]");
    const all = root.querySelector("[data-variable-all]");
    const optionsContainer = root.querySelector("[data-variable-options]");
    const search = root.querySelector("[data-variable-search]");
    const matchingOptions = options.filter((option) => (
      MonitorUtils.matchesSearchQuery(option.label, search?.value)
    ));
    if (value) value.textContent = monitorVariableSummary(config, selected);
    if (all) all.checked = selected.length === 0;
    if (optionsContainer) {
      const emptyMessage = !state.overviewCatalog
        ? state.overviewCatalogLoading
          ? "正在加载选项…"
          : state.overviewCatalogError
            ? "选项目录加载失败"
            : "选项目录尚未加载"
        : options.length
          ? "未找到匹配项"
          : `暂无${config.label}可选项`;
      optionsContainer.innerHTML = matchingOptions.length
        ? matchingOptions.map((option) => `
          <label class="usage-variable-option" data-variable-option>
            <input type="checkbox" data-variable-value-option value="${escapeHTML(option.value)}" ${selected.includes(option.value) ? "checked" : ""}>
            <span>${escapeHTML(option.label)}</span>
          </label>`).join("")
        : `<span class="usage-variable-empty">${escapeHTML(emptyMessage)}</span>`;
    }
    if (search) {
      search.placeholder = config.searchPlaceholder;
    }
  };

  const renderOverviewUsageFilters = () => {
    renderMonitorVariable("account");
    renderMonitorVariable("user");
    $$('[data-overview-window]', $("#overview-usage-window")).forEach((button) => {
      const active = button.dataset.overviewWindow === state.overviewUsageWindow;
      button.setAttribute("aria-pressed", String(active));
    });
    renderCustomUsageRangeControl("overview");
    $("#overview-usage-user-limit").value = state.overviewUsageUserLimit;
    $("#overview-usage-refresh-interval").value = state.overviewUsageRefresh;
  };

  const closeMonitorVariableMenus = (except = null) => {
    $$("[data-variable-menu]").forEach((menu) => {
      const root = menu.closest(".usage-variable");
      if (root === except) return;
      menu.hidden = true;
      root?.querySelector("[data-variable-trigger]")?.setAttribute("aria-expanded", "false");
    });
  };

  const commitMonitorVariable = (kind, value, checked) => {
    const config = monitorVariableConfig[kind];
    if (!config) return;
    if (!value) {
      state[config.stateKey] = [];
    } else {
      const selected = new Set(state[config.stateKey]);
      if (checked) selected.add(value);
      else selected.delete(value);
      state[config.stateKey] = [...selected];
    }
    state.overviewUsage = null;
    loadOverviewUsage(false);
  };

  const bindMonitorVariableControls = () => {
    Object.keys(monitorVariableConfig).forEach((kind) => {
      const root = $(`#overview-usage-${kind}`);
      if (!root) return;
      root.addEventListener("click", (event) => {
        const trigger = event.target.closest("[data-variable-trigger]");
        if (trigger) {
          const menu = root.querySelector("[data-variable-menu]");
          const opening = menu.hidden;
          closeMonitorVariableMenus(opening ? root : null);
          menu.hidden = !opening;
          trigger.setAttribute("aria-expanded", String(opening));
          if (opening) root.querySelector("[data-variable-search]")?.focus();
        }
      });
      root.addEventListener("change", (event) => {
        if (event.target.matches("[data-variable-all]")) {
          commitMonitorVariable(kind, "", false);
          return;
        }
        if (event.target.matches("[data-variable-value-option]")) {
          commitMonitorVariable(kind, event.target.value, event.target.checked);
        }
      });
      root.addEventListener("input", (event) => {
        if (!event.target.matches("[data-variable-search]")) return;
        renderMonitorVariable(kind);
      });
    });
    document.addEventListener("click", (event) => {
      if (!event.target.closest(".usage-variable")) closeMonitorVariableMenus();
    });
  };

  const monitorSeriesStatus = (name, kind) => {
    return AdminViewStateUtils.monitorSeriesStatus(
      state.overviewCatalog,
      name,
      kind
    );
  };

  const renderMonitorChart = (container, buckets, series, kind, options = {}) => {
    const hasUsage = series.some((item) => Number(item.total) > 0);
    if (!series.length || !buckets.length || !hasUsage) {
      container.innerHTML = `<div class="usage-monitor-empty"><strong>当前范围暂无 Token 用量</strong><span>调整时间范围或过滤条件后重试。</span></div>`;
      return;
    }

    const summary = options.variant === "summary";
    const width = summary
      ? Math.max(320, Math.round(container.getBoundingClientRect().width) || 1000)
      : 1000;
    const height = summary ? 260 : 300;
    const plot = summary
      ? { left: width <= 520 ? 58 : 64, right: 16, top: 24, bottom: 44 }
      : { left: 72, right: 20, top: 24, bottom: 44 };
    const plotWidth = width - plot.left - plot.right;
    const plotHeight = height - plot.top - plot.bottom;
    const recordedMaximum = Math.max(1, ...series.flatMap((item) => item.values.map((value) => Number(value) || 0)));
    const maximum = summary ? recordedMaximum * 1.08 : recordedMaximum;
    const xAt = (index) => plot.left + (buckets.length === 1 ? plotWidth / 2 : index * plotWidth / (buckets.length - 1));
    const yAt = (value) => plot.top + plotHeight - (Number(value) || 0) * plotHeight / maximum;

    const grid = Array.from({ length: 5 }, (_, index) => {
      const value = maximum * (4 - index) / 4;
      const y = plot.top + index * plotHeight / 4;
      return `<line class="usage-monitor-grid-line" x1="${plot.left}" y1="${y}" x2="${width - plot.right}" y2="${y}"></line>
        <text class="usage-monitor-axis-label" x="${plot.left - 12}" y="${y + 4}" text-anchor="end">${escapeHTML(monitorTokenLabel(value))}</text>`;
    }).join("");
    const xRatios = summary && width <= 520 ? [0, .5, 1] : [0, .25, .5, .75, 1];
    const xIndexes = [...new Set(xRatios.map((ratio) => Math.round((buckets.length - 1) * ratio)))];
    const xLabels = xIndexes.map((index, labelIndex) => {
      const anchor = summary && labelIndex === 0
        ? "start"
        : summary && labelIndex === xIndexes.length - 1
          ? "end"
          : "middle";
      return `<text class="usage-monitor-axis-label" x="${xAt(index)}" y="${height - 14}" text-anchor="${anchor}">${escapeHTML(monitorTimeLabel(buckets[index], monitorWindowUsesDateLabels()))}</text>`;
    }).join("");
    const areas = summary ? series.map((item) => {
      const path = item.values.map((value, index) => `${index ? "L" : "M"}${xAt(index).toFixed(2)},${yAt(value).toFixed(2)}`).join(" ");
      return `<path class="usage-monitor-summary-area" d="${path} L${xAt(item.values.length - 1).toFixed(2)},${height - plot.bottom} L${xAt(0).toFixed(2)},${height - plot.bottom} Z"></path>`;
    }).join("") : "";
    const lines = series.map((item, seriesIndex) => {
      const path = item.values.map((value, index) => `${index ? "L" : "M"}${xAt(index).toFixed(2)},${yAt(value).toFixed(2)}`).join(" ");
      return `<path class="usage-monitor-series${summary ? " usage-monitor-summary-series" : ""} usage-series-color-${seriesIndex % 10}" d="${path}"></path>`;
    }).join("");
    const pointIndexes = summary
      ? MonitorUtils.adaptivePointIndexes(buckets.length, plotWidth, 10)
      : [];
    const points = summary ? series.map((item, seriesIndex) => pointIndexes.map((index) => (
      `<circle class="usage-monitor-summary-point usage-series-color-${seriesIndex % 10}" data-monitor-point-index="${index}" cx="${xAt(index).toFixed(2)}" cy="${yAt(item.values[index]).toFixed(2)}" r="3" aria-hidden="true"></circle>`
    )).join("")).join("") : "";
    const hoverPoints = series.map((item, seriesIndex) => (
      `<circle class="usage-monitor-hover-point usage-series-color-${seriesIndex % 10}" data-monitor-hover-series="${seriesIndex}" cx="0" cy="0" r="4" aria-hidden="true"></circle>`
    )).join("");
    const accessible = series.map((item) => `${item.name} ${monitorTokenLabel(item.total)}`).join("，");
    const accessibleLabel = options.accessibleLabel || (kind === "account" ? "CPA 账号" : kind === "user" ? "用户" : "账号汇总");
    container.innerHTML = `<div class="usage-monitor-chart-stage">
      <div class="usage-monitor-chart-plot">
        <svg class="usage-monitor-svg" viewBox="0 0 ${width} ${height}" preserveAspectRatio="none" role="img" aria-label="${escapeHTML(`${accessibleLabel} Token 使用趋势：${accessible}`)}">
          ${grid}${xLabels}${areas}${lines}${points}
          <line class="usage-monitor-crosshair" data-monitor-crosshair x1="0" y1="${plot.top}" x2="0" y2="${height - plot.bottom}" hidden></line>
          <g data-monitor-hover-points hidden>${hoverPoints}</g>
        </svg>
      </div>
      <aside class="usage-monitor-tooltip" data-monitor-tooltip role="tooltip" aria-hidden="true"></aside>
    </div>`;

    const stage = container.querySelector(".usage-monitor-chart-stage");
    const svg = container.querySelector("svg");
    const crosshair = container.querySelector("[data-monitor-crosshair]");
    const hoverPointGroup = container.querySelector("[data-monitor-hover-points]");
    const hoverPointNodes = [...container.querySelectorAll("[data-monitor-hover-series]")];
    const tooltip = container.querySelector("[data-monitor-tooltip]");
    const colorIndexByName = new Map(
      series.map((item, seriesIndex) => [String(item.name), seriesIndex])
    );
    let activeTooltipIndex = -1;
    let tooltipSize = { width: 0, height: 0 };

    const hideTooltip = () => {
      crosshair.hidden = true;
      hoverPointGroup.hidden = true;
      tooltip.removeAttribute("data-active");
      tooltip.setAttribute("aria-hidden", "true");
      activeTooltipIndex = -1;
    };

    svg.addEventListener("mousemove", (event) => {
      const bounds = svg.getBoundingClientRect();
      const viewX = (event.clientX - bounds.left) * width / Math.max(1, bounds.width);
      const ratio = Math.max(0, Math.min(1, (viewX - plot.left) / plotWidth));
      const index = Math.round(ratio * (buckets.length - 1));
      const x = xAt(index);
      crosshair.removeAttribute("hidden");
      crosshair.setAttribute("x1", x);
      crosshair.setAttribute("x2", x);
      hoverPointGroup.removeAttribute("hidden");
      hoverPointNodes.forEach((point, seriesIndex) => {
        point.setAttribute("cx", x.toFixed(2));
        point.setAttribute("cy", yAt(series[seriesIndex]?.values?.[index]).toFixed(2));
      });
      if (activeTooltipIndex !== index) {
        const tooltipSeries = MonitorUtils.sortTooltipSeries(series, index);
        tooltip.innerHTML = `<strong>${escapeHTML(monitorTimeLabel(buckets[index], true))}</strong>${tooltipSeries.map((item) => `
          <span><i class="usage-series-color-${(colorIndexByName.get(String(item.name)) ?? 0) % 10}"></i><b>${escapeHTML(item.name)}</b><em>${escapeHTML(monitorTokenLabel(item.values[index]))}</em></span>
        `).join("")}`;
        tooltip.scrollTop = 0;
        tooltipSize = { width: tooltip.offsetWidth, height: tooltip.offsetHeight };
        activeTooltipIndex = index;
      }

      const stageBounds = stage.getBoundingClientRect();
      const scaleX = bounds.width / width;
      const scaleY = bounds.height / height;
      const pointObstacles = series.map((item) => ({
        x: bounds.left - stageBounds.left + x * scaleX,
        y: bounds.top - stageBounds.top + yAt(item.values[index]) * scaleY,
        radius: 10
      }));
      const position = MonitorUtils.placeTooltip(
        { x: event.clientX - stageBounds.left, y: event.clientY - stageBounds.top },
        tooltipSize,
        { width: stageBounds.width, height: stageBounds.height },
        pointObstacles,
        { gap: 14, padding: 8 }
      );
      tooltip.style.transform = `translate3d(${position.x.toFixed(2)}px, ${position.y.toFixed(2)}px, 0)`;
      tooltip.dataset.placement = position.placement;
      tooltip.dataset.active = "true";
      tooltip.setAttribute("aria-hidden", "false");
    });
    stage.addEventListener("mouseleave", hideTooltip);
  };

  const renderMonitorTable = (body, series, kind) => {
    const table = body.closest("table");
    const sortState = state.overviewUsageSort[kind];
    const valueFor = (item) => ({
      name: item.name,
      status: monitorSeriesStatusRank(item, kind),
      current: item.current,
      average: item.average,
      maximum: item.maximum,
      total: item.total
    }[sortState.field]);
    const colorIndexByName = new Map(
      series.map((item, index) => [String(item.name), index])
    );
    const sortedSeries = [...series].sort((left, right) => (
      compareTableValues(valueFor(left), valueFor(right), sortState.direction)
      || tableCollator.compare(String(left.name), String(right.name))
    ));
    $$("[data-monitor-sort]", table).forEach((button) => {
      const active = button.dataset.monitorSort === sortState.field;
      button.classList.toggle("active", active);
      button.dataset.direction = active ? sortState.direction : "";
      button.closest("th")?.setAttribute("aria-sort", active
        ? (sortState.direction === "asc" ? "ascending" : "descending")
        : "none");
      const label = button.textContent.trim();
      button.setAttribute("aria-label", active
        ? label + "，当前" + (sortState.direction === "asc" ? "升序" : "降序") + "，点击切换排序方向"
        : label + "，点击排序");
    });
    if (!series.length) {
      body.innerHTML = '<tr><td colspan="6" class="usage-monitor-table-empty">当前范围暂无数据</td></tr>';
      return;
    }
    body.innerHTML = sortedSeries.map((item, index) => {
      const status = monitorSeriesStatus(item.name, kind);
      const colorIndex = colorIndexByName.get(String(item.name)) ?? index;
      return `<tr>
        <td><span class="usage-monitor-series-name"><i class="usage-series-color-${colorIndex % 10}"></i><strong>${escapeHTML(item.name)}</strong></span></td>
        <td><span class="status-chip ${status.tone}">${escapeHTML(status.label)}</span></td>
        <td>${renderTokenUsage(item.current)}</td>
        <td>${renderTokenUsage(item.average)}</td>
        <td>${renderTokenUsage(item.maximum)}</td>
        <td class="usage-monitor-total">${renderTokenUsage(item.total)}</td>
      </tr>`;
    }).join("");
  };

  const overviewSummaryScope = () => {
    const accountFiltered = state.overviewUsageAccounts.length > 0;
    const accountLabel = accountFiltered
      ? monitorVariableSummary(monitorVariableConfig.account, state.overviewUsageAccounts)
      : "全部 CPA";
    const userLabel = state.overviewUsageUsers.length
      ? monitorVariableSummary(monitorVariableConfig.user, state.overviewUsageUsers)
      : "全部用户";
    return {
      title: accountFiltered ? "所选账号 Token 使用量" : "所有账号 Token 使用量",
      seriesName: accountFiltered ? "所选账号合计" : "全部账号合计",
      subtitle: `${accountLabel} · ${userLabel} · ${monitorWindowLabel()}`
    };
  };

  const renderOverviewSummaryMetadata = (interval = "") => {
    const scope = overviewSummaryScope();
    $("#overview-summary-usage-title").textContent = scope.title;
    $("#overview-summary-usage-legend").textContent = scope.seriesName;
    $("#overview-summary-usage-subtitle").textContent = `${scope.subtitle}${interval ? ` · 聚合间隔 ${interval}` : ""}`;
    $("#overview-summary-usage-unit").textContent = interval
      ? `单位：Token / ${interval}`
      : "单位：Token / 聚合间隔";
    return scope;
  };

  const renderOverviewSummaryValues = (summary = null) => {
    for (const field of ["total", "average", "maximum"]) {
      $("#overview-summary-usage-" + field).textContent = summary
        ? monitorTokenLabel(summary[field])
        : "—";
    }
  };

  const renderOverviewUsage = () => {
    renderOverviewUsageFilters();
    const usage = state.overviewUsage;
    const notice = $("#overview-usage-notice");
    const collector = usage?.collector || state.accountCollector || {};
    const collectorChip = $("#overview-usage-collector");
    const collectorState = collector.status || "starting";
    collectorChip.className = `status-chip ${collectorState === "healthy" ? "success" : collectorState === "degraded" ? "warning" : "neutral"}`;
    collectorChip.textContent = collectorState === "healthy" ? "采集正常" : collectorState === "degraded" ? "采集异常" : "等待采集";
    $("#overview-usage-generated").textContent = usage?.generated_at
      ? `更新于 ${formatFullTime(usage.generated_at)}`
      : "尚未生成趋势";

    const messages = [];
    if (state.overviewUsageError) messages.push(`趋势刷新失败：${state.overviewUsageError}${usage ? "；当前展示上一次成功数据" : ""}`);
    if (collectorState === "degraded") messages.push("用量采集异常，空白时间段不代表实际 Token 为 0");
    const unavailableAccounts = Array.isArray(usage?.unavailable_accounts)
      ? usage.unavailable_accounts
      : [];
    if (unavailableAccounts.length) {
      messages.push(`${unavailableAccounts.length} 个 CPA 未获得额度周期边界，本周期趋势未纳入这些账号`);
    }
    notice.hidden = messages.length === 0;
    notice.textContent = messages.join("。 ");

    if (state.overviewUsageLoading && !usage) {
      renderOverviewSummaryMetadata();
      renderOverviewSummaryValues();
      for (const id of ["overview-summary-usage-chart", "overview-account-usage-chart", "overview-user-usage-chart"]) {
        $("#" + id).innerHTML = '<div class="usage-monitor-loading"><span></span><span></span><span></span></div>';
      }
      $("#overview-account-usage-table").innerHTML = '<tr><td colspan="6" class="usage-monitor-table-empty">正在加载账号趋势…</td></tr>';
      $("#overview-user-usage-table").innerHTML = '<tr><td colspan="6" class="usage-monitor-table-empty">正在加载用户趋势…</td></tr>';
      return;
    }
    if (!usage) {
      renderOverviewSummaryMetadata();
      renderOverviewSummaryValues();
      for (const id of ["overview-summary-usage-chart", "overview-account-usage-chart", "overview-user-usage-chart"]) {
        $("#" + id).innerHTML = '<div class="usage-monitor-empty"><strong>趋势数据不可用</strong><span>请点击“刷新”或调整 Dashboard 变量后重试。</span></div>';
      }
      return;
    }

    const interval = monitorIntervalLabel(usage.bucket_seconds);
    const accountScope = state.overviewUsageAccounts.length
      ? ` · ${monitorVariableSummary(monitorVariableConfig.account, state.overviewUsageAccounts)}`
      : "";
    const userScope = state.overviewUsageUsers.length
      ? ` · ${monitorVariableSummary(monitorVariableConfig.user, state.overviewUsageUsers)}`
      : ` · Top ${state.overviewUsageUserLimit}`;
    $("#overview-account-usage-subtitle").textContent = `${monitorWindowLabel()} · 聚合间隔 ${interval}${accountScope}`;
    $("#overview-user-usage-subtitle").textContent = `${monitorWindowLabel()} · 聚合间隔 ${interval}${userScope}`;
    const summaryScope = renderOverviewSummaryMetadata(interval);
    const accountSummary = MonitorUtils.summarizeSeries(
      usage.accounts || [],
      (usage.buckets || []).length,
      summaryScope.seriesName
    );
    renderOverviewSummaryValues(accountSummary);
    renderMonitorChart(
      $("#overview-summary-usage-chart"),
      usage.buckets || [],
      [accountSummary],
      "summary",
      { variant: "summary", accessibleLabel: summaryScope.title }
    );
    renderMonitorChart($("#overview-account-usage-chart"), usage.buckets || [], usage.accounts || [], "account");
    renderMonitorChart($("#overview-user-usage-chart"), usage.buckets || [], usage.users || [], "user");
    renderMonitorTable($("#overview-account-usage-table"), usage.accounts || [], "account");
    renderMonitorTable($("#overview-user-usage-table"), usage.users || [], "user");
  };

  const renderOverview = () => {
    if (!state.overview) return;
    const summary = state.overview.summary;
    const metrics = [
      ["有效用户", summary.users, "用户邮箱"],
      ["业务 CPA", summary.business_accounts, "可继续扩展"],
      ["有效 Key", summary.active_keys, `跨 ${summary.business_accounts} 个 CPA`],
      ["已授权 CPA", `${summary.authorized_accounts}/${summary.business_accounts}`, "OAuth 文件"],
      ["运行服务", `${summary.running_services}/${summary.total_services}`, "Compose 服务"],
      ["5 分钟请求", summary.requests_5m, "网关访问日志"]
    ];
    $("#metric-grid").innerHTML = metrics.map(([label, value, note]) => `
      <div class="metric"><label>${escapeHTML(label)}</label><strong>${escapeHTML(value)}</strong><small>${escapeHTML(note)}</small></div>
    `).join("");
    $("#overview-warnings").innerHTML = (state.overview.warnings || [])
      .map((warning) => `<div class="notice">${escapeHTML(warning)}</div>`).join("");
    renderOverviewUsage();
    renderJobList($("#recent-jobs"), state.overview.recent_jobs || [], true);
  };

  const accountRuntimeStatus = (account) => {
    return accountAvailabilityStatus(account);
  };

  const runtimeDetail = (runtime = {}, operational = {}) => {
    const parts = [String(operational.reason || "").trim()].filter(Boolean);
    const errors = Number(runtime.error_count) || 0;
    const rate429 = Number(runtime.rate_429_count) || 0;
    if (errors) parts.push(`近 1h ${errors} 次错误${rate429 ? `，其中 429 × ${rate429}` : ""}`);
    if (Number(runtime.affected_users) > 0) parts.push(`影响 ${Number(runtime.affected_users)} 位用户`);
    if (Number(runtime.last_error_status) > 0) parts.push(`最近 HTTP ${Number(runtime.last_error_status)} · ${formatFullTime(runtime.last_error_at)}`);
    if (runtime.error_log_status === "ok") parts.push(`原生错误文件 ${Number(runtime.error_log_files) || 0} 个`);
    return [...new Set(parts)].join(" · ") || "CPA 原生凭据状态正常";
  };

  const renderWeeklyQuota = (quota = {}) => {
    const weekly = quota.weekly;
    if (!weekly || quota.status !== "ok") {
      const label = quota.status === "auth_missing" ? "待授权" : "暂不可用";
      return `<span class="table-secondary quota-unavailable">${label}</span>`;
    }
    const used = weekly.limit_reached || quota.limit_reached
      ? 100
      : Math.max(0, Math.min(100, Number(weekly.used_percent) || 0));
    const tone = used >= 100 ? "danger" : used >= 80 ? "warning" : "success";
    const resetAt = Number(weekly.reset_at) || 0;
    const resetLabel = resetAt ? `下次重置 ${formatFullTime(resetAt)}` : "重置时间未知";
    return `<div class="quota-cell">
      <div><strong>${used.toFixed(0)}%</strong></div>
      <progress class="${tone}" max="100" value="${used}" aria-label="已使用 ${used.toFixed(0)}%"></progress>
      <small>${escapeHTML(resetLabel)}</small>
    </div>`;
  };

  const quotaWeeklyWindows = (quota = {}) => {
    if (Array.isArray(quota.weekly_windows) && quota.weekly_windows.length) {
      return quota.weekly_windows;
    }
    return quota.weekly ? [{ ...quota.weekly, key: "default:primary_window", label: "常规周限额" }] : [];
  };

  const renderQuotaReset = (account) => {
    const quota = account.quota || {};
    const windows = quotaWeeklyWindows(quota);
    const resettable = windows.filter((window) => window.resettable && window.reset_at);
    const credits = Array.isArray(quota.reset_credits?.credits) ? quota.reset_credits.credits : [];
    const available = quota.reset_credits?.available_count;
    const hasKnownCount = Number.isInteger(available) && available >= 0;
    const count = hasKnownCount ? `${available} 次可用` : "额度未知";
    const canReset = resettable.length > 0 && credits.length > 0;
    const unavailableReason = !resettable.length
      ? "该账号当前没有可重置的周限额"
      : hasKnownCount && available === 0
        ? "当前没有可用重置额度"
        : "重置额度明细暂不可用，请刷新后重试";
    const action = `<button class="quota-reset-action" type="button" ${canReset
      ? `data-quota-reset="${escapeHTML(account.id)}"`
      : `disabled aria-disabled="true" title="${escapeHTML(unavailableReason)}"`
    }>重置</button>`;
    return `<div class="quota-reset-cell" aria-label="${escapeHTML(count)}">
      <span class="quota-reset-count">${escapeHTML(count)}</span>
      ${action}
    </div>`;
  };

  const accountUsageWindowUnavailable = (account) => (
    state.accountUsageWindow === "since_reset" && account.usage_window_available === false
  );

  const renderAccountActivity = (account, usage = {}) => {
    const failed = Number(usage.failed_count) || 0;
    const usageUnavailable = accountUsageWindowUnavailable(account);
    const collector = state.accountCollector || {};
    const collectorStatus = collector.status || "unknown";
    const activeUsers = Number(account.active_users_1h);
    const hasActiveUsers = account.active_users_1h !== null
      && account.active_users_1h !== undefined
      && Number.isFinite(activeUsers)
      && activeUsers >= 0;
    const activeUserEmails = Array.isArray(account.active_user_emails_1h)
      ? [...new Set(account.active_user_emails_1h
        .filter((email) => typeof email === "string")
        .map((email) => email.trim())
        .filter(Boolean))]
      : [];
    let activeValue = hasActiveUsers ? formatNumber(activeUsers) : "—";
    let activeDetail = !hasActiveUsers
      ? "数据暂不可用"
      : activeUsers === 0 ? "近 1h 无请求" : "近 1h";
    let activeDetailClass = hasActiveUsers ? "" : "warning";
    if (collectorStatus === "starting") {
      activeValue = "—";
      activeDetail = "正在采集";
    } else if (collectorStatus !== "healthy") {
      activeDetail = collector.heartbeat_at
        ? `采集截至 ${formatClockTime(collector.heartbeat_at)}`
        : "数据暂不可用";
      activeDetailClass = "warning";
    }
    const activeValueMarkup = activeUsers > 0 && activeUserEmails.length
      ? `<span class="account-active-users" tabindex="0" aria-label="${escapeHTML(`近 1 小时活跃使用者：${activeUserEmails.join("，")}`)}">
          <strong>${activeValue}</strong>
          <span class="account-active-users-tooltip" role="tooltip">
            <b>近 1 小时活跃使用者（${formatNumber(activeUsers)}）</b>
            ${activeUserEmails.map((email) => `<span class="account-active-user-email">${escapeHTML(email)}</span>`).join("")}
          </span>
        </span>`
      : `<strong>${activeValue}</strong>`;
    const activeHelp = "过去滚动 60 分钟内至少发起 1 次业务请求的去重用户；成功和失败请求均计入。";
    return `<div class="account-activity">
      <div class="active"><span>活跃 ${activeValueMarkup}<button class="account-activity-help" type="button" data-tooltip="${escapeHTML(activeHelp)}" aria-label="${escapeHTML(activeHelp)}">?</button></span><small class="${activeDetailClass}">${escapeHTML(activeDetail)}</small></div>
      <div><span>路由 <strong>${formatNumber(account.routed_users)}</strong></span><small>${formatNumber(account.associated_users)} 关联</small></div>
      <div><span>请求 <strong>${usageUnavailable ? "—" : formatNumber(usage.request_count)}</strong></span><small class="${usageUnavailable || failed ? "warning" : ""}">${usageUnavailable ? "额度周期不可用" : failed ? `${formatNumber(failed)} 失败` : "全部成功"}</small></div>
    </div>`;
  };

  const renderAccountUsageFacts = (account, usage) => {
    if (accountUsageWindowUnavailable(account)) {
      return `<div class="account-usage-unavailable" role="status">
        <strong>本周期用量暂不可用</strong>
        <span>未获得该 CPA 的额度周期边界；请刷新额度信息后重试。</span>
      </div>`;
    }
    return `<div class="account-detail-facts account-usage-facts">
      <div><span>成功请求</span><strong>${formatNumber(usage.success_count)}</strong></div>
      <div><span>失败请求</span><strong>${formatNumber(usage.failed_count)}</strong></div>
      <div><span>输入 Token</span>${renderTokenUsage(usage.input_tokens)}</div>
      <div><span>输出 Token</span>${renderTokenUsage(usage.output_tokens)}</div>
      <div><span>推理 Token</span>${renderTokenUsage(usage.reasoning_tokens)}</div>
      <div class="account-cache-fact"><div class="account-cache-head"><span>缓存 Token</span><small class="account-cache-rate" title="缓存 Token ÷ 输入 Token">缓存率 ${escapeHTML(formatUsagePercent(usage.cached_tokens, usage.input_tokens))}</small></div>${renderTokenUsage(usage.cached_tokens)}</div>
      <div class="account-token-total-fact"><span>Token 总计</span>${renderTokenUsage(usage.total_tokens)}</div>
    </div>`;
  };

  const accountUsageBreakdownKey = (accountId) => {
    const account = state.accounts.find((item) => item.id === accountId);
    return [
      accountId,
      state.accountUsageWindow,
      state.accountCustomRange?.startAt ?? "",
      state.accountCustomRange?.endAt ?? "",
      account?.usage_window_start_at ?? ""
    ].join("\u0000");
  };

  const accountUsageTooltip = (model, effort) => [
    `${model} · ${effort.reasoning_effort}`,
    `调用：${formatNumber(effort.request_count)}`,
    `输入：${formatNumber(effort.input_tokens)}`,
    `输出：${formatNumber(effort.output_tokens)}`,
    `推理：${formatNumber(effort.reasoning_tokens)}`,
    `缓存：${formatNumber(effort.cached_tokens)}`,
    `总 Token：${formatNumber(effort.total_tokens)}`
  ];

  const userUsageTooltip = (model, effort) => [
    `${model} · ${effort.reasoning_effort}`,
    `调用：${formatNumber(effort.request_count)}`,
    `输入：${formatNumber(effort.input_tokens)}`,
    `输出：${formatNumber(effort.output_tokens)}`,
    `推理：${formatNumber(effort.reasoning_tokens)}`,
    `缓存：${formatNumber(effort.cached_tokens)}`,
    `总 Token：${formatNumber(effort.total_tokens)}`,
    `加权 Token：${formatNumber(effort.weighted_tokens ?? effort.total_tokens)}`
  ];

  const renderModelEffortProgress = (model) => `
    <div class="account-model-progress" role="group" aria-label="${escapeHTML(`${model.model} 各推理强度 Token 占比`)}">
      ${model.efforts.map((effort) => {
        const tooltip = userUsageTooltip(model.model, effort);
        const share = `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(effort.share_percent)}%`;
        const compact = effort.share_percent < 18 ? " compact" : "";
        const shareUnits = Math.max(1, Math.min(100, Math.round(effort.share_percent)));
        const shareClasses = `account-model-share-tens-${Math.floor(shareUnits / 10)} account-model-share-ones-${shareUnits % 10}`;
        const effortColor = globalThis.MonitorUtils.accountModelEffortColorKey(effort.reasoning_effort);
        return `<button class="account-model-progress-segment account-model-effort-${effortColor} ${shareClasses}${compact}" type="button" data-tooltip="${escapeHTML(tooltip.join("\n"))}" aria-label="${escapeHTML(tooltip.join("，"))}"><span>${escapeHTML(effort.reasoning_effort)}</span><em>${escapeHTML(share)}</em></button>`;
      }).join("")}
    </div>`;

  const userUsageTooltipLayer = () => {
    let layer = $("#user-usage-tooltip-layer");
    if (layer) return layer;
    layer = document.createElement("div");
    layer.id = "user-usage-tooltip-layer";
    layer.className = "user-usage-tooltip-layer";
    layer.hidden = true;
    document.body.append(layer);
    return layer;
  };

  const showUserUsageTooltip = (segment) => {
    if (!segment?.dataset.tooltip) return;
    const layer = userUsageTooltipLayer();
    const rect = segment.getBoundingClientRect();
    layer.textContent = segment.dataset.tooltip;
    layer.hidden = false;
    const layerRect = layer.getBoundingClientRect();
    const left = Math.min(
      window.innerWidth - layerRect.width - 12,
      Math.max(12, rect.left + rect.width / 2 - layerRect.width / 2)
    );
    const top = Math.max(12, rect.top - layerRect.height - 8);
    layer.style.left = `${left}px`;
    layer.style.top = `${top}px`;
  };

  const hideUserUsageTooltip = () => {
    const layer = $("#user-usage-tooltip-layer");
    if (layer) layer.hidden = true;
  };

  const renderAccountUsageAnalysis = (account) => {
    const key = accountUsageBreakdownKey(account.id);
    const cached = state.accountUsageBreakdowns.get(key);
    const payload = cached?.payload;
    const error = state.accountUsageBreakdownErrors.get(key);
    const loading = state.accountUsageBreakdownLoading.has(key) || (!payload && !error);
    let content = "";
    if (accountUsageWindowUnavailable(account)) {
      content = `<div class="account-model-usage-message" role="status">本周期边界暂不可用，无法展示 Token 明细。</div>`;
    } else if (loading && !payload) {
      content = `<div class="account-model-usage-skeleton" aria-label="正在加载模型 Token 明细"><span></span><span></span></div>`;
    } else if (error && !payload) {
      content = `<div class="account-model-usage-message error" role="alert">
        <span>${escapeHTML(error)}</span>
        <button class="inline-action" type="button" data-account-usage-retry="${escapeHTML(account.id)}">重试</button>
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
                const tooltip = accountUsageTooltip(model.model, effort);
                const share = `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(effort.share_percent)}%`;
                const compact = effort.share_percent < 18 ? " compact" : "";
                const shareUnits = Math.max(1, Math.min(100, Math.round(effort.share_percent)));
                const shareClasses = `account-model-share-tens-${Math.floor(shareUnits / 10)} account-model-share-ones-${shareUnits % 10}`;
                const effortColor = globalThis.MonitorUtils.accountModelEffortColorKey(effort.reasoning_effort);
                return `<button class="account-model-progress-segment account-model-effort-${effortColor} ${shareClasses}${compact}" type="button" data-tooltip="${escapeHTML(tooltip.join("\n"))}" aria-label="${escapeHTML(tooltip.join("，"))}"><span>${escapeHTML(effort.reasoning_effort)}</span><em>${escapeHTML(share)}</em></button>`;
              }).join("")}
            </div>
          </div>`).join("")}</div>`
        : `<div class="account-model-usage-message">当前范围暂无可展示的模型 Token 数据。</div>`;
    }
    return `<section class="account-model-usage" aria-label="模型与推理强度 Token 明细">
      <div class="account-model-usage-title">模型 × 推理强度 Token 明细</div>
      ${content}
    </section>`;
  };

  const loadAccountUsageBreakdown = async (accountId, force = false) => {
    const account = state.accounts.find((item) => item.id === accountId);
    if (!account || accountUsageWindowUnavailable(account)) return;
    const key = accountUsageBreakdownKey(accountId);
    const cached = state.accountUsageBreakdowns.get(key);
    if (!force && cached && Date.now() - cached.fetchedAt < 30000) return;
    if (state.accountUsageBreakdownLoading.has(key)) return;
    state.accountUsageBreakdownLoading.add(key);
    state.accountUsageBreakdownErrors.delete(key);
    renderAccounts();
    try {
      const query = usageRangeQuery(
        state.accountUsageWindow,
        state.accountCustomRange,
        { account: accountId }
      );
      const payload = await api(`/accounts/usage-breakdown?${query.toString()}`);
      state.accountUsageBreakdowns.set(key, { payload, fetchedAt: Date.now() });
    } catch (error) {
      state.accountUsageBreakdownErrors.set(key, error.message);
    } finally {
      state.accountUsageBreakdownLoading.delete(key);
      if (state.expandedAccount === accountId) renderAccounts();
    }
  };

  const toggleAccountExpansion = (accountId, forceOpen = false) => {
    const opening = forceOpen || state.expandedAccount !== accountId;
    state.expandedAccount = opening ? accountId : "";
    renderAccounts();
    if (opening) loadAccountUsageBreakdown(accountId);
  };

  const renderAccounts = () => {
    renderCustomUsageRangeControl("account");
    const query = $("#account-search").value.trim().toLowerCase();
    const runtimeFilter = $("#account-runtime-filter").value;
    const authFilter = $("#account-auth-filter").value;
    const accounts = state.accounts.filter((account) => {
      const searchable = `${account.id} ${account.email}`.toLowerCase();
      const runtime = !account.group_enabled
        ? "disabled"
        : account.container_state === "running" ? "running" : "stopped";
      return searchable.includes(query)
        && (runtimeFilter === "all" || runtime === runtimeFilter)
        && (authFilter === "all" || account.auth_state === authFilter);
    });
    const accountQuotaSortValue = (account) => {
      if (account.quota?.status !== "ok" || !account.quota?.weekly) return null;
      if (account.quota.weekly.limit_reached || account.quota.limit_reached) return 100;
      const rawUsedPercent = account.quota.weekly.used_percent;
      if (rawUsedPercent === null || rawUsedPercent === undefined || rawUsedPercent === "") return null;
      const usedPercent = Number(rawUsedPercent);
      return Number.isFinite(usedPercent)
        ? Math.max(0, Math.min(usedPercent, 100))
        : null;
    };
    const accountSortValue = (account) => ({
      account: account.id,
      runtime: accountRuntimeStatus(account).label,
      auth: statusLabel(account.auth_state),
      quota: accountQuotaSortValue(account),
      activity: account.usage?.request_count ?? 0,
      tokens: account.usage?.total_tokens ?? 0,
      last_used: account.usage?.last_used_at || null
    }[state.accountSort.field]);
    accounts.sort((left, right) => (
      compareTableValues(accountSortValue(left), accountSortValue(right), state.accountSort.direction)
      || tableCollator.compare(left.id, right.id)
    ));
    $$('[data-account-sort]').forEach((button) => {
      const active = button.dataset.accountSort === state.accountSort.field;
      button.classList.toggle("active", active);
      button.dataset.direction = active ? state.accountSort.direction : "";
      button.closest("th")?.setAttribute("aria-sort", active
        ? (state.accountSort.direction === "asc" ? "ascending" : "descending")
        : "none");
      const label = button.textContent.trim();
      button.setAttribute("aria-label", active
        ? `${label}，当前${state.accountSort.direction === "asc" ? "升序" : "降序"}，点击切换排序方向`
        : `${label}，点击排序`);
    });
    const imageByAccount = new Map(
      (state.imageStatus?.accounts || []).map((item) => [item.account, item])
    );
    $("#account-table-body").innerHTML = accounts.map((account, index) => {
      const expanded = state.expandedAccount === account.id;
      const runtime = accountRuntimeStatus(account);
      const runtimeTooltip = runtimeDetail(account.runtime, runtime);
      const usage = account.usage || {};
      const usageUnavailable = accountUsageWindowUnavailable(account);
      const image = imageByAccount.get(account.id) || {};
      const imageLabel = image.image_short_id || "—";
      const imageUpdateDisabled = !account.group_enabled || !image.running || !state.imageStatus?.local_image?.available || image.using_target;
      const imageUpdateTitle = !account.group_enabled
        ? "CPA 账号已停用；启用后再更新镜像"
        : !image.running
        ? "CPA 未运行；拉取镜像后下次启动会使用目标镜像"
        : !state.imageStatus?.local_image?.available
          ? "请先拉取目标镜像"
          : image.using_target
            ? "当前 CPA 已使用目标镜像"
            : "使用目标镜像重建此 CPA";
      const routedUsers = Number(account.routed_users) || 0;
      const availableOtherAccounts = state.accounts.some(
        (item) => item.id !== account.id && item.group_enabled
      );
      const rebalanceDisabled = routedUsers <= 0 || !availableOtherAccounts;
      const rebalanceTitle = !availableOtherAccounts
        ? "当前没有其他已启用的 CPA"
        : routedUsers <= 0
          ? "当前账号没有路由用户"
          : `刷新官方额度后，将 ${formatNumber(routedUsers)} 位用户按自动切换算法分配到其他可用账号`;
      return `<tr class="account-summary-row ${expanded ? "expanded" : ""}" data-account-row="${escapeHTML(account.id)}" tabindex="0" aria-expanded="${expanded}">
        <td class="table-index-cell">${index + 1}</td>
        <td class="account-toggle-cell"><div class="account-cell-content account-toggle-content"><span class="account-chevron" aria-hidden="true">›</span></div></td>
        <td><div class="account-cell-content"><div><span class="table-primary">${escapeHTML(account.id)}</span><span class="table-secondary">:${escapeHTML(account.port)}</span></div></div></td>
        <td class="account-runtime-cell"><div class="account-cell-content"><div class="account-tag-stack"><span class="status-chip ${runtime.tone} account-runtime-status" tabindex="0" data-tooltip="${escapeHTML(runtimeTooltip)}" aria-label="${escapeHTML(`${runtime.label}：${runtimeTooltip}`)}">${runtime.label}</span></div></div></td>
        <td class="account-auth-cell"><div class="account-cell-content"><div class="account-tag-stack"><span class="status-chip ${statusClass(account.auth_state)}">${statusLabel(account.auth_state)}</span></div></div></td>
        <td><div class="account-cell-content"><div class="account-quota-overview"><div class="account-quota-main">${renderWeeklyQuota(account.quota)}</div>${renderQuotaReset(account)}</div></div></td>
        <td><div class="account-cell-content">${renderAccountActivity(account, usage)}</div></td>
        <td class="number-cell token-total account-token-cell"><div class="account-cell-content account-token-content">${usageUnavailable ? "—" : renderTokenUsage(usage.total_tokens)}</div></td>
        <td><div class="account-cell-content">${usageUnavailable ? "—" : formatLastUsed(usage.last_used_at)}</div></td>
      </tr>
      ${expanded ? `<tr class="account-detail-row">
        <td colspan="9">
          <div class="account-detail-panel">
            <div class="account-detail-facts account-runtime-facts">
              <div><span>上游邮箱</span><strong>${escapeHTML(account.email)}</strong></div>
              <div><span>容器</span><strong>${escapeHTML(account.service)}</strong><small class="account-runtime-note">${escapeHTML(account.container_status || "—")}</small></div>
              <div><span>账号状态</span><strong title="${escapeHTML(runtime.reason)}">${escapeHTML(runtime.label)}</strong><small class="account-runtime-note" title="${escapeHTML(runtimeTooltip)}">${escapeHTML(runtimeTooltip)}</small></div>
              <div><span>OAuth 文件</span><strong>${formatNumber(account.auth_files)}</strong></div>
              <div><span>镜像摘要</span><strong>${escapeHTML(imageLabel)}</strong></div>
              <div><span>出口代理</span><strong>${escapeHTML(account.proxy_source === "account" ? "账号自定义" : account.proxy_source === "default" ? "控制面默认" : "强制直连")}</strong><small class="account-runtime-note">${escapeHTML(account.proxy_display || "direct")}</small></div>
            </div>
            ${renderAccountUsageFacts(account, usage)}
            ${renderAccountUsageAnalysis(account)}
            <div class="account-detail-actions">
              <button class="button secondary" type="button" data-operation="login" data-target="${escapeHTML(account.id)}">${account.auth_state === "configured" ? "重新 OAuth" : "开始 OAuth"}</button>
              <button class="button ghost" type="button" data-operation="${account.container_state === "running" ? "restart" : "up"}" data-target="${escapeHTML(account.id)}">${account.container_state === "running" ? "重启容器" : "启动容器"}</button>
              <button class="button ghost" type="button" data-operation="image-update" data-target="${escapeHTML(account.id)}" title="${escapeHTML(imageUpdateTitle)}" ${imageUpdateDisabled ? "disabled" : ""}>${image.using_target ? "镜像已同步" : "更新镜像"}</button>
              ${account.container_state === "running" ? `<button class="button danger-outline" type="button" data-operation="stop" data-target="${escapeHTML(account.id)}">停止容器</button>` : ""}
              <button class="button ghost" type="button" data-log-target="${escapeHTML(account.id)}">查看日志</button>
              <button class="button ghost" type="button" data-account-edit="${escapeHTML(account.id)}">编辑账号</button>
              <button class="button ${account.group_enabled ? "danger-outline is-enabled" : "secondary"} account-policy-action" type="button" data-account-policy="${escapeHTML(account.id)}" title="${escapeHTML(account.group_enabled && !availableOtherAccounts ? "至少保留一个可用 CPA" : account.group_enabled ? "停止新用户选择并迁移现有路由" : "允许用户选择该 CPA")}" ${account.group_enabled && !availableOtherAccounts ? "disabled" : ""}>${account.group_enabled ? "停用账号" : "启用账号"}</button>
              <button class="button secondary account-rebalance-action" type="button" data-account-rebalance="${escapeHTML(account.id)}" title="${escapeHTML(rebalanceTitle)}" aria-label="${escapeHTML(`迁移全部用户：${rebalanceTitle}`)}" ${rebalanceDisabled ? "disabled" : ""}>迁移全部用户</button>
            </div>
          </div>
        </td>
      </tr>` : ""}`;
    }).join("");
    const notice = $("#account-usage-notice");
    const collectorStatus = state.accountCollector?.status;
    const unavailableCount = state.accountUsageWindow === "since_reset"
      ? state.accounts.filter((account) => account.usage_window_available === false).length
      : 0;
    const notices = [];
    if (collectorStatus && collectorStatus !== "healthy") {
      notices.push(collectorStatus === "starting" ? "用量采集器正在启动" : "用量采集暂不可用");
    }
    if (unavailableCount) {
      notices.push(`${unavailableCount} 个 CPA 未获得额度周期边界，本周期用量显示为不可用`);
    }
    notice.hidden = notices.length === 0;
    notice.textContent = notices.join("；");
    $("#account-empty").hidden = accounts.length > 0;
    if (!accounts.length) {
      $("#account-empty h3").textContent = state.accounts.length
        ? "没有匹配的 CPA"
        : "还没有 CPA 账号";
    }
  };

  const renderImageManager = () => {
    const image = state.imageStatus || {};
    const local = image.local_image || {};
    const target = image.target_image || "—";
    const running = Number(image.running_count) || 0;
    const current = Number(image.current_count) || 0;
    const outdated = Number(image.outdated_count) || 0;
    $("#cliproxy-target-image").textContent = target;
    const status = $("#cliproxy-image-state");
    if (!local.available) {
      status.className = "status-chip warning";
      status.textContent = "尚未拉取";
    } else if (outdated > 0) {
      status.className = "status-chip warning";
      status.textContent = "待更新";
    } else {
      status.className = "status-chip success";
      status.textContent = "已同步";
    }
    $("#cliproxy-image-summary").textContent = local.available
      ? `${current}/${running} 个运行中的已启用 CPA · ${local.short_id || "摘要未知"}`
      : `${running} 个已启用 CPA 运行中`;
    const updateAll = $("#update-all-cpa-images");
    updateAll.disabled = !local.available || outdated === 0;
    updateAll.title = !local.available
      ? "请先拉取目标镜像"
      : outdated === 0
        ? "全部运行中的已启用 CPA 已使用目标镜像"
        : `将逐个更新 ${outdated} 个运行中的已启用 CPA；停用账号会跳过`;
  };

  const userUsageAccount = (email) => state.userUsageAccountFilters.get(email) || "";
  const userUsageBreakdownKey = (email) => [
    email,
    state.userUsageWindow,
    state.userCustomRange?.startAt ?? "",
    state.userCustomRange?.endAt ?? "",
    userUsageAccount(email) || "all"
  ].join("\u0000");

  const renderUserUsageAnalysis = (user) => {
    const account = userUsageAccount(user.email);
    const key = userUsageBreakdownKey(user.email);
    const cached = state.userUsageBreakdowns.get(key);
    const payload = cached?.payload;
    const error = state.userUsageBreakdownErrors.get(key);
    const loading = state.userUsageBreakdownLoading.has(key) || (!payload && !error);
    const accountOptions = user.accounts.map((item) => `
      <option value="${escapeHTML(item.account)}" ${item.account === account ? "selected" : ""}>${escapeHTML(item.account)}</option>
    `).join("");
    const header = (successCount = null) => `<div class="usage-analysis-header">
      <div class="usage-analysis-title"><strong>模型与推理分析</strong>${successCount === null ? "" : `<span>成功调用 <b>${formatNumber(successCount)}</b></span>`}</div>
      <label class="compact-select usage-analysis-filter"><span>CPA</span>
        <select data-user-usage-account="${escapeHTML(user.email)}" data-enhance-select>
          <option value="">全部 CPA</option>
          ${accountOptions}
        </select>
      </label>
    </div>`;
    if (loading && !payload) {
      return `<section class="user-usage-analysis">${header()}<div class="usage-analysis-skeleton" aria-label="正在加载模型分析">
        <span></span><span></span><span></span>
      </div></section>`;
    }
    if (error && !payload) {
      return `<section class="user-usage-analysis">${header()}<div class="usage-analysis-message error">
        <strong>模型分析加载失败</strong><span>${escapeHTML(error)}</span>
        <button class="inline-action" type="button" data-user-usage-retry="${escapeHTML(user.email)}">重试</button>
      </div></section>`;
    }
    if (!payload?.collection_started_at) {
      return `<section class="user-usage-analysis">${header()}<div class="usage-analysis-message">
        <strong>等待新统计开始</strong><span>用量采集器启动后，将从该时刻开始记录模型和推理强度。</span>
      </div></section>`;
    }
    const totals = payload.totals || {};
    const successCount = Number(totals.success_count) || 0;
    const failedCount = Number(totals.failed_count) || 0;
    const knownEffortCount = Number(totals.known_effort_count) || 0;
    const coverage = formatUsagePercent(knownEffortCount, successCount);
    const summary = `<div class="usage-analysis-summary">
      <div><span>失败调用</span><strong>${formatNumber(failedCount)}</strong></div>
      <div><span>强度覆盖率</span><strong>${escapeHTML(coverage)}</strong></div>
      <div class="usage-analysis-token-stat"><span>未加权 Token</span><strong>${renderTokenUsage(totals.total_tokens)}</strong></div>
      <div class="usage-analysis-token-stat"><span>加权 Token</span><strong>${renderTokenUsage(totals.weighted_tokens ?? totals.total_tokens)}</strong></div>
      <div class="usage-analysis-time-stat"><span>统计开始</span><strong>${escapeHTML(formatFullTime(payload.collection_started_at))}</strong></div>
    </div>`;
    if (!successCount) {
      return `<section class="user-usage-analysis">${header(successCount)}<div class="usage-analysis-layout usage-analysis-layout-empty">
        <div class="usage-analysis-message compact">
          <strong>当前范围暂无成功调用</strong><span>${failedCount ? `有 ${formatNumber(failedCount)} 次失败调用，未计入占比。` : "产生新调用后将在这里显示模型与推理强度组合。"}</span>
        </div>
        ${summary}
      </div></section>`;
    }
    const breakdownSort = state.userUsageBreakdownSort;
    const breakdownRows = (payload.combinations || []).map((item) => {
      const successCount = Number(item.success_count) || 0;
      const totalTokens = Number(item.total_tokens) || 0;
      const weightedTokens = Number(item.weighted_tokens ?? totalTokens) || 0;
      return {
        ...item,
        weighted_tokens: weightedTokens,
        effective_multiplier: totalTokens > 0 ? weightedTokens / totalTokens : 1,
        average_total: successCount
          ? Math.round(totalTokens / successCount)
          : 0
      };
    });
    const breakdownSortValue = (item) => ({
      account: item.account,
      combination: usageCombinationLabel(item),
      success_count: Number(item.success_count) || 0,
      share: Number(item.success_count) || 0,
      total_tokens: Number(item.total_tokens) || 0,
      weighted_tokens: Number(item.weighted_tokens) || 0,
      multiplier: Number(item.effective_multiplier) || 1,
      average_total: item.average_total,
      last_used_at: item.last_used_at || null
    }[breakdownSort.field]);
    breakdownRows.sort((left, right) => (
      compareTableValues(breakdownSortValue(left), breakdownSortValue(right), breakdownSort.direction)
      || (breakdownSort.field === "total_tokens"
        ? compareTableValues(left.success_count, right.success_count, "desc")
        : 0)
      || tableCollator.compare(usageCombinationLabel(left), usageCombinationLabel(right))
      || tableCollator.compare(String(left.account), String(right.account))
    ));
    const models = globalThis.MonitorUtils.groupAccountModelUsage(breakdownRows);
    const modelRows = models.map((model, index) => {
      const inputTokens = model.efforts.reduce((total, effort) => total + effort.input_tokens, 0);
      const outputTokens = model.efforts.reduce((total, effort) => total + effort.output_tokens, 0);
      const reasoningTokens = model.efforts.reduce((total, effort) => total + effort.reasoning_tokens, 0);
      const cachedTokens = model.efforts.reduce((total, effort) => total + effort.cached_tokens, 0);
      const modelSuccessCount = model.efforts.reduce((total, effort) => total + effort.success_count, 0);
      return `<tr>
        <td class="table-index-cell">${index + 1}</td>
        <td><span class="table-primary model-name">${escapeHTML(model.model)}</span></td>
        <td class="number-cell">${renderTokenUsage(model.total_tokens)}</td>
        <td>
          ${renderModelEffortProgress(model)}
        </td>
        <td>
          <dl class="usage-model-token-details">
            <div><dt>输入</dt><dd>${renderTokenUsage(inputTokens)}</dd></div>
            <div><dt>输出</dt><dd>${renderTokenUsage(outputTokens)}</dd></div>
            <div><dt>推理</dt><dd>${renderTokenUsage(reasoningTokens)}</dd></div>
            <div><dt>缓存</dt><dd>${renderTokenUsage(cachedTokens)}</dd></div>
          </dl>
        </td>
        <td class="number-cell">${formatNumber(modelSuccessCount)}</td>
      </tr>`;
    }).join("");
    const rows = breakdownRows.map((item, index) => {
      const count = Number(item.success_count) || 0;
      return `<tr>
        <td class="table-index-cell">${index + 1}</td>
        <td><span class="table-primary">${escapeHTML(item.account)}</span></td>
        <td><span class="table-primary model-name">${escapeHTML(usageCombinationLabel(item))}</span></td>
        <td class="number-cell">${formatNumber(count)}</td>
        <td class="number-cell usage-percentage">${escapeHTML(formatUsagePercent(count, successCount))}</td>
        <td class="number-cell">${renderTokenUsage(item.total_tokens)}</td>
        <td class="number-cell">×${escapeHTML(item.effective_multiplier.toFixed(2))}</td>
        <td class="number-cell token-total">${renderTokenUsage(item.weighted_tokens)}</td>
        <td class="number-cell">${renderTokenUsage(item.average_total)}</td>
        <td>${formatLastUsed(item.last_used_at)}</td>
      </tr>`;
    }).join("");
    return `<section class="user-usage-analysis">
      ${header(successCount)}
      <div class="usage-model-table-wrap">
        <table class="usage-model-table">
          <thead><tr><th class="table-index-column">序号</th><th>模型</th><th>使用量</th><th>推理强度构成</th><th>Token 明细</th><th>调用</th></tr></thead>
          <tbody>${modelRows}</tbody>
        </table>
      </div>
      ${summary}
      <div class="usage-breakdown-table-wrap">
        <table class="usage-breakdown-table">
          <thead><tr>
            <th class="table-index-column">序号</th>
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "account", label: "CPA", sortState: breakdownSort })}
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "combination", label: "模型 × 推理强度", sortState: breakdownSort })}
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "success_count", label: "调用", sortState: breakdownSort })}
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "share", label: "占比", sortState: breakdownSort })}
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "total_tokens", label: "未加权 Token", sortState: breakdownSort })}
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "multiplier", label: "实际倍率", sortState: breakdownSort })}
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "weighted_tokens", label: "加权 Token", sortState: breakdownSort })}
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "average_total", label: "平均/次", sortState: breakdownSort })}
            ${sortableTableHeader({ attribute: "data-user-breakdown-sort", field: "last_used_at", label: "最后使用", sortState: breakdownSort })}
          </tr></thead>
          <tbody>${rows}</tbody>
        </table>
      </div>
      ${error ? `<div class="usage-analysis-stale">刷新失败，当前展示上一次成功数据：${escapeHTML(error)}</div>` : ""}
    </section>`;
  };

  const loadUserUsageBreakdown = async (email, force = false) => {
    const user = state.users.find((item) => item.email === email);
    if (!user) return;
    const account = userUsageAccount(email);
    const key = userUsageBreakdownKey(email);
    const cached = state.userUsageBreakdowns.get(key);
    if (!force && cached && Date.now() - cached.fetchedAt < 30000) return;
    if (state.userUsageBreakdownLoading.has(key)) return;
    state.userUsageBreakdownLoading.add(key);
    state.userUsageBreakdownErrors.delete(key);
    renderUsers();
    try {
      const query = usageRangeQuery(
        state.userUsageWindow,
        state.userCustomRange,
        { email }
      );
      if (account) query.set("account", account);
      const payload = await api(`/users/usage-breakdown?${query.toString()}`);
      state.userUsageBreakdowns.set(key, { payload, fetchedAt: Date.now() });
    } catch (error) {
      state.userUsageBreakdownErrors.set(key, error.message);
    } finally {
      state.userUsageBreakdownLoading.delete(key);
      if (state.expandedUser === email) renderUsers();
    }
  };

  const userDetailKey = (email) => [
    email,
    state.userUsageWindow,
    state.userCustomRange?.startAt || "",
    state.userCustomRange?.endAt || ""
  ].join("|");

  const currentUserDetail = (email) => state.userDetails.get(userDetailKey(email))?.payload || null;

  const loadUserDetail = async (email, force = false) => {
    const key = userDetailKey(email);
    const cached = state.userDetails.get(key);
    if (!force && cached && Date.now() - cached.fetchedAt < 30000) return;
    if (state.userDetailLoading.has(key)) return;
    state.userDetailLoading.add(key);
    state.userDetailErrors.delete(key);
    if (state.expandedUser === email) renderUsers();
    try {
      const query = usageRangeQuery(
        state.userUsageWindow,
        state.userCustomRange,
        { email }
      );
      const payload = await api(`/users/detail?${query.toString()}`);
      state.userDetails.set(key, {
        payload: payload.user,
        fetchedAt: Date.now()
      });
    } catch (error) {
      state.userDetailErrors.set(key, error.message);
    } finally {
      state.userDetailLoading.delete(key);
      if (state.expandedUser === email) renderUsers();
    }
  };

  const toggleUserExpansion = (email) => {
    const opening = state.expandedUser !== email;
    state.expandedUser = opening ? email : "";
    renderUsers();
    if (opening) {
      loadUserDetail(email);
      loadUserUsageBreakdown(email);
    }
  };

  const userQuotaSourceLabel = (quota = {}) => ({
    default: "组织默认",
    user_unlimited: "单独不限额",
    user_custom: "用户自定义"
  })[quota.source] || "额度未知";

  const quotaTokenText = (value) => {
    const formatted = TokenUsageFormatter.format(value);
    return `${formatted.amount} ${formatted.unit}`;
  };

  const renderUserTokenCell = (user) => {
    const usage = user.usage || {};
    const rawTokens = Number(usage.total_tokens) || 0;
    const weightedTokens = Number(usage.weighted_tokens ?? rawTokens) || 0;
    return `<div class="user-token-summary">
      <div class="user-token-stat user-token-weighted">
        <span>${escapeHTML(userUsageWindowLabel())}加权</span>
        ${renderTokenUsage(weightedTokens)}
      </div>
      <div class="user-token-stat user-token-current">
        <span>${escapeHTML(userUsageWindowLabel())}未加权</span>
        ${renderTokenUsage(rawTokens)}
      </div>
    </div>`;
  };

  const renderUserQuotaCell = (user) => {
    const quota = user.weekly_quota || {};
    if (!quota.period) return '<span class="quota-unavailable">暂不可用</span>';
    const formattedLimit = TokenUsageFormatter.format(quota.limit_tokens);
    const limit = quota.unlimited ? "不限额" : `${formattedLimit.amount} ${formattedLimit.unit}`;
    const weightedUsed = Number(quota.weighted_used_tokens ?? quota.used_tokens) || 0;
    const rawUsed = Number(quota.raw_used_tokens) || 0;
    const percentage = quota.unlimited ? null : formatUsagePercent(quota.used_tokens, quota.limit_tokens);
    const progressValue = Math.min(100, Math.max(0, Number(quota.used_percent) || 0));
    const progress = quota.unlimited
      ? `<div class="user-quota-meter-copy"><span>本周加权用量</span>${renderTokenUsage(weightedUsed)}</div>
        <div class="user-quota-progress-copy"><span>无比例限制</span><span>剩余不限</span></div>`
      : `<div class="user-quota-meter-copy"><span>本周加权用量</span>${renderTokenUsage(weightedUsed)}</div>
        <progress class="user-quota-progress" aria-label="本周额度使用比例" value="${progressValue}" max="100"></progress>
        <div class="user-quota-progress-copy"><span>已用 ${escapeHTML(percentage)}</span><span>剩余 ${escapeHTML(quotaTokenText(quota.remaining_tokens))}</span></div>`;
    const adjustments = [
      quota.bonus_tokens > 0 ? `本周已追加 ${quotaTokenText(quota.bonus_tokens)}` : "",
      quota.usage_reset_tokens > 0 ? `本周已重置 ${quotaTokenText(quota.usage_reset_tokens)}` : ""
    ].filter(Boolean);
    return `<div class="user-quota-cell">
      <div class="user-quota-primary">
        <span class="user-quota-source">${escapeHTML(userQuotaSourceLabel(quota))}</span>
        <strong>上限 ${escapeHTML(limit)}</strong>
        <button class="inline-action" type="button" data-user-quota="${escapeHTML(user.email)}">配置</button>
      </div>
      ${progress}
      <div class="user-quota-raw-copy"><span>本周未加权</span>${renderTokenUsage(rawUsed)}</div>
      ${adjustments.length ? `<span class="user-quota-adjustment-copy">${escapeHTML(adjustments.join(" · "))}</span>` : ""}
    </div>`;
  };

  const updateUserQuotaMode = () => {
    const mode = $('input[name="user-quota-mode"]:checked')?.value || "inherit";
    const input = $("#user-quota-custom-tokens");
    input.disabled = mode !== "custom";
    $("#user-quota-custom-field").classList.toggle("disabled", mode !== "custom");
  };

  const renderQuotaAdjustmentHistory = (adjustments = [], totalCount = adjustments.length) => {
    $("#user-quota-adjustment-count").className = `status-chip ${totalCount ? "success" : "neutral"}`;
    $("#user-quota-adjustment-count").textContent = totalCount
      ? `${totalCount} 条调整`
      : "暂无调整";
    $("#user-quota-adjustment-history").innerHTML = adjustments.slice(0, 4).map((item) => {
      const label = item.action === "bonus" ? "追加本周额度" : "清零本周已用量";
      return `<div class="quota-adjustment-history-row">
        <strong>${escapeHTML(label)} · ${escapeHTML(quotaTokenText(item.token_amount))}</strong>
        <time>${escapeHTML(formatTime(item.created_at))}</time>
        <p title="${escapeHTML(item.reason)}">${escapeHTML(item.reason)}</p>
      </div>`;
    }).join("");
  };

  const hydrateUserQuotaDrawer = (email, quota = {}, adjustments = []) => {
    state.selectedUserQuota = quota;
    $("#user-quota-email").textContent = email;
    $("#user-quota-default-copy").textContent = quota.default_limit_tokens == null
      ? "当前组织默认不限额"
      : `当前组织默认 ${tokenReadableText(quota.default_limit_tokens)}`;
    const baseLimitCopy = quota.base_limit_tokens == null
      ? "不限额"
      : tokenReadableText(quota.base_limit_tokens);
    const limitCopy = quota.unlimited
      ? "不限额"
      : tokenReadableText(quota.limit_tokens);
    const bonusCopy = quota.bonus_tokens > 0
      ? `（含追加 ${tokenReadableText(quota.bonus_tokens)}）`
      : "";
    $("#user-quota-summary").innerHTML = `
      <div><dt>本周加权已用</dt><dd>${renderTokenUsage(quota.weighted_used_tokens ?? quota.used_tokens)}</dd></div>
      <div><dt>本周未加权</dt><dd>${renderTokenUsage(quota.raw_used_tokens)}</dd></div>
      <div><dt>当前加权上限</dt><dd><strong>${escapeHTML(limitCopy)}</strong>${bonusCopy ? `<small>${escapeHTML(bonusCopy)}</small>` : ""}</dd></div>
      <div><dt>基础额度</dt><dd><strong>${escapeHTML(baseLimitCopy)}</strong></dd></div>
      <div><dt>加权剩余额度</dt><dd><strong>${quota.unlimited ? "不限额" : escapeHTML(tokenReadableText(quota.remaining_tokens))}</strong></dd></div>
      <div><dt>下次重置</dt><dd><strong>${formatFullTime(quota.week_end_at)}</strong></dd></div>`;
    const mode = ["inherit", "unlimited", "custom"].includes(quota.policy_mode)
      ? quota.policy_mode
      : "inherit";
    const radio = $(`input[name="user-quota-mode"][value="${mode}"]`);
    if (radio) radio.checked = true;
    $("#user-quota-custom-tokens").value = quota.policy_tokens ?? "";
    updateTokenInputPreview($("#user-quota-custom-tokens"));
    $("#user-quota-add-bonus").disabled = Boolean(quota.unlimited);
    $("#user-quota-add-bonus").title = quota.unlimited
      ? "当前用户不限额，无需追加额度"
      : "";
    $("#user-quota-restore-default").disabled = mode === "inherit";
    $("#user-quota-reset-usage").disabled = !(Number(quota.used_tokens) > 0);
    $("#user-quota-reset-usage").title = Number(quota.used_tokens) > 0
      ? ""
      : "当前本周已用量为 0";
    renderQuotaAdjustmentHistory(adjustments, Number(quota.adjustment_count || adjustments.length));
    updateUserQuotaMode();
  };

  const openUserQuota = async (email) => {
    const user = state.users.find((item) => item.email === email);
    if (!user) return;
    state.selectedUser = email;
    $("#user-quota-error").textContent = "";
    hydrateUserQuotaDrawer(email, user.weekly_quota || {}, []);
    $("#user-quota-dialog").showModal();
    try {
      const payload = await api(`/users/quota?email=${encodeURIComponent(email)}`);
      if (state.selectedUser === email && $("#user-quota-dialog").open) {
        hydrateUserQuotaDrawer(email, payload.weekly_quota || {}, payload.adjustments || []);
      }
    } catch (error) {
      if (state.selectedUser === email) $("#user-quota-error").textContent = error.message;
    }
  };

  $("#user-quota-dialog").addEventListener("click", (event) => {
    if (event.target === event.currentTarget) closeDialog("user-quota-dialog");
  });

  const renderUserSelection = (pageUsers = []) => {
    const knownUsers = new Set(state.users.map((user) => user.email));
    [...state.selectedUsers].forEach((email) => {
      if (!knownUsers.has(email)) state.selectedUsers.delete(email);
    });
    const selectedCount = state.selectedUsers.size;
    $("#user-selection-bar").hidden = selectedCount === 0;
    $("#user-selection-count").textContent = `已选择 ${formatNumber(selectedCount)} 位用户`;
    const pageEmails = pageUsers.map((user) => user.email);
    const selectedOnPage = pageEmails.filter((email) => state.selectedUsers.has(email)).length;
    const selectPage = $("#user-select-page");
    selectPage.disabled = pageEmails.length === 0;
    selectPage.checked = pageEmails.length > 0 && selectedOnPage === pageEmails.length;
    selectPage.indeterminate = selectedOnPage > 0 && selectedOnPage < pageEmails.length;
  };

  const userWeeklyQuotaSortDetails = (user) => {
    const quota = user.weekly_quota || {};
    const usedTokens = Number(quota.used_tokens);
    if (!Number.isFinite(usedTokens)) return { category: 1, value: null };
    return { category: 0, value: Math.max(0, usedTokens) };
  };

  const compareUserWeeklyQuota = (left, right, direction) => {
    const leftQuota = userWeeklyQuotaSortDetails(left);
    const rightQuota = userWeeklyQuotaSortDetails(right);
    if (leftQuota.category !== rightQuota.category) {
      return leftQuota.category - rightQuota.category;
    }
    return compareTableValues(leftQuota.value, rightQuota.value, direction);
  };

  const userAccountStatusRank = (status) => ({
    revoked: 0,
    failed: 0,
    missing: 1,
    inactive: 2,
    rotated: 2,
    active: 3
  }[status] ?? 2);

  const renderOrganizationFilters = () => {
    const teamFilter = $("#user-team-filter");
    const availableTeamValues = new Set(["", "unassigned", ...state.teams.map((team) => team.id)]);
    if (!availableTeamValues.has(state.userTeamFilter)) state.userTeamFilter = "";
    teamFilter.innerHTML = `<option value="">全部团队</option><option value="unassigned">未分组</option>${state.teams.map((team) =>
      `<option value="${escapeHTML(team.id)}">${escapeHTML(team.name)}（${formatNumber(team.user_count)}）</option>`
    ).join("")}`;
    teamFilter.value = state.userTeamFilter;
    syncEnhancedSelect(teamFilter);
    renderTeamUsageTrigger();
  };

  const selectedTeamUsage = () => state.teamUsage.find((item) => item.id === state.userTeamFilter) || null;

  const renderTeamUsageTrigger = () => {
    const button = $("#team-usage-button");
    const label = $("#team-usage-button-label");
    if (!button || !label) return;
    const team = selectedTeamUsage();
    button.disabled = !team;
    label.textContent = team ? `${team.name} Token 用量` : "选择团队后查看用量";
    button.title = team
      ? `查看 ${team.name} 的模型与推理强度 Token 用量`
      : "请先在团队筛选器中选择一个团队";
  };

  const teamCombinationList = (payload) => {
    const models = globalThis.MonitorUtils.groupAccountModelUsage(payload.combinations || []);
    return models.map((model) => {
      const weightedTokens = model.efforts.reduce((total, effort) => total + Number(effort.weighted_tokens ?? effort.total_tokens), 0);
      const requestCount = model.efforts.reduce((total, effort) => total + Number(effort.request_count || 0), 0);
      return `<div class="team-combination-row">
        <span class="team-combination-label"><strong>${escapeHTML(model.model)}</strong><small>${formatNumber(requestCount)} 次调用</small></span>
        <span class="team-combination-progress">${renderModelEffortProgress(model)}</span>
        <span class="team-combination-value"><strong>${renderTokenUsage(weightedTokens)}</strong><small>加权 Token</small></span>
      </div>`;
    }).join("") || '<div class="team-usage-state"><strong>暂无模型明细</strong><span>当前范围内没有成功记录模型与推理强度的调用。</span></div>';
  };

  const teamMemberRanking = (rows) => `<div class="team-member-ranking">${(rows || []).slice(0, 8).map((item, index) => `
    <div><span>${String(index + 1).padStart(2, "0")}</span><strong title="${escapeHTML(item.user)}">${escapeHTML(item.user)}</strong><em>${renderTokenUsage(item.weighted_tokens)}</em></div>
  `).join("") || '<div class="team-usage-state"><span>当前范围暂无活跃成员</span></div>'}</div>`;

  const renderSelectedTeamUsage = (team, payload) => {
    const target = $("#team-usage-content");
    const totals = payload.totals || {};
    const rawTokens = Number(totals.total_tokens) || 0;
    const weightedTokens = Number(totals.weighted_tokens) || 0;
    const multiplier = rawTokens ? weightedTokens / rawTokens : 1;
    target.innerHTML = `<section class="team-detail-summary">
      <div class="team-detail-primary"><span>${escapeHTML(userUsageWindowLabel())}加权 Token</span><strong>${renderTokenUsage(weightedTokens)}</strong><small>${formatNumber(totals.request_count)} 次调用 · ${formatNumber(totals.failed_count)} 次失败</small></div>
      <div class="team-detail-facts">
        <div><span>未加权 Token</span><strong>${renderTokenUsage(rawTokens)}</strong></div>
        <div><span>平均倍率</span><strong>×${multiplier.toFixed(2)}</strong></div>
        <div><span>当前成员</span><strong>${formatNumber(team.current_user_count)}</strong></div>
        <div><span>活跃成员</span><strong>${formatNumber(team.usage?.active_users)}</strong></div>
      </div>
    </section>
    ${renderTeamTrend(payload.series)}
    <section class="team-combination-section"><div class="team-detail-heading"><div><h4>模型与推理强度</h4><p class="section-kicker">MODEL × EFFORT</p></div><span>色块表示该模型各推理强度 Token 占比</span></div><div class="team-combination-list">${teamCombinationList(payload)}</div></section>
    <section class="team-member-section"><div class="team-detail-heading"><div><h4>活跃成员排行</h4><p class="section-kicker">MEMBERS</p></div><span>前 8 位</span></div>${teamMemberRanking(payload.users)}</section>`;
  };

  const openTeamUsageDrawer = async () => {
    const team = selectedTeamUsage();
    if (!team) return;
    $("#team-usage-drawer-title").textContent = `${team.name} · Token 用量`;
    $("#team-usage-drawer-subtitle").textContent = `${userUsageWindowLabel()} · 模型 × 推理强度`;
    $("#team-usage-content").innerHTML = '<div class="team-usage-skeleton" aria-label="正在加载团队 Token 用量"><span></span><span></span><span></span><span></span></div>';
    $("#team-usage-drawer").showModal();
    try {
      const query = usageRangeQuery(state.userUsageWindow, state.userCustomRange, { team_id: team.id });
      const payload = await api(`/teams/usage-breakdown?${query.toString()}`);
      if (!$("#team-usage-drawer").open || state.userTeamFilter !== team.id) return;
      renderSelectedTeamUsage(team, payload);
    } catch (error) {
      if ($("#team-usage-drawer").open) {
        $("#team-usage-content").innerHTML = `<div class="team-usage-state error">团队用量加载失败：${escapeHTML(error.message)}</div>`;
      }
    }
  };

  const renderTeamTrend = (series = {}) => {
    const values = Array.isArray(series.values) ? series.values.map((value) => Number(value) || 0) : [];
    const buckets = Array.isArray(series.buckets) ? series.buckets : [];
    if (!values.length || !buckets.length) {
      return '<div class="team-trend-empty">当前范围暂无趋势数据</div>';
    }
    const width = 640;
    const height = 120;
    const paddingX = 8;
    const paddingY = 10;
    const maximum = Math.max(...values, 0);
    const scaleMaximum = Math.max(maximum, 1);
    const points = values.map((value, index) => {
      const x = values.length === 1
        ? width / 2
        : paddingX + index * (width - paddingX * 2) / (values.length - 1);
      const y = height - paddingY - value * (height - paddingY * 2) / scaleMaximum;
      return `${x.toFixed(2)},${y.toFixed(2)}`;
    }).join(" ");
    const lastPoint = points.split(" ").at(-1).split(",");
    return `<section class="team-trend"><div class="team-trend-head"><h4>加权 Token 趋势</h4><span>每 ${escapeHTML(Math.max(1, Math.round((Number(series.bucket_seconds) || 60) / 60)))} 分钟</span></div>
      <svg viewBox="0 0 ${width} ${height}" role="img" aria-label="团队加权 Token 趋势，最高 ${escapeHTML(formatNumber(maximum))} Token">
        <line x1="${paddingX}" y1="${height - paddingY}" x2="${width - paddingX}" y2="${height - paddingY}"></line>
        <polyline points="${points}"></polyline>
        <circle cx="${lastPoint[0]}" cy="${lastPoint[1]}" r="4"></circle>
      </svg>
      <div class="team-trend-axis"><span>${escapeHTML(formatFullTime(series.start_at))}</span><strong>峰值 ${renderTokenUsage(maximum)}</strong><span>${escapeHTML(formatFullTime(series.end_at))}</span></div></section>`;
  };

  const renderOrganizationCatalog = () => {
    const teamSearch = $("#organization-team-search")?.value.trim().toLowerCase() || "";
    const teamStatus = $("#organization-team-status")?.value || "all";
    const teams = state.teams.filter((team) => {
      const matchesSearch = !teamSearch || `${team.name} ${team.description || ""}`.toLowerCase().includes(teamSearch);
      const matchesStatus = teamStatus === "all"
        || (teamStatus === "active" && Number(team.user_count) > 0)
        || (teamStatus === "empty" && Number(team.user_count) === 0);
      return matchesSearch && matchesStatus;
    });
    const usageByTeam = new Map(state.teamUsage.map((item) => [item.id, item.usage || {}]));
    $("#team-catalog-list").innerHTML = teams.map((team, index) => {
      const usage = usageByTeam.get(team.id) || {};
      return `<tr><td class="table-index-cell">${index + 1}</td><td><span class="organization-catalog-name"><strong>${escapeHTML(team.name)}</strong><small>${escapeHTML(team.description || "无说明")}</small></span></td><td class="number-cell">${formatNumber(team.user_count)}</td><td class="number-cell">${formatNumber(usage.active_users || 0)}</td><td class="number-cell token-total">${renderTokenUsage(usage.weighted_tokens || 0)}</td><td>${formatTime(team.updated_at)}</td><td><div class="organization-row-actions"><button class="inline-action" type="button" data-organization-members="${escapeHTML(team.id)}">成员</button><button class="inline-action" type="button" data-organization-edit="${escapeHTML(team.id)}">编辑</button><button class="inline-action danger-text" type="button" data-organization-delete="${escapeHTML(team.id)}" ${team.user_count ? 'disabled title="请先移出团队成员"' : ""}>删除</button></div></td></tr>`;
    }).join("") || '<tr><td colspan="7" class="team-usage-state">没有匹配的团队</td></tr>';
    $("#organization-team-summary").textContent = `${formatNumber(teams.length)} 个团队`;
  };

  const loadOrganizationCatalog = async () => {
    const payload = await api("/teams");
    state.teams = payload.teams || [];
    renderOrganizationCatalog();
    renderOrganizationFilters();
  };

  const openOrganizationCatalog = async () => {
    setView("organization");
  };

  const selectedOrganizationTeam = () => state.teams.find((team) => team.id === state.organizationTeamId) || null;
  const organizationUsageWindowLabel = () => ({ today: "今日", "604800": "近 7 天", "2592000": "近 30 天", all: "全部历史" }[$("#organization-usage-window").value] || "当前范围");

  const organizationTeamRow = (user, index) => {
    const selected = state.organizationSelectedUsers.has(user.email);
    const conflict = Boolean(user.team_id && user.team_id !== state.organizationTeamId);
    const currentMember = user.team_id === state.organizationTeamId;
    const relationship = conflict
      ? '<span class="status-chip warning">属于其他团队</span>'
      : currentMember
        ? '<span class="status-chip success">本团队成员</span>'
        : '<span class="status-chip neutral">尚未加入</span>';
    return `<tr><td class="table-index-cell">${(state.organizationPagination.page - 1) * state.organizationPagination.page_size + index + 1}</td><td><input type="checkbox" data-organization-user="${escapeHTML(user.email)}" ${selected ? "checked" : ""}></td><td><span class="table-primary">${escapeHTML(user.email)}</span></td><td>${user.team ? `<span class="team-chip">${escapeHTML(user.team.name)}</span>` : '<span class="team-chip unassigned">未分组</span>'}</td><td class="number-cell token-total">${renderTokenUsage(user.usage?.weighted_tokens || 0)}</td><td>${relationship}</td></tr>`;
  };

  const renderOrganizationTeamMembers = () => {
    const team = selectedOrganizationTeam();
    if (!team) {
      $("#organization-member-body").innerHTML = '<tr><td colspan="6" class="team-usage-state">请选择一个团队</td></tr>';
      $("#organization-pagination").hidden = true;
      $("#organization-select-all").hidden = true;
      $("#organization-team-bulk").hidden = true;
      return;
    }
    $("#organization-member-body").innerHTML = state.organizationUsers.map(organizationTeamRow).join("") || '<tr><td colspan="6" class="team-usage-state">当前条件没有匹配用户</td></tr>';
    const pagination = state.organizationPagination;
    $("#organization-pagination").hidden = !pagination.total;
    $("#organization-pagination-summary").textContent = `共 ${formatNumber(pagination.total)} 位匹配用户`;
    $("#organization-page-label").textContent = `${pagination.page} / ${pagination.total_pages}`;
    $("#organization-page-prev").disabled = pagination.page <= 1;
    $("#organization-page-next").disabled = pagination.page >= pagination.total_pages;
    $("#organization-select-all").hidden = !pagination.total;
    const scopeLabel = ({ current: "当前团队成员", unassigned: "未分组用户", all: "全部用户" })[organizationTeamScope()] || "全部用户";
    const usageLabel = ({ used: "已产生 Token", unused: "未产生 Token", all: "不限用量" })[$("#organization-usage-state").value] || "不限用量";
    $("#organization-match-summary").textContent = `${scopeLabel} · ${usageLabel} · ${organizationUsageWindowLabel()}，共 ${formatNumber(pagination.total)} 位`;
    $("#organization-select-matches").textContent = state.organizationAllMatches ? "已选择全部匹配用户" : `选择全部匹配的 ${formatNumber(pagination.total)} 位用户`;
    $("#organization-select-matches").disabled = state.organizationAllMatches;
    const selectionCount = state.organizationSelectedUsers.size;
    $("#organization-team-bulk").hidden = selectionCount === 0;
    $("#organization-selection-count").textContent = `已选择 ${formatNumber(selectionCount)} 位用户`;
    const everyVisible = state.organizationUsers.length > 0 && state.organizationUsers.every((user) => state.organizationSelectedUsers.has(user.email));
    $("#organization-select-page").checked = everyVisible;
    $("#organization-select-page").indeterminate = !everyVisible && state.organizationUsers.some((user) => state.organizationSelectedUsers.has(user.email));
  };

  const loadOrganizationTeamMembers = async () => {
    if (!state.organizationTeamId) return renderOrganizationTeamMembers();
    $("#organization-team-error").textContent = "";
    const [users, usage] = await Promise.all([
      api(`/users?${organizationTeamQuery().toString()}`),
      api(`/teams/usage?window=${encodeURIComponent($("#organization-usage-window").value)}`)
    ]);
    state.organizationUsers = users.users || [];
    state.organizationPagination = users.pagination || { page: 1, page_size: 50, total: 0, total_pages: 1 };
    state.teamUsage = usage.teams || [];
    renderOrganizationCatalog();
    renderOrganizationTeamMembers();
  };

  const loadOrganizationWorkspace = async () => {
    await loadOrganizationCatalog();
    const usage = await api("/teams/usage?window=all").catch(() => ({ teams: [] }));
    state.teamUsage = usage.teams || [];
    renderOrganizationCatalog();
  };

  const openOrganizationCatalogDialog = (item = null) => {
    const editing = Boolean(item);
    $("#organization-catalog-id").value = item?.id || "";
    $("#organization-catalog-kicker").textContent = "TEAM CATALOG";
    $("#organization-catalog-dialog-title").textContent = `${editing ? "编辑" : "创建"}团队`;
    $("#organization-catalog-name-label").textContent = "团队名称";
    $("#organization-catalog-name").value = item?.name || "";
    $("#organization-catalog-description").value = item?.description || "";
    $("#organization-catalog-notice").textContent = "每位用户只能属于一个团队；报表按当前成员动态汇总所选范围内的 Token。";
    $("#organization-catalog-submit").textContent = `${editing ? "保存" : "创建"}团队`;
    $("#organization-catalog-error").textContent = "";
    $("#organization-catalog-dialog").showModal();
    $("#organization-catalog-name").focus();
  };

  const saveOrganizationCatalogItem = async () => {
    const id = $("#organization-catalog-id").value;
    const submit = $("#organization-catalog-submit");
    submit.disabled = true;
    $("#organization-catalog-error").textContent = "";
    try {
      const payload = await api("/teams", {
        method: id ? "PUT" : "POST",
        body: JSON.stringify({
          ...(id ? { id } : {}),
          name: $("#organization-catalog-name").value,
          description: $("#organization-catalog-description").value
        })
      });
      $("#organization-catalog-dialog").close();
      showToast(payload.message);
      await loadOrganizationWorkspace();
      state.viewLoadedAt.delete("users");
    } catch (error) {
      $("#organization-catalog-error").textContent = error.message;
    } finally {
      submit.disabled = false;
    }
  };

  const openOrganizationMembersDialog = async (id) => {
    state.organizationTeamId = id;
    state.organizationSelectedUsers.clear();
    state.organizationAllMatches = false;
    const item = selectedOrganizationTeam();
    if (!item) return;
    $("#organization-members-kicker").textContent = "TEAM MEMBERS";
    $("#organization-members-title").textContent = `${item.name} · 成员管理`;
    $("#organization-team-context").hidden = false;
    $("#organization-team-context-name").textContent = item.name;
    $("#organization-team-context-count").textContent = `${formatNumber(item.user_count)} 位现有成员`;
    $("#organization-team-relation-header").textContent = `与“${item.name}”的关系`;
    $("#organization-pagination").hidden = true;
    $("#organization-team-bulk").hidden = true;
    $("#organization-select-all").hidden = true;
    $("#organization-team-error").textContent = "";
    $("#organization-members-dialog").showModal();
    try {
      await loadOrganizationTeamMembers();
    } catch (error) {
      $("#organization-team-error").textContent = error.message;
    }
  };

  const deleteOrganizationTeam = async (id) => {
    const item = state.teams.find((entry) => entry.id === id);
    if (!item || item.user_count) return;
    if (!await askConfirm({ title: `删除“${item.name}”`, message: "空团队删除后无法恢复。", label: "确认删除", danger: true })) return;
    try {
      const payload = await api(`/teams?id=${encodeURIComponent(id)}`, { method: "DELETE" });
      showToast(payload.message);
      await loadOrganizationWorkspace();
      state.viewLoadedAt.delete("users");
    } catch (error) {
      showToast(error.message, "error");
    }
  };

  const loadAllOrganizationMatches = async () => {
    const selected = new Map();
    let page = 1;
    let totalPages = 1;
    do {
      const payload = await api(`/users?${organizationTeamQuery(page, 100).toString()}`);
      (payload.users || []).forEach((user) => selected.set(user.email, user.team_id || null));
      totalPages = payload.pagination?.total_pages || 1;
      page += 1;
    } while (page <= totalPages);
    state.organizationSelectedUsers = selected;
    state.organizationAllMatches = true;
    renderOrganizationTeamMembers();
  };

  const submitOrganizationTeamAssignment = async (mode = "join") => {
    const team = selectedOrganizationTeam();
    if (!team || !state.organizationSelectedUsers.size) return;
    const users = [...state.organizationSelectedUsers.entries()];
    const conflicts = users.filter(([, currentTeam]) => currentTeam && currentTeam !== team.id);
    if (mode === "join" && conflicts.length) {
      $("#organization-team-error").textContent = `有 ${formatNumber(conflicts.length)} 位用户已在其他团队；请将用户范围切换为“仅未分组”，或先移出原团队。`;
      return;
    }
    const eligible = users.filter(([, currentTeam]) => mode === "remove"
      ? currentTeam === team.id
      : mode === "move"
        ? Boolean(currentTeam && currentTeam !== team.id)
        : currentTeam === null);
    if (!eligible.length) {
      $("#organization-team-error").textContent = mode === "remove" ? "所选用户已不在当前团队" : mode === "move" ? "没有属于其他团队的用户" : "没有可直接加入的未分组用户";
      return;
    }
    const actionLabel = mode === "remove" ? "移出" : mode === "move" ? "移动到" : "加入";
    const confirmed = await askConfirm({ title: `${actionLabel}“${team.name}”`, message: `${eligible.length} 位用户将${mode === "remove" ? "变为未分组" : mode === "move" ? "从原团队移动到该团队" : "加入该团队"}。保存后，所选统计范围内这些用户的 Token 会立即按当前团队重新汇总；历史事件本身不会改写。`, label: `确认${actionLabel}`, danger: mode !== "join" });
    if (!confirmed) return;
    try {
      const groups = new Map();
      eligible.forEach(([email, currentTeam]) => {
        if (!groups.has(currentTeam)) groups.set(currentTeam, []);
        groups.get(currentTeam).push(email);
      });
      for (const [expectedTeam, emails] of groups) {
        for (let offset = 0; offset < emails.length; offset += 500) {
          await api("/users/team/batch", { method: "POST", body: JSON.stringify({ users: emails.slice(offset, offset + 500), team_id: mode === "remove" ? null : team.id, expected_team_id: expectedTeam }) });
        }
      }
      showToast(`已更新 ${eligible.length} 位用户的团队归属`);
      state.organizationSelectedUsers.clear();
      state.organizationAllMatches = false;
      state.viewLoadedAt.delete("users");
      await loadOrganizationWorkspace();
    } catch (error) {
      $("#organization-team-error").textContent = error.message;
    }
  };

  const openUserClassification = (users) => {
    const normalized = [...new Set(users)].filter(Boolean);
    if (!normalized.length) return;
    state.classificationUsers = normalized;
    const batch = normalized.length > 1;
    const user = batch ? null : state.users.find((item) => item.email === normalized[0]);
    $("#user-classification-title").textContent = batch ? "批量分配团队" : "设置团队";
    $("#user-classification-target").textContent = batch
      ? `已选择 ${formatNumber(normalized.length)} 位用户`
      : normalized[0];
    $("#user-classification-team").innerHTML = `<option value="">未分组</option>${state.teams.map((team) => `<option value="${escapeHTML(team.id)}">${escapeHTML(team.name)}</option>`).join("")}`;
    $("#user-classification-team").value = batch ? "" : (user?.team_id || "");
    $("#user-classification-error").textContent = "";
    $("#user-classification-dialog").showModal();
    $("#user-classification-team").focus();
  };

  const saveUserClassification = async () => {
    const users = state.classificationUsers;
    const teamId = $("#user-classification-team").value || null;
    const submit = $("#user-classification-submit");
    submit.disabled = true;
    submit.textContent = "正在保存…";
    $("#user-classification-error").textContent = "";
    try {
      let payload;
      if (users.length > 1) {
        payload = await api("/users/team/batch", {
          method: "POST",
          body: JSON.stringify({ users, team_id: teamId })
        });
      } else {
        payload = await api("/users/team", {
          method: "PUT",
          body: JSON.stringify({ email: users[0], team_id: teamId })
        });
      }
      $("#user-classification-dialog").close();
      state.classificationUsers = [];
      showToast(payload.message || "用户团队已更新");
      await refreshView("users", false);
    } catch (error) {
      $("#user-classification-error").textContent = error.message;
    } finally {
      submit.disabled = false;
      submit.textContent = "保存团队";
    }
  };

  const renderUsers = () => {
    renderCustomUsageRangeControl("user");
    $$('[data-user-sort]').forEach((button) => {
      const active = button.dataset.userSort === state.userSort.field;
      button.classList.toggle("active", active);
      button.dataset.direction = active ? state.userSort.direction : "";
      button.closest("th")?.setAttribute("aria-sort", active
        ? (state.userSort.direction === "asc" ? "ascending" : "descending")
        : "none");
      const label = button.textContent.trim();
      button.setAttribute("aria-label", active
        ? `${label}，当前${state.userSort.direction === "asc" ? "升序" : "降序"}，点击切换排序方向`
        : `${label}，点击排序`);
    });
    const users = state.users;
    const paginationState = state.userPagination || {};
    const totalUsers = Number(paginationState.total) || 0;
    const totalPages = Math.max(1, Number(paginationState.total_pages) || 1);
    const startIndex = (state.userPage - 1) * state.userPageSize;
    $("#user-table-body").innerHTML = users.map((user, index) => {
      const expanded = state.expandedUser === user.email;
      const detail = expanded ? currentUserDetail(user.email) : null;
      const detailKey = userDetailKey(user.email);
      const detailLoading = state.userDetailLoading.has(detailKey);
      const detailError = state.userDetailErrors.get(detailKey);
      const activeAccounts = Number(user.active_accounts) || 0;
      const accountCount = Number(user.account_count) || detail?.accounts?.length || 0;
      const coverageSlots = Math.min(12, accountCount);
      const activeSlots = accountCount
        ? Math.round(coverageSlots * activeAccounts / accountCount)
        : 0;
      const coverage = Array.from(
        { length: coverageSlots },
        (unused, slot) => `<i class="${slot < activeSlots ? "active" : ""}"></i>`
      ).join("");
      const usage = user.usage || {};
      const teamMarkup = user.team
        ? `<span class="team-chip">${escapeHTML(user.team.name)}</span>`
        : '<span class="team-chip unassigned">未分组</span>';
      const accountSort = state.userAccountSort;
      const userAccounts = detail ? [...(detail.accounts || [])] : [];
      const accountSortValue = (item) => {
        const accountUsage = item.usage || {};
        return ({
          account: item.account,
          status: userAccountStatusRank(item.status),
          requests: accountUsage.request_count || 0,
          input_tokens: accountUsage.input_tokens || 0,
          output_tokens: accountUsage.output_tokens || 0,
          reasoning_tokens: accountUsage.reasoning_tokens || 0,
          cached_tokens: accountUsage.cached_tokens || 0,
          total_tokens: accountUsage.total_tokens || 0,
          weighted_tokens: accountUsage.weighted_tokens ?? accountUsage.total_tokens ?? 0,
          last_used_at: accountUsage.last_used_at || null
        }[accountSort.field]);
      };
      userAccounts.sort((left, right) => (
        compareTableValues(accountSortValue(left), accountSortValue(right), accountSort.direction)
        || tableCollator.compare(String(left.account), String(right.account))
      ));
      const accountRows = userAccounts.map((item, accountIndex) => {
        const accountUsage = item.usage || {};
        return `<tr>
          <td class="table-index-cell">${accountIndex + 1}</td>
          <td><span class="table-primary">${escapeHTML(item.account)}</span></td>
          <td><span class="status-chip ${statusClass(item.status)}">${statusLabel(item.status)}</span></td>
          <td class="number-cell">${formatNumber(accountUsage.request_count)}</td>
          <td class="number-cell">${renderTokenUsage(accountUsage.input_tokens)}</td>
          <td class="number-cell">${renderTokenUsage(accountUsage.output_tokens)}</td>
          <td class="number-cell">${renderTokenUsage(accountUsage.reasoning_tokens)}</td>
          <td class="number-cell">${renderTokenUsage(accountUsage.cached_tokens)}</td>
          <td class="number-cell token-total">${renderTokenUsage(accountUsage.total_tokens)}</td>
          <td class="number-cell token-total">${renderTokenUsage(accountUsage.weighted_tokens ?? accountUsage.total_tokens)}</td>
          <td>${formatLastUsed(accountUsage.last_used_at)}</td>
        </tr>`;
      }).join("");
      let detailMarkup = "";
      if (expanded && !detail && detailLoading) {
        detailMarkup = `<tr class="user-detail-row"><td colspan="11"><div class="user-detail-panel"><div class="account-model-usage-skeleton" aria-label="正在加载用户详情"><span></span><span></span></div></div></td></tr>`;
      } else if (expanded && !detail && detailError) {
        detailMarkup = `<tr class="user-detail-row"><td colspan="11"><div class="user-detail-panel"><div class="account-model-usage-message error" role="alert"><span>${escapeHTML(detailError)}</span><button class="inline-action" type="button" data-user-detail-retry="${escapeHTML(user.email)}">重试</button></div></div></td></tr>`;
      } else if (expanded && detail) {
        const detailedUser = { ...user, ...detail };
        const keyLabel = detail.accounts?.find((item) => item.key)?.key?.label || "";
        detailMarkup = `<tr class="user-detail-row">
          <td colspan="11">
            <div class="user-detail-panel">
              ${renderUserUsageAnalysis(detailedUser)}
              <div class="user-account-table-wrap">
                <table class="user-account-table">
                  <thead><tr>
                    <th class="table-index-column">序号</th>
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "account", label: "CPA 账号", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "status", label: "Key 状态", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "requests", label: "次数", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "input_tokens", label: "输入 Token", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "output_tokens", label: "输出 Token", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "reasoning_tokens", label: "推理 Token", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "cached_tokens", label: "缓存 Token", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "total_tokens", label: "未加权 Token", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "weighted_tokens", label: "加权 Token", sortState: accountSort })}
                    ${sortableTableHeader({ attribute: "data-user-account-sort", field: "last_used_at", label: "最后使用", sortState: accountSort })}
                  </tr></thead>
                  <tbody>${accountRows}</tbody>
                </table>
              </div>
              <div class="user-detail-actions">
                <button class="inline-action" type="button" data-user-classification="${escapeHTML(user.email)}">设置团队</button>
                <button class="inline-action" type="button" data-user-quota="${escapeHTML(user.email)}">配置周额度</button>
                <button class="inline-action" type="button" data-user-reset-password="${escapeHTML(user.email)}">重置密码</button>
                <button class="inline-action danger-text" type="button" data-user-delete="${escapeHTML(user.email)}">删除用户</button>
                ${user.active_keys ? `
                  <button class="inline-action" type="button" data-key-action="rotate" data-user="${escapeHTML(user.email)}" data-label="${escapeHTML(keyLabel)}">轮换唯一 Key</button>
                  <button class="inline-action danger-text" type="button" data-user-revoke="${escapeHTML(user.email)}">停用唯一 Key</button>` : ""}
              </div>
            </div>
          </td>
        </tr>`;
      }
      return `<tr class="user-summary-row ${expanded ? "expanded" : ""}" data-user-row="${escapeHTML(user.email)}" tabindex="0" aria-expanded="${expanded}">
        <td class="table-index-cell">${startIndex + index + 1}</td>
        <td class="user-select-cell"><input type="checkbox" data-user-select="${escapeHTML(user.email)}" aria-label="选择 ${escapeHTML(user.email)}" ${state.selectedUsers.has(user.email) ? "checked" : ""}></td>
        <td class="user-toggle-cell"><span class="user-chevron" aria-hidden="true">›</span></td>
        <td><span class="table-primary">${escapeHTML(user.email)}</span><span class="table-secondary">${user.total_records} 条历史记录</span></td>
        <td><button class="classification-button" type="button" data-user-classification="${escapeHTML(user.email)}" aria-label="设置 ${escapeHTML(user.email)} 的团队">${teamMarkup}</button></td>
        <td><span class="status-chip ${statusClass(user.status)}">${statusLabel(user.status)}</span></td>
        <td><span class="coverage" aria-hidden="true">${coverage}</span>${activeAccounts}/${accountCount}</td>
        <td class="number-cell">${formatNumber(usage.request_count)}${usage.failed_count ? `<span class="usage-failed">${formatNumber(usage.failed_count)} 失败</span>` : ""}</td>
        <td class="number-cell token-total user-token-cell">${renderUserTokenCell(user)}</td>
        <td>${renderUserQuotaCell(user)}</td>
        <td>${formatLastUsed(usage.last_used_at)}</td>
      </tr>
      ${detailMarkup}`;
    }).join("");
    enhanceSelects($("#user-table-body"));
    const notice = $("#user-usage-notice");
    const collectorStatus = state.userCollector?.status;
    notice.hidden = !collectorStatus || collectorStatus === "healthy";
    if (!notice.hidden) {
      notice.textContent = collectorStatus === "starting" ? "用量采集器正在启动" : "用量采集暂不可用，用户管理不受影响";
    }
    const pagination = $("#user-pagination");
    pagination.hidden = totalUsers === 0;
    if (totalUsers) {
      const endIndex = Math.min(startIndex + state.userPageSize, totalUsers);
      $("#user-pagination-summary").textContent = `共 ${formatNumber(totalUsers)} 位用户 · ${formatNumber(startIndex + 1)}–${formatNumber(endIndex)}`;
      $("#user-page-size").value = String(state.userPageSize);
      $("#user-page-prev").disabled = state.userPage === 1;
      $("#user-page-next").disabled = state.userPage === totalPages;
      $("#user-page-buttons").innerHTML = paginationItems(state.userPage, totalPages).map((item) => item === "…"
        ? '<span class="pagination-ellipsis" aria-hidden="true">…</span>'
        : `<button class="pagination-page ${item === state.userPage ? "active" : ""}" type="button" data-user-page="${item}" ${item === state.userPage ? 'aria-current="page"' : ""}>${item}</button>`
      ).join("");
    }
    $("#user-empty").hidden = totalUsers > 0;
    if (!totalUsers) {
      const searching = Boolean(
        $("#user-search").value.trim()
        || state.userTeamFilter
      );
      $("#user-empty h3").textContent = searching ? "没有匹配的用户" : "还没有用户";
      $("#user-empty p").textContent = searching ? "请调整搜索条件。" : "添加用户邮箱后，将创建一个统一 API Key 并关联全部 CPA。";
    }
    renderUserSelection(users);
  };

  const serviceDescription = (service) => {
    if (service === "edge") return "稳定 API 入口与无中断路由切换";
    if (service === "web") return "Portal、使用中心与管理页面静态资源";
    if (service === "gateway-blue" || service === "gateway-green") return "API Key 鉴权、额度与 CPA 路由数据面";
    if (service === "management") return "插件与原生界面资源服务";
    if (service === "usage-collector") return "用户请求与 Token 用量采集";
    if (service === "log-maintenance") return "宿主机日志容量与备份控制";
    if (service === "admin") return "当前综合管理界面";
    return "独立 Codex 账号代理";
  };

  const targetForService = (service) => service.startsWith("cliproxy-") ? service.slice("cliproxy-".length) : service;

  const configurationApplyLabel = (mode, key = "") => key === "runtime.cliproxy_image"
    ? "镜像管理"
    : ({
    live: "立即生效",
    accounts: "重建业务 CPA",
    collector: "重启采集器",
    future: "仅新账号",
    deployment: "下次部署",
    quota: "下次采集生效",
    readonly: "系统只读"
  })[mode] || mode;

  const configurationGroupDescription = (name) => ({
    "品牌与身份": "统一管理公开名称、Logo、邮箱域名、新 Key 前缀和客户端导出参数",
    "CPA 请求": "统一作用于所有业务 CPA",
    "用量与额度": "额度查询与用量事件策略",
    "账号自动切换": "官方账号额度耗尽后按剩余资源批量迁移用户路由",
    "用户额度": "全部用户的系统默认周额度与网关故障策略",
    "推理强度策略": "同一处管理用户额度倍率和账号 Token 明细配色；两类配置独立生效",
    "企业微信通知": "定时发送 markdown_v2 额度表格并执行阈值预警",
    "会话与采集": "用户会话和采集器吞吐",
    "账号供应": "后续创建账号时使用",
    "部署环境": "保存到 .env，不中断当前入口",
    "系统约束": "为安全和协议兼容保持只读"
  })[name] || "";

  const configurationDefaultText = (field) => {
    if (!("default" in field)) return "";
    if (field.type === "boolean") return field.default ? "开启" : "关闭";
    if (field.type === "nullable_integer" && field.default == null) return "不限额";
    return `${field.default}${field.unit ? ` ${field.unit}` : ""}`;
  };

  const configurationGroups = () => state.settings?.configuration?.groups || [];

  const configurationAllFields = () => configurationGroups()
    .flatMap((group) => (group.fields || []).map((field) => ({ ...field, group: group.name })));

  const configurationEditableFields = () => configurationAllFields().filter((field) => field.editable);

  const configurationField = (key) => configurationAllFields().find((field) => field.key === key);

  const configurationDraftValue = (field) => Object.prototype.hasOwnProperty.call(state.configurationDraft, field.key)
    ? state.configurationDraft[field.key]
    : field.value;

  const normalizedConfigurationValue = (field, value) => {
    if (field.type === "boolean") return Boolean(value);
    if (field.type === "nullable_integer") return String(value ?? "").trim() === "" ? null : Number(value);
    if (["integer", "number"].includes(field.type)) return value === "" ? "" : Number(value);
    if (field.type === "color") return String(value ?? "").trim().toLowerCase();
    if (["domain_list", "host_list"].includes(field.type)) {
      const items = Array.isArray(value) ? value : String(value ?? "").split(/[,，\s]+/);
      return [...new Set(items.map((item) => String(item).trim().toLowerCase()).filter(Boolean))];
    }
    return String(value ?? "").trim();
  };

  const configurationValuesFromDraft = () => Object.fromEntries(configurationEditableFields().map((field) => [
    field.key,
    normalizedConfigurationValue(field, configurationDraftValue(field))
  ]));

  const configurationChangedFields = () => {
    const values = configurationValuesFromDraft();
    return configurationEditableFields().filter((field) => JSON.stringify(values[field.key])
      !== JSON.stringify(state.configurationOriginal[field.key]));
  };

  const reasoningEffortStrategyEditor = (group) => {
    const fields = group?.fields || [];
    const fieldFor = (prefix, effort) => fields.find(
      (field) => field.key === `${prefix}${effort}`
    );
    const rows = REASONING_EFFORTS.map(([effort, label]) => {
      const multiplier = fieldFor(REASONING_MULTIPLIER_PREFIX, effort);
      const color = fieldFor(REASONING_COLOR_PREFIX, effort);
      if (!multiplier || !color) return "";
      const multiplierValue = configurationDraftValue(multiplier);
      const colorValue = normalizedConfigurationValue(color, configurationDraftValue(color));
      const multiplierDirty = normalizedConfigurationValue(multiplier, multiplierValue)
        !== state.configurationOriginal[multiplier.key];
      const colorDirty = colorValue !== state.configurationOriginal[color.key];
      return `<div class="reasoning-strategy-row">
        <div class="reasoning-strategy-name"><strong>${escapeHTML(label)}</strong><code>${escapeHTML(effort)}</code></div>
        <div class="reasoning-strategy-control ${multiplierDirty ? "configuration-field-dirty" : ""}" data-configuration-field="${escapeHTML(multiplier.key)}">
          <label class="visually-hidden" for="reasoning-multiplier-${escapeHTML(effort)}">${escapeHTML(label)} 用户额度倍率</label>
          <div class="reasoning-multiplier-input"><input id="reasoning-multiplier-${escapeHTML(effort)}" type="number" data-configuration-key="${escapeHTML(multiplier.key)}" value="${escapeHTML(multiplierValue)}" min="${escapeHTML(multiplier.min)}" max="${escapeHTML(multiplier.max)}" step="any"><span>倍</span></div>
        </div>
        <div class="reasoning-strategy-control ${colorDirty ? "configuration-field-dirty" : ""}" data-configuration-field="${escapeHTML(color.key)}">
          <div class="reasoning-color-inputs">
            <label class="reasoning-color-swatch" title="选择 ${escapeHTML(label)} 颜色"><input type="color" data-configuration-key="${escapeHTML(color.key)}" value="${escapeHTML(/^#[0-9a-f]{6}$/i.test(colorValue) ? colorValue : color.default)}" aria-label="选择 ${escapeHTML(label)} 颜色"></label>
            <label class="visually-hidden" for="reasoning-color-${escapeHTML(effort)}">${escapeHTML(label)} 颜色值</label>
            <input id="reasoning-color-${escapeHTML(effort)}" class="reasoning-color-hex" type="text" data-configuration-key="${escapeHTML(color.key)}" value="${escapeHTML(colorValue)}" maxlength="7" pattern="#[0-9A-Fa-f]{6}" spellcheck="false" autocomplete="off" aria-label="${escapeHTML(label)} 颜色值">
          </div>
        </div>
      </div>`;
    }).join("");
    return `<section class="reasoning-strategy-editor" aria-label="推理强度倍率与颜色">
      <div class="reasoning-strategy-scope">
        <div><strong>用户额度倍率</strong><span>仅影响后续新采集事件，不追溯历史。</span></div>
        <div><strong>账号明细颜色</strong><span>固定应用于所有模型，保存后立即生效。</span></div>
      </div>
      <div class="reasoning-color-preview">
        <div><strong>配色预览</strong><span>每种推理强度使用固定颜色</span></div>
        <canvas id="reasoning-color-preview-canvas" height="58" role="img" aria-label="推理强度颜色预览"></canvas>
      </div>
      <div class="reasoning-strategy-table">
        <div class="reasoning-strategy-table-head"><span>推理强度</span><span>用户额度倍率 <small>下次采集</small></span><span>账号明细颜色 <small>立即生效</small></span></div>
        ${rows}
      </div>
      <div class="reasoning-strategy-defaults">
        <button class="button ghost" type="button" data-reasoning-strategy-reset="multiplier">恢复默认倍率</button>
        <button class="button ghost" type="button" data-reasoning-strategy-reset="color">恢复默认配色</button>
      </div>
    </section>`;
  };

  const reasoningColorPresentation = (value, fallback = "#687287") => {
    const color = /^#[0-9a-f]{6}$/i.test(String(value || "")) ? String(value).toLowerCase() : fallback;
    const channels = [1, 3, 5].map((index) => Number.parseInt(color.slice(index, index + 2), 16) / 255)
      .map((channel) => channel <= .04045 ? channel / 12.92 : ((channel + .055) / 1.055) ** 2.4);
    const luminance = .2126 * channels[0] + .7152 * channels[1] + .0722 * channels[2];
    return { color, text: luminance > .179 ? "#171d2b" : "#ffffff" };
  };

  const drawReasoningEffortColorPreview = () => {
    const canvas = $("#reasoning-color-preview-canvas");
    if (!canvas) return;
    const width = Math.max(320, Math.round(canvas.getBoundingClientRect().width || 800));
    const height = 58;
    const ratio = Math.max(1, window.devicePixelRatio || 1);
    canvas.width = Math.round(width * ratio);
    canvas.height = Math.round(height * ratio);
    const context = canvas.getContext("2d");
    if (!context) return;
    context.scale(ratio, ratio);
    const segmentWidth = width / REASONING_EFFORTS.length;
    REASONING_EFFORTS.forEach(([effort], index) => {
      const field = configurationField(`${REASONING_COLOR_PREFIX}${effort}`);
      const presentation = reasoningColorPresentation(
        field ? normalizedConfigurationValue(field, configurationDraftValue(field)) : "",
        field?.default
      );
      context.fillStyle = presentation.color;
      context.fillRect(index * segmentWidth, 0, Math.ceil(segmentWidth), height);
      context.fillStyle = presentation.text;
      context.font = "600 9px ui-monospace, SFMono-Regular, Menlo, monospace";
      context.textAlign = "center";
      context.textBaseline = "middle";
      context.fillText(effort, (index + .5) * segmentWidth, height / 2, Math.max(24, segmentWidth - 8));
    });
  };

  const resetReasoningEffortStrategy = (kind) => {
    const prefix = kind === "color" ? REASONING_COLOR_PREFIX : REASONING_MULTIPLIER_PREFIX;
    configurationEditableFields()
      .filter((field) => field.key.startsWith(prefix))
      .forEach((field) => { state.configurationDraft[field.key] = field.default; });
    renderSettingsWorkspace();
  };

  const refreshReasoningEffortColorStylesheet = () => {
    const link = $("#reasoning-effort-colors");
    if (link) link.href = `/admin/reasoning-effort-colors.css?v=${Date.now()}`;
  };

  const hydrateConfigurationDraft = () => {
    state.configurationOriginal = Object.fromEntries(
      configurationEditableFields().map((field) => [field.key, field.value])
    );
    state.configurationDraft = { ...state.configurationOriginal };
    state.configurationDirty = false;
    const groups = configurationGroups();
    if (!groups.some((group) => group.name === state.configurationGroup)) {
      state.configurationGroup = groups[0]?.name || "";
    }
  };

  const configurationInput = (field) => {
    if (!field.editable) {
      return `<span class="configuration-value-readonly">${escapeHTML(field.value)}${field.unit ? ` ${escapeHTML(field.unit)}` : ""}</span>`;
    }
    const value = configurationDraftValue(field);
    if (field.type === "boolean") {
      return `<div class="configuration-field-control boolean-control"><label><input type="checkbox" data-configuration-key="${escapeHTML(field.key)}" ${value ? "checked" : ""}><span>${value ? "已启用" : "已关闭"}</span></label></div>`;
    }
    if (field.type === "choice") {
      const options = (field.choices || []).map((choice) =>
        `<option value="${escapeHTML(choice.value)}" ${choice.value === value ? "selected" : ""}>${escapeHTML(`${choice.label} · ${choice.address || choice.value}`)}</option>`
      ).join("");
      const pending = normalizedConfigurationValue(field, value) !== state.configurationOriginal[field.key];
      return `<div class="configuration-choice-control">
        <select data-configuration-key="${escapeHTML(field.key)}" aria-label="${escapeHTML(field.label)}">${options}</select>
        <div class="configuration-choice-address">
          <span data-choice-address-label>${pending ? "待切换地址" : "当前地址"}</span>
          <code data-choice-address>${escapeHTML(value)}</code>
        </div>
      </div>`;
    }
    if (field.type === "proxy_url_secret") {
      return `<input type="password" data-configuration-key="${escapeHTML(field.key)}" value="" placeholder="${field.configured ? "已配置；留空保持不变" : "例如 socks5://user:pass@host:1080"}" autocomplete="new-password" aria-label="${escapeHTML(field.label)}">`;
    }
    const numeric = ["integer", "number", "nullable_integer"].includes(field.type);
    const inputType = numeric ? "number" : "text";
    const step = field.type === "number" ? "any" : "1";
    const constraints = numeric
      ? ` step="${step}"${field.min !== undefined ? ` min="${escapeHTML(field.min)}"` : ""}${field.max !== undefined ? ` max="${escapeHTML(field.max)}"` : ""}`
      : "";
    const tokenInput = numeric && field.unit === "Token";
    const emptyLabel = field.type === "nullable_integer"
      ? "留空表示不限额"
      : "请输入 Token 数量";
    const input = `<input type="${inputType}" data-configuration-key="${escapeHTML(field.key)}" value="${escapeHTML(value)}"${constraints}${field.type === "nullable_integer" ? ' placeholder="不限额"' : ""}${tokenInput ? ` data-token-input data-token-empty-label="${escapeHTML(emptyLabel)}"` : ""} autocomplete="off" aria-label="${escapeHTML(field.label)}">`;
    if (tokenInput) {
      const presentation = tokenInputPresentation(value, emptyLabel);
      return `<div class="configuration-token-control token-input-control">${input}<div class="token-input-preview" data-token-input-preview data-state="${presentation.state}" aria-live="polite">${presentation.html}</div></div>`;
    }
    return field.type === "nullable_integer"
      ? `<div class="configuration-nullable-control">${input}<small>留空表示不限额</small></div>`
      : input;
  };

  const configurationImpactLabel = (mode, key = "") => configurationApplyLabel(mode, key);

  const renderConfigurationChangeSummary = () => {
    const changedFields = configurationChangedFields();
    state.configurationDirty = changedFields.length > 0;
    const status = $("#configuration-change-state");
    status.className = `status-chip ${changedFields.length ? "warning" : "neutral"}`;
    status.textContent = changedFields.length ? `${changedFields.length} 项未保存` : "未修改";
    $("#configuration-save-button").disabled = !changedFields.length;
    $("#configuration-revert-button").disabled = !state.configurationDirty;
    const impactCounts = changedFields.reduce((counts, field) => {
      const label = configurationImpactLabel(field.apply_mode, field.key);
      counts.set(label, (counts.get(label) || 0) + 1);
      return counts;
    }, new Map());
    $("#configuration-impact-summary").innerHTML = impactCounts.size
      ? [...impactCounts.entries()].map(([label, count]) => `<span><strong>${count}</strong>${escapeHTML(label)}</span>`).join("")
      : "<span>修改后将在这里汇总生效影响</span>";
    $("#configuration-actions").hidden = false;
    return state.configurationDirty;
  };

  const notificationIntegration = () => `
    <article class="notification-integration">
      <div class="notification-integration-head">
        <div class="notification-integration-copy">
          <strong>企业微信群 Webhook</strong>
          <p>与通知开关、发送时间和额度阈值统一配置；固定以 markdown_v2 表格发送常规周额度、近 1 小时用户数、重置次数和刷新时间。</p>
        </div>
        <span class="status-chip neutral" id="notification-webhook-state">Webhook 未配置</span>
      </div>
      <div class="notification-webhook-editor">
        <label for="notification-webhook-url">Webhook 地址</label>
        <div class="notification-webhook-control">
          <input id="notification-webhook-url" type="url" maxlength="2048" autocomplete="off" value="${escapeHTML(state.notificationWebhookDirty ? state.notificationWebhookDraft : (state.settings?.notifications?.webhook_url || ""))}" placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...">
          <div class="notification-webhook-actions">
            <button class="button primary" type="button" id="save-notification-webhook">保存 Webhook</button>
            <button class="button danger-outline" type="button" id="clear-notification-webhook">清除 Webhook</button>
            <button class="button ghost" type="button" id="send-notification-button" disabled>发送账号信息</button>
          </div>
        </div>
        <p class="notification-webhook-help" id="notification-webhook-help">仅支持企业微信官方 qyapi.weixin.qq.com 消息推送地址。</p>
        <p class="form-error" id="notification-webhook-error" role="alert"></p>
      </div>
      <div class="notification-status-list">
        <span>最近成功<strong id="notification-last-success">—</strong></span>
        <span>下次发送<strong id="notification-next-schedule">—</strong></span>
        <span id="notification-last-error-row" hidden>最近错误<strong id="notification-last-error">—</strong></span>
      </div>
    </article>`;

  const brandingLogoEditor = () => {
    const logo = state.settings?.branding?.logo || {};
    const source = logo.url || "/portal/assets/codex-cpa-cluster-logo.svg";
    const version = logo.sha256 ? `?v=${encodeURIComponent(String(logo.sha256).slice(0, 16))}` : "";
    return `<article class="branding-logo-editor">
      <div class="branding-logo-preview">
        <img src="${escapeHTML(`${source}${version}`)}" alt="当前 Logo">
      </div>
      <div class="branding-logo-copy">
        <strong>品牌 Logo</strong>
        <p>支持 PNG、JPEG、GIF、WebP 和经过安全校验的 SVG，最大 2 MiB。保存后立即应用到入口、使用中心和管理登录页。</p>
        <span class="status-chip ${logo.custom ? "success" : "neutral"}">${logo.custom ? "自定义 Logo" : "默认 Logo"}</span>
      </div>
      <div class="branding-logo-actions">
        <label class="button secondary" for="branding-logo-file">选择并上传</label>
        <input id="branding-logo-file" type="file" accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml" hidden>
        <button class="button danger-outline" id="branding-logo-reset" type="button" ${logo.custom ? "" : "disabled"}>恢复默认</button>
        <small id="branding-logo-error" class="form-error" role="alert"></small>
      </div>
    </article>`;
  };

  const fileAsBase64 = (file) => new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.addEventListener("load", () => resolve(String(reader.result || "").split(",", 2)[1] || ""));
    reader.addEventListener("error", () => reject(new Error("Logo 文件读取失败")));
    reader.readAsDataURL(file);
  });

  const uploadBrandingLogo = async (file) => {
    if (!file) return;
    const errorNode = $("#branding-logo-error");
    if (file.size > 2 * 1024 * 1024) {
      errorNode.textContent = "Logo 文件不能超过 2 MiB";
      return;
    }
    errorNode.textContent = "正在上传…";
    try {
      const payload = await api("/settings/logo", {
        method: "POST",
        body: JSON.stringify({
          filename: file.name,
          content_type: file.type || "application/octet-stream",
          data_base64: await fileAsBase64(file),
          confirm: "save"
        })
      });
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      errorNode.textContent = error.message;
    }
  };

  const resetBrandingLogo = async () => {
    const confirmed = await askConfirm({
      title: "恢复默认 Logo？",
      message: "已上传的 Logo 将从控制面数据库删除，页面立即恢复开源默认 Logo。",
      label: "恢复默认"
    });
    if (!confirmed) return;
    try {
      const payload = await api("/settings/logo", {
        method: "DELETE",
        body: JSON.stringify({ confirm: "reset" })
      });
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      $("#branding-logo-error").textContent = error.message;
    }
  };

  const quotaSystemDangerZone = () => {
    const summary = state.settings?.user_quota_operations || {};
    const impact = AdminViewStateUtils.allUserQuotaImpact(summary);
    const canReset = impact.available && impact.usersWithUsage > 0;
    const metrics = impact.available
      ? `<span>${formatNumber(impact.totalUsers)} 位用户</span>
          <span>${formatNumber(impact.usersWithUsage)} 位有用量</span>
          <span>当前加权已用 ${escapeHTML(quotaTokenText(impact.totalUsedTokens))}</span>
          <span>未加权累计 ${escapeHTML(quotaTokenText(impact.totalRawUsedTokens))}</span>
          <span>${formatFullTime(summary.week_end_at)} 自动换周</span>`
      : "<span>影响范围暂不可确认，请刷新配置后重试</span>";
    return `<section class="quota-system-danger" aria-label="全员额度危险操作">
      <div class="quota-system-danger-copy">
        <strong>全员本周用量清零</strong>
        <p>仅用于系统异常后的统一补偿。原始 Token 事件、用户额度策略和本周追加额度都会保留；提交前必须填写原因并输入确认文字。</p>
        <div class="quota-system-danger-metrics">
          ${metrics}
        </div>
      </div>
      <button class="button danger-outline" type="button" id="quota-reset-all-users" ${canReset ? "" : "disabled"}>${!impact.available ? "影响范围暂不可确认" : canReset ? "清零全部用户本周已用量" : "当前无需清零"}</button>
    </section>`;
  };

  const renderNotificationStatus = () => {
    const webhookState = $("#notification-webhook-state");
    if (!webhookState) return;
    const notifications = state.settings?.notifications || {};
    const webhookConfigured = Boolean(notifications.webhook_configured);
    const webhookUrl = String(notifications.webhook_url || "");
    webhookState.className = `status-chip ${webhookConfigured ? "success" : "neutral"}`;
    webhookState.textContent = webhookConfigured ? "Webhook 已配置" : "Webhook 未配置";
    const webhookInput = $("#notification-webhook-url");
    if (webhookInput) {
      if (!state.notificationWebhookDirty) webhookInput.value = webhookUrl;
      webhookInput.placeholder = "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...";
    }
    const webhookHelp = $("#notification-webhook-help");
    if (webhookHelp) {
      webhookHelp.textContent = webhookConfigured
        ? "当前地址已完整显示；修改后点击保存即可替换。"
        : "仅支持企业微信官方 qyapi.weixin.qq.com 消息推送地址。";
    }
    $("#notification-last-success").textContent = formatFullTime(notifications.last_success_at);
    $("#notification-next-schedule").textContent = formatFullTime(notifications.next_schedule_at);
    const notificationError = String(notifications.last_error || "");
    $("#notification-last-error-row").hidden = !notificationError;
    $("#notification-last-error").textContent = notificationError || "—";
    $("#send-notification-button").disabled = !webhookConfigured;
    $("#clear-notification-webhook").disabled = !webhookConfigured;
  };

  const configurationGroupDirtyCount = (group) => {
    const changed = new Set(configurationChangedFields().map((field) => field.key));
    return (group.fields || []).filter((field) => changed.has(field.key)).length;
  };

  const configurationSearchMatches = () => {
    const query = state.configurationSearch.trim().toLocaleLowerCase("zh-CN");
    if (!query) return [];
    return configurationAllFields().filter((field) => [
      field.group,
      field.label,
      field.description,
      field.key
    ].join(" ").toLocaleLowerCase("zh-CN").includes(query)).slice(0, 12);
  };

  const renderConfigurationSearchResults = () => {
    const container = $("#configuration-search-results");
    const matches = configurationSearchMatches();
    const hasQuery = Boolean(state.configurationSearch.trim());
    container.hidden = !hasQuery;
    if (!hasQuery) {
      container.innerHTML = "";
      return;
    }
    container.innerHTML = matches.length ? matches.map((field) => `
      <button type="button" data-configuration-result="${escapeHTML(field.key)}" data-configuration-result-group="${escapeHTML(field.group)}">
        <span>${escapeHTML(field.label)}</span>
        <small>${escapeHTML(field.group)} · ${escapeHTML(field.key)}</small>
      </button>`).join("") : '<p class="configuration-search-empty">没有匹配项</p>';
  };

  const renderConfigurationNavigation = () => {
    const groups = configurationGroups();
    $("#configuration-navigation").innerHTML = groups.map((group) => {
      const dirtyCount = configurationGroupDirtyCount(group);
      const active = state.settingsSection === "configuration" && state.configurationGroup === group.name;
      return `<button class="${active ? "active" : ""}" type="button" data-configuration-group="${escapeHTML(group.name)}" ${active ? 'aria-current="page"' : ""}>
        <span>${escapeHTML(group.name)}</span>
        <small class="${dirtyCount ? "dirty" : ""}">${dirtyCount ? `${dirtyCount} 项修改` : `${(group.fields || []).length} 项`}</small>
      </button>`;
    }).join("");
    $$("[data-settings-section]").forEach((button) => {
      button.classList.toggle("active", button.dataset.settingsSection === state.settingsSection);
    });
    $("#settings-backup-nav-count").textContent = `${state.settings?.backups?.count || 0} 个`;
    renderConfigurationSearchResults();
  };

  const renderSettingsSectionSelect = () => {
    const select = $("#settings-section-select");
    const configurationOptions = configurationGroups().map((group) =>
      `<option value="configuration:${escapeHTML(group.name)}">${escapeHTML(group.name)}</option>`
    ).join("");
    select.innerHTML = `<optgroup label="配置分类">${configurationOptions}</optgroup>
      <optgroup label="系统管理">
        <option value="system:access">访问凭据</option>
        <option value="system:backups">安全归档</option>
        <option value="system:storage">本地数据</option>
        <option value="system:audit">审计记录</option>
      </optgroup>`;
    select.value = state.settingsSection === "configuration"
      ? `configuration:${state.configurationGroup}`
      : `system:${state.settingsSection}`;
  };

  const focusConfigurationField = () => {
    if (!state.configurationFocusKey) return;
    const key = state.configurationFocusKey;
    state.configurationFocusKey = "";
    window.requestAnimationFrame(() => {
      const field = $$('[data-configuration-field]').find(
        (item) => item.dataset.configurationField === key
      );
      if (!field) return;
      field.classList.add("configuration-field-highlight");
      field.scrollIntoView({ block: "center", behavior: "smooth" });
      field.querySelector("input")?.focus({ preventScroll: true });
      window.setTimeout(() => field.classList.remove("configuration-field-highlight"), 1600);
    });
  };

  const renderConfiguration = () => {
    const groups = configurationGroups();
    const group = groups.find((item) => item.name === state.configurationGroup) || groups[0];
    const container = $("#configuration-groups");
    const empty = $("#configuration-empty");
    if (!group) {
      container.innerHTML = "";
      empty.hidden = false;
      return;
    }
    state.configurationGroup = group.name;
    const changedKeys = new Set(configurationChangedFields().map((field) => field.key));
    const reasoningStrategy = group.name === "推理强度策略";
    const fieldsMarkup = reasoningStrategy
      ? reasoningEffortStrategyEditor(group)
      : `${group.name === "品牌与身份" ? brandingLogoEditor() : ""}${group.name === "企业微信通知" ? notificationIntegration() : ""}${(group.fields || []).map((field) => `
        <article class="configuration-field ${changedKeys.has(field.key) ? "configuration-field-dirty" : ""}" data-configuration-field="${escapeHTML(field.key)}">
          <div class="configuration-field-copy">
            <${field.editable ? "label" : "strong"}>${escapeHTML(field.label)}</${field.editable ? "label" : "strong"}>
            <p>${escapeHTML(field.description)}</p>
          </div>
          <div class="configuration-field-value">${configurationInput(field)}</div>
          <div class="configuration-field-meta">
            <span class="configuration-apply">${escapeHTML(configurationApplyLabel(field.apply_mode, field.key))}</span>
            <code>${field.editable ? escapeHTML(field.key) : "不可在线修改"}</code>
            <small>${field.editable ? `默认 ${escapeHTML(configurationDefaultText(field))}` : "由运行环境决定"}</small>
          </div>
        </article>`).join("")}`;
    $("#configuration-group-description").textContent = configurationGroupDescription(group.name);
    $("#configuration-field-count").textContent = reasoningStrategy
      ? `${REASONING_EFFORTS.length} 个强度 · ${(group.fields || []).length} 项配置`
      : `${(group.fields || []).length} 项配置`;
    empty.hidden = true;
    container.innerHTML = `<section class="configuration-group" aria-label="${escapeHTML(group.name)}">
      <div class="configuration-fields">${fieldsMarkup}</div>
      ${group.name === "用户额度" ? quotaSystemDangerZone() : ""}
    </section>`;
    enhancePasswordFields(container);
    renderConfigurationChangeSummary();
    renderNotificationStatus();
    if (reasoningStrategy) drawReasoningEffortColorPreview();
    focusConfigurationField();
  };

  const renderSettingsWorkspace = () => {
    renderConfigurationNavigation();
    renderSettingsSectionSelect();
    $$("[data-settings-panel]").forEach((panel) => {
      panel.hidden = panel.dataset.settingsPanel !== state.settingsSection;
    });
    if (state.settingsSection === "configuration") renderConfiguration();
    if (state.view === "settings") updatePageHeading();
  };

  const selectConfigurationGroup = (group, focusKey = "") => {
    if (!configurationGroups().some((item) => item.name === group)) return;
    state.settingsSection = "configuration";
    state.configurationGroup = group;
    state.configurationFocusKey = focusKey;
    state.configurationSearch = "";
    $("#configuration-search-input").value = "";
    renderSettingsWorkspace();
    if (!focusKey) $(".settings-workspace-content").scrollTop = 0;
  };

  const renderOperations = () => {
    if (!state.overview) return;
    const serviceStateRank = {
      dead: 0,
      failed: 0,
      exited: 1,
      restarting: 2,
      paused: 3,
      created: 4,
      unknown: 5,
      running: 6
    };
    const services = [...state.overview.services].sort((left, right) => (
      (serviceStateRank[left.state] ?? 5) - (serviceStateRank[right.state] ?? 5)
      || tableCollator.compare(String(left.service), String(right.service))
    ));
    $("#service-table-body").innerHTML = services.map((service, index) => {
      const target = targetForService(service.service);
      const isAdmin = service.service === "admin";
      const isGateway = service.service === "edge" || service.service.startsWith("gateway-");
      return `<tr>
        <td class="table-index-cell">${index + 1}</td>
        <td><span class="table-primary">${escapeHTML(service.service)}</span></td>
        <td><span class="table-secondary">${escapeHTML(service.name)}</span></td>
        <td><span class="status-chip ${statusClass(service.state)}">${statusLabel(service.state)}</span></td>
        <td>${escapeHTML(serviceDescription(service.service))}</td>
        <td><div class="table-actions">
          ${isAdmin || isGateway ? "" : `<button class="button ghost" type="button" data-operation="${service.state === "running" ? "restart" : "up"}" data-target="${escapeHTML(target)}">${service.state === "running" ? "重启" : "启动"}</button>`}
          <button class="button ghost" type="button" data-log-target="${escapeHTML(target)}">日志</button>
          ${isAdmin || isGateway ? "" : `<button class="button danger-outline" type="button" data-operation="stop" data-target="${escapeHTML(target)}">停止</button>`}
        </div></td>
      </tr>`;
    }).join("");
    renderJobList($("#all-jobs"), state.overview.recent_jobs || [], false);
  };

  const renderSettings = () => {
    if (!state.settings) return;
    if (!state.configurationDirty) {
      hydrateConfigurationDraft();
      $("#configuration-error").textContent = "";
    }
    renderSettingsWorkspace();
    const configured = Boolean(state.settings.management_key_configured);
    const keyState = $("#management-key-state");
    keyState.className = `status-chip ${configured ? "success" : "danger"}`;
    keyState.textContent = configured ? "管理密钥已配置" : "管理密钥未配置";
    const initialPasswordConfigured = Boolean(state.settings.initial_password_configured);
    const initialPasswordState = $("#initial-password-state");
    initialPasswordState.textContent = initialPasswordConfigured
      ? "已安全配置；当前值不会回显"
      : "未配置；新建和重置用户将被拒绝";
    $("#backup-count").textContent = `${state.settings.backups?.count || 0} 个归档`;
    $("#latest-backup").textContent = state.settings.backups?.latest || "暂无归档";
    $("#storage-table-body").innerHTML = (state.settings.storage || []).map((item, index) => `<tr>
      <td class="table-index-cell">${index + 1}</td>
      <td><span class="table-primary">${escapeHTML(item.label)}</span></td>
      <td><span class="settings-path">${escapeHTML(item.path)}</span></td>
      <td><span class="status-chip ${item.exists ? "success" : "neutral"}">${item.exists ? "已创建" : "尚未创建"}</span></td>
      <td><span class="settings-path">${escapeHTML(item.mode)}</span></td>
    </tr>`).join("");
    const audit = state.settings.recent_audit || [];
    $("#audit-table-body").innerHTML = audit.map((item, index) => `<tr>
      <td class="table-index-cell">${index + 1}</td>
      <td>${formatTime(item.timestamp)}</td>
      <td><span class="settings-path">${escapeHTML(item.action)}</span></td>
      <td>${escapeHTML(item.target)}</td>
      <td><span class="status-chip ${item.outcome === "accepted" ? "success" : "neutral"}">${escapeHTML(item.outcome || "unknown")}</span></td>
    </tr>`).join("");
    $("#audit-empty").hidden = audit.length > 0;
  };

  const renderJobList = (container, jobs, compact) => {
    if (!jobs.length) {
      container.innerHTML = '<div class="empty-state"><div class="empty-icon">⌘</div><h3>暂无任务</h3><p>启动、授权和诊断任务会显示在这里。</p></div>';
      return;
    }
    container.innerHTML = `<div class="job-list">${jobs.map((job) => `
      <div class="job-row">
        <div><div class="job-name">${escapeHTML(job.name)}</div>${compact ? "" : `<div class="job-target">${escapeHTML(job.id)}</div>`}</div>
        <div class="job-target">${escapeHTML(job.target)}</div>
        <div class="job-time">${formatTime(job.created_at)}</div>
        <button class="button ghost" type="button" data-job-id="${escapeHTML(job.id)}"><span class="status-chip ${statusClass(job.status)}">${statusLabel(job.status)}</span></button>
      </div>`).join("")}</div>`;
  };

  const openAddUser = () => {
    $("#new-user-email").value = "";
    $("#new-user-team").innerHTML = `<option value="">未分组</option>${state.teams.map((team) => `<option value="${escapeHTML(team.id)}">${escapeHTML(team.name)}</option>`).join("")}`;
    $("#new-user-team").value = state.teams.some((team) => team.id === state.userTeamFilter)
      ? state.userTeamFilter
      : "";
    $("#new-user-team-help").textContent = state.teams.length
      ? "可选；团队仅用于用量统计，不影响 CPA 自动分配。"
      : "暂无可选团队，本次将创建为未分组；可在团队管理中创建团队。";
    $("#add-user-error").textContent = "";
    $("#add-user-dialog").showModal();
    $("#new-user-email").focus();
  };

  const openAddAccount = () => {
    $("#new-account-id").value = "";
    $("#new-account-email").value = "";
    $("#new-account-proxy-mode").value = "inherit";
    $("#new-account-proxy-url").value = "";
    $("#new-account-proxy-url-field").hidden = true;
    $("#add-account-error").textContent = "";
    $("#add-account-dialog").showModal();
    $("#new-account-id").focus();
  };

  const saveNotificationWebhook = async (button) => {
    const input = $("#notification-webhook-url");
    const webhookUrl = input?.value.trim() || "";
    $("#notification-webhook-error").textContent = "";
    if (!webhookUrl) {
      $("#notification-webhook-error").textContent = "请输入企业微信 Webhook 地址";
      input?.focus();
      return;
    }
    button.disabled = true;
    button.textContent = "正在保存…";
    input.disabled = true;
    try {
      const payload = await api("/settings/notification-webhook", {
        method: "POST",
        body: JSON.stringify({ webhook_url: webhookUrl, confirm: "save" })
      });
      state.notificationWebhookDraft = "";
      state.notificationWebhookDirty = false;
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      $("#notification-webhook-error").textContent = error.message;
    } finally {
      const currentButton = $("#save-notification-webhook");
      const currentInput = $("#notification-webhook-url");
      if (currentButton) {
        currentButton.disabled = false;
        currentButton.textContent = "保存 Webhook";
      }
      if (currentInput) currentInput.disabled = false;
    }
  };

  const clearNotificationWebhook = async () => {
    const confirmed = await askConfirm({
      title: "清除企业微信 Webhook？",
      message: "Webhook 地址会从本地删除，同时关闭企业微信通知。",
      label: "确认清除",
      danger: true
    });
    if (!confirmed) return;
    try {
      const payload = await api("/settings/notification-webhook/clear", {
        method: "POST",
        body: JSON.stringify({ confirm: "clear" })
      });
      state.notificationWebhookDraft = "";
      state.notificationWebhookDirty = false;
      const input = $("#notification-webhook-url");
      if (input) input.value = "";
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      $("#notification-webhook-error").textContent = error.message;
    }
  };

  const sendAccountNotification = async (button) => {
    button.disabled = true;
    button.textContent = "正在发送…";
    try {
      const payload = await api("/notifications/send", {
        method: "POST",
        body: JSON.stringify({})
      });
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      showToast(error.message, "error");
    } finally {
      button.textContent = "发送账号信息";
      button.disabled = !state.settings?.notifications?.webhook_configured;
    }
  };

  const openQuotaReset = (accountId) => {
    const account = state.accounts.find((item) => item.id === accountId);
    if (!account) {
      showToast("CPA 账号不存在，请刷新后重试", "error");
      return;
    }
    const windows = quotaWeeklyWindows(account.quota)
      .filter((window) => window.resettable && window.reset_at)
      .map((window) => ({ ...window }));
    const credits = Array.isArray(account.quota?.reset_credits?.credits)
      ? account.quota.reset_credits.credits.map((credit) => ({ ...credit }))
      : [];
    if (!windows.length) {
      showToast("该账号当前没有可重置的周限额", "error");
      return;
    }
    if (!credits.length) {
      showToast("该账号的重置额度明细暂不可用，请刷新后重试", "error");
      return;
    }
    state.quotaResetAccount = accountId;
    state.quotaResetCredits = credits;
    const available = account.quota?.reset_credits?.available_count;
    const detailsTruncated = account.quota?.reset_credits?.details_truncated;
    $("#quota-reset-title").textContent = `重置 ${accountId} 周限额`;
    $("#quota-reset-credit-summary").textContent = detailsTruncated
      ? `上游显示可用 ${Number.isInteger(available) ? available : "—"} 次，目前提供 ${credits.length} 条可选择明细。`
      : `当前可用 ${Number.isInteger(available) ? available : credits.length} 次，本次将消耗其中 1 次。`;
    $("#quota-reset-targets").textContent = `将刷新：${windows.map((window) => window.label || "周限额").join("、")}`;
    $("#quota-reset-credit").innerHTML = credits.map((credit, index) => {
      const suffix = credits.length > 1 ? ` #${index + 1}` : "";
      const expiry = credit.expires_at ? `${formatFullTime(credit.expires_at)} 到期` : "长期有效";
      return `<option value="${escapeHTML(credit.id)}">${escapeHTML(credit.title || "Full reset")}${escapeHTML(suffix)} · ${escapeHTML(expiry)}</option>`;
    }).join("");
    $("#quota-reset-error").textContent = "";
    $("#quota-reset-dialog").showModal();
    $("#quota-reset-credit").focus();
  };

  const fallbackOptions = (accountId) => state.accounts
    .filter((item) => item.id !== accountId && item.group_enabled)
    .map((item) => `<option value="${escapeHTML(item.id)}">${escapeHTML(item.id)}</option>`)
    .join("");

  const openAccountDetail = (accountId) => {
    const account = state.accounts.find((item) => item.id === accountId);
    if (!account) return;
    state.selectedAccount = accountId;
    $("#detail-account-id").textContent = account.id;
    $("#detail-account-new-id").value = account.id;
    $("#detail-account-email").value = account.email;
    $("#detail-account-proxy-mode").value = account.proxy_mode || "inherit";
    $("#detail-account-proxy-url").value = "";
    $("#detail-account-proxy-url-field").hidden = account.proxy_mode !== "custom";
    $("#detail-account-proxy-help").textContent = `当前生效：${account.proxy_display || "直连"}；账号代理设置优先，修改后只重建当前 CPA。`;
    $("#account-update-error").textContent = "";
    $("#account-detail-facts").innerHTML = `
      <div><span>端口</span><strong>:${escapeHTML(account.port)}</strong></div>
      <div><span>出口</span><strong>${escapeHTML(account.proxy_source === "account" ? "账号代理" : account.proxy_source === "default" ? "默认代理" : "直连")}</strong></div>
      <div><span>容器</span><strong>${account.container_state === "running" ? "运行中" : "已停止"}</strong></div>
      <div><span>OAuth</span><strong>${statusLabel(account.auth_state)}</strong></div>
      <div><span>当前用户</span><strong>${escapeHTML(account.routed_users)}</strong></div>`;
    $("#account-detail-dialog").showModal();
  };

  const openAccountPolicy = (accountId) => {
    const account = state.accounts.find((item) => item.id === accountId);
    if (!account) return;
    const enabling = !account.group_enabled;
    state.accountPolicyAccount = accountId;
    $("#account-policy-title").textContent = `${enabling ? "启用" : "停用"} ${account.id}`;
    $("#account-policy-message").textContent = enabling
      ? `启用后，状态可用时用户可以选择 ${account.id}；已有用户路由不会自动变化。`
      : account.routed_users > 0
        ? `停用后将不再允许用户选择；当前 ${formatNumber(account.routed_users)} 位用户必须迁移到其他已启用 CPA。`
        : "停用后将不再允许用户选择；当前没有用户路由到该账号。";
    $("#account-policy-fallback").innerHTML = fallbackOptions(account.id);
    $("#account-policy-fallback-field").hidden = enabling || Number(account.routed_users) <= 0;
    $("#account-policy-error").textContent = "";
    const submit = $("#account-policy-submit");
    submit.textContent = enabling ? "确认启用" : "确认停用";
    submit.className = `button ${enabling ? "primary" : "danger-outline"}`;
    $("#account-policy-dialog").showModal();
  };

  const openDeleteAccount = () => {
    const account = state.accounts.find((item) => item.id === state.selectedAccount);
    if (!account) return;
    $("#delete-account-confirm").value = "";
    $("#delete-account-confirm").placeholder = account.id;
    $("#delete-account-revoke-keys").checked = false;
    $("#delete-account-keys-row").hidden = true;
    $("#delete-account-fallback").innerHTML = fallbackOptions(account.id);
    $("#delete-account-fallback-field").hidden = state.accounts.length <= 1;
    $("#delete-account-error").textContent = "";
    $("#delete-account-dialog").showModal();
    $("#delete-account-confirm").focus();
  };

  const showSecrets = (keys, message) => {
    state.secrets = keys;
    $("#secret-list").innerHTML = keys.map((item, index) => `
      <div class="secret-item">
        <div class="secret-item-head"><strong>${escapeHTML(item.account)}</strong><span>${escapeHTML(item.user)}</span></div>
        <div class="secret-value"><code>${escapeHTML(item.key)}</code><button class="secret-copy" type="button" data-copy-secret="${index}">复制</button></div>
      </div>`).join("");
    $("#secret-dialog .warning-banner").textContent = message || "关闭后平台不会再次展示完整凭据。";
    $("#secret-dialog").showModal();
  };

  const closeDialog = (id) => {
    const dialog = $(`#${id}`);
    if (dialog?.open) dialog.close();
    if (id === "secret-dialog") {
      state.secrets = [];
      $("#secret-list").replaceChildren();
    }
    if (id === "output-dialog") {
      state.activeJob = "";
      state.oauthUrl = "";
      state.oauthCode = "";
      updateOAuthCopyPanel("");
      window.clearTimeout(state.jobTimer);
    }
    if (id === "quota-reset-dialog") {
      state.quotaResetAccount = "";
      state.quotaResetCredits = [];
    }
    if (id === "account-policy-dialog") state.accountPolicyAccount = "";
    if (id === "custom-usage-range-dialog") state.customUsageRangeTarget = "";
    if (id === "user-quota-action-dialog") state.quotaAction = null;
  };

  const askConfirm = ({ title, message, label = "确认", danger = false }) => new Promise((resolve) => {
    const dialog = $("#confirm-dialog");
    $("#confirm-title").textContent = title;
    $("#confirm-message").textContent = message;
    $("#confirm-submit").textContent = label;
    $("#confirm-submit").className = `button ${danger ? "danger-outline" : "primary"}`;
    dialog.addEventListener("close", () => resolve(dialog.returnValue === "confirm"), { once: true });
    dialog.showModal();
  });

  const selectedQuotaUsers = (emails) => emails
    .map((email) => state.users.find((user) => user.email === email))
    .filter(Boolean);

  const openUserQuotaAction = (action, { scope = "selected", users = [] } = {}) => {
    const summary = state.settings?.user_quota_operations || {};
    const allImpact = AdminViewStateUtils.allUserQuotaImpact(summary);
    const targetUsers = scope === "all" ? [] : selectedQuotaUsers(users);
    if (scope === "all" && !allImpact.available) {
      showToast("全员影响范围暂不可确认，请刷新后重试", "error");
      return;
    }
    if (scope === "all" ? allImpact.totalUsers <= 0 : !targetUsers.length) {
      showToast(scope === "all" ? "当前没有可操作用户" : "请选择用户", "error");
      return;
    }
    const targetCount = scope === "all" ? allImpact.totalUsers : targetUsers.length;
    const usedUsers = scope === "all"
      ? allImpact.usersWithUsage
      : targetUsers.filter((user) => Number(user.weekly_quota?.used_tokens) > 0).length;
    const totalUsed = scope === "all"
      ? allImpact.totalUsedTokens
      : targetUsers.reduce(
        (total, user) => total + Number(user.weekly_quota?.used_tokens || 0),
        0
      );
    const totalRawUsed = scope === "all"
      ? allImpact.totalRawUsedTokens
      : targetUsers.reduce(
        (total, user) => total + Number(user.weekly_quota?.raw_used_tokens || 0),
        0
      );
    const titles = {
      add_bonus: "追加本周额度",
      reset_usage: scope === "all" ? "清零全部用户本周已用量" : "清零本周已用量"
    };
    const confirmPhrase = action === "reset_usage"
      ? (scope === "all" ? "确认清零全部" : "确认清零")
      : "";
    state.quotaAction = {
      action,
      scope,
      users: scope === "all" ? [] : targetUsers.map((user) => user.email),
      confirmPhrase
    };
    $("#user-quota-action-title").textContent = titles[action] || "额度操作";
    $("#user-quota-action-subtitle").textContent = scope === "all"
      ? `全部 ${formatNumber(targetCount)} 位用户`
      : targetUsers.length === 1
        ? targetUsers[0].email
        : `已选择 ${formatNumber(targetUsers.length)} 位用户`;
    $("#user-quota-action-impact").innerHTML = `
      <strong>${action === "add_bonus" ? "增加本周可用额度，基础策略保持不变" : "清零计费用量，原始 Token 事件与统计历史保持不变"}</strong>
      <dl>
        <div><dt>影响用户</dt><dd>${formatNumber(targetCount)} 位</dd></div>
        <div><dt>有本周用量</dt><dd>${formatNumber(usedUsers)} 位</dd></div>
        <div><dt>当前加权已用</dt><dd>${escapeHTML(quotaTokenText(totalUsed))}</dd></div>
        <div><dt>未加权累计</dt><dd>${escapeHTML(quotaTokenText(totalRawUsed))}</dd></div>
      </dl>`;
    const tokenField = $("#user-quota-action-token-field");
    tokenField.hidden = action !== "add_bonus";
    $("#user-quota-action-tokens").required = action === "add_bonus";
    $("#user-quota-action-tokens").value = "";
    updateTokenInputPreview($("#user-quota-action-tokens"));
    const confirmField = $("#user-quota-action-confirm-field");
    confirmField.hidden = !confirmPhrase;
    $("#user-quota-action-confirm").required = Boolean(confirmPhrase);
    $("#user-quota-action-confirm").value = "";
    $("#user-quota-action-confirm-label").textContent = confirmPhrase
      ? `输入“${confirmPhrase}”后继续`
      : "输入确认文字";
    $("#user-quota-action-reason").value = "";
    $("#user-quota-action-error").textContent = "";
    $("#user-quota-action-notice").textContent = action === "add_bonus"
      ? "追加额度只在当前自然周有效，下周一 00:00 自动回到基础额度。"
      : scope === "all"
        ? "这是全员危险操作。没有本周用量的用户会自动跳过，额度策略和追加额度不会改变。"
        : "系统会记录本次抵扣基准；后续新增 Token 仍会继续计入本周已用量。";
    const submit = $("#user-quota-action-submit");
    submit.className = `button ${action === "reset_usage" ? "danger-outline" : "primary"}`;
    submit.textContent = action === "add_bonus" ? "确认追加" : "确认清零";
    $("#user-quota-action-dialog").showModal();
    (action === "add_bonus"
      ? $("#user-quota-action-tokens")
      : $("#user-quota-action-reason")).focus();
  };

  const restoreUserQuotaDefaults = async (users) => {
    const targets = selectedQuotaUsers(users);
    if (!targets.length) {
      showToast("请选择用户", "error");
      return;
    }
    const customCount = targets.filter(
      (user) => user.weekly_quota?.policy_mode !== "inherit"
    ).length;
    if (!await askConfirm({
      title: `恢复 ${targets.length} 位用户的组织默认额度？`,
      message: customCount
        ? `将删除 ${customCount} 位用户的个人额度策略；当前周追加额度与用量调整保持不变。`
        : "所选用户已经继承组织默认额度，不会修改当前周追加额度或用量调整。",
      label: "恢复组织默认"
    })) return;
    try {
      const payload = await api("/users/quota-actions", {
        method: "POST",
        body: JSON.stringify({
          action: "restore_default",
          scope: "selected",
          users: targets.map((user) => user.email),
          confirm: "restore_default"
        })
      });
      closeDialog("user-quota-dialog");
      state.selectedUsers.clear();
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      showToast(error.message, "error");
    }
  };

  const submitOperation = async (action, target) => {
    let stopImpact = null;
    let stopImpactAvailable = true;
    if (action === "stop" && target !== "all") {
      try {
        const query = new URLSearchParams({ action, target });
        stopImpact = await api(`/operations/impact?${query.toString()}`);
      } catch (_error) {
        if (!state.authenticated) return;
        stopImpactAvailable = false;
      }
    }
    const messages = {
      login: ["开始 OAuth 授权？", "任务输出会显示设备授权地址和一次性验证码。完成浏览器授权前请勿关闭任务窗口。", "开始授权", false],
      stop: ["停止服务？", AdminViewStateUtils.stopOperationMessage(target, stopImpact, stopImpactAvailable), "确认停止", true],
      restart: ["重启服务？", target === "all" ? "将依次重启所有业务服务，短时间内可能无法调用。" : `将重启 ${target}。`, "确认重启", false],
      "image-update": ["更新 CPA 镜像？", target === "all" ? "将使用已拉取的目标镜像逐个重建运行中的已启用 CPA，停用账号会跳过；失败时自动恢复原镜像。" : `将使用目标镜像重建 ${target}；失败时自动恢复原镜像。`, target === "all" ? "更新全部 CPA" : "更新此 CPA", false]
    };
    if (action === "login") {
      try {
        const payload = await api("/jobs");
        const jobs = payload.jobs || [];
        const existing = jobs.find((job) => job.name === "OAuth 授权" && job.target === target && job.status === "running")
          || jobs.find((job) => job.name === "OAuth 授权" && job.target === target && job.status === "queued");
        if (existing) {
          showToast("该账号已有 OAuth 授权任务，已直接打开");
          showJob(existing);
          return;
        }
      } catch (error) {
        showToast(error.message, "error");
        return;
      }
    }
    if (messages[action]) {
      const [title, message, label, danger] = messages[action];
      if (!await askConfirm({ title, message, label, danger })) return;
    }
    try {
      const payload = await api("/operations", { method: "POST", body: JSON.stringify({ action, target }) });
      showToast(payload.message);
      showJob(payload.job);
      await refreshAll(false);
    } catch (error) { showToast(error.message, "error"); }
  };

  const showJob = async (job) => {
    state.activeJob = job.id;
    $("#output-kicker").textContent = "TASK OUTPUT";
    $("#output-title").textContent = job.name;
    $("#output-dialog").showModal();
    updateJobOutput(job);
    pollJob(job.id);
  };

  const updateJobOutput = (job) => {
    $("#output-title").textContent = job.name;
    $("#output-meta").innerHTML = `<span>${escapeHTML(job.target)}</span><span>${statusLabel(job.status)}</span><span>${formatTime(job.started_at || job.created_at)}</span>`;
    const output = (job.output || []).join("\n") || "任务正在排队…";
    updateOAuthCopyPanel(output);
    $("#cancel-job-button").hidden = !["queued", "running", "cancelling"].includes(job.status);
    return setOutputText(output);
  };

  const pollJob = async (jobId) => {
    window.clearTimeout(state.jobTimer);
    if (state.activeJob !== jobId) return;
    try {
      const payload = await api(`/jobs/${encodeURIComponent(jobId)}`);
      const outputCommitted = updateJobOutput(payload.job);
      if (["queued", "running"].includes(payload.job.status)) {
        state.jobTimer = window.setTimeout(() => pollJob(jobId), 1200);
      } else if (!outputCommitted) {
        state.jobTimer = window.setTimeout(() => pollJob(jobId), 500);
      } else {
        showToast(payload.job.status === "succeeded" ? "任务执行成功" : "任务执行失败", payload.job.status === "succeeded" ? "success" : "error");
        await refreshAll(false);
      }
    } catch (error) { showToast(error.message, "error"); }
  };

  const openExistingJob = async (jobId) => {
    try {
      const payload = await api(`/jobs/${encodeURIComponent(jobId)}`);
      showJob(payload.job);
    } catch (error) { showToast(error.message, "error"); }
  };

  const loadJobs = async () => {
    try {
      const payload = await api("/jobs");
      renderJobList($("#all-jobs"), payload.jobs || [], false);
    } catch (error) { if (state.authenticated) showToast(error.message, "error"); }
  };

  const showLogs = async (target) => {
    $("#output-kicker").textContent = "SERVICE LOGS";
    $("#output-title").textContent = `${target} 日志`;
    $("#output-meta").innerHTML = `<span>最近 200 行</span><span>${escapeHTML(target)}</span>`;
    updateOAuthCopyPanel("");
    setOutputText("正在读取…", true);
    $("#output-dialog").showModal();
    try {
      const payload = await api(`/logs?target=${encodeURIComponent(target)}`);
      setOutputText(payload.output || "暂无日志", true);
    } catch (error) { setOutputText(error.message, true); }
  };

  const handleKeyAction = async (button) => {
    const action = button.dataset.keyAction;
    const user = button.dataset.user || state.selectedUser;
    state.selectedUser = user;
    let path;
    let body;
    if (action === "create") {
      path = "/keys/create";
      body = { email: user, account: button.dataset.account };
    } else {
      path = `/keys/${action}`;
      body = { label: button.dataset.label };
      const confirmed = await askConfirm({
        title: action === "rotate" ? "轮换 Key？" : "停用 Key？",
        message: action === "rotate" ? "旧 Key 将立即失效，新 Key 只展示一次。" : "客户端使用该 Key 的后续请求将被拒绝。",
        label: action === "rotate" ? "确认轮换" : "确认停用",
        danger: action === "revoke"
      });
      if (!confirmed) return;
    }
    try {
      const payload = await api(path, { method: "POST", body: JSON.stringify(body) });
      if (payload.keys?.length) showSecrets(payload.keys, payload.message);
      else showToast(payload.message);
      await refreshAll(false);
    } catch (error) { showToast(error.message, "error"); }
  };

  const revokeUser = async (email) => {
    if (!await askConfirm({
      title: "停用用户的 API Key？",
      message: `${email} 的统一 API Key 会立即失效。`,
      label: "全部停用",
      danger: true
    })) return;
    try {
      const payload = await api("/users/revoke", { method: "POST", body: JSON.stringify({ email }) });
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) { showToast(error.message, "error"); }
  };

  const resetUserPassword = async (email) => {
    if (!await askConfirm({
      title: "重置用户密码？",
      message: `${email} 将恢复为系统默认初始密码，现有登录会话会立即失效；下次登录必须修改密码。`,
      label: "确认重置",
      danger: true
    })) return;
    try {
      const payload = await api("/users/reset-password", {
        method: "POST",
        body: JSON.stringify({ email })
      });
      showSecrets([{
        label: `${email}:usage-password`,
        account: "使用中心默认初始密码",
        user: email,
        key: payload.initial_password
      }], payload.message);
      await refreshAll(false);
    } catch (error) { showToast(error.message, "error"); }
  };

  const deleteUser = async (email) => {
    const user = state.users.find((item) => item.email === email);
    const activeKeys = user?.active_keys || 0;
    if (!await askConfirm({
      title: "删除用户与 API Key？",
      message: activeKeys
        ? `${email} 将从管理列表移除，其 ${activeKeys} 个有效 Key 会立即失效。历史用量与签发审计仍会保留。`
        : `${email} 将从管理列表移除。历史用量与签发审计仍会保留。`,
      label: "删除用户",
      danger: true
    })) return;
    try {
      const payload = await api("/users/delete", {
        method: "POST",
        body: JSON.stringify({ email, confirm: email, revoke_keys: true })
      });
      if (state.expandedUser === email) state.expandedUser = "";
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) { showToast(error.message, "error"); }
  };

  $("#auth-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    if (button) button.disabled = true;
    $("#auth-error").textContent = "";
    state.key = $("#management-key").value;
    try { await enterApp(true); }
    catch (error) { $("#auth-error").textContent = error.message; }
    finally {
      state.key = "";
      if (button) button.disabled = false;
    }
  });

  $("#add-user-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    if (button) button.disabled = true;
    $("#add-user-error").textContent = "";
    try {
      const payload = await api("/users", {
        method: "POST",
        body: JSON.stringify({
          email: $("#new-user-email").value,
          team_id: $("#new-user-team").value || null
        })
      });
      closeDialog("add-user-dialog");
      const secrets = [...(payload.keys || [])];
      if (payload.initial_password) {
        secrets.push({
          label: `${$("#new-user-email").value.trim().toLowerCase()}:usage-password`,
          account: "使用中心默认初始密码",
          user: $("#new-user-email").value.trim().toLowerCase(),
          key: payload.initial_password
        });
      }
      showSecrets(secrets, payload.message);
      await refreshAll(false);
    } catch (error) { $("#add-user-error").textContent = error.message; }
    finally { if (button) button.disabled = false; }
  });

  $("#user-quota-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    const mode = $('input[name="user-quota-mode"]:checked')?.value || "inherit";
    const rawTokens = $("#user-quota-custom-tokens").value.trim();
    $("#user-quota-error").textContent = "";
    if (mode === "custom" && (!/^\d+$/.test(rawTokens) || Number(rawTokens) <= 0)) {
      $("#user-quota-error").textContent = "自定义周额度必须为正整数";
      return;
    }
    const originalLabel = button?.textContent;
    if (button) {
      button.disabled = true;
      button.textContent = "正在保存…";
    }
    try {
      const payload = await api("/users/quota", {
        method: "PUT",
        body: JSON.stringify({
          email: state.selectedUser,
          mode,
          weekly_tokens: mode === "custom" ? Number(rawTokens) : null
        })
      });
      closeDialog("user-quota-dialog");
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      $("#user-quota-error").textContent = error.message;
    } finally {
      if (button) {
        button.disabled = false;
        button.textContent = originalLabel;
      }
    }
  });

  $("#user-quota-action-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const action = state.quotaAction;
    if (!action) return;
    const reason = $("#user-quota-action-reason").value.trim();
    const rawTokens = $("#user-quota-action-tokens").value.trim();
    const confirmation = $("#user-quota-action-confirm").value.trim();
    const error = $("#user-quota-action-error");
    error.textContent = "";
    if (!reason) {
      error.textContent = "请填写额度操作原因";
      $("#user-quota-action-reason").focus();
      return;
    }
    if (action.action === "add_bonus" && (!/^\d+$/.test(rawTokens) || Number(rawTokens) <= 0)) {
      error.textContent = "追加额度必须为正整数";
      $("#user-quota-action-tokens").focus();
      return;
    }
    if (action.confirmPhrase && confirmation !== action.confirmPhrase) {
      error.textContent = `请输入“${action.confirmPhrase}”`;
      $("#user-quota-action-confirm").focus();
      return;
    }
    const button = event.submitter;
    const originalLabel = button?.textContent;
    if (button) {
      button.disabled = true;
      button.textContent = "正在处理…";
    }
    const confirm = action.action === "add_bonus"
      ? "add_bonus"
      : action.scope === "all"
        ? "reset_all_current_week_usage"
        : "reset_current_week_usage";
    try {
      const payload = await api("/users/quota-actions", {
        method: "POST",
        body: JSON.stringify({
          action: action.action,
          scope: action.scope,
          users: action.scope === "selected" ? action.users : undefined,
          token_amount: action.action === "add_bonus" ? Number(rawTokens) : undefined,
          reason,
          confirm
        })
      });
      closeDialog("user-quota-action-dialog");
      closeDialog("user-quota-dialog");
      state.selectedUsers.clear();
      showToast(payload.message);
      await refreshAll(false);
    } catch (requestError) {
      error.textContent = requestError.message;
    } finally {
      if (button) {
        button.disabled = false;
        button.textContent = originalLabel;
      }
    }
  });

  $("#add-account-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    const originalLabel = button?.textContent;
    if (button) {
      button.disabled = true;
      button.textContent = "正在创建…";
    }
    $("#add-account-error").textContent = "";
    try {
      const payload = await api("/accounts", {
        method: "POST",
        body: JSON.stringify({
          id: $("#new-account-id").value,
          email: $("#new-account-email").value,
          proxy_mode: $("#new-account-proxy-mode").value,
          proxy_url: $("#new-account-proxy-url").value.trim()
        })
      });
      closeDialog("add-account-dialog");
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) { $("#add-account-error").textContent = error.message; }
    finally {
      if (button) {
        button.disabled = false;
        button.textContent = originalLabel;
      }
    }
  });

  $("#new-account-proxy-mode").addEventListener("change", (event) => {
    $("#new-account-proxy-url-field").hidden = event.target.value !== "custom";
    if (event.target.value === "custom") $("#new-account-proxy-url").focus();
  });

  $("#detail-account-proxy-mode").addEventListener("change", (event) => {
    $("#detail-account-proxy-url-field").hidden = event.target.value !== "custom";
    if (event.target.value === "custom") $("#detail-account-proxy-url").focus();
  });

  $("#quota-reset-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    const originalLabel = button?.textContent;
    const accountId = state.quotaResetAccount;
    const creditId = $("#quota-reset-credit").value;
    const selected = state.quotaResetCredits.find((credit) => credit.id === creditId);
    $("#quota-reset-error").textContent = "";
    if (!accountId || !selected) {
      $("#quota-reset-error").textContent = "所选重置额度无效，请关闭后重新选择";
      return;
    }
    if (button) {
      button.disabled = true;
      button.textContent = "正在重置…";
    }
    try {
      const payload = await api("/accounts/reset-quota", {
        method: "POST",
        body: JSON.stringify({
          account: accountId,
          credit_id: selected.id,
          confirm: accountId
        })
      });
      closeDialog("quota-reset-dialog");
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      $("#quota-reset-error").textContent = error.message;
      await refreshAll(false);
    } finally {
      if (button) {
        button.disabled = false;
        button.textContent = originalLabel;
      }
    }
  });

  $("#account-update-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    if (button) button.disabled = true;
    $("#account-update-error").textContent = "";
    try {
      const account = state.accounts.find((item) => item.id === state.selectedAccount);
      if (!account) throw new Error("CPA 账号不存在，请刷新后重试");
      const newId = $("#detail-account-new-id").value.trim().toLowerCase();
      const email = $("#detail-account-email").value.trim().toLowerCase();
      const proxyMode = $("#detail-account-proxy-mode").value;
      const proxyUrl = $("#detail-account-proxy-url").value.trim();
      const proxyChanged = proxyMode !== account.proxy_mode || Boolean(proxyUrl);
      if (newId !== state.selectedAccount || proxyChanged) {
        const changes = [];
        if (newId !== state.selectedAccount) changes.push(`${state.selectedAccount} 将迁移为 ${newId}`);
        if (proxyChanged) changes.push("出口代理设置将更新");
        const confirmed = await askConfirm({
          title: newId !== state.selectedAccount ? "修改业务 CPA？" : "修改出口代理？",
          message: `${changes.join("；")}。相关容器会短暂重启，OAuth、日志和 Key 关联会保留。`,
          label: "确认修改",
          danger: false
        });
        if (!confirmed) return;
      }
      const payload = await api("/accounts/update", {
        method: "POST",
        body: JSON.stringify({
          id: state.selectedAccount,
          new_id: newId,
          confirm: newId !== state.selectedAccount ? state.selectedAccount : "",
          email,
          proxy_mode: proxyMode,
          proxy_url: proxyUrl
        })
      });
      closeDialog("account-detail-dialog");
      showToast(payload.message);
      state.selectedAccount = newId;
      if (state.expandedAccount === account.id) state.expandedAccount = newId;
      if (newId !== account.id) {
        state.accountUsageBreakdowns.clear();
        state.accountUsageBreakdownLoading.clear();
        state.accountUsageBreakdownErrors.clear();
      }
      await refreshAll(false);
    } catch (error) { $("#account-update-error").textContent = error.message; }
    finally { if (button) button.disabled = false; }
  });

  $("#account-policy-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    const account = state.accounts.find((item) => item.id === state.accountPolicyAccount);
    if (!account) {
      $("#account-policy-error").textContent = "CPA 账号不存在，请关闭后刷新";
      return;
    }
    const enabling = !account.group_enabled;
    const fallback = $("#account-policy-fallback").value || null;
    if (!enabling && Number(account.routed_users) > 0 && !fallback) {
      $("#account-policy-error").textContent = "请选择现有用户迁移到的备用 CPA";
      return;
    }
    if (button) button.disabled = true;
    try {
      const payload = await api("/accounts/policy", {
        method: "POST",
        body: JSON.stringify({
          id: account.id,
          group_enabled: enabling,
          default_group: false,
          fallback_account: fallback
        })
      });
      closeDialog("account-policy-dialog");
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      $("#account-policy-error").textContent = error.message;
    } finally {
      if (button) button.disabled = false;
    }
  });

  $("#clear-account-auth-button").addEventListener("click", async () => {
    const accountId = state.selectedAccount;
    if (!await askConfirm({
      title: "清除 OAuth 授权？",
      message: `${accountId} 的本地授权文件会先归档再清除，容器随后重启。用户 Key 不会被删除。`,
      label: "清除授权",
      danger: true
    })) return;
    try {
      const payload = await api("/accounts/clear-auth", {
        method: "POST",
        body: JSON.stringify({ id: accountId, confirm: accountId })
      });
      closeDialog("account-detail-dialog");
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) { showToast(error.message, "error"); }
  });

  const rebalanceAccountUsers = async (accountId, button) => {
    const account = state.accounts.find((item) => item.id === accountId);
    if (!account || Number(account.routed_users) <= 0) return;
    const confirmed = await askConfirm({
      title: `迁移 ${account.id} 的全部用户？`,
      message: `系统会先刷新所有账号的官方额度，再将当前 ${formatNumber(account.routed_users)} 位用户按自动切换算法分配到其他可用账号。已经开始的请求不会被重放。`,
      label: "确认迁移",
      danger: false
    });
    if (!confirmed) return;
    const originalLabel = button?.textContent || "迁移全部用户";
    if (button) {
      button.disabled = true;
      button.textContent = "正在刷新额度并迁移…";
    }
    try {
      const payload = await api("/accounts/rebalance", {
        method: "POST",
        body: JSON.stringify({ id: account.id, confirm: account.id })
      });
      showToast(payload.message);
      await refreshAll(false);
    } catch (error) {
      showToast(error.message, "error");
    } finally {
      if (button) {
        button.disabled = false;
        button.textContent = originalLabel;
      }
    }
  };

  $("#delete-account-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    if (button) button.disabled = true;
    $("#delete-account-error").textContent = "";
    try {
      const payload = await api("/accounts/delete", {
        method: "POST",
        body: JSON.stringify({
          id: state.selectedAccount,
          confirm: $("#delete-account-confirm").value,
          revoke_keys: $("#delete-account-revoke-keys").checked,
          fallback_account: $("#delete-account-fallback").value || null
        })
      });
      closeDialog("delete-account-dialog");
      closeDialog("account-detail-dialog");
      showToast(payload.message);
      state.selectedAccount = "";
      await refreshAll(false);
    } catch (error) { $("#delete-account-error").textContent = error.message; }
    finally { if (button) button.disabled = false; }
  });

  $("#management-key-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    const newKey = $("#new-management-key").value;
    const confirmation = $("#confirm-management-key").value;
    $("#management-key-error").textContent = "";
    if (newKey !== confirmation) {
      $("#management-key-error").textContent = "两次输入的管理密钥不一致";
      return;
    }
    if (button) {
      button.disabled = true;
      button.textContent = "正在更新…";
    }
    try {
      const payload = await api("/settings/management-key", {
        method: "POST",
        body: JSON.stringify({ new_key: newKey, confirmation })
      });
      closeDialog("management-key-dialog");
      resetToAuth(payload.message);
    } catch (error) { $("#management-key-error").textContent = error.message; }
    finally {
      if (button) {
        button.disabled = false;
        button.textContent = "更新并重新进入";
      }
    }
  });

  $("#initial-password-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    const initialPassword = $("#new-initial-password").value;
    const confirmation = $("#confirm-initial-password").value;
    $("#initial-password-error").textContent = "";
    if (initialPassword !== confirmation) {
      $("#initial-password-error").textContent = "两次输入的初始密码不一致";
      return;
    }
    if (button) {
      button.disabled = true;
      button.textContent = "正在保存…";
    }
    try {
      const payload = await api("/settings/initial-password", {
        method: "POST",
        body: JSON.stringify({ initial_password: initialPassword, confirmation })
      });
      closeDialog("initial-password-dialog");
      $("#new-initial-password").value = "";
      $("#confirm-initial-password").value = "";
      state.settings.initial_password_configured = Boolean(payload.configured);
      renderSettings();
      showToast(payload.message);
    } catch (error) { $("#initial-password-error").textContent = error.message; }
    finally {
      if (button) {
        button.disabled = false;
        button.textContent = "安全保存";
      }
    }
  });

  $("#configuration-form").addEventListener("input", (event) => {
    if (event.target.matches("[data-configuration-key]")) {
      const field = configurationField(event.target.dataset.configurationKey);
      if (field) {
        state.configurationDraft[field.key] = field.type === "boolean"
          ? event.target.checked
          : event.target.value;
        if (field.type === "color") {
          const normalized = normalizedConfigurationValue(field, event.target.value);
          const valid = /^#[0-9a-f]{6}$/i.test(normalized);
          if (valid) {
            state.configurationDraft[field.key] = normalized;
            $$('[data-configuration-key]').filter(
              (input) => input !== event.target && input.dataset.configurationKey === field.key
            ).forEach((input) => {
              input.value = normalized;
            });
          }
          drawReasoningEffortColorPreview();
        }
        if (field.type === "boolean") {
          const label = event.target.closest("label")?.querySelector("span");
          if (label) label.textContent = event.target.checked ? "已启用" : "已关闭";
        }
        if (field.type === "choice") {
          const control = event.target.closest(".configuration-choice-control");
          const address = control?.querySelector("[data-choice-address]");
          const addressLabel = control?.querySelector("[data-choice-address-label]");
          const pending = normalizedConfigurationValue(field, state.configurationDraft[field.key]) !== state.configurationOriginal[field.key];
          if (address) address.textContent = event.target.value;
          if (addressLabel) addressLabel.textContent = pending ? "待切换地址" : "当前地址";
        }
        event.target.closest("[data-configuration-field]")?.classList.toggle(
          "configuration-field-dirty",
          normalizedConfigurationValue(field, state.configurationDraft[field.key]) !== state.configurationOriginal[field.key]
        );
        renderConfigurationChangeSummary();
        renderConfigurationNavigation();
        renderSettingsSectionSelect();
      }
    }
    if (event.target.matches("#notification-webhook-url")) {
      state.notificationWebhookDraft = event.target.value;
      state.notificationWebhookDirty = true;
      $("#notification-webhook-error").textContent = "";
    }
  });

  document.addEventListener("input", (event) => {
    if (event.target.matches("[data-token-input]")) {
      updateTokenInputPreview(event.target);
    }
  });
  document.addEventListener("change", (event) => {
    if (event.target.matches("#branding-logo-file")) uploadBrandingLogo(event.target.files?.[0]);
  });
  $("#configuration-form").addEventListener("keydown", (event) => {
    if (event.target.matches("#notification-webhook-url") && event.key === "Enter") {
      event.preventDefault();
      saveNotificationWebhook($("#save-notification-webhook"));
    }
  });

  $("#configuration-search-input").addEventListener("input", (event) => {
    state.configurationSearch = event.target.value;
    renderConfigurationSearchResults();
  });

  $("#settings-section-select").addEventListener("change", (event) => {
    const [kind, ...parts] = event.target.value.split(":");
    const value = parts.join(":");
    if (kind === "configuration") {
      selectConfigurationGroup(value);
      return;
    }
    state.settingsSection = value;
    state.configurationSearch = "";
    $("#configuration-search-input").value = "";
    renderSettingsWorkspace();
    $(".settings-workspace-content").scrollTop = 0;
  });

  $("#configuration-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const button = event.submitter;
    const values = configurationValuesFromDraft();
    const changedFields = configurationChangedFields();
    if (!changedFields.length) return;
    const colorChanged = changedFields.some((field) => field.key.startsWith(REASONING_COLOR_PREFIX));
    const modes = new Set(changedFields.map((field) => field.apply_mode));
    const effects = [];
    if (modes.has("accounts")) effects.push("业务 CPA 会依次重建");
    if (modes.has("collector")) effects.push("用量采集器会重启");
    if (modes.has("quota")) effects.push("用户额度将在下次采集后生效");
    if (modes.has("deployment")) effects.push("部署参数仅保存，等待下次重建");
    const confirmed = !effects.length || await askConfirm({
      title: `保存 ${changedFields.length} 项配置？`,
      message: effects.join("；") + "。如果应用失败，系统会尝试恢复原配置。",
      label: "保存并应用"
    });
    if (!confirmed) return;
    const changedValues = Object.fromEntries(
      changedFields.map((field) => [field.key, values[field.key]])
    );
    $("#configuration-error").textContent = "";
    if (button) {
      button.disabled = true;
      button.textContent = "正在保存…";
    }
    try {
      const payload = await api("/settings/configuration", {
        method: "POST",
        body: JSON.stringify({ values: changedValues, confirm: "save" })
      });
      showToast(payload.message);
      changedFields.forEach((field) => {
        state.configurationOriginal[field.key] = values[field.key];
        state.configurationDraft[field.key] = values[field.key];
      });
      state.configurationDirty = configurationChangedFields().length > 0;
      if (colorChanged) refreshReasoningEffortColorStylesheet();
      await refreshAll(false);
    } catch (error) {
      $("#configuration-error").textContent = error.message;
    } finally {
      if (button) button.textContent = "保存配置";
      renderConfigurationChangeSummary();
      renderConfigurationNavigation();
    }
  });

  document.addEventListener("click", (event) => {
    if (!event.target.closest(".enhanced-select")) closeEnhancedSelects();
    const dateFocus = event.target.closest("[data-custom-date-focus]");
    if (dateFocus) {
      const calendar = $(`[data-custom-calendar="${dateFocus.dataset.customDateFocus}"]`);
      (calendar?.querySelector(".selected") || calendar?.querySelector("button:not(:disabled)"))?.focus();
      return;
    }
    const calendarNav = event.target.closest("[data-calendar-nav]");
    if (calendarNav) {
      const [boundary, direction] = calendarNav.dataset.calendarNav.split(":");
      const month = customRangeDraft[boundary]?.month;
      if (month) {
        customRangeDraft[boundary].month = new Date(month.getFullYear(), month.getMonth() + Number(direction), 1);
        renderCustomCalendar(boundary);
      }
      return;
    }
    const calendarDay = event.target.closest("[data-calendar-date]");
    if (calendarDay) {
      const calendar = calendarDay.closest("[data-custom-calendar]");
      const boundary = calendar?.dataset.customCalendar;
      const [year, month, day] = calendarDay.dataset.calendarDate.split("-").map(Number);
      if (boundary && year && month && day) {
        customRangeDraft[boundary].date = new Date(year, month - 1, day);
        customRangeDraft[boundary].month = new Date(year, month - 1, 1);
        renderCustomCalendars();
        $("#custom-usage-range-error").textContent = "";
      }
      return;
    }
    const reasoningStrategyReset = event.target.closest("[data-reasoning-strategy-reset]");
    if (reasoningStrategyReset) {
      resetReasoningEffortStrategy(reasoningStrategyReset.dataset.reasoningStrategyReset);
      return;
    }
    const view = event.target.closest("[data-view]");
    if (view) setView(view.dataset.view);
    const userDetailRetry = event.target.closest("[data-user-detail-retry]");
    if (userDetailRetry) loadUserDetail(userDetailRetry.dataset.userDetailRetry, true);
    const configurationGroup = event.target.closest("[data-configuration-group]");
    if (configurationGroup) selectConfigurationGroup(configurationGroup.dataset.configurationGroup);
    const configurationResult = event.target.closest("[data-configuration-result]");
    if (configurationResult) {
      selectConfigurationGroup(
        configurationResult.dataset.configurationResultGroup,
        configurationResult.dataset.configurationResult
      );
    }
    const settingsSection = event.target.closest("[data-settings-section]");
    if (settingsSection) {
      state.settingsSection = settingsSection.dataset.settingsSection;
      state.configurationSearch = "";
      $("#configuration-search-input").value = "";
      renderSettingsWorkspace();
      $(".settings-workspace-content").scrollTop = 0;
    }
    const close = event.target.closest("[data-close-dialog]");
    if (close) closeDialog(close.dataset.closeDialog);
    const add = event.target.closest('[data-action="open-add-user"]');
    if (add) openAddUser();
    const openTeamUsage = event.target.closest("#team-usage-button");
    if (openTeamUsage) openTeamUsageDrawer();
    const classification = event.target.closest("[data-user-classification]");
    if (classification) openUserClassification([classification.dataset.userClassification]);
    const organizationMembers = event.target.closest("[data-organization-members]");
    if (organizationMembers) {
      openOrganizationMembersDialog(organizationMembers.dataset.organizationMembers);
    }
    const organizationEdit = event.target.closest("[data-organization-edit]");
    if (organizationEdit) {
      const item = state.teams.find((entry) => entry.id === organizationEdit.dataset.organizationEdit);
      if (item) openOrganizationCatalogDialog(item);
    }
    const organizationDelete = event.target.closest("[data-organization-delete]");
    if (organizationDelete && !organizationDelete.disabled) {
      deleteOrganizationTeam(organizationDelete.dataset.organizationDelete);
    }
    const addAccount = event.target.closest('[data-action="open-add-account"]');
    if (addAccount) openAddAccount();
    const saveNotification = event.target.closest("#save-notification-webhook");
    if (saveNotification) saveNotificationWebhook(saveNotification);
    const clearNotification = event.target.closest("#clear-notification-webhook");
    if (clearNotification) clearNotificationWebhook();
    const sendNotificationButton = event.target.closest("#send-notification-button");
    if (sendNotificationButton) sendAccountNotification(sendNotificationButton);
    const resetLogoButton = event.target.closest("#branding-logo-reset");
    if (resetLogoButton) resetBrandingLogo();
    const accountFocus = event.target.closest("[data-account-focus]");
    if (accountFocus) {
      toggleAccountExpansion(accountFocus.dataset.accountFocus, true);
    }
    const accountRow = event.target.closest("[data-account-row]");
    if (accountRow && !event.target.closest("button, a, input, select")) {
      toggleAccountExpansion(accountRow.dataset.accountRow);
    }
    const accountUsageRetry = event.target.closest("[data-account-usage-retry]");
    if (accountUsageRetry) {
      loadAccountUsageBreakdown(accountUsageRetry.dataset.accountUsageRetry, true);
    }
    const userRow = event.target.closest("[data-user-row]");
    if (userRow && !event.target.closest("button, a, input, select")) {
      toggleUserExpansion(userRow.dataset.userRow);
    }
    const userUsageRetry = event.target.closest("[data-user-usage-retry]");
    if (userUsageRetry) loadUserUsageBreakdown(userUsageRetry.dataset.userUsageRetry, true);
    const userQuota = event.target.closest("[data-user-quota]");
    if (userQuota) openUserQuota(userQuota.dataset.userQuota);
    const resetAllUserUsage = event.target.closest("#quota-reset-all-users");
    if (resetAllUserUsage) openUserQuotaAction("reset_usage", { scope: "all" });
    const userRevoke = event.target.closest("[data-user-revoke]");
    if (userRevoke) revokeUser(userRevoke.dataset.userRevoke);
    const userResetPassword = event.target.closest("[data-user-reset-password]");
    if (userResetPassword) resetUserPassword(userResetPassword.dataset.userResetPassword);
    const userDelete = event.target.closest("[data-user-delete]");
    if (userDelete) deleteUser(userDelete.dataset.userDelete);
    const accountEdit = event.target.closest("[data-account-edit]");
    if (accountEdit) openAccountDetail(accountEdit.dataset.accountEdit);
    const accountPolicy = event.target.closest("[data-account-policy]");
    if (accountPolicy) openAccountPolicy(accountPolicy.dataset.accountPolicy);
    const accountRebalance = event.target.closest("[data-account-rebalance]");
    if (accountRebalance) {
      rebalanceAccountUsers(
        accountRebalance.dataset.accountRebalance,
        accountRebalance
      );
    }
    const quotaReset = event.target.closest("[data-quota-reset]");
    if (quotaReset) openQuotaReset(quotaReset.dataset.quotaReset);
    const operation = event.target.closest("[data-operation]");
    if (operation) submitOperation(operation.dataset.operation, operation.dataset.target);
    const logs = event.target.closest("[data-log-target]");
    if (logs) showLogs(logs.dataset.logTarget);
    const job = event.target.closest("[data-job-id]");
    if (job) openExistingJob(job.dataset.jobId);
    const keyAction = event.target.closest("[data-key-action]");
    if (keyAction) handleKeyAction(keyAction);
    const copySecret = event.target.closest("[data-copy-secret]");
    if (copySecret) copyText(state.secrets[Number(copySecret.dataset.copySecret)]?.key || "");
    const monitorSort = event.target.closest("[data-monitor-sort]");
    if (monitorSort) {
      const table = monitorSort.closest("[data-monitor-table]");
      const kind = table?.dataset.monitorTable;
      if (kind && state.overviewUsageSort[kind]) {
        const field = monitorSort.dataset.monitorSort;
        if (state.overviewUsageSort[kind].field === field) {
          state.overviewUsageSort[kind].direction = state.overviewUsageSort[kind].direction === "asc"
            ? "desc"
            : "asc";
        } else {
          state.overviewUsageSort[kind] = {
            field,
            direction: monitorSortDefaultDirection(field)
          };
        }
        const body = table.querySelector("tbody");
        const series = state.overviewUsage?.[kind === "account" ? "accounts" : "users"] || [];
        renderMonitorTable(body, series, kind);
      }
      return;
    }
    const sort = event.target.closest("[data-user-sort]");
    if (sort) {
      const field = sort.dataset.userSort;
      if (state.userSort.field === field) state.userSort.direction = state.userSort.direction === "asc" ? "desc" : "asc";
      else state.userSort = { field, direction: field === "email" ? "asc" : "desc" };
      state.userPage = 1;
      refreshView("users", false);
    }
    const userBreakdownSort = event.target.closest("[data-user-breakdown-sort]");
    if (userBreakdownSort) {
      const field = userBreakdownSort.dataset.userBreakdownSort;
      if (state.userUsageBreakdownSort.field === field) {
        state.userUsageBreakdownSort.direction = state.userUsageBreakdownSort.direction === "asc" ? "desc" : "asc";
      } else {
        state.userUsageBreakdownSort = {
          field,
          direction: ["account", "combination"].includes(field) ? "asc" : "desc"
        };
      }
      renderUsers();
      return;
    }
    const userAccountSort = event.target.closest("[data-user-account-sort]");
    if (userAccountSort) {
      const field = userAccountSort.dataset.userAccountSort;
      if (state.userAccountSort.field === field) {
        state.userAccountSort.direction = state.userAccountSort.direction === "asc" ? "desc" : "asc";
      } else {
        state.userAccountSort = {
          field,
          direction: ["account", "status"].includes(field) ? "asc" : "desc"
        };
      }
      renderUsers();
      return;
    }
    const accountSort = event.target.closest("[data-account-sort]");
    if (accountSort) {
      const field = accountSort.dataset.accountSort;
      if (state.accountSort.field === field) {
        state.accountSort.direction = state.accountSort.direction === "asc" ? "desc" : "asc";
      } else {
        state.accountSort = {
          field,
          direction: ["account", "runtime", "auth", "quota"].includes(field) ? "asc" : "desc"
        };
      }
      renderAccounts();
    }
    const page = event.target.closest("[data-user-page]");
    if (page) {
      state.userPage = Number(page.dataset.userPage) || 1;
      state.expandedUser = "";
      refreshView("users", false);
    }
    if (event.target.closest("#user-page-prev") && state.userPage > 1) {
      state.userPage -= 1;
      state.expandedUser = "";
      refreshView("users", false);
    }
    if (event.target.closest("#user-page-next")) {
      state.userPage += 1;
      state.expandedUser = "";
      refreshView("users", false);
    }
    if (event.target.closest("#organization-page-prev") && state.organizationPage > 1) {
      state.organizationPage -= 1;
      loadOrganizationTeamMembers();
    }
    if (event.target.closest("#organization-page-next") && state.organizationPage < state.organizationPagination.total_pages) {
      state.organizationPage += 1;
      loadOrganizationTeamMembers();
    }
  });

  document.addEventListener("keydown", (event) => {
    const accountRow = event.target.closest("[data-account-row]");
    if (accountRow && event.target === accountRow && ["Enter", " "].includes(event.key)) {
      event.preventDefault();
      toggleAccountExpansion(accountRow.dataset.accountRow);
      return;
    }
    const userRow = event.target.closest("[data-user-row]");
    if (!userRow || event.target !== userRow || !["Enter", " "].includes(event.key)) return;
    event.preventDefault();
    toggleUserExpansion(userRow.dataset.userRow);
  });

  document.addEventListener("change", (event) => {
    if (event.target.matches("[data-user-select]")) {
      const email = event.target.dataset.userSelect;
      if (event.target.checked) state.selectedUsers.add(email);
      else state.selectedUsers.delete(email);
      renderUsers();
      return;
    }
    if (event.target.matches("#user-select-page")) {
      $$("[data-user-select]").forEach((checkbox) => {
        if (event.target.checked) state.selectedUsers.add(checkbox.dataset.userSelect);
        else state.selectedUsers.delete(checkbox.dataset.userSelect);
      });
      renderUsers();
      return;
    }
    if (event.target.matches("[data-organization-user]")) {
      const user = state.organizationUsers.find((item) => item.email === event.target.dataset.organizationUser);
      if (event.target.checked) state.organizationSelectedUsers.set(user.email, user.team_id || null);
      else state.organizationSelectedUsers.delete(event.target.dataset.organizationUser);
      state.organizationAllMatches = false;
      renderOrganizationTeamMembers();
      return;
    }
    if (event.target.matches("#organization-select-page")) {
      state.organizationUsers.forEach((user) => {
        if (event.target.checked) state.organizationSelectedUsers.set(user.email, user.team_id || null);
        else state.organizationSelectedUsers.delete(user.email);
      });
      state.organizationAllMatches = false;
      renderOrganizationTeamMembers();
      return;
    }
    if (event.target.matches('input[name="user-quota-mode"]')) {
      updateUserQuotaMode();
      return;
    }
    const accountFilter = event.target.closest("[data-user-usage-account]");
    if (!accountFilter) return;
    const email = accountFilter.dataset.userUsageAccount;
    state.userUsageAccountFilters.set(email, accountFilter.value);
    loadUserUsageBreakdown(email, true);
  });

  document.addEventListener("pointerover", (event) => {
    const segment = event.target.closest(".usage-model-table .account-model-progress-segment, .team-combination-list .account-model-progress-segment");
    if (segment) showUserUsageTooltip(segment);
  });
  document.addEventListener("pointerout", (event) => {
    const segment = event.target.closest(".usage-model-table .account-model-progress-segment, .team-combination-list .account-model-progress-segment");
    if (segment && !segment.contains(event.relatedTarget)) hideUserUsageTooltip();
  });
  document.addEventListener("focusin", (event) => {
    const segment = event.target.closest(".usage-model-table .account-model-progress-segment, .team-combination-list .account-model-progress-segment");
    if (segment) showUserUsageTooltip(segment);
  });
  document.addEventListener("focusout", (event) => {
    if (event.target.closest(".usage-model-table .account-model-progress-segment, .team-combination-list .account-model-progress-segment")) hideUserUsageTooltip();
  });

  $("#add-user-button").addEventListener("click", openAddUser);
  $("#manage-organization-button").addEventListener("click", openOrganizationCatalog);
  $("#organization-select-matches").addEventListener("click", () => {
    loadAllOrganizationMatches().catch((error) => { $("#organization-team-error").textContent = error.message; });
  });
  $("#organization-selection-clear").addEventListener("click", () => {
    state.organizationSelectedUsers.clear();
    state.organizationAllMatches = false;
    renderOrganizationTeamMembers();
  });
  $("#organization-team-assign").addEventListener("click", () => submitOrganizationTeamAssignment("join"));
  $("#organization-team-remove").addEventListener("click", () => submitOrganizationTeamAssignment("remove"));
  $("#organization-team-move").addEventListener("click", () => submitOrganizationTeamAssignment("move"));
  $("#user-selection-clear").addEventListener("click", () => {
    state.selectedUsers.clear();
    renderUsers();
  });
  $("#user-bulk-team").addEventListener("click", () => {
    openUserClassification([...state.selectedUsers]);
  });
  $("#user-bulk-restore-default").addEventListener("click", () => {
    restoreUserQuotaDefaults([...state.selectedUsers]);
  });
  $("#user-bulk-reset-usage").addEventListener("click", () => {
    openUserQuotaAction("reset_usage", {
      users: [...state.selectedUsers]
    });
  });
  $("#user-quota-add-bonus").addEventListener("click", () => {
    openUserQuotaAction("add_bonus", { users: [state.selectedUser] });
  });
  $("#user-quota-restore-default").addEventListener("click", () => {
    restoreUserQuotaDefaults([state.selectedUser]);
  });
  $("#user-quota-reset-usage").addEventListener("click", () => {
    openUserQuotaAction("reset_usage", { users: [state.selectedUser] });
  });
  $("#add-account-button").addEventListener("click", openAddAccount);
  $("#open-delete-account-button").addEventListener("click", openDeleteAccount);
  $("#rotate-management-key-button").addEventListener("click", () => {
    $("#new-management-key").value = "";
    $("#confirm-management-key").value = "";
    $("#management-key-error").textContent = "";
    $("#management-key-dialog").showModal();
    $("#new-management-key").focus();
  });
  $("#set-initial-password-button").addEventListener("click", () => {
    $("#new-initial-password").value = "";
    $("#confirm-initial-password").value = "";
    $("#initial-password-error").textContent = "";
    $("#initial-password-dialog").showModal();
    $("#new-initial-password").focus();
  });
  $("#configuration-revert-button").addEventListener("click", () => {
    state.configurationDraft = { ...state.configurationOriginal };
    state.configurationDirty = false;
    $("#configuration-error").textContent = "";
    renderSettingsWorkspace();
  });
  let userSearchTimer = null;
  $("#user-search").addEventListener("input", () => {
    state.userPage = 1;
    state.expandedUser = "";
    window.clearTimeout(userSearchTimer);
    userSearchTimer = window.setTimeout(() => refreshView("users", false), 250);
  });
  $("#user-page-size").addEventListener("change", (event) => {
    state.userPageSize = Number(event.target.value) || 50;
    state.userPage = 1;
    state.expandedUser = "";
    refreshView("users", false);
  });
  $("#account-search").addEventListener("input", renderAccounts);
  $("#account-runtime-filter").addEventListener("change", (event) => {
    syncEnhancedSelect(event.target);
    renderAccounts();
  });
  $("#account-auth-filter").addEventListener("change", (event) => {
    syncEnhancedSelect(event.target);
    renderAccounts();
  });
  $("#account-usage-window").addEventListener("change", async (event) => {
    if (event.target.value === "custom") {
      event.target.value = state.accountUsageWindow;
      syncEnhancedSelect(event.target);
      openCustomUsageRange("account");
      return;
    }
    state.accountUsageWindow = event.target.value;
    syncEnhancedSelect(event.target);
    clearUsageRangeCaches("account");
    await refreshAll(false);
  });
  $("#user-usage-window").addEventListener("change", async (event) => {
    if (event.target.value === "custom") {
      event.target.value = state.userUsageWindow;
      syncEnhancedSelect(event.target);
      openCustomUsageRange("user");
      return;
    }
    state.userUsageWindow = event.target.value;
    syncEnhancedSelect(event.target);
    clearUsageRangeCaches("user");
    await refreshAll(false);
  });
  $("#user-team-filter").addEventListener("change", async (event) => {
    state.userTeamFilter = event.target.value;
    syncEnhancedSelect(event.target);
    state.userPage = 1;
    state.expandedUser = "";
    await refreshView("users", false);
  });
  let organizationSearchTimer = null;
  const scheduleOrganizationTeamLoad = () => {
    window.clearTimeout(organizationSearchTimer);
    state.organizationPage = 1;
    state.organizationSelectedUsers.clear();
    state.organizationAllMatches = false;
    organizationSearchTimer = window.setTimeout(() => loadOrganizationTeamMembers().catch((error) => { $("#organization-team-error").textContent = error.message; }), 220);
  };
  $("#organization-user-search").addEventListener("input", scheduleOrganizationTeamLoad);
  ["organization-user-scope", "organization-usage-state", "organization-usage-window"].forEach((id) => {
    $(`#${id}`).addEventListener("change", (event) => { syncEnhancedSelect(event.target); scheduleOrganizationTeamLoad(); });
  });
  $("#organization-team-search").addEventListener("input", renderOrganizationCatalog);
  $("#organization-team-status").addEventListener("change", renderOrganizationCatalog);
  $("#user-classification-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    await saveUserClassification();
  });
  $("#organization-create-team").addEventListener("click", () => openOrganizationCatalogDialog());
  $("#organization-catalog-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    await saveOrganizationCatalogItem();
  });
  $("#custom-usage-range-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    await applyCustomUsageRange();
  });
  ["start", "end"].forEach((boundary) => {
    $(`#custom-usage-${boundary}-time`).addEventListener("input", () => {
      $("#custom-usage-range-error").textContent = "";
      renderCustomRangePreview();
    });
  });
  $("#overview-usage-window").addEventListener("click", async (event) => {
    const button = event.target.closest("[data-overview-window]");
    if (!button) return;
    const nextWindow = button.dataset.overviewWindow;
    if (nextWindow === "custom") {
      openCustomUsageRange("overview");
      return;
    }
    state.overviewUsageWindow = nextWindow;
    state.overviewUsage = null;
    renderOverviewUsageFilters();
    await loadOverviewUsage(false);
  });
  enhanceFilterSelects();
  applyTheme(document.documentElement.dataset.theme);
  $("#theme-toggle").addEventListener("click", () => {
    applyTheme(document.documentElement.dataset.theme === "dark" ? "light" : "dark", true);
  });
  $("#overview-usage-user-limit").addEventListener("change", async (event) => {
    state.overviewUsageUserLimit = event.target.value;
    state.overviewUsage = null;
    await loadOverviewUsage(false);
  });
  $("#overview-usage-refresh-interval").addEventListener("change", (event) => {
    state.overviewUsageRefresh = event.target.value;
    scheduleOverviewUsageRefresh();
  });
  $("#overview-usage-refresh").addEventListener("click", () => loadOverviewUsage(true));
  $("#refresh-button").addEventListener("click", () => refreshAll(true));
  $("#release-check-button").addEventListener("click", () => loadReleaseStatus(true));
  $("#refresh-jobs-button").addEventListener("click", loadJobs);
  $("#logout-button").addEventListener("click", async () => {
    try { await api("/session", { method: "DELETE" }); } catch { /* local cleanup below */ }
    resetToAuth("");
  });
  bindMonitorVariableControls();
  document.addEventListener("visibilitychange", () => {
    if (document.hidden) {
      window.clearTimeout(state.refreshTimer);
      window.clearTimeout(state.overviewUsageTimer);
      return;
    }
    refreshView(state.view, false);
  });
  $("#copy-all-secrets").addEventListener("click", () => {
    copyText(state.secrets.map((item) => `${item.label}\t${item.key}`).join("\n"));
  });
  $("#copy-oauth-url").addEventListener("click", () => copyText(state.oauthUrl));
  $("#copy-oauth-code").addEventListener("click", () => {
    const visibleCode = $("#oauth-code-value").textContent.trim();
    copyText(state.oauthCode || (visibleCode === "—" ? "" : visibleCode));
  });
  $("#copy-output-button").addEventListener("click", () => copyText($("#output-content").textContent));
  $("#cancel-job-button").addEventListener("click", async () => {
    if (!state.activeJob) return;
    try {
      const payload = await api("/jobs/cancel", {
        method: "POST",
        body: JSON.stringify({ id: state.activeJob })
      });
      updateJobOutput(payload.job);
      showToast(payload.message);
    } catch (error) { showToast(error.message, "error"); }
  });
  window.addEventListener("resize", drawReasoningEffortColorPreview);

  enterApp(false).catch(() => resetToAuth(""));
})();
