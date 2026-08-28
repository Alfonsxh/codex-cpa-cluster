import { apiRequest } from "./client";
import type {
  MessageResponse,
  UserCatalog,
  UserCreateResponse,
  UserDetailResponse,
  UserKeyRotationResponse,
  UserPasswordResetResponse,
  UserQuotaActionResponse,
  UserQuotaOperationSummary,
  UserQuotaMode,
  UserQuotaResult
} from "./generated";

export type {
  UserCatalog,
  UserAccountDetail,
  UserDetail,
  UserDetailResponse,
  UserOneTimeKey,
  UserQuotaActionResponse,
  UserQuotaOperationSummary,
  UserQuotaAdjustment,
  UserQuotaMode,
  UserQuotaResult,
  UserSummary,
  UserTeam,
  UserWeeklyQuota
} from "./generated";

export type UserListParams = {
  query: string;
  teamId: string;
  usageState: "all" | "used" | "unused";
  window: string;
  startAt?: number;
  endAt?: number;
  sort: "email" | "requests" | "tokens" | "quota" | "last_used";
  direction: "asc" | "desc";
  page: number;
  pageSize: number;
  fresh?: boolean;
};

export type TeamMemberListParams = {
  query: string;
  teamId: string;
  usageState: "all" | "used" | "unused";
  window: string;
  page: number;
  pageSize: number;
};

export type UserCreateResult = UserCreateResponse;
export type UserKeyRotationResult = UserKeyRotationResponse;
export type UserPasswordResetResult = UserPasswordResetResponse;
export type UserActionResult = MessageResponse;

export const usersQueryRoot = ["users"] as const;

export function usersQueryKey(params: UserListParams) {
  return [
    ...usersQueryRoot,
    params.query,
    params.teamId,
    params.usageState,
    params.window,
    params.startAt ?? null,
    params.endAt ?? null,
    params.sort,
    params.direction,
    params.page,
    params.pageSize,
    Boolean(params.fresh)
  ] as const;
}

export function listUsers(params: UserListParams, signal?: AbortSignal): Promise<UserCatalog> {
  const query = new URLSearchParams({
    view: "summary",
    window: params.window,
    page: String(params.page),
    page_size: String(params.pageSize),
    q: params.query,
    sort: params.sort,
    direction: params.direction
  });
  if (params.teamId) query.set("team_id", params.teamId);
  if (params.usageState !== "all") query.set("usage_state", params.usageState);
  if (params.window === "custom" && params.startAt !== undefined && params.endAt !== undefined) {
    query.set("start_at", String(params.startAt));
    query.set("end_at", String(params.endAt));
  }
  if (params.fresh) query.set("fresh", "1");
  return apiRequest<UserCatalog>(`/admin/api/users?${query.toString()}`, { signal });
}

export function listTeamMembers(params: TeamMemberListParams, signal?: AbortSignal): Promise<UserCatalog> {
  const query = new URLSearchParams({
    view: "members",
    window: params.window,
    page: String(params.page),
    page_size: String(params.pageSize),
    q: params.query,
    sort: "tokens",
    direction: "desc",
    usage_state: params.usageState
  });
  if (params.teamId) query.set("team_id", params.teamId);
  return apiRequest<UserCatalog>(`/admin/api/users?${query.toString()}`, { signal });
}

export function userDetailQueryKey(email: string, params: Pick<UserListParams, "window" | "startAt" | "endAt">) {
  return [...usersQueryRoot, "detail", email, params.window, params.startAt ?? null, params.endAt ?? null] as const;
}

export function readUserDetail(
  email: string,
  params: Pick<UserListParams, "window" | "startAt" | "endAt">,
  signal?: AbortSignal
) {
  const query = new URLSearchParams({ email, window: params.window });
  if (params.window === "custom" && params.startAt !== undefined && params.endAt !== undefined) {
    query.set("start_at", String(params.startAt));
    query.set("end_at", String(params.endAt));
  }
  return apiRequest<UserDetailResponse>(`/admin/api/users/detail?${query.toString()}`, { signal });
}

export function createUser(email: string, teamId: string | null, csrfToken: string) {
  return apiRequest<UserCreateResult>("/admin/api/users", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ email, team_id: teamId })
  });
}

export function rotateUserKey(label: string, csrfToken: string) {
  return apiRequest<UserKeyRotationResult>("/admin/api/keys/rotate", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ label })
  });
}

export function resetUserPassword(email: string, csrfToken: string) {
  return apiRequest<UserPasswordResetResult>("/admin/api/users/reset-password", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ email })
  });
}

export function revokeUser(email: string, csrfToken: string) {
  return apiRequest<UserActionResult>("/admin/api/users/revoke", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ email })
  });
}

export function deleteUser(email: string, revokeKeys: boolean, csrfToken: string) {
  return apiRequest<UserActionResult>("/admin/api/users/delete", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ email, confirm: email, revoke_keys: revokeKeys })
  });
}

export function userQuotaQueryKey(email: string) {
  return [...usersQueryRoot, "quota", email] as const;
}

export function readUserQuota(email: string, signal?: AbortSignal) {
  const query = new URLSearchParams({ email });
  return apiRequest<UserQuotaResult>(`/admin/api/users/quota?${query.toString()}`, { signal });
}

export function updateUserQuota(
  email: string,
  mode: UserQuotaMode,
  weeklyTokens: number | null,
  csrfToken: string
) {
  return apiRequest<UserQuotaResult>("/admin/api/users/quota", {
    method: "PUT",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({
      email,
      mode,
      weekly_tokens: mode === "custom" ? weeklyTokens : null
    })
  });
}

export function clearUserQuota(email: string, csrfToken: string) {
  const query = new URLSearchParams({ email });
  return apiRequest<UserQuotaResult>(`/admin/api/users/quota?${query.toString()}`, {
    method: "DELETE",
    headers: { "X-CSRF-Token": csrfToken }
  });
}

export function assignUserTeam(
  email: string,
  teamId: string | null,
  csrfToken: string
) {
  return apiRequest<{ message: string }>("/admin/api/users/team", {
    method: "PUT",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({
      email,
      team_id: teamId
    })
  });
}

export function assignUsersTeam(
  users: string[],
  teamId: string | null,
  csrfToken: string,
  expectedTeamId?: string | null
) {
  const body: { users: string[]; team_id: string | null; expected_team_id?: string | null } = {
    users,
    team_id: teamId
  };
  if (expectedTeamId !== undefined) body.expected_team_id = expectedTeamId;
  return apiRequest<{ message: string }>("/admin/api/users/team/batch", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify(body)
  });
}

export type UserQuotaActionInput = {
  action: "restore_default" | "add_bonus" | "reset_usage";
  scope: "selected" | "all";
  users: string[];
  tokenAmount?: number;
  reason?: string;
  confirm: "restore_default" | "add_bonus" | "reset_current_week_usage" | "reset_all_current_week_usage";
};

export const userQuotaOperationsQueryKey = [...usersQueryRoot, "quota-operations"] as const;

export function readUserQuotaOperations(signal?: AbortSignal) {
  return apiRequest<UserQuotaOperationSummary>("/admin/api/users/quota-actions", { signal });
}

export function applyUserQuotaAction(input: UserQuotaActionInput, csrfToken: string) {
  const body: Record<string, unknown> = {
    action: input.action,
    scope: input.scope,
    users: input.scope === "selected" ? input.users : [],
    confirm: input.confirm
  };
  if (input.action === "add_bonus") body.token_amount = input.tokenAmount;
  if (input.action !== "restore_default") body.reason = input.reason ?? "";
  return apiRequest<UserQuotaActionResponse>("/admin/api/users/quota-actions", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify(body)
  });
}
