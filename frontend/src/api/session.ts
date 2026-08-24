import { apiRequest } from "./client";
import type { AdminSession } from "./generated";

export type { AdminSession } from "./generated";

export const sessionQueryKey = ["admin-session"] as const;

export function readSession(): Promise<AdminSession> {
  return apiRequest<AdminSession>("/admin/api/session");
}

export function login(managementKey: string): Promise<AdminSession> {
  return apiRequest<AdminSession>("/admin/api/session", {
    method: "POST",
    headers: { "X-Management-Key": managementKey }
  });
}

export function logout(csrfToken: string): Promise<{ logged_out: true }> {
  return apiRequest<{ logged_out: true }>("/admin/api/session", {
    method: "DELETE",
    headers: { "X-CSRF-Token": csrfToken }
  });
}
