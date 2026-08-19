"use strict";

(() => {
  const normalizeSearchQuery = (value) => String(value ?? "")
    .trim()
    .toLocaleLowerCase("zh-CN");

  const matchesSearchQuery = (label, query) => {
    const normalizedQuery = normalizeSearchQuery(query);
    return !normalizedQuery || normalizeSearchQuery(label).includes(normalizedQuery);
  };

  const runtimeUnavailableDueToQuota = (runtime = {}) => {
    const raw = String(runtime?.status_message ?? "").trim();
    if (!raw) return false;
    try {
      const payload = JSON.parse(raw);
      const error = payload?.error || {};
      return [error.type, error.code]
        .map((value) => String(value ?? "").trim().toLowerCase())
        .includes("usage_limit_reached");
    } catch (_error) {
      return raw.toLowerCase().includes("usage_limit_reached");
    }
  };

  const seriesValueAt = (series, index) => Number(series?.values?.[index]) || 0;
  const seriesTotal = (series) => Number(series?.total) || 0;
  const seriesName = (series) => String(series?.name ?? "");
  const seriesNameCollator = new Intl.Collator("zh-CN", {
    numeric: true,
    sensitivity: "base"
  });

  const sortTooltipSeries = (series, index) => [...series].sort((left, right) => (
    seriesValueAt(right, index) - seriesValueAt(left, index)
    || seriesTotal(right) - seriesTotal(left)
    || seriesNameCollator.compare(seriesName(left), seriesName(right))
  ));

  const summarizeSeries = (series, valueCount, name = "") => {
    const safeValueCount = Math.max(0, Number.parseInt(valueCount, 10) || 0);
    const values = Array.from({ length: safeValueCount }, (_, index) => (
      series.reduce((total, item) => total + seriesValueAt(item, index), 0)
    ));
    const total = values.reduce((sum, value) => sum + value, 0);
    return {
      name: String(name),
      values,
      current: values.at(-1) || 0,
      average: values.length ? Math.round(total / values.length) : 0,
      maximum: Math.max(0, ...values),
      total
    };
  };

  const adaptivePointIndexes = (valueCount, plotWidth, minimumSpacing = 10) => {
    const count = Math.max(0, Number.parseInt(valueCount, 10) || 0);
    if (!count) return [];
    if (count === 1) return [0];
    const width = Math.max(0, Number(plotWidth) || 0);
    const spacing = Math.max(1, Number(minimumSpacing) || 10);
    const visibleCount = Math.min(count, Math.max(2, Math.floor(width / spacing) + 1));
    if (visibleCount === count) return Array.from({ length: count }, (_, index) => index);
    return [...new Set(Array.from({ length: visibleCount }, (_, index) => (
      Math.round(index * (count - 1) / (visibleCount - 1))
    )))];
  };

  const rectangleIntersectionArea = (left, right) => {
    const width = Math.max(0, Math.min(left.right, right.right) - Math.max(left.left, right.left));
    const height = Math.max(0, Math.min(left.bottom, right.bottom) - Math.max(left.top, right.top));
    return width * height;
  };

  const placeTooltip = (anchor = {}, tooltipSize = {}, bounds = {}, points = [], options = {}) => {
    const gap = Math.max(0, Number(options.gap) || 14);
    const padding = Math.max(0, Number(options.padding) || 8);
    const anchorX = Number(anchor.x) || 0;
    const anchorY = Number(anchor.y) || 0;
    const width = Math.max(0, Number(tooltipSize.width) || 0);
    const height = Math.max(0, Number(tooltipSize.height) || 0);
    const boundWidth = Math.max(0, Number(bounds.width) || 0);
    const boundHeight = Math.max(0, Number(bounds.height) || 0);
    const minX = padding;
    const minY = padding;
    const maxX = Math.max(minX, boundWidth - width - padding);
    const maxY = Math.max(minY, boundHeight - height - padding);
    const clamp = (value, minimum, maximum) => Math.max(minimum, Math.min(maximum, value));
    const candidates = [
      { placement: "bottom-right", x: anchorX + gap, y: anchorY + gap },
      { placement: "top-right", x: anchorX + gap, y: anchorY - height - gap },
      { placement: "bottom-left", x: anchorX - width - gap, y: anchorY + gap },
      { placement: "top-left", x: anchorX - width - gap, y: anchorY - height - gap }
    ];

    return candidates.map((candidate, preference) => {
      const x = clamp(candidate.x, minX, maxX);
      const y = clamp(candidate.y, minY, maxY);
      const tooltipRectangle = { left: x, top: y, right: x + width, bottom: y + height };
      const overlap = points.reduce((total, point) => {
        const radius = Math.max(0, Number(point.radius) || 10);
        const pointX = Number(point.x) || 0;
        const pointY = Number(point.y) || 0;
        return total + rectangleIntersectionArea(tooltipRectangle, {
          left: pointX - radius,
          top: pointY - radius,
          right: pointX + radius,
          bottom: pointY + radius
        });
      }, 0);
      const clampedDistance = Math.abs(x - candidate.x) + Math.abs(y - candidate.y);
      return {
        x,
        y,
        placement: candidate.placement,
        // 避让数据点优先于候选方向，边界修正距离用于选择更自然的位置。
        score: overlap * 10000 + clampedDistance * 10 + preference
      };
    }).sort((left, right) => left.score - right.score)[0];
  };

  const accountUsageNumberFields = [
    "request_count",
    "success_count",
    "failed_count",
    "input_tokens",
    "output_tokens",
    "reasoning_tokens",
    "cached_tokens",
    "total_tokens",
    "weighted_tokens"
  ];

  const reportedValue = (value, fallback) => {
    const normalized = String(value ?? "").trim();
    return !normalized || normalized === "unknown" ? fallback : normalized;
  };

  const accountModelEffortColorKey = (reasoningEffort) => {
    const normalized = String(reasoningEffort ?? "").trim().toLowerCase();
    return [
      "none",
      "minimal",
      "low",
      "medium",
      "high",
      "xhigh",
      "ultra",
      "max",
      "auto"
    ].includes(normalized) ? normalized : "unknown";
  };

  const groupAccountModelUsage = (combinations = []) => {
    const models = new Map();
    (Array.isArray(combinations) ? combinations : []).forEach((item) => {
      const totalTokens = Math.max(0, Number(item?.total_tokens) || 0);
      if (!totalTokens) return;
      const modelName = reportedValue(item?.model, "未上报模型");
      const effortName = reportedValue(item?.reasoning_effort, "未上报强度");
      const model = models.get(modelName) || {
        model: modelName,
        total_tokens: 0,
        efforts: new Map()
      };
      const effort = model.efforts.get(effortName) || {
        reasoning_effort: effortName,
        ...Object.fromEntries(accountUsageNumberFields.map((field) => [field, 0]))
      };
      accountUsageNumberFields.forEach((field) => {
        effort[field] += Math.max(0, Number(item?.[field]) || 0);
      });
      model.total_tokens += totalTokens;
      model.efforts.set(effortName, effort);
      models.set(modelName, model);
    });

    return [...models.values()]
      .map((model) => {
        const efforts = [...model.efforts.values()].sort((left, right) => (
          right.total_tokens - left.total_tokens
          || seriesNameCollator.compare(left.reasoning_effort, right.reasoning_effort)
        ));
        let allocatedPercent = 0;
        efforts.forEach((effort, index) => {
          const share = index === efforts.length - 1
            ? 100 - allocatedPercent
            : Math.round((effort.total_tokens / model.total_tokens) * 10000) / 100;
          effort.share_percent = Math.max(0, Math.round(share * 100) / 100);
          allocatedPercent += effort.share_percent;
        });
        return {
          model: model.model,
          total_tokens: model.total_tokens,
          efforts
        };
      })
      .sort((left, right) => (
        right.total_tokens - left.total_tokens
        || seriesNameCollator.compare(left.model, right.model)
      ));
  };

  const monitorUtils = {
    accountModelEffortColorKey,
    adaptivePointIndexes,
    groupAccountModelUsage,
    matchesSearchQuery,
    placeTooltip,
    runtimeUnavailableDueToQuota,
    sortTooltipSeries,
    summarizeSeries
  };
  if (typeof module !== "undefined" && module.exports) module.exports = monitorUtils;
  globalThis.MonitorUtils = monitorUtils;
})();
