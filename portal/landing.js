(() => {
  "use strict";

  const THEME_STORAGE_KEY = "cpa-ui-theme";
  const LEGACY_THEME_STORAGE_KEY = "cpa-admin-theme";
  const root = document.documentElement;
  const toggle = document.querySelector("#portal-theme-toggle");

  const storedTheme = () => {
    try {
      const value = window.localStorage.getItem(THEME_STORAGE_KEY)
        || window.localStorage.getItem(LEGACY_THEME_STORAGE_KEY);
      return value === "light" || value === "dark" ? value : "";
    } catch {
      return "";
    }
  };

  const preferredTheme = () => storedTheme()
    || (window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light");

  const applyTheme = (theme, persist = false) => {
    const resolved = theme === "dark" ? "dark" : "light";
    root.dataset.theme = resolved;
    root.style.colorScheme = resolved;
    const nextTheme = resolved === "dark" ? "light" : "dark";
    if (toggle) {
      toggle.setAttribute("aria-label", `切换为${nextTheme === "dark" ? "深色" : "浅色"}主题`);
      toggle.querySelector(".portal-theme-toggle-icon").textContent = resolved === "dark" ? "☀" : "☾";
      toggle.querySelector(".portal-theme-toggle-label").textContent = resolved === "dark" ? "浅色" : "深色";
    }
    const favicon = document.querySelector("#portal-favicon");
    if (favicon) {
      favicon.href = `/portal/assets/codex-cpa-cluster-favicon${resolved === "dark" ? "-dark" : ""}.svg`;
    }
    if (persist) {
      try { window.localStorage.setItem(THEME_STORAGE_KEY, resolved); } catch { /* storage may be unavailable */ }
    }
    document.dispatchEvent(new CustomEvent("cpa-theme-change", { detail: { theme: resolved } }));
  };

  applyTheme(preferredTheme());
  toggle?.addEventListener("click", () => {
    applyTheme(root.dataset.theme === "dark" ? "light" : "dark", true);
  });
})();
