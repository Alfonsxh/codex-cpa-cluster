import {
  ArrowLeftOutlined,
  ArrowRightOutlined,
  CheckOutlined,
  ClockCircleOutlined,
  SettingOutlined
} from "@ant-design/icons";
import { Alert, Button, Input, Progress, Result, Skeleton, Tag } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useSearchParams } from "react-router-dom";

import { saveConfiguration } from "../api/configuration";
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
  initial_password: "初始密码",
  first_account: "首个 CPA",
  account_authorization: "OAuth 授权",
  first_user: "首个用户"
};

const recommendationLabels: Record<string, string> = {
  public_base_url: "访问地址",
  quota_timezone: "额度时区",
  weekly_quota: "默认额度",
  notifications: "通知",
  branding: "品牌",
  proxy: "上游代理"
};

export function OnboardingPage({ csrfToken }: { csrfToken: string }) {
  const location = useLocation();
  const navigate = useNavigate();
  const [searchParams, setSearchParams] = useSearchParams();
  const queryClient = useQueryClient();
  const { setRefreshAction, setRefreshLabel, setRefreshing } = useAdminToolbar();
  const leavingSetup = useRef(false);
  const [domains, setDomains] = useState("");
  const [publicURL, setPublicURL] = useState(() => window.location.origin);
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
  const selectedID = searchParams.get("step") ?? "";
  const selected = useMemo(() => selectOnboardingStep(onboarding.data, selectedID), [onboarding.data, selectedID]);

  useEffect(() => setRefreshing(onboarding.isFetching), [onboarding.isFetching, setRefreshing]);
  useEffect(() => {
    if (onboarding.data) setRefreshLabel(`初始化状态更新于 ${formatStatusTime(onboarding.data.generated_at)}`);
    return () => setRefreshLabel("");
  }, [onboarding.data, setRefreshLabel]);
  useEffect(() => {
    setRefreshAction(async () => {
      const result = await onboarding.refetch();
      if (result.error) throw result.error;
    });
    return () => setRefreshAction(null);
  }, [onboarding, setRefreshAction]);
  useEffect(() => {
    if (leavingSetup.current || !location.pathname.startsWith("/setup") || !onboarding.data || selectedID || !selected) return;
    setSearchParams({ step: selected.id }, { replace: true });
  }, [location.pathname, onboarding.data, selected, selectedID, setSearchParams]);

  const preferences = useMutation({
    mutationFn: (next: { deferred: boolean; skippedRecommended: string[] }) => (
      saveOnboardingPreferences(next, csrfToken)
    ),
    onSuccess: (result) => {
      queryClient.setQueryData(onboardingQueryKey, result);
      setNotice("初始化偏好已保存");
    }
  });
  const configuration = useMutation({
    mutationFn: ({ key, value }: { key: string; value: unknown }) => saveConfiguration({ [key]: value }, csrfToken),
    onSuccess: async () => {
      setNotice("设置已保存，完成状态已重新检查");
      await queryClient.invalidateQueries({ queryKey: onboardingQueryKey, exact: true });
    }
  });
  const continueLater = async () => {
    leavingSetup.current = true;
    try {
      await preferences.mutateAsync({
        deferred: true,
        skippedRecommended: onboarding.data?.skipped_recommended ?? []
      });
      navigate("/overview");
    } catch {
      leavingSetup.current = false;
    }
  };

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
          subTitle={onboarding.error instanceof Error ? onboarding.error.message : "无法读取初始化状态，其他管理功能不受影响。"}
          extra={[
            <Button key="retry" type="primary" onClick={() => void onboarding.refetch()}>重新加载</Button>,
            <Button key="overview" onClick={() => navigate("/overview")}>进入运行总览</Button>
          ]}
        />
      </section>
    );
  }

  const status = onboarding.data;
  const steps = status.steps;
  const selectedIndex = steps.findIndex((step) => step.id === selected.id);
  const requiredPercent = Math.round(status.required.complete / Math.max(1, status.required.total) * 100);
  const updateSkipped = (stepID: string, skipped: boolean) => {
    const next = skipped
      ? Array.from(new Set([...status.skipped_recommended, stepID]))
      : status.skipped_recommended.filter((id) => id !== stepID);
    preferences.mutate({ deferred: false, skippedRecommended: next });
  };
  const jump = (index: number) => {
    const target = steps[index];
    if (target) setSearchParams({ step: target.id });
  };
  const saveDomains = () => configuration.mutate({
    key: "identity.allowed_email_domains",
    value: domains.trim()
  });
  const savePublicURL = () => configuration.mutate({
    key: "branding.public_base_url",
    value: publicURL.trim()
  });

  return (
    <section className="page-content onboarding-page">
      <div className="onboarding-shell">
        <header className="onboarding-hero">
          <div>
            <span className="section-kicker">FIRST RUN WORKSPACE</span>
            <h2>让第一个用户安全开始使用</h2>
            <p>先完成 5 项必需设置；推荐设置可逐项跳过，也可以之后从运行总览继续。</p>
          </div>
          <div className="onboarding-progress-card">
            <strong>{status.required.complete}<span>/{status.required.total}</span></strong>
            <div><span>必需设置</span><Progress percent={requiredPercent} showInfo={false} size="small" /></div>
          </div>
        </header>

        {notice ? <Alert className="page-alert" type="success" showIcon closable title={notice} onClose={() => setNotice("")} /> : null}
        {preferences.isError || configuration.isError ? (
          <Alert
            className="page-alert"
            type="error"
            showIcon
            title="设置未保存"
            description={(preferences.error ?? configuration.error) instanceof Error
              ? (preferences.error ?? configuration.error as Error).message
              : "请稍后重试"}
          />
        ) : null}

        <div className="onboarding-workspace">
          <aside className="onboarding-steps" aria-label="首次设置步骤">
            <OnboardingStepGroup
              title="必须完成"
              detail={`${status.required.complete}/${status.required.total}`}
              steps={steps.filter((step) => step.kind === "required")}
              selectedID={selected.id}
              onSelect={(id) => setSearchParams({ step: id })}
            />
            <OnboardingStepGroup
              title="推荐设置"
              detail={`${status.recommended.complete + status.recommended.skipped}/${status.recommended.total}`}
              steps={steps.filter((step) => step.kind === "recommended")}
              selectedID={selected.id}
              onSelect={(id) => setSearchParams({ step: id })}
            />
          </aside>

          <main className="onboarding-step-panel">
            <div className="onboarding-step-heading">
              <div>
                <span className="section-kicker">{selected.kind === "required" ? "REQUIRED STEP" : "RECOMMENDED"}</span>
                <h3>{selected.title}</h3>
                <p>{selected.description}</p>
              </div>
              <StepStatusTag status={selected.status} />
            </div>

            {selected.blockers.length ? (
              <Alert className="onboarding-blockers" type="warning" showIcon title="完成此步骤前还需要" description={selected.blockers.join("；")} />
            ) : null}

            <OnboardingStepAction
              step={selected}
              domains={domains}
              publicURL={publicURL}
              pending={configuration.isPending}
              onDomainsChange={setDomains}
              onPublicURLChange={setPublicURL}
              onSaveDomains={saveDomains}
              onSavePublicURL={savePublicURL}
              onOpenInitialPassword={() => setInitialPasswordOpen(true)}
              onNavigate={() => navigate(selected.action_path)}
            />

            {selected.kind === "recommended" && selected.status !== "complete" ? (
              <div className="onboarding-recommendation-actions">
                {selected.status === "skipped" ? (
                  <Button disabled={preferences.isPending} onClick={() => updateSkipped(selected.id, false)}>恢复此推荐项</Button>
                ) : (
                  <Button disabled={preferences.isPending} onClick={() => updateSkipped(selected.id, true)}>暂时跳过此项</Button>
                )}
                <Button
                  type="text"
                  disabled={preferences.isPending}
                  onClick={() => preferences.mutate({
                    deferred: false,
                    skippedRecommended: steps.filter((step) => step.kind === "recommended" && step.status !== "complete").map((step) => step.id)
                  })}
                >全部推荐项稍后再说</Button>
              </div>
            ) : null}

            <footer className="onboarding-step-footer">
              <Button icon={<ArrowLeftOutlined />} disabled={selectedIndex <= 0} onClick={() => jump(selectedIndex - 1)}>上一步</Button>
              <span>第 {selectedIndex + 1} 步，共 {steps.length} 步</span>
              {selectedIndex < steps.length - 1 ? (
                <Button type="primary" onClick={() => jump(selectedIndex + 1)}>下一步<ArrowRightOutlined /></Button>
              ) : (
                <Button type="primary" onClick={() => navigate("/overview")}>进入运行总览<ArrowRightOutlined /></Button>
              )}
            </footer>
          </main>
        </div>

        <div className="onboarding-bottom-bar">
          <p>{status.required_complete ? "必需设置已完成。推荐项不会阻塞使用。" : "还没准备好？保存进度后可从运行总览继续。"}</p>
          {!status.required_complete ? (
            <Button
              icon={<ClockCircleOutlined />}
              loading={preferences.isPending}
              onClick={() => void continueLater()}
            >稍后继续</Button>
          ) : <Button type="primary" onClick={() => navigate("/overview")}>完成引导</Button>}
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
        }}
      />
    </section>
  );
}

function OnboardingStepGroup({
  title,
  detail,
  steps,
  selectedID,
  onSelect
}: {
  title: string;
  detail: string;
  steps: OnboardingStep[];
  selectedID: string;
  onSelect: (id: string) => void;
}) {
  return (
    <section className="onboarding-step-group">
      <div className="onboarding-step-group-title"><strong>{title}</strong><span>{detail}</span></div>
      <nav aria-label={title}>
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
  publicURL,
  pending,
  onDomainsChange,
  onPublicURLChange,
  onSaveDomains,
  onSavePublicURL,
  onOpenInitialPassword,
  onNavigate
}: {
  step: OnboardingStep;
  domains: string;
  publicURL: string;
  pending: boolean;
  onDomainsChange: (value: string) => void;
  onPublicURLChange: (value: string) => void;
  onSaveDomains: () => void;
  onSavePublicURL: () => void;
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
        <Input.TextArea id="onboarding-email-domains" value={domains} onChange={(event) => onDomainsChange(event.target.value)} autoSize={{ minRows: 3, maxRows: 5 }} placeholder="example.com, example.org" />
        <small>使用逗号、空格或换行分隔。未列入的邮箱无法创建或登录。</small>
        <Button type="primary" loading={pending} disabled={!domains.trim()} onClick={onSaveDomains}>保存并检查</Button>
      </div>
    );
  }
  if (step.id === "public_base_url") {
    return (
      <div className="onboarding-inline-form">
        <label htmlFor="onboarding-public-url">公开访问地址</label>
        <Input id="onboarding-public-url" type="url" value={publicURL} onChange={(event) => onPublicURLChange(event.target.value)} placeholder="https://cpa.example.com" />
        <small>默认使用当前浏览器地址；通知和客户端配置导出会引用此地址。</small>
        <Button type="primary" loading={pending} disabled={!/^https?:\/\//i.test(publicURL.trim())} onClick={onSavePublicURL}>使用此地址</Button>
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
  return (
    <div className="onboarding-action-card">
      <SettingOutlined aria-hidden="true" />
      <div><strong>{step.kind === "required" ? "前往现有管理流程" : "按需完成此项"}</strong><p>完成后返回此页面，系统会重新读取真实状态。</p></div>
      <Button type={step.kind === "required" ? "primary" : "default"} onClick={onNavigate}>前往设置<ArrowRightOutlined /></Button>
    </div>
  );
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

function formatStatusTime(timestamp: number) {
  return new Intl.DateTimeFormat("zh-CN", { hour: "2-digit", minute: "2-digit", hour12: false })
    .format(new Date(timestamp * 1000));
}
