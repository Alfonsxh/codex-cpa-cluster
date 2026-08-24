import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { NotificationSettingsPage } from "./NotificationSettingsPage";

const settings = {
  notifications: {
    webhook_configured: true,
    webhook_url: "",
    heartbeat_at: 1_800_000_000,
    last_success_at: null,
    last_error: "",
    next_schedule_at: 1_800_000_600
  },
  values: {
    enabled: true,
    timezone: "Asia/Shanghai",
    daily_times: "09:00,18:00",
    schedule_grace_minutes: 15,
    quota_alert_enabled: true,
    weekly_threshold_percent: 90
  }
};

describe("NotificationSettingsPage", () => {
  it("loads only its fine-grained status and sends with CSRF on demand", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/settings/notifications" && (!init?.method || init.method === "GET")) {
        return new Response(JSON.stringify(settings), { status: 200, headers: { "Content-Type": "application/json" } });
      }
      if (path === "/admin/api/notifications/send" && init?.method === "POST") {
        return new Response(JSON.stringify({ message: "账号信息已发送到企业微信群", format: "markdown_v2" }), {
          status: 200,
          headers: { "Content-Type": "application/json" }
        });
      }
      if (path === "/admin/api/notifications/test" && init?.method === "POST") {
        return new Response(JSON.stringify({ message: "测试消息已发送到企业微信群", format: "markdown_v2" }), {
          status: 200,
          headers: { "Content-Type": "application/json" }
        });
      }
      throw new Error(`unexpected request: ${path} ${init?.method ?? "GET"}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <NotificationSettingsPage csrfToken="csrf-test" />
      </QueryClientProvider>
    );

    expect(await screen.findByText("已配置")).toBeInTheDocument();
    expect(await screen.findByDisplayValue("Asia/Shanghai")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole("button", { name: "发送账号信息" }));
    expect(await screen.findByText("账号信息已发送到企业微信群")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/notifications/send", expect.objectContaining({
      method: "POST",
      credentials: "same-origin",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    }));
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("/accounts"))).toBe(false);

    await user.click(screen.getByRole("button", { name: "发送测试消息" }));
    expect(await screen.findByText("测试消息已发送到企业微信群")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/notifications/test", expect.objectContaining({
      method: "POST",
      credentials: "same-origin",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    }));
  });

  it("clears the webhook and keeps the disabled form state after refetch", async () => {
	let current = structuredClone(settings);
	const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
	  const path = String(input);
	  if (path === "/admin/api/settings/notifications" && (!init?.method || init.method === "GET")) {
		return new Response(JSON.stringify(current), { status: 200, headers: { "Content-Type": "application/json" } });
	  }
	  if (path === "/admin/api/settings/notification-webhook/clear" && init?.method === "POST") {
		current = {
		  ...current,
		  notifications: { ...current.notifications, webhook_configured: false, webhook_url: "" },
		  values: { ...current.values, enabled: false }
		};
		return new Response(JSON.stringify({ message: "企业微信 Webhook 已清除，通知已关闭", notifications: current.notifications }), {
		  status: 200,
		  headers: { "Content-Type": "application/json" }
		});
	  }
	  throw new Error(`unexpected request: ${path} ${init?.method ?? "GET"}`);
	});
	vi.stubGlobal("fetch", fetchMock);
	const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
	const user = userEvent.setup();
	render(
	  <QueryClientProvider client={queryClient}>
		<NotificationSettingsPage csrfToken="csrf-test" />
	  </QueryClientProvider>
	);

	expect(await screen.findByText("已配置")).toBeInTheDocument();
	await user.click(screen.getByRole("button", { name: "清除" }));
	await user.click(screen.getByRole("button", { name: "确认清除" }));
	expect(await screen.findByText("未配置")).toBeInTheDocument();
	expect(screen.getByRole("switch", { name: "启用通知调度" })).not.toBeChecked();
	expect(screen.getByLabelText("Webhook 地址")).toHaveValue("");
  });
});
