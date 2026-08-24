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

export function usageBreakdownQueryKey(
  kind: "account" | "user",
  subject: string,
  window: UsageWindow
) {
  return [...usageBreakdownQueryRoot, kind, subject, window] as const;
}

export function readUsageBreakdown(
  kind: "account" | "user",
  subject: string,
  window: UsageWindow,
  signal?: AbortSignal
): Promise<UsageBreakdown> {
  const query = new URLSearchParams({ window });
  const path = kind === "account"
    ? "/admin/api/accounts/usage-breakdown"
    : "/admin/api/users/usage-breakdown";
  query.set(kind === "account" ? "account" : "email", subject);
  return apiRequest<UsageBreakdown>(`${path}?${query.toString()}`, { signal });
}
