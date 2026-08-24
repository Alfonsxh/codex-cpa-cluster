import { apiRequest } from "./client";
import type { NotificationSettings, NotificationStatus, NotificationValues } from "./generated";

export type { NotificationSettings, NotificationStatus, NotificationValues } from "./generated";

export const notificationSettingsQueryKey = ["notification-settings"] as const;

export function readNotificationSettings(signal?: AbortSignal): Promise<NotificationSettings> {
  return apiRequest<NotificationSettings>("/admin/api/settings/notifications", { signal });
}

export function saveNotificationSettings(values: NotificationValues, csrfToken: string) {
  return apiRequest<NotificationSettings & { message: string }>("/admin/api/settings/notifications", {
    method: "PUT",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ confirm: "save", values })
  });
}

export function saveNotificationWebhook(webhookUrl: string, csrfToken: string) {
  return apiRequest<{ message: string; notifications: NotificationStatus }>(
    "/admin/api/settings/notification-webhook",
    {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken },
      body: JSON.stringify({ confirm: "save", webhook_url: webhookUrl })
    }
  );
}

export function clearNotificationWebhook(csrfToken: string) {
  return apiRequest<{ message: string; notifications: NotificationStatus }>(
    "/admin/api/settings/notification-webhook/clear",
    {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken },
      body: JSON.stringify({ confirm: "clear" })
    }
  );
}

export function sendNotification(csrfToken: string) {
  return apiRequest<{ message: string; format: "markdown_v2" }>("/admin/api/notifications/send", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({})
  });
}

export function testNotification(csrfToken: string) {
  return apiRequest<{ message: string; format: "markdown_v2" }>("/admin/api/notifications/test", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({})
  });
}
