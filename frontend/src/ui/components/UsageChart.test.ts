import { describe, expect, it } from "vitest";

import { renderUsageTooltip, tooltipRows } from "./UsageChart";

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

  it("moves aggregate metrics into the shared hover card", () => {
    const html = renderUsageTooltip({
      seriesName: "全部账号合计",
      seriesIndex: 0,
      dataIndex: 0,
      value: 42,
      color: "#ffffff"
    } as never, [1_799_996_400], "未加权", "Asia/Shanghai", {
      current: 42,
      total: 100,
      average: 25,
      maximum: 50
    });

    expect(html).toContain('data-summary="true"');
    expect(html).toContain('class="overview-chart-mode-tag unweighted"');
    expect(html).toContain("悬停时段");
    expect(html).not.toContain("全部账号合计");
    expect(html).toContain("当前值");
    expect(html).toContain("范围内总量");
    expect(html).toContain("平均值");
    expect(html).toContain("最大值");
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
