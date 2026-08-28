import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Popover } from "antd";
import { lazy, Suspense, useCallback, useEffect, useRef } from "react";
import { useState } from "react";
import { Link, Navigate, Route, Routes, useLocation } from "react-router-dom";

import { ApiError, subscribeUnauthorized } from "../api/client";
import { readReleaseStatus, type ReleaseStatus } from "../api/overview";
import { logout, readSession, sessionQueryKey } from "../api/session";
import { AdminToolbarContext } from "./AdminToolbarContext";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { LoginPage } from "./LoginPage";
import { ThemeToggle, useTheme } from "./ThemeProvider";

const AccountsPage = lazy(() => import("./AccountsPage").then((module) => ({ default: module.AccountsPage })));
const OverviewPage = lazy(() => import("./OverviewPage").then((module) => ({ default: module.OverviewPage })));
const TeamsPage = lazy(() => import("./TeamsPage").then((module) => ({ default: module.TeamsPage })));
const UsersPage = lazy(() => import("./UsersPage").then((module) => ({ default: module.UsersPage })));
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
    if (event.scope === "admin" && event.path !== "/admin/api/session") {
      expireSession("管理会话已失效，请重新输入管理密钥");
    }
  }), [expireSession]);
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
        <Routes>
          <Route path="/overview" element={<OverviewPage />} />
          <Route path="/accounts" element={<AccountsPage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/users" element={<UsersPage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/teams" element={<TeamsPage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/notifications" element={<Navigate to="/configuration" replace />} />
          <Route path="/runtime" element={<RuntimePage csrfToken={session.data.csrf_token ?? ""} />} />
          <Route path="/configuration" element={<ConfigurationPage csrfToken={session.data.csrf_token ?? ""} onManagementKeyRotated={(message) => expireSession(message)} />} />
          <Route path="/settings" element={<Navigate to="/configuration" replace />} />
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
    return { eyebrow: "CONTROL PLANE SETTINGS", title: "系统设置" };
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
  const queryClient = useQueryClient();
  const { toasts, showToast } = useLegacyToasts();
  const { theme } = useTheme();
  const location = useLocation();
  const page = currentAdminPage(location.pathname);
  const selectedPath = currentNavigationPath(location.pathname);
  const navigationRef = useRef<HTMLElement>(null);
  const refreshActionRef = useRef<(() => Promise<void>) | null>(null);
  const [pageRefreshing, setPageRefreshing] = useState(false);
  const [manualRefreshing, setManualRefreshing] = useState(false);
  const [refreshLabel, setRefreshLabel] = useState("等待刷新");
  const [pageDetail, setPageDetail] = useState<{ title: string; eyebrow: string } | null>(null);
  const setRefreshAction = useCallback((action: (() => Promise<void>) | null) => {
    refreshActionRef.current = action;
  }, []);
  const releaseStatus = useQuery({
    queryKey: ["admin-release-status"],
    queryFn: ({ signal }) => readReleaseStatus(false, signal),
    retry: false,
    refetchInterval: 15 * 60 * 1_000,
    refetchOnWindowFocus: false
  });
  const releaseCheck = useMutation({
    mutationFn: () => readReleaseStatus(true),
    onSuccess: (status) => {
      queryClient.setQueryData(["admin-release-status"], status);
      showToast(status.status === "ok"
        ? status.available ? "检测到可用新版本" : "当前已是最新版本"
        : "版本检查暂不可用，请稍后重试", status.status === "ok" ? "success" : "error");
    },
    onError: (error) => showToast(error instanceof Error ? error.message : "版本检查暂不可用，请稍后重试", "error")
  });
  useEffect(() => {
    const navigation = navigationRef.current;
    const selectedItem = navigation?.querySelector<HTMLElement>('[aria-current="page"]');
    if (!navigation || !selectedItem || navigation.scrollWidth <= navigation.clientWidth) return;
    selectedItem.scrollIntoView({ block: "nearest", inline: "center", behavior: "auto" });
  }, [selectedPath]);
  useEffect(() => setRefreshLabel("等待刷新"), [selectedPath]);
  useEffect(() => setPageDetail(null), [selectedPath]);
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
          <span>管理 API 已鉴权</span>
          <ReleaseVersionIndicator
            className="side-nav-release"
            status={releaseStatus.data}
            checking={releaseCheck.isPending}
            onCheck={() => releaseCheck.mutate()}
          />
        </div>
      </aside>
      <main className="main-surface">
        <header className="top-bar">
          <div className="top-bar-heading">
            <h1>
              <span>{page.title}</span>
              {pageDetail ? <span className="page-heading-path"><span className="page-heading-separator" aria-hidden="true">/</span><span>{pageDetail.title}</span></span> : null}
            </h1>
            <span className="eyebrow">
              <span>{page.eyebrow}</span>
              {pageDetail ? <span className="page-heading-path"><span className="page-heading-separator" aria-hidden="true">/</span><span>{pageDetail.eyebrow}</span></span> : null}
            </span>
          </div>
          <div className="top-bar-actions">
            <span className="top-bar-refresh-state">{refreshing ? "正在刷新" : refreshLabel}</span>
            <ReleaseVersionIndicator
              className="mobile-release-indicator"
              status={releaseStatus.data}
              checking={releaseCheck.isPending}
              onCheck={() => releaseCheck.mutate()}
              showCurrent={false}
            />
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
        <AdminToolbarContext.Provider value={{
          setRefreshing: setPageRefreshing,
          setRefreshLabel,
          setRefreshAction,
          setPageDetail
        }}>
          {children}
        </AdminToolbarContext.Provider>
        <LegacyToastRegion toasts={toasts} />
      </main>
    </div>
  );
}

function ReleaseVersionIndicator({
  className,
  status,
  checking,
  onCheck,
  showCurrent = true
}: {
  className?: string;
  status?: ReleaseStatus;
  checking: boolean;
  onCheck: () => void;
  showCurrent?: boolean;
}) {
  if (!status?.current_version) return null;
  if (!status.available) {
    return showCurrent
      ? <span className={["release-version-current", className].filter(Boolean).join(" ")}>当前版本 {status.current_version}</span>
      : null;
  }
  const content = (
    <section className="release-version-popover" aria-label="应用版本详情">
      <div><span>当前版本</span><strong>{status.current_version}</strong></div>
      <div><span>最新版本</span><strong>{status.latest_version || "未知"}</strong></div>
      <div><span>检查时间</span><time>{formatReleaseCheckTime(status.checked_at)}</time></div>
      <p>请由授权操作者在部署环境拉取并应用所选版本；此处只检查版本，不执行部署。</p>
      <button className="button release-check-button" type="button" disabled={checking} onClick={onCheck}>
        {checking ? "正在检查…" : "重新检查"}
      </button>
    </section>
  );
  return (
    <Popover content={content} placement="topLeft" trigger="click" arrow>
      <button className={["release-version-update", className].filter(Boolean).join(" ")} type="button" aria-label={`发现新版本 ${status.latest_version || "未知"}，查看详情`}>
        <span><strong>发现新版本</strong><b>{status.latest_version || "未知"}</b></span>
        <small>当前版本 {status.current_version}</small>
        <span className="release-version-arrow" aria-hidden="true">⌃</span>
      </button>
    </Popover>
  );
}

function formatReleaseCheckTime(timestamp?: number) {
  if (!timestamp) return "尚未记录";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false
  }).format(new Date(timestamp * 1000));
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
