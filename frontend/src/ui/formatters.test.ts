import { describe, expect, it } from "vitest";

import { formatTokenAmount, formatTokens } from "./formatters";

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
    expect(formatTokens(Number.NaN)).toBe("0 Token");
  });
});
