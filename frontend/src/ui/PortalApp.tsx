import {
  ArrowRightOutlined,
  BarChartOutlined,
  ControlOutlined
} from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { Badge, Card, Col, Row, Space, Tag, Typography } from "antd";
import { useEffect } from "react";
import { Link, Navigate, Route, Routes } from "react-router-dom";

import { ApiError } from "../api/client";
import {
  defaultPublicSiteConfiguration,
  listNativeAccounts,
  nativeAccountsQueryKey,
  publicSiteQueryKey,
  readPublicSiteConfiguration
} from "../api/public-site";
import type { NativeAccount, PublicSiteConfiguration } from "../api/public-site";
import { ThemeToggle, useTheme, type ThemeMode } from "./ThemeProvider";

const { Paragraph, Text, Title } = Typography;

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
          <Title id="portal-title">选择要进入的界面</Title>
          <Paragraph>添加和管理业务 CPA，或进入使用中心查看自己的 Key、账号与用量。</Paragraph>
        </section>
        <Space className="portal-header-actions" size="middle" wrap>
          {branding.degraded ? <Tag color="warning">默认品牌配置</Tag> : null}
          <Badge status="success" text={branding.configuration.environment_label || "Self-hosted service"} />
          <ThemeToggle />
        </Space>
      </header>

      <Row gutter={[20, 20]} className="portal-entry-grid" aria-label="可用界面">
        <Col xs={24} lg={12}>
          <EntryCard
            className="portal-entry-primary"
            href="/admin/"
            number="01"
            badge="需要管理密钥"
            icon={<ControlOutlined aria-hidden="true" />}
            eyebrow="CONTROL PLANE"
            title="综合管理平台"
            description="添加业务 CPA、管理用户与 Key、OAuth 授权、容器、日志和诊断任务。"
            action="进入管理平台"
          />
        </Col>
        <Col xs={24} lg={12}>
          <EntryCard
            href="/usage/"
            number="02"
            badge="邮箱进入"
            icon={<BarChartOutlined aria-hidden="true" />}
            eyebrow="ACCESS & OBSERVABILITY"
            title="使用中心"
            description="查看唯一 API Key，切换 CPA 账号，并统计各账号的请求和 Token 用量。"
            action="进入使用中心"
          />
        </Col>
      </Row>
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
      <a href="/admin/?action=add-account" className="native-card native-add-card" aria-label="添加业务 CPA">
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
  icon: React.ReactNode;
  eyebrow: string;
  title: string;
  description: string;
  action: string;
}) {
  return (
    <a href={href} className="portal-entry-link">
      <Card className={`portal-entry-card ${className}`} hoverable>
        <div className="portal-card-top"><span>{number}</span><Tag>{badge}</Tag></div>
        <div className="portal-card-icon">{icon}</div>
        <Text className="portal-eyebrow">{eyebrow}</Text>
        <Title level={2}>{title}</Title>
        <Paragraph>{description}</Paragraph>
        <strong><span>{action}</span><ArrowRightOutlined /></strong>
      </Card>
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
