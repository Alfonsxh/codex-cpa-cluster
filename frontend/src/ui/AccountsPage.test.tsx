import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { AccountsPage } from "./AccountsPage";

const catalog = {
  accounts: [
    {
      id: "alpha",
      email: "alpha@example.com",
      port: 18319,
      proxy_mode: "inherit",
      enabled: true,
      default: true,
      routed_users: 3,
      active_users_1h: 2,
      proxy_configured: false,
      account_state: {
        account: "alpha",
        eligible: true,
        exhausted: false,
        reason: "available",
        used_percent: 20,
        remaining_percent: 80,
        headroom: 75,
        reset_at: 0,
        observed_at: 100
      },
      state_available: true
    },
    {
      id: "beta",
      email: "beta@example.com",
      port: 18320,
      proxy_mode: "direct",
      enabled: true,
      default: false,
      routed_users: 1,
      active_users_1h: 1,
      proxy_configured: false,
      account_state: {
        account: "beta",
        eligible: true,
        exhausted: false,
        reason: "available",
        used_percent: 10,
        remaining_percent: 90,
        headroom: 85,
        reset_at: 0,
        observed_at: 100
      },
      state_available: true
    }
  ],
  generated_at: 100,
  warnings: []
};

describe("AccountsPage", () => {
  it("shows a runtime warning without disguising it as healthy migration capacity", async () => {
    const degradedCatalog = {
      ...catalog,
      accounts: catalog.accounts.map((account) => account.id === "alpha"
        ? { ...account, account_state: { ...account.account_state, reason: "degraded" } }
        : account)
    };
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(jsonResponse(degradedCatalog)));
    renderPage();

    expect(await screen.findByText("近期异常")).toBeInTheDocument();
  });

  it("confirms global rebalance and refreshes only the account query", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(catalog))
      .mockResolvedValueOnce(jsonResponse({
        message: "账号用户负载均衡已完成，近 1 小时活跃用户数已刷新",
        rebalance: {
          moved_users: 1,
          destinations: { beta: 1 },
          target_counts: { alpha: 2, beta: 2 },
          snapshot_generation: "0123456789abcdef0123456789abcdef",
          active_users_1h: { alpha: 2, beta: 2 },
          activity_refreshed: true
        }
      }))
      .mockResolvedValueOnce(jsonResponse(catalog));
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
    });
    queryClient.setQueryData(["teams"], { teams: [{ id: "preserved" }] });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <AccountsPage csrfToken="csrf-test" />
      </QueryClientProvider>
    );

    expect(await screen.findByText("alpha@example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "一键负载均衡" }));
    expect(screen.getByRole("dialog", { name: "一键负载均衡所有账号" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认开始均衡" }));

    expect(await screen.findByText("迁移用户：1")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    const mutation = fetchMock.mock.calls[1];
    expect(mutation[0]).toBe("/admin/api/accounts/rebalance-all");
    expect(mutation[1]).toMatchObject({
      method: "POST",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    expect(queryClient.getQueryData(["teams"])).toEqual({ teams: [{ id: "preserved" }] });
  });

  it("creates an account through the write-only lifecycle API and refreshes the catalog", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(catalog))
      .mockResolvedValueOnce(jsonResponse({
        message: "业务 CPA 已创建并通过运行探针",
        account: {
          account: {
            id: "gamma",
            email: "gamma@example.com",
            port: 18321,
            proxy_mode: "inherit",
            created_at: 101,
            group_enabled: true,
            default_group: false
          },
          created_key_rows: 4,
          snapshot_generation: "new-snapshot"
        }
      }))
      .mockResolvedValueOnce(jsonResponse(catalog));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha@example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "创建账号" }));
    expect(screen.getByRole("dialog", { name: "创建业务 CPA" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("CPA 标识"), "Gamma");
    await user.type(screen.getByLabelText("上游账号邮箱"), "Gamma@Example.com");
    await user.click(screen.getByRole("button", { name: "创建并探测" }));

    expect(await screen.findByText("业务 CPA 已创建并通过运行探针")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    expect(fetchMock.mock.calls[1][0]).toBe("/admin/api/accounts");
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({
      id: "gamma",
      email: "gamma@example.com",
      proxy_mode: "inherit"
    });
  });

  it("never echoes an encrypted proxy and only submits an explicitly replaced value", async () => {
    const proxyCatalog = {
      ...catalog,
      accounts: catalog.accounts.map((account) => account.id === "alpha"
        ? { ...account, proxy_mode: "custom", proxy_configured: true }
        : account)
    };
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(proxyCatalog))
      .mockResolvedValueOnce(jsonResponse({
        message: "CPA 账号已更新并通过运行探针",
        account: {
          account: {
            id: "alpha",
            email: "alpha@example.com",
            port: 18319,
            proxy_mode: "custom",
            created_at: 100,
            group_enabled: true,
            default_group: true
          },
          rerouted_users: 0,
          snapshot_generation: "updated-snapshot"
        }
      }))
      .mockResolvedValueOnce(jsonResponse(proxyCatalog));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha@example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "编辑 alpha" }));
    const proxyInput = screen.getByLabelText("独立代理地址");
    expect(proxyInput).toHaveValue("");
    expect(proxyInput).toHaveAttribute("placeholder", "已安全配置（留空保持不变）");
    await user.type(proxyInput, "socks5://user:password@127.0.0.1:1080");
    await user.click(screen.getByRole("button", { name: "保存并重建" }));

    expect(await screen.findByText("CPA 账号已更新并通过运行探针")).toBeInTheDocument();
    const payload = JSON.parse(fetchMock.mock.calls[1][1].body);
    expect(payload.proxy_url).toBe("socks5://user:password@127.0.0.1:1080");
    expect(payload).toMatchObject({ id: "alpha", new_id: "alpha", proxy_mode: "custom" });
  });

  it("requires a fallback and exact confirmation before deleting an account", async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(catalog))
      .mockResolvedValueOnce(jsonResponse({
        message: "业务 CPA 已删除，配置、授权和日志已安全归档",
        account: {
          account_id: "alpha",
          removed_key_rows: 4,
          revoked_exclusive_keys: 0,
          rerouted_users: 3,
          replacement_account: "beta",
          backup: "state/backups/account-alpha",
          snapshot_generation: "delete-snapshot"
        }
      }))
      .mockResolvedValueOnce(jsonResponse(catalog));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderPage();

    expect(await screen.findByText("alpha@example.com")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "alpha 更多操作" }));
    await user.click(await screen.findByText("删除账号"));
    expect(screen.getByRole("dialog", { name: "删除 CPA · alpha" })).toBeInTheDocument();
    await user.click(screen.getByLabelText("删除备用账号"));
    await user.click(await screen.findByText("beta · beta@example.com"));
    await user.type(screen.getByLabelText("输入 alpha 确认"), "alpha");
    await user.click(screen.getByRole("button", { name: "确认删除并归档" }));

    expect(await screen.findByText("业务 CPA 已删除，配置、授权和日志已安全归档")).toBeInTheDocument();
    expect(JSON.parse(fetchMock.mock.calls[1][1].body)).toEqual({
      id: "alpha",
      confirm: "alpha",
      revoke_keys: false,
      fallback_account: "beta"
    });
  });
});

function renderPage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <AccountsPage csrfToken="csrf-test" />
    </QueryClientProvider>
  );
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}
