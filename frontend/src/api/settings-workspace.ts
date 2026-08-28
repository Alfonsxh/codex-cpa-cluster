import { apiRequest } from "./client";
import type { SettingsWorkspace } from "./generated";

export type { SettingsWorkspace } from "./generated";

export const settingsWorkspaceQueryKey = ["settings-workspace"] as const;

export function readSettingsWorkspace(signal?: AbortSignal): Promise<SettingsWorkspace> {
  return apiRequest<SettingsWorkspace>("/admin/api/settings/workspace", { signal });
}
