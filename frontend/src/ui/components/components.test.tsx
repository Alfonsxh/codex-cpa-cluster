import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { AdminTable } from "./AdminTable";
import { PageState } from "./PageState";
import { PageToolbar } from "./PageToolbar";
import { TokenValue } from "./TokenValue";
import { WideSelect } from "./WideSelect";

describe("shared Admin UI components", () => {
  it("keeps toolbar actions and descriptions in one reusable layout", () => {
    render(<PageToolbar description="只加载当前页面数据" actions={<button type="button">刷新</button>} />);

    expect(screen.getByText("只加载当前页面数据")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "刷新" })).toBeInTheDocument();
  });

  it("renders the shared empty table state without page-owned conditionals", () => {
    render(
      <AdminTable<{ id: string }>
        rowKey="id"
        columns={[{ title: "编号", dataIndex: "id" }]}
        dataSource={[]}
        emptyText="还没有记录"
      />
    );

    expect(screen.getByText("还没有记录")).toBeInTheDocument();
  });

  it("shows compact Token units while preserving the exact value as a tooltip", () => {
    render(<TokenValue value={1_250_000} />);

    expect(screen.getByText("1.3 M Token")).toHaveAttribute("title", "1,250,000 Token");
  });

  it("applies the shared long-option popup and keeps selection behavior", async () => {
    const onChange = vi.fn();
    const user = userEvent.setup();
    render(
      <WideSelect
        aria-label="业务账号"
        value="alpha"
        options={[
          { value: "alpha", label: "alpha · alpha@example.com" },
          { value: "beta", label: "beta · beta@example.com" }
        ]}
        onChange={onChange}
      />
    );

    await user.click(screen.getByLabelText("业务账号"));
    expect(document.querySelector(".admin-wide-select-popup")).toBeInTheDocument();
    await user.click(screen.getByText("beta · beta@example.com"));
    expect(onChange).toHaveBeenCalledWith("beta", expect.anything());
  });

  it("provides one recoverable error state", async () => {
    const retry = vi.fn();
    const user = userEvent.setup();
    render(<PageState kind="error" title="加载失败" detail="网络不可用" onAction={retry} />);

    await user.click(screen.getByRole("button", { name: "重新加载" }));
    expect(retry).toHaveBeenCalledTimes(1);
  });
});
