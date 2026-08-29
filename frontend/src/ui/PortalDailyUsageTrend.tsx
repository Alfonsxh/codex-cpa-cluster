import * as echarts from "echarts/core";
import { LineChart, type LineSeriesOption } from "echarts/charts";
import {
  AriaComponent,
  GridComponent,
  TooltipComponent,
  type AriaComponentOption,
  type GridComponentOption,
  type TooltipComponentOption
} from "echarts/components";
import { SVGRenderer } from "echarts/renderers";
import type { ComposeOption, EChartsType } from "echarts/core";
import type { DefaultLabelFormatterCallbackParams as CallbackDataParams } from "echarts";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";

import { ApiError } from "../api/client";
import {
  portalUsageTrendQueryKey,
  readPortalUsageTrend,
  type PortalUsageTrend,
  type PortalUsageTrendDimension,
  type PortalUsageTrendWindow
} from "../api/portal";
import { formatTokens } from "./formatters";
import { usageChartColors } from "./components/UsageChart";
import { useTheme } from "./ThemeProvider";

echarts.use([LineChart, GridComponent, TooltipComponent, AriaComponent, SVGRenderer]);

type DailyTrendOption = ComposeOption<
  LineSeriesOption | GridComponentOption | TooltipComponentOption | AriaComponentOption
>;

type PortalTrendSeries = {
  name: string;
  values: Array<number | null>;
};

const trendWindows: Array<{ value: PortalUsageTrendWindow; label: string }> = [
  { value: "7d", label: "7天" },
  { value: "30d", label: "30天" },
  { value: "90d", label: "90天" }
];
const directCombinationLimit = 9;

export function PortalDailyUsageTrend({ onSessionExpired }: { onSessionExpired: () => void }) {
  const [window, setWindow] = useState<PortalUsageTrendWindow>("30d");
  const [dimension, setDimension] = useState<PortalUsageTrendDimension>("total");
  const [expanded, setExpanded] = useState(true);
  const query = useQuery({
    queryKey: portalUsageTrendQueryKey(window, dimension),
    queryFn: ({ signal }) => readPortalUsageTrend(window, dimension, signal),
    enabled: expanded,
    staleTime: 30_000,
    gcTime: 5 * 60_000,
    retry: false,
    refetchOnWindowFocus: false
  });

  useEffect(() => {
    if (query.error instanceof ApiError && query.error.status === 401) onSessionExpired();
  }, [onSessionExpired, query.error]);

  const summary = useMemo(
    () => summarizePortalTrend(query.data, dimension),
    [dimension, query.data]
  );
  const series = useMemo(
    () => buildPortalTrendSeries(query.data, dimension),
    [dimension, query.data]
  );
  const hasData = Boolean(query.data?.days.some((day) => day.request_count > 0 || day.weighted_tokens > 0 || day.total_tokens > 0));
  const partial = Boolean(query.data?.days.some((day) => day.collection_state !== "complete"));

  return (
    <section className={`usage-trend-card${expanded ? "" : " collapsed"}`} aria-labelledby="usage-trend-title">
      <header className="usage-trend-header">
        <div className="usage-trend-heading">
          <h2 id="usage-trend-title">每日用量趋势</h2>
          <div className="usage-trend-dimensions" role="group" aria-label="趋势统计维度">
            <button type="button" aria-pressed={dimension === "total"} onClick={() => setDimension("total")}>总用量</button>
            <button type="button" aria-pressed={dimension === "model_reasoning"} onClick={() => setDimension("model_reasoning")}>模型 + 推理强度</button>
          </div>
        </div>

        <div className="usage-trend-summary" aria-label="趋势摘要">
          {summary.items.map((item) => (
            <div key={item.label}>
              <span>{item.label}</span>
              <strong title={item.title}>{query.isPending ? "—" : item.value}</strong>
            </div>
          ))}
        </div>

        <div className="usage-trend-actions">
          {expanded ? (
            <div className="usage-trend-windows" role="group" aria-label="每日趋势时间范围">
              {trendWindows.map((option) => (
                <button type="button" key={option.value} aria-pressed={window === option.value} onClick={() => setWindow(option.value)}>{option.label}</button>
              ))}
            </div>
          ) : <strong className="usage-trend-current-window">{trendWindowLabel(window)}</strong>}
          <button className="usage-trend-collapse" type="button" aria-expanded={expanded} aria-controls="usage-trend-body" onClick={() => setExpanded((current) => !current)}>
            {expanded ? "收起" : "展开"}<span aria-hidden="true">{expanded ? "⌃" : "⌄"}</span>
          </button>
        </div>
      </header>

      {expanded ? (
        <div className="usage-trend-body" id="usage-trend-body">
          {query.isPending ? <TrendSkeleton /> : null}
          {query.isError ? (
            <div className="usage-trend-state error" role="alert">
              <span><strong>每日用量趋势加载失败</strong> · {errorMessage(query.error)}</span>
              <button type="button" onClick={() => void query.refetch()}>重试</button>
            </div>
          ) : null}
          {query.data && !hasData ? (
            <div className="usage-trend-state">所选范围暂无已采集用量</div>
          ) : null}
          {query.data && hasData ? (
            <>
              <div className="usage-trend-legend" aria-label="趋势图例">
                {series.map((item, index) => (
                  <span key={item.name}><i style={{ background: usageChartColors[index % usageChartColors.length] }} />{item.name}</span>
                ))}
              </div>
              {partial ? <p className="usage-trend-partial">浅色缺口表示该自然日尚未采集或只采集了部分时段。</p> : null}
              <PortalTrendChart trend={query.data} series={series} dimension={dimension} />
            </>
          ) : null}
        </div>
      ) : null}
    </section>
  );
}

function TrendSkeleton() {
  return (
    <div className="usage-trend-skeleton" aria-label="正在加载每日用量趋势">
      <span /><span /><span /><span />
    </div>
  );
}

function PortalTrendChart({
  trend,
  series,
  dimension
}: {
  trend: PortalUsageTrend;
  series: PortalTrendSeries[];
  dimension: PortalUsageTrendDimension;
}) {
  const { theme } = useTheme();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<EChartsType | null>(null);
  const labels = useMemo(() => trend.days.map((day) => day.date.slice(5).replace("-", "/")), [trend.days]);

  useEffect(() => {
    const container = containerRef.current;
    if (!container || series.length === 0) return;
    const chart = echarts.init(container, undefined, { renderer: "svg" });
    chartRef.current = chart;
    const dark = theme === "dark";
    const axisColor = dark ? "#7f8ba3" : "#8b95a7";
    const gridColor = dark ? "#273247" : "#e3e8f1";
    const labelIndexes = new Set([0, 0.25, 0.5, 0.75, 1].map((ratio) => (
      Math.round(Math.max(0, labels.length - 1) * ratio)
    )));
    const option: DailyTrendOption = {
      animation: false,
      aria: { enabled: true, description: "个人每日 Token 用量趋势" },
      grid: { left: 58, right: 18, top: 16, bottom: 36, containLabel: false },
      tooltip: {
        trigger: "axis",
        triggerOn: "mousemove|click|mousewheel",
        confine: true,
        enterable: false,
        renderMode: "html",
        className: "usage-chart-echarts-tooltip usage-trend-echarts-tooltip",
        backgroundColor: "#171d2b",
        borderColor: "#39455e",
        borderWidth: 1,
        padding: 0,
        extraCssText: "max-height:none;overflow:hidden;border-radius:9px;box-shadow:0 12px 28px rgb(8 12 22 / 28%);",
        axisPointer: { type: "line", lineStyle: { color: axisColor, type: "dashed", width: 1 } },
        formatter: (parameters) => renderPortalTrendTooltip(parameters, trend, dimension)
      },
      xAxis: {
        type: "category",
        boundaryGap: false,
        data: labels,
        axisLine: { lineStyle: { color: gridColor } },
        axisTick: { show: false },
        axisLabel: {
          color: axisColor,
          fontFamily: "SFMono-Regular, Consolas, Liberation Mono, monospace",
          fontSize: 9,
          margin: 17,
          interval: (index: number) => labelIndexes.has(index),
          hideOverlap: true
        }
      },
      yAxis: {
        type: "value",
        min: 0,
        splitNumber: 4,
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: {
          color: axisColor,
          fontFamily: "SFMono-Regular, Consolas, Liberation Mono, monospace",
          fontSize: 9,
          margin: 10,
          formatter: (value: number) => formatTokens(value)
        },
        splitLine: { lineStyle: { color: gridColor, width: 1 } }
      },
      series: series.map((item, index): LineSeriesOption => ({
        name: item.name,
        type: "line",
        data: item.values,
        connectNulls: false,
        showSymbol: true,
        symbol: "circle",
        symbolSize: 5,
        sampling: "lttb",
        smooth: false,
        emphasis: { focus: "series" },
        lineStyle: { width: 2, color: usageChartColors[index % usageChartColors.length] },
        itemStyle: {
          color: dark ? "#151b28" : "#ffffff",
          borderColor: usageChartColors[index % usageChartColors.length],
          borderWidth: 2
        }
      }))
    };
    chart.setOption(option, { notMerge: true });
    const resizeObserver = new ResizeObserver(() => chart.resize());
    resizeObserver.observe(container);
    return () => {
      resizeObserver.disconnect();
      chart.dispose();
      if (chartRef.current === chart) chartRef.current = null;
    };
  }, [dimension, labels, series, theme, trend]);

  const showDay = (index: number) => {
    if (!chartRef.current || trend.days.length === 0) return;
    const bounded = Math.max(0, Math.min(trend.days.length - 1, index));
    chartRef.current.dispatchAction({ type: "showTip", seriesIndex: 0, dataIndex: bounded });
    containerRef.current?.setAttribute("data-active-index", String(bounded));
  };

  return (
    <div
      className="usage-trend-chart"
      role="img"
      aria-label="个人每日 Token 用量趋势。鼠标悬停或使用左右方向键查看每日详情。"
      tabIndex={0}
      onFocus={() => showDay(trend.days.length - 1)}
      onBlur={() => chartRef.current?.dispatchAction({ type: "hideTip" })}
      onKeyDown={(event) => {
        if (!trend.days.length || !["ArrowLeft", "ArrowRight", "Home", "End", "Escape"].includes(event.key)) return;
        event.preventDefault();
        if (event.key === "Escape") {
          chartRef.current?.dispatchAction({ type: "hideTip" });
          return;
        }
        const current = Number(containerRef.current?.getAttribute("data-active-index") ?? trend.days.length - 1);
        const next = event.key === "Home" ? 0 : event.key === "End"
          ? trend.days.length - 1
          : current + (event.key === "ArrowLeft" ? -1 : 1);
        showDay(next);
      }}
    >
      <div className="usage-trend-chart-canvas" ref={containerRef} aria-hidden="true" />
    </div>
  );
}

export function buildPortalTrendSeries(
  trend: PortalUsageTrend | undefined,
  dimension: PortalUsageTrendDimension
): PortalTrendSeries[] {
  if (!trend) return [];
  const visibleValue = (state: string, value: number) => state === "uncollected" ? null : value;
  if (dimension === "total") {
    return [
      { name: "加权 Token", values: trend.days.map((day) => visibleValue(day.collection_state, day.weighted_tokens)) },
      { name: "未加权 Token", values: trend.days.map((day) => visibleValue(day.collection_state, day.total_tokens)) }
    ];
  }

  const totals = new Map<string, { model: string; effort: string; total: number }>();
  for (const day of trend.days) {
    for (const item of day.combinations) {
      const key = combinationKey(item.model, item.reasoning_effort);
      const current = totals.get(key) ?? { model: item.model, effort: item.reasoning_effort, total: 0 };
      current.total += item.weighted_tokens;
      totals.set(key, current);
    }
  }
  const ranked = [...totals.entries()].sort((left, right) => (
    right[1].total - left[1].total || combinationLabel(left[1].model, left[1].effort).localeCompare(
      combinationLabel(right[1].model, right[1].effort), "zh-CN", { numeric: true }
    )
  ));
  const direct = ranked.slice(0, directCombinationLimit);
  const overflow = new Set(ranked.slice(directCombinationLimit).map(([key]) => key));
  const result = direct.map(([key, identity]) => ({
    name: combinationLabel(identity.model, identity.effort),
    values: trend.days.map((day) => visibleValue(day.collection_state, day.combinations
      .filter((item) => combinationKey(item.model, item.reasoning_effort) === key)
      .reduce((sum, item) => sum + item.weighted_tokens, 0)))
  }));
  if (overflow.size > 0) {
    result.push({
      name: "其他组合",
      values: trend.days.map((day) => visibleValue(day.collection_state, day.combinations
        .filter((item) => overflow.has(combinationKey(item.model, item.reasoning_effort)))
        .reduce((sum, item) => sum + item.weighted_tokens, 0)))
    });
  }
  return result;
}

export function summarizePortalTrend(
  trend: PortalUsageTrend | undefined,
  dimension: PortalUsageTrendDimension
) {
  if (!trend) {
    return { items: defaultSummaryItems(dimension) };
  }
  const total = trend.days.reduce((sum, day) => sum + day.weighted_tokens, 0);
  if (dimension === "total") {
    const peak = trend.days.reduce((maximum, day) => Math.max(maximum, day.weighted_tokens), 0);
    return { items: [
      { label: `${trend.window_days}天加权`, value: formatTokens(total), title: `${total.toLocaleString("en-US")} 加权 Token` },
      { label: "日均", value: formatTokens(Math.round(total / Math.max(1, trend.window_days))), title: "所选自然日范围的日均加权 Token" },
      { label: "峰值", value: formatTokens(peak), title: `${peak.toLocaleString("en-US")} 加权 Token` }
    ] };
  }
  const combinations = new Map<string, { label: string; total: number }>();
  for (const day of trend.days) {
    for (const item of day.combinations) {
      const key = combinationKey(item.model, item.reasoning_effort);
      const current = combinations.get(key) ?? { label: combinationLabel(item.model, item.reasoning_effort), total: 0 };
      current.total += item.weighted_tokens;
      combinations.set(key, current);
    }
  }
  const primary = [...combinations.values()].sort((left, right) => right.total - left.total || left.label.localeCompare(right.label))[0];
  return { items: [
    { label: `${trend.window_days}天总量`, value: formatTokens(total), title: `${total.toLocaleString("en-US")} 加权 Token` },
    { label: "主要组合", value: primary?.label ?? "—", title: primary?.label ?? "暂无组合" },
    { label: "组合数", value: String(combinations.size), title: `${combinations.size} 个模型与推理强度组合` }
  ] };
}

function defaultSummaryItems(dimension: PortalUsageTrendDimension) {
  return dimension === "total"
    ? [
      { label: "30天加权", value: "—", title: "正在读取" },
      { label: "日均", value: "—", title: "正在读取" },
      { label: "峰值", value: "—", title: "正在读取" }
    ]
    : [
      { label: "30天总量", value: "—", title: "正在读取" },
      { label: "主要组合", value: "—", title: "正在读取" },
      { label: "组合数", value: "—", title: "正在读取" }
    ];
}

export function renderPortalTrendTooltip(
  parameters: CallbackDataParams | CallbackDataParams[],
  trend: PortalUsageTrend,
  dimension: PortalUsageTrendDimension
) {
  const values = Array.isArray(parameters) ? parameters : [parameters];
  const index = Number(values[0]?.dataIndex ?? 0);
  const day = trend.days[index];
  if (!day) return "";
  const rows = values
    .map((item) => ({
      name: String(item.seriesName ?? ""),
      value: chartValue(item.value),
      color: typeof item.color === "string" ? item.color : usageChartColors[0]
    }))
    .filter((item) => item.value !== null)
    .sort((left, right) => dimension === "total" ? 0 : (right.value ?? 0) - (left.value ?? 0) || left.name.localeCompare(right.name))
    .slice(0, 10)
    .map((item) => `<span><i style="background:${escapeAttribute(item.color)}"></i><b title="${escapeAttribute(item.name)}">${escapeHTML(item.name)}</b><em>${escapeHTML(formatTokens(item.value ?? 0))}</em></span>`)
    .join("");
  const requestRow = dimension === "total"
    ? `<span class="usage-trend-tooltip-request"><b>请求</b><em>${day.request_count.toLocaleString("en-US")}</em></span>`
    : `<span class="usage-trend-tooltip-total"><b>合计</b><em>${escapeHTML(formatTokens(day.weighted_tokens))}</em></span>`;
  return `<div class="overview-chart-tooltip usage-trend-tooltip" role="tooltip" data-active="true" data-layout="single-column"><strong>${escapeHTML(formatTrendDate(day.date))}</strong>${rows}${requestRow}</div>`;
}

function chartValue(value: CallbackDataParams["value"]): number | null {
  if (Array.isArray(value)) {
    const last = value[value.length - 1];
    return last === null || last === undefined ? null : Number(last) || 0;
  }
  return value === null || value === undefined || value === "-" ? null : Number(value) || 0;
}

function combinationKey(model: string, effort: string) {
  return `${model}\u0000${effort}`;
}

function combinationLabel(model: string, effort: string) {
  return `${model || "unknown"} · ${effort || "unknown"}`;
}

function trendWindowLabel(window: PortalUsageTrendWindow) {
  return trendWindows.find((item) => item.value === window)?.label ?? "30天";
}

function formatTrendDate(date: string) {
  const [, month = "", day = ""] = date.split("-");
  return `${Number(month)}月${Number(day)}日`;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "未知错误";
}

function escapeHTML(value: string) {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
  })[character] ?? character);
}

function escapeAttribute(value: string) {
  return escapeHTML(value).replace(/`/g, "&#96;");
}
