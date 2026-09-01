import { apiRequest } from "./client";
import type { OnboardingStatus } from "./generated";

export type { OnboardingStatus, OnboardingStep } from "./generated";

export const onboardingQueryKey = ["admin-onboarding"] as const;

export function readOnboarding(signal?: AbortSignal): Promise<OnboardingStatus> {
  return apiRequest<OnboardingStatus>("/admin/api/onboarding", { signal });
}

export function saveOnboardingPreferences(
  preferences: { deferred: boolean; skippedRecommended: string[] },
  csrfToken: string
): Promise<OnboardingStatus> {
  return apiRequest<OnboardingStatus>("/admin/api/onboarding/preferences", {
    method: "PUT",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({
      confirm: "save",
      deferred: preferences.deferred,
      skipped_recommended: preferences.skippedRecommended
    })
  });
}
