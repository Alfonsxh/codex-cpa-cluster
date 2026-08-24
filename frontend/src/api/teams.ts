import { apiRequest } from "./client";
import type { Team, TeamInput as GeneratedTeamInput, TeamUpdateInput } from "./generated";

export type { Team } from "./generated";
export type TeamInput = GeneratedTeamInput & { id?: string };

export const teamsQueryKey = ["teams"] as const;

export function listTeams(signal?: AbortSignal): Promise<{ teams: Team[] }> {
  return apiRequest<{ teams: Team[] }>("/admin/api/teams", { signal });
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
