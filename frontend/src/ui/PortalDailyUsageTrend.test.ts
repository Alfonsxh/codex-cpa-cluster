import { describe, expect, it } from "vitest";

import type { PortalUsageTrend } from "../api/portal";
import {
  buildPortalTrendSeries,
  renderPortalTrendTooltip,
  summarizePortalTrend
} from "./PortalDailyUsageTrend";

describe("PortalDailyUsageTrend", () => {
  it("keeps uncollected days disconnected and summarizes weighted versus raw totals", () => {
    const trend = fixtureTrend("total", [
      day("2026-08-27", "uncollected", 0, 0),
      day("2026-08-28", "partial", 100, 125),
      day("2026-08-29", "complete", 200, 250)
    ]);
    const series = buildPortalTrendSeries(trend, "total");
    expect(series).toEqual([
      { name: "加权 Token", values: [null, 125, 250] },
      { name: "未加权 Token", values: [null, 100, 200] }
    ]);
    expect(summarizePortalTrend(trend, "total").items.map((item) => item.value)).toEqual([
      "375 Token", "13 Token", "250 Token"
    ]);
  });

  it("caps the combined Tooltip surface at ten visible rows and folds the remainder into other", () => {
    const combinations = Array.from({ length: 12 }, (_, index) => ({
      model: index === 11 ? "gpt-5.6-sol" : `model-${String(index + 1).padStart(2, "0")}`,
      reasoning_effort: index === 11 ? "xhigh" : index % 2 ? "medium" : "high",
      request_count: 1,
      total_tokens: (index + 1) * 100,
      weighted_tokens: (index + 1) * 125
    }));
    const trend = fixtureTrend("model_reasoning", [{
      ...day("2026-08-29", "complete", 7_800, 9_750), combinations
    }]);
    const series = buildPortalTrendSeries(trend, "model_reasoning");
    expect(series).toHaveLength(10);
    expect(series.at(-1)?.name).toBe("其他组合");
    expect(series[0]?.name).toBe("gpt-5.6-sol · xhigh");
    expect(summarizePortalTrend(trend, "model_reasoning").items[1]?.value).toBe("gpt-5.6-sol · xhigh");
    expect(summarizePortalTrend(trend, "model_reasoning").items[2]?.value).toBe("12");

    const tooltip = renderPortalTrendTooltip(series.map((item, index) => ({
      seriesName: item.name,
      value: item.values[0],
      color: `#00000${index}`,
      dataIndex: 0
    })) as never, trend, "model_reasoning");
    expect(tooltip).toContain('data-layout="single-column"');
    expect(tooltip).toContain("其他组合");
    expect(tooltip).toContain("gpt-5.6-sol · xhigh");
    expect(tooltip).toContain("Token");
    expect(tooltip).toContain("当日合计");
    expect(tooltip).not.toContain("<small>");
    expect((tooltip.match(/<span/g) ?? [])).toHaveLength(11);
  });
});

function fixtureTrend(dimension: "total" | "model_reasoning", days: PortalUsageTrend["days"]): PortalUsageTrend {
  return {
    generated_at: 1,
    window: "30d",
    window_days: 30,
    window_start_at: days[0]?.start_at ?? 0,
    window_end_at: days.at(-1)?.end_at ?? 0,
    window_timezone: "Asia/Shanghai",
    dimension,
    definition: "test",
    collection_started_at: days[0]?.start_at ?? 0,
    effective_start_at: days[0]?.start_at ?? 0,
    days
  };
}

function day(
  date: string,
  collectionState: PortalUsageTrend["days"][number]["collection_state"],
  totalTokens: number,
  weightedTokens: number
): PortalUsageTrend["days"][number] {
  return {
    date,
    start_at: 1,
    end_at: 2,
    collection_state: collectionState,
    request_count: totalTokens > 0 ? 1 : 0,
    total_tokens: totalTokens,
    weighted_tokens: weightedTokens,
    combinations: []
  };
}
