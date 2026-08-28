import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { GeneralSettingsPage } from "./GeneralSettingsPage";

const current = {
  version: 1,
  apply_mode: "live",
  generated_at: 1_800_000_000,
  values: {
    product_name: "Codex CPA Cluster",
    short_name: "Codex CPA",
    environment_label: "Internal",
    public_base_url: "https://cpa.example.com",
    allowed_email_domains: ["example.com"],
    key_prefix: "cpa_",
    provider_name: "Codex CPA",
    api_key_env: "CPA_API_KEY",
    default_model: "gpt-5.6-sol"
  },
  security: {
    management_key_configured: true,
    initial_password_configured: false
  },
  branding: { custom_logo: false }
};

describe("GeneralSettingsPage", () => {
  it("uses the fine-grained live settings API and sends a typed CSRF mutation", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/settings/general" && init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { values: typeof current.values };
        return jsonResponse({
          message: "通用设置已保存并实时生效",
          settings: { ...current, values: body.values }
        });
      }
      if (path === "/admin/api/settings/general") return jsonResponse(current);
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
    });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <GeneralSettingsPage csrfToken="csrf-test" />
      </QueryClientProvider>
    );

    const productName = await screen.findByLabelText("产品名称");
    expect(productName).toHaveValue("Codex CPA Cluster");
    expect(screen.getByText("未配置")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await user.clear(productName);
    await user.type(productName, "CPA Control");
    await user.clear(screen.getByLabelText("允许的邮箱域名"));
    await user.type(screen.getByLabelText("允许的邮箱域名"), "Example.com, example.org");
    await user.click(screen.getByRole("button", { name: "保存通用设置" }));

    expect(await screen.findByText("通用设置已保存并实时生效")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const request = fetchMock.mock.calls[1];
    expect(request[0]).toBe("/admin/api/settings/general");
    expect(request[1]).toMatchObject({
      method: "PUT",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    const body = JSON.parse(String(request[1]?.body));
    expect(body).toMatchObject({
      confirm: "save",
      values: {
        product_name: "CPA Control",
        allowed_email_domains: ["Example.com", "example.org"]
      }
    });
    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/settings")).toBe(false);
  });

  it("sets the future-user password through an ephemeral validated modal", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/settings/initial-password" && init?.method === "POST") {
        return jsonResponse({ message: "用户初始密码已安全保存；已有用户密码不会自动变化", configured: true });
      }
      if (path === "/admin/api/settings/general") return jsonResponse(current);
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false }, mutations: { retry: false } }
    });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <GeneralSettingsPage csrfToken="csrf-test" />
      </QueryClientProvider>
    );

    await user.click(await screen.findByRole("button", { name: "立即设置" }));
    await user.type(screen.getByLabelText("新初始密码"), "future-user-password!");
    await user.type(screen.getByLabelText("再次输入"), "future-user-password!");
    await user.click(screen.getByRole("button", { name: "安全保存" }));

    expect(await screen.findByText("用户初始密码已安全保存；已有用户密码不会自动变化")).toBeInTheDocument();
    expect(screen.queryByLabelText("新初始密码")).not.toBeInTheDocument();
    expect(screen.queryByDisplayValue("future-user-password!")).not.toBeInTheDocument();
    const request = fetchMock.mock.calls.find(([path]) => String(path) === "/admin/api/settings/initial-password");
    expect(request?.[1]).toMatchObject({
      method: "POST",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({
      initial_password: "future-user-password!",
      confirmation: "future-user-password!"
    });
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("uploads and resets the public Logo without using browser storage", async () => {
    let branding = { custom_logo: false, logo_sha256: undefined as string | undefined };
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/settings/general") return jsonResponse({ ...current, branding });
      if (path === "/admin/api/settings/logo" && init?.method === "POST") {
        const body = JSON.parse(String(init.body));
        expect(body).toMatchObject({
          filename: "brand.svg",
          content_type: "image/svg+xml",
          confirm: "save"
        });
        expect(atob(body.data_base64)).toContain("<svg");
        branding = { custom_logo: true, logo_sha256: "0123456789abcdef" };
        return jsonResponse({
          message: "Logo 已更新",
          logo: { custom: true, url: "/branding/logo", content_type: "image/svg+xml", sha256: branding.logo_sha256 }
        });
      }
      if (path === "/admin/api/settings/logo" && init?.method === "DELETE") {
        expect(JSON.parse(String(init.body))).toEqual({ confirm: "reset" });
        branding = { custom_logo: false, logo_sha256: undefined };
        return jsonResponse({ message: "已恢复默认 Logo", logo: { custom: false } });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <GeneralSettingsPage csrfToken="csrf-test" />
      </QueryClientProvider>
    );

    await user.click(await screen.findByRole("button", { name: "上传" }));
    await user.upload(
      screen.getByLabelText("Logo 文件"),
      new File([`<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`], "brand.svg", { type: "image/svg+xml" })
    );
    await user.click(screen.getByRole("button", { name: "保存 Logo" }));
    expect(await screen.findByText("Logo 已更新")).toBeInTheDocument();
    expect(screen.getByText("自定义")).toBeInTheDocument();
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);

    await user.click(screen.getByRole("button", { name: "恢复默认" }));
    await user.click(screen.getByRole("button", { name: "确认恢复" }));
    expect(await screen.findByText("已恢复默认 Logo")).toBeInTheDocument();
    expect(screen.getByText("默认")).toBeInTheDocument();
  });

  it("rotates the management key and immediately hands control back to the login boundary", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/settings/general") return jsonResponse(current);
      if (path === "/admin/api/settings/management-key" && init?.method === "POST") {
        return jsonResponse({
          message: "管理密钥已更新，请使用新密钥重新进入",
          result: { rotated: true, services: 0 }
        });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const onManagementKeyRotated = vi.fn();
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <GeneralSettingsPage csrfToken="csrf-test" onManagementKeyRotated={onManagementKeyRotated} />
      </QueryClientProvider>
    );

    await user.click(await screen.findByRole("button", { name: "轮换密钥" }));
    await user.type(screen.getByLabelText("新管理密钥"), "replacement-management-key");
    await user.type(screen.getByLabelText("确认新管理密钥"), "replacement-management-key");
    await user.click(screen.getByRole("button", { name: "确认轮换并重新登录" }));

    await waitFor(() => expect(onManagementKeyRotated).toHaveBeenCalledWith("管理密钥已更新，请使用新密钥重新进入"));
    const request = fetchMock.mock.calls.find(([path]) => String(path) === "/admin/api/settings/management-key");
    expect(request?.[1]).toMatchObject({
      method: "POST",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({
      new_key: "replacement-management-key",
      confirmation: "replacement-management-key"
    });
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });
});

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}
