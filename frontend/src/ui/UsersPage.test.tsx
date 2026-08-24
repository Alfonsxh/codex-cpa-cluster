import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { UsersPage } from "./UsersPage";

describe("UsersPage", () => {
  it("loads only the user and team catalogs and preserves optimistic concurrency on assignment", async () => {
    const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.startsWith("/admin/api/users?")) {
        return Promise.resolve(jsonResponse({
          users: [{
            email: "alice@example.com",
            status: "active",
            active_keys: 1,
            active_accounts: 2,
            total_records: 3,
            created_at: 100,
            updated_at: 200,
            route_account_id: "beta",
            team_id: "team_platform",
            team: { id: "team_platform", name: "Platform", description: "Core" },
            team_membership_version: 4
          }],
          pagination: { page: 1, page_size: 50, total: 1, total_pages: 1 },
          generated_at: 300
        }));
      }
      if (path === "/admin/api/teams") {
        return Promise.resolve(jsonResponse({
          teams: [{
            id: "team_platform",
            name: "Platform",
            description: "Core",
            user_count: 1,
            created_at: 100,
            updated_at: 200
          }]
        }));
      }
      if (path === "/admin/api/users/team" && init?.method === "PUT") {
        return Promise.resolve(jsonResponse({ message: "已更新 1 位用户的团队归属" }));
      }
      if (path === "/admin/api/users" && init?.method === "POST") {
        return Promise.resolve(jsonResponse({
          message: "用户已创建",
          user: {
            user: "new@example.com",
            api_key: "one-time-api-key",
            initial_password: "one-time-password",
            team_id: null,
            accounts: 2,
            snapshot_generation: "generation-test"
          }
        }, { status: 201 }));
      }
      if (path === "/admin/api/keys/rotate" && init?.method === "POST") {
        return Promise.resolve(jsonResponse({
          message: "API Key 已轮换",
          key: { api_key: "rotated-one-time-key", snapshot_generation: "generation-test" }
        }));
      }
      return Promise.reject(new Error(`unexpected request: ${path}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <UsersPage csrfToken="csrf-test" />
      </QueryClientProvider>
    );

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    const readPaths = fetchMock.mock.calls.map(([input]) => String(input));
    expect(readPaths).toContain("/admin/api/users?page=1&page_size=50");
    expect(readPaths).toContain("/admin/api/teams");
    expect(readPaths.some((path) => /accounts|usage|logs/.test(path))).toBe(false);

    await user.click(screen.getByRole("button", { name: "调整 alice@example.com 的团队" }));
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("调整用户团队")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存团队归属" }));
    expect(await screen.findByText("已更新 1 位用户的团队归属")).toBeInTheDocument();

    const mutation = fetchMock.mock.calls.find(
      ([input, init]) => String(input) === "/admin/api/users/team" && init?.method === "PUT"
    );
    expect(mutation).toBeDefined();
    expect(JSON.parse(String(mutation?.[1]?.body))).toEqual({
      email: "alice@example.com",
      team_id: "team_platform",
      expected_team_id: "team_platform"
    });
    expect(new Headers(mutation?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(5));

    await user.click(screen.getByRole("button", { name: "新增用户" }));
    await user.type(screen.getByRole("textbox", { name: "新增用户邮箱" }), "new@example.com");
    await user.click(screen.getByRole("button", { name: "创建并发布 API Key" }));
    expect(await screen.findByText("已创建 new@example.com")).toBeInTheDocument();
    expect(screen.getByDisplayValue("one-time-api-key")).toBeInTheDocument();
    expect(screen.getByDisplayValue("one-time-password")).toBeInTheDocument();
    const createRequest = fetchMock.mock.calls.find(
      ([input, init]) => String(input) === "/admin/api/users" && init?.method === "POST"
    );
    expect(JSON.parse(String(createRequest?.[1]?.body))).toEqual({
      email: "new@example.com",
      team_id: null
    });
    expect(new Headers(createRequest?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");
    await user.click(screen.getByRole("button", { name: "我已安全保存" }));

    await user.click(screen.getByRole("button", { name: "管理 alice@example.com" }));
    await user.click(await screen.findByText("轮换 API Key"));
    expect(screen.getByText(/新 Key 的 Gateway 快照激活后/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认执行" }));
    expect(await screen.findByText("已轮换 alice@example.com")).toBeInTheDocument();
    expect(screen.getByDisplayValue("rotated-one-time-key")).toBeInTheDocument();
    const rotateRequest = fetchMock.mock.calls.find(
      ([input, init]) => String(input) === "/admin/api/keys/rotate" && init?.method === "POST"
    );
    expect(JSON.parse(String(rotateRequest?.[1]?.body))).toEqual({
      email: "alice@example.com",
      confirm: "rotate"
    });
  });

  it("loads and changes one user quota only when the quota modal is opened", async () => {
    let quotaMode: "inherit" | "custom" = "inherit";
    let quotaTokens: number | null = null;
    const fetchMock = vi.fn().mockImplementation((input: RequestInfo | URL, init?: RequestInit) => {
      const path = String(input);
      if (path.startsWith("/admin/api/users?")) {
        return Promise.resolve(jsonResponse({
          users: [{
            email: "alice@example.com",
            status: "active",
            active_keys: 1,
            active_accounts: 2,
            total_records: 2,
            created_at: 100,
            updated_at: 200,
            route_account_id: "alpha",
            team_id: null,
            team: null,
            team_membership_version: 0
          }],
          pagination: { page: 1, page_size: 50, total: 1, total_pages: 1 },
          generated_at: 300
        }));
      }
      if (path === "/admin/api/teams") {
        return Promise.resolve(jsonResponse({ teams: [] }));
      }
      if (path === "/admin/api/users/quota?email=alice%40example.com" && (!init?.method || init.method === "GET")) {
        return Promise.resolve(jsonResponse(quotaResult(quotaMode, quotaTokens)));
      }
      if (path === "/admin/api/users/quota" && init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { mode: "inherit" | "custom"; weekly_tokens: number | null };
        quotaMode = body.mode;
        quotaTokens = body.weekly_tokens;
        return Promise.resolve(jsonResponse({
          ...quotaResult(quotaMode, quotaTokens),
          message: "用户周额度策略已保存"
        }));
      }
      if (path === "/admin/api/users/quota?email=alice%40example.com" && init?.method === "DELETE") {
        quotaMode = "inherit";
        quotaTokens = null;
        return Promise.resolve(jsonResponse({
          ...quotaResult(quotaMode, quotaTokens),
          message: "已恢复继承组织默认周额度"
        }));
      }
      return Promise.reject(new Error(`unexpected request: ${path}`));
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <UsersPage csrfToken="csrf-test" />
      </QueryClientProvider>
    );

    expect(await screen.findByText("alice@example.com")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/users/quota"))).toBe(false);

    await user.click(screen.getByRole("button", { name: "管理 alice@example.com" }));
    await user.click(await screen.findByText("额度策略"));
    expect(await screen.findByText("本周已用")).toBeInTheDocument();
    expect(screen.getByText("追加 200 Token")).toBeInTheDocument();
    expect(screen.getByText(/临时项目扩容/)).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes("/users/quota"))).toHaveLength(1);
    await user.click(screen.getByRole("radio", { name: "自定义" }));
    await user.type(screen.getByRole("spinbutton", { name: "自定义每周 Token" }), "500");
    await user.click(screen.getByRole("button", { name: "保存额度策略" }));
    expect(await screen.findByText("用户周额度策略已保存")).toBeInTheDocument();

    const updateRequest = fetchMock.mock.calls.find(
      ([input, init]) => String(input) === "/admin/api/users/quota" && init?.method === "PUT"
    );
    expect(JSON.parse(String(updateRequest?.[1]?.body))).toEqual({
      email: "alice@example.com",
      mode: "custom",
      weekly_tokens: 500
    });
    expect(new Headers(updateRequest?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");

    await user.click(screen.getByRole("button", { name: "管理 alice@example.com" }));
    await user.click(await screen.findByText("额度策略"));
    expect(await screen.findByRole("button", { name: "恢复继承组织默认" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(
      ([input, init]) => String(input).includes("/users/quota?email=") && (!init?.method || init.method === "GET")
    )).toHaveLength(2);
    await user.click(screen.getByRole("button", { name: "恢复继承组织默认" }));
    expect(await screen.findByText("已恢复继承组织默认周额度")).toBeInTheDocument();
    const clearRequest = fetchMock.mock.calls.find(
      ([input, init]) => String(input).includes("/admin/api/users/quota?email=") && init?.method === "DELETE"
    );
    expect(new Headers(clearRequest?.[1]?.headers).get("X-CSRF-Token")).toBe("csrf-test");
  });
});

function quotaResult(mode: "inherit" | "custom", policyTokens: number | null) {
  const effectiveLimit = mode === "custom" ? policyTokens : 1_000;
  return {
    user: "alice@example.com",
    weekly_quota: {
      period: "natural_week",
      timezone: "UTC",
      week_start_at: 1_000,
      week_end_at: 2_000,
      limit_tokens: effectiveLimit,
      base_limit_tokens: effectiveLimit,
      bonus_tokens: 0,
      used_tokens: 100,
      weighted_used_tokens: 100,
      raw_used_tokens: 80,
      remaining_tokens: effectiveLimit === null ? null : effectiveLimit - 100,
      used_percent: effectiveLimit === null ? null : 10,
      limit_reached: false,
      policy_mode: mode,
      policy_tokens: policyTokens,
      policy_reset_at: mode === "custom" ? 2_000 : null,
      default_limit_tokens: 1_000,
      unlimited: false,
      quota_unit: "weighted_tokens",
      personal_policy_reset_enabled: true
    },
    adjustments: [{
      action: "bonus",
      token_amount: 200,
      reason: "临时项目扩容",
      created_at: 1_500,
      created_by: "admin"
    }]
  };
}

function jsonResponse(payload: unknown, init: ResponseInit = {}) {
  return new Response(JSON.stringify(payload), {
    ...init,
    status: init.status ?? 200,
    headers: { "Content-Type": "application/json", ...init.headers }
  });
}
