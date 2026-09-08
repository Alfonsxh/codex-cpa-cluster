import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";
import { defaultSiteTimezone, getSiteTimezone, setSiteTimezone, siteDateTimeFormat, useSiteTimezone } from "./site-time";
import { formatServerTimestamp } from "./UsageDashboard";
import { timezoneOptions } from "./components/TimezoneSelect";

afterEach(() => { act(() => setSiteTimezone(defaultSiteTimezone)); });

describe("site timezone", () => {
  it("repaints a mounted surface and formats dates independently of the browser zone", () => {
    const timestamp = Date.parse("2026-09-07T00:30:00Z") / 1000;
    function Clock() {
      useSiteTimezone();
      return <time>{formatServerTimestamp(timestamp)}</time>;
    }
    render(<Clock />);
    expect(screen.getByText("2026/09/07 08:30:00")).toBeInTheDocument();
    act(() => setSiteTimezone("America/Los_Angeles"));
    expect(screen.getByText("2026/09/06 17:30:00")).toBeInTheDocument();
    expect(getSiteTimezone()).toBe("America/Los_Angeles");
  });

  it("honors daylight saving changes and keeps the last valid value on failure", () => {
    setSiteTimezone("America/New_York");
    const formatter = siteDateTimeFormat("en-GB", { hour: "2-digit", minute: "2-digit", hourCycle: "h23" });
    expect(formatter.format(new Date("2026-03-08T06:30:00Z"))).toBe("01:30");
    expect(formatter.format(new Date("2026-03-08T07:30:00Z"))).toBe("03:30");
    expect(() => setSiteTimezone("Unknown/Zone")).toThrow();
    expect(getSiteTimezone()).toBe("America/New_York");
  });

  it("offers IANA choices including UTC and an existing alias", () => {
    const options = timezoneOptions("US/Eastern");
    expect(options.find((option) => option.value === "UTC")?.label).toContain("协调世界时");
    expect(options.find((option) => option.value === "Asia/Shanghai")?.label).toContain("北京时间");
    expect(options.some((option) => option.value === "US/Eastern")).toBe(true);
    expect(options.some((option) => option.value === "America/New_York")).toBe(true);
  });
});
