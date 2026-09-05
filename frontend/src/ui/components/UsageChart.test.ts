import { describe, expect, it } from "vitest";

import { renderUsageTooltip, summarizeUsageChart, tooltipRows, usageChartPeak } from "./UsageChart";

describe("UsageChart peak marker", () => {
  it("finds the selected plot's peak and consistently uses the first tied point", () => {
    expect(usageChartPeak([3, 1, 2])).toEqual({ index: 0, value: 3 });
    expect(usageChartPeak([1, 2, 3])).toEqual({ index: 2, value: 3 });
    expect(usageChartPeak([1, 6, 6])).toEqual({ index: 1, value: 6 });
    expect(usageChartPeak([0, 0])).toEqual({ index: 0, value: 0 });
  });

  it("does not invent a peak for an empty or invalid series", () => {
    expect(usageChartPeak([])).toBeNull();
    expect(usageChartPeak([NaN, Infinity])).toBeNull();
  });
});

describe("UsageChart summary uses the plotted Token basis", () => {
  it("aggregates raw and weighted plots independently, including zero buckets", () => {
    const buckets = [1, 2, 3];
    expect(summarizeUsageChart(buckets, [{ values: [1_000_000, 3_000_000, 0] }, { values: [200, 400, 0] }])).toEqual({
      values: [1_000_200, 3_000_400, 0], current: 0, total: 4_000_600, average: 1_333_533, maximum: 3_000_400
    });
    expect(summarizeUsageChart(buckets, [{ values: [2_000_000, 6_000_000, 0] }, { values: [600, 1_200, 0] }])).toEqual({
      values: [2_000_600, 6_001_200, 0], current: 0, total: 8_001_800, average: 2_667_267, maximum: 6_001_200
    });
  });

  it("ignores summary metadata and values outside the displayed buckets", () => {
    const series = { values: [100, 300, 999], current: 999, total: 999, weighted_total: 999 };
    expect(summarizeUsageChart([1, 2], [series])).toEqual({
      values: [100, 300], current: 300, total: 400, average: 200, maximum: 300
    });
    expect(summarizeUsageChart([], [series])).toEqual({ values: [], current: 0, total: 0, average: 0, maximum: 0 });
  });
});

describe("UsageChart tooltip contract", () => {
  it("sorts by the hovered Token value and keeps one Top 10 column", () => {
    const parameters = Array.from({ length: 15 }, (_, index) => ({
      seriesName: `cpa-${String(index + 1).padStart(2, "0")}`,
      seriesIndex: index,
      dataIndex: 1,
      value: index === 1 ? 2_000_000 : index,
      color: `#6374${String(index).padStart(2, "0")}`
    }));

    const rows = tooltipRows(parameters as never);
    expect(rows).toHaveLength(10);
    expect(rows[0]).toMatchObject({ name: "cpa-02", value: 2_000_000, color: "#4b8ccf" });
    expect(rows.some((row) => row.name === "cpa-01")).toBe(false);

    const html = renderUsageTooltip(parameters as never, [1_799_996_400, 1_799_997_300], "加权");
    expect(html).toContain('data-layout="single-column"');
    expect(html.match(/<span><i/g)).toHaveLength(10);
    expect(html).toContain("2 M");
    expect(html).toContain('class="overview-chart-mode-tag weighted"');
    expect(html).not.toContain("overflow");
  });

  it("keeps per-account hover cards limited to the hovered time and value", () => {
    const html = renderUsageTooltip({
      seriesName: "cpa-01",
      seriesIndex: 0,
      dataIndex: 0,
      value: 42,
      color: "#ffffff"
    } as never, [1_799_996_400], "未加权", "Asia/Shanghai");

    expect(html).toContain('class="overview-chart-mode-tag unweighted"');
    expect(html).toContain("cpa-01");
    expect(html).toContain("42 Token");
    expect(html).not.toContain("当前值");
    expect(html).not.toContain("范围内总量");
  });

  it("escapes series names before rendering tooltip HTML", () => {
    const html = renderUsageTooltip({
      seriesName: '<img src=x onerror="alert(1)">',
      seriesIndex: 0,
      dataIndex: 0,
      value: 42,
      color: "#6374d8"
    } as never, [1_799_996_400]);

    expect(html).toContain("&lt;img src=x onerror=&quot;alert(1)&quot;&gt;");
    expect(html).not.toContain("<img");
  });
});
