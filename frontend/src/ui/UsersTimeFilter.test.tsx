import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";

import { UsersPage } from "./UsersPage";

const startAt = Date.parse("2026-09-01T00:00:00+08:00") / 1000;
const endAt = startAt + 3600;
const team = { id: "platform", name: "Platform", description: "", tag_style: "rose", user_count: 0, created_at: 1, updated_at: 1 };

function catalog(params: URLSearchParams) {
  return {
    users: [], accounts: {}, teams: [team], tags: [],
    collector: { status: "healthy", heartbeat_at: endAt, last_success_at: endAt, last_error: "", queue_depth: 0 },
    pagination: { page: 1, page_size: 50, total: 0, total_pages: 1 },
    generated_at: endAt, summary_generated_at: endAt, summary_cached: false,
    window: params.get("window"), window_seconds: 3600,
    window_start_at: params.get("window") === "all" ? null : Number(params.get("start_at") || startAt),
    window_end_at: Number(params.get("end_at") || endAt), window_timezone: "Asia/Shanghai"
  };
}

function response(payload: unknown, status = 200) {
  return new Response(JSON.stringify(payload), { status, headers: { "Content-Type": "application/json" } });
}

function setup(override?: (params: URLSearchParams) => Promise<Response> | undefined) {
  const requests: URLSearchParams[] = [];
  vi.stubGlobal("fetch", vi.fn((input: RequestInfo | URL) => {
    const url = new URL(String(input), "http://localhost");
    if (url.pathname === "/admin/api/users") {
      requests.push(url.searchParams);
      return override?.(url.searchParams) ?? Promise.resolve(response(catalog(url.searchParams)));
    }
    if (url.pathname === "/admin/api/teams/usage") {
      return Promise.resolve(response({ teams: [], generated_at: endAt }));
    }
    return Promise.reject(new Error(`Unexpected request: ${url.pathname}`));
  }));
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(
    <MemoryRouter>
      <QueryClientProvider client={queryClient}>
        <UsersPage csrfToken="test" />
      </QueryClientProvider>
    </MemoryRouter>
  );
  return { user: userEvent.setup(), requests };
}

describe("user time range filters", () => {
  it("places the overview-style time range first and preserves search and team filters", async () => {
    const { user, requests } = setup();
    expect(await screen.findByText("2026/09/01 00:00:00")).toBeInTheDocument();
    const ranges = screen.getByRole("group", { name: "用户用量时间范围" });
    expect(ranges.closest(".user-time-filter-toolbar")?.firstElementChild).toBe(ranges.closest(".user-time-filter"));
    expect(ranges).toHaveClass("overview-legacy-window-segments");
    expect(screen.queryByText("统计范围")).not.toBeInTheDocument();
    expect(within(ranges).getByRole("button", { name: "今日" })).toHaveAttribute("aria-pressed", "true");

    await user.type(screen.getByRole("searchbox", { name: "搜索用户" }), "alice{Enter}");
    await user.click(screen.getByRole("button", { name: "团队：全部团队" }));
    await user.click(screen.getByRole("option", { name: "Platform" }));
    await user.click(within(ranges).getByRole("button", { name: "7 天" }));
    await waitFor(() => expect(requests.at(-1)?.get("window")).toBe("604800"));
    expect(requests.at(-1)?.get("q")).toBe("alice");
    expect(requests.at(-1)?.get("team_id")).toBe("platform");
    expect(requests.at(-1)?.get("page")).toBe("1");
    expect(within(ranges).getByRole("button", { name: "7 天" })).toHaveAttribute("aria-pressed", "true");

    await user.click(within(ranges).getByRole("button", { name: "全部" }));
    expect(await screen.findByText("不限")).toBeInTheDocument();
  });

  it("uses the shared custom picker without applying cancelled selections", async () => {
    const { user, requests } = setup();
    expect(await screen.findByText("2026/09/01 00:00:00")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "时间选择" }));
    const dialog = await screen.findByRole("dialog", { name: "选择时间范围" });
    expect(dialog).toHaveClass("custom-usage-range-modal");
    await user.click(within(dialog).getByRole("button", { name: /取\s*消/ }));
    expect(requests.every((params) => params.get("window") === "today")).toBe(true);

    await user.click(screen.getByRole("button", { name: "时间选择" }));
    await user.click(within(screen.getByRole("dialog")).getByRole("button", { name: "应用范围" }));
    await waitFor(() => expect(requests.at(-1)?.get("window")).toBe("custom"));
    expect(Number(requests.at(-1)?.get("end_at"))).toBeGreaterThan(Number(requests.at(-1)?.get("start_at")));
    expect(screen.getByRole("button", { name: "时间选择" })).toHaveAttribute("aria-pressed", "true");
  });

  it("replaces stale boundaries with ellipses during range changes and dashes on failure", async () => {
    let rejectRange: (() => void) | undefined;
    const pending = new Promise<Response>((resolve) => {
      rejectRange = () => resolve(response({ error: "test range failure" }, 500));
    });
    const { user } = setup((params) => params.get("window") === "604800" ? pending : undefined);
    expect(await screen.findByText("2026/09/01 00:00:00")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "7 天" }));
    const boundaries = screen.getByLabelText("用户用量时间边界");
    expect(boundaries).toHaveAttribute("aria-busy", "true");
    expect(within(boundaries).getAllByText("…")).toHaveLength(2);
    rejectRange?.();
    await waitFor(() => expect(boundaries).toHaveAttribute("aria-busy", "false"));
    expect(within(boundaries).getAllByText("—")).toHaveLength(2);
  });
});
