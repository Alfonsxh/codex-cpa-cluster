import { apiRequest } from "./client";
import type {
  Team,
  TeamInput as GeneratedTeamInput,
  TeamUpdateInput,
  TeamUsageBreakdownResponse,
  TeamUsageResponse,
  UsageWindow
} from "./generated";

export type {
  Team,
  TeamCombinationUsage,
  TeamUsageBreakdownResponse,
  TeamUsageRow,
  TeamUsageSeries,
  TeamUserUsage
} from "./generated";
export type TeamInput = GeneratedTeamInput & { id?: string };
export type TeamUsageRange = {
  window: UsageWindow;
  startAt?: number;
  endAt?: number;
  fresh?: boolean;
};

export const teamsQueryKey = ["teams"] as const;
export const teamUsageQueryRoot = ["teams", "usage"] as const;

export function listTeams(signal?: AbortSignal): Promise<{ teams: Team[] }> {
  return apiRequest<{ teams: Team[] }>("/admin/api/teams", { signal });
}

export function teamUsageQueryKey(range: TeamUsageRange) {
  return [
    ...teamUsageQueryRoot,
    range.window,
    range.startAt ?? null,
    range.endAt ?? null,
    Boolean(range.fresh)
  ] as const;
}

export function readTeamUsage(range: TeamUsageRange, signal?: AbortSignal): Promise<TeamUsageResponse> {
  return apiRequest<TeamUsageResponse>(`/admin/api/teams/usage?${teamUsageSearchParams(range).toString()}`, { signal });
}

export function readTeamUsageBreakdown(
  teamID: string,
  range: TeamUsageRange,
  signal?: AbortSignal
): Promise<TeamUsageBreakdownResponse> {
  const query = teamUsageSearchParams(range);
  query.set("team_id", teamID);
  return apiRequest<TeamUsageBreakdownResponse>(`/admin/api/teams/usage-breakdown?${query.toString()}`, { signal });
}

function teamUsageSearchParams(range: TeamUsageRange) {
  const query = new URLSearchParams({ window: range.window });
  if (range.window === "custom" && range.startAt !== undefined && range.endAt !== undefined) {
    query.set("start_at", String(range.startAt));
    query.set("end_at", String(range.endAt));
  }
  if (range.fresh) query.set("fresh", "1");
  return query;
}

export function createTeam(input: TeamInput, csrfToken: string) {
  return apiRequest<{ message: string; team: Team }>("/admin/api/teams", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify(input)
  });
}

export function updateTeam(input: TeamUpdateInput, csrfToken: string) {
  return apiRequest<{ message: string; team: Team }>("/admin/api/teams", {
    method: "PUT",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify(input)
  });
}

export function deleteTeam(id: string, csrfToken: string) {
  return apiRequest<{ message: string; team: Team }>(
    `/admin/api/teams?id=${encodeURIComponent(id)}`,
    {
      method: "DELETE",
      headers: { "X-CSRF-Token": csrfToken }
    }
  );
}
