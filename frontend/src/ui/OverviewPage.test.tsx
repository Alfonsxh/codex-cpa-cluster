import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { formatOverviewUsageRange, OverviewPage } from "./OverviewPage";

describe("OverviewPage legacy dashboard contract", () => {
  it("formats an exact custom range without dropping the exclusive end minute", () => {
    const startAt = Math.floor(Date.parse("2026-09-03T10:00:00+08:00") / 1000);
    const endAt = Math.floor(Date.parse("2026-09-03T11:00:00+08:00") / 1000);
    expect(formatOverviewUsageRange(startAt, endAt)).toMatch(
      /2026\/09\/03 10:00\s*—\s*2026\/09\/03 11:00/
    );
  });

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
      window_timezone: "Asia/Shanghai",
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
        tokenSeries("bob@example.com", [200, 200], [250, 250]),
        ...Array.from({ length: 13 }, (_, index) => tokenSeries(
          `user-${String(index + 3).padStart(2, "0")}@example.com`,
          [0, 0],
          [0, 0]
        ))
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
      users: usage.users.map((user) => ({ email: user.name, status: "active" }))
    };
    const fetchMock = vi.fn().mockImplementation((input: string | URL | Request) => {
      const path = String(input);
      if (path.startsWith("/admin/api/overview/usage?")) {
        const requestURL = new URL(path, "http://preview.test");
        const requestedLimit = Number(requestURL.searchParams.get("user_limit") ?? 10);
        const weighted = requestURL.searchParams.get("token_mode") === "weighted";
        const users = [...usage.users]
          .sort((left, right) => (weighted ? right.weighted_total - left.weighted_total : right.total - left.total)
            || left.name.localeCompare(right.name))
          .slice(0, requestedLimit);
        return Promise.resolve(jsonResponse({ ...usage, users, user_limit: requestedLimit }));
      }
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
    expect(within(metrics).getByText("CPA 账号")).toBeInTheDocument();
    expect(within(metrics).getByText("用户状态")).toBeInTheDocument();
    expect(within(metrics).getByText("Key 健康")).toBeInTheDocument();
    expect(within(metrics).getByText("团队覆盖")).toBeInTheDocument();
    expect(within(metrics).getByText("服务状态")).toBeInTheDocument();
    expect(within(metrics).getByText("5 分钟请求")).toBeInTheDocument();
    expect(within(metrics).getByText("启用 2 · 已授权 2")).toBeInTheDocument();
    expect(within(metrics).getAllByText("7/8")).toHaveLength(2);
    expect(within(metrics).getByText("74")).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "账号周额度" })).toBeInTheDocument();
    expect(screen.getByText("47.5%")).toBeInTheDocument();
    expect(screen.getByText("1.6 个账号")).toBeInTheDocument();

    expect(await screen.findByRole("heading", { name: "Token 使用" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "Token 统计口径" })).toBeInTheDocument();
    expect(screen.getByRole("tablist", { name: "Token 使用数据视角" })).toBeInTheDocument();
    expect(screen.getByRole("tab", { name: "全部账号" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel")).toHaveAttribute("aria-labelledby", "overview-token-tab-aggregate");
    expect(screen.getByRole("tabpanel")).toHaveClass("overview-token-data-scroll");
    expect(screen.queryByText("实际统计范围")).not.toBeInTheDocument();
    const windowSegments = screen.getByRole("group", { name: "Token 使用时间范围" });
    expect(windowSegments.closest(".overview-token-window-row")).toHaveTextContent(
      formatOverviewUsageRange(usage.window_start_at, usage.generated_at)
    );
    const refreshHeading = screen.getByText("自动刷新").closest(".overview-refresh-heading");
    expect(refreshHeading).toHaveTextContent("采集正常");
    expect(within(refreshHeading as HTMLElement).getByLabelText("最近采集时间")).toHaveTextContent(
      /\d{2}\/\d{2} \d{2}:\d{2}/
    );
    expect(screen.getByRole("tablist", { name: "Token 使用数据视角" }).closest("header"))
      .not.toContainElement(screen.getByLabelText("全部账号未加权 Token 使用量汇总值"));
    expect(screen.getByLabelText("全部账号统计摘要")).toContainElement(
      screen.getByLabelText("全部账号未加权 Token 使用量汇总值")
    );
    expect(screen.queryByLabelText("CPA用量明细表格")).not.toBeInTheDocument();
    expect(screen.queryByRole("heading", { name: /Token 使用量|Token 使用趋势/ })).not.toBeInTheDocument();
    expect(screen.getByText("重启 alpha")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "全部用户" }));
    expect(await screen.findByPlaceholderText("搜索用户邮箱")).toHaveFocus();
    expect(await screen.findByTitle("alice@example.com")).toBeVisible();

    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/overview/usage?window=today&user_limit=10&token_mode=unweighted")).toBe(true);
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/overview/catalog")).toBe(true);
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("/release"))).toBe(false);

    expect(await screen.findByRole("img", { name: /全部账号未加权 Token 使用趋势/ })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^加权$/ }));
    const aggregateWeightedSummary = screen.getByLabelText("全部账号加权 Token 使用量汇总值");
    expect(within(aggregateWeightedSummary).getByText("当前值").closest("div")).toHaveTextContent("加权250 Token");
    expect(await screen.findByRole("img", { name: /全部账号加权 Token 使用趋势/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^未加权$/ }));

    await user.click(screen.getByRole("tab", { name: "CPA 账号 Token 统计" }));
    expect(screen.getByRole("tab", { name: "CPA 账号 Token 统计" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel")).toHaveAttribute("aria-labelledby", "overview-token-tab-account");
    expect(await screen.findByRole("img", { name: /CPA 账号未加权 Token 使用趋势：.*alpha/ })).toBeInTheDocument();
    expect(screen.queryByLabelText("CPA 账号统计摘要")).not.toBeInTheDocument();
    const accountTable = screen.getByLabelText("CPA用量明细表格");
    expect(accountTable).toHaveTextContent("alpha");
    expect(accountTable).not.toHaveTextContent("cpa-15");
    Object.defineProperties(accountTable, {
      clientHeight: { configurable: true, value: 300 },
      scrollHeight: { configurable: true, value: 900 },
      scrollTop: { configurable: true, value: 600 }
    });
    fireEvent.scroll(accountTable);
    expect(accountTable).toHaveTextContent("cpa-15");
    expect(within(accountTable).getAllByText("200 Token").length).toBeGreaterThan(0);

    await user.click(screen.getByRole("button", { name: /^加权$/ }));
    expect(await screen.findByRole("img", { name: /CPA 账号加权 Token 使用趋势：.*alpha/ })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /^未加权$/ }));

    await user.click(screen.getByRole("tab", { name: "用户 Token 统计" }));
    expect(screen.getByRole("tab", { name: "用户 Token 统计" })).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("tabpanel")).toHaveAttribute("aria-labelledby", "overview-token-tab-user");
    expect(await screen.findByRole("img", { name: /用户未加权 Token 使用趋势：.*alice@example.com/ })).toBeInTheDocument();

    const userTable = screen.getByLabelText("用户用量明细表格");
    const userRows = within(userTable).getAllByRole("row");
    expect(userRows[1]).toHaveTextContent("bob@example.com");
    expect(userRows[2]).toHaveTextContent("alice@example.com");
    expect(within(userRows[1]).queryByText("未加权")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^加权$/ }));
    expect(screen.getByRole("button", { name: /^加权$/ })).toHaveAttribute("aria-pressed", "true");
    expect(screen.queryByLabelText("用户加权 Token 使用量汇总值")).not.toBeInTheDocument();
    expect(await screen.findByRole("img", { name: /用户加权 Token 使用趋势：.*alice@example.com/ })).toBeInTheDocument();
    const weightedRows = within(userTable).getAllByRole("row");
    expect(weightedRows[1]).toHaveTextContent("alice@example.com");
    expect(within(weightedRows[1]).queryByText("加权")).not.toBeInTheDocument();
    expect(within(weightedRows[1]).queryByText("未加权")).not.toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "6 小时" }));
    expect(await waitForRequest(fetchMock, "/admin/api/overview/usage?window=21600&user_limit=10&token_mode=weighted")).toBe(true);

    await user.click(screen.getByRole("button", { name: "时间选择" }));
    expect(await screen.findByRole("dialog", { name: "时间选择" })).toBeInTheDocument();
    expect(screen.queryByText(/查询包含开始时刻，不包含结束时刻。/)).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /取\s*消/ }));
    expect(screen.getByRole("button", { name: "6 小时" })).toHaveAttribute("aria-pressed", "true");
    expect(usageRequests(fetchMock, "custom")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "时间选择" }));
    await user.click(await screen.findByRole("button", { name: "应用范围" }));
    await waitForUsageWindow(fetchMock, "custom");
    const customRequest = new URL(usageRequests(fetchMock, "custom")[0], "http://preview.test");
    expect(Number(customRequest.searchParams.get("start_at"))).toBeLessThan(Number(customRequest.searchParams.get("end_at")));
    expect(customRequest.searchParams.get("user_limit")).toBe("10");
    expect(screen.getByRole("button", { name: "时间选择" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /范围内总量，当前降序/ }));
    expect(screen.getByRole("button", { name: /范围内总量，当前升序/ })).toBeInTheDocument();

    Object.defineProperties(userTable, {
      clientHeight: { configurable: true, value: 300 },
      scrollHeight: { configurable: true, value: 900 },
      scrollTop: { configurable: true, value: 600 }
    });
    fireEvent.scroll(userTable);
    expect(await waitForUsageLimit(fetchMock, 15)).toBe(true);
    expect(await within(userTable).findByText("user-15@example.com")).toBeInTheDocument();
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

async function waitForUsageLimit(fetchMock: ReturnType<typeof vi.fn>, limit: number) {
  for (let attempt = 0; attempt < 30; attempt += 1) {
    if (fetchMock.mock.calls.some(([path]) => {
      const url = new URL(String(path), "http://preview.test");
      return url.pathname === "/admin/api/overview/usage" && url.searchParams.get("user_limit") === String(limit);
    })) return true;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  return false;
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
