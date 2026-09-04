import { Button, Result } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useCallback, useEffect, useState } from "react";

import { ApiError } from "../api/client";
import {
  logoutPortal,
  portalSessionQueryKey,
  readPortalSession,
  type PortalSession
} from "../api/portal";
import { UsageLoginPage } from "./UsageLoginPage";
import { ThemeToggle, useTheme } from "./ThemeProvider";
import { NativeTableViewport } from "./components/NativeTableViewport";

const PortalPasswordModal = lazy(() => import("./PortalPasswordModal").then((module) => ({
  default: module.PortalPasswordModal
})));
const UsageDashboard = lazy(() => import("./UsageDashboard").then((module) => ({
  default: module.UsageDashboard
})));

export function UsageApp() {
  const queryClient = useQueryClient();
  const [sessionExpired, setSessionExpired] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const session = useQuery({
    queryKey: portalSessionQueryKey,
    queryFn: ({ signal }) => readPortalSession(signal),
    retry: false,
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const logout = useMutation({
    mutationFn: logoutPortal,
    onSettled: () => {
      queryClient.clear();
      void queryClient.invalidateQueries({ queryKey: portalSessionQueryKey, exact: true });
    }
  });
  const expireSession = useCallback(() => {
    // A child query already proved the cookie is invalid. Clear every cached
    // management value immediately and show the authentication boundary
    // without waiting for a second session request to race the stale screen.
    queryClient.clear();
    setSessionExpired(true);
  }, [queryClient]);
  useEffect(() => {
    if (session.data?.authenticated) setSessionExpired(false);
  }, [session.data?.authenticated, session.dataUpdatedAt]);

  if (sessionExpired) {
    return <UsageAuthenticationBoundary />;
  }

  if (session.isPending) {
    return <UsageLoading />;
  }
  if (session.error instanceof ApiError && session.error.status === 401) {
    return <UsageAuthenticationBoundary />;
  }
  if (session.isError || !session.data?.authenticated) {
    return (
      <main className="centered-state">
        <Result
          status="warning"
          title="使用中心暂时不可用"
          subTitle={session.error instanceof Error ? session.error.message : "无法确认用户会话"}
          extra={<Button type="primary" onClick={() => void session.refetch()}>重试</Button>}
        />
      </main>
    );
  }

  const updateSession = (updates: Partial<PortalSession>) => {
    queryClient.setQueryData<PortalSession>(portalSessionQueryKey, (current) => current ? { ...current, ...updates } : current);
  };

  return (
    <UsageShell
      user={session.data.user}
      loggingOut={logout.isPending}
      onLogout={() => logout.mutate()}
      onChangePassword={() => setPasswordOpen(true)}
    >
      <Suspense fallback={<UsageLoading />}>
        {session.data.password_change_required ? (
          <section className="usage-password-required">
            <div>
              <span className="eyebrow">SECURITY CHECK</span>
              <h1>先设置你的个人密码</h1>
              <p>初始密码仅用于首次登录。完成修改后再加载 API Key 与个人周用量。</p>
            </div>
            <PortalPasswordModal
              open
              mandatory
              onClose={() => undefined}
              onSuccess={() => updateSession({ password_change_required: false })}
            />
          </section>
        ) : <UsageDashboard onSessionExpired={expireSession} />}
      </Suspense>
      <Suspense fallback={null}>
        <PortalPasswordModal
          open={passwordOpen}
          onClose={() => setPasswordOpen(false)}
          onSuccess={() => setPasswordOpen(false)}
        />
      </Suspense>
    </UsageShell>
  );
}

function UsageAuthenticationBoundary() {
  const { theme } = useTheme();
  return (
    <div className="usage-shell usage-authentication-boundary">
      <header className="usage-topbar usage-preview-topbar">
        <a className="usage-preview-brand" href="/" aria-label="返回 Codex CPA 首页">
          <img
            src={`/portal/assets/codex-cpa-cluster-logo${theme === "dark" ? "-dark" : ""}.svg`}
            alt="Codex CPA Cluster"
          />
          <strong>使用中心</strong>
        </a>
        <ThemeToggle />
      </header>
      <main className="usage-main usage-preview" aria-hidden="true">
        <section className="usage-preview-summary">
          <div className="usage-preview-key">
            <span>我的 API Key</span>
            <code>出于安全，仅在需要时读取</code>
            <div>
              {['管理 API Key', '配置 Codex', '配置 Claude Code', '导入 CC Switch'].map((label) => (
                <button type="button" disabled key={label}>{label}</button>
              ))}
            </div>
          </div>
          <div className="usage-preview-stat-grid">
            <PreviewStat title="当前账号" value="尚未选择" detail="选择可用账号后显示" />
            <PreviewStat title="个人周用量" value="—" detail="周额度正在读取…" />
            <PreviewStat title="今日 Token" value="—" />
          </div>
        </section>
        <section className="usage-preview-accounts">
          <div className="usage-preview-toolbar">
            <h2>账号明细</h2>
            <div>
              {['1 小时', '今日', '24 小时', '7 天', '刷新'].map((label) => (
                <button type="button" disabled key={label}>{label}</button>
              ))}
            </div>
          </div>
          <NativeTableViewport className="usage-preview-table-wrap" aria-label="账号明细加载预览">
            <table>
              <thead>
                <tr>
                  {['序号', '当前账号', 'CPA 账号', '账号周额度', '活跃用户', '账号状态', '我的请求', '我的 Token', '最后使用', '使用明细'].map((label) => <th key={label}>{label}</th>)}
                </tr>
              </thead>
              <tbody>
                {[0, 1, 2].map((row) => (
                  <tr key={row}>{Array.from({ length: 10 }, (_, column) => <td key={column}><span /></td>)}</tr>
                ))}
              </tbody>
            </table>
          </NativeTableViewport>
        </section>
      </main>
      <UsageLoginPage overlay />
    </div>
  );
}

function PreviewStat({ title, value, detail }: { title: string; value: string; detail?: string }) {
  return (
    <article>
      <span>{title}</span>
      <strong>{value}</strong>
      <div className="usage-preview-progress"><i /></div>
      {detail ? <small>{detail}</small> : null}
    </article>
  );
}

function UsageShell({
  user,
  loggingOut,
  onLogout,
  onChangePassword,
  children
}: {
  user: string;
  loggingOut: boolean;
  onLogout: () => void;
  onChangePassword: () => void;
  children: React.ReactNode;
}) {
  const { theme } = useTheme();
  return (
    <main className="usage-shell usage-center-shell">
      <header className="usage-center-head">
        <div className="usage-brand-block">
          <a href="/" aria-label="Codex CPA 使用中心">
            <img
              className="usage-brand-logo"
              src={`/portal/assets/codex-cpa-cluster-logo${theme === "dark" ? "-dark" : ""}.svg`}
              alt="Codex CPA Cluster"
            />
          </a>
          <h1>使用中心</h1>
        </div>
        <div className="usage-user-actions">
          <span className="usage-user-badge" title={user}>{user}</span>
          <button className="usage-link-button usage-password-action" type="button" aria-label="修改密码" onClick={onChangePassword}>
            <span className="usage-password-action-icon" aria-hidden="true">
              <svg viewBox="0 0 24 24" fill="none"><path d="M7 10V8a5 5 0 0 1 10 0v2M6 10h12v10H6zM12 14v2" /></svg>
            </span>
            <span className="usage-password-action-label">修改密码</span>
          </button>
          <ThemeToggle className="usage-theme-toggle" />
          <button className="usage-link-button" type="button" onClick={onLogout} disabled={loggingOut}>
            {loggingOut ? "退出中…" : "退出"}
          </button>
        </div>
      </header>
      <div className="usage-center-content">{children}</div>
    </main>
  );
}

function UsageLoading() {
  return (
    <div className="usage-loading" aria-label="正在加载使用中心">
      <div className="skeleton skeleton-title" />
      <div className="skeleton skeleton-line" />
      <div className="skeleton skeleton-table" />
    </div>
  );
}
