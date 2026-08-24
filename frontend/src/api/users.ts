import { apiRequest } from "./client";
import type {
  MessageResponse,
  UserCatalog,
  UserCreateResponse,
  UserKeyRotationResponse,
  UserPasswordResetResponse,
  UserQuotaMode,
  UserQuotaResult
} from "./generated";

export type {
  UserCatalog,
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
    params.page,
    params.pageSize
  ] as const;
}

export function listUsers(params: UserListParams): Promise<UserCatalog> {
  const query = new URLSearchParams({
    page: String(params.page),
    page_size: String(params.pageSize)
  });
  if (params.query) query.set("q", params.query);
  if (params.teamId) query.set("team_id", params.teamId);
  return apiRequest<UserCatalog>(`/admin/api/users?${query.toString()}`);
}

export function createUser(email: string, teamId: string | null, csrfToken: string) {
  return apiRequest<UserCreateResult>("/admin/api/users", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ email, team_id: teamId })
  });
}

export function rotateUserKey(email: string, csrfToken: string) {
  return apiRequest<UserKeyRotationResult>("/admin/api/keys/rotate", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ email, confirm: "rotate" })
  });
}

export function resetUserPassword(email: string, csrfToken: string) {
  return apiRequest<UserPasswordResetResult>("/admin/api/users/reset-password", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ email, confirm: "reset" })
  });
}

export function revokeUser(email: string, csrfToken: string) {
  return apiRequest<UserActionResult>("/admin/api/users/revoke", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ email, confirm: "revoke" })
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

export function readUserQuota(email: string) {
  const query = new URLSearchParams({ email });
  return apiRequest<UserQuotaResult>(`/admin/api/users/quota?${query.toString()}`);
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
  expectedTeamId: string | null,
  csrfToken: string
) {
  return apiRequest<{ message: string }>("/admin/api/users/team", {
    method: "PUT",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({
      email,
      team_id: teamId,
      expected_team_id: expectedTeamId
    })
  });
}

export function assignUsersTeam(users: string[], teamId: string | null, csrfToken: string) {
  return apiRequest<{ message: string }>("/admin/api/users/team/batch", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ users, team_id: teamId })
  });
}
