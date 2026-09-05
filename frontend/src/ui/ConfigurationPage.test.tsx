import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import type { ConfigurationCatalog } from "../api/configuration";
import { ConfigurationPage } from "./ConfigurationPage";

describe("ConfigurationPage", () => {
  it("opens the deep-linked access section used by first-run guidance", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      throw new Error(`unexpected request: ${path}`);
    }));
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />, "/configuration?section=access");

    const access = within(await screen.findByRole("navigation", { name: "系统管理" }))
      .getByRole("button", { name: /访问凭据/ });
    await waitFor(() => expect(access).toHaveAttribute("aria-current", "page"));
    expect(screen.getByRole("button", { name: "设置用户初始密码" })).toBeInTheDocument();
  });

  it("loads only the complete fine-grained catalog, masks secrets, searches and saves changed live fields", async () => {
    let current = configurationFixture();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${path}`);
      if (init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { confirm: string; values: Record<string, unknown> };
        current = withUpdatedValues(current, body.values);
        return jsonResponse({
          message: "已保存 1 项配置",
          changed: Object.keys(body.values),
          applied: ["live"],
          pending_deployment: false
        });
      }
      return jsonResponse(current);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    expect(await screen.findByPlaceholderText("已配置；留空保持不变")).toHaveValue("");
    expect(fetchMock).toHaveBeenCalledTimes(4);

    await user.type(screen.getByLabelText("搜索配置"), "产品名称{Enter}");
    const productName = await screen.findByLabelText("产品名称");
    await user.clear(productName);
    await user.type(productName, "CPA Control");
    expect(screen.getByText("1 项未保存")).toBeInTheDocument();
    productName.focus();
    await user.keyboard("{Enter}");

    expect(await screen.findByText("已保存 1 项配置")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(6));
    const post = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(post?.[0]).toBe("/admin/api/settings/configuration");
    expect(post?.[1]).toMatchObject({
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({
      confirm: "save",
      values: { "branding.product_name": "CPA Control" }
    });
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/settings")).toBe(false);
  });

  it("requires an impact confirmation before saving account rebuild fields", async () => {
    const current = configurationFixture();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${path}`);
      if (init?.method === "POST") {
        return jsonResponse({
          message: "已保存 1 项配置",
          changed: ["cpa.request_retry"],
          applied: ["accounts"],
          pending_deployment: false
        });
      }
      return jsonResponse(current);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    const retry = await screen.findByLabelText("请求重试次数");
    await user.clear(retry);
    await user.type(retry, "3");
    await user.click(screen.getByRole("button", { name: "保存配置" }));
    expect(await screen.findByRole("dialog", { name: "保存 1 项配置？" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "保存并应用" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
  });

  it("keeps apply failures visible in the confirmation dialog and lets the operator close it", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${path}`);
      if (init?.method === "POST") {
        return new Response(JSON.stringify({
          error: { message: "配置应用失败，已尝试恢复原配置", type: "request_error", code: "configuration_apply_failed" }
        }), { status: 502, headers: { "Content-Type": "application/json" } });
      }
      return jsonResponse(configurationFixture());
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    const retry = await screen.findByLabelText("请求重试次数");
    await user.clear(retry);
    await user.type(retry, "3");
    await user.click(screen.getByRole("button", { name: "保存配置" }));
    await user.click(screen.getByRole("button", { name: "保存并应用" }));

    expect(await screen.findByText("配置应用失败，已尝试恢复原配置")).toBeInTheDocument();
    expect(screen.getByRole("dialog", { name: "保存 1 项配置？" })).toBeInTheDocument();
    await user.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog", { name: "保存 1 项配置？" })).not.toBeInTheDocument());
  });

  it("renders field descriptions and places the WeCom switch before the webhook editor", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    await screen.findByLabelText("请求重试次数");
    expect(screen.queryByText("统一作用于所有业务 CPA。")).not.toBeInTheDocument();
    expect(screen.getByText("单次上游请求失败后的重试次数。")).toBeInTheDocument();
    expect(screen.queryByText("branding.product_name", { exact: true })).not.toBeInTheDocument();
    expect(screen.queryByText("默认 Codex CPA Cluster", { exact: true })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /企业微信通知/ }));
    const enabled = screen.getByLabelText("启用企业微信通知");
    const webhook = screen.getByText("企业微信群 Webhook");
    expect(enabled.compareDocumentPosition(webhook) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("loads the destructive all-user impact only when User Quota is opened and refreshes it after reset", async () => {
    let quotaReads = 0;
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      if (path === "/admin/api/users/quota-actions" && init?.method === "POST") {
        return jsonResponse({
          action: "reset_usage",
          applied_users: ["alice@example.com", "bob@example.com"],
          skipped_users: [],
          message: "已清零 2 位用户的本周已用量；将在下次采集后生效",
          quota_operations: quotaSummary(0)
        });
      }
      if (path === "/admin/api/users/quota-actions") {
        quotaReads += 1;
        return jsonResponse(quotaSummary(quotaReads === 1 ? 2 : 0));
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    await screen.findByPlaceholderText("已配置；留空保持不变");
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/users/quota-actions")).toBe(false);
    await user.click(screen.getByRole("button", { name: /用户额度/ }));
    expect(await screen.findByText("2 位有用量")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "清零全部用户本周已用量" }));
    const reason = screen.getByLabelText("操作原因");
    await user.type(reason, "incident{Enter}correction");
    expect(reason).toHaveValue("incident\ncorrection");
    await user.type(screen.getByLabelText("清零确认文字"), "RESET ALL USERS");
    await user.keyboard("{Enter}");
    expect(fetchMock.mock.calls.filter(([path, init]) => String(path) === "/admin/api/users/quota-actions" && init?.method === "POST")).toHaveLength(0);
    await user.click(screen.getByRole("button", { name: "确认清零" }));

    expect(await screen.findByText("已清零 2 位用户的本周已用量；将在下次采集后生效")).toBeInTheDocument();
    await waitFor(() => expect(quotaReads).toBe(2));
    expect(screen.getByRole("button", { name: "当前无需清零" })).toBeDisabled();
  });

  it("keeps access, storage and audit in the same workspace and expires the session after key rotation", async () => {
    const rotated = vi.fn();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      if (path === "/admin/api/settings/management-key" && init?.method === "POST") {
        return jsonResponse({ message: "管理密钥已更新，请重新登录", rotated: true, services: 0 });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" onManagementKeyRotated={rotated} />);

    const storageButton = await screen.findByRole("button", { name: /本地数据/ });
    await user.click(storageButton);
    expect(storageButton).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("heading", { name: "持久化数据" })).toBeInTheDocument();
    expect(screen.getByText("state/control-plane.sqlite3")).toBeInTheDocument();
    const auditButton = screen.getByRole("button", { name: /审计记录/ });
    await user.click(auditButton);
    expect(auditButton).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("heading", { name: "最近管理操作" })).toBeInTheDocument();
    expect(screen.getByText("configuration.update")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: /访问凭据/ }));
    await user.click(screen.getByRole("button", { name: "更换管理密钥" }));
    const newKey = screen.getByLabelText("新管理密钥");
    const confirmation = screen.getByLabelText("再次输入管理密钥");
    await user.type(newKey, "replacement-key-2026");
    await user.tab();
    expect(confirmation).toHaveFocus();
    await user.type(confirmation, "replacement-key-2026");
    await user.tab();
    expect(screen.getByRole("button", { name: "更新并重新进入" })).toHaveFocus();
    await user.keyboard("{Enter}");

    await waitFor(() => expect(rotated).toHaveBeenCalledWith("管理密钥已更新，请重新登录"));
    const request = fetchMock.mock.calls.find(([path]) => String(path) === "/admin/api/settings/management-key");
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({ new_key: "replacement-key-2026", confirmation: "replacement-key-2026" });
  });

  it("presents an informative and recoverable audit empty state", async () => {
    let workspaceReads = 0;
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      if (path === "/admin/api/settings/workspace") {
        workspaceReads += 1;
        return jsonResponse({
          storage: [{ label: "控制面数据库", path: "state/control-plane.sqlite3", exists: true, mode: "600" }],
          backups: { count: 1, latest: "backups/accounts/fixture" },
          recent_audit: []
        });
      }
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") return jsonResponse(configurationFixture());
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    await user.click(await screen.findByRole("button", { name: /审计记录/ }));
    expect(screen.getByRole("heading", { name: "最近管理操作" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "暂无管理操作" })).toBeInTheDocument();
    expect(screen.getByText("新的配置和维护操作会显示在这里。")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "刷新审计记录" }));

    await waitFor(() => expect(workspaceReads).toBe(2));
  });

  it("shows a recoverable empty state when the configuration catalog has no groups", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      const supporting = supportingSettingsResponse(path);
      if (supporting) return supporting;
      if (path === "/admin/api/settings/configuration") {
        return jsonResponse({ version: 1, generated_at: 1_800_000_000, field_count: 0, groups: [] });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderConfiguration(<ConfigurationPage csrfToken="csrf-test" />);

    expect(await screen.findByText("当前没有可配置项")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "进入访问凭据" }));
    expect(screen.getByText("管理密钥已配置")).toBeInTheDocument();
  });
});

function configurationFixture(): ConfigurationCatalog {
  return {
    version: 1,
    generated_at: 1_800_000_000,
    field_count: 6,
    groups: [
      {
        name: "CPA 请求",
        description: "统一作用于所有业务 CPA。",
        fields: [
          {
            key: "cpa.proxy_url",
            label: "默认上游代理 URL",
            description: "加密保存，不会回显。",
            type: "proxy_url_secret",
            value: "",
            default: "",
            apply_mode: "accounts",
            editable: true,
            configured: true
          },
          {
            key: "cpa.request_retry",
            label: "请求重试次数",
            description: "单次上游请求失败后的重试次数。",
            type: "integer",
            value: 2,
            default: 2,
            apply_mode: "accounts",
            editable: true,
            min: 0,
            max: 10
          }
        ]
      },
      {
        name: "品牌与身份",
        description: "管理公开名称和客户端导出参数。",
        fields: [
          {
            key: "branding.product_name",
            label: "产品名称",
            description: "所有页面显示的完整名称。",
            type: "text",
            value: "Codex CPA Cluster",
            default: "Codex CPA Cluster",
            apply_mode: "live",
            editable: true,
            min_length: 2,
            max_length: 64
          }
        ]
      },
      {
        name: "账号自动切换",
        description: "额度耗尽后自动迁移路由。",
        fields: [
          {
            key: "account_failover.mode",
            label: "自动切换模式",
            description: "只保留关闭或自动执行。",
            type: "choice",
            value: "active",
            default: "active",
            apply_mode: "live",
            editable: true,
            choices: [
              { value: "off", label: "关闭" },
              { value: "active", label: "自动执行" }
            ]
          }
        ]
      },
      {
        name: "用户额度",
        description: "全部用户的系统默认周额度与网关故障策略。",
        fields: [
          {
            key: "user_quota.default_weekly_tokens",
            label: "用户周额度系统默认值",
            description: "个人未配置策略时使用。",
            type: "nullable_integer",
            value: 20_000_000,
            default: null,
            apply_mode: "quota",
            editable: true,
            unit: "Token",
            min: 1,
            max: 1_000_000_000_000
          }
        ]
      },
      {
        name: "企业微信通知",
        description: "企业微信通知配置。",
        fields: [
          {
            key: "notification.enabled",
            label: "启用企业微信通知",
            description: "启用定时通知。",
            type: "boolean",
            value: false,
            default: false,
            apply_mode: "live",
            editable: true
          }
        ]
      }
    ]
  };
}

function withUpdatedValues(catalog: ConfigurationCatalog, values: Record<string, unknown>): ConfigurationCatalog {
  return {
    ...catalog,
    generated_at: catalog.generated_at + 1,
    groups: catalog.groups.map((group) => ({
      ...group,
      fields: group.fields.map((field) => Object.prototype.hasOwnProperty.call(values, field.key)
        ? { ...field, value: values[field.key] as never }
        : field)
    }))
  };
}

function renderConfiguration(element: React.ReactNode, entry = "/configuration") {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <QueryClientProvider client={queryClient}>{element}</QueryClientProvider>
    </MemoryRouter>
  );
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}

function supportingSettingsResponse(path: string) {
  if (path === "/admin/api/settings/general") {
    return jsonResponse({
      version: 1,
      apply_mode: "live",
      generated_at: 1_800_000_000,
      values: {
        product_name: "Codex CPA Cluster",
        short_name: "Codex CPA",
        environment_label: "Test",
        public_base_url: "https://example.test",
        allowed_email_domains: ["example.com"],
        key_prefix: "cpa_",
        provider_name: "Codex CPA",
        api_key_env: "CPA_API_KEY",
        default_model: "gpt-5.6-sol"
      },
      security: { management_key_configured: true, initial_password_configured: true },
      branding: { custom_logo: false }
    });
  }
  if (path === "/admin/api/settings/notifications") {
    return jsonResponse({
      notifications: { webhook_configured: false, webhook_url: "", heartbeat_at: null, last_success_at: null, last_error: "", next_schedule_at: null },
      values: { enabled: false, timezone: "UTC", daily_times: "09:00", schedule_grace_minutes: 15, quota_alert_enabled: true, weekly_threshold_percent: 90 }
    });
  }
  if (path === "/admin/api/settings/workspace") {
    return jsonResponse({
      storage: [{ label: "控制面数据库", path: "state/control-plane.sqlite3", exists: true, mode: "600" }],
      backups: { count: 1, latest: "backups/accounts/fixture" },
      recent_audit: [{ timestamp: 1_800_000_000, action: "configuration.update", target: "settings", outcome: "accepted" }]
    });
  }
  return null;
}

function quotaSummary(usersWithUsage: number) {
  return {
    total_users: 3,
    users_with_usage: usersWithUsage,
    total_used_tokens: usersWithUsage ? 3_000_000 : 0,
    total_raw_used_tokens: usersWithUsage ? 2_000_000 : 0,
    users_with_personal_policy: 0,
    users_with_bonus: 0,
    users_with_usage_reset: 0,
    week_start_at: 1_799_900_000,
    week_end_at: 1_800_500_000
  };
}
