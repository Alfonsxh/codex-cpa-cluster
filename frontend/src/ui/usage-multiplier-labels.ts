import type { UsageDisplayMultipliers } from "../api/generated";

function labelWithMultiplier(name: string, multipliers?: Record<string, number>): string {
  const label = name.trim() || "unknown";
  const key = label.toLowerCase();
  const multiplier = multipliers && Object.hasOwn(multipliers, key) ? multipliers[key] : multipliers?.unknown;
  return multiplier !== undefined && Number.isFinite(multiplier) && multiplier > 0 && multiplier !== 1
    ? `${label} (×${multiplier})`
    : label;
}

export function formatUsageModelLabel(model: string, multipliers?: UsageDisplayMultipliers): string {
  return labelWithMultiplier(model, multipliers?.models);
}

export function formatUsageReasoningLabel(effort: string, multipliers?: UsageDisplayMultipliers): string {
  return labelWithMultiplier(effort, multipliers?.reasoning_efforts);
}

export function formatUsageCombinationLabel(model: string, effort: string, multipliers?: UsageDisplayMultipliers): string {
  return `${formatUsageModelLabel(model, multipliers)} · ${formatUsageReasoningLabel(effort, multipliers)}`;
}
