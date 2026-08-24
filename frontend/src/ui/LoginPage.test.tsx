import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { sessionQueryKey } from "../api/session";
import { LoginPage } from "./LoginPage";

describe("LoginPage", () => {
  it("clears the key after creating an in-memory session", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      authenticated: true,
      csrf_token: "csrf-test"
    }), { status: 201, headers: { "Content-Type": "application/json" } })));
    const queryClient = new QueryClient({ defaultOptions: { mutations: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <LoginPage />
      </QueryClientProvider>
    );

    const input = screen.getByLabelText("管理密钥");
    expect(screen.queryByRole("button", { name: "切换为深色主题" })).not.toBeInTheDocument();
    await user.type(input, "test-management-key");
    await user.click(screen.getByRole("button", { name: "验证并进入" }));

    await waitFor(() => expect(queryClient.getQueryData(sessionQueryKey)).toEqual({
      authenticated: true,
      csrf_token: "csrf-test"
    }));
    expect(input).toHaveValue("");
  });
});
