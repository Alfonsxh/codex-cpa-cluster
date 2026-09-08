import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import type { Account } from "../../api/accounts";
import { AccountModelTestModal } from "./AccountModelTestModal";

function mount(fetchMock: ReturnType<typeof vi.fn>) {
  vi.stubGlobal("fetch", fetchMock);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><AccountModelTestModal
    account={{ id: "empty", email: "empty@example.com", routed_users: 0 } as Account} csrfToken="fixture-csrf" onClose={() => {}}
  /></QueryClientProvider>);
}
function json(body: unknown, status = 200) { return new Response(JSON.stringify(body), { status }); }

describe("account model communication test", () => {
  it("loads choices without generating, tests an unbound account, and allows retry after upstream auth failure", async () => {
    let posts = 0;
    const fetchMock = vi.fn(async (path: string, init?: RequestInit) => {
      if (path.startsWith("/admin/api/accounts/models?")) return json({ account: "empty", models: ["gpt-5.5", "gpt-6-astra"] });
      expect(path).toBe("/admin/api/accounts/model-test");
      expect(JSON.parse(String(init?.body))).toEqual({ account: "empty", model: "gpt-6-astra" });
      expect(init?.headers).toMatchObject({ "X-CSRF-Token": "fixture-csrf" });
      posts++;
      return json({ account: "empty", model: "gpt-6-astra", success: posts > 1, elapsed_ms: 2500, checked_at: 1788840000,
        upstream_status: posts > 1 ? 200 : 401, code: posts > 1 ? "model_test_passed" : "model_auth_failed", message: posts > 1 ? "模型已完成生成并返回文本" : "账号授权失败，请检查 OAuth 状态" });
    });
    const user = userEvent.setup();
    mount(fetchMock);
    await waitFor(() => expect(screen.getByRole("button", { name: "开始测试" })).toBeEnabled());
    expect(posts).toBe(0);
    await user.click(screen.getByRole("button", { name: "开始测试" }));
    expect(await screen.findByText("模型通信失败")).toBeInTheDocument();
    expect(screen.getByText("401")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "重新测试" }));
    expect(await screen.findByText("模型通信正常")).toBeInTheDocument();
    expect(screen.getByText("2.50 秒")).toBeInTheDocument();
    expect(posts).toBe(2);
  });

  it("does not enable generation when the model list is unavailable", async () => {
    const fetchMock = vi.fn(async () => json({ error: { code: "model_list_unavailable", message: "无法读取此账号的模型列表" } }, 502));
    mount(fetchMock);
    expect(await screen.findByText("模型列表读取失败")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "开始测试" })).toBeDisabled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
