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
import { useEffect, useMemo, useRef } from "react";

import type { TokenSeries } from "../../api/overview";
import { formatTokens } from "../formatters";
import { useTheme } from "../ThemeProvider";

echarts.use([LineChart, GridComponent, TooltipComponent, AriaComponent, SVGRenderer]);

type UsageChartOption = ComposeOption<
  LineSeriesOption | GridComponentOption | TooltipComponentOption | AriaComponentOption
>;

export const usageChartColors = [
  "#6374d8", "#4b8ccf", "#c58a34", "#9070c5", "#5263aa",
  "#c45757", "#d16f4f", "#b96894", "#447a9d", "#8b6d48"
] as const;

export type UsageChartProps = {
  buckets: number[];
  series: TokenSeries[];
  summary?: boolean;
  includeDateLabels?: boolean;
  ariaLabel: string;
};

export function UsageChart({ buckets, series, summary = false, includeDateLabels = false, ariaLabel }: UsageChartProps) {
  const { theme } = useTheme();
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<EChartsType | null>(null);
  const height = summary ? 260 : 300;
  const labels = useMemo(
    () => buckets.map((timestamp) => formatChartTime(timestamp, includeDateLabels)),
    [buckets, includeDateLabels]
  );

  useEffect(() => {
    const container = containerRef.current;
    if (!container || !buckets.length || !series.length) return;

    const chart = echarts.init(container, undefined, { renderer: "svg" });
    chartRef.current = chart;
    const dark = theme === "dark";
    const axisColor = dark ? "#7f8ba3" : "#8b95a7";
    const gridColor = dark ? "#273247" : "#e3e8f1";
    const chartWidth = Math.round(container.getBoundingClientRect().width) || 1_000;
    const recordedMaximum = Math.max(1, ...series.flatMap((item) => item.values.map((value) => Number(value) || 0)));
    const maximum = summary ? recordedMaximum * 1.08 : recordedMaximum;
    const xLabelIndexes = new Set([0, 0.25, 0.5, 0.75, 1].map((ratio) => (
      Math.round(Math.max(0, labels.length - 1) * ratio)
    )));
    const option: UsageChartOption = {
      animation: false,
      color: [...usageChartColors],
      aria: { enabled: true, description: ariaLabel },
      grid: {
        // Frozen v1 renders non-summary SVGs in a fixed 1000px viewBox and
        // stretches them to the panel width. Scale its plot margins too.
        left: summary ? chartWidth <= 520 ? 58 : 64 : chartWidth * 72 / 1_000,
        right: summary ? 16 : chartWidth * 20 / 1_000,
        top: 24,
        bottom: 44,
        containLabel: false
      },
      tooltip: {
        trigger: "axis",
        triggerOn: "mousemove|click|mousewheel",
        confine: true,
        enterable: false,
        renderMode: "html",
        className: "usage-chart-echarts-tooltip",
        backgroundColor: "#171d2b",
        borderColor: "#39455e",
        borderWidth: 1,
        padding: 0,
        extraCssText: "max-height:none;overflow:hidden;border-radius:9px;box-shadow:0 12px 28px rgb(8 12 22 / 28%);",
        axisPointer: {
          type: "line",
          lineStyle: { color: axisColor, type: "dashed", width: 1 }
        },
        formatter: (parameters) => renderUsageTooltip(parameters, buckets)
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
          margin: 22,
          interval: (index: number) => xLabelIndexes.has(index),
          hideOverlap: true
        }
      },
      yAxis: {
        type: "value",
        min: 0,
        max: maximum,
        interval: maximum / 4,
        splitNumber: 4,
        axisLine: { show: false },
        axisTick: { show: false },
        axisLabel: {
          color: axisColor,
          fontFamily: "SFMono-Regular, Consolas, Liberation Mono, monospace",
          fontSize: 9,
          margin: 12,
          formatter: (value: number) => formatTokens(value)
        },
        splitLine: { lineStyle: { color: gridColor, width: 1 } }
      },
      series: series.map((item, index): LineSeriesOption => ({
        name: item.name,
        type: "line",
        data: item.values,
        showSymbol: summary,
        symbol: "circle",
        symbolSize: summary ? 6 : 4,
        sampling: "lttb",
        smooth: false,
        connectNulls: false,
        emphasis: { focus: "series" },
        lineStyle: { width: summary ? 2.5 : 2, color: usageChartColors[index % usageChartColors.length] },
        itemStyle: {
          color: dark ? "#151b28" : "#ffffff",
          borderColor: usageChartColors[index % usageChartColors.length],
          borderWidth: 2
        },
        areaStyle: summary
          ? { color: dark ? "#262d51" : "#eef0ff", opacity: 0.62 }
          : undefined
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
  }, [ariaLabel, buckets, labels, series, summary, theme]);

  const showBucket = (index: number) => {
    const chart = chartRef.current;
    if (!chart || !buckets.length) return;
    const bounded = Math.max(0, Math.min(buckets.length - 1, index));
    chart.dispatchAction({ type: "showTip", seriesIndex: 0, dataIndex: bounded });
    containerRef.current?.setAttribute("data-active-index", String(bounded));
  };

  return (
    <div
      className={`overview-legacy-chart${summary ? " summary" : ""}`}
      role="img"
      aria-label={`${ariaLabel}。鼠标悬停或使用左右方向键查看聚合点详情。`}
      tabIndex={0}
      onFocus={() => showBucket(buckets.length - 1)}
      onBlur={() => chartRef.current?.dispatchAction({ type: "hideTip" })}
      onKeyDown={(event) => {
        if (!buckets.length || !["ArrowLeft", "ArrowRight", "Home", "End", "Escape"].includes(event.key)) return;
        event.preventDefault();
        if (event.key === "Escape") {
          chartRef.current?.dispatchAction({ type: "hideTip" });
          return;
        }
        const current = Number(containerRef.current?.getAttribute("data-active-index") ?? buckets.length - 1);
        const next = event.key === "Home"
          ? 0
          : event.key === "End"
            ? buckets.length - 1
            : current + (event.key === "ArrowLeft" ? -1 : 1);
        showBucket(next);
      }}
    >
      <div ref={containerRef} className="usage-chart-canvas" style={{ height }} aria-hidden="true" />
    </div>
  );
}

export function tooltipRows(parameters: CallbackDataParams | CallbackDataParams[]) {
  const values = Array.isArray(parameters) ? parameters : [parameters];
  return values
    .map((item) => ({
      name: String(item.seriesName ?? ""),
      value: chartValue(item.value),
      color: typeof item.color === "string" ? item.color : "#6374d8",
      seriesIndex: Number(item.seriesIndex ?? 0)
    }))
    .sort((left, right) => right.value - left.value || left.name.localeCompare(right.name, "zh-CN", { numeric: true }))
    .slice(0, 10);
}

export function renderUsageTooltip(
  parameters: CallbackDataParams | CallbackDataParams[],
  buckets: number[]
) {
  const values = Array.isArray(parameters) ? parameters : [parameters];
  const dataIndex = Number(values[0]?.dataIndex ?? 0);
  const timestamp = formatTimestamp(buckets[dataIndex] ?? 0);
  const rows = tooltipRows(values).map((item) => (
    `<span><i style="background:${escapeAttribute(item.color)}"></i>`
    + `<b title="${escapeAttribute(item.name)}">${escapeHtml(item.name)}</b>`
    + `<em>${escapeHtml(formatTokens(item.value))}</em></span>`
  )).join("");
  return `<div class="overview-chart-tooltip" role="tooltip" data-active="true" data-layout="single-column" data-list="${values.length > 1}"><strong>${escapeHtml(timestamp)}</strong>${rows}</div>`;
}

function chartValue(value: CallbackDataParams["value"]) {
  if (Array.isArray(value)) {
    for (let index = value.length - 1; index >= 0; index -= 1) {
      if (typeof value[index] === "number") return Number(value[index]);
    }
    return 0;
  }
  return Number(value ?? 0);
}

function formatChartTime(timestamp: number, includeDate: boolean) {
  if (!timestamp) return "—";
  const date = new Date(timestamp * 1000);
  const time = `${String(date.getHours()).padStart(2, "0")}:${String(date.getMinutes()).padStart(2, "0")}`;
  return includeDate
    ? `${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")} ${time}`
    : time;
}

function formatTimestamp(timestamp: number) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
    hour12: false
  }).format(new Date(timestamp * 1000));
}

function escapeHtml(value: string) {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", "\"": "&quot;", "'": "&#39;"
  })[character] ?? character);
}

function escapeAttribute(value: string) {
  return escapeHtml(value).replace(/[^#(),.%\w\s-]/g, "");
}
