import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

import { UsageBreakdownDrawer } from "./UsageBreakdownDrawer";

describe("UsageBreakdownDrawer", () => {
  it("does not query until opened and requests only the selected subject", async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      generated_at: 1000,
      window: 86400,
      window_seconds: 86400,
      window_start_at: 0,
      collection_started_at: 100,
      effective_start_at: 100,
      definition: "account_model_reasoning_effort_tokens",
      account: "alpha",
      totals: {
        request_count: 2,
        success_count: 2,
        failed_count: 0,
        input_tokens: 80,
        output_tokens: 30,
        reasoning_tokens: 10,
        cached_tokens: 0,
        total_tokens: 120,
        last_used_at: 900
      },
      models: [],
      combinations: [{
        model: "gpt-5.6-sol",
        reasoning_effort: "high",
        request_count: 2,
        success_count: 2,
        failed_count: 0,
        input_tokens: 80,
        output_tokens: 30,
        reasoning_tokens: 10,
        cached_tokens: 0,
        total_tokens: 120,
        last_used_at: 900
      }]
    }));
    vi.stubGlobal("fetch", fetchMock);
    const queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } }
    });
    const view = render(
      <QueryClientProvider client={queryClient}>
        <UsageBreakdownDrawer kind="account" subject={null} onClose={() => undefined} />
      </QueryClientProvider>
    );

    expect(fetchMock).not.toHaveBeenCalled();
    view.rerender(
      <QueryClientProvider client={queryClient}>
        <UsageBreakdownDrawer kind="account" subject="alpha" onClose={() => undefined} />
      </QueryClientProvider>
    );

    expect(await screen.findByText("gpt-5.6-sol")).toBeInTheDocument();
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    expect(fetchMock.mock.calls[0][0]).toBe(
      "/admin/api/accounts/usage-breakdown?window=86400&account=alpha"
    );
    expect(screen.getByText("按需实时查询")).toBeInTheDocument();
  });
});

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), {
    status: 200,
    headers: { "Content-Type": "application/json" }
  });
}
