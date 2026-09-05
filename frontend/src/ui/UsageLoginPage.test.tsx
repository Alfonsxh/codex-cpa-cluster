import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { portalSessionQueryKey } from "../api/portal";
import { UsageLoginPage } from "./UsageLoginPage";

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

function json(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
}

function setup({
  domains = ["example.com", "example.org"],
  overlay = true,
  siteResponse,
  loginFails = false
}: { domains?: string[]; overlay?: boolean; siteResponse?: () => Promise<Response>; loginFails?: boolean } = {}) {
  const logins: Array<{ email: string; password: string }> = [];
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    if (String(input) === "/site-config.json") return siteResponse?.() ?? Promise.resolve(json({ allowed_email_domains: domains }));
    if (String(input) === "/usage/session" && init?.method === "POST") {
      const credentials = JSON.parse(String(init.body));
      logins.push(credentials);
      return Promise.resolve(loginFails
        ? json({ error: { code: "invalid_credentials", message: "邮箱或密码错误" } }, 401)
        : json({ authenticated: true, user: credentials.email, expires_at: 20_000, password_change_required: false }));
    }
    return Promise.reject(new Error(`Unexpected request: ${String(input)}`));
  });
  vi.stubGlobal("fetch", fetchMock);
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={queryClient}><UsageLoginPage overlay={overlay} /></QueryClientProvider>);
  return { user: userEvent.setup(), logins, queryClient };
}

describe("UsageLoginPage email suffix selector", () => {
  it.each([true, false])("returns to the correct service entry in development=%s", async (development) => {
    vi.stubEnv("DEV", development);
    vi.stubEnv("VITE_DEV_PORTAL_ORIGIN", "http://127.0.0.1:5192");
    setup({ overlay: false });
    await screen.findByRole("button", { name: "邮箱后缀：@example.com" });
    const href = development ? "http://127.0.0.1:5192/" : "/";
    expect(screen.getByRole("link", { name: /返回服务入口/ })).toHaveAttribute("href", href);
    expect(screen.getByRole("link", { name: "返回 Codex CPA 首页" })).toHaveAttribute("href", href);
  });

  it.each([true, false])("combines the default suffix for overlay=%s without changing the session contract", async (overlay) => {
    const { user, logins, queryClient } = setup({ domains: [" @Example.COM ", "example.com"], overlay });
    await screen.findByRole("button", { name: "邮箱后缀：@example.com" });
    await user.type(screen.getByRole("textbox", { name: "邮箱用户名" }), "alice");
    await user.type(screen.getByLabelText("密码"), "test-password{Enter}");
    await waitFor(() => expect(logins).toEqual([{ email: "alice@example.com", password: "test-password" }]));
    await waitFor(() => expect(queryClient.getQueryData(portalSessionQueryKey)).toMatchObject({ authenticated: true, user: "alice@example.com" }));
    expect(screen.getByRole("textbox", { name: "邮箱用户名" })).toHaveValue("");
    expect(screen.getByLabelText("密码")).toHaveValue("");
  });

  it("supports keyboard suffix selection and keeps the selected identity after a rejected login", async () => {
    const { user, logins } = setup({ loginFails: true });
    await user.type(screen.getByRole("textbox", { name: "邮箱用户名" }), "alice");
    await user.tab();
    expect(screen.getByRole("button", { name: "邮箱后缀：@example.com" })).toHaveFocus();
    await user.keyboard("{Enter}");
    expect(within(screen.getByRole("listbox", { name: "邮箱后缀" })).getAllByRole("option")).toHaveLength(2);
    await user.keyboard("{ArrowDown}{Enter}");
    expect(logins).toHaveLength(0);
    await user.tab();
    expect(screen.getByLabelText("密码")).toHaveFocus();
    await user.keyboard("test-password{Enter}");
    expect(await screen.findByRole("alert")).toHaveTextContent("邮箱或密码错误");
    expect(logins).toEqual([{ email: "alice@example.org", password: "test-password" }]);
    expect(screen.getByRole("textbox", { name: "邮箱用户名" })).toHaveValue("alice");
    expect(screen.getByRole("button", { name: "邮箱后缀：@example.org" })).toBeEnabled();
    expect(screen.getByLabelText("密码")).toHaveValue("test-password");
  });

  it("splits a pasted address and does not append a second suffix", async () => {
    const { user, logins } = setup();
    await screen.findByRole("button", { name: "邮箱后缀：@example.com" });
    const input = screen.getByRole("textbox", { name: "邮箱用户名" });
    await user.click(input);
    await user.paste(" alice@Example.ORG ");
    expect(input).toHaveValue("alice");
    expect(screen.getByRole("button", { name: "邮箱后缀：@example.org" })).toBeInTheDocument();
    await user.type(screen.getByLabelText("密码"), "test-password{Enter}");
    await waitFor(() => expect(logins[0]?.email).toBe("alice@example.org"));
  });

  it("does not replace unconfigured domains or send invalid addresses to the login endpoint", async () => {
    const { user, logins } = setup();
    const input = screen.getByRole("textbox", { name: "邮箱用户名" });
    await user.type(input, "alice@unconfigured.test");
    await user.type(screen.getByLabelText("密码"), "test-password{Enter}");
    expect(await screen.findByText(/邮箱后缀不匹配/)).toBeInTheDocument();
    expect(input).toHaveValue("alice@unconfigured.test");
    expect(logins).toHaveLength(0);
    await user.clear(input);
    await user.type(input, "invalid name");
    await user.click(screen.getByRole("button", { name: "登录" }));
    expect(await screen.findByText("请输入有效的企业邮箱")).toBeInTheDocument();
    expect(logins).toHaveLength(0);
  });

  it("preserves drafts while loading and retrying suffix configuration", async () => {
    let finishLoading: ((response: Response) => void) | undefined;
    const pending = new Promise<Response>((resolve) => { finishLoading = resolve; });
    let attempts = 0;
    const { user, logins } = setup({ siteResponse: () => ++attempts === 1 ? pending : Promise.resolve(json({ allowed_email_domains: ["example.com", "example.com.test"] })) });
    const input = screen.getByRole("textbox", { name: "邮箱用户名" });
    await user.type(input, "alice@example.com.test");
    await user.type(screen.getByLabelText("密码"), "test-password{Enter}");
    expect(screen.getByRole("button", { name: "登录" })).toBeDisabled();
    expect(logins).toHaveLength(0);
    finishLoading?.(json({ error: { message: "configuration unavailable" } }, 503));
    await user.click(await screen.findByRole("button", { name: "重试" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "登录" })).toBeEnabled());
    expect(input).toHaveValue("alice@example.com.test");
    expect(screen.getByLabelText("密码")).toHaveValue("test-password");
    await user.click(screen.getByRole("button", { name: "登录" }));
    await waitFor(() => expect(logins[0]?.email).toBe("alice@example.com.test"));
  });

  it("requires configured suffixes and explains how to recover when they are missing", async () => {
    const { logins } = setup({ domains: [] });
    expect(await screen.findByText("尚未配置企业邮箱后缀，请联系管理员设置。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "登录" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "邮箱后缀：暂无可用后缀" })).toBeDisabled();
    expect(logins).toHaveLength(0);
  });
});
