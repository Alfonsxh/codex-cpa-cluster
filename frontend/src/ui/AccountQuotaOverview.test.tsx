import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes, useLocation } from "react-router-dom";
import { describe, expect, it } from "vitest";

import type { OverviewAccountQuotaSummary } from "../api/overview";
import { AccountQuotaOverview } from "./AccountQuotaOverview";

describe("AccountQuotaOverview", () => {
  it("shows the real aggregate, coverage, risk counts, and accessible progress", () => {
    renderQuota({
      available: true,
      enabled_accounts: 5,
      known_accounts: 4,
      unknown_accounts: 1,
      average_used_percent: 47.47,
      average_remaining_percent: 52.53,
      equivalent_remaining_accounts: 2.1,
      exhausted_accounts: 1,
      high_risk_accounts: 1
    });

    expect(screen.getByText("47.5%")).toBeInTheDocument();
    expect(screen.getByText("已用 47.5%")).toBeInTheDocument();
    expect(screen.getByText("剩余 52.5%")).toBeInTheDocument();
    const metrics = screen.getByLabelText("账号周额度汇总指标");
    expect(within(metrics).getByText("2.1 个账号")).toBeInTheDocument();
    expect(within(metrics).getByText("4 / 5")).toBeInTheDocument();
    expect(within(metrics).getAllByText("1")).toHaveLength(3);
    expect(screen.getByRole("progressbar", { name: "账号平均周额度已用" })).toHaveAttribute("aria-valuenow", "47.47");
    expect(screen.queryByText(/未知账号不参与平均/)).not.toBeInTheDocument();
  });

  it("shows an empty state when no account is enabled", () => {
    renderQuota(emptyQuota());

    expect(screen.getByRole("status")).toHaveTextContent("暂无启用账号");
    expect(screen.queryByRole("progressbar")).not.toBeInTheDocument();
  });

  it("shows an unavailable state when all enabled account quotas are unknown", () => {
    renderQuota({ ...emptyQuota(), enabled_accounts: 3, unknown_accounts: 3 });

    expect(screen.getByRole("status")).toHaveTextContent("额度数据暂不可用");
    expect(screen.getByRole("status")).toHaveTextContent("3 个启用账号");
  });

  it("navigates to the existing account-management route", async () => {
    const user = userEvent.setup();
    renderQuota(emptyQuota());

    await user.click(screen.getByRole("link", { name: "查看账号详情" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/accounts");
  });
});

function renderQuota(quota: OverviewAccountQuotaSummary) {
  render(
    <MemoryRouter initialEntries={["/overview"]}>
      <Routes>
        <Route path="*" element={<><AccountQuotaOverview quota={quota} /><Location /></>} />
      </Routes>
    </MemoryRouter>
  );
}

function Location() {
  return <span data-testid="location">{useLocation().pathname}</span>;
}

function emptyQuota(): OverviewAccountQuotaSummary {
  return {
    available: false,
    enabled_accounts: 0,
    known_accounts: 0,
    unknown_accounts: 0,
    average_used_percent: null,
    average_remaining_percent: null,
    equivalent_remaining_accounts: 0,
    exhausted_accounts: 0,
    high_risk_accounts: 0
  };
}
