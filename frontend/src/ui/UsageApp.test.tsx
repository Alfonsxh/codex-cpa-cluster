import { App as AntApp, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { UsageApp } from "./UsageApp";
import { UsageDashboard } from "./UsageDashboard";

const profile = {
  user: "alice@example.com",
  api_key: "old-secret-api-key-1234",
  current_group: "alpha",
  generated_at: 10_000
};

const accounts = {
  generated_at: 10_000,
  window: {
    window: "today",
    window_seconds: null,
    window_start_at: 9_000,
    window_end_at: 10_000,
    window_timezone: "Asia/Shanghai"
  },
  current_group: "alpha",
  accounts: [
    {
      id: "alpha",
      display_name: "CPA 1",
      current: true,
      enabled: true,
      selectable: true,
      status: {
        code: "available",
        label: "可用",
        tone: "success",
        reason: "账号当前可用",
        selectable: true,
        remaining_percent: 80
      },
      active_users_1h: 2,
      usage: metrics(120, 180)
    },
    {
      id: "beta",
      display_name: "CPA 2",
      current: false,
      enabled: true,
      selectable: true,
      status: {
        code: "available",
        label: "可用",
        tone: "success",
        reason: "账号当前可用",
        selectable: true,
        remaining_percent: 65
      },
      active_users_1h: 1,
      usage: metrics(80, 110)
    }
  ],
  totals: metrics(200, 290),
  warnings: []
};

const breakdown = {
  generated_at: 10_000,
  window: 86400,
  window_seconds: 86400,
  window_start_at: 0,
  window_end_at: 10_000,
  collection_started_at: 1,
  effective_start_at: 1,
  definition: "test",
  account: "alpha",
  user: "alice@example.com",
  totals: metrics(120, 180),
  models: [{ model: "gpt-5.6", ...metrics(120, 180) }],
  reasoning_efforts: [{ reasoning_effort: "high", ...metrics(120, 180) }],
  combinations: [{ account: "alpha", model: "gpt-5.6", reasoning_effort: "high", ...metrics(120, 180) }]
};

const resizeObserverStub = globalThis.ResizeObserver;

afterEach(() => {
  vi.unstubAllGlobals();
  vi.stubGlobal("ResizeObserver", resizeObserverStub);
});

describe("UsageApp", () => {
  it("logs in through the narrow session endpoint before loading user data", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/usage/session" && (!init?.method || init.method === "GET")) {
        return jsonResponse({ error: { code: "session_required", message: "用户会话已失效" } }, 401);
      }
      if (path === "/usage/session" && init?.method === "POST") {
        return jsonResponse({
          authenticated: true,
          user: "alice@example.com",
          expires_at: 20_000,
          password_change_required: false
        }, 201);
      }
      return portalReadResponse(path);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPortal(<UsageApp />);

    expect(await screen.findByRole("heading", { name: "登录使用中心" })).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "登录使用中心" })).toBeInTheDocument();
    expect(screen.getByText("账号明细")).toBeInTheDocument();
    expect(screen.getByText("我的 API Key")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "切换为深色主题" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path).startsWith("/usage/me/"))).toBe(false);
    await user.type(screen.getByLabelText("用户邮箱"), "alice@example.com");
    await user.type(screen.getByLabelText("密码"), "initial-password");
    await user.click(screen.getByRole("button", { name: "登录" }));

    expect((await screen.findAllByText("CPA 1")).length).toBeGreaterThan(0);
    expect(fetchMock).toHaveBeenCalledWith("/usage/session", expect.objectContaining({
      method: "POST",
      credentials: "same-origin"
    }));
  });

  it("blocks all profile and usage reads until the required password change succeeds", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/usage/session") {
        return jsonResponse({
          authenticated: true,
          user: "alice@example.com",
          expires_at: 20_000,
          password_change_required: true
        });
      }
      if (path === "/usage/me/password" && init?.method === "PUT") {
        return jsonResponse({ message: "密码已修改", password_change_required: false });
      }
      return portalReadResponse(path);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPortal(<UsageApp />);

    expect(await screen.findByRole("dialog", { name: "首次登录必须修改密码" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path).startsWith("/usage/me/") && String(path) !== "/usage/me/password")).toBe(false);
    await user.type(screen.getByLabelText("当前密码"), "initial-password");
    await user.type(screen.getByLabelText("新密码"), "replacement-password");
    await user.type(screen.getByLabelText("确认新密码"), "replacement-password");
    await user.click(screen.getByRole("button", { name: "保存新密码" }));

    expect((await screen.findAllByText("CPA 1")).length).toBeGreaterThan(0);
    expect(fetchMock).toHaveBeenCalledWith("/usage/me/password", expect.objectContaining({
      method: "PUT",
      body: JSON.stringify({ current_password: "initial-password", new_password: "replacement-password" })
    }));
  });
});

describe("UsageDashboard", () => {
  it("queries detail only while opened and replaces the in-memory key after confirmed rotation", async () => {
    const storageSpy = vi.spyOn(Storage.prototype, "setItem");
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/usage/me/key/rotate" && init?.method === "POST") {
        return jsonResponse({
          message: "API Key 已刷新",
          api_key: "new-secret-api-key-9876",
          snapshot_generation: "generation-2"
        });
      }
      if (path.startsWith("/usage/me/usage-breakdown?")) return jsonResponse(breakdown);
      return portalReadResponse(path);
    });
    vi.stubGlobal("fetch", fetchMock);
    vi.stubGlobal("navigator", {
      ...navigator,
      clipboard: { writeText: vi.fn(async () => undefined) }
    });
    const user = userEvent.setup();
    renderPortal(<UsageDashboard onSessionExpired={() => undefined} />);

    expect((await screen.findAllByText("CPA 1")).length).toBeGreaterThan(0);
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("usage-breakdown"))).toBe(false);
    await user.click(screen.getAllByRole("button", { name: "用量明细" })[0]);
    expect(await screen.findByText("gpt-5.6")).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/usage/me/usage-breakdown?window=86400&account=alpha")).toBe(true);

    await user.click(screen.getByRole("button", { name: "显示" }));
    expect(screen.getByText("old-secret-api-key-1234")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "刷新 Key" }));
    await user.click(screen.getByRole("button", { name: "确认刷新并使旧 Key 失效" }));
    expect(await screen.findByText("new-secret-api-key-9876")).toBeInTheDocument();
    expect(screen.queryByText("old-secret-api-key-1234")).not.toBeInTheDocument();
    expect(storageSpy).not.toHaveBeenCalled();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith("/usage/me/key/rotate", expect.objectContaining({
      method: "POST",
      body: JSON.stringify({ confirm: true })
    })));
  });
});

function renderPortal(element: React.ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } }
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <ConfigProvider>
        <AntApp>{element}</AntApp>
      </ConfigProvider>
    </QueryClientProvider>
  );
}

function portalReadResponse(path: string) {
  if (path === "/usage/me/profile") return jsonResponse(profile);
  if (path === "/usage/me/route") return jsonResponse({ current_group: "alpha", generated_at: 10_000 });
  if (path === "/usage/me/accounts?window=today") return jsonResponse(accounts);
  throw new Error(`unexpected request: ${path}`);
}

function metrics(totalTokens: number, weightedTokens: number) {
  return {
    request_count: 2,
    success_count: 2,
    failed_count: 0,
    input_tokens: totalTokens - 20,
    output_tokens: 20,
    reasoning_tokens: 10,
    cached_tokens: 0,
    total_tokens: totalTokens,
    weighted_tokens: weightedTokens,
    last_used_at: 9_999
  };
}

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}
