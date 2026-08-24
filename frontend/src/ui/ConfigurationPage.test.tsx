import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import type { ConfigurationCatalog } from "../api/configuration";
import { ConfigurationPage } from "./ConfigurationPage";

describe("ConfigurationPage", () => {
  it("loads only the complete fine-grained catalog, masks secrets, searches and saves changed live fields", async () => {
    let current = configurationFixture();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
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

    expect(await screen.findByText("当前秘密已配置；留空不会覆盖。")).toBeInTheDocument();
    expect(screen.getByPlaceholderText("已配置；留空保持不变")).toHaveValue("");
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await user.type(screen.getByLabelText("搜索配置"), "产品名称");
    const productName = await screen.findByLabelText("产品名称");
    await user.clear(productName);
    await user.type(productName, "CPA Control");
    expect(screen.getByText("1 项未保存")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存配置" }));

    expect(await screen.findByText("已保存 1 项配置")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
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
      if (String(input) !== "/admin/api/settings/configuration") throw new Error(`unexpected request: ${String(input)}`);
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
    expect(await screen.findByRole("dialog", { name: "保存并应用配置？" })).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(0);

    await user.click(screen.getByRole("button", { name: "保存并应用" }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([, init]) => init?.method === "POST")).toHaveLength(1));
  });
});

function configurationFixture(): ConfigurationCatalog {
  return {
    version: 1,
    generated_at: 1_800_000_000,
    field_count: 4,
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

function renderConfiguration(element: React.ReactNode) {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
  });
  return render(<QueryClientProvider client={queryClient}>{element}</QueryClientProvider>);
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}
