import { apiRequest } from "./client";
import type {
  PortalAccounts,
  PortalKeyResponse,
  PortalProfile,
  PortalQuota,
  PortalRoute,
  PortalRouteMutationResponse,
  PortalSession,
  PortalUsageTrend,
  PortalUsageTrendDimension,
  PortalUsageTrendWindow,
  PortalUsageWindow,
  UsageBreakdown
} from "./generated";

export type {
  PortalAccount,
  PortalAccounts,
  PortalAccountStatus,
  PortalKeyResponse,
  PortalProfile,
  PortalQuota,
  PortalRoute,
  PortalRouteMutationResponse,
  PortalSession,
  PortalUsageTrend,
  PortalUsageTrendDimension,
  PortalUsageTrendWindow,
  PortalUsageWindow
} from "./generated";

export const portalSessionQueryKey = ["portal-session"] as const;
export const portalProfileQueryKey = ["portal-profile"] as const;
export const portalQuotaQueryKey = ["portal-quota"] as const;
export const portalRouteQueryKey = ["portal-route"] as const;
export const portalAccountsQueryRoot = ["portal-accounts"] as const;
export const portalBreakdownQueryRoot = ["portal-breakdown"] as const;
export const portalUsageTrendQueryRoot = ["portal-usage-trend"] as const;

export function portalAccountsQueryKey(window: PortalUsageWindow) {
  return [...portalAccountsQueryRoot, window] as const;
}

export function portalBreakdownQueryKey(account: string, window: PortalUsageWindow) {
  return [...portalBreakdownQueryRoot, account || "all", window] as const;
}

export function portalUsageTrendQueryKey(
  window: PortalUsageTrendWindow,
  dimension: PortalUsageTrendDimension
) {
  return [...portalUsageTrendQueryRoot, window, dimension] as const;
}

export function readPortalSession(signal?: AbortSignal): Promise<PortalSession> {
  return apiRequest<PortalSession>("/usage/session", { signal });
}

export function loginPortal(email: string, password: string): Promise<PortalSession> {
  return apiRequest<PortalSession>("/usage/session", {
    method: "POST",
    body: JSON.stringify({ email, password })
  });
}

export function logoutPortal(): Promise<{ logged_out: true }> {
  return apiRequest<{ logged_out: true }>("/usage/session", { method: "DELETE" });
}

export function readPortalProfile(signal?: AbortSignal): Promise<PortalProfile> {
  return apiRequest<PortalProfile>("/usage/me/profile", { signal });
}

export function readPortalKey(signal?: AbortSignal): Promise<PortalKeyResponse> {
  return apiRequest<PortalKeyResponse>("/usage/me/key", { signal, cache: "no-store" });
}

export function readPortalQuota(signal?: AbortSignal): Promise<PortalQuota> {
  return apiRequest<PortalQuota>("/usage/me/quota", { signal });
}

export function readPortalRoute(signal?: AbortSignal): Promise<PortalRoute> {
  return apiRequest<PortalRoute>("/usage/me/route", { signal });
}

export function readPortalAccounts(
  window: PortalUsageWindow,
  signal?: AbortSignal
): Promise<PortalAccounts> {
  const query = new URLSearchParams({ window });
  return apiRequest<PortalAccounts>(`/usage/me/accounts?${query.toString()}`, { signal });
}

export function readPortalBreakdown(
  account: string,
  window: PortalUsageWindow,
  signal?: AbortSignal
): Promise<UsageBreakdown> {
  const query = new URLSearchParams({ window });
  if (account) query.set("account", account);
  return apiRequest<UsageBreakdown>(`/usage/me/usage-breakdown?${query.toString()}`, { signal });
}

export function readPortalUsageTrend(
  window: PortalUsageTrendWindow,
  dimension: PortalUsageTrendDimension,
  signal?: AbortSignal
): Promise<PortalUsageTrend> {
  const query = new URLSearchParams({ window, dimension });
  return apiRequest<PortalUsageTrend>(`/usage/me/usage-trend?${query.toString()}`, { signal });
}

export function changePortalPassword(
  currentPassword: string,
  newPassword: string
): Promise<{ message: string; password_change_required: false }> {
  return apiRequest("/usage/me/password", {
    method: "PUT",
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword })
  });
}

export function switchPortalAccount(groupID: string): Promise<PortalRouteMutationResponse> {
  return apiRequest("/usage/me/group", {
    method: "PUT",
    body: JSON.stringify({ group_id: groupID })
  });
}

export function autoAssignPortalAccount(): Promise<PortalRouteMutationResponse> {
  return apiRequest("/usage/me/route/auto-assign", {
    method: "POST"
  });
}

export function rotatePortalKey(): Promise<{
  message: string;
  api_key: string;
  snapshot_generation: string;
}> {
  return apiRequest("/usage/me/key/rotate", {
    method: "POST",
    cache: "no-store",
    body: JSON.stringify({ confirm: true })
  });
}
