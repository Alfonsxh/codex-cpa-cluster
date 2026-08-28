import { Alert, Button, Form, Input, Modal } from "antd";
import { useMutation, useQuery } from "@tanstack/react-query";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Controller, useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";

import { ApiError } from "../api/client";
import {
  configurationQueryKey,
  readConfiguration,
  saveConfiguration,
  type ConfigurationCatalog,
  type ConfigurationField,
  type ConfigurationValue
} from "../api/configuration";
import {
  generalSettingsQueryKey,
  readGeneralSettings,
  resetBrandingLogo,
  rotateManagementKey,
  saveBrandingLogo
} from "../api/general-settings";
import {
  clearNotificationWebhook,
  notificationSettingsQueryKey,
  readNotificationSettings,
  saveNotificationWebhook,
  sendNotification
} from "../api/notifications";
import {
  readSettingsWorkspace,
  settingsWorkspaceQueryKey
} from "../api/settings-workspace";
import {
  applyUserQuotaAction,
  readUserQuotaOperations,
  userQuotaOperationsQueryKey
} from "../api/users";
import { useAdminToolbar } from "./AdminToolbarContext";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { PageState } from "./components/PageState";
import { tokenInputPresentation, tokenReadableText } from "./formatters";
import { InitialPasswordModal } from "./InitialPasswordModal";
import { LegacyPasswordInput } from "./components/LegacyPasswordInput";

type DraftValue = string | number | boolean | null;
type Draft = Record<string, DraftValue>;
type SystemSection = "access" | "backups" | "storage" | "audit";
type SectionSelection =
  | { kind: "configuration"; group: string }
  | { kind: "system"; section: SystemSection };
type EditorField = ConfigurationField & { group: string };

const managementKeySchema = z.object({
  newKey: z.string().min(12, "至少输入 12 个字符").max(128, "最多输入 128 个字符").regex(/^\S+$/, "不能包含空白字符"),
  confirmation: z.string().min(1, "请再次输入新管理密钥")
}).refine((values) => values.newKey === values.confirmation, {
  path: ["confirmation"],
  message: "两次输入的管理密钥不一致"
});
const quotaResetSchema = z.object({
  reason: z.string().trim().min(4, "请填写至少 4 个字符的清零原因").max(240, "原因不能超过 240 个字符"),
  confirmation: z.literal("RESET ALL USERS", { message: "请输入 RESET ALL USERS" })
});
type ManagementKeyValues = z.infer<typeof managementKeySchema>;
type QuotaResetValues = z.infer<typeof quotaResetSchema>;

const maxLogoBytes = 2 * 1024 * 1024;
const supportedLogoTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml"]);
const reasoningMultiplierPrefix = "user_quota.reasoning_multiplier.";
const reasoningColorPrefix = "admin.account_usage.reasoning_effort_color.";
const reasoningEfforts = ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "auto", "unknown"] as const;

export function ConfigurationPage({
  csrfToken,
  onManagementKeyRotated = () => undefined
}: {
  csrfToken: string;
  onManagementKeyRotated?: (message: string) => void;
}) {
  const { setRefreshing, setRefreshAction, setRefreshLabel, setPageDetail } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const [selection, setSelection] = useState<SectionSelection>({ kind: "configuration", group: "" });
  const [search, setSearch] = useState("");
  const [draft, setDraft] = useState<Draft>({});
  const [focusKey, setFocusKey] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [saveError, setSaveError] = useState("");
  const [initialPasswordOpen, setInitialPasswordOpen] = useState(false);
  const [managementKeyOpen, setManagementKeyOpen] = useState(false);
  const [logoError, setLogoError] = useState("");
  const [logoResetOpen, setLogoResetOpen] = useState(false);
  const [webhookDraft, setWebhookDraft] = useState("");
  const [webhookError, setWebhookError] = useState("");
  const [webhookClearOpen, setWebhookClearOpen] = useState(false);
  const [quotaResetOpen, setQuotaResetOpen] = useState(false);
  const workspaceContentRef = useRef<HTMLDivElement>(null);

  const catalog = useQuery({
    queryKey: configurationQueryKey,
    queryFn: ({ signal }) => readConfiguration(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const general = useQuery({
    queryKey: generalSettingsQueryKey,
    queryFn: ({ signal }) => readGeneralSettings(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const notification = useQuery({
    queryKey: notificationSettingsQueryKey,
    queryFn: ({ signal }) => readNotificationSettings(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const workspace = useQuery({
    queryKey: settingsWorkspaceQueryKey,
    queryFn: ({ signal }) => readSettingsWorkspace(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const quotaOperations = useQuery({
    queryKey: userQuotaOperationsQueryKey,
    queryFn: ({ signal }) => readUserQuotaOperations(signal),
    enabled: selection.kind === "configuration" && selection.group === "用户额度",
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });

  const fields = useMemo(() => flattenConfiguration(catalog.data), [catalog.data]);
  const dirtyFields = useMemo(
    () => fields.filter((field) => !sameConfigurationValue(normalizeDraftValue(field, draft[field.key]), field.value)),
    [draft, fields]
  );
  const errors = useMemo(() => Object.fromEntries(
    fields.map((field) => [field.key, validateDraftValue(field, draft[field.key])]).filter(([, error]) => Boolean(error))
  ) as Record<string, string>, [draft, fields]);

  useEffect(() => {
    if (!catalog.data) return;
    setDraft((current) => Object.keys(current).length ? current : configurationDraft(catalog.data));
    setSelection((current) => {
      if (current.kind === "system") return current;
      return catalog.data.groups.some((group) => group.name === current.group)
        ? current
        : { kind: "configuration", group: catalog.data.groups[0]?.name ?? "" };
    });
  }, [catalog.data]);

  useEffect(() => {
    if (selection.kind === "system") {
      setPageDetail(settingsSectionHeading(selection.section));
      return () => setPageDetail(null);
    }
    const group = selection.group || catalog.data?.groups[0]?.name || "";
    setPageDetail(group ? { title: group, eyebrow: configurationHeadingEyebrow(group) } : null);
    return () => setPageDetail(null);
  }, [catalog.data, selection, setPageDetail]);

  const refreshWorkspace = useCallback(async (notify = false) => {
    setRefreshing(true);
    try {
      const results = await Promise.all([catalog.refetch(), general.refetch(), notification.refetch(), workspace.refetch()]);
      const resultError = results.find((result) => result.error)?.error;
      if (resultError) throw resultError;
      setRefreshLabel("配置已刷新");
      if (notify) showToast("配置中心已刷新");
    } catch (error) {
      if (notify) showToast(error instanceof Error ? error.message : "配置中心刷新失败", "error");
      throw error;
    } finally {
      setRefreshing(false);
    }
  }, [catalog, general, notification, workspace, setRefreshLabel, setRefreshing, showToast]);

  useEffect(() => {
    setRefreshAction(() => refreshWorkspace(true));
    return () => setRefreshAction(null);
  }, [refreshWorkspace, setRefreshAction]);

  useEffect(() => {
    if (!catalog.data || !general.data || !notification.data || !workspace.data) return;
    setRefreshLabel("配置已刷新");
  }, [catalog.data, general.data, notification.data, setRefreshLabel, workspace.data]);

  useEffect(() => {
    if (!focusKey) return;
    const target = [...document.querySelectorAll<HTMLElement>("[data-configuration-field]")]
      .find((item) => item.dataset.configurationField === focusKey);
    if (!target) return;
    setFocusKey("");
    target.classList.add("configuration-field-highlight");
    target.scrollIntoView?.({ block: "center", behavior: "smooth" });
    target.querySelector<HTMLElement>("input, select, textarea, button")?.focus({ preventScroll: true });
    const timer = window.setTimeout(() => target.classList.remove("configuration-field-highlight"), 1_600);
    return () => window.clearTimeout(timer);
  }, [focusKey, selection]);

  const saveMutation = useMutation({
    onMutate: () => setSaveError(""),
    mutationFn: () => saveConfiguration(
      Object.fromEntries(dirtyFields.map((field) => [field.key, normalizeDraftValue(field, draft[field.key])])),
      csrfToken
    ),
    onSuccess: async (result) => {
      setConfirmOpen(false);
      setSaveError("");
      showToast(result.message);
      const refreshed = await catalog.refetch();
      if (refreshed.data) setDraft(configurationDraft(refreshed.data));
      if (dirtyFields.some((field) => field.key.startsWith(reasoningColorPrefix))) {
        const stylesheet = document.querySelector<HTMLLinkElement>('link[href*="reasoning-effort-colors.css"]');
        if (stylesheet) stylesheet.href = `/admin/reasoning-effort-colors.css?v=${Date.now()}`;
      }
    },
    onError: (error) => setSaveError(error instanceof ApiError ? error.message : "配置未保存")
  });
  const logoMutation = useMutation({
    gcTime: 0,
    mutationFn: (file: File) => saveBrandingLogo(file, csrfToken),
    onSuccess: async (result) => {
      setLogoError("");
      showToast(result.message);
      await general.refetch();
    },
    onError: (error) => setLogoError(error instanceof Error ? error.message : "Logo 未保存")
  });
  const logoResetMutation = useMutation({
    mutationFn: () => resetBrandingLogo(csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      await general.refetch();
    },
    onError: (error) => setLogoError(error instanceof Error ? error.message : "Logo 未恢复")
  });
  const webhookMutation = useMutation({
    gcTime: 0,
    mutationFn: () => saveNotificationWebhook(webhookDraft, csrfToken),
    onSuccess: async (result) => {
      setWebhookDraft("");
      setWebhookError("");
      showToast(result.message);
      await notification.refetch();
    },
    onError: (error) => setWebhookError(error instanceof Error ? error.message : "Webhook 未保存")
  });
  const webhookClearMutation = useMutation({
    mutationFn: () => clearNotificationWebhook(csrfToken),
    onSuccess: async (result) => {
      setWebhookClearOpen(false);
      setWebhookDraft("");
      setWebhookError("");
      showToast(result.message);
      await notification.refetch();
    },
    onError: (error) => setWebhookError(error instanceof Error ? error.message : "Webhook 未清除")
  });
  const notificationSendMutation = useMutation({
    mutationFn: () => sendNotification(csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      await notification.refetch();
    },
    onError: (error) => showToast(error instanceof Error ? error.message : "账号信息发送失败", "error")
  });

  const managementKeyForm = useForm<ManagementKeyValues>({
    resolver: zodResolver(managementKeySchema),
    defaultValues: { newKey: "", confirmation: "" }
  });
  const managementKeyMutation = useMutation({
    gcTime: 0,
    mutationFn: () => rotateManagementKey(managementKeyForm.getValues("newKey"), managementKeyForm.getValues("confirmation"), csrfToken),
    onSuccess: (result) => {
      managementKeyForm.reset();
      setManagementKeyOpen(false);
      onManagementKeyRotated(result.message);
    }
  });
  const quotaResetForm = useForm<QuotaResetValues>({
    resolver: zodResolver(quotaResetSchema),
    defaultValues: { reason: "", confirmation: "" as QuotaResetValues["confirmation"] }
  });
  const quotaResetMutation = useMutation({
    mutationFn: () => applyUserQuotaAction({
      action: "reset_usage",
      scope: "all",
      users: [],
      reason: quotaResetForm.getValues("reason"),
      confirm: "reset_all_current_week_usage"
    }, csrfToken),
    onSuccess: (result) => {
      quotaResetForm.reset();
      setQuotaResetOpen(false);
      showToast(result.message);
      void quotaOperations.refetch();
    }
  });

  const pending = catalog.isPending || general.isPending || notification.isPending || workspace.isPending;
  const loadError = catalog.error ?? general.error ?? notification.error ?? workspace.error;
  if (pending) return <ConfigurationSkeleton />;
  if (loadError || !catalog.data || !general.data || !notification.data || !workspace.data) {
    return (
      <section className="page-content legacy-settings-page">
        <PageState kind="error" title="配置中心加载失败" detail={loadError instanceof Error ? loadError.message : "配置中心数据不完整"} onAction={() => void refreshWorkspace(false)} />
      </section>
    );
  }

  const selectedGroup = selection.kind === "configuration"
    ? catalog.data.groups.find((group) => group.name === selection.group) ?? catalog.data.groups[0]
    : undefined;
  const dirtyModes = new Map<string, number>();
  dirtyFields.forEach((field) => {
    const label = applyModeLabel(field.apply_mode, field.key);
    dirtyModes.set(label, (dirtyModes.get(label) ?? 0) + 1);
  });
  const riskyEffects = configurationEffects(dirtyFields);
  const searchMatches = search.trim()
    ? fields.filter((field) => [field.group, field.label, field.description, field.key].join(" ").toLocaleLowerCase("zh-CN").includes(search.trim().toLocaleLowerCase("zh-CN"))).slice(0, 12)
    : [];
  const selectedValue = selection.kind === "configuration" ? `configuration:${selection.group}` : `system:${selection.section}`;
  const managementKeyError = managementKeyForm.formState.errors.newKey?.message
    ?? managementKeyForm.formState.errors.confirmation?.message
    ?? (managementKeyMutation.isError
      ? managementKeyMutation.error instanceof Error ? managementKeyMutation.error.message : "管理密钥未更新"
      : "");

  const selectConfigurationGroup = (group: string, key = "") => {
    setSearch("");
    setSelection({ kind: "configuration", group });
    setFocusKey(key);
    if (!key) workspaceContentRef.current?.scrollTo?.({ top: 0 });
  };
  const selectSystemSection = (section: SystemSection) => {
    setSearch("");
    setSelection({ kind: "system", section });
    workspaceContentRef.current?.scrollTo?.({ top: 0 });
  };
  const requestSave = () => {
    if (!dirtyFields.length) return;
    const invalid = Object.keys(errors)[0];
    if (invalid) {
      const field = fields.find((item) => item.key === invalid);
      if (field) selectConfigurationGroup(field.group, field.key);
      setSaveError(`请先修正：${errors[invalid]}`);
      return;
    }
    if (riskyEffects.length) setConfirmOpen(true);
    else saveMutation.mutate();
  };

  return (
    <section className="page-content legacy-settings-page">
      <LegacyToastRegion toasts={toasts} />
      <div className="settings-workspace">
        <aside className="settings-navigation" aria-label="配置中心导航">
          <div className="settings-navigation-fixed">
            <label className="configuration-search">
              <span aria-hidden="true">⌕</span>
              <input aria-label="搜索配置" type="search" placeholder="搜索名称、说明或 Key" autoComplete="off" value={search} onChange={(event) => setSearch(event.target.value)} />
            </label>
            <div className="configuration-search-results" hidden={!search.trim()}>
              {searchMatches.length ? searchMatches.map((field) => (
                <button key={field.key} type="button" onClick={() => selectConfigurationGroup(field.group, field.key)}><span>{field.label}</span><small>{field.group} · {field.key}</small></button>
              )) : <p className="configuration-search-empty">没有匹配项</p>}
            </div>
            <label className="settings-mobile-selector">
              <span>当前分类</span>
              <select aria-label="选择配置分类" value={selectedValue} onChange={(event) => {
                const [kind, ...parts] = event.target.value.split(":");
                const value = parts.join(":");
                if (kind === "configuration") selectConfigurationGroup(value);
                else selectSystemSection(value as SystemSection);
              }}>
                <optgroup label="配置分类">{catalog.data.groups.map((group) => <option key={group.name} value={`configuration:${group.name}`}>{group.name}</option>)}</optgroup>
                <optgroup label="系统管理"><option value="system:access">访问凭据</option><option value="system:backups">安全归档</option><option value="system:storage">本地数据</option><option value="system:audit">审计记录</option></optgroup>
              </select>
            </label>
            <p className="settings-navigation-label settings-category-label">配置分类</p>
          </div>
          <div className="settings-navigation-scroll">
            <div className="settings-navigation-desktop">
              <nav className="configuration-navigation" aria-label="配置分类">
                {catalog.data.groups.map((group) => {
                  const dirtyCount = group.fields.filter((field) => dirtyFields.some((item) => item.key === field.key)).length;
                  const active = selection.kind === "configuration" && selection.group === group.name;
                  return <button key={group.name} className={active ? "active" : ""} type="button" aria-current={active ? "page" : undefined} onClick={() => selectConfigurationGroup(group.name)}><span>{group.name}</span><small className={dirtyCount ? "dirty" : ""}>{dirtyCount ? `${dirtyCount} 项修改` : `${group.fields.length} 项`}</small></button>;
                })}
              </nav>
              <p className="settings-navigation-label settings-management-label">系统管理</p>
              <nav className="settings-management-navigation" aria-label="系统管理">
                <SystemNavigationButton active={selection.kind === "system" && selection.section === "access"} label="访问凭据" detail="密钥与初始密码" onClick={() => selectSystemSection("access")} />
                <SystemNavigationButton active={selection.kind === "system" && selection.section === "backups"} label="安全归档" detail={`${workspace.data.backups.count} 个`} onClick={() => selectSystemSection("backups")} />
                <SystemNavigationButton active={selection.kind === "system" && selection.section === "storage"} label="本地数据" detail="路径与权限" onClick={() => selectSystemSection("storage")} />
                <SystemNavigationButton active={selection.kind === "system" && selection.section === "audit"} label="审计记录" detail="最近操作" onClick={() => selectSystemSection("audit")} />
              </nav>
            </div>
          </div>
        </aside>

        <div className="settings-workspace-content" ref={workspaceContentRef}>
          {selection.kind === "configuration" && selectedGroup ? (
            <form className="configuration-panel" onSubmit={(event) => { event.preventDefault(); requestSave(); }}>
              <header className="configuration-panel-head"><p>{configurationGroupDescription(selectedGroup.name)}</p><span className="configuration-field-count">{selectedGroup.name === "推理强度策略" ? `${reasoningEfforts.length} 个强度 · ${selectedGroup.fields.length} 项配置` : `${selectedGroup.fields.length} 项配置`}</span></header>
              <div className="configuration-groups"><section className="configuration-group" aria-label={selectedGroup.name}><div className="configuration-fields">
                {selectedGroup.name === "品牌与身份" ? <BrandingLogoEditor custom={general.data.branding.custom_logo} sha256={general.data.branding.logo_sha256} pending={logoMutation.isPending || logoResetMutation.isPending} error={logoError} onFile={(file) => { const error = validateLogoFile(file); setLogoError(error); if (!error) logoMutation.mutate(file); }} onReset={() => setLogoResetOpen(true)} /> : null}
                {selectedGroup.name === "企业微信通知" ? <NotificationIntegration status={notification.data.notifications} value={webhookDraft} error={webhookError} saving={webhookMutation.isPending} clearing={webhookClearMutation.isPending} sending={notificationSendMutation.isPending} onChange={(value) => { setWebhookDraft(value); setWebhookError(""); }} onSave={() => webhookMutation.mutate()} onClear={() => setWebhookClearOpen(true)} onSend={() => notificationSendMutation.mutate()} /> : null}
                {selectedGroup.name === "推理强度策略" ? <ReasoningStrategyEditor fields={selectedGroup.fields} draft={draft} onChange={(field, value) => setDraft((current) => ({ ...current, [field.key]: value }))} /> : selectedGroup.fields.map((field) => <ConfigurationEditor key={field.key} field={{ ...field, group: selectedGroup.name }} value={draft[field.key]} error={errors[field.key]} dirty={dirtyFields.some((item) => item.key === field.key)} onChange={(value) => setDraft((current) => ({ ...current, [field.key]: value }))} />)}
              </div></section></div>
              {selectedGroup.name === "用户额度" ? <QuotaSystemDanger summary={quotaOperations.data} pending={quotaOperations.isPending || quotaOperations.isFetching} failed={quotaOperations.isError} onReset={() => setQuotaResetOpen(true)} /> : null}
              {saveMutation.isError ? <p className="form-error" role="alert">{saveMutation.error instanceof Error ? saveMutation.error.message : "配置未保存"}</p> : null}
              <p className="form-error" role="alert">{saveError}</p>
              <div className="configuration-actions"><div className="configuration-change-summary"><span className={`status-chip ${dirtyFields.length ? "warning" : "neutral"}`}>{dirtyFields.length ? `${dirtyFields.length} 项未保存` : "未修改"}</span><div className="configuration-impact-summary">{dirtyModes.size ? [...dirtyModes.entries()].map(([label, count]) => <span key={label}><strong>{count}</strong>{label}</span>) : <span>修改后将在这里汇总生效影响</span>}</div></div><div className="configuration-action-buttons"><button className="button button-quiet" type="button" disabled={!dirtyFields.length || saveMutation.isPending} onClick={() => { setDraft(configurationDraft(catalog.data)); setSaveError(""); }}>撤销未保存修改</button><button className="button button-primary" type="submit" disabled={!dirtyFields.length || saveMutation.isPending}>{saveMutation.isPending ? "正在保存…" : "保存配置"}</button></div></div>
            </form>
          ) : null}
          {selection.kind === "configuration" && !selectedGroup ? (
            <div className="configuration-empty-state" role="status">
              <div className="empty-icon" aria-hidden="true">⚙</div>
              <h3>当前没有可配置项</h3>
              <p>仍可进入系统管理查看访问凭据、归档、本地数据和审计记录。</p>
              <button className="button button-primary" type="button" onClick={() => selectSystemSection("access")}>进入访问凭据</button>
            </div>
          ) : null}
          {selection.kind === "system" && selection.section === "access" ? <AccessPanel managementKeyConfigured={general.data.security.management_key_configured} initialPasswordConfigured={general.data.security.initial_password_configured} onInitialPassword={() => setInitialPasswordOpen(true)} onManagementKey={() => setManagementKeyOpen(true)} /> : null}
          {selection.kind === "system" && selection.section === "backups" ? <BackupsPanel count={workspace.data.backups.count} latest={workspace.data.backups.latest} /> : null}
          {selection.kind === "system" && selection.section === "storage" ? <StoragePanel rows={workspace.data.storage} /> : null}
          {selection.kind === "system" && selection.section === "audit" ? <AuditPanel rows={workspace.data.recent_audit} /> : null}
        </div>
      </div>

      <InitialPasswordModal open={initialPasswordOpen} csrfToken={csrfToken} onClose={() => setInitialPasswordOpen(false)} onSuccess={(message) => { setInitialPasswordOpen(false); showToast(message); void general.refetch(); }} />
      <Modal
        className="legacy-account-editor-modal legacy-settings-form-modal"
        open={managementKeyOpen}
        width={560}
        centered
        title={<div className="legacy-dialog-title"><strong>更换管理密钥</strong><span>ACCESS CONTROL</span></div>}
        closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
        transitionName=""
        maskTransitionName=""
        afterOpenChange={(visible) => { if (visible) managementKeyForm.setFocus("newKey"); }}
        onCancel={() => { if (managementKeyMutation.isPending) return; setManagementKeyOpen(false); managementKeyForm.reset(); managementKeyMutation.reset(); }}
        destroyOnHidden
        footer={[
          <Button key="cancel" className="legacy-modal-ghost" disabled={managementKeyMutation.isPending} onClick={() => { setManagementKeyOpen(false); managementKeyForm.reset(); managementKeyMutation.reset(); }}>取消</Button>,
          <Button key="submit" type="primary" htmlType="submit" form="settings-management-key-form" disabled={managementKeyMutation.isPending}>{managementKeyMutation.isPending ? "正在更新…" : "更新并重新进入"}</Button>
        ]}
      >
        <p className="warning-banner">更新后当前及其他管理会话立即失效；API Key、用户会话和数据面流量不变。</p>
        <form id="settings-management-key-form" noValidate onSubmit={managementKeyForm.handleSubmit(() => managementKeyMutation.mutate())}>
          <div className="field"><label htmlFor="settings-management-key">新管理密钥</label><Controller control={managementKeyForm.control} name="newKey" render={({ field }) => <LegacyPasswordInput id="settings-management-key" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={12} maxLength={128} onValueChange={field.onChange} />} /></div>
          <div className="field account-email-field"><label htmlFor="settings-management-key-confirmation">再次输入</label><Controller control={managementKeyForm.control} name="confirmation" render={({ field }) => <LegacyPasswordInput id="settings-management-key-confirmation" ariaLabel="再次输入管理密钥" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={12} maxLength={128} onValueChange={field.onChange} />} /></div>
          <p className="form-error" role="alert">{managementKeyError}</p>
        </form>
      </Modal>
      <LegacyConfirmModal title={`保存 ${dirtyFields.length} 项配置？`} open={confirmOpen} okText="保存并应用" confirmLoading={saveMutation.isPending} onCancel={() => !saveMutation.isPending && setConfirmOpen(false)} onOk={() => saveMutation.mutate()}><p>{riskyEffects.join("；")}。如果应用失败，系统会尝试恢复原配置。</p></LegacyConfirmModal>
      <LegacyConfirmModal title="恢复默认 Logo？" open={logoResetOpen} okText="恢复默认" confirmLoading={logoResetMutation.isPending} onCancel={() => !logoResetMutation.isPending && setLogoResetOpen(false)} onOk={() => { setLogoResetOpen(false); logoResetMutation.mutate(); }}><p>已上传的 Logo 将从控制面数据库删除，页面立即恢复开源默认 Logo。</p></LegacyConfirmModal>
      <LegacyConfirmModal title="清除企业微信 Webhook？" open={webhookClearOpen} okText="确认清除" danger confirmLoading={webhookClearMutation.isPending} onCancel={() => !webhookClearMutation.isPending && setWebhookClearOpen(false)} onOk={() => { setWebhookClearOpen(false); webhookClearMutation.mutate(); }}><p>Webhook 地址会从本地删除，同时关闭企业微信通知。</p></LegacyConfirmModal>
      <Modal className="legacy-settings-modal" open={quotaResetOpen} title="清零全部用户本周已用量" okText="确认清零" cancelText="取消" okButtonProps={{ danger: true }} confirmLoading={quotaResetMutation.isPending} onCancel={() => { if (quotaResetMutation.isPending) return; setQuotaResetOpen(false); quotaResetForm.reset(); quotaResetMutation.reset(); }} onOk={() => void quotaResetForm.handleSubmit(() => quotaResetMutation.mutate())()} destroyOnHidden>
        <p className="warning-banner">该操作不删除原始事件，但会立即改变所有有用量用户的本周剩余额度。操作不可撤销。</p>
        {quotaResetMutation.isError ? <Alert type="error" showIcon message={quotaResetMutation.error instanceof Error ? quotaResetMutation.error.message : "本周用量未清零"} /> : null}
        <Form layout="vertical" requiredMark={false}><Controller control={quotaResetForm.control} name="reason" render={({ field, fieldState }) => <Form.Item label="操作原因" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}><Input.TextArea {...field} aria-label="操作原因" autoSize={{ minRows: 3, maxRows: 5 }} /></Form.Item>} /><Controller control={quotaResetForm.control} name="confirmation" render={({ field, fieldState }) => <Form.Item label="输入 RESET ALL USERS 确认" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}><Input {...field} aria-label="清零确认文字" autoComplete="off" /></Form.Item>} /></Form>
      </Modal>
    </section>
  );
}

function ConfigurationSkeleton() {
  return <section className="page-content legacy-settings-page" aria-label="正在加载配置中心"><div className="settings-workspace settings-workspace-skeleton"><aside className="settings-navigation"><div className="skeleton skeleton-line" /><div className="skeleton skeleton-table" /></aside><div className="configuration-panel"><div className="skeleton skeleton-title" /><div className="skeleton skeleton-table" /></div></div></section>;
}

function SystemNavigationButton({ active, label, detail, onClick }: { active: boolean; label: string; detail: string; onClick: () => void }) {
  return <button className={active ? "active" : ""} type="button" aria-current={active ? "page" : undefined} onClick={onClick}><span>{label}</span><small>{detail}</small></button>;
}

function BrandingLogoEditor({ custom, sha256, pending, error, onFile, onReset }: { custom: boolean; sha256?: string; pending: boolean; error: string; onFile: (file: File) => void; onReset: () => void }) {
  const source = custom ? `/branding/logo${sha256 ? `?v=${encodeURIComponent(sha256.slice(0, 16))}` : ""}` : "/portal/assets/codex-cpa-cluster-logo.svg";
  return <article className="branding-logo-editor"><div className="branding-logo-preview"><img src={source} alt="当前 Logo" /></div><div className="branding-logo-copy"><strong>品牌 Logo</strong><p>支持 PNG、JPEG、GIF、WebP 和经过安全校验的 SVG，最大 2 MiB。保存后立即应用到入口、使用中心和管理登录页。</p><span className={`status-chip ${custom ? "success" : "neutral"}`}>{custom ? "自定义 Logo" : "默认 Logo"}</span></div><div className="branding-logo-actions"><label className="button button-secondary" aria-disabled={pending}>{pending ? "正在上传…" : "选择并上传"}<input type="file" accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml" disabled={pending} hidden onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) onFile(file); }} /></label><button className="button danger-outline" type="button" disabled={!custom || pending} onClick={onReset}>恢复默认</button><small className="form-error" role="alert">{error}</small></div></article>;
}

function NotificationIntegration({ status, value, error, saving, clearing, sending, onChange, onSave, onClear, onSend }: { status: { webhook_configured: boolean; last_success_at: number | null; next_schedule_at: number | null; last_error: string }; value: string; error: string; saving: boolean; clearing: boolean; sending: boolean; onChange: (value: string) => void; onSave: () => void; onClear: () => void; onSend: () => void }) {
  return <article className="notification-integration"><div className="notification-integration-head"><div className="notification-integration-copy"><strong>企业微信群 Webhook</strong><p>与通知开关、发送时间和额度阈值统一配置；固定以 markdown_v2 表格发送常规周额度、近 1 小时用户数、重置次数和刷新时间。</p></div><span className={`status-chip ${status.webhook_configured ? "success" : "neutral"}`}>{status.webhook_configured ? "Webhook 已配置" : "Webhook 未配置"}</span></div><div className="notification-webhook-editor"><label htmlFor="notification-webhook-url-react">Webhook 地址</label><div className="notification-webhook-control"><input id="notification-webhook-url-react" type="url" maxLength={2048} autoComplete="off" value={value} placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..." onChange={(event) => onChange(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); onSave(); } }} /><div className="notification-webhook-actions"><button className="button button-primary" type="button" disabled={saving || !value.trim()} onClick={onSave}>{saving ? "正在保存…" : "保存 Webhook"}</button><button className="button danger-outline" type="button" disabled={!status.webhook_configured || clearing} onClick={onClear}>清除 Webhook</button><button className="button button-quiet" type="button" disabled={!status.webhook_configured || sending} onClick={onSend}>{sending ? "正在发送…" : "发送账号信息"}</button></div></div><p className="notification-webhook-help">{status.webhook_configured ? "当前地址不会回显；重新填写并保存即可替换。" : "仅支持企业微信官方 qyapi.weixin.qq.com 消息推送地址。"}</p><p className="form-error" role="alert">{error}</p></div><div className="notification-status-list"><span>最近成功<strong>{formatFullTime(status.last_success_at)}</strong></span><span>下次发送<strong>{formatFullTime(status.next_schedule_at)}</strong></span>{status.last_error ? <span>最近错误<strong>{status.last_error}</strong></span> : null}</div></article>;
}

function ConfigurationEditor({ field, value, error, dirty, onChange }: { field: EditorField; value: DraftValue; error?: string; dirty: boolean; onChange: (value: DraftValue) => void }) {
  return <article className={`configuration-field${dirty ? " configuration-field-dirty" : ""}`} data-configuration-field={field.key}><div className="configuration-field-copy"><label htmlFor={`configuration-${field.key}`}>{field.label}</label><p>{field.description}</p></div><div className="configuration-field-value"><ConfigurationControl field={field} value={value} onChange={onChange} />{error ? <small className="configuration-control-error">{error}</small> : null}</div><div className="configuration-field-meta"><span className="configuration-apply">{applyModeLabel(field.apply_mode, field.key)}</span><code>{field.key}</code><small>默认 {configurationDefaultText(field)}</small></div></article>;
}

function ConfigurationControl({ field, value, onChange }: { field: ConfigurationField; value: DraftValue; onChange: (value: DraftValue) => void }) {
  const id = `configuration-${field.key}`;
  if (field.type === "boolean") return <div className="configuration-field-control boolean-control"><label><input id={id} type="checkbox" checked={Boolean(value)} onChange={(event) => onChange(event.target.checked)} /><span>{value ? "已启用" : "已关闭"}</span></label></div>;
  if (field.type === "choice") return <div className="configuration-choice-control"><select id={id} aria-label={field.label} value={String(value ?? "")} onChange={(event) => onChange(event.target.value)}>{(field.choices ?? []).map((choice) => <option key={choice.value} value={choice.value}>{choice.label} · {choice.value}</option>)}</select><div className="configuration-choice-address"><span>{sameConfigurationValue(normalizeDraftValue(field, value), field.value) ? "当前地址" : "待切换地址"}</span><code>{String(value ?? "")}</code></div></div>;
  if (field.type === "color") { const color = /^#[0-9a-f]{6}$/i.test(String(value ?? "")) ? String(value) : "#687287"; return <div className="reasoning-color-inputs"><label className="reasoning-color-swatch"><input type="color" value={color} aria-label={`选择${field.label}颜色`} onChange={(event) => onChange(event.target.value)} /></label><input id={id} className="reasoning-color-hex" type="text" value={String(value ?? "")} maxLength={7} pattern="#[0-9A-Fa-f]{6}" onChange={(event) => onChange(event.target.value)} /></div>; }
  const numeric = ["integer", "number", "nullable_integer"].includes(field.type);
  const tokenInput = numeric && field.unit === "Token";
  const input = field.type === "proxy_url_secret"
    ? <LegacyPasswordInput id={id} value={value == null ? "" : String(value)} placeholder={field.configured ? "已配置；留空保持不变" : "例如 socks5://user:pass@host:1080"} onValueChange={onChange} />
    : <input id={id} type={numeric ? "number" : "text"} value={value == null ? "" : String(value)} min={field.min} max={field.max} step={field.type === "number" ? "any" : numeric ? 1 : undefined} placeholder={field.type === "nullable_integer" ? "不限额" : undefined} autoComplete="off" onChange={(event) => onChange(event.target.value)} />;
  if (tokenInput) { const presentation = tokenInputPresentation(typeof value === "boolean" ? null : value, field.type === "nullable_integer" ? "留空表示不限额" : "请输入 Token 数量"); return <div className="configuration-token-control token-input-control">{input}<div className="token-input-preview" data-state={presentation.state}>{presentation.state === "ready" ? <><strong>{presentation.compact}</strong>{presentation.localized ? <span>{presentation.localized}</span> : null}<small>精确值 {presentation.exact}</small></> : <small>{presentation.state === "empty" ? presentation.emptyLabel : "请输入有效的正整数 Token 数量"}</small>}</div></div>; }
  return field.type === "nullable_integer" ? <div className="configuration-nullable-control">{input}<small>留空表示不限额</small></div> : input;
}

function LegacyConfirmModal({ title, open, children, okText, danger = false, confirmLoading = false, onCancel, onOk }: { title: string; open: boolean; children: ReactNode; okText: string; danger?: boolean; confirmLoading?: boolean; onCancel: () => void; onOk: () => void }) {
  return <Modal className="legacy-confirm-modal" title={<span className="sr-only">{title}</span>} open={open} width={430} centered closable={false} transitionName="" maskTransitionName="" onCancel={onCancel} destroyOnHidden footer={[<Button key="cancel" disabled={confirmLoading} onClick={onCancel}>取消</Button>, <Button key="confirm" type={danger ? "default" : "primary"} danger={danger} loading={confirmLoading} onClick={onOk}>{okText}</Button>]}><div className="legacy-confirm-body"><div className="legacy-confirm-icon" aria-hidden="true">!</div><h3>{title}</h3><div className="legacy-confirm-message">{children}</div></div></Modal>;
}

function ReasoningStrategyEditor({ fields, draft, onChange }: { fields: ConfigurationField[]; draft: Draft; onChange: (field: ConfigurationField, value: DraftValue) => void }) {
  const fieldFor = (prefix: string, effort: string) => fields.find((field) => field.key === `${prefix}${effort}`);
  const canvasRef = useRef<HTMLCanvasElement>(null);
  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas) return;
    const draw = () => {
      const width = Math.max(320, Math.round(canvas.getBoundingClientRect().width || 800));
      const height = 58;
      const ratio = Math.max(1, window.devicePixelRatio || 1);
      canvas.width = Math.round(width * ratio);
      canvas.height = Math.round(height * ratio);
      const context = canvas.getContext("2d");
      if (!context) return;
      context.scale(ratio, ratio);
      const segmentWidth = width / reasoningEfforts.length;
      reasoningEfforts.forEach((effort, index) => {
        const field = fieldFor(reasoningColorPrefix, effort);
        const presentation = reasoningColorPresentation(field ? String(draft[field.key] ?? field.value) : "", String(field?.default ?? "#687287"));
        context.fillStyle = presentation.color;
        context.fillRect(index * segmentWidth, 0, Math.ceil(segmentWidth), height);
        context.fillStyle = presentation.text;
        context.font = "600 9px ui-monospace, SFMono-Regular, Menlo, monospace";
        context.textAlign = "center";
        context.textBaseline = "middle";
        context.fillText(effort, (index + 0.5) * segmentWidth, height / 2, Math.max(24, segmentWidth - 8));
      });
    };
    draw();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(draw);
    observer.observe(canvas);
    return () => observer.disconnect();
  }, [draft, fields]);
  return <section className="reasoning-strategy-editor" aria-label="推理强度倍率与颜色"><div className="reasoning-strategy-scope"><div><strong>用户额度倍率</strong><span>仅影响后续新采集事件，不追溯历史。</span></div><div><strong>账号明细颜色</strong><span>固定应用于所有模型，保存后立即生效。</span></div></div><div className="reasoning-color-preview"><div><strong>配色预览</strong><span>每种推理强度使用固定颜色</span></div><canvas ref={canvasRef} height="58" role="img" aria-label="推理强度颜色预览" /></div><div className="reasoning-strategy-table"><div className="reasoning-strategy-table-head"><span>推理强度</span><span>用户额度倍率 <small>下次采集</small></span><span>账号明细颜色 <small>立即生效</small></span></div>{reasoningEfforts.map((effort) => { const multiplier = fieldFor(reasoningMultiplierPrefix, effort); const color = fieldFor(reasoningColorPrefix, effort); if (!multiplier || !color) return null; const multiplierDirty = !sameConfigurationValue(normalizeDraftValue(multiplier, draft[multiplier.key]), multiplier.value); const colorDirty = !sameConfigurationValue(normalizeDraftValue(color, draft[color.key]), color.value); return <div className="reasoning-strategy-row" key={effort}><div className="reasoning-strategy-name"><strong>{reasoningEffortLabel(effort)}</strong><code>{effort}</code></div><div className={`reasoning-strategy-control${multiplierDirty ? " configuration-field-dirty" : ""}`} data-configuration-field={multiplier.key}><div className="reasoning-multiplier-input"><input type="number" value={String(draft[multiplier.key] ?? multiplier.value)} min={multiplier.min} max={multiplier.max} step="any" aria-label={`${reasoningEffortLabel(effort)}用户额度倍率`} onChange={(event) => onChange(multiplier, event.target.value)} /><span>倍</span></div></div><div className={`reasoning-strategy-control${colorDirty ? " configuration-field-dirty" : ""}`} data-configuration-field={color.key}><ConfigurationControl field={color} value={draft[color.key]} onChange={(value) => onChange(color, value)} /></div></div>; })}</div><div className="reasoning-strategy-defaults"><button className="button button-quiet" type="button" onClick={() => fields.filter((field) => field.key.startsWith(reasoningMultiplierPrefix)).forEach((field) => onChange(field, draftValueFromConfiguration(field.default, field.type)))}>恢复默认倍率</button><button className="button button-quiet" type="button" onClick={() => fields.filter((field) => field.key.startsWith(reasoningColorPrefix)).forEach((field) => onChange(field, draftValueFromConfiguration(field.default, field.type)))}>恢复默认配色</button></div></section>;
}

function QuotaSystemDanger({ summary, pending, failed, onReset }: { summary?: { total_users: number; users_with_usage: number; total_used_tokens: number; total_raw_used_tokens: number; week_end_at: number | null }; pending: boolean; failed: boolean; onReset: () => void }) {
  const available = Boolean(summary) && !failed;
  const canReset = available && Number(summary?.users_with_usage ?? 0) > 0;
  return <section className="quota-system-danger" aria-label="全员额度危险操作"><div className="quota-system-danger-copy"><strong>全员本周用量清零</strong><p>仅用于系统异常后的统一补偿。原始 Token 事件、用户额度策略和本周追加额度都会保留；提交前必须填写原因并输入确认文字。</p><div className="quota-system-danger-metrics">{available && summary ? <><span>{summary.total_users.toLocaleString("zh-CN")} 位用户</span><span>{summary.users_with_usage.toLocaleString("zh-CN")} 位有用量</span><span>当前加权已用 {tokenReadableText(summary.total_used_tokens)}</span><span>未加权累计 {tokenReadableText(summary.total_raw_used_tokens)}</span><span>{formatFullTime(summary.week_end_at)} 自动换周</span></> : <span>{pending ? "正在确认影响范围" : "影响范围暂不可确认，请刷新配置后重试"}</span>}</div></div><button className="button danger-outline" type="button" disabled={!canReset || pending} onClick={onReset}>{pending ? "正在确认影响范围" : !available ? "影响范围暂不可确认" : canReset ? "清零全部用户本周已用量" : "当前无需清零"}</button></section>;
}

function AccessPanel({ managementKeyConfigured, initialPasswordConfigured, onInitialPassword, onManagementKey }: { managementKeyConfigured: boolean; initialPasswordConfigured: boolean; onInitialPassword: () => void; onManagementKey: () => void }) {
  return <section className="settings-secondary-panel"><div className="settings-panel-meta"><span className={`status-chip ${managementKeyConfigured ? "success" : "danger"}`}>{managementKeyConfigured ? "管理密钥已配置" : "管理密钥未配置"}</span></div><div className="settings-panel-body"><p>管理密钥用于进入本界面；用户初始密码只用于新建或重置用户，均保存在控制面加密数据库中。</p><div className="settings-panel-callout"><strong>用户初始密码</strong><span>{initialPasswordConfigured ? "已安全配置；当前值不会回显" : "未配置；新建和重置用户将被拒绝"}</span></div><div className="settings-panel-callout"><strong>变更影响</strong><span>更换初始密码不会修改已有用户；更换管理密钥会退出后台并重启相关服务。</span></div></div><footer><div className="button-group"><button className="button button-secondary" type="button" onClick={onInitialPassword}>设置用户初始密码</button>{" "}<button className="button button-secondary" type="button" disabled={!managementKeyConfigured} onClick={onManagementKey}>更换管理密钥</button></div></footer></section>;
}
function BackupsPanel({ count, latest }: { count: number; latest: string }) { return <section className="settings-secondary-panel"><div className="settings-panel-meta"><strong>{count} 个归档</strong></div><div className="settings-panel-body"><p>删除 CPA 或清除 OAuth 前会自动归档配置、授权文件和相关记录。</p><div className="settings-panel-callout"><strong>最近归档</strong><span className="settings-path">{latest || "暂无归档"}</span></div></div></section>; }
function StoragePanel({ rows }: { rows: Array<{ label: string; path: string; exists: boolean; mode: string }> }) { return <section className="settings-secondary-panel"><p className="settings-panel-intro">敏感注册表只显示路径与权限，不展示内容</p><div className="panel table-panel settings-table-panel"><NativeTableViewport className="table-wrap" aria-label="存储状态表格"><table className="storage-table"><thead><tr><th className="table-index-column">序号</th><th>数据</th><th>本地路径</th><th>状态</th><th>权限</th></tr></thead><tbody>{rows.map((item, index) => <tr key={item.path}><td className="table-index-cell">{index + 1}</td><td><span className="table-primary">{item.label}</span></td><td><span className="settings-path">{item.path}</span></td><td><span className={`status-chip ${item.exists ? "success" : "neutral"}`}>{item.exists ? "已创建" : "尚未创建"}</span></td><td><span className="settings-path">{item.mode}</span></td></tr>)}</tbody></table></NativeTableViewport></div></section>; }
function AuditPanel({ rows }: { rows: Array<{ timestamp: number; action: string; target: string; outcome: string }> }) { return <section className="settings-secondary-panel"><p className="settings-panel-intro">只记录动作、目标与结果，不记录管理密钥或完整 Key</p><div className="panel table-panel settings-table-panel"><NativeTableViewport className="table-wrap" aria-label="管理审计表格"><table className="audit-table"><thead><tr><th className="table-index-column">序号</th><th>时间</th><th>动作</th><th>目标</th><th>结果</th></tr></thead><tbody>{rows.map((item, index) => <tr key={`${item.timestamp}-${index}`}><td className="table-index-cell">{index + 1}</td><td>{formatTime(item.timestamp)}</td><td><span className="settings-path">{item.action}</span></td><td>{item.target}</td><td><span className={`status-chip ${item.outcome === "accepted" ? "success" : "neutral"}`}>{item.outcome || "unknown"}</span></td></tr>)}</tbody></table></NativeTableViewport>{!rows.length ? <div className="empty-state compact-empty"><h3>暂无管理操作</h3><p>后续变更会显示在这里。</p></div> : null}</div></section>; }

function flattenConfiguration(catalog?: ConfigurationCatalog): EditorField[] { return catalog?.groups.flatMap((group) => group.fields.map((field) => ({ ...field, group: group.name }))) ?? []; }
function configurationDraft(catalog: ConfigurationCatalog): Draft { return Object.fromEntries(flattenConfiguration(catalog).map((field) => [field.key, draftValueFromConfiguration(field.value, field.type)])); }
function draftValueFromConfiguration(value: ConfigurationValue, type: ConfigurationField["type"]): DraftValue { if (type === "domain_list") return Array.isArray(value) ? value.join(", ") : ""; if (type === "proxy_url_secret") return ""; if (Array.isArray(value)) return value.join(", "); return value; }
function normalizeDraftValue(field: ConfigurationField, value: DraftValue): ConfigurationValue { if (field.type === "proxy_url_secret" && String(value ?? "").trim() === "") return field.value; if (field.type === "domain_list") return [...new Set(String(value ?? "").split(/[,，\s]+/).map((item) => item.trim().toLocaleLowerCase("zh-CN")).filter(Boolean))]; if (field.type === "boolean") return Boolean(value); if (["integer", "number", "nullable_integer"].includes(field.type)) { if (field.type === "nullable_integer" && String(value ?? "").trim() === "") return null; const number = Number(value); return Number.isFinite(number) ? number : String(value ?? "").trim(); } if (typeof value === "string") return value.trim(); return value; }
function sameConfigurationValue(left: ConfigurationValue, right: ConfigurationValue): boolean { return JSON.stringify(left) === JSON.stringify(right); }
function validateDraftValue(field: ConfigurationField, raw: DraftValue): string { if (field.type === "nullable_integer" && String(raw ?? "").trim() === "") return ""; if (["integer", "number", "nullable_integer"].includes(field.type)) { const value = Number(raw); if (!Number.isFinite(value)) return "请输入有效数字"; if ((field.type === "integer" || field.type === "nullable_integer") && !Number.isInteger(value)) return "请输入整数"; if (field.min !== undefined && value < field.min) return `不能小于 ${field.min}`; if (field.max !== undefined && value > field.max) return `不能大于 ${field.max}`; return ""; } if (field.type === "boolean" || field.type === "choice" || field.type === "domain_list") return ""; const value = String(raw ?? "").trim(); if (field.type === "proxy_url_secret" && value === "") return ""; if (["optional_text", "optional_image", "base_url"].includes(field.type) && value === "") return ""; if (!value) return "不能为空"; if (field.min_length !== undefined && [...value].length < field.min_length) return `至少输入 ${field.min_length} 个字符`; if (field.max_length !== undefined && [...value].length > field.max_length) return `最多输入 ${field.max_length} 个字符`; if (field.type === "key_prefix" && !/^[a-z][a-z0-9_]{1,30}_$/.test(value)) return "请输入 3-32 位小写前缀，并以下划线结尾"; if (field.type === "env_name" && !/^[A-Z][A-Z0-9_]{1,63}$/.test(value)) return "请输入有效的大写环境变量名"; if (field.type === "color" && !/^#[0-9a-fA-F]{6}$/.test(value)) return "请输入 #RRGGBB 颜色"; if (field.type === "duration" && !/^[1-9][0-9]*[smhd]$/.test(value)) return "请输入 30s、5m、1h 或 7d 格式"; if (field.type === "time_list" && !/^([01]?\d|2[0-3]):[0-5]\d(?:\s*[,，]\s*([01]?\d|2[0-3]):[0-5]\d)*$/.test(value)) return "请输入 HH:MM，多个时间使用逗号分隔"; if ((field.type === "base_url" || field.type === "proxy_url_secret") && !validConfigurationURL(value, field.type === "proxy_url_secret")) return "请输入有效的 HTTP(S) 或 SOCKS5 根地址"; if (field.type === "ip" && !validIPv4(value)) return "请输入有效 IPv4 地址"; if ((field.type === "image" || field.type === "optional_image") && !/^[A-Za-z0-9._:/@-]+$/.test(value)) return "镜像名称格式无效"; if (field.digest_required && !/^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$/.test(value)) return "必须使用 name:tag@sha256:digest 固定镜像"; return ""; }
function validConfigurationURL(value: string, proxy: boolean): boolean { try { const parsed = new URL(value); if (!["http:", "https:", ...(proxy ? ["socks5:"] : [])].includes(parsed.protocol)) return false; return Boolean(parsed.hostname) && parsed.pathname === "/" && !parsed.search && !parsed.hash && (proxy || (!parsed.username && !parsed.password)); } catch { return false; } }
function validIPv4(value: string): boolean { const parts = value.split("."); return parts.length === 4 && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) >= 0 && Number(part) <= 255); }
function configurationDefaultText(field: ConfigurationField): string { if (!Object.prototype.hasOwnProperty.call(field, "default")) return ""; if (field.type === "boolean") return field.default ? "开启" : "关闭"; if (field.type === "nullable_integer" && field.default == null) return "不限额"; return `${field.default ?? ""}${field.unit ? ` ${field.unit}` : ""}`; }
function applyModeLabel(mode: ConfigurationField["apply_mode"], key = ""): string { if (key === "runtime.cliproxy_image") return "镜像管理"; return ({ live: "立即生效", accounts: "重建业务 CPA", collector: "重启采集器", future: "仅新账号", deployment: "下次部署", quota: "下次采集生效" })[mode]; }
function configurationEffects(fields: EditorField[]): string[] { const modes = new Set(fields.map((field) => field.apply_mode)); return [modes.has("accounts") ? "业务 CPA 会依次重建" : "", modes.has("collector") ? "用量采集器会重启" : "", modes.has("quota") ? "用户额度将在下次采集后生效" : "", modes.has("deployment") ? "部署参数仅保存，等待下次重建" : ""].filter(Boolean); }
function validateLogoFile(file: File): string { if (!supportedLogoTypes.has(file.type)) return "仅支持 PNG、JPEG、GIF、WebP 或 SVG 文件"; if (file.size < 1) return "Logo 文件不能为空"; if (file.size > maxLogoBytes) return "Logo 文件不能超过 2 MiB"; if ([...file.name].length > 128) return "Logo 文件名不能超过 128 个字符"; return ""; }
function reasoningEffortLabel(effort: string): string { return ({ none: "无", minimal: "最小", low: "低", medium: "中", high: "高", xhigh: "极高", max: "最大", ultra: "超高", auto: "自动", unknown: "未知" } as Record<string, string>)[effort] ?? effort; }
function configurationGroupDescription(name: string): string { return ({ "品牌与身份": "统一管理公开名称、Logo、邮箱域名、新 Key 前缀和客户端导出参数", "CPA 请求": "统一作用于所有业务 CPA", "用量与额度": "额度查询与用量事件策略", "账号自动切换": "官方账号额度耗尽后按剩余资源批量迁移用户路由", "用户额度": "全部用户的系统默认周额度与网关故障策略", "推理强度策略": "同一处管理用户额度倍率和账号 Token 明细配色；两类配置独立生效", "企业微信通知": "定时发送 markdown_v2 额度表格并执行阈值预警", "会话与采集": "用户会话和采集器吞吐", "账号供应": "后续创建账号时使用", "部署环境": "保存到 .env，不中断当前入口", "系统约束": "为安全和协议兼容保持只读" } as Record<string, string>)[name] ?? ""; }
function configurationHeadingEyebrow(name: string): string { return ({ "品牌与身份": "BRAND & IDENTITY", "CPA 请求": "CPA REQUESTS", "用量与额度": "USAGE & QUOTAS", "账号自动切换": "ACCOUNT FAILOVER", "用户额度": "USER QUOTAS", "推理强度策略": "REASONING EFFORT", "企业微信通知": "WECOM NOTIFICATIONS", "会话与采集": "SESSIONS & COLLECTION", "账号供应": "ACCOUNT PROVISIONING", "部署环境": "DEPLOYMENT ENVIRONMENT", "系统约束": "SYSTEM CONSTRAINTS" } as Record<string, string>)[name] ?? "CONFIGURATION GROUP"; }
function settingsSectionHeading(section: SystemSection): { title: string; eyebrow: string } { return ({ access: { title: "访问凭据", eyebrow: "ACCESS CONTROL" }, backups: { title: "安全归档", eyebrow: "RECOVERY" }, storage: { title: "本地数据", eyebrow: "LOCAL STORAGE" }, audit: { title: "审计记录", eyebrow: "AUDIT TRAIL" } })[section]; }
function reasoningColorPresentation(value: string, fallback = "#687287") { const color = /^#[0-9a-f]{6}$/i.test(value) ? value.toLowerCase() : fallback; const channels = [1, 3, 5].map((index) => Number.parseInt(color.slice(index, index + 2), 16) / 255).map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4); const luminance = 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2]; return { color, text: luminance > 0.179 ? "#171d2b" : "#ffffff" }; }
function formatTime(timestamp: number): string { if (!timestamp) return "—"; return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(timestamp * 1_000)); }
function formatFullTime(timestamp: number | null): string { if (!timestamp) return "—"; return new Intl.DateTimeFormat("zh-CN", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false }).format(new Date(timestamp * 1_000)); }
