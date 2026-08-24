import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { OverviewPage } from "./OverviewPage";

describe("OverviewPage legacy dashboard contract", () => {
  it("uses fine-grained APIs while restoring the legacy metrics, monitor, tables, and activity layout", async () => {
    const summary = {
      generated_at: 1_800_000_000,
      source: "control-plane",
      summary: {
        accounts: 3,
        enabled_accounts: 2,
        users: 8,
        active_users: 7,
        active_keys: 7,
        routed_users: 6,
        unassigned_users: 2,
        teams: 3,
        incomplete_key_matrices: 1
      }
    };
    const usage = {
      generated_at: 1_800_000_000,
      window: "today",
      window_seconds: null,
      window_start_at: 1_799_996_400,
      window_start_at_by_account: null,
      unavailable_accounts: [],
      bucket_seconds: 900,
      buckets: [1_799_996_400, 1_799_997_300],
      accounts: Array.from({ length: 15 }, (_, index) => index === 0
        ? { name: "alpha", values: [100, 200], current: 200, average: 150, maximum: 200, total: 300 }
        : index === 1
          ? { name: "cpa-02", values: [2_000_000, 0], current: 0, average: 1_000_000, maximum: 2_000_000, total: 2_000_000 }
        : { name: `cpa-${String(index + 1).padStart(2, "0")}`, values: [0, 0], current: 0, average: 0, maximum: 0, total: 0 }),
      users: [{ name: "alice@example.com", values: [100, 200], current: 200, average: 150, maximum: 200, total: 300 }],
      selected_account: null,
      selected_user: null,
      selected_accounts: [],
      selected_users: [],
      user_limit: 10,
      collector: {
        status: "ok", heartbeat_at: 1_800_000_000, last_error: "", event_count: 2,
        collection_started_at: 1_799_996_400, usage_breakdown_started_at: 1_799_996_400, last_event_at: 1_800_000_000
      },
      cached: false
    };
    const jobs = {
      jobs: [{ id: "job-1", name: "重启 alpha", action: "restart", target: "alpha", status: "succeeded", created_at: 1_800_000_000 }]
    };
    const fetchMock = vi.fn().mockImplementation((input: string | URL | Request) => {
      const path = String(input);
      if (path.startsWith("/admin/api/overview/usage?")) return Promise.resolve(jsonResponse(usage));
      if (path === "/admin/api/overview/summary") return Promise.resolve(jsonResponse(summary));
      if (path === "/admin/api/runtime/jobs?limit=30") return Promise.resolve(jsonResponse(jobs));
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <OverviewPage />
      </QueryClientProvider>
    );

    expect(await screen.findByText("1 个用户的统一 Key 账号矩阵不完整")).toBeInTheDocument();
    const metrics = screen.getByLabelText("关键指标");
    expect(within(metrics).getAllByRole("article")).toHaveLength(6);
    expect(within(metrics).getByText("有效用户")).toBeInTheDocument();
    expect(within(metrics).getByText("启用 CPA")).toBeInTheDocument();

    expect(await screen.findByRole("heading", { name: "所有账号 Token 使用量" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "CPA 账号 Token 使用趋势" })).toBeInTheDocument();
    expect(screen.getByRole("heading", { name: "用户 Token 使用趋势" })).toBeInTheDocument();
    expect(screen.getAllByText("alpha")).not.toHaveLength(0);
    expect(screen.getByText("alice@example.com")).toBeInTheDocument();
    expect(screen.getByText("重启 alpha")).toBeInTheDocument();

    await user.click(screen.getByRole("combobox", { name: "用户" }));
    const userOption = await screen.findByTitle("alice@example.com");
    const identityPopup = userOption.closest(".overview-identity-select-popup");
    expect(identityPopup).not.toBeNull();
    expect(identityPopup).toHaveStyle({ width: "420px" });

    expect(fetchMock.mock.calls.some(([path]) => String(path) === "/admin/api/overview/usage?window=today&user_limit=10")).toBe(true);
    expect(fetchMock.mock.calls.some(([path]) => String(path).includes("/release"))).toBe(false);

    expect(await screen.findByRole("img", { name: /所有账号 Token 使用趋势/ })).toBeInTheDocument();
    expect(await screen.findByRole("img", { name: /分项 Token 使用趋势：alpha/ })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "6 小时" }));
    expect(await waitForRequest(fetchMock, "/admin/api/overview/usage?window=21600&user_limit=10")).toBe(true);

    await user.click(screen.getAllByRole("button", { name: "范围内总量 ↓" })[0]);
    expect(screen.getAllByRole("button", { name: "范围内总量 ↑" })).not.toHaveLength(0);
  });
});

async function waitForRequest(fetchMock: ReturnType<typeof vi.fn>, expected: string) {
  for (let attempt = 0; attempt < 20; attempt += 1) {
    if (fetchMock.mock.calls.some(([path]) => String(path) === expected)) return true;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  return false;
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}
