import { MoonOutlined, SunOutlined } from "@ant-design/icons";
import { App as AntApp, ConfigProvider, theme as antTheme } from "antd";
import zhCN from "antd/locale/zh_CN";
import { createContext, useContext, useLayoutEffect, useMemo, useState } from "react";

export type ThemeMode = "light" | "dark";

export const THEME_STORAGE_KEY = "cpa-ui-theme";
export const LEGACY_THEME_STORAGE_KEY = "cpa-admin-theme";

type ThemeContextValue = {
  theme: ThemeMode;
  setTheme: (theme: ThemeMode) => void;
  toggleTheme: () => void;
};

const ThemeContext = createContext<ThemeContextValue>({
  theme: "light",
  setTheme: () => undefined,
  toggleTheme: () => undefined
});

export function resolveInitialTheme(): ThemeMode {
  try {
    const stored = window.localStorage.getItem(THEME_STORAGE_KEY)
      || window.localStorage.getItem(LEGACY_THEME_STORAGE_KEY);
    if (stored === "light" || stored === "dark") return stored;
  } catch {
    // Storage can be unavailable in hardened or private browser contexts.
  }
  return window.matchMedia?.("(prefers-color-scheme: dark)").matches ? "dark" : "light";
}

function applyDocumentTheme(theme: ThemeMode) {
  document.documentElement.dataset.theme = theme;
  document.documentElement.style.colorScheme = theme;
  const favicon = document.querySelector<HTMLLinkElement>('link[rel~="icon"]');
  if (favicon?.href.includes("codex-cpa-cluster-favicon")) {
    favicon.href = `/portal/assets/codex-cpa-cluster-favicon${theme === "dark" ? "-dark" : ""}.svg`;
  }
}

export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [theme, setThemeState] = useState<ThemeMode>(() => {
    const initial = resolveInitialTheme();
    applyDocumentTheme(initial);
    return initial;
  });

  useLayoutEffect(() => applyDocumentTheme(theme), [theme]);

  const value = useMemo<ThemeContextValue>(() => ({
    theme,
    setTheme: (nextTheme) => {
      try { window.localStorage.setItem(THEME_STORAGE_KEY, nextTheme); } catch { /* ignore unavailable storage */ }
      setThemeState(nextTheme);
    },
    toggleTheme: () => {
      const nextTheme = theme === "dark" ? "light" : "dark";
      try { window.localStorage.setItem(THEME_STORAGE_KEY, nextTheme); } catch { /* ignore unavailable storage */ }
      setThemeState(nextTheme);
    }
  }), [theme]);

  const componentTheme = useMemo(() => ({
    algorithm: theme === "dark" ? antTheme.darkAlgorithm : antTheme.defaultAlgorithm,
    token: {
      colorPrimary: theme === "dark" ? "#8d9bf1" : "#6374d8",
      colorInfo: theme === "dark" ? "#8d9bf1" : "#6374d8",
      colorSuccess: theme === "dark" ? "#73d3a5" : "#3f8f67",
      colorWarning: theme === "dark" ? "#e0b55f" : "#9b6718",
      colorError: theme === "dark" ? "#ef8e94" : "#b5474f",
      colorBgBase: theme === "dark" ? "#0d121c" : "#f3f6fb",
      colorBgContainer: theme === "dark" ? "#151b28" : "#ffffff",
      colorTextBase: theme === "dark" ? "#edf1fb" : "#1d2437",
      colorBorder: theme === "dark" ? "#263146" : "#e3e8f1",
      borderRadius: 9,
      fontFamily: '"Geist", "Plus Jakarta Sans", "Avenir Next", "Segoe UI", "PingFang SC", sans-serif'
    }
  }), [theme]);

  return (
    <ThemeContext.Provider value={value}>
      <ConfigProvider locale={zhCN} theme={componentTheme}>
        <AntApp>{children}</AntApp>
      </ConfigProvider>
    </ThemeContext.Provider>
  );
}

export function useTheme() {
  return useContext(ThemeContext);
}

export function ThemeToggle({ className = "" }: { className?: string }) {
  const { theme, toggleTheme } = useTheme();
  const nextLabel = theme === "dark" ? "浅色" : "深色";
  return (
    <button
      className={`theme-toggle ${className}`.trim()}
      type="button"
      aria-label={`切换为${nextLabel}主题`}
      title="切换主题"
      onClick={toggleTheme}
    >
      <span className="theme-toggle-icon" aria-hidden="true">
        {theme === "dark" ? <SunOutlined /> : <MoonOutlined />}
      </span>
      <span className="theme-toggle-label">{nextLabel}</span>
    </button>
  );
}
