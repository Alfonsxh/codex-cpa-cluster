import { apiRequest } from "./client";
import type {
  Account,
  AccountCatalog,
  AccountClearAuthRequest,
  AccountClearAuthResponse,
  AccountCreateRequestWritable,
  AccountCreateResponse,
  AccountDeleteRequest,
  AccountDeleteResponse,
  AccountUpdateRequestWritable,
  AccountUpdateResponse,
  RebalanceResponse,
  ResetAccountQuotaInspection,
  ResetAccountQuotaRequestWritable,
  ResetAccountQuotaResponse,
  UsageWindow
} from "./generated";

export type {
  Account,
  AccountCatalog,
  AccountClearAuthRequest,
  AccountClearAuthResponse,
  AccountCreateRequestWritable,
  AccountCreateResponse,
  AccountDeleteRequest,
  AccountDeleteResponse,
  AccountState,
  AccountUpdateRequestWritable,
  AccountUpdateResponse,
  RebalanceResponse,
  RebalanceResult,
  ResetAccountQuotaInspection,
  ResetAccountQuotaRequestWritable,
  ResetAccountQuotaResponse
} from "./generated";

export const accountsQueryKey = ["accounts"] as const;
export type AccountUsageWindow = Extract<UsageWindow, "3600" | "today" | "86400" | "604800" | "2592000" | "since_reset" | "all" | "custom">;
export type AccountUsageRange = {
  window: AccountUsageWindow;
  startAt?: number;
  endAt?: number;
};

export function accountListQueryKey(range: AccountUsageRange) {
  return [...accountsQueryKey, range.window, range.startAt ?? null, range.endAt ?? null] as const;
}

export function listAccounts(range: AccountUsageRange, signal?: AbortSignal, fresh = false): Promise<AccountCatalog> {
  const query = new URLSearchParams({ window: range.window });
  if (range.window === "custom" && range.startAt !== undefined && range.endAt !== undefined) {
    query.set("start_at", String(range.startAt));
    query.set("end_at", String(range.endAt));
  }
  if (fresh) query.set("fresh", "1");
  return apiRequest<AccountCatalog>(`/admin/api/accounts?${query.toString()}`, { signal });
}

export function createAccount(
  request: AccountCreateRequestWritable,
  csrfToken: string
): Promise<AccountCreateResponse> {
  return accountMutation<AccountCreateResponse>("/admin/api/accounts", request, csrfToken);
}

export function updateAccount(
  request: AccountUpdateRequestWritable,
  csrfToken: string
): Promise<AccountUpdateResponse> {
  return accountMutation<AccountUpdateResponse>("/admin/api/accounts/update", request, csrfToken);
}

export function updateAccountPolicy(
  request: AccountUpdateRequestWritable,
  csrfToken: string
): Promise<AccountUpdateResponse> {
  return accountMutation<AccountUpdateResponse>("/admin/api/accounts/policy", request, csrfToken);
}

export function clearAccountAuth(
  request: AccountClearAuthRequest,
  csrfToken: string
): Promise<AccountClearAuthResponse> {
  return accountMutation<AccountClearAuthResponse>("/admin/api/accounts/clear-auth", request, csrfToken);
}

export function deleteAccount(
  request: AccountDeleteRequest,
  csrfToken: string
): Promise<AccountDeleteResponse> {
  return accountMutation<AccountDeleteResponse>("/admin/api/accounts/delete", request, csrfToken);
}

export function rebalanceAllAccounts(csrfToken: string): Promise<RebalanceResponse> {
  return apiRequest<RebalanceResponse>("/admin/api/accounts/rebalance-all", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ confirm: "rebalance-all" })
  });
}

export function rebalanceAccount(accountID: string, csrfToken: string): Promise<RebalanceResponse> {
  return apiRequest<RebalanceResponse>("/admin/api/accounts/rebalance", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ id: accountID, confirm: accountID })
  });
}

export function accountQuotaResetQueryKey(accountID: string) {
  return [...accountsQueryKey, "quota-reset", accountID] as const;
}

export function inspectAccountQuotaReset(
  accountID: string,
  signal?: AbortSignal
): Promise<ResetAccountQuotaInspection> {
  const query = new URLSearchParams({ account: accountID });
  return apiRequest<ResetAccountQuotaInspection>(`/admin/api/accounts/quota-reset?${query.toString()}`, {
    signal,
    cache: "no-store"
  });
}

export function resetAccountQuota(
  request: ResetAccountQuotaRequestWritable,
  csrfToken: string
): Promise<ResetAccountQuotaResponse> {
  return accountMutation<ResetAccountQuotaResponse>("/admin/api/accounts/reset-quota", request, csrfToken);
}

function accountMutation<T>(path: string, body: unknown, csrfToken: string): Promise<T> {
  return apiRequest<T>(path, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify(body)
  });
}
