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
  RebalanceResponse
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
  RebalanceResult
} from "./generated";

export const accountsQueryKey = ["accounts"] as const;

export function listAccounts(signal?: AbortSignal): Promise<AccountCatalog> {
  return apiRequest<AccountCatalog>("/admin/api/accounts", { signal });
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

function accountMutation<T>(path: string, body: unknown, csrfToken: string): Promise<T> {
  return apiRequest<T>(path, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify(body)
  });
}
