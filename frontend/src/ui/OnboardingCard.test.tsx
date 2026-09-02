import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it } from "vitest";

import type { OnboardingStatus } from "../api/onboarding";
import { OnboardingCard } from "./OnboardingCard";

describe("OnboardingCard", () => {
  it("does not render after required setup is complete even when optional settings remain", () => {
    const { container } = renderCard(status({
      required_complete: true,
      required: { complete: 2, total: 2 },
      recommended: { complete: 3, skipped: 0, total: 6 }
    }));

    expect(container).toBeEmptyDOMElement();
    expect(screen.queryByText("继续完善系统配置")).not.toBeInTheDocument();
  });

  it("only reports required setup progress while required setup is incomplete", () => {
    renderCard(status({
      required_complete: false,
      required: { complete: 1, total: 2 },
      recommended: { complete: 3, skipped: 0, total: 6 }
    }));

    expect(screen.getByRole("heading", { name: "完成基础配置" })).toBeInTheDocument();
    expect(screen.getByText("1/2 项基础配置已完成。")).toBeInTheDocument();
    expect(screen.getByText("配置进度 50%")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "继续设置" })).toBeInTheDocument();
  });
});

function renderCard(value: OnboardingStatus) {
  return render(
    <MemoryRouter>
      <OnboardingCard status={value} />
    </MemoryRouter>
  );
}

function status(overrides: Partial<OnboardingStatus>): OnboardingStatus {
  return {
    version: 1,
    generated_at: 1_800_000_000,
    required_complete: false,
    required: { complete: 0, total: 2 },
    recommended: { complete: 0, skipped: 0, total: 6 },
    skipped_recommended: [],
    steps: [],
    ...overrides
  };
}
