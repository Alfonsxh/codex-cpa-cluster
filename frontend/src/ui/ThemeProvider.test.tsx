import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  LEGACY_THEME_STORAGE_KEY,
  THEME_STORAGE_KEY,
  ThemeProvider,
  ThemeToggle,
  resolveInitialTheme
} from "./ThemeProvider";

afterEach(() => {
  window.localStorage.clear();
  document.documentElement.dataset.theme = "light";
  document.documentElement.style.colorScheme = "";
});

describe("shared CPA theme contract", () => {
  it("prefers the shared key and remains compatible with the legacy Admin key", () => {
    window.localStorage.setItem(LEGACY_THEME_STORAGE_KEY, "dark");
    expect(resolveInitialTheme()).toBe("dark");

    window.localStorage.setItem(THEME_STORAGE_KEY, "light");
    expect(resolveInitialTheme()).toBe("light");
  });

  it("falls back to the operating-system preference when no valid value is stored", () => {
    const previous = window.matchMedia;
    window.matchMedia = vi.fn().mockReturnValue({ matches: true }) as typeof window.matchMedia;
    expect(resolveInitialTheme()).toBe("dark");
    window.matchMedia = previous;
  });

  it("updates data-theme, Ant Design and the shared persisted preference together", async () => {
    window.localStorage.setItem(LEGACY_THEME_STORAGE_KEY, "dark");
    const user = userEvent.setup();
    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>
    );

    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
    expect(screen.getByRole("button", { name: "切换为浅色主题" })).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: "切换为浅色主题" }));
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
    expect(window.localStorage.getItem(THEME_STORAGE_KEY)).toBe("light");
    expect(screen.getByRole("button", { name: "切换为深色主题" })).toBeInTheDocument();
  });
});
