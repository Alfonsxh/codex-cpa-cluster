import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { OverviewPage } from "./OverviewPage";

describe("OverviewPage legacy dashboard contract", () => {
  it("uses fine-grained APIs while restoring the legacy metrics, monitor, tables, and activity layout", async () => {
    const summary = {
      generated_at: 1_800_000_000,
      source: "control-plane",
      summary: {
        accounts: 3,
        enabled_accounts: 2,
        users: 8,
        active_users: 7,
        active_keys: 7,
        routed_users: 6,
        unassigned_users: 2,
        teams: 3,
        incomplete_key_matrices: 1
      }
    };
    const usage = {
      generated_at: 1_800_000_000,
      window: "today",
      window_seconds: null,
      window_start_at: 1_799_996_400,
      window_start_at_by_account: null,
      unavailable_accounts: [],
      bucket_seconds: 900,
      buckets: [1_799_996_400, 1_799_997_300],
      accounts: Array.from({ length: 15 }, (_, index) => index === 0
        ? tokenSeries("alpha", [100, 200], [125, 250])
        : index === 1
          ? tokenSeries("cpa-02", [2_000_000, 0], [2_500_000, 0])
          : tokenSeries(`cpa-${String(index + 1).padStart(2, "0")}`, [0, 0], [0, 0])),
      users: [
        tokenSeries("alice@example.com", [100, 200], [300, 300]),
        tokenSeries("bob@example.com", [200, 200], [250, 250])
      ],
      selected_account: null,
      selected_user: null,
      selected_accounts: [],
      selected_users: [],
      user_limit: 10,
      collector: {
        status: "ok", heartbeat_at: 1_800_000_000, last_error: "", event_count: 2,
        collection_started_at: 1_799_996_400, usage_breakdown_started_at: 1_799_996_400, last_event_at: 1_800_000_000
      },
      cached: false
    };
    const status = {
      generated_at: 1_800_000_000,
      authorized_accounts: 2,
      running_services: 7,
      total_services: 8,
      requests_5m: 74,
      account_quota: {
        available: true,
        enabled_accounts: 3,
        known_accounts: 3,
        unknown_accounts: 0,
        average_used_percent: 47.47,
        average_remaining_percent: 52.53,
        equivalent_remaining_accounts: 1.58,
        exhausted_accounts: 0,
        high_risk_accounts: 0
      },
      warnings: []
    };
    const jobs = {
      jobs: [{ id: "job-1", name: "重启 alpha", action: "restart", target: "alpha", status: "succeeded", created_at: 1_800_000_000 }]
    };
    const catalog = {
      generated_at: 1_800_000_000,
      accounts: usage.accounts.map((account) => ({ id: account.name, operational_status: { label: "可用", tone: "success" } })),
      users: [
        { email: "alice@example.com", status: "active" },
        { email: "bob@example.com", status: "active" }
      ]
    };
    const fetchMock = vi.fn().mockImplementation((input: string | URL | Request) => {
      const path = String(input);
      if (path.startsWith("/admin/api/overview/usage?")) return Promise.resolve(jsonResponse(usage));
      if (path === "/admin/api/overview/summary") return Promise.resolve(jsonResponse(summary));
      if (path === "/admin/api/overview/catalog") return Promise.resolve(jsonResponse(catalog));
      if (path === "/admin/api/overview/status") return Promise.resolve(jsonResponse(status));
      if (path === "/admin/api/runtime/jobs?limit=30") return Promise.resolve(jsonResponse(jobs));
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/overview"]}>
        <QueryClientProvider client={queryClient}>
          <OverviewPage />
        </QueryClientProvider>
      </MemoryRouter>
    );

    expect(await screen.findByText("1 个用户的统一 Key 账号矩阵不完整")).toBeInTheDocument();
    const metrics = screen.getByLabelText("关键指标");
    expect(within(metrics).getAllByRole("article")).toHaveLength(6);
    expect(within(metrics).getByText("有效用户")).toBeInTheDocument();
    expect(within(metrics).getByText("已授权 CPA")).toBeInTheDocument();
    expect(within(metrics).getByText("运行服务")).toBeInTheDocument();
    expect(within(metrics).getByText("5 分钟请求")).toBeInTheDocument();
    expect(within(metrics).getByText("2/3")).toBeInTheDocument();
    expect(within(metrics).getByText("7/8")).toBeInTheDocument();
    expect(within(metrics).getByText("74")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "账号周额度" })).toBeInTheDocument();
    expect(screen.getByText("47.5%")).toBeInTheDocument();
    expect(screen.getByText("1.6 个账号")).toBeInTheDocument();

    expect(await screen.findByRole("heading", { name: "所有账号未加权 Token 使用量" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "CPA 账号未加权 Token 使用趋势" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "用户加权 Token 使用趋势" })).toBeInTheDocument();
    expect(screen.getAllByText("alpha")).not.toHaveLength(0);
    expect(screen.getAllByText("alice@example.com")).not.toHaveLength(0);
    expect(screen.getByText("重启 alpha")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "全部用户" }));
    expect(await screen.findByPlaceholderText("搜索用户邮箱")).toHaveFocus();
    expect(await screen.findByTitle("alice@example.com")).toBeVisible();

    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/overview/usage?window=today&user_limit=10")).toBe(true);
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/overview/catalog")).toBe(true);
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("/release"))).toBe(false);

    expect(await screen.findByRole("img", { name: /所有账号未加权 Token 使用趋势/ })).toBeInTheDocument();
    expect(await screen.findByRole("img", { name: /分项未加权 Token 使用趋势：alpha/ })).toBeInTheDocument();

    const userTable = screen.getByLabelText("用户用量明细表格");
    const userRows = within(userTable).getAllByRole("row");
    expect(userRows[1]).toHaveTextContent("alice@example.com");
    expect(userRows[2]).toHaveTextContent("bob@example.com");
    expect(within(userRows[1]).getAllByText("加权")).toHaveLength(4);
    expect(within(userRows[1]).getAllByText("未加权")).toHaveLength(4);

    await user.click(screen.getByRole("button", { name: "6 小时" }));
    expect(await waitForRequest(fetchMock, "/admin/api/overview/usage?window=21600&user_limit=10")).toBe(true);

    await user.click(screen.getByRole("button", { name: "自定义" }));
    expect(await screen.findByText("Token 趋势自定义统计范围")).toBeInTheDocument();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    expect(screen.getByText("CUSTOM USAGE RANGE")).toBeInTheDocument();
    expect(screen.getByText(/查询包含开始时刻，不包含结束时刻。/)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /取\s*消/ }));
    expect(screen.getByRole("button", { name: "6 小时" })).toHaveAttribute("aria-pressed", "true");
    expect(usageRequests(fetchMock, "custom")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "自定义" }));
    await user.click(await screen.findByRole("button", { name: "应用范围" }));
    await waitForUsageWindow(fetchMock, "custom");
    const customRequest = new URL(usageRequests(fetchMock, "custom")[0], "http://preview.test");
    expect(Number(customRequest.searchParams.get("start_at"))).toBeLessThan(Number(customRequest.searchParams.get("end_at")));
    expect(customRequest.searchParams.get("user_limit")).toBe("10");
    expect(screen.queryByRole("button", { name: "自定义" })).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /范围内总量（按加权），当前降序/ }));
    expect(screen.getByRole("button", { name: /范围内总量（按加权），当前升序/ })).toBeInTheDocument();
  });
});

function tokenSeries(name: string, values: number[], weightedValues: number[]) {
  const total = values.reduce((sum, value) => sum + value, 0);
  const weightedTotal = weightedValues.reduce((sum, value) => sum + value, 0);
  return {
    name,
    values,
    current: values.at(-1) ?? 0,
    average: values.length ? Math.round(total / values.length) : 0,
    maximum: Math.max(...values, 0),
    total,
    weighted_values: weightedValues,
    weighted_current: weightedValues.at(-1) ?? 0,
    weighted_average: weightedValues.length ? Math.round(weightedTotal / weightedValues.length) : 0,
    weighted_maximum: Math.max(...weightedValues, 0),
    weighted_total: weightedTotal
  };
}

async function waitForRequest(fetchMock: ReturnType<typeof vi.fn>, expected: string) {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    if (fetchMock.mock.calls.some(([path]) => String(path) === expected)) return true;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  return false;
}

async function waitForUsageWindow(fetchMock: ReturnType<typeof vi.fn>, window: string) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    if (usageRequests(fetchMock, window).length) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error(`usage request for ${window} was not observed`);
}

function usageRequests(fetchMock: ReturnType<typeof vi.fn>, window: string) {
  return fetchMock.mock.calls
    .map(([path]) => String(path))
    .filter((path) => {
      const url = new URL(path, "http://preview.test");
      return url.pathname === "/admin/api/overview/usage" && url.searchParams.get("window") === window;
    });
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}
