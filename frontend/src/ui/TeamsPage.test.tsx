import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { TeamsPage } from "./TeamsPage";

describe("TeamsPage", () => {
  it("renders a recoverable empty state and opens the create workflow", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ teams: [] }), {
      status: 200,
      headers: { "Content-Type": "application/json" }
    })));
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const user = userEvent.setup();
    render(
      <QueryClientProvider client={queryClient}>
        <TeamsPage csrfToken="csrf-test" />
      </QueryClientProvider>
    );

    expect(await screen.findByText("还没有团队")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "新建第一个团队" }));
    expect(screen.getByRole("dialog", { name: "新建团队" })).toBeInTheDocument();
    expect(screen.getByLabelText("团队名称")).toHaveFocus();
  });
});
