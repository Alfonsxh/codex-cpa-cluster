import { apiRequest } from "./client";
import type { OverviewPayload, OverviewUsageResponse, ReleaseStatus } from "./generated";

export type { OverviewPayload, OverviewSummary, OverviewUsageResponse, ReleaseStatus, TokenSeries } from "./generated";

export const overviewSummaryQueryKey = ["overview-summary"] as const;

export function readOverviewSummary(signal?: AbortSignal): Promise<OverviewPayload> {
  return apiRequest<OverviewPayload>("/admin/api/overview/summary", { signal });
}

export type OverviewUsageWindow = "3600" | "21600" | "86400" | "604800" | "2592000" | "today" | "since_reset" | "custom";

export type OverviewUsageOptions = {
  window: OverviewUsageWindow;
  accounts?: string[];
  users?: string[];
  userLimit?: number;
  startAt?: number;
  endAt?: number;
};

export function readOverviewUsage(options: OverviewUsageOptions, signal?: AbortSignal): Promise<OverviewUsageResponse> {
  const query = new URLSearchParams({
    window: options.window,
    user_limit: String(options.userLimit ?? 10)
  });
  options.accounts?.forEach((account) => query.append("account", account));
  options.users?.forEach((user) => query.append("user", user));
  if (options.window === "custom" && options.startAt !== undefined && options.endAt !== undefined) {
    query.set("start_at", String(options.startAt));
    query.set("end_at", String(options.endAt));
  }
  return apiRequest<OverviewUsageResponse>(`/admin/api/overview/usage?${query}`, { signal });
}

export function readReleaseStatus(fresh = false, signal?: AbortSignal): Promise<ReleaseStatus> {
  return apiRequest<ReleaseStatus>(`/admin/api/release?fresh=${fresh ? "1" : "0"}`, { signal });
}
