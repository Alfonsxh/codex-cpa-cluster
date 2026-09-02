import {
  ArrowLeftOutlined,
  ArrowRightOutlined,
  CheckOutlined,
  SettingOutlined
} from "@ant-design/icons";
import { Alert, Button, Input, InputNumber, Progress, Result, Skeleton, Tag } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";

import {
  configurationQueryKey,
  readConfiguration,
  saveConfiguration,
  type ConfigurationCatalog
} from "../api/configuration";
import { saveNotificationWebhook } from "../api/notifications";
import {
  onboardingQueryKey,
  readOnboarding,
  saveOnboardingPreferences,
  type OnboardingStatus,
  type OnboardingStep
} from "../api/onboarding";
import { useAdminToolbar } from "./AdminToolbarContext";
import { InitialPasswordModal } from "./InitialPasswordModal";

const requiredLabels: Record<string, string> = {
  email_domains: "访问范围",
  initial_password: "初始密码"
};

const recommendationLabels: Record<string, string> = {
  public_base_url: "访问地址",
  quota_timezone: "额度时区",
  weekly_quota: "默认额度",
  notifications: "通知",
  branding: "品牌",
  proxy: "上游代理"
};

type OnboardingDrafts = {
  publicURL: string;
  quotaTimezone: string;
  weeklyQuota: number | null;
  webhookURL: string;
  productName: string;
  shortName: string;
  environmentLabel: string;
  proxyURL: string;
};

export function OnboardingPage({ csrfToken }: { csrfToken: string }) {
  const location = useLocation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const { setRefreshAction, setRefreshLabel, setRefreshing } = useAdminToolbar();
  const configurationHydrated = useRef(false);
  const [domains, setDomains] = useState("");
  const [drafts, setDrafts] = useState<OnboardingDrafts>({
    publicURL: window.location.origin,
    quotaTimezone: "",
    weeklyQuota: null,
    webhookURL: "",
    productName: "",
    shortName: "",
    environmentLabel: "",
    proxyURL: ""
  });
  const [notice, setNotice] = useState("");
  const [initialPasswordOpen, setInitialPasswordOpen] = useState(false);

  const onboarding = useQuery({
    queryKey: onboardingQueryKey,
    queryFn: ({ signal }) => readOnboarding(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const catalog = useQuery({
    queryKey: configurationQueryKey,
    queryFn: ({ signal }) => readConfiguration(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const selectedID = searchParams.get("step") ?? "";
  const selected = useMemo(() => selectOnboardingStep(onboarding.data, selectedID), [onboarding.data, selectedID]);
  const configurationValues = useMemo(() => configurationValueMap(catalog.data), [catalog.data]);
  const proxyConfigured = configurationField(catalog.data, "cpa.proxy_url")?.configured === true;

  useEffect(() => setRefreshing(onboarding.isFetching || catalog.isFetching), [catalog.isFetching, onboarding.isFetching, setRefreshing]);
  useEffect(() => {
    if (onboarding.data) setRefreshLabel(`初始化状态更新于 ${formatStatusTime(onboarding.data.generated_at)}`);
    return () => setRefreshLabel("");
  }, [onboarding.data, setRefreshLabel]);
  useEffect(() => {
    setRefreshAction(async () => {
      const results = await Promise.all([onboarding.refetch(), catalog.refetch()]);
      const error = results.find((result) => result.error)?.error;
      if (error) throw error;
    });
    return () => setRefreshAction(null);
  }, [catalog, onboarding, setRefreshAction]);
  useEffect(() => {
    if (!location.pathname.startsWith("/setup") || !onboarding.data || !selected || selectedID === selected.id) return;
    setSearchParams({ step: selected.id }, { replace: true });
  }, [location.pathname, onboarding.data, selected, selectedID, setSearchParams]);
  useEffect(() => {
    if (!catalog.data || configurationHydrated.current) return;
    configurationHydrated.current = true;
    const browserTimezone = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
    setDrafts((current) => ({
      ...current,
      publicURL: configurationStringValue(catalog.data, "branding.public_base_url") || window.location.origin,
      quotaTimezone: configurationStringValue(catalog.data, "user_quota.timezone") || browserTimezone,
      weeklyQuota: configurationNumberValue(catalog.data, "user_quota.default_weekly_tokens"),
      productName: configurationStringValue(catalog.data, "branding.product_name"),
      shortName: configurationStringValue(catalog.data, "branding.short_name"),
      environmentLabel: configurationStringValue(catalog.data, "branding.environment_label")
    }));
  }, [catalog.data]);

  const advanceAfterSave = () => {
    const steps = onboarding.data?.steps ?? [];
    const currentIndex = steps.findIndex((step) => step.id === selected?.id);
    const next = currentIndex >= 0 ? steps[currentIndex + 1] : undefined;
    if (next) setSearchParams({ step: next.id });
  };

  const preferences = useMutation({
    mutationFn: (skippedRecommended: string[]) => (
      saveOnboardingPreferences(skippedRecommended, csrfToken)
    ),
    onSuccess: (result) => {
      queryClient.setQueryData(onboardingQueryKey, result);
      setNotice("初始化偏好已保存");
    }
  });
  const configuration = useMutation({
    mutationFn: (values: Record<string, unknown>) => saveConfiguration(values, csrfToken),
    onSuccess: async (result) => {
      setNotice(`${result.message}，完成状态已重新检查`);
      await Promise.all([
        catalog.refetch(),
        queryClient.invalidateQueries({ queryKey: onboardingQueryKey, exact: true })
      ]);
      advanceAfterSave();
    }
  });
  const notificationWebhook = useMutation({
    mutationFn: () => saveNotificationWebhook(drafts.webhookURL.trim(), csrfToken),
    onSuccess: async (result) => {
      setDrafts((current) => ({ ...current, webhookURL: "" }));
      setNotice(`${result.message}，完成状态已重新检查`);
      await queryClient.invalidateQueries({ queryKey: onboardingQueryKey, exact: true });
      advanceAfterSave();
    }
  });
  if (onboarding.isPending) {
    return (
      <section className="page-content onboarding-page" aria-label="正在加载首次设置">
        <div className="onboarding-shell"><Skeleton active paragraph={{ rows: 12 }} /></div>
      </section>
    );
  }
  if (onboarding.isError || !onboarding.data || !selected) {
    return (
      <section className="page-content onboarding-page">
        <Result
          status="warning"
          title="首次设置状态暂时不可用"
          subTitle={onboarding.error instanceof Error ? onboarding.error.message : "无法读取首次设置状态，请稍后重试。"}
          extra={[
            <Button key="retry" type="primary" onClick={() => void onboarding.refetch()}>重新加载</Button>
          ]}
        />
      </section>
    );
  }

  const status = onboarding.data;
  const steps = status.steps;
  const selectedIndex = steps.findIndex((step) => step.id === selected.id);
  const completedCount = status.required.complete + status.recommended.complete + status.recommended.skipped;
  const totalCount = status.required.total + status.recommended.total;
  const completionPercent = Math.round(completedCount / Math.max(1, totalCount) * 100);
  const updateSkipped = (stepID: string, skipped: boolean) => {
    const next = skipped
      ? Array.from(new Set([...status.skipped_recommended, stepID]))
      : status.skipped_recommended.filter((id) => id !== stepID);
    preferences.mutate(next);
  };
  const jump = (index: number) => {
    const target = steps[index];
    if (target) setSearchParams({ step: target.id });
  };
  const saveDomains = () => configuration.mutate({ "identity.allowed_email_domains": domains.trim() });
  const updateDraft = (key: keyof OnboardingDrafts, value: OnboardingDrafts[keyof OnboardingDrafts]) => {
    setDrafts((current) => ({ ...current, [key]: value }));
  };

  return (
    <section className="page-content onboarding-page">
      <div className="onboarding-shell">
        <header className="onboarding-hero">
          <div>
            <h2>完成基础配置</h2>
            <p>集中设置访问范围、初始密码和运行参数；其他业务操作可在对应管理页面中完成。</p>
          </div>
          <div className="onboarding-hero-actions">
            <div className="onboarding-progress-card">
              <strong>{completedCount}<span>/{totalCount}</span></strong>
              <div><span>配置进度</span><Progress percent={completionPercent} showInfo={false} size="small" /></div>
            </div>
          </div>
        </header>

        {notice ? <Alert className="page-alert" type="success" showIcon closable title={notice} onClose={() => setNotice("")} /> : null}
        {preferences.isError || configuration.isError || notificationWebhook.isError ? (
          <Alert
            className="page-alert"
            type="error"
            showIcon
            title="设置未保存"
            description={(preferences.error ?? configuration.error ?? notificationWebhook.error) instanceof Error
              ? (preferences.error ?? configuration.error ?? notificationWebhook.error as Error).message
              : "请稍后重试"}
          />
        ) : null}

        <div className="onboarding-workspace">
          <aside className="onboarding-steps" aria-label="初始化配置">
            <OnboardingStepList
              steps={steps}
              selectedID={selected.id}
              onSelect={(id) => setSearchParams({ step: id })}
            />
          </aside>

          <main className="onboarding-step-panel">
            <div className="onboarding-step-heading">
              <div>
                <h3>{selected.title}</h3>
                <p>{selected.description}</p>
              </div>
              <StepStatusTag status={selected.status} />
            </div>

            <OnboardingStepAction
              step={selected}
              domains={domains}
              drafts={drafts}
              configurationValues={configurationValues}
              configurationPending={catalog.isPending}
              configurationError={catalog.error}
              proxyConfigured={proxyConfigured}
              pending={configuration.isPending || notificationWebhook.isPending}
              onDomainsChange={setDomains}
              onDraftChange={updateDraft}
              onSaveDomains={saveDomains}
              onSaveConfiguration={(values) => configuration.mutate(values)}
              onSaveNotification={() => notificationWebhook.mutate()}
              onRetryConfiguration={() => void catalog.refetch()}
              onOpenInitialPassword={() => setInitialPasswordOpen(true)}
              onNavigate={() => navigate(selected.action_path)}
            />

            <footer className="onboarding-step-footer">
              <Button icon={<ArrowLeftOutlined />} disabled={selectedIndex <= 0} onClick={() => jump(selectedIndex - 1)}>上一步</Button>
              <span>第 {selectedIndex + 1} 步，共 {steps.length} 步</span>
              <div className="onboarding-step-footer-actions">
                {selected.kind === "recommended" && selected.status !== "complete" ? (
                  selected.status === "skipped" ? (
                    <Button disabled={preferences.isPending} onClick={() => updateSkipped(selected.id, false)}>重新设置</Button>
                  ) : (
                    <Button disabled={preferences.isPending} onClick={() => updateSkipped(selected.id, true)}>暂时跳过</Button>
                  )
                ) : null}
                {selectedIndex < steps.length - 1 ? (
                  <Button type="primary" onClick={() => jump(selectedIndex + 1)}>下一步<ArrowRightOutlined /></Button>
                ) : (
                  <Button type="primary" onClick={() => navigate("/overview")}>进入运行总览<ArrowRightOutlined /></Button>
                )}
              </div>
            </footer>
          </main>
        </div>
      </div>

      <InitialPasswordModal
        open={initialPasswordOpen}
        csrfToken={csrfToken}
        onClose={() => setInitialPasswordOpen(false)}
        onSuccess={(message) => {
          setInitialPasswordOpen(false);
          setNotice(message);
          void queryClient.invalidateQueries({ queryKey: onboardingQueryKey, exact: true });
          advanceAfterSave();
        }}
      />
    </section>
  );
}

function OnboardingStepList({
  steps,
  selectedID,
  onSelect
}: {
  steps: OnboardingStep[];
  selectedID: string;
  onSelect: (id: string) => void;
}) {
  return (
    <section className="onboarding-step-group">
      <nav aria-label="初始化配置">
        {steps.map((step, index) => (
          <button
            key={step.id}
            className={selectedID === step.id ? "active" : ""}
            type="button"
            aria-current={selectedID === step.id ? "step" : undefined}
            onClick={() => onSelect(step.id)}
          >
            <span className={`onboarding-step-index status-${step.status}`} aria-hidden="true">
              {step.status === "complete" ? <CheckOutlined /> : index + 1}
            </span>
            <span><strong>{requiredLabels[step.id] ?? recommendationLabels[step.id] ?? step.title}</strong><small>{step.title}</small></span>
            <i className={`onboarding-step-dot status-${step.status}`} aria-label={stepStatusLabel(step.status)} />
          </button>
        ))}
      </nav>
    </section>
  );
}

function OnboardingStepAction({
  step,
  domains,
  drafts,
  configurationValues,
  configurationPending,
  configurationError,
  proxyConfigured,
  pending,
  onDomainsChange,
  onDraftChange,
  onSaveDomains,
  onSaveConfiguration,
  onSaveNotification,
  onRetryConfiguration,
  onOpenInitialPassword,
  onNavigate
}: {
  step: OnboardingStep;
  domains: string;
  drafts: OnboardingDrafts;
  configurationValues: Record<string, unknown>;
  configurationPending: boolean;
  configurationError: unknown;
  proxyConfigured: boolean;
  pending: boolean;
  onDomainsChange: (value: string) => void;
  onDraftChange: (key: keyof OnboardingDrafts, value: OnboardingDrafts[keyof OnboardingDrafts]) => void;
  onSaveDomains: () => void;
  onSaveConfiguration: (values: Record<string, unknown>) => void;
  onSaveNotification: () => void;
  onRetryConfiguration: () => void;
  onOpenInitialPassword: () => void;
  onNavigate: () => void;
}) {
  if (step.status === "complete") {
    return <div className="onboarding-complete-state"><CheckOutlined /><div><strong>此步骤已完成</strong><p>状态来自控制面实时检查，无需重复配置。</p></div></div>;
  }
  if (step.id === "email_domains") {
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-email-domains">允许的邮箱域名</label>
        <Input.TextArea id="onboarding-email-domains" value={domains} onChange={(event) => onDomainsChange(event.target.value)} autoSize={{ minRows: 2, maxRows: 4 }} placeholder="example.com, example.org" />
        <small>使用逗号、空格或换行分隔。未列入的邮箱无法创建或登录。</small>
        <Button type="primary" loading={pending} disabled={!domains.trim()} onClick={onSaveDomains}>保存并检查</Button>
      </div>
    );
  }
  if (step.id === "initial_password") {
    return (
      <div className="onboarding-action-card">
        <SettingOutlined aria-hidden="true" />
        <div><strong>密码只写入、不回显</strong><p>设置后，新建或重置用户时由系统交付该初始密码。</p></div>
        <Button type="primary" onClick={onOpenInitialPassword}>设置初始密码</Button>
      </div>
    );
  }
  if (step.kind === "required") {
    return (
      <div className="onboarding-action-card">
        <SettingOutlined aria-hidden="true" />
        <div><strong>前往现有管理流程</strong><p>完成后返回此页面，系统会重新读取真实状态。</p></div>
        <Button type="primary" onClick={onNavigate}>前往设置<ArrowRightOutlined /></Button>
      </div>
    );
  }
  if (configurationPending) {
    return <div className="onboarding-inline-form onboarding-form-state" aria-label="正在读取配置"><Skeleton active title={false} paragraph={{ rows: 3 }} /></div>;
  }
  if (configurationError) {
    return (
      <div className="onboarding-action-card">
        <SettingOutlined aria-hidden="true" />
        <div><strong>配置暂时不可用</strong><p>{configurationError instanceof Error ? configurationError.message : "无法读取当前配置，请稍后重试。"}</p></div>
        <Button type="primary" onClick={onRetryConfiguration}>重新读取</Button>
      </div>
    );
  }
  if (step.id === "public_base_url") {
    const valid = /^https?:\/\/[^\s]+$/i.test(drafts.publicURL.trim());
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-public-url">公开访问地址</label>
        <Input id="onboarding-public-url" type="url" value={drafts.publicURL} onChange={(event) => onDraftChange("publicURL", event.target.value)} placeholder="https://cpa.example.com" />
        <small>默认使用当前浏览器地址；通知和客户端配置导出会引用此地址。</small>
        <Button type="primary" loading={pending} disabled={!valid} onClick={() => onSaveConfiguration({ "branding.public_base_url": drafts.publicURL.trim() })}>使用此地址</Button>
      </div>
    );
  }
  if (step.id === "quota_timezone") {
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-quota-timezone">用户额度时区</label>
        <Input id="onboarding-quota-timezone" value={drafts.quotaTimezone} onChange={(event) => onDraftChange("quotaTimezone", event.target.value)} placeholder="Asia/Shanghai" />
        <small>使用 IANA 时区，例如 Asia/Shanghai、Europe/London 或 UTC。</small>
        <Button type="primary" loading={pending} disabled={!drafts.quotaTimezone.trim()} onClick={() => onSaveConfiguration({ "user_quota.timezone": drafts.quotaTimezone.trim() })}>保存时区</Button>
      </div>
    );
  }
  if (step.id === "weekly_quota") {
    const valid = drafts.weeklyQuota !== null && Number.isInteger(drafts.weeklyQuota) && drafts.weeklyQuota > 0 && drafts.weeklyQuota <= 1_000_000_000_000;
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-weekly-quota">新用户默认周额度</label>
        <InputNumber id="onboarding-weekly-quota" aria-label="新用户默认周额度" min={1} max={1_000_000_000_000} precision={0} value={drafts.weeklyQuota} onChange={(value) => onDraftChange("weeklyQuota", typeof value === "number" ? value : null)} placeholder="20000000" />
        <small>按自然周统计加权 Token；不设置额度时可直接跳过此推荐项。</small>
        <Button type="primary" loading={pending} disabled={!valid} onClick={() => onSaveConfiguration({ "user_quota.default_weekly_tokens": drafts.weeklyQuota })}>保存默认额度</Button>
      </div>
    );
  }
  if (step.id === "notifications") {
    const valid = drafts.webhookURL.trim().startsWith("https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=");
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-notification-webhook">企业微信群 Webhook</label>
        <Input.Password id="onboarding-notification-webhook" value={drafts.webhookURL} onChange={(event) => onDraftChange("webhookURL", event.target.value)} autoComplete="new-password" placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..." />
        <small>地址只写入加密存储，不会通过初始化状态接口或浏览器缓存回显。</small>
        <Button type="primary" loading={pending} disabled={!valid} onClick={onSaveNotification}>保存 Webhook</Button>
      </div>
    );
  }
  if (step.id === "branding") {
    const productName = drafts.productName.trim();
    const shortName = drafts.shortName.trim();
    const environmentLabel = drafts.environmentLabel.trim();
    const valid = productName.length >= 2 && productName.length <= 64 && shortName.length >= 2 && shortName.length <= 32 && environmentLabel.length <= 64;
    const changed = productName !== configurationValues["branding.product_name"] || shortName !== configurationValues["branding.short_name"] || environmentLabel !== configurationValues["branding.environment_label"];
    return (
      <div className="onboarding-inline-form onboarding-inline-form-multi">
        <div className="onboarding-form-fields onboarding-branding-fields">
          <label htmlFor="onboarding-product-name"><span>产品名称</span><Input id="onboarding-product-name" maxLength={64} value={drafts.productName} onChange={(event) => onDraftChange("productName", event.target.value)} /></label>
          <label htmlFor="onboarding-short-name"><span>产品简称</span><Input id="onboarding-short-name" maxLength={32} value={drafts.shortName} onChange={(event) => onDraftChange("shortName", event.target.value)} /></label>
          <label htmlFor="onboarding-environment-label"><span>环境说明</span><Input id="onboarding-environment-label" maxLength={64} value={drafts.environmentLabel} onChange={(event) => onDraftChange("environmentLabel", event.target.value)} placeholder="例如：研发团队专用" /></label>
        </div>
        <small>至少修改一项才会标记为已配置；保持默认品牌时可跳过此推荐项。</small>
        <Button type="primary" loading={pending} disabled={!valid || !changed} onClick={() => onSaveConfiguration({ "branding.product_name": productName, "branding.short_name": shortName, "branding.environment_label": environmentLabel })}>保存品牌信息</Button>
      </div>
    );
  }
  if (step.id === "proxy") {
    const proxyURL = drafts.proxyURL.trim();
    const valid = proxyConfigured ? !proxyURL || /^(?:https?|socks5):\/\/[^\s]+$/i.test(proxyURL) : /^(?:https?|socks5):\/\/[^\s]+$/i.test(proxyURL);
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-proxy-url">默认上游代理 URL</label>
        <Input.Password id="onboarding-proxy-url" value={drafts.proxyURL} onChange={(event) => onDraftChange("proxyURL", event.target.value)} autoComplete="new-password" placeholder={proxyConfigured ? "已加密保存；留空直接启用现有代理" : "socks5://user:password@proxy.example.com:1080"} />
        <small>保存后会启用默认代理，并应用到所有选择“继承默认”的 CPA；密钥不会回显。</small>
        <Button type="primary" loading={pending} disabled={!valid} onClick={() => onSaveConfiguration({ "cpa.proxy_enabled": true, ...(proxyURL ? { "cpa.proxy_url": proxyURL } : {}) })}>保存并启用代理</Button>
      </div>
    );
  }
  return null;
}

function StepStatusTag({ status }: { status: OnboardingStep["status"] }) {
  const colors: Record<OnboardingStep["status"], string> = {
    complete: "success",
    incomplete: "processing",
    blocked: "warning",
    skipped: "default",
    unavailable: "error"
  };
  return <Tag color={colors[status]}>{stepStatusLabel(status)}</Tag>;
}

function stepStatusLabel(status: OnboardingStep["status"]) {
  return ({
    complete: "已完成",
    incomplete: "待设置",
    blocked: "等待前置步骤",
    skipped: "已跳过",
    unavailable: "状态暂不可用"
  } as const)[status];
}

function selectOnboardingStep(status: OnboardingStatus | undefined, selectedID: string) {
  if (!status?.steps.length) return undefined;
  const explicit = status.steps.find((step) => step.id === selectedID);
  if (explicit) return explicit;
  return status.steps.find((step) => step.kind === "required" && step.status !== "complete")
    ?? status.steps.find((step) => step.kind === "recommended" && !["complete", "skipped"].includes(step.status))
    ?? status.steps[0];
}

function configurationField(catalog: ConfigurationCatalog | undefined, key: string) {
  return catalog?.groups.flatMap((group) => group.fields).find((field) => field.key === key);
}

function configurationValueMap(catalog: ConfigurationCatalog | undefined): Record<string, unknown> {
  return Object.fromEntries(catalog?.groups.flatMap((group) => group.fields).map((field) => [field.key, field.value]) ?? []);
}

function configurationStringValue(catalog: ConfigurationCatalog, key: string): string {
  const value = configurationField(catalog, key)?.value;
  return typeof value === "string" ? value : "";
}

function configurationNumberValue(catalog: ConfigurationCatalog, key: string): number | null {
  const value = configurationField(catalog, key)?.value;
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

function formatStatusTime(timestamp: number) {
  return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false })
    .format(new Date(timestamp * 1000));
}
