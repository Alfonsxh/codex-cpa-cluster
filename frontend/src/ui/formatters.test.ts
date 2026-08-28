import { describe, expect, it } from "vitest";

import {
  formatTokenAmount,
  formatTokens,
  tokenInputPresentation,
  tokenReadableText
} from "./formatters";

describe("shared Token formatting", () => {
  it("uses the same K/M/B units on every Admin surface", () => {
    expect(formatTokens(999)).toBe("999 Token");
    expect(formatTokens(1_000)).toBe("1 K");
    expect(formatTokens(2_000_000)).toBe("2 M");
    expect(formatTokens(1_250_000_000)).toBe("1.3 B");
  });

  it("provides a unit-aware amount for labels that already say Token", () => {
    expect(formatTokenAmount(200)).toBe("200");
    expect(formatTokenAmount(12_400)).toBe("12.4 K");
    expect(formatTokenAmount(516_600)).toBe("516.6 K");
    expect(formatTokenAmount(999_960)).toBe("1 M");
    expect(formatTokens(Number.NaN)).toBe("0 Token");
  });

  it("matches the frozen quota formatter including Chinese magnitudes", () => {
    expect(tokenReadableText(20_000_000)).toBe("20 M Token（2,000 万 Token）");
    expect(tokenReadableText(4_058_200)).toBe("4.1 M Token（405.82 万 Token）");
    expect(tokenReadableText(999)).toBe("999 Token");
    expect(tokenReadableText(null)).toBe("—");
  });

  it("preserves empty, invalid and exact-value quota input states", () => {
    expect(tokenInputPresentation("", "请输入自定义周额度")).toEqual({
      state: "empty",
      emptyLabel: "请输入自定义周额度"
    });
    expect(tokenInputPresentation("0")).toEqual({ state: "invalid" });
    expect(tokenInputPresentation("abc")).toEqual({ state: "invalid" });
    expect(tokenInputPresentation("4058200")).toMatchObject({
      state: "ready",
      compact: "4.1 M Token",
      localized: "405.82 万 Token",
      exact: "4,058,200 Token",
      compacted: true
    });
  });
});
