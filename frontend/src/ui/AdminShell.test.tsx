import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useEffect } from "react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { defaultPublicSiteConfiguration } from "../api/public-site";
import { AdminShell } from "./App";
import { useAdminToolbar } from "./AdminToolbarContext";
import { ConfigurationSectionNav } from "./ConfigurationSectionNav";
import { ThemeProvider } from "./ThemeProvider";

beforeEach(() => {
  vi.stubEnv("DEV", false);
  vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
    const payload = String(input) === "/site-config.json" ? defaultPublicSiteConfiguration
      : { configured: false, current_version: "v2.0.0", status: "disabled", available: false };
    return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
  }));
});
afterEach(() => vi.unstubAllEnvs());

function renderShell(pathname: string, children: React.ReactNode = <div>页面内容</div>) {
  window.history.pushState({}, "", `/admin${pathname}`);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <ThemeProvider>
        <MemoryRouter initialEntries={[pathname]}>
          <AdminShell loggingOut={false} onLogout={vi.fn()}>{children}</AdminShell>
        </MemoryRouter>
      </ThemeProvider>
    </QueryClientProvider>
  );
}

function ConfigurationDetailFixture() {
  const { setPageDetail } = useAdminToolbar();
  useEffect(() => {
    const timer = window.setTimeout(() => setPageDetail({ title: "系统设置", eyebrow: "SYSTEM SETTINGS" }), 0);
    return () => {
      window.clearTimeout(timer);
      setPageDetail(null);
    };
  }, [setPageDetail]);
  return <div>系统设置内容</div>;
}

describe("AdminShell legacy visual contract", () => {
  it("switches development apps across ports without changing internal navigation", () => {
    vi.stubEnv("DEV", true);
    vi.stubEnv("VITE_DEV_USAGE_ORIGIN", "http://127.0.0.1:5194");
    vi.stubEnv("VITE_DEV_PORTAL_ORIGIN", "http://127.0.0.1:5192");
    renderShell("/settings", <ConfigurationSectionNav />);

    const switcher = screen.getByRole("region", { name: "界面切换" });
    expect(within(switcher).getByRole("link", { name: /服务入口/ }))
      .toHaveAttribute("href", "http://127.0.0.1:5192/");
    expect(within(switcher).getByRole("link", { name: /使用中心/ }))
      .toHaveAttribute("href", "http://127.0.0.1:5194/usage/");
    expect(screen.getByRole("link", { name: /运行配置/ })).toHaveAttribute("href", "/admin/configuration");
  });

  it("keeps one configuration entry and the legacy interface switcher", () => {
    renderShell("/notifications");

    const navigation = screen.getByRole("navigation", { name: "主导航" });
    expect(within(navigation).getAllByRole("link")).toHaveLength(6);
    expect(within(navigation).getByRole("link", { name: /配置中心/ })).toHaveAttribute("aria-current", "page");
    expect(within(navigation).queryByRole("link", { name: /通知设置/ })).not.toBeInTheDocument();
    expect(within(navigation).queryByRole("link", { name: /通用设置/ })).not.toBeInTheDocument();

    const switcher = screen.getByRole("region", { name: "界面切换" });
    expect(within(switcher).getByRole("link", { name: /服务入口/ })).toHaveAttribute("href", "/");
    expect(within(switcher).getByRole("link", { name: /使用中心/ })).toHaveAttribute("href", "/usage/");
    expect(screen.getByRole("heading", { name: "配置中心" })).toBeInTheDocument();
    expect(screen.getByText("CONTROL PLANE SETTINGS")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Codex CPA Pool 管理中心" }).querySelector("img"))
      .toHaveAttribute("src", "/portal/assets/codex-cpa-pool-mark.svg");
  });

  it("keeps all configuration pages discoverable inside the single center", () => {
    renderShell("/settings", <ConfigurationSectionNav />);

    const sectionNavigation = screen.getByRole("navigation", { name: "配置中心页面" });
    expect(within(sectionNavigation).getAllByRole("link")).toHaveLength(3);
    expect(within(sectionNavigation).getByRole("link", { name: /运行配置/ })).toHaveAttribute("href", "/admin/configuration");
    expect(within(sectionNavigation).getByRole("link", { name: /通用设置/ })).toHaveAttribute("aria-current", "page");
    expect(within(sectionNavigation).getByRole("link", { name: /通知设置/ })).toHaveAttribute("href", "/admin/notifications");
  });

  it("shows the configuration center as the parent of the selected settings group", async () => {
    renderShell("/configuration", <ConfigurationDetailFixture />);

    await waitFor(() => expect(screen.getByRole("heading", { level: 1 })).toHaveTextContent("配置中心/系统设置"));
  });

  it("restores the legacy overview refresh control in the top bar", async () => {
    const user = userEvent.setup();
    renderShell("/overview");

    expect(screen.getByText("等待刷新")).toBeInTheDocument();
    const refresh = screen.getByRole("button", { name: "刷新" });
    await user.click(refresh);
    await waitFor(() => expect(refresh).toBeEnabled());
  });

  it("uses a dedicated distraction-free shell for first setup", () => {
    renderShell("/setup", <div>首次设置内容</div>);

    expect(screen.getByText("首次设置内容")).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "主导航" })).not.toBeInTheDocument();
    expect(screen.queryByRole("complementary", { name: "管理中心导航" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "刷新" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "退出" })).not.toBeInTheDocument();
  });
});
