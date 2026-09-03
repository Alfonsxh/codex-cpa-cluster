import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { UsersPage } from "./UsersPage";

describe("UsersPage legacy parity", () => {
  it("opens the create dialog from the first-run deep link", async () => {
    vi.stubGlobal("fetch", userFetchMock());
    renderUsers("/users?create=1");
    expect(await screen.findByRole("dialog", { name: /添加用户/ })).toBeInTheDocument();
  });

  it("loads the paginated eleven-column catalog and defers user and team detail until expansion", async () => {
    const fetchMock = userFetchMock();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    expect(screen.getByText("Platform", { selector: ".team-chip" })).toHaveClass("team-tag-style-rose");
    await waitFor(() => expect(requestPaths(fetchMock)).toContain("/admin/api/teams/usage?window=today"));
    expect(screen.getAllByRole("columnheader")).toHaveLength(11);
    expect(screen.getByRole("button", { name: /Token 用量，当前降序/ })).toBeInTheDocument();
    await user.type(screen.getByRole("searchbox", { name: "搜索用户" }), "alice{Enter}");
    await waitFor(() => expect(requestPaths(fetchMock).some((path) => path.includes("q=alice"))).toBe(true));
    expect(requestPaths(fetchMock).some((path) => path.includes("/users/detail"))).toBe(false);
    expect(requestPaths(fetchMock).some((path) => path.includes("/users/usage-breakdown"))).toBe(false);
    expect(requestPaths(fetchMock).some((path) => path.includes("/teams/usage-breakdown"))).toBe(false);

    const row = screen.getByText("alice@example.com").closest("tr");
    expect(row).not.toBeNull();
    await user.click(row!);
    expect(await screen.findByText("模型与推理分析")).toBeInTheDocument();
    expect(await screen.findByText("alpha", { selector: ".user-account-table .table-primary" })).toBeInTheDocument();
    await waitFor(() => {
      expect(requestPaths(fetchMock).some((path) => path.startsWith("/admin/api/users/detail?"))).toBe(true);
      expect(requestPaths(fetchMock).some((path) => path.startsWith("/admin/api/users/usage-breakdown?"))).toBe(true);
    });

    await user.click(screen.getByRole("button", { name: "团队：全部团队" }));
    await user.click(screen.getByRole("option", { name: "Platform" }));
    await user.click(screen.getByRole("button", { name: "Platform Token 用量" }));
    expect(await screen.findByText("Platform · Token 用量")).toBeInTheDocument();
    expect(await screen.findByText("活跃成员排行")).toBeInTheDocument();
    expect(requestPaths(fetchMock).some((path) => path.startsWith("/admin/api/teams/usage-breakdown?"))).toBe(true);
  });

  it("preserves team assignment, create-secret and key-rotation request contracts", async () => {
    const fetchMock = userFetchMock();
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "设置 alice@example.com 的团队" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存团队" }));
    expect(await screen.findByText("已更新 1 位用户的团队归属")).toBeInTheDocument();
    const assignment = request(fetchMock, "/admin/api/users/team", "PUT");
    expect(JSON.parse(String(assignment?.[1]?.body))).toEqual({
      email: "alice@example.com",
      team_id: "team_platform"
    });
    expect(new Headers(assignment?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");

    await user.click(screen.getByRole("button", { name: "添加用户" }));
    expect(await screen.findByText("企业邮箱后缀：@example.com")).toBeInTheDocument();
    expect(screen.getByRole("textbox", { name: "用户邮箱" })).toHaveAttribute("placeholder", "name@example.com");
    await user.type(screen.getByRole("textbox", { name: "用户邮箱" }), "new@example.com");
    await user.click(screen.getByRole("button", { name: "创建用户" }));
    expect(await screen.findByText("保存新生成的凭据")).toBeInTheDocument();
    expect(screen.getByText("one-time-api-key")).toBeInTheDocument();
    expect(screen.getByText("one-time-password")).toBeInTheDocument();
    const create = request(fetchMock, "/admin/api/users", "POST");
    expect(JSON.parse(String(create?.[1]?.body))).toEqual({ email: "new@example.com", team_id: null });
    await user.click(screen.getByRole("button", { name: "我已保存" }));
    expect(screen.queryByText("one-time-api-key")).not.toBeInTheDocument();

    const row = screen.getByText("alice@example.com").closest("tr");
    await user.click(row!);
    await user.click(await screen.findByRole("button", { name: "轮换唯一 Key" }));
    await user.click(screen.getByRole("button", { name: "确认轮换" }));
    expect(await screen.findByText("rotated-one-time-key")).toBeInTheDocument();
    const rotate = request(fetchMock, "/admin/api/keys/rotate", "POST");
    expect(JSON.parse(String(rotate?.[1]?.body))).toEqual({ label: "alice@example.com:alpha" });
    expect(new Headers(rotate?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");
  });

  it("hydrates quota from the list, fetches detail on demand and confirms batch restore", async () => {
    let resolveQuota: ((response: Response) => void) | undefined;
    const quotaResponse = new Promise<Response>((resolve) => { resolveQuota = resolve; });
    const fetchMock = userFetchMock((path, init) => {
      if (path === "/admin/api/users/quota?email=alice%40example.com" && (!init?.method || init.method === "GET")) {
        return quotaResponse;
      }
      return undefined;
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderUsers();

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "配置" }));
    expect(screen.getByText("当前加权上限")).toBeInTheDocument();
    expect(screen.getByText("基础额度")).toBeInTheDocument();
    expect(requestPaths(fetchMock).filter((path) => path.includes("/users/quota?email="))).toHaveLength(1);
    resolveQuota?.(jsonResponse(quotaResult()));
    expect(await screen.findByText("临时项目扩容")).toBeInTheDocument();

    await user.click(screen.getByRole("radio", { name: /自定义额度/ }));
    const tokenInput = screen.getByRole("spinbutton", { name: "每周 Token" });
    await user.clear(tokenInput);
    await user.type(tokenInput, "500");
    await user.click(screen.getByRole("button", { name: "保存额度策略" }));
    expect(await screen.findByText("用户周额度策略已保存")).toBeInTheDocument();
    const update = request(fetchMock, "/admin/api/users/quota", "PUT");
    expect(JSON.parse(String(update?.[1]?.body))).toEqual({
      email: "alice@example.com",
      mode: "custom",
      weekly_tokens: 500
    });

    await user.click(screen.getByRole("checkbox", { name: "选择 alice@example.com" }));
    const selectionBar = screen.getByText("已选择 1 位用户").closest(".user-selection-bar");
    expect(selectionBar).not.toBeNull();
    await user.click(within(selectionBar as HTMLElement).getByRole("button", { name: "恢复组织默认" }));
    expect(screen.getByText("恢复 1 位用户的组织默认额度？")).toBeInTheDocument();
    expect(request(fetchMock, "/admin/api/users/quota-actions", "POST")).toBeUndefined();
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "恢复组织默认" }));
    await waitFor(() => expect(request(fetchMock, "/admin/api/users/quota-actions", "POST")).toBeDefined());
    const restore = request(fetchMock, "/admin/api/users/quota-actions", "POST");
    expect(JSON.parse(String(restore?.[1]?.body))).toEqual({
      action: "restore_default",
      scope: "selected",
      users: ["alice@example.com"],
      confirm: "restore_default"
    });
  });
});

function renderUsers(entry = "/admin/users") {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={queryClient}>
        <UsersPage csrfToken="csrf-test" />
      </QueryClientProvider>
    </MemoryRouter>
  );
}

function userFetchMock(override?: (path: string, init?: RequestInit) => Promise<Response> | undefined) {
  return vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
    const path = String(input);
    const overridden = override?.(path, init);
    if (overridden) return overridden;
    if (path.startsWith("/admin/api/users?")) return Promise.resolve(jsonResponse(userCatalog()));
    if (path === "/site-config.json") {
      return Promise.resolve(jsonResponse({
        version: 1,
        product_name: "Codex CPA Cluster",
        short_name: "Codex CPA",
        environment_label: "Test",
        public_base_url: "https://cpa.example.com",
        allowed_email_domains: ["example.com"],
        provider_name: "Codex CPA",
        api_key_env: "CPA_API_KEY",
        default_model: "gpt-5.6-sol",
        logo: {
          custom: false,
          url: "/portal/assets/codex-cpa-cluster-logo.svg",
          content_type: "image/svg+xml",
          sha256: "",
          updated_at: null
        }
      }));
    }
    if (path.startsWith("/admin/api/teams/usage-breakdown?")) return Promise.resolve(jsonResponse(teamUsageBreakdown()));
    if (path.startsWith("/admin/api/teams/usage?")) return Promise.resolve(jsonResponse(teamUsageCatalog()));
    if (path.startsWith("/admin/api/users/detail?")) return Promise.resolve(jsonResponse(userDetail()));
    if (path.startsWith("/admin/api/users/usage-breakdown?")) return Promise.resolve(jsonResponse(usageBreakdown()));
    if (path === "/admin/api/users/team" && init?.method === "PUT") {
      return Promise.resolve(jsonResponse({ message: "已更新 1 位用户的团队归属" }));
    }
    if (path === "/admin/api/users" && init?.method === "POST") {
      return Promise.resolve(jsonResponse({
        message: "用户已创建",
        keys: [oneTimeKey("one-time-api-key", "new@example.com")],
        initial_password: "one-time-password",
        team_id: null
      }, { status: 201 }));
    }
    if (path === "/admin/api/keys/rotate" && init?.method === "POST") {
      return Promise.resolve(jsonResponse({ message: "API Key 已轮换", keys: [oneTimeKey("rotated-one-time-key", "alice@example.com")] }));
    }
    if (path === "/admin/api/users/quota?email=alice%40example.com" && (!init?.method || init.method === "GET")) {
      return Promise.resolve(jsonResponse(quotaResult()));
    }
    if (path === "/admin/api/users/quota" && init?.method === "PUT") {
      return Promise.resolve(jsonResponse({ ...quotaResult("custom", 500), message: "用户周额度策略已保存" }));
    }
    if (path === "/admin/api/users/quota-actions" && init?.method === "POST") {
      return Promise.resolve(jsonResponse({
        message: "已恢复所选用户的组织默认额度",
        action: "restore_default",
        scope: "selected",
        requested_users: 1,
        affected_users: 1,
        skipped_users: 0,
        skipped: []
      }));
    }
    return Promise.reject(new Error(`unexpected request: ${path}`));
  });
}

function userCatalog() {
  return {
    users: [baseUser()],
    accounts: { alpha: { email: "alpha@example.com" } },
    teams: [{ id: "team_platform", name: "Platform", description: "Core", tag_style: "rose", user_count: 1, created_at: 100, updated_at: 200 }],
    tags: [],
    collector: { status: "healthy", heartbeat_at: 300, last_success_at: 300, last_error: "", queue_depth: 0 },
    pagination: { page: 1, page_size: 50, total: 1, total_pages: 1 },
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    window_timezone: "Asia/Shanghai",
    summary_generated_at: 300,
    summary_cached: false
  };
}

function baseUser() {
  return {
    email: "alice@example.com",
    status: "active",
    active_keys: 1,
    active_accounts: 1,
    total_records: 3,
    created_at: 100,
    updated_at: 200,
    route_account_id: "alpha",
    team_id: "team_platform",
    team: { id: "team_platform", name: "Platform", description: "Core", tag_style: "rose" },
    team_membership_version: 4,
    account_count: 1,
    usage: metrics(1_200, 1_500),
    weekly_quota: weeklyQuota()
  };
}

function userDetail() {
  return {
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    window_timezone: "Asia/Shanghai",
    user: {
      ...baseUser(),
      accounts: [{
        account: "alpha",
        account_email: "alpha@example.com",
        status: "active",
        history_count: 3,
        key: { label: "alice@example.com:alpha", account: "alpha", account_email: "alpha@example.com", user: "alice@example.com", status: "active", created_at: 100, updated_at: 200, preview: "sk-…abcd" },
        usage: metrics(1_200, 1_500)
      }]
    }
  };
}

function usageBreakdown() {
  return {
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    collection_started_at: 90,
    effective_start_at: 100,
    definition: "user_model_reasoning_effort_tokens",
    account: null,
    user: "alice@example.com",
    totals: metrics(1_200, 1_500),
    models: [],
    reasoning_efforts: [],
    combinations: [{ ...metrics(1_200, 1_500), account: "alpha", model: "gpt-5.6", reasoning_effort: "high" }]
  };
}

function teamUsageCatalog() {
  return {
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    window_timezone: "Asia/Shanghai",
    attribution: "current_membership",
    teams: [{
      id: "team_platform",
      name: "Platform",
      description: "Core",
      tag_style: "rose",
      user_count: 1,
      current_user_count: 1,
      created_at: 100,
      updated_at: 200,
      usage: { ...metrics(1_200, 1_500), active_users: 1 }
    }]
  };
}

function teamUsageBreakdown() {
  return {
    generated_at: 300,
    window: "today",
    window_seconds: 3600,
    window_start_at: 100,
    window_end_at: 300,
    window_timezone: "Asia/Shanghai",
    definition: "team_model_reasoning_effort_tokens",
    team_id: "team_platform",
    attribution: "current_membership",
    totals: metrics(1_200, 1_500),
    users: [{ user: "alice@example.com", ...metrics(1_200, 1_500) }],
    accounts: [{ account: "alpha", ...metrics(1_200, 1_500) }],
    models: [{ model: "gpt-5.6", ...metrics(1_200, 1_500) }],
    combinations: [{ model: "gpt-5.6", reasoning_effort: "high", ...metrics(1_200, 1_500) }],
    series: { start_at: 100, end_at: 300, bucket_seconds: 60, buckets: [100, 160, 220], values: [200, 600, 700] }
  };
}

function weeklyQuota(mode: "inherit" | "custom" = "inherit", policyTokens: number | null = null) {
  const effectiveLimit = mode === "custom" ? policyTokens : 1_000;
  return {
    period: "natural_week",
    timezone: "Asia/Shanghai",
    week_start_at: 100,
    week_end_at: 700,
    limit_tokens: effectiveLimit,
    base_limit_tokens: effectiveLimit,
    bonus_tokens: 200,
    used_tokens: 100,
    weighted_used_tokens: 100,
    raw_used_tokens: 80,
    unweighted_used_tokens: 80,
    weighted_raw_used_tokens: 100,
    usage_reset_tokens: 0,
    remaining_tokens: effectiveLimit === null ? null : effectiveLimit - 100,
    used_percent: effectiveLimit === null ? null : 10,
    limit_reached: false,
    source: mode === "custom" ? "user_custom" : "default",
    policy_mode: mode,
    policy_tokens: policyTokens,
    policy_updated_at: mode === "custom" ? 200 : null,
    policy_updated_by: mode === "custom" ? "admin" : null,
    policy_reset_at: mode === "custom" ? 700 : null,
    default_limit_tokens: 1_000,
    unlimited: false,
    soft_limit: true,
    quota_unit: "weighted_tokens",
    adjustment_count: 1,
    personal_policy_reset_enabled: true
  };
}

function quotaResult(mode: "inherit" | "custom" = "inherit", policyTokens: number | null = null) {
  return {
    user: "alice@example.com",
    weekly_quota: weeklyQuota(mode, policyTokens),
    adjustments: [{ action: "bonus", token_amount: 200, reason: "临时项目扩容", created_at: 150, created_by: "admin" }]
  };
}

function metrics(totalTokens: number, weightedTokens: number) {
  return {
    request_count: 4,
    success_count: 3,
    failed_count: 1,
    input_tokens: 500,
    output_tokens: 300,
    reasoning_tokens: 300,
    cached_tokens: 100,
    total_tokens: totalTokens,
    weighted_tokens: weightedTokens,
    last_used_at: 250
  };
}

function oneTimeKey(key: string, user: string) {
  return { label: `${user}:alpha`, account: "alpha", account_email: "alpha@example.com", user, status: "active", created_at: 100, updated_at: 200, preview: "sk-…abcd", key };
}

function requestPaths(fetchMock: ReturnType<typeof userFetchMock>) {
  return fetchMock.mock.calls.map(([input]) => String(input));
}

function request(fetchMock: ReturnType<typeof userFetchMock>, path: string, method: string) {
  return fetchMock.mock.calls.find(([input, init]) => String(input) === path && init?.method === method);
}

function jsonResponse(payload: unknown, init: ResponseInit = {}) {
  return new Response(JSON.stringify(payload), {
    ...init,
    status: init.status ?? 200,
    headers: { "Content-Type": "application/json", ...init.headers }
  });
}
