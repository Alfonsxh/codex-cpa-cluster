import { App as AntApp, ConfigProvider } from "antd";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { PortalApp, safeManagementURL } from "./PortalApp";

const branding = {
  version: 1,
  product_name: "Test CPA",
  short_name: "T-CPA",
  environment_label: "Isolated Test",
  public_base_url: "https://cpa.example.com",
  provider_name: "Test Provider",
  api_key_env: "TEST_API_KEY",
  default_model: "gpt-test",
  logo: {
    custom: false,
    url: "/portal/assets/codex-cpa-cluster-logo.svg",
    content_type: "image/svg+xml",
    sha256: "",
    updated_at: null
  }
};

beforeEach(() => vi.stubEnv("DEV", false));
afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

describe("PortalApp", () => {
  it("links development entries to their isolated app origins", async () => {
    vi.stubEnv("DEV", true);
    vi.stubEnv("VITE_DEV_ADMIN_ORIGIN", "http://127.0.0.1:5193");
    vi.stubEnv("VITE_DEV_USAGE_ORIGIN", "http://127.0.0.1:5194");
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(branding)));
    renderPortal("/");

    expect(await screen.findByRole("link", { name: /综合管理平台/ }))
      .toHaveAttribute("href", "http://127.0.0.1:5193/admin/");
    expect(screen.getByRole("link", { name: /使用中心/ }))
      .toHaveAttribute("href", "http://127.0.0.1:5194/usage/");
  });

  it("renders the framework-backed landing entries from public branding", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(jsonResponse(branding)));
    renderPortal("/");

    expect(await screen.findByRole("heading", { name: "选择要进入的界面" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: /综合管理平台/ })).toHaveAttribute("href", "/admin/");
    expect(screen.getByRole("link", { name: /使用中心/ })).toHaveAttribute("href", "/usage/");
    expect(screen.getByRole("button", { name: "切换为深色主题" })).toBeInTheDocument();
    expect(await screen.findByText("Isolated Test")).toBeInTheDocument();
  });

  it("shows a recoverable login boundary for the native catalog", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
      if (String(input) === "/site-config.json") return jsonResponse(branding);
      return jsonResponse({ error: { code: "unauthorized", message: "需要管理员身份" } }, 401);
    }));
    const view = renderPortal("/native/");

    expect(await screen.findByText("请先登录管理中心")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "前往管理中心登录" })).not.toBeInTheDocument();
    expect(screen.getByRole("link", { name: "添加业务 CPA" })).toHaveAttribute("href", "/admin/?action=add-account");
    expect(screen.queryByRole("button", { name: "切换为深色主题" })).not.toBeInTheDocument();
    expect(screen.queryByRole("img", { name: "Test CPA" })).not.toBeInTheDocument();
    expect(screen.getByText("ACCESS CONTROL")).toBeInTheDocument();
    expect(document.documentElement).toHaveClass("native-page-active");
    expect(document.documentElement.style.colorScheme).toBe("light");
    view.unmount();
    expect(document.documentElement).not.toHaveClass("native-page-active");
  });

  it("links only server URLs that also pass the browser loopback allowlist", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
      if (String(input) === "/site-config.json") return jsonResponse(branding);
      return jsonResponse({
        accounts: [
          { id: "alpha", group_enabled: true, management_url: "http://127.0.0.1:18318/management.html" },
          { id: "beta", group_enabled: false, management_url: "https://evil.example.com/management.html" }
        ]
      });
    }));
    renderPortal("/native/");

    const alpha = await screen.findByRole("link", { name: /alpha/ });
    expect(alpha).toHaveAttribute("href", "http://127.0.0.1:18318/management.html");
    expect(screen.queryByRole("link", { name: /beta/ })).not.toBeInTheDocument();
    expect(screen.getByText("仅本机可访问")).toBeInTheDocument();
    expect(screen.getAllByText("业务 CPA").length).toBeGreaterThan(1);
    expect(screen.getByText("仅允许从部署主机访问")).toBeInTheDocument();
    expect(screen.getByText("公网入口不开放原生管理端口")).toBeInTheDocument();
    expect(screen.getByText("账号已启用")).toBeInTheDocument();
    expect(screen.getByText("账号已停用")).toBeInTheDocument();
    expect(screen.getByText("自动持久化")).toBeInTheDocument();
  });

  it("retries a failed native catalog without reloading unrelated data", async () => {
    let nativeCalls = 0;
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
      if (String(input) === "/site-config.json") return jsonResponse(branding);
      nativeCalls += 1;
      if (nativeCalls === 1) return jsonResponse({ error: { code: "unavailable", message: "暂时不可用" } }, 503);
      return jsonResponse({ accounts: [] });
    }));
    const user = userEvent.setup();
    renderPortal("/native/");

    await user.click(await screen.findByRole("button", { name: "重新读取" }));
    expect(await screen.findByText("0 个业务 CPA")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "添加业务 CPA" })).toBeInTheDocument();
    expect(nativeCalls).toBe(2);
  });
});

describe("safeManagementURL", () => {
  it("accepts only exact loopback HTTP management URLs", () => {
    expect(safeManagementURL("http://[::1]:18318/management.html")).toBe("http://[::1]:18318/management.html");
    for (const value of [
      "https://127.0.0.1:18318/management.html",
      "http://example.com:18318/management.html",
      "http://127.0.0.1:18318/other",
      "http://user:secret@127.0.0.1:18318/management.html"
    ]) {
      expect(safeManagementURL(value)).toBeUndefined();
    }
  });
});

function renderPortal(entry: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <ConfigProvider>
        <AntApp>
          <MemoryRouter initialEntries={[entry]}>
            <PortalApp />
          </MemoryRouter>
        </AntApp>
      </ConfigProvider>
    </QueryClientProvider>
  );
}

function jsonResponse(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
}
