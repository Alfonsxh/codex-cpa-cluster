import { useSyncExternalStore } from "react";

// Used only before the public site configuration has loaded. Keep in sync with
// internal/sitetime.DefaultName; the server is authoritative after loading.
export const defaultSiteTimezone = "Asia/Shanghai";
let timezone = defaultSiteTimezone;
const listeners = new Set<() => void>();

export function getSiteTimezone() { return timezone; }

export function setSiteTimezone(value: string) {
  // Validate before publishing so a failed refresh preserves the last known zone.
  new Intl.DateTimeFormat("en-US", { timeZone: value }).format();
  if (value === timezone) return;
  timezone = value;
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => { listeners.delete(listener); };
}

export function useSiteTimezone() {
  return useSyncExternalStore(subscribe, getSiteTimezone, getSiteTimezone);
}

export function siteDateTimeFormat(locales?: Intl.LocalesArgument, options: Intl.DateTimeFormatOptions = {}) {
  return new Intl.DateTimeFormat(locales, { ...options, timeZone: options.timeZone || timezone });
}
