import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RuntimePage } from "./RuntimePage";

const services = {
  services: [
    {
      service: "cliproxy-alpha",
      container_id: "aaaaaaaaaaaa",
      name: "cpa-alpha",
      image: "registry.example.com/cpa:v2",
      state: "running",
      status: "Up 10 minutes",
      health: "healthy"
    },
    {
      service: "edge",
      container_id: "eeeeeeeeeeee",
      name: "cpa-edge",
      image: "registry.example.com/edge:v2",
      state: "running",
      status: "Up 10 minutes",
      health: "healthy"
    }
  ]
};

const completedJob = {
  id: "job-complete",
  name: "健康检查",
  target: "all",
  status: "succeeded",
  created_at: 1_787_500_000,
  started_at: 1_787_500_001,
  finished_at: 1_787_500_002,
  exit_code: 0,
  output: ["health: ok"]
};

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("RuntimePage frozen legacy contract", () => {
  it("renders the legacy bulk actions, six-column service table, diagnostics and job history", async () => {
    const fetchMock = installFetch(({ path }) => {
      if (path === "/admin/api/runtime/services") return jsonResponse(services);
      if (path === "/admin/api/jobs") return jsonResponse({ jobs: [completedJob] });
      if (path === "/admin/api/logs?target=edge") return jsonResponse({ target: "edge", output: "edge log", exit_code: 0, truncated: false });
      throw new Error(`unexpected request: ${path}`);
    });
    const user = userEvent.setup();
    renderRuntimePage();

    expect(await screen.findByRole("heading", { name: "批量操作" })).toBeInTheDocument();
    expect(screen.getAllByRole("columnheader").map((header) => header.textContent)).toEqual([
      "序号", "服务", "容器", "状态", "说明", "操作"
    ]);
    expect(screen.getByRole("button", { name: "启动全部" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "重启全部" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "全部日志" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "停止业务服务" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /健康检查/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /路由验证/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /配置校验/ })).toBeInTheDocument();
    expect(screen.getByText("健康检查")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes("/images/cliproxy"))).toBe(false);

    const edgeRow = (await screen.findByText("cpa-edge")).closest("tr");
    expect(edgeRow).not.toBeNull();
    expect(within(edgeRow!).queryByRole("button", { name: "停止" })).not.toBeInTheDocument();
    expect(within(edgeRow!).queryByRole("button", { name: "重启" })).not.toBeInTheDocument();
    await user.click(within(edgeRow!).getByRole("button", { name: "日志" }));
    expect(await screen.findByText("edge log")).toBeInTheDocument();
    expect(screen.getByText("SERVICE LOGS")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(
      "/admin/api/logs?target=edge",
      expect.objectContaining({ credentials: "same-origin" })
    );
  });

  it.each([
    ["启动全部", "up", "all"],
    ["健康检查", "health", "all"],
    ["路由验证", "verify-routing", "all"],
    ["配置校验", "render", "all"]
  ])("submits %s through the frozen legacy operations path", async (buttonName, action, target) => {
    const fetchMock = installFetch(({ path, init }) => {
      if (path === "/admin/api/runtime/services") return jsonResponse(services);
      if (path === "/admin/api/jobs") return jsonResponse({ jobs: [] });
      if (path === "/admin/api/operations" && init?.method === "POST") {
        return jsonResponse({
          message: "任务已提交",
          reused: false,
          job: { ...completedJob, id: `job-${action}`, name: buttonName, status: "queued", output: [] }
        }, 202);
      }
      if (path === `/admin/api/jobs/job-${action}`) {
        return jsonResponse({ job: { ...completedJob, id: `job-${action}`, name: buttonName } });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    const user = userEvent.setup();
    renderRuntimePage();
    await screen.findByText("cpa-alpha");

    await user.click(screen.getByRole("button", { name: new RegExp(buttonName) }));
    const request = await waitFor(() => {
      const match = fetchMock.mock.calls.find(([path, init]) => String(path) === "/admin/api/operations" && init?.method === "POST");
      expect(match).toBeDefined();
      return match!;
    });
    expect(JSON.parse(String(request[1]?.body))).toEqual({ action, target });
    expect(request[1]?.headers).toEqual(expect.objectContaining({ "X-CSRF-Token": "csrf-test" }));
    expect(await screen.findByText("TASK OUTPUT")).toBeInTheDocument();
  });

  it("matches restart confirmation cancellation and sends one request for a synchronous double click", async () => {
    const fetchMock = installFetch(({ path, init }) => {
      if (path === "/admin/api/runtime/services") return jsonResponse(services);
      if (path === "/admin/api/jobs") return jsonResponse({ jobs: [] });
      if (path === "/admin/api/operations" && init?.method === "POST") {
        return jsonResponse({ message: "任务已提交", reused: false, job: { ...completedJob, id: "job-restart", name: "重启服务", status: "queued", output: [] } }, 202);
      }
      throw new Error(`unexpected request: ${path}`);
    });
    const user = userEvent.setup();
    renderRuntimePage();
    const alphaRow = (await screen.findByText("cpa-alpha")).closest("tr")!;

    await user.click(within(alphaRow).getByRole("button", { name: "重启" }));
    expect(screen.getByRole("dialog")).toHaveTextContent("重启服务？");
    expect(screen.getByRole("dialog")).toHaveTextContent("将重启 alpha。");
    await user.click(screen.getByRole("button", { name: /取\s*消/ }));
    expect(requestsTo(fetchMock, "/admin/api/operations")).toHaveLength(0);

    await user.click(within(alphaRow).getByRole("button", { name: "重启" }));
    await user.dblClick(screen.getByRole("button", { name: "确认重启" }));
    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/operations")).toHaveLength(1));
    expect(JSON.parse(String(requestsTo(fetchMock, "/admin/api/operations")[0][1]?.body))).toEqual({ action: "restart", target: "alpha" });
  });

  it("reads exact stop impact before confirmation and fails closed when impact is unavailable", async () => {
    let impactFails = false;
    const fetchMock = installFetch(({ path, init }) => {
      if (path === "/admin/api/runtime/services") return jsonResponse(services);
      if (path === "/admin/api/jobs") return jsonResponse({ jobs: [] });
      if (path === "/admin/api/operations/impact?action=stop&target=alpha") {
        return impactFails
          ? jsonResponse({ error: { message: "影响查询失败", code: "impact_failed" } }, 503)
          : jsonResponse({ action: "stop", target: "alpha", target_type: "account", routed_users: 17 });
      }
      if (path === "/admin/api/operations" && init?.method === "POST") {
        return jsonResponse({ message: "任务已提交", reused: false, job: { ...completedJob, id: "job-stop", name: "停止服务", status: "queued", output: [] } }, 202);
      }
      throw new Error(`unexpected request: ${path}`);
    });
    const user = userEvent.setup();
    renderRuntimePage();
    const alphaRow = (await screen.findByText("cpa-alpha")).closest("tr")!;

    await user.click(within(alphaRow).getByRole("button", { name: "停止" }));
    expect(await screen.findByText("将停止 alpha，当前有 17 个用户路由到该账号。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认停止" }));
    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/operations")).toHaveLength(1));
    await user.click(screen.getByRole("button", { name: /关\s*闭/ }));

    impactFails = true;
    await user.click(within(alphaRow).getByRole("button", { name: "停止" }));
    expect(await screen.findByText("无法确认停止影响，操作已锁定；请取消后重试。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "确认停止" })).toBeDisabled();
    expect(requestsTo(fetchMock, "/admin/api/operations")).toHaveLength(1);
  });

  it("opens a task in the shared output modal, copies output and cancels through the legacy path", async () => {
    const runningJob = { ...completedJob, id: "job-running", status: "running", finished_at: null, exit_code: null, output: ["line one", "line two"] };
    const clipboard = { writeText: vi.fn(async () => undefined) };
    const fetchMock = installFetch(({ path, init }) => {
      if (path === "/admin/api/runtime/services") return jsonResponse(services);
      if (path === "/admin/api/jobs") return jsonResponse({ jobs: [runningJob] });
      if (path === "/admin/api/jobs/job-running") return jsonResponse({ job: runningJob });
      if (path === "/admin/api/jobs/cancel" && init?.method === "POST") {
        return jsonResponse({ message: "任务取消请求已提交", job: { ...runningJob, status: "cancelling" } });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    const user = userEvent.setup();
    Object.defineProperty(window, "isSecureContext", { value: true, configurable: true });
    Object.defineProperty(navigator, "clipboard", { value: clipboard, configurable: true });
    renderRuntimePage();

    await user.click(await screen.findByRole("button", { name: "运行中" }));
    await screen.findByText("TASK OUTPUT");
    expect(document.querySelector(".oauth-task-output")).toHaveTextContent(/line one\s+line two/);
    expect(screen.getByText("TASK OUTPUT")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "复制完整输出" }));
    expect(clipboard.writeText).toHaveBeenCalledWith("line one\nline two");
    expect(await screen.findByText("已复制到剪贴板")).toBeInTheDocument();

    await user.dblClick(screen.getByRole("button", { name: "取消任务" }));
    await waitFor(() => expect(requestsTo(fetchMock, "/admin/api/jobs/cancel")).toHaveLength(1));
    expect(JSON.parse(String(requestsTo(fetchMock, "/admin/api/jobs/cancel")[0][1]?.body))).toEqual({ id: "job-running" });
  });

  it("keeps the page usable and reports a failed diagnostic without opening fake output", async () => {
    const fetchMock = installFetch(({ path, init }) => {
      if (path === "/admin/api/runtime/services") return jsonResponse(services);
      if (path === "/admin/api/jobs") return jsonResponse({ jobs: [] });
      if (path === "/admin/api/operations" && init?.method === "POST") {
        return jsonResponse({ error: { message: "诊断任务未提交", code: "runtime_unavailable" } }, 503);
      }
      throw new Error(`unexpected request: ${path}`);
    });
    const user = userEvent.setup();
    renderRuntimePage();
    await screen.findByText("cpa-alpha");

    await user.click(screen.getByRole("button", { name: /健康检查/ }));
    expect(await screen.findByText("诊断任务未提交")).toBeInTheDocument();
    expect(screen.queryByText("TASK OUTPUT")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /健康检查/ })).toBeEnabled();
    expect(requestsTo(fetchMock, "/admin/api/operations")).toHaveLength(1);
  });
});

function renderRuntimePage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } }
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <RuntimePage csrfToken="csrf-test" />
    </QueryClientProvider>
  );
}

type RequestContext = { path: string; init?: RequestInit };

function installFetch(resolver: (request: RequestContext) => Response | Promise<Response>) {
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => resolver({ path: String(input), init }));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

function requestsTo(fetchMock: ReturnType<typeof installFetch>, path: string) {
  return fetchMock.mock.calls.filter(([input]) => String(input) === path);
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}
