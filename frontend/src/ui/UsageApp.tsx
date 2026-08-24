import { Button, Result } from "antd";
import { LogoutOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { lazy, Suspense, useCallback } from "react";

import { ApiError } from "../api/client";
import {
  logoutPortal,
  portalSessionQueryKey,
  readPortalSession,
  type PortalSession
} from "../api/portal";
import { UsageLoginPage } from "./UsageLoginPage";
import { ThemeToggle, useTheme } from "./ThemeProvider";

const PortalPasswordModal = lazy(() => import("./PortalPasswordModal").then((module) => ({
  default: module.PortalPasswordModal
})));
const UsageDashboard = lazy(() => import("./UsageDashboard").then((module) => ({
  default: module.UsageDashboard
})));

export function UsageApp() {
  const queryClient = useQueryClient();
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
    queryClient.removeQueries({ queryKey: portalSessionQueryKey, exact: true });
    void queryClient.invalidateQueries({ queryKey: portalSessionQueryKey, exact: true });
  }, [queryClient]);

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
    >
      <Suspense fallback={<UsageLoading />}>
        {session.data.password_change_required ? (
          <section className="usage-password-required">
            <div>
              <span className="eyebrow">SECURITY CHECK</span>
              <h1>先设置你的个人密码</h1>
              <p>初始密码仅用于首次登录。完成修改后再加载 API Key 与个人用量。</p>
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
            <code>—</code>
            <div>
              {['复制 Key', '刷新 Key', 'Codex', 'Claude Code', '导入 CC Switch'].map((label) => (
                <button type="button" disabled key={label}>{label}</button>
              ))}
            </div>
          </div>
          <div className="usage-preview-stat-grid">
            <PreviewStat title="当前账号" value="尚未选择" detail="选择可用账号后显示" />
            <PreviewStat title="个人用量" value="—" detail="周额度正在读取…" />
            <PreviewStat title="今日 Token" value="—" detail="今日请求 —" />
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
          <div className="usage-preview-table-wrap">
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
          </div>
        </section>
      </main>
      <UsageLoginPage overlay />
    </div>
  );
}

function PreviewStat({ title, value, detail }: { title: string; value: string; detail: string }) {
  return (
    <article>
      <span>{title}</span>
      <strong>{value}</strong>
      <div className="usage-preview-progress"><i /></div>
      <small>{detail}</small>
    </article>
  );
}

function UsageShell({
  user,
  loggingOut,
  onLogout,
  children
}: {
  user: string;
  loggingOut: boolean;
  onLogout: () => void;
  children: React.ReactNode;
}) {
  return (
    <div className="usage-shell">
      <header className="usage-topbar">
        <a className="brand" href="/" aria-label="Codex CPA 使用中心">
          <span className="brand-mark">C</span>
          <span>
            <strong>Codex CPA</strong>
            <small>Usage center</small>
          </span>
        </a>
        <div className="usage-user-actions">
          <span>{user}</span>
          <ThemeToggle />
          <Button icon={<LogoutOutlined aria-hidden="true" />} onClick={onLogout} loading={loggingOut}>退出</Button>
        </div>
      </header>
      <main className="usage-main">{children}</main>
    </div>
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
