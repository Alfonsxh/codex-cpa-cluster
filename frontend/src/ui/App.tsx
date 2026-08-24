import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useCallback, useEffect, useRef } from "react";
import { useState } from "react";
import { Link, Navigate, Route, Routes, useLocation } from "react-router-dom";

import { ApiError, subscribeUnauthorized } from "../api/client";
import { logout, readSession, sessionQueryKey } from "../api/session";
import { AdminToolbarContext } from "./AdminToolbarContext";
import { LoginPage } from "./LoginPage";
import { ThemeToggle, useTheme } from "./ThemeProvider";

const AccountsPage = lazy(() => import("./AccountsPage").then((module) => ({ default: module.AccountsPage })));
const OverviewPage = lazy(() => import("./OverviewPage").then((module) => ({ default: module.OverviewPage })));
const TeamsPage = lazy(() => import("./TeamsPage").then((module) => ({ default: module.TeamsPage })));
const UsersPage = lazy(() => import("./UsersPage").then((module) => ({ default: module.UsersPage })));
const NotificationSettingsPage = lazy(() => import("./NotificationSettingsPage").then((module) => ({ default: module.NotificationSettingsPage })));
const GeneralSettingsPage = lazy(() => import("./GeneralSettingsPage").then((module) => ({ default: module.GeneralSettingsPage })));
const ConfigurationPage = lazy(() => import("./ConfigurationPage").then((module) => ({ default: module.ConfigurationPage })));
const RuntimePage = lazy(() => import("./RuntimePage").then((module) => ({ default: module.RuntimePage })));

export function App() {
  const queryClient = useQueryClient();
  const [loginNotice, setLoginNotice] = useState("");
  const session = useQuery({
    queryKey: sessionQueryKey,
    queryFn: ({ signal }) => readSession(signal),
    retry: false,
    refetchOnWindowFocus: false
  });

  const logoutMutation = useMutation({
    mutationFn: () => logout(session.data?.csrf_token ?? ""),
    onSettled: () => {
      queryClient.clear();
      void queryClient.invalidateQueries({ queryKey: sessionQueryKey, exact: true });
    }
  });
  const expireSession = useCallback((notice = "") => {
    if (notice) setLoginNotice(notice);
    queryClient.removeQueries({
      predicate: (query) => query.queryKey[0] !== sessionQueryKey[0]
    });
    void queryClient.resetQueries({ queryKey: sessionQueryKey, exact: true });
  }, [queryClient]);
  useEffect(() => subscribeUnauthorized((event) => {
    if (event.scope === "admin" && event.path !== "/admin/api/session") expireSession();
  }), [expireSession]);
  useEffect(() => {
    if (session.data?.authenticated) setLoginNotice("");
  }, [session.data?.authenticated]);

  if (session.isPending) {
    return <AppLoading />;
  }
  if (session.error instanceof ApiError && session.error.status === 401) {
    return <LoginPage notice={loginNotice} />;
  }
  if (session.isError || !session.data?.authenticated) {
    return (
      <CenteredState
        title="管理服务暂时不可用"
        detail={session.error instanceof Error ? session.error.message : "无法确认管理会话"}
        actionLabel="重试"
        onAction={() => void session.refetch()}
      />
    );
  }

  return (
    <AdminShell
      loggingOut={logoutMutation.isPending}
      onLogout={() => logoutMutation.mutate()}
    >
      <Suspense fallback={<PageLoading />}>
        <Routes>
          <Route path="/overview" element={<OverviewPage />} />
          <Route path="/accounts" element={<AccountsPage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/users" element={<UsersPage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/teams" element={<TeamsPage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/notifications" element={<NotificationSettingsPage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/runtime" element={<RuntimePage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/configuration" element={<ConfigurationPage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route
            path="/settings"
            element={(
              <GeneralSettingsPage
                csrfToken={session.data.csrf_token ?? ""}
                onManagementKeyRotated={(message) => expireSession(message)}
              />
            )}
          />
          <Route path="*" element={<Navigate to="/overview" replace />} />
        </Routes>
      </Suspense>
    </AdminShell>
  );
}

type AdminPage = {
  eyebrow: string;
  title: string;
};

const adminNavigation = [
  { to: "/overview", icon: "⌂", label: "运行总览" },
  { to: "/accounts", icon: "▣", label: "账号管理" },
  { to: "/users", icon: "◎", label: "用户管理" },
  { to: "/teams", icon: "◇", label: "团队管理" },
  { to: "/runtime", icon: "⌘", label: "运行维护" },
  { to: "/configuration", icon: "⚙", label: "配置中心" }
] as const;

function currentAdminPage(pathname: string): AdminPage {
  if (pathname.startsWith("/configuration") || pathname.startsWith("/settings") || pathname.startsWith("/notifications")) {
    return { eyebrow: "CONFIGURATION CENTER", title: "配置中心" };
  }
  if (pathname.startsWith("/runtime")) return { eyebrow: "STACK CONTROL", title: "运行维护" };
  if (pathname.startsWith("/teams")) return { eyebrow: "TEAM MANAGEMENT", title: "团队管理" };
  if (pathname.startsWith("/users")) return { eyebrow: "USER MANAGEMENT", title: "用户管理" };
  if (pathname.startsWith("/accounts")) return { eyebrow: "ACCOUNT MANAGEMENT", title: "账号管理" };
  return { eyebrow: "OPERATIONS OVERVIEW", title: "运行总览" };
}

function currentNavigationPath(pathname: string) {
  if (pathname.startsWith("/configuration") || pathname.startsWith("/settings") || pathname.startsWith("/notifications")) {
    return "/configuration";
  }
  return adminNavigation.find((item) => pathname.startsWith(item.to))?.to ?? "/overview";
}

export function AdminShell({
  children,
  loggingOut,
  onLogout
}: {
  children: React.ReactNode;
  loggingOut: boolean;
  onLogout: () => void;
}) {
  const { theme } = useTheme();
  const location = useLocation();
  const page = currentAdminPage(location.pathname);
  const selectedPath = currentNavigationPath(location.pathname);
  const navigationRef = useRef<HTMLElement>(null);
  const [overviewRefreshRevision, setOverviewRefreshRevision] = useState(0);
  const [overviewRefreshing, setOverviewRefreshing] = useState(false);
  useEffect(() => {
    const navigation = navigationRef.current;
    const selectedItem = navigation?.querySelector<HTMLElement>('[aria-current="page"]');
    if (!navigation || !selectedItem || navigation.scrollWidth <= navigation.clientWidth) return;
    selectedItem.scrollIntoView({ block: "nearest", inline: "center", behavior: "auto" });
  }, [selectedPath]);
  return (
    <div className="app-shell">
      <aside className="side-nav" aria-label="管理中心导航">
        <Link className="brand side-nav-brand" to="/overview" aria-label="Codex CPA 管理中心">
          <span className="brand-mark">
            <img
              src={`/portal/assets/codex-cpa-cluster-mark${theme === "dark" ? "-dark" : ""}.svg`}
              alt=""
            />
          </span>
          <span className="brand-copy">
            <strong>Codex CPA Cluster</strong>
            <small>Control Plane</small>
          </span>
        </Link>
        <nav ref={navigationRef} className="admin-nav" aria-label="主导航">
          {adminNavigation.map((item) => (
            <Link
              key={item.to}
              className={`admin-nav-item${selectedPath === item.to ? " active" : ""}`}
              to={item.to}
              aria-current={selectedPath === item.to ? "page" : undefined}
            >
              <span className="admin-nav-icon" aria-hidden="true">{item.icon}</span>
              <span>{item.label}</span>
            </Link>
          ))}
        </nav>
        <section className="side-nav-switcher" aria-label="界面切换">
          <div className="side-nav-switcher-heading"><span>界面切换</span><small>SWITCH</small></div>
          <div className="side-nav-switcher-links">
            <a href="/">
              <span className="side-nav-switcher-index">01</span>
              <span className="side-nav-switcher-copy"><strong>服务入口</strong><small>返回界面选择</small></span>
              <span className="side-nav-switcher-arrow" aria-hidden="true">›</span>
            </a>
            <a href="/usage/">
              <span className="side-nav-switcher-index">02</span>
              <span className="side-nav-switcher-copy"><strong>使用中心</strong><small>Key、账号与用量</small></span>
              <span className="side-nav-switcher-arrow" aria-hidden="true">›</span>
            </a>
          </div>
        </section>
        <div className="side-nav-footer">
          <span className="status-dot" aria-hidden="true" />
          管理 API 已鉴权
        </div>
      </aside>
      <main className="main-surface">
        <header className="top-bar">
          <div className="top-bar-heading">
            <h1>{page.title}</h1>
            <span className="eyebrow">{page.eyebrow}</span>
          </div>
          <div className="top-bar-actions">
            {selectedPath === "/overview" ? (
              <>
                <span className="top-bar-refresh-state">{overviewRefreshing ? "正在刷新" : "总览已更新"}</span>
                <button
                  className="button button-quiet top-bar-refresh"
                  type="button"
                  disabled={overviewRefreshing}
                  onClick={() => setOverviewRefreshRevision((revision) => revision + 1)}
                >
                  刷新
                </button>
              </>
            ) : null}
            <ThemeToggle />
            <button className="button button-quiet top-bar-logout" type="button" onClick={onLogout} disabled={loggingOut}>
              {loggingOut ? "正在退出…" : "退出"}
            </button>
          </div>
        </header>
        <AdminToolbarContext.Provider value={{
          refreshRevision: overviewRefreshRevision,
          setRefreshing: setOverviewRefreshing
        }}>
          {children}
        </AdminToolbarContext.Provider>
      </main>
    </div>
  );
}

function PageLoading() {
  return (
    <section className="page-content" aria-label="正在加载当前页面">
      <div className="skeleton skeleton-title" />
      <div className="skeleton skeleton-line" />
      <div className="skeleton skeleton-table" />
    </section>
  );
}

function AppLoading() {
  return (
    <div className="loading-shell" aria-label="正在加载管理中心">
      <div className="loading-brand" />
      <div className="loading-panel">
        <div className="skeleton skeleton-title" />
        <div className="skeleton skeleton-line" />
        <div className="skeleton skeleton-table" />
      </div>
    </div>
  );
}

export function CenteredState({
  title,
  detail,
  actionLabel,
  onAction
}: {
  title: string;
  detail: string;
  actionLabel: string;
  onAction: () => void;
}) {
  return (
    <main className="centered-state">
      <div className="state-symbol" aria-hidden="true">!</div>
      <h1>{title}</h1>
      <p>{detail}</p>
      <button className="button button-primary" type="button" onClick={onAction}>
        {actionLabel}
      </button>
    </main>
  );
}
