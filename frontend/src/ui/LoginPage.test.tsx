import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import { sessionQueryKey } from "../api/session";
import { LoginPage } from "./LoginPage";

afterEach(() => {
  vi.unstubAllGlobals();
  vi.unstubAllEnvs();
});

function renderLogin(properties: React.ComponentProps<typeof LoginPage> = {}) {
  const queryClient = new QueryClient({
    defaultOptions: {
      mutations: { retry: false },
      queries: { retry: false }
    }
  });
  const user = userEvent.setup();
  render(
    <QueryClientProvider client={queryClient}>
      <LoginPage {...properties} />
    </QueryClientProvider>
  );
  return { queryClient, user };
}

describe("LoginPage", () => {
  it.each([true, false])("uses the correct usage entry in development=%s", (development) => {
    vi.stubEnv("DEV", development);
    vi.stubEnv("VITE_DEV_USAGE_ORIGIN", "http://127.0.0.1:5194");
    renderLogin();
    expect(screen.getByRole("link", { name: /进入使用中心/ })).toHaveAttribute(
      "href", development ? "http://127.0.0.1:5194/usage/" : "/usage/"
    );
  });

  it("matches the legacy password visibility state and clears the key after success", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      authenticated: true,
      csrf_token: "csrf-test"
    }), { status: 201, headers: { "Content-Type": "application/json" } })));
    const onAuthenticated = vi.fn();
    const { queryClient, user } = renderLogin({ onAuthenticated });

    const input = screen.getByLabelText("管理密钥");
    const visibility = screen.getByRole("button", { name: "显示密码" });
    expect(screen.queryByRole("button", { name: "切换为深色主题" })).not.toBeInTheDocument();
    expect(input).toHaveAttribute("type", "password");
    expect(visibility).toHaveAttribute("aria-pressed", "false");
    expect(visibility).toHaveAttribute("tabindex", "-1");
    input.focus();
    await user.tab();
    expect(screen.getByRole("button", { name: "验证并进入" })).toHaveFocus();
    input.focus();
    await user.type(input, "test-management-key");
    await user.click(visibility);
    expect(input).toHaveAttribute("type", "text");
    expect(screen.getByRole("button", { name: "隐藏密码" })).toHaveAttribute("aria-pressed", "true");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));

    await waitFor(() => expect(queryClient.getQueryData(sessionQueryKey)).toEqual({
      authenticated: true,
      csrf_token: "csrf-test"
    }));
    expect(onAuthenticated).toHaveBeenCalledOnce();
    expect(input).toHaveValue("");
    expect(input).toHaveAttribute("type", "password");
  });

  it("submits with Enter and keeps the legacy button label while preventing duplicates", async () => {
    let resolveResponse!: (response: Response) => void;
    const response = new Promise<Response>((resolve) => { resolveResponse = resolve; });
    const fetchMock = vi.fn().mockReturnValue(response);
    vi.stubGlobal("fetch", fetchMock);
    const { user } = renderLogin();

    const input = screen.getByLabelText("管理密钥");
    await user.type(input, "pending-key{Enter}");
    const submit = screen.getByRole("button", { name: "验证并进入" });
    await waitFor(() => expect(submit).toBeDisabled());
    expect(submit).toHaveTextContent("验证并进入");
    await user.keyboard("{Enter}{Enter}");
    expect(fetchMock).toHaveBeenCalledOnce();

    resolveResponse(new Response(JSON.stringify({ authenticated: true, csrf_token: "csrf-test" }), {
      status: 201,
      headers: { "Content-Type": "application/json" }
    }));
    await waitFor(() => expect(input).toHaveValue(""));
  });

  it.each([
    [401, "invalid_management_key", "管理密钥无效", ""],
    [429, "rate_limited", "验证过于频繁，请稍后重试", "rejected-key"],
    [500, "internal_error", "管理服务暂时不可用", "rejected-key"]
  ])("renders HTTP %s in the fixed legacy error region", async (status, code, message, retainedValue) => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code, message }
    }), { status, headers: { "Content-Type": "application/json" } })));
    const { user } = renderLogin({ notice: "管理会话已失效，请重新输入管理密钥" });

    const input = screen.getByLabelText("管理密钥");
    await user.type(input, "rejected-key");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));

    const error = await screen.findByRole("alert");
    expect(error).toHaveClass("form-error");
    expect(error).toHaveTextContent(message);
    expect(input).toHaveValue(retainedValue);
    if (status === 401) expect(input).toHaveAttribute("type", "password");
  });
});
