import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useCallback, useEffect, useRef } from "react";
import { useState } from "react";
import { Link, Navigate, Route, Routes, useLocation } from "react-router-dom";

import { ApiError, subscribeUnauthorized } from "../api/client";
import { onboardingQueryKey, readOnboarding } from "../api/onboarding";
import { defaultPublicSiteConfiguration, publicSiteQueryKey, readPublicSiteConfiguration } from "../api/public-site";
import { logout, readSession, refreshSession, sessionQueryKey } from "../api/session";
import { applicationHref } from "../application-links";
import { AdminToolbarContext, type AdminPageDetail } from "./AdminToolbarContext";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { ReleaseVersionIndicator } from "./components/ReleaseVersionIndicator";
import { LoginPage } from "./LoginPage";
import { ThemeToggle, useTheme } from "./ThemeProvider";

const AccountsPage = lazy(() => import("./AccountsPage").then((module) => ({ default: module.AccountsPage })));
const OverviewPage = lazy(() => import("./OverviewPage").then((module) => ({ default: module.OverviewPage })));
const TeamsPage = lazy(() => import("./TeamsPage").then((module) => ({ default: module.TeamsPage })));
const UsersPage = lazy(() => import("./UsersPage").then((module) => ({ default: module.UsersPage })));
const ConfigurationPage = lazy(() => import("./ConfigurationPage").then((module) => ({ default: module.ConfigurationPage })));
const RuntimePage = lazy(() => import("./RuntimePage").then((module) => ({ default: module.RuntimePage })));
const OnboardingPage = lazy(() => import("./OnboardingPage").then((module) => ({ default: module.OnboardingPage })));

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
    if (event.scope === "admin" && event.path !== "/admin/api/session") {
      expireSession(adminSessionNotice(event.code, event.message));
    }
  }), [expireSession]);
  useEffect(() => {
    const csrfToken = session.data?.csrf_token;
    if (!csrfToken) return;
    let refreshPending = false;
    let lastRefreshAt = 0;
    const refreshInterval = 5 * 60 * 1_000;
    const onActivity = (event: PointerEvent | KeyboardEvent) => {
      if (!event.isTrusted || (event instanceof KeyboardEvent && event.repeat)) return;
      const now = Date.now();
      if (refreshPending || now - lastRefreshAt < refreshInterval) return;
      refreshPending = true;
      lastRefreshAt = now;
      void refreshSession(csrfToken)
        .then((payload) => queryClient.setQueryData(sessionQueryKey, payload))
        .catch(() => undefined)
        .finally(() => { refreshPending = false; });
    };
    window.addEventListener("pointerdown", onActivity, { passive: true });
    window.addEventListener("keydown", onActivity);
    return () => {
      window.removeEventListener("pointerdown", onActivity);
      window.removeEventListener("keydown", onActivity);
    };
  }, [queryClient, session.data?.csrf_token]);
  if (session.isPending) {
    return <AppLoading />;
  }
  if (session.error instanceof ApiError && session.error.status === 401) {
    return <LoginPage notice={loginNotice} onAuthenticated={() => setLoginNotice("")} />;
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
        <AuthenticatedRoutes
          csrfToken={session.data.csrf_token ?? ""}
          onManagementKeyRotated={(message) => expireSession(message)}
        />
      </Suspense>
    </AdminShell>
  );
}

function adminSessionNotice(code: string, fallback: string) {
  if (code === "session_expired") return "管理会话已过期，请重新输入管理密钥";
  if (code === "session_invalidated") return "管理密钥已更新，请重新输入管理密钥";
  if (code === "session_missing") return "管理会话已结束，请重新输入管理密钥";
  return fallback || "管理会话已失效，请重新输入管理密钥";
}

function AuthenticatedRoutes({
  csrfToken,
  onManagementKeyRotated
}: {
  csrfToken: string;
  onManagementKeyRotated: (message: string) => void;
}) {
  const location = useLocation();
  const onboarding = useQuery({
    queryKey: onboardingQueryKey,
    queryFn: ({ signal }) => readOnboarding(signal),
    staleTime: 30_000,
    retry: false,
    refetchOnWindowFocus: false
  });
  const autoRedirected = useRef(false);
  useEffect(() => {
    if (location.pathname.startsWith("/setup")) autoRedirected.current = true;
  }, [location.pathname]);
  if (
    onboarding.data
    && !onboarding.data.required_complete
    && !location.pathname.startsWith("/setup")
    && !autoRedirected.current
  ) {
    autoRedirected.current = true;
    return <Navigate to="/setup" replace />;
  }
  return (
    <Routes>
      <Route path="/setup" element={<OnboardingPage csrfToken={csrfToken} />} />
      <Route path="/overview" element={<OverviewPage />} />
      <Route path="/accounts" element={<AccountsPage csrfToken={csrfToken} />} />
      <Route path="/users" element={<UsersPage csrfToken={csrfToken} />} />
      <Route path="/teams" element={<TeamsPage csrfToken={csrfToken} />} />
      <Route path="/notifications" element={<Navigate to="/configuration" replace />} />
      <Route path="/runtime" element={<RuntimePage csrfToken={csrfToken} />} />
      <Route path="/configuration" element={<ConfigurationPage csrfToken={csrfToken} onManagementKeyRotated={onManagementKeyRotated} />} />
      <Route path="/settings" element={<Navigate to="/configuration" replace />} />
      <Route path="*" element={<Navigate to="/overview" replace />} />
    </Routes>
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
  if (pathname.startsWith("/setup")) return { eyebrow: "GETTING STARTED", title: "首次设置" };
  if (pathname.startsWith("/configuration") || pathname.startsWith("/settings") || pathname.startsWith("/notifications")) {
    return { eyebrow: "CONTROL PLANE SETTINGS", title: "配置中心" };
  }
  if (pathname.startsWith("/runtime")) return { eyebrow: "STACK CONTROL", title: "运行维护" };
  if (pathname.startsWith("/teams")) return { eyebrow: "TEAM MANAGEMENT", title: "团队管理" };
  if (pathname.startsWith("/users")) return { eyebrow: "USER MANAGEMENT", title: "用户管理" };
  if (pathname.startsWith("/accounts")) return { eyebrow: "ACCOUNT MANAGEMENT", title: "账号管理" };
  return { eyebrow: "OPERATIONS OVERVIEW", title: "运行总览" };
}

function currentNavigationPath(pathname: string) {
  if (pathname.startsWith("/setup")) return "";
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
  const queryClient = useQueryClient();
  const { toasts, showToast } = useLegacyToasts();
  const { theme } = useTheme();
  const publicSite = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    retry: 1,
    refetchOnWindowFocus: true
  });
  const productName = publicSite.data?.product_name ?? defaultPublicSiteConfiguration.product_name;
  const location = useLocation();
  const isOnboarding = location.pathname.startsWith("/setup");
  const page = currentAdminPage(location.pathname);
  const selectedPath = currentNavigationPath(location.pathname);
  const navigationRef = useRef<HTMLElement>(null);
  const refreshActionRef = useRef<(() => Promise<void>) | null>(null);
  const [pageRefreshing, setPageRefreshing] = useState(false);
  const [manualRefreshing, setManualRefreshing] = useState(false);
  const [refreshLabel, setRefreshLabel] = useState("等待刷新");
  const [pageDetail, setPageDetail] = useState<AdminPageDetail | null>(null);
  const setRefreshAction = useCallback((action: (() => Promise<void>) | null) => {
    refreshActionRef.current = action;
  }, []);
  useEffect(() => {
    const navigation = navigationRef.current;
    const selectedItem = navigation?.querySelector<HTMLElement>('[aria-current="page"]');
    if (!navigation || !selectedItem || navigation.scrollWidth <= navigation.clientWidth) return;
    selectedItem.scrollIntoView({ block: "nearest", inline: "center", behavior: "auto" });
  }, [selectedPath]);
  useEffect(() => setRefreshLabel("等待刷新"), [selectedPath]);
  useEffect(() => setPageDetail(null), [selectedPath]);
  const visiblePageDetail = selectedPath === "/configuration" ? pageDetail : null;
  const refreshActivePage = async () => {
    if (manualRefreshing) return;
    setManualRefreshing(true);
    try {
      if (refreshActionRef.current) await refreshActionRef.current();
      else await queryClient.refetchQueries({ type: "active" });
    } catch (error) {
      showToast(error instanceof Error ? error.message : "刷新失败，请稍后重试", "error");
    } finally {
      setManualRefreshing(false);
    }
  };
  const refreshing = pageRefreshing || manualRefreshing;
  const toolbar = {
    setRefreshing: setPageRefreshing,
    setRefreshLabel,
    setRefreshAction,
    setPageDetail
  };
  if (isOnboarding) {
    return (
      <div className="onboarding-app-shell">
        <AdminToolbarContext.Provider value={toolbar}>
          {children}
        </AdminToolbarContext.Provider>
      </div>
    );
  }
  return (
    <div className="app-shell">
      <aside className="side-nav" aria-label="管理中心导航">
        <Link className="brand side-nav-brand" to="/overview" aria-label={`${productName} 管理中心`}>
          <span className="brand-mark">
            <img
              src={`/portal/assets/codex-cpa-pool-mark${theme === "dark" ? "-dark" : ""}.svg`}
              alt=""
            />
          </span>
          <span className="brand-copy">
            <strong title={productName}>{productName}</strong>
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
            <a href={applicationHref("portal")}>
              <span className="side-nav-switcher-index">01</span>
              <span className="side-nav-switcher-copy"><strong>服务入口</strong><small>返回界面选择</small></span>
              <span className="side-nav-switcher-arrow" aria-hidden="true">›</span>
            </a>
            <a href={applicationHref("usage")}>
              <span className="side-nav-switcher-index">02</span>
              <span className="side-nav-switcher-copy"><strong>使用中心</strong><small>Key、账号与用量</small></span>
              <span className="side-nav-switcher-arrow" aria-hidden="true">›</span>
            </a>
          </div>
        </section>
        <div className="side-nav-footer">
          <div className="side-nav-auth-status">
            <span className="status-dot" aria-hidden="true" />
            <span>管理 API 已鉴权</span>
          </div>
          <span className="side-nav-footer-separator" aria-hidden="true">|</span>
          <ReleaseVersionIndicator className="side-nav-release" />
        </div>
      </aside>
      <main className="main-surface">
        <header className="top-bar">
          <div className="top-bar-heading">
            <h1>
              <span>{page.title}</span>
              {visiblePageDetail ? <span className="page-heading-path"><span className="page-heading-separator" aria-hidden="true">/</span><span>{visiblePageDetail.title}</span></span> : null}
              {visiblePageDetail?.sectionTitle ? <span className="page-heading-path page-heading-section"><span className="page-heading-separator" aria-hidden="true">/</span><span>{visiblePageDetail.sectionTitle}</span></span> : null}
            </h1>
            <span className="eyebrow">
              <span>{page.eyebrow}</span>
              {visiblePageDetail ? <span className="page-heading-path"><span className="page-heading-separator" aria-hidden="true">/</span><span>{visiblePageDetail.eyebrow}</span></span> : null}
            </span>
          </div>
          <div className="top-bar-actions">
            <span className="top-bar-refresh-state">{refreshing ? "正在刷新" : refreshLabel}</span>
            <ThemeToggle />
            <button
              className="button button-quiet top-bar-refresh"
              type="button"
              disabled={manualRefreshing}
              onClick={() => void refreshActivePage()}
            >
              刷新
            </button>
            <button className="button button-quiet top-bar-logout" type="button" onClick={onLogout} disabled={loggingOut}>
              {loggingOut ? "正在退出…" : "退出"}
            </button>
          </div>
        </header>
        <AdminToolbarContext.Provider value={toolbar}>
          {children}
        </AdminToolbarContext.Provider>
        <LegacyToastRegion toasts={toasts} />
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
