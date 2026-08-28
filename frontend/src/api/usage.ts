import { apiRequest } from "./client";
import type { UsageBreakdown, UsageWindow } from "./generated";

export type {
  UsageBreakdown,
  UsageCombination,
  UsageMetrics,
  UsageModelMetrics,
  UsageReasoningMetrics,
  UsageWindow
} from "./generated";

export const usageBreakdownQueryRoot = ["usage-breakdown"] as const;

export type UsageRange = {
  window: UsageWindow;
  startAt?: number;
  endAt?: number;
  account?: string;
};

type UsageRangeInput = UsageWindow | UsageRange;

function normalizeUsageRange(range: UsageRangeInput): UsageRange {
  return typeof range === "string" ? { window: range } : range;
}

export function usageBreakdownQueryKey(
  kind: "account" | "user",
  subject: string,
  rangeInput: UsageRangeInput
) {
  const range = normalizeUsageRange(rangeInput);
  return [...usageBreakdownQueryRoot, kind, subject, range.window, range.startAt ?? null, range.endAt ?? null, range.account ?? ""] as const;
}

export function readUsageBreakdown(
  kind: "account" | "user",
  subject: string,
  rangeInput: UsageRangeInput,
  signal?: AbortSignal
): Promise<UsageBreakdown> {
  const range = normalizeUsageRange(rangeInput);
  const query = new URLSearchParams({ window: range.window });
  if (range.window === "custom" && range.startAt !== undefined && range.endAt !== undefined) {
    query.set("start_at", String(range.startAt));
    query.set("end_at", String(range.endAt));
  }
  const path = kind === "account"
    ? "/admin/api/accounts/usage-breakdown"
    : "/admin/api/users/usage-breakdown";
  query.set(kind === "account" ? "account" : "email", subject);
  if (kind === "user" && range.account) query.set("account", range.account);
  return apiRequest<UsageBreakdown>(`${path}?${query.toString()}`, { signal });
}
