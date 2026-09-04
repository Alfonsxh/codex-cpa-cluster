import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { CustomUsageRangeModal } from "./CustomUsageRangeModal";

const localEpoch = (year: number, month: number, day: number, hour = 0, minute = 0) => (
  Math.floor(new Date(year, month, day, hour, minute).getTime() / 1000)
);

describe("CustomUsageRangeModal visual contract", () => {
  it("uses one range picker for consecutive start/end selection and cancels without applying", async () => {
    const onCancel = vi.fn();
    const onApply = vi.fn();
    const user = userEvent.setup();
    render(
      <CustomUsageRangeModal
        open
        title="时间选择"
        range={{ startAt: localEpoch(2026, 7, 20), endAt: localEpoch(2026, 7, 21) }}
        onCancel={onCancel}
        onApply={onApply}
      />
    );

    const dialog = screen.getByRole("dialog", { name: "时间选择" });
    const inputs = within(dialog).getAllByLabelText("时间范围");
    expect(inputs).toHaveLength(2);
    expect(inputs[0]).toHaveAttribute("date-range", "start");
    expect(inputs[1]).toHaveAttribute("date-range", "end");
    expect(within(dialog).getByText("2026/08/20 00:00 → 2026/08/21 00:00")).toBeInTheDocument();
    expect(within(dialog).queryByText(/本地时间|包含开始时刻|不包含结束时刻/)).not.toBeInTheDocument();

    await user.click(inputs[0]);
    expect(document.querySelector(".ant-picker-dropdown")).toBeInTheDocument();
    await user.click(within(dialog).getByRole("button", { name: /取\s*消/ }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onApply).not.toHaveBeenCalled();
  });

  it("applies the exact selected epoch-second range", async () => {
    const onApply = vi.fn();
    const user = userEvent.setup();
    const range = {
      startAt: localEpoch(2026, 7, 20, 8),
      endAt: localEpoch(2026, 7, 21, 9)
    };
    render(
      <CustomUsageRangeModal
        open
        title="时间选择"
        range={range}
        onCancel={() => undefined}
        onApply={onApply}
      />
    );

    await user.click(screen.getByRole("button", { name: "应用范围" }));
    expect(onApply).toHaveBeenCalledWith(range);
  });
});
