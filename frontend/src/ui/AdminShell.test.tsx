import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { AdminShell } from "./App";
import { ConfigurationSectionNav } from "./ConfigurationSectionNav";
import { ThemeProvider } from "./ThemeProvider";

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

describe("AdminShell legacy visual contract", () => {
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
    expect(screen.getByText("CONFIGURATION CENTER")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Codex CPA 管理中心" }).querySelector("img"))
      .toHaveAttribute("src", "/portal/assets/codex-cpa-cluster-mark.svg");
  });

  it("keeps all configuration pages discoverable inside the single center", () => {
    renderShell("/settings", <ConfigurationSectionNav />);

    const sectionNavigation = screen.getByRole("navigation", { name: "配置中心页面" });
    expect(within(sectionNavigation).getAllByRole("link")).toHaveLength(3);
    expect(within(sectionNavigation).getByRole("link", { name: /运行配置/ })).toHaveAttribute("href", "/admin/configuration");
    expect(within(sectionNavigation).getByRole("link", { name: /通用设置/ })).toHaveAttribute("aria-current", "page");
    expect(within(sectionNavigation).getByRole("link", { name: /通知设置/ })).toHaveAttribute("href", "/admin/notifications");
  });

  it("restores the legacy overview refresh control in the top bar", async () => {
    const user = userEvent.setup();
    renderShell("/overview");

    expect(screen.getByText("总览已更新")).toBeInTheDocument();
    const refresh = screen.getByRole("button", { name: "刷新" });
    await user.click(refresh);
    expect(refresh).toBeEnabled();
  });
});
