import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it } from "vitest";

import { LegacyUsageMultiSelect } from "./LegacyUsageMultiSelect";

const options = [
  { value: "alpha", label: "alpha" },
  { value: "beta", label: "beta-long-account-name" },
  { value: "gamma", label: "gamma" }
];

function Harness() {
  const [accounts, setAccounts] = useState<string[]>([]);
  const [users, setUsers] = useState<string[]>([]);
  return (
    <>
      <LegacyUsageMultiSelect id="accounts" label="CPA" allLabel="全部 CPA" searchPlaceholder="搜索 CPA" options={options} value={accounts} onChange={setAccounts} />
      <LegacyUsageMultiSelect id="users" label="用户" allLabel="全部用户" searchPlaceholder="搜索用户邮箱" options={options} value={users} onChange={setUsers} />
      <button type="button">外部按钮</button>
    </>
  );
}

describe("LegacyUsageMultiSelect", () => {
  it("matches the frozen open, search, multi-select, clear and outside-close flow", async () => {
    const user = userEvent.setup();
    render(<Harness />);

    const accountTrigger = screen.getByRole("button", { name: "全部 CPA" });
    await user.click(accountTrigger);
    expect(accountTrigger).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByPlaceholderText("搜索 CPA")).toHaveFocus();

    await user.type(screen.getByPlaceholderText("搜索 CPA"), "long");
    const accountMenu = document.querySelector("#accounts-menu") as HTMLElement;
    expect(within(accountMenu).queryByText("alpha")).not.toBeInTheDocument();
    await user.click(within(accountMenu).getByTitle("beta-long-account-name"));
    expect(accountTrigger).toHaveTextContent("beta");
    expect(accountTrigger).toHaveAttribute("aria-expanded", "true");

    await user.clear(screen.getByPlaceholderText("搜索 CPA"));
    await user.click(within(accountMenu).getByTitle("alpha"));
    expect(accountTrigger).toHaveTextContent("beta、alpha");
    await user.click(within(accountMenu).getByTitle("gamma"));
    expect(accountTrigger).toHaveTextContent("3 个已选");

    await user.click(within(accountMenu).getByText("全部 CPA"));
    expect(accountTrigger).toHaveTextContent("全部 CPA");
    await user.click(screen.getByRole("button", { name: "外部按钮" }));
    expect(accountTrigger).toHaveAttribute("aria-expanded", "false");
  });

  it("closes the other variable and restores trigger focus on Escape", async () => {
    const user = userEvent.setup();
    render(<Harness />);
    const accounts = screen.getByRole("button", { name: "全部 CPA" });
    const users = screen.getByRole("button", { name: "全部用户" });
    await user.click(accounts);
    await user.click(users);
    expect(accounts).toHaveAttribute("aria-expanded", "false");
    expect(users).toHaveAttribute("aria-expanded", "true");
    await user.keyboard("{Escape}");
    expect(users).toHaveAttribute("aria-expanded", "false");
    expect(users).toHaveFocus();
  });
});
