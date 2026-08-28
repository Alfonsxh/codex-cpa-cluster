import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { CustomUsageRangeModal } from "./CustomUsageRangeModal";

const localEpoch = (year: number, month: number, day: number, hour = 0, minute = 0) => (
  Math.floor(new Date(year, month, day, hour, minute).getTime() / 1000)
);

describe("CustomUsageRangeModal visual contract", () => {
  it("renders the two-calendar inclusive/exclusive contract and cancels without applying", async () => {
    const onCancel = vi.fn();
    const onApply = vi.fn();
    const user = userEvent.setup();
    render(
      <CustomUsageRangeModal
        open
        title="账号信息自定义统计范围"
        range={{ startAt: localEpoch(2026, 7, 20), endAt: localEpoch(2026, 7, 21) }}
        onCancel={onCancel}
        onApply={onApply}
      />
    );

    const dialog = screen.getByRole("dialog", { name: /账号信息自定义统计范围/ });
    expect(within(dialog).getByText("账号信息自定义统计范围")).toBeInTheDocument();
    expect(within(dialog).getByText("CUSTOM USAGE RANGE")).toBeInTheDocument();
    expect(within(dialog).getByText("包含")).toBeInTheDocument();
    expect(within(dialog).getByText("不包含")).toBeInTheDocument();
    expect(within(dialog).getByText(/查询包含开始时刻，不包含结束时刻。/)).toBeInTheDocument();
    expect(within(dialog).getByText("2026/08/20 00:00 → 2026/08/21 00:00")).toBeInTheDocument();
    expect(within(dialog).getAllByText(/[日一二三四五六]/)).not.toHaveLength(0);

    await user.click(within(dialog).getByRole("button", { name: /取\s*消/ }));
    expect(onCancel).toHaveBeenCalledTimes(1);
    expect(onApply).not.toHaveBeenCalled();
  });

  it("keeps invalid input open and emits exact start-inclusive/end-exclusive epoch seconds after correction", async () => {
    const onApply = vi.fn();
    render(
      <CustomUsageRangeModal
        open
        title="Token 趋势自定义统计范围"
        range={{ startAt: localEpoch(2026, 7, 20), endAt: localEpoch(2026, 7, 21) }}
        onCancel={() => undefined}
        onApply={onApply}
      />
    );

    const dialog = screen.getByRole("dialog", { name: /Token 趋势自定义统计范围/ });
    fireEvent.change(within(dialog).getByLabelText("开始时间时间"), { target: { value: "bad" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "应用范围" }));
    expect(within(dialog).getByRole("alert")).toHaveTextContent("请选择有效的开始和结束时间");
    expect(onApply).not.toHaveBeenCalled();

    fireEvent.change(within(dialog).getByLabelText("开始时间时间"), { target: { value: "08:00" } });
    fireEvent.change(within(dialog).getByLabelText("结束时间时间"), { target: { value: "09:00" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "应用范围" }));
    expect(onApply).toHaveBeenCalledWith({
      startAt: localEpoch(2026, 7, 20, 8),
      endAt: localEpoch(2026, 7, 21, 9)
    });
  });
});
