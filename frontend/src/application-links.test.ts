import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { applicationHref } from "./application-links";

beforeEach(() => {
  vi.stubEnv("DEV", true);
  vi.stubEnv("VITE_DEV_ADMIN_ORIGIN", "");
  vi.stubEnv("VITE_DEV_USAGE_ORIGIN", "");
  vi.stubEnv("VITE_DEV_PORTAL_ORIGIN", "");
  vi.stubGlobal("window", { location: new URL("http://127.0.0.1:5173/admin/") });
});

afterEach(() => {
  vi.unstubAllEnvs();
  vi.unstubAllGlobals();
});

describe("applicationHref", () => {
  it("keeps production links relative even with invalid development overrides", () => {
    vi.stubEnv("DEV", false);
    vi.stubEnv("VITE_DEV_ADMIN_ORIGIN", "not-a-url");
    vi.stubEnv("VITE_DEV_USAGE_ORIGIN", "http://localhost:5194");
    vi.stubEnv("VITE_DEV_PORTAL_ORIGIN", "http://localhost:5192");

    expect(applicationHref("admin")).toBe("/admin/");
    expect(applicationHref("usage")).toBe("/usage/");
    expect(applicationHref("portal")).toBe("/");
    expect(applicationHref("admin", "?action=add-account")).toBe("/admin/?action=add-account");
  });

  it("uses each application's default development port", () => {
    expect(applicationHref("admin")).toBe("http://127.0.0.1:5173/admin/");
    expect(applicationHref("usage")).toBe("http://127.0.0.1:5174/usage/");
    expect(applicationHref("portal")).toBe("http://127.0.0.1:5175/");
  });

  it("preserves localhost to avoid switching cookie hosts", () => {
    vi.stubGlobal("window", { location: new URL("http://localhost:5174/usage/") });
    expect(applicationHref("admin")).toBe("http://localhost:5173/admin/");
    expect(applicationHref("portal")).toBe("http://localhost:5175/");
  });

  it("uses loopback for defaults instead of forwarding to an arbitrary host", () => {
    vi.stubGlobal("window", { location: new URL("https://cpa.example.com/admin/") });
    expect(applicationHref("usage")).toBe("http://127.0.0.1:5174/usage/");
  });

  it("honors isolated origins and preserves query strings and fragments", () => {
    vi.stubEnv("VITE_DEV_ADMIN_ORIGIN", "http://127.0.0.1:5193/");
    vi.stubEnv("VITE_DEV_USAGE_ORIGIN", "http://127.0.0.1:5194");
    vi.stubEnv("VITE_DEV_PORTAL_ORIGIN", "https://localhost:5192");
    expect(applicationHref("admin", "?action=add-account&name=a%2Bb#form"))
      .toBe("http://127.0.0.1:5193/admin/?action=add-account&name=a%2Bb#form");
    expect(applicationHref("usage")).toBe("http://127.0.0.1:5194/usage/");
    expect(applicationHref("portal")).toBe("https://localhost:5192/");
  });

  it.each([
    "not-a-url", "javascript:alert(1)", "//localhost:5194", "http://user:secret@localhost:5194",
    "http://localhost:5194/usage/", "http://localhost:5194?test=1", "http://localhost:5194#test"
  ])("rejects malformed or non-origin development configuration: %s", (origin) => {
    vi.stubEnv("VITE_DEV_USAGE_ORIGIN", origin);
    expect(() => applicationHref("usage")).toThrow("VITE_DEV_USAGE_ORIGIN must be an HTTP(S) origin");
  });
});
