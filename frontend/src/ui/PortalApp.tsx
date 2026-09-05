import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { Link, Navigate, Route, Routes } from "react-router-dom";

import { ApiError } from "../api/client";
import { applicationHref } from "../application-links";
import {
  defaultPublicSiteConfiguration,
  listNativeAccounts,
  nativeAccountsQueryKey,
  publicSiteQueryKey,
  readPublicSiteConfiguration
} from "../api/public-site";
import type { NativeAccount, PublicSiteConfiguration } from "../api/public-site";
import { ThemeToggle, useTheme, type ThemeMode } from "./ThemeProvider";

export function PortalApp() {
  return (
    <Routes>
      <Route path="/" element={<PortalLandingApp />} />
      <Route path="/native/*" element={<NativeAccountsPage />} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  );
}

export function PortalLandingApp() {
  const branding = useBranding();
  const { theme } = useTheme();
  usePageTitle(`服务入口 · ${branding.configuration.product_name}`);

  return (
    <main className="portal-shell">
      <header className="portal-masthead">
        <section className="portal-hero" aria-labelledby="portal-title">
          <a className="portal-brand" href="/" aria-label={`${branding.configuration.product_name} 服务入口`}>
            <img
              src={brandLogoURL(branding.configuration, theme)}
              alt={branding.configuration.product_name}
            />
          </a>
          <h1 id="portal-title">选择要进入的界面</h1>
          <p className="portal-subtitle">添加和管理业务 CPA，或进入使用中心查看自己的 Key、账号与用量。</p>
        </section>
        <div className="portal-header-actions">
          <div className="portal-environment"><span aria-hidden="true" /><b>{branding.configuration.environment_label || "Self-hosted service"}</b></div>
          <ThemeToggle className="portal-theme-toggle" />
        </div>
      </header>

      <section className="portal-entry-grid" aria-label="可用界面">
        <EntryCard
          className="portal-entry-primary"
          href={applicationHref("admin")}
          number="01"
          badge="需要管理密钥"
          icon="control"
          eyebrow="CONTROL PLANE"
          title="综合管理平台"
          description="添加业务 CPA、管理用户与 Key、OAuth 授权、容器、日志和诊断任务。"
          action="进入管理平台"
        />
        <EntryCard
          href={applicationHref("usage")}
          number="02"
          badge="邮箱进入"
          icon="usage"
          eyebrow="ACCESS & OBSERVABILITY"
          title="使用中心"
          description="查看唯一 API Key，切换 CPA 账号，并统计各账号的请求和 Token 用量。"
          action="进入使用中心"
        />
      </section>
      <PortalFooter productName={branding.configuration.product_name} />
    </main>
  );
}

export function NativeAccountsPage() {
  const branding = useBranding();
  const accounts = useQuery({
    queryKey: nativeAccountsQueryKey,
    queryFn: ({ signal }) => listNativeAccounts(signal),
    retry: false,
    refetchOnWindowFocus: true
  });
  usePageTitle(`业务 CPA · ${branding.configuration.product_name}`);
  useNativeLightPresentation();

  const nativeAccounts = accounts.data?.accounts ?? [];
  const loginRequired = accounts.error instanceof ApiError && accounts.error.status === 401;
  const countLabel = accounts.isPending
    ? "正在读取"
    : loginRequired
      ? "需要管理员登录"
      : accounts.isError
        ? "列表不可用"
        : `${nativeAccounts.length} 个业务 CPA`;

  return (
    <main className="portal-shell native-page">
      <Link className="native-back" to="/">← 返回服务入口</Link>
      <section className="native-heading">
        <div>
          <p className="native-eyebrow">{branding.configuration.product_name} · BUSINESS ACCOUNTS</p>
          <h1>业务 CPA</h1>
          <p className="native-subtitle">每个上游账号对应一个独立 CPA。选择账号进入原生管理界面，管理员可以继续添加账号。</p>
        </div>
        <div className="native-environment"><span aria-hidden="true" /><b>{countLabel}</b></div>
      </section>

      <NativeAccountGrid accounts={nativeAccounts} />
      {accounts.isError ? (
        <div className="native-error" role="alert">
          <strong>{loginRequired ? "请先登录管理中心" : "业务 CPA 列表读取失败"}</strong>
          <span>
            {loginRequired
              ? "登录后返回本页，系统才会读取业务账号；公网不会返回原生端口。"
              : "请稍后重试，或进入管理中心检查服务状态。"}
            {!loginRequired ? (
              <button type="button" aria-label="重新读取" onClick={() => void accounts.refetch()}>重新读取 →</button>
            ) : null}
          </span>
        </div>
      ) : null}
      <section className="native-access-note">
        <div><p className="native-kicker">ACCESS CONTROL</p><h2>新增操作只允许管理员执行</h2></div>
        <span>账号信息保存在控制面数据库；公开页面不会展示管理密钥。</span>
      </section>
    </main>
  );
}

function NativeAccountGrid({ accounts }: { accounts: NativeAccount[] }) {
  return (
    <section className="native-grid" aria-label="业务 CPA 原生管理入口">
      {accounts.map((account, index) => <NativeAccountCard account={account} index={index} key={account.id} />)}
      <a href={applicationHref("admin", "?action=add-account")} className="native-card native-add-card" aria-label="添加业务 CPA">
        <div className="native-card-top"><span className="native-index">＋</span><span className="native-access native-access-guarded">仅管理员</span></div>
        <div>
          <p className="native-kicker">EXPAND ACCOUNT POOL</p>
          <h2>添加业务 CPA</h2>
          <p>验证管理密钥后填写账号标识与邮箱，系统自动分配端口、生成配置并启动容器。</p>
        </div>
        <div className="native-meta"><span>自动持久化</span><b>添加 →</b></div>
      </a>
    </section>
  );
}

function NativeAccountCard({ account, index }: { account: NativeAccount; index: number }) {
  const managementURL = safeManagementURL(account.management_url);
  const content = (
    <>
      <div className="native-card-top">
        <span className="native-index">{String(index + 1).padStart(2, "0")}</span>
        <span className="native-access native-access-public">业务 CPA</span>
      </div>
      <div>
        <p className="native-kicker">{account.id.toUpperCase()}</p>
        <h2>{account.id}</h2>
        <p>{managementURL ? "仅允许从部署主机访问" : "公网入口不开放原生管理端口"}</p>
      </div>
      <div className="native-meta">
        <span>{account.group_enabled ? "账号已启用" : "账号已停用"}</span>
        <b>{managementURL ? "打开 ↗" : "仅本机可访问"}</b>
      </div>
    </>
  );
  if (!managementURL) return <article className="native-card">{content}</article>;
  return <a href={managementURL} rel="noreferrer" className="native-card">{content}</a>;
}

function EntryCard({
  className = "", href, number, badge, icon, eyebrow, title, description, action
}: {
  className?: string;
  href: string;
  number: string;
  badge: string;
  icon: "control" | "usage";
  eyebrow: string;
  title: string;
  description: string;
  action: string;
}) {
  return (
    <a href={href} className={`portal-entry-card ${className}`.trim()}>
      <div className="portal-card-top"><span>{number}</span><span className={`portal-access ${className ? "guarded" : "public"}`}>{badge}</span></div>
      <div className="portal-card-content">
        <span className="portal-card-icon" aria-hidden="true">
          {icon === "control" ? (
            <svg viewBox="0 0 24 24" fill="none"><path d="M4 6.5h16M7 3.5v6M17 3.5v6M6 12h5v5H6zM14 12h4M14 16h4M4 20.5h16" /></svg>
          ) : (
            <svg viewBox="0 0 24 24" fill="none"><path d="M4 19.5V14m5 5.5V9m5 10.5V12m5 7.5V5M3 20.5h18" /></svg>
          )}
        </span>
        <p className="portal-eyebrow">{eyebrow}</p>
        <h2>{title}</h2>
        <p>{description}</p>
      </div>
      <span className="portal-enter"><span>{action}</span><b>→</b></span>
    </a>
  );
}

function PortalFooter({ productName }: { productName: string }) {
  return <footer className="portal-footer"><span>{productName}</span><span>选择界面后再执行对应操作</span></footer>;
}

function useBranding() {
  const query = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    retry: 1,
    refetchOnWindowFocus: true
  });
  return { configuration: query.data ?? defaultPublicSiteConfiguration, degraded: query.isError };
}

function useNativeLightPresentation() {
  useEffect(() => {
    const root = document.documentElement;
    const previousColorScheme = root.style.colorScheme;
    root.classList.add("native-page-active");
    root.style.colorScheme = "light";
    return () => {
      root.classList.remove("native-page-active");
      root.style.colorScheme = previousColorScheme;
    };
  }, []);
}

function usePageTitle(title: string) {
  useEffect(() => {
    document.title = title;
  }, [title]);
}

function brandLogoURL(configuration: PublicSiteConfiguration, theme: ThemeMode): string {
  if (!configuration.logo.custom || !configuration.logo.sha256) {
    if (theme === "dark" && configuration.logo.url.endsWith("codex-cpa-cluster-logo.svg")) {
      return configuration.logo.url.replace(/\.svg$/, "-dark.svg");
    }
    return configuration.logo.url;
  }
  const separator = configuration.logo.url.includes("?") ? "&" : "?";
  return `${configuration.logo.url}${separator}v=${encodeURIComponent(configuration.logo.sha256.slice(0, 16))}`;
}

export function safeManagementURL(value?: string): string | undefined {
  if (!value) return undefined;
  try {
    const parsed = new URL(value);
    const hostname = parsed.hostname.replace(/^\[|\]$/g, "").toLowerCase();
    const loopback = hostname === "localhost" || hostname === "::1" || /^127(?:\.\d{1,3}){3}$/.test(hostname);
    if (parsed.protocol !== "http:" || !loopback || !parsed.port || parsed.username || parsed.password ||
      parsed.pathname !== "/management.html" || parsed.search || parsed.hash) {
      return undefined;
    }
    return parsed.href;
  } catch {
    return undefined;
  }
}
