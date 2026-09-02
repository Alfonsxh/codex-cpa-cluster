import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { TeamsPage } from "./TeamsPage";

const teams = [
  { id: "platform", name: "平台研发", description: "核心平台与基础设施", tag_style: "indigo", user_count: 2, created_at: 10, updated_at: 100 },
  { id: "data", name: "数据智能", description: "数据产品与分析", tag_style: "cyan", user_count: 1, created_at: 20, updated_at: 200 },
  { id: "empty", name: "空团队", description: "", tag_style: "rose", user_count: 0, created_at: 30, updated_at: 300 }
];

const users = [
  user("lin.chen@example.com", "platform", "平台研发", 9_650_000),
  user("kai.wang@example.com", "data", "数据智能", 4_920_000),
  user("yan.liu@example.com", null, "", 0)
];

afterEach(() => vi.unstubAllGlobals());

describe("TeamsPage frozen legacy contract", () => {
  it("does not start all-history usage until the team catalog has completed", async () => {
    let releaseCatalog = () => {};
    const catalogGate = new Promise<void>((resolve) => { releaseCatalog = resolve; });
    let usageStarted = false;
    const fetchMock = vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      if (url.endsWith("/admin/api/teams")) {
        await catalogGate;
        return json({ teams });
      }
      if (url.includes("/admin/api/teams/usage?window=all")) {
        usageStarted = true;
        return json({
          generated_at: 1000,
          window: "all",
          window_start_at: null,
          window_end_at: 1000,
          window_seconds: null,
          teams: []
        });
      }
      return json({ error: "not found", code: "not_found" }, 404);
    });
    vi.stubGlobal("fetch", fetchMock);

    renderTeams();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(String(fetchMock.mock.calls[0][0])).toMatch(/\/admin\/api\/teams$/);
    expect(usageStarted).toBe(false);

    releaseCatalog();
    expect(await screen.findByText("平台研发")).toBeInTheDocument();
    await waitFor(() => expect(usageStarted).toBe(true));
  });

  it("loads the catalog and all-history usage, then renders the seven-column directory and local filters", async () => {
    const fetchMock = installFetch();
    const userDriver = userEvent.setup();
    renderTeams();

    expect(await screen.findByText("平台研发")).toBeInTheDocument();
    expect(screen.getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "序号", "团队", "当前成员", "活跃成员", "全部历史 Token", "更新时间", "操作"
    ]);
    expect(await screen.findByText("12.4")).toBeInTheDocument();
    expect(screen.getByText("平台研发")).toHaveClass("team-tag-style-indigo");
    expect(screen.getByText("数据智能")).toHaveClass("team-tag-style-cyan");
    expect(screen.getAllByText("12,400,000 Token")).toHaveLength(2);
    expect(screen.getAllByRole("button", { name: "删除" })[0]).toBeDisabled();
    expect(screen.getAllByRole("button", { name: "删除" })[2]).toBeEnabled();

    await userDriver.type(screen.getByRole("searchbox", { name: "搜索团队名称或说明" }), "数据");
    expect(screen.getByText("1 个团队")).toBeInTheDocument();
    expect(screen.queryByText("平台研发")).not.toBeInTheDocument();
    await userDriver.clear(screen.getByRole("searchbox", { name: "搜索团队名称或说明" }));
    await userDriver.click(screen.getByRole("button", { name: "团队状态：全部团队" }));
    await userDriver.click(screen.getByRole("option", { name: "空团队" }));
    expect(screen.getByText("1 个团队")).toBeInTheDocument();
    expect(screen.getByText("无说明")).toBeInTheDocument();

    const requested = fetchMock.mock.calls.map(([input]) => String(input));
    expect(requested.some((url) => url.endsWith("/admin/api/teams"))).toBe(true);
    expect(requested.some((url) => url.includes("/admin/api/teams/usage?window=all"))).toBe(true);
  });

  it("uses the frozen create/edit form lifecycle and keeps failed input in the open modal", async () => {
    let createAttempts = 0;
    const fetchMock = installFetch(({ url, method }) => {
      if (url.endsWith("/admin/api/teams") && method === "POST") {
        createAttempts += 1;
        return createAttempts === 1 ? json({ error: { message: "团队名称已存在", code: "conflict" } }, 409) : json({ message: "团队已创建", team: teams[2] });
      }
      return null;
    });
    const userDriver = userEvent.setup();
    renderTeams();
    await screen.findByText("平台研发");

    await userDriver.click(screen.getByRole("button", { name: "创建团队" }));
    const dialog = screen.getByRole("dialog", { name: /创建团队/ });
    expect(within(dialog).getByLabelText("团队名称")).toHaveFocus();
    await userDriver.type(within(dialog).getByLabelText("团队名称"), "新团队");
    await userDriver.type(within(dialog).getByLabelText("团队说明"), "新团队说明");
    await userDriver.click(within(dialog).getByRole("button", { name: "创建团队" }));
    expect(await within(dialog).findByText("团队名称已存在")).toBeInTheDocument();
    expect(within(dialog).getByLabelText("团队名称")).toHaveValue("新团队");
    expect(within(dialog).getByRole("button", { name: "创建团队" })).toBeEnabled();

    await userDriver.click(within(dialog).getByRole("button", { name: "创建团队" }));
    expect(await screen.findByText("团队已创建")).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: /创建团队/ })).not.toBeInTheDocument();
    const createRequest = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith("/admin/api/teams") && init?.method === "POST");
    expect(JSON.parse(String(createRequest?.[1]?.body))).toEqual({ name: "新团队", description: "新团队说明" });
  });

  it("opens the member workspace on demand, debounces filters and preserves the legacy selection states", async () => {
    const fetchMock = installFetch();
    const userDriver = userEvent.setup();
    renderTeams();
    await screen.findByText("平台研发");
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/admin/api/users?"))).toBe(false);

    await userDriver.click(screen.getAllByRole("button", { name: "成员" })[0]);
    const dialog = await screen.findByRole("dialog", { name: /平台研发 · 成员管理/ });
    expect(await within(dialog).findByText("lin.chen@example.com")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "成员范围：当前团队成员" })).toBeInTheDocument();
    expect(within(dialog).queryByText("当前管理团队")).not.toBeInTheDocument();
    expect(within(dialog).getByText("共 3 位匹配用户；批量操作仅作用于已勾选用户")).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: /选择全部匹配/ })).not.toBeInTheDocument();
    expect(within(dialog).getByText("本团队成员")).toBeInTheDocument();
    expect(within(dialog).getByText("属于其他团队")).toBeInTheDocument();
    expect(within(dialog).getByText("尚未加入")).toBeInTheDocument();

    const memberRequest = fetchMock.mock.calls.map(([input]) => String(input)).find((url) => url.includes("/admin/api/users?"));
    expect(memberRequest).toContain("view=members");
    expect(memberRequest).toContain("window=all");
    expect(memberRequest).toContain("page_size=50");
    expect(memberRequest).toContain("sort=tokens");
    expect(memberRequest).toContain("direction=desc");
    expect(memberRequest).toContain("usage_state=all");
    expect(memberRequest).toContain("team_id=platform");

    await userDriver.click(within(dialog).getByRole("checkbox", { name: "选择 lin.chen@example.com" }));
    expect(within(dialog).getByText("已选择 1 位用户")).toBeInTheDocument();
    await userDriver.click(within(dialog).getByRole("button", { name: "取消选择" }));
    expect(within(dialog).queryByText("已选择 1 位用户")).not.toBeInTheDocument();

    await userDriver.click(within(dialog).getByRole("button", { name: "成员范围：当前团队成员" }));
    await userDriver.click(screen.getByRole("option", { name: "未分组用户" }));
    await waitFor(() => {
      const urls = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urls.some((url) => url.includes("team_id=unassigned"))).toBe(true);
    });
  });

  it("打开空团队时默认展示可直接加入的未分组用户", async () => {
    const fetchMock = installFetch(({ url, method }) => {
      if (!url.includes("/admin/api/users?") || method !== "GET") return null;
      const params = new URL(url, "http://localhost").searchParams;
      const matchingUsers = params.get("team_id") === "unassigned" ? users.filter((item) => item.team_id === null) : users;
      return json(memberCatalog(matchingUsers));
    });
    const userDriver = userEvent.setup();
    renderTeams();
    await screen.findByText("平台研发");

    await userDriver.click(screen.getAllByRole("button", { name: "成员" })[2]);
    const dialog = await screen.findByRole("dialog", { name: /空团队 · 成员管理/ });
    expect(await within(dialog).findByText("yan.liu@example.com")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "成员范围：未分组用户" })).toBeInTheDocument();
    expect(within(dialog).queryByText("lin.chen@example.com")).not.toBeInTheDocument();

    const memberRequests = fetchMock.mock.calls
      .map(([input]) => String(input))
      .filter((url) => url.includes("/admin/api/users?"));
    expect(memberRequests).toHaveLength(1);
    expect(memberRequests[0]).toContain("team_id=unassigned");
    expect(memberRequests[0]).not.toContain("team_id=empty");
  });

  it("从未分组列表加入空团队后切换到当前成员并展示新状态", async () => {
    let yanTeamID: string | null = null;
    const fetchMock = installFetch(({ url, method, body }) => {
      if (url.includes("/admin/api/users?") && method === "GET") {
        const params = new URL(url, "http://localhost").searchParams;
        const currentUsers = [
          users[0],
          users[1],
          user("yan.liu@example.com", yanTeamID, yanTeamID === "empty" ? "空团队" : "", 0)
        ];
        const teamID = params.get("team_id");
        const matchingUsers = teamID === "unassigned"
          ? currentUsers.filter((item) => item.team_id === null)
          : teamID
            ? currentUsers.filter((item) => item.team_id === teamID)
            : currentUsers;
        return json(memberCatalog(matchingUsers));
      }
      if (url.endsWith("/admin/api/users/team/batch") && method === "POST") {
        const payload = JSON.parse(String(body));
        yanTeamID = payload.team_id;
        return json({ message: "团队归属已更新" });
      }
      return null;
    });
    const userDriver = userEvent.setup();
    renderTeams();
    await screen.findByText("平台研发");

    await userDriver.click(screen.getAllByRole("button", { name: "成员" })[2]);
    const dialog = await screen.findByRole("dialog", { name: /空团队 · 成员管理/ });
    await within(dialog).findByText("yan.liu@example.com");
    await userDriver.click(within(dialog).getByRole("checkbox", { name: "选择 yan.liu@example.com" }));
    await userDriver.click(within(dialog).getByRole("button", { name: "加入当前团队" }));
    const confirm = screen.getAllByRole("dialog").find((item) => within(item).queryByText("加入“空团队”"));
    expect(confirm).toBeDefined();
    await userDriver.click(within(confirm!).getByRole("button", { name: "确认加入" }));

    expect(await screen.findByText("已更新 1 位用户的团队归属")).toBeInTheDocument();
    expect(await within(dialog).findByText("本团队成员")).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: "成员范围：当前团队成员" })).toBeInTheDocument();
    const memberRequests = fetchMock.mock.calls
      .map(([input]) => String(input))
      .filter((url) => url.includes("/admin/api/users?"));
    expect(memberRequests.some((url) => url.includes("team_id=empty"))).toBe(true);
  });

  it("让成员空态占据剩余列表区域并使用独立表格单元格样式", async () => {
    installFetch(({ url, method }) => url.includes("/admin/api/users?") && method === "GET" ? json(memberCatalog([])) : null);
    const userDriver = userEvent.setup();
    renderTeams();
    await screen.findByText("平台研发");

    await userDriver.click(screen.getAllByRole("button", { name: "成员" })[0]);
    const dialog = await screen.findByRole("dialog", { name: /平台研发 · 成员管理/ });
    const emptyState = await within(dialog).findByRole("status", { name: "" });
    expect(emptyState).toHaveTextContent("当前条件没有匹配用户");
    expect(emptyState.closest("td")).toHaveClass("organization-member-state");
    expect(emptyState.closest("table")).toHaveClass("organization-member-table", "is-state");
    expect(emptyState.closest("td")).not.toHaveClass("team-usage-state");
  });

  it("更新团队归属后精确刷新当前工作区、目录和选择状态", async () => {
    let yanTeamID: string | null = null;
    let catalogTeams = teams;
    const fetchMock = installFetch(({ url, method, body }) => {
      if (url.includes("/admin/api/users?") && method === "GET") {
        const params = new URL(url, "http://localhost").searchParams;
        const currentUsers = [
          users[0],
          users[1],
          user("yan.liu@example.com", yanTeamID, yanTeamID === "empty" ? "空团队" : "", 0)
        ];
        const teamID = params.get("team_id");
        const matchingUsers = teamID === "unassigned"
          ? currentUsers.filter((item) => item.team_id === null)
          : teamID
            ? currentUsers.filter((item) => item.team_id === teamID)
            : currentUsers;
        return json(memberCatalog(matchingUsers, catalogTeams));
      }
      if (url.endsWith("/admin/api/users/team/batch") && method === "POST") {
        const payload = JSON.parse(String(body));
        yanTeamID = payload.team_id;
        catalogTeams = teams.map((item) => item.id === "empty" ? { ...item, user_count: 1, updated_at: 400 } : item);
        return json({ message: "团队归属已更新" });
      }
      if (url.endsWith("/admin/api/teams") && method === "GET") return json({ teams: catalogTeams });
      return null;
    });
    const userDriver = userEvent.setup();
    renderTeams();
    await screen.findByText("平台研发");

    await userDriver.click(screen.getAllByRole("button", { name: "成员" })[2]);
    const dialog = await screen.findByRole("dialog", { name: /空团队 · 成员管理/ });
    await within(dialog).findByText("yan.liu@example.com");
    fireEvent.change(dialog.querySelector("#organization-user-scope-react")!, { target: { value: "all" } });
    await within(dialog).findByText("lin.chen@example.com");

    await userDriver.click(within(dialog).getByRole("checkbox", { name: "选择 yan.liu@example.com" }));
    await userDriver.click(within(dialog).getByRole("button", { name: "加入当前团队" }));
    const confirm = screen.getAllByRole("dialog").find((item) => within(item).queryByText("加入“空团队”"));
    expect(confirm).toBeDefined();
    await userDriver.click(within(confirm!).getByRole("button", { name: "确认加入" }));

    expect(await screen.findByText("已更新 1 位用户的团队归属")).toBeInTheDocument();
    await waitFor(() => expect(within(dialog).getByText("本团队成员")).toBeInTheDocument());
    expect(within(dialog).queryByText("已选择 1 位用户")).not.toBeInTheDocument();
    expect(within(dialog).getByRole("checkbox", { name: "选择 yan.liu@example.com" })).not.toBeChecked();

    await userDriver.click(within(dialog).getByRole("checkbox", { name: "选择 lin.chen@example.com" }));
    expect(within(dialog).getByText("已选择 1 位用户")).toBeInTheDocument();
    const directory = screen.getByLabelText("团队目录表格");
    const emptyTeamRow = within(directory).getByText("空团队").closest("tr");
    expect(emptyTeamRow?.children[2]).toHaveTextContent("1");

    const allScopeRequests = fetchMock.mock.calls
      .map(([input]) => String(input))
      .filter((url) => url.includes("/admin/api/users?") && !new URL(url, "http://localhost").searchParams.has("team_id"));
    expect(allScopeRequests.length).toBeGreaterThanOrEqual(2);
  });

  it("blocks silent joins and sends move batches with the original team expectation", async () => {
    const fetchMock = installFetch();
    const userDriver = userEvent.setup();
    renderTeams();
    await screen.findByText("平台研发");
    await userDriver.click(screen.getAllByRole("button", { name: "成员" })[0]);
    const dialog = await screen.findByRole("dialog", { name: /平台研发 · 成员管理/ });
    await within(dialog).findByText("kai.wang@example.com");

    await userDriver.click(within(dialog).getByRole("checkbox", { name: "选择 kai.wang@example.com" }));
    await userDriver.click(within(dialog).getByRole("button", { name: "加入当前团队" }));
    expect(within(dialog).getByText("有 1 位用户已在其他团队；请将用户范围切换为“仅未分组”，或先移出原团队。")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input, init]) => String(input).endsWith("/admin/api/users/team/batch") && init?.method === "POST")).toBe(false);

    await userDriver.click(within(dialog).getByRole("button", { name: "从其他团队移动" }));
    const confirm = screen.getAllByRole("dialog").find((item) => within(item).queryByText("移动到“平台研发”"));
    expect(confirm).toBeDefined();
    await userDriver.click(within(confirm!).getByRole("button", { name: "确认移动到" }));
    await screen.findByText("已更新 1 位用户的团队归属");
    const batch = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith("/admin/api/users/team/batch") && init?.method === "POST");
    expect(JSON.parse(String(batch?.[1]?.body))).toEqual({ users: ["kai.wang@example.com"], team_id: "platform", expected_team_id: "data" });
  });

  it("never offers deletion for a non-empty team and deletes an empty team only after confirmation", async () => {
    const fetchMock = installFetch();
    const userDriver = userEvent.setup();
    renderTeams();
    await screen.findByText("平台研发");
    const deleteButtons = screen.getAllByRole("button", { name: "删除" });
    expect(deleteButtons[0]).toBeDisabled();
    await userDriver.click(deleteButtons[2]);
    const confirm = screen.getByRole("dialog");
    expect(within(confirm).getByText("删除“空团队”")).toBeInTheDocument();
    expect(within(confirm).getByText("空团队删除后无法恢复。")).toBeInTheDocument();
    await userDriver.click(within(confirm).getByRole("button", { name: "确认删除" }));
    expect(await screen.findByText("团队已删除")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input, init]) => String(input).endsWith("/admin/api/teams?id=empty") && init?.method === "DELETE")).toBe(true);
  });
});

function renderTeams() {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={queryClient}><TeamsPage csrfToken="csrf-test" /></QueryClientProvider>);
}

function installFetch(override?: (request: { url: string; method: string; body: unknown }) => Response | null) {
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = init?.method ?? "GET";
    const custom = override?.({ url, method, body: init?.body });
    if (custom) return custom;
    if (url.includes("/admin/api/teams/usage")) return json({ generated_at: 1000, window: "all", window_start_at: null, window_end_at: 1000, window_seconds: null, teams: [
      { ...teams[0], current_user_count: 2, usage: usage(12_400_000, 2) },
      { ...teams[1], current_user_count: 1, usage: usage(9_300_000, 1) },
      { ...teams[2], current_user_count: 0, usage: usage(0, 0) }
    ] });
    if (url.includes("/admin/api/users?")) return json({ generated_at: 1000, window: "all", window_start_at: null, window_end_at: 1000, window_seconds: null, summary_generated_at: 1000, summary_cached: true, users, accounts: {}, teams, tags: [], collector: {}, pagination: { page: 1, page_size: url.includes("page_size=100") ? 100 : 50, total: users.length, total_pages: 1 } });
    if (url.endsWith("/admin/api/teams") && method === "GET") return json({ teams });
    if (url.endsWith("/admin/api/users/team/batch") && method === "POST") return json({ message: "团队归属已更新" });
    if (url.endsWith("/admin/api/teams?id=empty") && method === "DELETE") return json({ message: "团队已删除", team: teams[2] });
    if (url.endsWith("/admin/api/teams") && method === "POST") return json({ message: "团队已创建", team: teams[2] });
    if (url.endsWith("/admin/api/teams") && method === "PUT") return json({ message: "团队已更新", team: teams[0] });
    return json({ error: "not found", code: "not_found" }, 404);
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function memberCatalog(memberUsers: ReturnType<typeof user>[], catalogTeams = teams) {
  return {
    generated_at: 1000,
    window: "all",
    window_start_at: null,
    window_end_at: 1000,
    window_seconds: null,
    summary_generated_at: 1000,
    summary_cached: true,
    users: memberUsers,
    accounts: {},
    teams: catalogTeams,
    tags: [],
    collector: {},
    pagination: { page: 1, page_size: 50, total: memberUsers.length, total_pages: 1 }
  };
}

function user(email: string, teamID: string | null, teamName: string, tokens: number) {
  const tagStyle = teams.find((team) => team.id === teamID)?.tag_style;
  return { email, status: "active", active_keys: 1, active_accounts: 3, total_records: 3, created_at: 1, updated_at: 2, route_account_id: "cpa-main", team_id: teamID, team: teamID && tagStyle ? { id: teamID, name: teamName, description: "", tag_style: tagStyle } : null, team_membership_version: 1, account_count: 3, usage: usage(tokens, tokens ? 1 : 0), weekly_quota: {} };
}

function usage(tokens: number, activeUsers: number) {
  return { request_count: activeUsers, failed_count: 0, input_tokens: Math.round(tokens * .2), cached_tokens: 0, output_tokens: Math.round(tokens * .8), reasoning_tokens: 0, total_tokens: tokens, weighted_tokens: tokens, last_used_at: 100, active_users: activeUsers };
}

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
}
