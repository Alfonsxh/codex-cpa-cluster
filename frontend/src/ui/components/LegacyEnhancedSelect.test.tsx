import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { useState } from "react";
import { describe, expect, it, vi } from "vitest";

import { LegacyEnhancedSelect } from "./LegacyEnhancedSelect";

const teamOptions = [
  { value: "", label: "全部团队" },
  { value: "platform", label: "平台研发" },
  { value: "ops", label: "运维" }
];

function SelectHarness() {
  const [team, setTeam] = useState("");
  const [window, setWindow] = useState("today");
  return (
    <div>
      <LegacyEnhancedSelect id="team" label="团队" value={team} options={teamOptions} onChange={setTeam} />
      <LegacyEnhancedSelect
        id="window"
        label="统计范围"
        value={window}
        options={[{ value: "today", label: "今日" }, { value: "custom", label: "自定义…" }]}
        onChange={setWindow}
      />
      <button type="button">外部按钮</button>
    </div>
  );
}

describe("LegacyEnhancedSelect", () => {
  it("matches the legacy click, selection, close and focus flow", async () => {
    const user = userEvent.setup();
    render(<SelectHarness />);

    const trigger = screen.getByRole("button", { name: "团队：全部团队" });
    vi.spyOn(trigger, "getBoundingClientRect").mockReturnValue({
      x: 120,
      y: 700,
      top: 700,
      right: 320,
      bottom: 740,
      left: 120,
      width: 200,
      height: 40,
      toJSON: () => undefined
    });
    await user.click(trigger);
    expect(trigger).toHaveAttribute("aria-expanded", "true");
    const menu = screen.getByRole("listbox", { name: "团队" });
    expect(menu.parentElement).toBe(document.body);
    expect(menu).toHaveClass("enhanced-select-menu-portal");
    expect(menu.style.bottom).not.toBe("");
    expect(menu.style.width).toBe("200px");
    expect(screen.getByRole("option", { name: "全部团队" })).toHaveFocus();

    await user.keyboard("{ArrowDown}{Enter}");
    expect(screen.getByRole("button", { name: "团队：平台研发" })).toHaveFocus();
    expect(screen.getByRole("button", { name: "团队：平台研发" })).toHaveAttribute("aria-expanded", "false");
    expect(document.querySelector<HTMLSelectElement>("#team")?.value).toBe("platform");
  });

  it("supports legacy navigation, Escape, outside click and closes other selects", async () => {
    const user = userEvent.setup();
    render(<SelectHarness />);

    const teamTrigger = screen.getByRole("button", { name: "团队：全部团队" });
    const windowTrigger = screen.getByRole("button", { name: "统计范围：今日" });
    await user.click(teamTrigger);
    await user.keyboard("{End}");
    expect(screen.getByRole("option", { name: "运维" })).toHaveFocus();
    await user.keyboard("{Home}");
    expect(screen.getByRole("option", { name: "全部团队" })).toHaveFocus();
    await user.keyboard("{Escape}");
    expect(teamTrigger).toHaveFocus();
    expect(teamTrigger).toHaveAttribute("aria-expanded", "false");

    await user.click(teamTrigger);
    await user.click(windowTrigger);
    expect(teamTrigger).toHaveAttribute("aria-expanded", "false");
    expect(windowTrigger).toHaveAttribute("aria-expanded", "true");
    await user.click(screen.getByRole("button", { name: "外部按钮" }));
    expect(windowTrigger).toHaveAttribute("aria-expanded", "false");
  });

  it("fires the legacy custom-range callback when the selected custom value is chosen again", async () => {
    const user = userEvent.setup();
    const onChange = vi.fn();
    render(
      <LegacyEnhancedSelect
        id="custom"
        label="统计范围"
        value="custom"
        options={[{ value: "today", label: "今日" }, { value: "custom", label: "自定义…" }]}
        onChange={onChange}
      />
    );

    await user.click(screen.getByRole("button", { name: "统计范围：自定义…" }));
    await user.click(screen.getByRole("option", { name: "自定义…" }));
    expect(onChange).toHaveBeenCalledOnce();
    expect(onChange).toHaveBeenCalledWith("custom");
  });
});
