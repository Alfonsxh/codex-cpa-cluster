import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { PortalPasswordModal } from "./PortalPasswordModal";

afterEach(() => vi.unstubAllGlobals());

describe("PortalPasswordModal keyboard flow", () => {
  it("跳过密码显隐附属控件，按字段顺序到达主操作并支持 Enter 提交", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      message: "密码已修改",
      password_change_required: false
    }), { status: 200, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);
    const onSuccess = vi.fn();
    const user = userEvent.setup();
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    render(
      <QueryClientProvider client={queryClient}>
        <PortalPasswordModal open mandatory onClose={() => undefined} onSuccess={onSuccess} />
      </QueryClientProvider>
    );

    const current = screen.getByLabelText("当前密码");
    const next = screen.getByLabelText("新密码");
    const confirmation = screen.getByLabelText("确认新密码");
    document.querySelectorAll<HTMLElement>(".ant-input-password-icon").forEach((toggle) => {
      expect(toggle).toHaveAttribute("tabindex", "-1");
      expect(toggle).toHaveAccessibleName();
    });

    current.focus();
    await user.type(current, "current-password");
    await user.tab();
    expect(next).toHaveFocus();
    await user.type(next, "replacement-password");
    await user.tab();
    expect(confirmation).toHaveFocus();
    await user.type(confirmation, "replacement-password");
    await user.tab();
    expect(screen.getByRole("button", { name: "保存新密码" })).toHaveFocus();
    await user.keyboard("{Enter}");

    await waitFor(() => expect(onSuccess).toHaveBeenCalledOnce());
    expect(fetchMock).toHaveBeenCalledWith("/usage/me/password", expect.objectContaining({
      method: "PUT",
      body: JSON.stringify({ current_password: "current-password", new_password: "replacement-password" })
    }));
  });
});
