import { useSiteTimezone, formatSiteTimestamp } from "./site-time";
import { Alert, Button, Form, Input, Modal } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type InputHTMLAttributes, type ReactNode } from "react";
import { Controller, useForm } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { z } from "zod";
import { useSearchParams } from "react-router-dom";

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
  sendNotification,
  testNotification,
  type NotificationSettings,
  type NotificationStatus
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
import { PageState } from "./components/PageState";
import { formatTokenAmount, tokenInputPresentation, tokenReadableParts, tokenReadableText } from "./formatters";
import { InitialPasswordModal } from "./InitialPasswordModal";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { LegacyPasswordInput } from "./components/LegacyPasswordInput";

import { TimezoneSelect } from "./components/TimezoneSelect";
import { publicSiteQueryKey } from "../api/public-site";
import { useTheme } from "./ThemeProvider";

import { configurationCategories, configurationControlWidth, configurationSections, configurationSectionFor, legacyConfigurationSection, type ConfigurationCategory } from "./configuration-layout";
import "./configuration-page.css";

type DraftValue = string | number | boolean | null;
type Draft = Record<string, DraftValue>;
type EditorField = ConfigurationField & { group: ConfigurationCategory; section: string };
const ConfigurationSavingContext = createContext(false);

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
const modelMultiplierPrefix = "user_quota.model_multiplier.";
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
  useSiteTimezone();
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel, setPageDetail } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const [category, setCategory] = useState<ConfigurationCategory>("品牌与身份");
  const [selectedSectionId, setSelectedSectionId] = useState("brand");
  const [expandedCategories, setExpandedCategories] = useState<Record<string, boolean>>(() => Object.fromEntries(configurationCategories.map(({ name }) => [name, true])));
  const [mobileNavigationOpen, setMobileNavigationOpen] = useState(false);
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
  const [webhookEditing, setWebhookEditing] = useState(false);
  const [webhookError, setWebhookError] = useState("");
  const [webhookClearOpen, setWebhookClearOpen] = useState(false);
  const [quotaResetOpen, setQuotaResetOpen] = useState(false);
  const workspaceContentRef = useRef<HTMLDivElement>(null);
  const [searchParams, setSearchParams] = useSearchParams();
  const handledDeepLink = useRef("");

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
  const fields = useMemo(() => flattenConfiguration(catalog.data), [catalog.data]);
  const availableSections = useMemo(() => configurationSections.filter((section) =>
    fields.some((field) => field.section === section.id)
    || ["access", "backups", "storage", "audit", "quota", "notifications"].includes(section.id)
    || (section.id === "brand" && fields.length > 0)
  ), [fields]);
  const activeSection = availableSections.find((section) => section.category === category && section.id === selectedSectionId)
    ?? availableSections.find((section) => section.category === category);
  const quotaOperations = useQuery({
    queryKey: userQuotaOperationsQueryKey,
    queryFn: ({ signal }) => readUserQuotaOperations(signal),
    enabled: activeSection?.id === "quota",
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });

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
  }, [catalog.data]);

  useEffect(() => {
    if (!catalog.data) return;
    const signature = searchParams.toString();
    if (handledDeepLink.current === signature) return;
    handledDeepLink.current = signature;
    const key = searchParams.get("key") ?? "";
    const field = fields.find((item) => item.key === key);
    const requestedSection = searchParams.get("section") ?? "";
    const section = availableSections.find((item) => item.id === requestedSection)
      ?? legacyConfigurationSection(requestedSection);
    const group = searchParams.get("group") ?? "";
    const current = configurationCategories.find((item) => item.name === group);
    const legacy = current ? undefined : legacyConfigurationSection(group);
    const requestedCategory = field?.group ?? section?.category ?? current?.name ?? legacy?.category ?? "品牌与身份";
    const destination = availableSections.some((item) => item.category === requestedCategory) ? requestedCategory : "品牌与身份";
    setCategory(destination);
    const targetSection = field?.section ?? section?.id ?? legacy?.id
      ?? availableSections.find((item) => item.category === destination)?.id ?? "";
    setSelectedSectionId(targetSection);
    setExpandedCategories((previous) => ({ ...previous, [destination]: true }));
    setFocusKey(field?.key ?? (requestedSection === "quota-reset" || group === "用量维护" ? "quota-reset" : ""));
    setSearch("");
    setMobileNavigationOpen(false);
  }, [availableSections, catalog.data, fields, searchParams]);

  useEffect(() => {
    const current = configurationCategories.find((item) => item.name === category)!;
    setPageDetail({ title: current.name, sectionTitle: activeSection?.title, eyebrow: current.eyebrow });
    return () => setPageDetail(null);
  }, [activeSection, category, setPageDetail]);

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
    target.classList.add("configuration-field-highlight");
    target.scrollIntoView?.({ block: "center", behavior: "smooth" });
    target.querySelector<HTMLElement>('input:not([type="hidden"]):not([type="file"]):not(:disabled), select:not(.enhanced-select-native):not(:disabled), textarea:not(:disabled), button:not(:disabled)')?.focus({ preventScroll: true });
    const timer = window.setTimeout(() => target.classList.remove("configuration-field-highlight"), 1_600);
    return () => { window.clearTimeout(timer); target.classList.remove("configuration-field-highlight"); };
  }, [focusKey, category, selectedSectionId, catalog.data, general.data, notification.data, workspace.data]);

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
      await queryClient.invalidateQueries({ queryKey: publicSiteQueryKey });
      const refreshed = await catalog.refetch();
      if (refreshed.data) setDraft(configurationDraft(refreshed.data));
      if (dirtyFields.some((field) => field.key.startsWith(reasoningColorPrefix))) {
        const stylesheet = document.querySelector<HTMLLinkElement>('link[href*="reasoning-effort-colors.css"]');
        if (stylesheet) stylesheet.href = `/admin/reasoning-effort-colors.css?v=${Date.now()}`;
      }
    },
    onError: () => undefined
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
      setWebhookEditing(false);
      setWebhookError("");
      queryClient.setQueryData<NotificationSettings>(notificationSettingsQueryKey, (current) =>
        current && result.notifications ? { ...current, notifications: result.notifications } : current);
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
      setWebhookEditing(false);
      setWebhookError("");
      queryClient.setQueryData<NotificationSettings>(notificationSettingsQueryKey, (current) =>
        current && result.notifications ? { ...current, notifications: result.notifications } : current);
      // Clearing the Webhook also disables notifications on the server. Rebase
      // this one field so a later global save cannot restore the old switch.
      queryClient.setQueryData<ConfigurationCatalog>(configurationQueryKey, (current) => current && ({
        ...current,
        groups: current.groups.map((group) => ({ ...group, fields: group.fields.map((field) =>
          field.key === "notification.enabled" ? { ...field, value: false } : field) }))
      }));
      setDraft((current) => ({ ...current, "notification.enabled": false }));
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
  const notificationTestMutation = useMutation({
    mutationFn: () => testNotification(csrfToken),
    onSuccess: async (result) => {
      showToast(result.message);
      await notification.refetch();
    },
    onError: (error) => showToast(error instanceof Error ? error.message : "测试消息发送失败", "error")
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

  const selectedSections = activeSection ? [activeSection] : [];
  const dirtyCategories = new Set(dirtyFields.map((field) => field.group));
  const dirtyModes = new Map<string, number>();
  dirtyFields.forEach((field) => {
    const label = applyModeLabel(field.apply_mode, field.key);
    dirtyModes.set(label, (dirtyModes.get(label) ?? 0) + 1);
  });
  const riskyEffects = configurationEffects(dirtyFields);
  const searchItems = [
    ...fields.map((field) => ({ key: field.key, label: field.label, group: field.group, section: field.section, description: field.description })),
    ...configurationSections.filter((section) => ["access", "backups", "storage", "audit", "notifications"].includes(section.id))
      .map((section) => ({ key: section.id, label: section.title, group: section.category, section: section.id, description: section.description })),
    { key: "quota-reset", label: "用量维护", group: "用量与额度" as const, section: "quota", description: "异常补偿时清零全员本周已用量" }
  ];
  const searchMatches = search.trim() ? searchItems.filter((item) => [item.group, item.label, item.key, item.description,
    configurationSections.find((section) => section.id === item.section)?.title].join(" ").toLocaleLowerCase("zh-CN").includes(search.trim().toLocaleLowerCase("zh-CN"))) : [];
  const managementKeyError = managementKeyForm.formState.errors.newKey?.message
    ?? managementKeyForm.formState.errors.confirmation?.message
    ?? (managementKeyMutation.isError
      ? managementKeyMutation.error instanceof Error ? managementKeyMutation.error.message : "管理密钥未更新"
      : "");
  const selectConfigurationGroup = (group: ConfigurationCategory, key = "", focus = true) => {
    const field = fields.find((item) => item.key === key);
    const section = availableSections.find((item) => item.id === (field?.section ?? legacyConfigurationSection(key)?.id ?? key))
      ?? availableSections.find((item) => item.category === group);
    const destination = section?.category ?? group;
    setSearch("");
    setCategory(destination);
    setSelectedSectionId(section?.id ?? "");
    setFocusKey(focus ? key : "");
    setExpandedCategories((previous) => ({ ...previous, [destination]: true }));
    const params = new URLSearchParams({ group: destination });
    if (section) params.set("section", section.id);
    if (field) params.set("key", field.key);
    handledDeepLink.current = params.toString();
    setSearchParams(params, { replace: true });
    setMobileNavigationOpen(false);
    if (!focus || !key) {
      workspaceContentRef.current?.scrollTo?.({ top: 0, left: 0 });
      if (mobileNavigationOpen) requestAnimationFrame(() => workspaceContentRef.current?.focus({ preventScroll: true }));
    }
  };
  const updateField = (field: ConfigurationField, value: DraftValue) => setDraft((current) => ({ ...current, [field.key]: value }));
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
        <aside className="settings-navigation" aria-label="配置中心导航" data-mobile-open={mobileNavigationOpen}>
          <div className="settings-navigation-fixed">
            <label className="configuration-search">
              <span aria-hidden="true">⌕</span>
              <input aria-label="搜索配置" type="search" placeholder="搜索全部配置" autoComplete="off" value={search} onChange={(event) => setSearch(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && searchMatches[0]) { event.preventDefault(); selectConfigurationGroup(searchMatches[0].group, searchMatches[0].key); } }} />
            </label>
            <div className="configuration-search-results" hidden={!search.trim()}>
              {searchMatches.length ? searchMatches.map((field) => (
                <button key={field.key} type="button" onClick={() => selectConfigurationGroup(field.group, field.key)}><span>{field.label}</span><small>{field.group} · {field.key}</small></button>
              )) : <p className="configuration-search-empty">没有匹配项</p>}
            </div>
            <button className="settings-mobile-navigation-toggle" type="button" aria-label="选择配置子项" aria-expanded={mobileNavigationOpen} aria-controls="configuration-navigation-tree" onClick={() => setMobileNavigationOpen((open) => !open)}>
              <span>{category}{activeSection ? ` / ${activeSection.title}` : ""}</span>
              <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="m5 7 5 5 5-5" /></svg>
            </button>
            <p className="settings-navigation-label settings-category-label">配置分类</p>
          </div>
          <div className="settings-navigation-scroll">
              <nav id="configuration-navigation-tree" className="configuration-navigation" aria-label="配置分类">
                {configurationCategories.map((item) => {
                  const dirtyCount = dirtyFields.filter((field) => field.group === item.name).length;
                  const sections = availableSections.filter((section) => section.category === item.name);
                  const expanded = expandedCategories[item.name] ?? false;
                  const groupId = `configuration-navigation-${item.eyebrow.toLowerCase().replaceAll(" ", "-").replaceAll("&", "and")}`;
                  return <div className="configuration-navigation-group" key={item.name}>
                    <button className="configuration-category-toggle" type="button" aria-label={item.name} aria-expanded={expanded} aria-controls={groupId} aria-describedby={dirtyCount ? `${groupId}-dirty` : undefined} data-current={category === item.name} onClick={() => setExpandedCategories((previous) => ({ ...previous, [item.name]: !expanded }))}>
                      <svg viewBox="0 0 20 20" aria-hidden="true" focusable="false"><path d="m7 4 6 6-6 6" /></svg>
                      <span>{item.name}</span>
                      {dirtyCount ? <small id={`${groupId}-dirty`} className="dirty">{dirtyCount} 项修改</small> : null}
                    </button>
                    <ul id={groupId} className="configuration-subnavigation" hidden={!expanded}>
                      {sections.map((section) => {
                        const sectionDirtyCount = dirtyFields.filter((field) => field.section === section.id).length;
                        const active = activeSection?.id === section.id;
                        return <li key={section.id}>
                          <button className={active ? "active" : ""} type="button" aria-label={section.title} aria-current={active ? "page" : undefined} aria-controls="configuration-detail" aria-describedby={sectionDirtyCount ? `configuration-dirty-${section.id}` : undefined} onClick={() => selectConfigurationGroup(item.name, section.id, false)}>
                            <span>{section.title}</span>
                            {sectionDirtyCount ? <small id={`configuration-dirty-${section.id}`} className="dirty">{sectionDirtyCount} 项修改</small> : null}
                          </button>
                        </li>;
                      })}
                      {!sections.length ? <li className="configuration-navigation-empty">暂无配置项</li> : null}
                    </ul>
                  </div>;
                })}
              </nav>
          </div>
        </aside>

        <form className="configuration-panel" noValidate aria-busy={saveMutation.isPending} onSubmit={(event) => { event.preventDefault(); requestSave(); }}>
          <div id="configuration-detail" className="settings-workspace-content" ref={workspaceContentRef} role="region" tabIndex={0} aria-label={activeSection?.title ?? "配置内容"}>
            <ConfigurationSavingContext.Provider value={saveMutation.isPending}>
            <fieldset className="configuration-section-list" disabled={saveMutation.isPending}>
              {selectedSections.map((section) => {
                const sectionFields = fields.filter((field) => field.section === section.id);
                const specialized = (field: EditorField) => section.id === "model-multipliers"
                  ? field.key.startsWith(modelMultiplierPrefix)
                  : section.id === "reasoning-multipliers" ? field.key.startsWith(reasoningMultiplierPrefix)
                  : section.id === "appearance" && field.key.startsWith(reasoningColorPrefix);
                const ordinaryFields = sectionFields.filter((field) => !specialized(field));
                return <section className="configuration-section" key={section.id} aria-label={section.title} data-configuration-field={section.id}>
                  <div id={`configuration-section-${section.id}`}>
                    {section.id === "brand" ? <BrandingLogoEditor custom={general.data.branding.custom_logo} sha256={general.data.branding.logo_sha256} pending={logoMutation.isPending || logoResetMutation.isPending} error={logoError} onFile={(file) => { const error = validateLogoFile(file); setLogoError(error); if (!error) logoMutation.mutate(file); }} onReset={() => setLogoResetOpen(true)} /> : null}
                    {ordinaryFields.length ? <div className="configuration-fields">
                      <ConfigurationTableHeader />
                      {ordinaryFields.map((field) => <ConfigurationEditor key={field.key} field={field} value={draft[field.key]} error={errors[field.key]} dirty={dirtyFields.some((item) => item.key === field.key)} onChange={(value) => updateField(field, value)} />)}
                    </div> : null}
                    {section.id === "model-multipliers" ? <ModelMultiplierEditor fields={sectionFields.filter(specialized)} draft={draft} onChange={updateField} /> : null}
                    {section.id === "reasoning-multipliers" || section.id === "appearance" ? <ReasoningStrategyEditor fields={sectionFields.filter(specialized)} draft={draft} onChange={updateField} /> : null}
                    {section.id === "notifications" ? <><p className="configuration-independent-note">Webhook 单独保存，立即生效。</p><NotificationIntegration status={notification.data.notifications} value={webhookDraft} error={webhookError} saving={webhookMutation.isPending} clearing={webhookClearMutation.isPending} sending={notificationSendMutation.isPending} testing={notificationTestMutation.isPending} editing={webhookEditing} onEdit={() => { setWebhookError(""); setWebhookEditing(true); }} onCancel={() => { setWebhookDraft(""); setWebhookError(""); setWebhookEditing(false); }} onTest={() => notificationTestMutation.mutate()} onChange={(value) => { setWebhookDraft(value); setWebhookError(""); }} onSave={() => webhookMutation.mutate()} onClear={() => setWebhookClearOpen(true)} onSend={() => notificationSendMutation.mutate()} /></> : null}
                    {section.id === "access" ? <AccessPanel managementKeyConfigured={general.data.security.management_key_configured} initialPasswordConfigured={general.data.security.initial_password_configured} onInitialPassword={() => setInitialPasswordOpen(true)} onManagementKey={() => setManagementKeyOpen(true)} /> : null}
                    {section.id === "backups" ? <BackupsPanel count={workspace.data.backups.count} latest={workspace.data.backups.latest} /> : null}
                    {section.id === "storage" ? <StoragePanel rows={workspace.data.storage} onRefresh={() => refreshWorkspace(true)} /> : null}
                    {section.id === "audit" ? <AuditPanel rows={workspace.data.recent_audit} onRefresh={() => refreshWorkspace(true)} /> : null}
                    {section.id === "quota" ? <QuotaSystemDanger summary={quotaOperations.data} pending={quotaOperations.isPending || quotaOperations.isFetching} failed={quotaOperations.isError} onReset={() => setQuotaResetOpen(true)} /> : null}
                  </div>
                </section>;
              })}
            </fieldset>
            </ConfigurationSavingContext.Provider>
            {!fields.length && category === "品牌与身份" ? <div className="configuration-empty-state" role="status"><h3>当前没有可配置项</h3><p>可在系统设置中管理访问凭据。</p><button className="button button-primary" type="button" onClick={() => selectConfigurationGroup("系统设置", "access")}>进入访问凭据</button></div> : null}
          </div>
          <div className="configuration-save-region">
            {saveMutation.isError && !confirmOpen ? <p className="form-error" role="alert">{saveMutation.error instanceof Error ? saveMutation.error.message : "配置未保存"}</p> : null}
            {saveError ? <p className="form-error" role="alert">{saveError}</p> : null}
            <div className="configuration-actions"><div className="configuration-change-summary"><span className={`status-chip ${dirtyFields.length ? "warning" : "neutral"}`} role="status">{dirtyFields.length ? `${dirtyFields.length} 项未保存` : "未修改"}</span>{dirtyCategories.size > 1 ? <small>涉及 {dirtyCategories.size} 个分类</small> : null}<div className="configuration-impact-summary">{dirtyModes.size ? [...dirtyModes.entries()].map(([label, count]) => <span key={label}><strong>{count}</strong>{label}</span>) : <span>修改后统一保存</span>}</div></div><div className="configuration-action-buttons"><button className="button button-quiet" type="button" disabled={!dirtyFields.length || saveMutation.isPending} onClick={() => { setDraft(configurationDraft(catalog.data)); setSaveError(""); saveMutation.reset(); }}>撤销未保存修改</button><button className="button button-primary" type="submit" disabled={!dirtyFields.length || saveMutation.isPending}>{saveMutation.isPending ? "正在保存…" : "保存配置"}</button></div></div>
          </div>
        </form>
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
          <Button key="cancel" className="legacy-modal-ghost" tabIndex={-1} disabled={managementKeyMutation.isPending} onClick={() => { setManagementKeyOpen(false); managementKeyForm.reset(); managementKeyMutation.reset(); }}>取消</Button>,
          <Button key="submit" type="primary" htmlType="submit" form="settings-management-key-form" disabled={managementKeyMutation.isPending}>{managementKeyMutation.isPending ? "正在更新…" : "更新并重新进入"}</Button>
        ]}
      >
        <p className="warning-banner">所有管理会话将立即退出；API Key、用户会话和请求不受影响。</p>
        <form id="settings-management-key-form" noValidate onSubmit={managementKeyForm.handleSubmit(() => managementKeyMutation.mutate())}>
          <div className="field"><label htmlFor="settings-management-key">新管理密钥</label><Controller control={managementKeyForm.control} name="newKey" render={({ field }) => <LegacyPasswordInput id="settings-management-key" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={12} maxLength={128} onValueChange={field.onChange} />} /></div>
          <div className="field account-email-field"><label htmlFor="settings-management-key-confirmation">再次输入</label><Controller control={managementKeyForm.control} name="confirmation" render={({ field }) => <LegacyPasswordInput id="settings-management-key-confirmation" ariaLabel="再次输入管理密钥" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={12} maxLength={128} onValueChange={field.onChange} />} /></div>
          <p className="form-error" role="alert">{managementKeyError}</p>
        </form>
      </Modal>
      <LegacyConfirmModal title={`保存 ${dirtyFields.length} 项配置？`} open={confirmOpen} okText="保存并应用" confirmLoading={saveMutation.isPending} onCancel={() => setConfirmOpen(false)} onOk={() => saveMutation.mutate()}><p>{riskyEffects.join("；")}。应用失败时将尝试恢复原配置。</p>{saveMutation.isError ? <Alert type="error" showIcon title={saveMutation.error instanceof Error ? saveMutation.error.message : "配置未保存"} /> : null}</LegacyConfirmModal>
      <LegacyConfirmModal title="恢复默认 Logo？" open={logoResetOpen} okText="恢复默认" confirmLoading={logoResetMutation.isPending} onCancel={() => !logoResetMutation.isPending && setLogoResetOpen(false)} onOk={() => { setLogoResetOpen(false); logoResetMutation.mutate(); }}><p>删除自定义 Logo，立即恢复默认。</p></LegacyConfirmModal>
      <LegacyConfirmModal title="清除企业微信 Webhook？" open={webhookClearOpen} okText="确认清除" danger confirmLoading={webhookClearMutation.isPending} onCancel={() => !webhookClearMutation.isPending && setWebhookClearOpen(false)} onOk={() => { setWebhookClearOpen(false); webhookClearMutation.mutate(); }}><p>删除 Webhook 并关闭企业微信通知。</p></LegacyConfirmModal>
      <Modal className="legacy-settings-modal" open={quotaResetOpen} title="清零全部用户本周已用量" okText="确认清零" cancelText="取消" okButtonProps={{ danger: true }} confirmLoading={quotaResetMutation.isPending} onCancel={() => { if (quotaResetMutation.isPending) return; setQuotaResetOpen(false); quotaResetForm.reset(); quotaResetMutation.reset(); }} onOk={() => void quotaResetForm.handleSubmit(() => quotaResetMutation.mutate())()} destroyOnHidden>
        <p className="warning-banner">全员本周剩余额度将立即改变；原始事件保留，不可撤销。</p>
        {quotaResetMutation.isError ? <Alert type="error" showIcon message={quotaResetMutation.error instanceof Error ? quotaResetMutation.error.message : "本周用量未清零"} /> : null}
        <Form layout="vertical" requiredMark={false}><Controller control={quotaResetForm.control} name="reason" render={({ field, fieldState }) => <Form.Item label="操作原因" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}><Input.TextArea {...field} aria-label="操作原因" autoSize={{ minRows: 3, maxRows: 5 }} /></Form.Item>} /><Controller control={quotaResetForm.control} name="confirmation" render={({ field, fieldState }) => <Form.Item label="输入 RESET ALL USERS 确认" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}><Input {...field} aria-label="清零确认文字" autoComplete="off" /></Form.Item>} /></Form>
      </Modal>
    </section>
  );
}

function ConfigurationSkeleton() {
  return <section className="page-content legacy-settings-page" aria-label="正在加载配置中心" aria-busy="true">
    <div className="settings-workspace settings-workspace-skeleton" aria-hidden="true">
      <aside className="settings-navigation"><div className="skeleton skeleton-line" /><div className="skeleton skeleton-table" /></aside>
      <div className="configuration-panel">
        <div className="settings-workspace-content"><div className="configuration-fields">
          <ConfigurationTableHeader />
          {Array.from({ length: 5 }, (_, index) => <div className="configuration-field" key={index}>
            <div className="configuration-field-copy"><div className="skeleton skeleton-line" /><div className="skeleton skeleton-line" /></div>
            <div className="configuration-field-meta"><div className="skeleton skeleton-line" /></div>
            <div className="configuration-field-value"><div className="skeleton skeleton-line" /></div>
          </div>)}
        </div></div>
        <div className="configuration-save-region"><div className="configuration-actions"><div className="skeleton skeleton-line" /><div className="skeleton skeleton-line" /></div></div>
      </div>
    </div>
  </section>;
}

function BrandingLogoEditor({ custom, sha256, pending, error, onFile, onReset }: { custom: boolean; sha256?: string; pending: boolean; error: string; onFile: (file: File) => void; onReset: () => void }) {
  const fileInputRef = useRef<HTMLInputElement>(null);
  const { theme } = useTheme();
  const source = custom ? `/branding/logo${sha256 ? `?v=${encodeURIComponent(sha256.slice(0, 16))}` : ""}` : `/portal/assets/codex-cpa-pool-logo${theme === "dark" ? "-dark" : ""}.svg`;
  return <article className="branding-logo-editor"><div className="branding-logo-preview"><img src={source} alt="当前 Logo" /></div><div className="branding-logo-copy"><strong>品牌 Logo</strong><small>上传或恢复后立即生效</small><span className={`status-chip ${custom ? "success" : "neutral"}`}>{custom ? "自定义 Logo" : "默认 Logo"}</span></div><div className="branding-logo-actions"><button className="button button-secondary" type="button" disabled={pending} onClick={() => fileInputRef.current?.click()}>{pending ? "正在上传…" : "选择并上传"}</button><input ref={fileInputRef} type="file" accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml" disabled={pending} hidden onChange={(event) => { const file = event.target.files?.[0]; event.target.value = ""; if (file) onFile(file); }} /><button className="button danger-outline" type="button" disabled={!custom || pending} onClick={onReset}>恢复默认</button><small className="form-error" role="alert">{error}</small></div></article>;
}

function NotificationIntegration({ status, value, error, saving, clearing, sending, testing, editing, onEdit, onCancel, onChange, onSave, onClear, onSend, onTest }: {
  status: NotificationStatus;
  value: string;
  error: string;
  saving: boolean;
  clearing: boolean;
  sending: boolean;
  testing: boolean;
  editing: boolean;
  onEdit: () => void;
  onCancel: () => void;
  onChange: (value: string) => void;
  onSave: () => void;
  onClear: () => void;
  onSend: () => void;
  onTest: () => void;
}) {
  const showEditor = editing || !status.webhook_configured;
  const busy = saving || clearing || sending || testing;
  const canSave = !busy && Boolean(value.trim());
  const canSend = status.webhook_configured && !showEditor && !busy;
  return <article className="notification-integration" aria-busy={busy}>
    <div className="notification-integration-head">
      <div className="notification-integration-copy"><strong>企业微信群 Webhook</strong></div>
      <span className={`status-chip ${status.webhook_configured ? "success" : "neutral"}`}>{status.webhook_configured ? "Webhook 已配置" : "Webhook 未配置"}</span>
    </div>
    <div className="notification-webhook-editor">
      <span className="notification-webhook-label" id="notification-webhook-label">Webhook 地址</span>
      <div className="notification-webhook-control">
        {showEditor ? <input id="notification-webhook-url-react" aria-labelledby="notification-webhook-label" aria-invalid={Boolean(error)} aria-describedby={error ? "notification-webhook-error" : undefined}
          type="url" maxLength={2048} autoComplete="off" spellCheck={false} autoFocus={editing} disabled={busy}
          value={value} placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
          onChange={(event) => onChange(event.target.value)} onKeyDown={(event) => {
            if (event.nativeEvent.isComposing) return;
            if (event.key === "Enter") {
              event.preventDefault();
              event.stopPropagation();
              if (canSave) onSave();
            } else if (event.key === "Escape" && !busy) {
              event.preventDefault();
              event.stopPropagation();
              onCancel();
            }
          }} /> : <code className="notification-webhook-saved" aria-labelledby="notification-webhook-label">
            {status.webhook_display_url || "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=••••••"}
          </code>}
        <div className="notification-webhook-actions">
          {showEditor ? <>
            <button className="button button-primary" type="button" disabled={!canSave} onClick={onSave}>{saving ? "正在保存…" : "保存 Webhook"}</button>
            {status.webhook_configured ? <button className="button button-quiet" type="button" disabled={busy} onClick={onCancel}>取消</button> : null}
          </> : <button className="button button-secondary" type="button" disabled={busy} onClick={onEdit}>更换地址</button>}
          <button className="button danger-outline" type="button" disabled={!canSend} onClick={onClear}>{clearing ? "正在清除…" : "清除 Webhook"}</button>
          <button className="button button-quiet" type="button" disabled={!canSend} onClick={onTest}>{testing ? "正在发送测试…" : "发送测试消息"}</button>
          <button className="button button-quiet" type="button" disabled={!canSend} onClick={onSend}>{sending ? "正在发送…" : "发送账号报告"}</button>
        </div>
      </div>
      {error ? <p className="form-error" id="notification-webhook-error" role="alert">{error}</p> : null}
    </div>
    <div className="notification-status-list">
      <span>最近成功<strong>{formatSiteTimestamp(status.last_success_at)}</strong></span>
      <span>下次发送<strong>{formatSiteTimestamp(status.next_schedule_at)}</strong></span>
      {status.last_error ? <span>最近错误<strong>{status.last_error}</strong></span> : null}
    </div>
  </article>;
}

function ConfigurationTableHeader({ label = "配置项", valueLabel = "配置值" }: { label?: ReactNode; valueLabel?: string }) {
  return <div className="configuration-table-head">
    <div>{label}</div>
    <span className="configuration-status-heading">生效方式</span>
    <span className="configuration-value-heading">{valueLabel}</span>
  </div>;
}

function ConfigurationApplyMode({ field }: { field: ConfigurationField }) {
  return <div className="configuration-field-meta"><span className="configuration-apply" title="保存后的生效方式">{applyModeLabel(field.apply_mode, field.key)}</span></div>;
}

function ConfigurationDirtyMark({ dirty }: { dirty: boolean }) {
  return dirty ? <small className="configuration-dirty-mark">未保存</small> : null;
}

function ConfigurationEditor({ field, value, error, dirty, onChange }: { field: EditorField; value: DraftValue; error?: string; dirty: boolean; onChange: (value: DraftValue) => void }) {
  return <article className={`configuration-field${dirty ? " configuration-field-dirty" : ""}`} data-configuration-field={field.key}>
    <div className="configuration-field-copy">
      <div className="configuration-field-name"><label htmlFor={`configuration-${field.key}`}>{field.label}</label><ConfigurationDirtyMark dirty={dirty} /></div>
      {field.description ? <p>{field.description}</p> : null}
    </div>
    <ConfigurationApplyMode field={field} />
    <ConfigurationValueEditor field={field} value={value} error={error} onChange={onChange} />
  </article>;
}

function ConfigurationValueEditor({ field, value, error, onChange }: { field: ConfigurationField; value: DraftValue; error?: string; onChange: (value: DraftValue) => void }) {
  const valueBeforeFocus = useRef(value);
  const [editRevision, setEditRevision] = useState(0);
  const saving = useContext(ConfigurationSavingContext);
  return <div className="configuration-field-value"
    onFocusCapture={(event) => {
      if (!(event.relatedTarget instanceof Node) || !event.currentTarget.contains(event.relatedTarget)) valueBeforeFocus.current = value;
    }}
    onKeyDown={(event) => {
      const input = event.target;
      // Composite selects own Enter/Escape. Text and numeric edits never submit the whole form.
      if (saving || event.defaultPrevented || event.nativeEvent.isComposing || !(input instanceof HTMLInputElement)
        || input.getAttribute("role") === "combobox" || ["checkbox", "radio", "color", "file"].includes(input.type)) return;
      if (event.key === "Enter") {
        event.preventDefault();
        input.blur();
      } else if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        onChange(valueBeforeFocus.current);
        // Also discard a duration's intermediate amount when its stored seconds have not changed.
        setEditRevision((revision) => revision + 1);
        input.blur();
      }
    }}>
    <div className="configuration-control-slot" style={{ width: configurationControlWidth(field) }}>
      <ConfigurationControl key={editRevision} field={field} value={value} onChange={onChange} />
    </div>
    {error ? <small className="configuration-control-error" id={`configuration-${field.key}-error`}>{error}</small> : null}
  </div>;
}

function ConfigurationControl({ field, value, onChange }: { field: ConfigurationField; value: DraftValue; onChange: (value: DraftValue) => void }) {
  const disabled = useContext(ConfigurationSavingContext);
  const id = `configuration-${field.key}`;
  const error = validateDraftValue(field, value);
  const errorId = error ? `${id}-error` : undefined;
  if (field.type === "timezone") return <TimezoneSelect id={id} value={String(value ?? "")} disabled={disabled} ariaInvalid={Boolean(error)} ariaDescribedBy={errorId} onChange={onChange} />;
  if (field.type === "duration" || field.key === "portal.session_ttl_seconds") return <ConfigurationDurationControl field={field} value={value} onChange={onChange} />;
  if (field.type === "boolean") return <div className="configuration-field-control boolean-control"><label><input id={id} type="checkbox" checked={Boolean(value)} disabled={disabled} onChange={(event) => onChange(event.target.checked)} /><span>{value ? "已启用" : "已关闭"}</span></label></div>;
  if (field.type === "choice") return <div className="configuration-choice-control">
    <LegacyEnhancedSelect id={id} label={field.label} value={String(value ?? "")} disabled={disabled} ariaInvalid={Boolean(error)} ariaDescribedBy={errorId} options={(field.choices ?? []).map((choice) => ({ value: choice.value, label: `${choice.label} · ${choice.value}` }))} onChange={onChange} />
    {field.choices?.some((choice) => /^https?:\/\//.test(choice.value)) ? <div className="configuration-choice-address"><span>{sameConfigurationValue(normalizeDraftValue(field, value), field.value) ? "当前地址" : "待切换地址"}</span><code>{String(value ?? "")}</code></div> : null}
  </div>;
  if (field.type === "color") {
    const color = /^#[0-9a-f]{6}$/i.test(String(value ?? "")) ? String(value) : "#687287";
    return <div className="reasoning-color-inputs">
      <label className="reasoning-color-swatch"><input type="color" value={color} disabled={disabled} aria-label={`选择${field.label}颜色`} onChange={(event) => onChange(event.target.value)} /></label>
      <input id={id} aria-label={field.label} aria-invalid={Boolean(error)} aria-describedby={errorId} className="reasoning-color-hex" type="text" value={String(value ?? "")} disabled={disabled} maxLength={7} pattern="#[0-9A-Fa-f]{6}" onChange={(event) => onChange(event.target.value)} />
    </div>;
  }
  if (field.type === "proxy_url_secret") return <LegacyPasswordInput id={id} value={value == null ? "" : String(value)} disabled={disabled} ariaLabel={field.label} ariaInvalid={Boolean(error)} ariaDescribedBy={errorId} placeholder={field.configured ? "已配置；留空保持不变" : "例如 socks5://user:pass@host:1080"} onValueChange={onChange} />;
  const numeric = ["integer", "number", "nullable_integer"].includes(field.type);
  const tokenInput = numeric && field.unit === "Token";
  const elevatedMultiplier = !error && field.unit === "倍"
    && (field.key.startsWith(modelMultiplierPrefix) || field.key.startsWith(reasoningMultiplierPrefix))
    && Number.isFinite(Number(value)) && Number(value) > 1;
  const inputProps: InputHTMLAttributes<HTMLInputElement> & { value: string } = {
    id, type: numeric ? "number" : "text", value: value == null ? "" : String(value), disabled,
    min: field.min, max: field.max, maxLength: field.max_length,
    step: field.type === "number" ? "any" : numeric ? 1 : undefined,
    placeholder: field.type === "nullable_integer" ? "不限额" : undefined,
    autoComplete: "off", title: value == null || value === "" ? "点击修改" : String(value),
    "aria-label": field.label, "aria-invalid": Boolean(error),
    "aria-describedby": [errorId, field.unit && !tokenInput ? `${id}-unit-label` : undefined, elevatedMultiplier ? `${id}-multiplier-mark` : undefined].filter(Boolean).join(" ") || undefined,
    onChange: (event) => onChange(event.target.value)
  };
  const input = (numeric && !tokenInput) || field.type === "time_list" ? <ConfigurationInlineInput {...inputProps} /> : <input {...inputProps} />;
  if (tokenInput) {
    const presentation = tokenInputPresentation(typeof value === "boolean" ? null : value, field.type === "nullable_integer" ? "留空表示不限额" : "请输入 Token 数量");
    return <div className="configuration-token-control token-input-control">{input}<div className="token-input-preview" data-state={presentation.state}>
      {presentation.state === "ready" ? <><strong>{presentation.compact}</strong>{presentation.localized ? <span>{presentation.localized}</span> : null}<small>精确值 {presentation.exact}</small></> : <small>{presentation.state === "empty" ? presentation.emptyLabel : "请输入有效的正整数 Token 数量"}</small>}
    </div></div>;
  }
  const control = field.unit ? <div className={`configuration-unit-control${elevatedMultiplier ? " configuration-multiplier-elevated" : ""}`}>
    {input}<span className="configuration-unit-label" id={`${id}-unit-label`}>{field.unit}</span>
    {elevatedMultiplier ? <span className="configuration-multiplier-mark" id={`${id}-multiplier-mark`} title="倍率大于 1">高倍率</span> : null}
  </div> : input;
  return field.type === "nullable_integer" ? <div className="configuration-nullable-control">{control}<small>留空表示不限额</small></div> : control;
}

function ConfigurationInlineInput({ value, placeholder, ...props }: Omit<InputHTMLAttributes<HTMLInputElement>, "value"> & { value: string }) {
  return <span className="configuration-inline-editor">
    <span className="configuration-inline-size" aria-hidden="true">{value || placeholder || "0"}</span>
    <input {...props} value={value} placeholder={placeholder} autoComplete="off" title={props.title ?? "点击修改"} />
  </span>;
}

const durationUnits = [
  { value: "seconds", label: "秒", suffix: "s", seconds: 1 },
  { value: "minutes", label: "分钟", suffix: "m", seconds: 60 },
  { value: "hours", label: "小时", suffix: "h", seconds: 3600 },
  { value: "days", label: "天", suffix: "d", seconds: 86400 }
] as const;
type DurationUnit = (typeof durationUnits)[number]["value"];
const durationBounds = { min: 30, max: 30 * 86400 };

function configurationDurationSeconds(field: ConfigurationField, value: unknown): number | null {
  if (field.type === "duration") {
    const match = /^(\d+)([smhd])$/i.exec(String(value ?? "").trim());
    if (!match) return null;
    const unit = durationUnits.find((item) => item.suffix === match[2].toLowerCase())!;
    const seconds = Number(match[1]) * unit.seconds;
    return Number.isSafeInteger(seconds) ? seconds : null;
  }
  if ((typeof value !== "number" && typeof value !== "string") || String(value).trim() === "") return null;
  const seconds = Number(value);
  return Number.isFinite(seconds) ? seconds : null;
}

function configurationDurationValue(field: ConfigurationField, seconds: number, current: DraftValue): DraftValue {
  if (field.type !== "duration") return seconds;
  // Preserve the existing spelling when the duration has not changed, including saved values such as 60m.
  for (const original of [current, field.value]) {
    if (typeof original === "string" && configurationDurationSeconds(field, original) === seconds) return original;
  }
  const unit = [...durationUnits].reverse().find((item) => seconds > 0 && seconds % item.seconds === 0)
    ?? durationUnits[0];
  return `${seconds / unit.seconds}${unit.suffix}`;
}

function durationAmount(value: DraftValue, secondsPerUnit: number): string {
  if (value == null || value === "" || !Number.isFinite(Number(value))) return "";
  // Eight decimal places keep the display readable without losing whole-second precision.
  return String(Number((Number(value) / secondsPerUnit).toFixed(8)));
}

function durationLimit(seconds: number): string {
  if (seconds % 86400 === 0) return `${seconds / 86400} 天`;
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`;
  if (seconds % 60 === 0) return `${seconds / 60} 分钟`;
  return `${seconds} 秒`;
}

function ConfigurationDurationControl({ field, value, onChange }: { field: ConfigurationField; value: DraftValue; onChange: (value: DraftValue) => void }) {
  const disabled = useContext(ConfigurationSavingContext);
  const id = `configuration-${field.key}`;
  const [unit, setUnit] = useState<DurationUnit>(() => field.type === "duration"
    ? durationUnits.find((item) => item.suffix === String(value ?? field.value ?? "").trim().slice(-1).toLowerCase())?.value ?? "hours"
    : "hours");
  const [inputDraft, setInputDraft] = useState<{ value: DraftValue; amount: string } | null>(null);
  const selectedUnit = durationUnits.find((item) => item.value === unit)!;
  const seconds = configurationDurationSeconds(field, value);
  const amount = inputDraft && inputDraft.value === value ? inputDraft.amount : durationAmount(seconds, selectedUnit.seconds);
  const minimum = field.min ?? (field.type === "duration" ? durationBounds.min : undefined);
  const maximum = field.max ?? (field.type === "duration" ? durationBounds.max : undefined);
  const error = validateDraftValue(field, value);

  return <div className="configuration-duration-control">
      <ConfigurationInlineInput
        id={id}
        type="number"
        value={amount}
        min={minimum === undefined ? undefined : minimum / selectedUnit.seconds}
        max={maximum === undefined ? undefined : maximum / selectedUnit.seconds}
        step="any"
        aria-label={field.label}
        aria-invalid={Boolean(error)}
        aria-describedby={error ? `${id}-error` : undefined}
        disabled={disabled}
        autoComplete="off"
        onChange={(event) => {
          const nextAmount = event.target.value;
          const seconds = Number(nextAmount) * selectedUnit.seconds;
          const nextValue = nextAmount === "" || !Number.isFinite(seconds) ? "" : configurationDurationValue(field, Math.round(seconds), value);
          setInputDraft({ value: nextValue, amount: nextAmount });
          onChange(nextValue);
        }}
      />
      <LegacyEnhancedSelect<DurationUnit>
        id={`${id}-unit`}
        label={`${field.label}单位`}
        value={unit}
        disabled={disabled}
        options={durationUnits.map(({ value: unitValue, label }) => ({ value: unitValue, label }))}
        onChange={(nextUnit) => {
          // Unit selection only changes the display; keep the API's original value and format.
          setUnit(nextUnit);
          setInputDraft(null);
        }}
      />
  </div>;
}

function LegacyConfirmModal({ title, open, children, okText, danger = false, confirmLoading = false, onCancel, onOk }: { title: string; open: boolean; children: ReactNode; okText: string; danger?: boolean; confirmLoading?: boolean; onCancel: () => void; onOk: () => void }) {
  return <Modal className="legacy-confirm-modal" title={<span className="sr-only">{title}</span>} open={open} width={430} centered closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>} transitionName="" maskTransitionName="" onCancel={onCancel} destroyOnHidden footer={[<Button key="cancel" disabled={confirmLoading} onClick={onCancel}>取消</Button>, <Button key="confirm" type={danger ? "default" : "primary"} danger={danger} loading={confirmLoading} onClick={onOk}>{okText}</Button>]}><div className="legacy-confirm-body"><div className="legacy-confirm-icon" aria-hidden="true">!</div><h3>{title}</h3><div className="legacy-confirm-message">{children}</div></div></Modal>;
}

function ModelMultiplierEditor({ fields, draft, onChange }: { fields: ConfigurationField[]; draft: Draft; onChange: (field: ConfigurationField, value: DraftValue) => void }) {
  const modelFields = fields.filter((field) => field.key.startsWith(modelMultiplierPrefix));
  return <section className="model-multiplier-editor" aria-label="模型倍率">
    <div className="configuration-matrix-viewport" role="region" aria-label="模型倍率配置表">
      <div className="configuration-matrix model-multiplier-table">
        <ConfigurationTableHeader label="模型" valueLabel="用户额度倍率" />
        {modelFields.map((field) => {
          const model = field.key.slice(modelMultiplierPrefix.length);
          const name = model === "unknown" ? "其他 / 未匹配" : model;
          const value = draft[field.key] === undefined ? draftValueFromConfiguration(field.value, field.type) : draft[field.key];
          const dirty = !sameConfigurationValue(normalizeDraftValue(field, value), field.value);
          return <div className={`configuration-field model-multiplier-row${dirty ? " configuration-field-dirty" : ""}`} key={field.key} data-configuration-field={field.key}>
            <div className="configuration-field-copy">
              <div className="model-multiplier-name"><label htmlFor={`configuration-${field.key}`} title={name}>{name}</label>{model === "unknown" ? <code>fallback</code> : null}</div>
              <ConfigurationDirtyMark dirty={dirty} />
            </div>
            <ConfigurationApplyMode field={field} />
            <ConfigurationValueEditor field={{ ...field, label: `${model === "unknown" ? "其他未匹配模型" : model}用户额度倍率`, unit: "倍" }} value={value} error={validateDraftValue(field, value)} onChange={(next) => onChange(field, next)} />
          </div>;
        })}
      </div>
    </div>
    <footer><button className="button button-quiet" type="button" onClick={() => modelFields.forEach((field) => onChange(field, draftValueFromConfiguration(field.default, field.type)))}>恢复默认模型倍率</button></footer>
  </section>;
}

function ReasoningStrategyEditor({ fields, draft, onChange }: { fields: ConfigurationField[]; draft: Draft; onChange: (field: ConfigurationField, value: DraftValue) => void }) {
  const fieldFor = (prefix: string, effort: string) => fields.find((field) => field.key === `${prefix}${effort}`);
  const hasColors = fields.some((field) => field.key.startsWith(reasoningColorPrefix));
  const hasMultipliers = fields.some((field) => field.key.startsWith(reasoningMultiplierPrefix));
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
  return <section className="reasoning-strategy-editor" aria-label={hasColors ? "推理强度颜色" : "推理倍率"}>
    {hasColors ? <div className="reasoning-color-preview"><strong>配色预览</strong><canvas ref={canvasRef} height="58" role="img" aria-label="推理强度颜色预览" /></div> : null}
    <div className="configuration-matrix-viewport" role="region" aria-label={hasColors ? "推理强度配色配置表" : "推理强度倍率配置表"}>
      <div className="configuration-matrix reasoning-strategy-table">
        <ConfigurationTableHeader label={<div className="reasoning-strategy-names"><span>推理强度（中文）</span><span>推理强度（英文）</span></div>} valueLabel={hasColors ? "账号明细颜色" : "用户额度倍率"} />
        {fields.map((field) => {
          const prefix = hasColors ? reasoningColorPrefix : reasoningMultiplierPrefix;
          if (!field.key.startsWith(prefix)) return null;
          const effort = field.key.slice(prefix.length);
          const value = draft[field.key] === undefined ? draftValueFromConfiguration(field.value, field.type) : draft[field.key];
          const dirty = !sameConfigurationValue(normalizeDraftValue(field, value), field.value);
          const editorField = hasColors ? field : { ...field, label: `${reasoningEffortLabel(effort)}用户额度倍率`, unit: "倍" };
          return <div className={`configuration-field reasoning-strategy-row${dirty ? " configuration-field-dirty" : ""}`} key={field.key} data-configuration-field={field.key}>
            <div className="configuration-field-copy">
              <div className="reasoning-strategy-names">
                <label className="reasoning-strategy-label" htmlFor={`configuration-${field.key}`}>{reasoningEffortLabel(effort)}</label>
                <code className="reasoning-strategy-code">{effort}</code>
              </div>
              <ConfigurationDirtyMark dirty={dirty} />
            </div>
            <ConfigurationApplyMode field={field} />
            <ConfigurationValueEditor field={editorField} value={value} error={validateDraftValue(field, value)} onChange={(next) => onChange(field, next)} />
          </div>;
        })}
      </div>
    </div>
    <div className="reasoning-strategy-defaults">
      {hasMultipliers ? <button className="button button-quiet" type="button" onClick={() => fields.filter((field) => field.key.startsWith(reasoningMultiplierPrefix)).forEach((field) => onChange(field, draftValueFromConfiguration(field.default, field.type)))}>恢复默认推理倍率</button> : null}
      {hasColors ? <button className="button button-quiet" type="button" onClick={() => fields.filter((field) => field.key.startsWith(reasoningColorPrefix)).forEach((field) => onChange(field, draftValueFromConfiguration(field.default, field.type)))}>恢复默认配色</button> : null}
    </div>
  </section>;
}

function QuotaSystemDanger({ summary, pending, failed, onReset }: { summary?: { total_users: number; users_with_usage: number; total_used_tokens: number; total_raw_used_tokens: number; week_end_at: number | null }; pending: boolean; failed: boolean; onReset: () => void }) {
  const available = Boolean(summary) && !failed;
  const canReset = available && Number(summary?.users_with_usage ?? 0) > 0;
  return (
    <section className="quota-system-danger" aria-label="全员额度危险操作" aria-busy={pending} data-configuration-field="quota-reset">
      <div className="quota-system-danger-copy">
        <strong>全员本周用量清零</strong>
        <p>仅用于异常补偿。保留原始事件、额度策略和追加额度；操作需填写原因并确认。</p>
      </div>
      {available && summary ? (
        <dl className="quota-system-danger-metrics" aria-label="本周用量清零影响范围">
          <div><dt>用户总数</dt><dd><strong>{summary.total_users.toLocaleString("zh-CN")}</strong><span className="quota-system-danger-unit">位</span></dd></div>
          <div><dt>有用量用户</dt><dd><strong>{summary.users_with_usage.toLocaleString("zh-CN")}</strong><span className="quota-system-danger-unit">位</span></dd></div>
          <QuotaResetTokenMetric label="本周加权已用" value={summary.total_used_tokens} primary />
          <QuotaResetTokenMetric label="本周未加权" value={summary.total_raw_used_tokens} />
        </dl>
      ) : (
        <p className="quota-system-danger-status" role="status">{pending ? "正在确认影响范围…" : "影响范围暂不可确认，请刷新配置后重试"}</p>
      )}
      <div className="quota-system-danger-footer">
        <div className="quota-system-danger-reset-time">
          <span>下次自动换周</span>
          {available && summary?.week_end_at
            ? <time dateTime={new Date(summary.week_end_at * 1_000).toISOString()}>{formatSiteTimestamp(summary.week_end_at)}</time>
            : <span>—</span>}
        </div>
        <button className="button danger-outline" type="button" disabled={!canReset || pending} onClick={onReset}>
          {pending ? "正在确认影响范围" : !available ? "影响范围暂不可确认" : canReset ? "清零全部用户本周已用量" : "当前无需清零"}
        </button>
      </div>
    </section>
  );
}

function QuotaResetTokenMetric({ label, value, primary = false }: { label: string; value: number; primary?: boolean }) {
  const details = tokenReadableParts(value, { allowZero: true });
  return <div data-primary={primary}>
    <dt>{label}</dt>
    <dd title={details.state === "ready" ? details.exact : undefined}>
      <strong>{formatTokenAmount(value)}</strong><span className="quota-system-danger-unit">Token</span>
      {details.state === "ready" && details.localized ? <small>{details.localized}</small> : null}
    </dd>
  </div>;
}

function AccessPanel({ managementKeyConfigured, initialPasswordConfigured, onInitialPassword, onManagementKey }: { managementKeyConfigured: boolean; initialPasswordConfigured: boolean; onInitialPassword: () => void; onManagementKey: () => void }) {
  return <div className="configuration-access">
    <div><span><strong>管理密钥</strong><small>{managementKeyConfigured ? "已配置 · 更换后管理会话将退出" : "未配置"}</small></span><button className="button button-secondary" type="button" disabled={!managementKeyConfigured} onClick={onManagementKey}>更换管理密钥</button></div>
    <div><span><strong>用户初始密码</strong><small>{initialPasswordConfigured ? "已配置 · 用于后续新建用户" : "未配置"}</small></span><button className="button button-secondary" type="button" onClick={onInitialPassword}>设置用户初始密码</button></div>
    <span className="sr-only">{managementKeyConfigured ? "管理密钥已配置" : "管理密钥未配置"}</span>
  </div>;
}
function BackupsPanel({ count, latest }: { count: number; latest: string }) { return <section className="settings-secondary-panel"><div className="settings-panel-meta"><strong>{count} 个归档</strong></div><div className="settings-panel-body"><div className="settings-panel-callout"><strong>最近归档</strong><span className="settings-path">{latest || "暂无归档"}</span></div></div></section>; }
function StoragePanel({ rows, onRefresh }: { rows: Array<{ label: string; path: string; exists: boolean; mode: string }>; onRefresh: () => Promise<void> }) {
  const createdCount = rows.filter((item) => item.exists).length;
  return (
    <section className="settings-secondary-panel settings-data-panel" aria-labelledby="settings-storage-title">
      <SettingsDataPanelHeader
        id="settings-storage-title"
        eyebrow="TARGET STORAGE"
        title="持久化数据"
        description="查看数据路径、状态和权限。"
        summary={`${createdCount}/${rows.length}`}
        summaryLabel="已创建"
      />
      {rows.length ? (
        <div className="settings-table-panel">
          <div className="settings-table-viewport" role="region" aria-label="存储状态表格">
            <table className="storage-table">
              <thead><tr><th className="table-index-column">序号</th><th>数据</th><th>本地路径</th><th>状态</th><th>权限</th></tr></thead>
              <tbody>{rows.map((item, index) => (
                <tr key={item.path}>
                  <td className="table-index-cell">{index + 1}</td>
                  <td><span className="table-primary">{item.label}</span></td>
                  <td><span className="settings-path">{item.path}</span></td>
                  <td><span className={`status-chip ${item.exists ? "success" : "neutral"}`}>{item.exists ? "已创建" : "尚未创建"}</span></td>
                  <td className="settings-mode-cell"><span className="settings-path">{item.mode}</span></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </div>
      ) : (
        <SettingsPanelEmptyState icon="▦" title="未发现本地数据项" description="请刷新；仍为空时检查版本支持。" actionLabel="刷新本地数据" onAction={onRefresh} />
      )}
    </section>
  );
}

function AuditPanel({ rows, onRefresh }: { rows: Array<{ timestamp: number; action: string; target: string; outcome: string }>; onRefresh: () => Promise<void> }) {
  return (
    <section className="settings-secondary-panel settings-data-panel" aria-labelledby="settings-audit-title">
      <SettingsDataPanelHeader
        id="settings-audit-title"
        eyebrow="ADMIN ACTIVITY"
        title="最近管理操作"
        description="查看操作时间、目标和结果。"
        summary={String(rows.length)}
        summaryLabel="条记录"
      />
      {rows.length ? (
        <div className="settings-table-panel">
          <div className="settings-table-viewport" role="region" aria-label="管理审计表格">
            <table className="audit-table">
              <thead><tr><th className="table-index-column">序号</th><th>时间</th><th>动作</th><th>目标</th><th>结果</th></tr></thead>
              <tbody>{rows.map((item, index) => (
                <tr key={`${item.timestamp}-${index}`}>
                  <td className="table-index-cell">{index + 1}</td>
                  <td className="settings-time-cell">{formatSiteTimestamp(item.timestamp)}</td>
                  <td><span className="settings-path">{item.action}</span></td>
                  <td>{item.target}</td>
                  <td><span className={`status-chip ${item.outcome === "accepted" ? "success" : "neutral"}`}>{item.outcome || "unknown"}</span></td>
                </tr>
              ))}</tbody>
            </table>
          </div>
        </div>
      ) : (
        <SettingsPanelEmptyState
          icon="◎"
          title="暂无管理操作"
          description="配置与维护操作将在此记录。"
          actionLabel="刷新审计记录"
          onAction={onRefresh}
        />
      )}
    </section>
  );
}

function SettingsDataPanelHeader({ id, eyebrow, title, description, summary, summaryLabel }: { id: string; eyebrow: string; title: string; description: string; summary: string; summaryLabel: string }) {
  return <header className="settings-data-panel-header"><div className="settings-data-panel-copy"><span>{eyebrow}</span><h2 id={id}>{title}</h2><p>{description}</p></div><div className="settings-data-panel-summary" aria-label={`${title}：${summary} ${summaryLabel}`}><strong>{summary}</strong><span>{summaryLabel}</span></div></header>;
}

function SettingsPanelEmptyState({ icon, title, description, actionLabel, onAction }: { icon: string; title: string; description: string; actionLabel?: string; onAction?: () => Promise<void> }) {
  return <div className="settings-panel-empty"><div className="settings-panel-empty-icon" aria-hidden="true">{icon}</div><h3>{title}</h3><p>{description}</p>{actionLabel && onAction ? <button className="button button-secondary" type="button" onClick={() => { void onAction().catch(() => undefined); }}>{actionLabel}</button> : null}</div>;
}

function flattenConfiguration(catalog?: ConfigurationCatalog): EditorField[] {
  return catalog?.groups.flatMap((group) => group.fields.map((field) => {
    const section = configurationSectionFor(field, group.name);
    return { ...field, group: section.category, section: section.id };
  })) ?? [];
}
function configurationDraft(catalog: ConfigurationCatalog): Draft { return Object.fromEntries(flattenConfiguration(catalog).map((field) => [field.key, draftValueFromConfiguration(field.value, field.type)])); }
function draftValueFromConfiguration(value: ConfigurationValue, type: ConfigurationField["type"]): DraftValue { if (type === "domain_list") return Array.isArray(value) ? value.join(", ") : ""; if (type === "proxy_url_secret") return ""; if (Array.isArray(value)) return value.join(", "); return value; }
function normalizeDraftValue(field: ConfigurationField, value: DraftValue): ConfigurationValue { if (field.type === "proxy_url_secret" && String(value ?? "").trim() === "") return field.value; if (field.type === "domain_list") return [...new Set(String(value ?? "").split(/[,，\s]+/).map((item) => item.trim().toLocaleLowerCase("zh-CN")).filter(Boolean))]; if (field.type === "boolean") return Boolean(value); if (["integer", "number", "nullable_integer"].includes(field.type)) { if (field.type === "nullable_integer" && String(value ?? "").trim() === "") return null; if (String(value ?? "").trim() === "") return ""; const number = Number(value); return Number.isFinite(number) ? number : String(value ?? "").trim(); } if (typeof value === "string") return value.trim(); return value; }
function sameConfigurationValue(left: ConfigurationValue, right: ConfigurationValue): boolean { return JSON.stringify(left) === JSON.stringify(right); }
function validateDraftValue(field: ConfigurationField, raw: DraftValue): string { if (field.type === "nullable_integer" && String(raw ?? "").trim() === "") return ""; if (["integer", "number", "nullable_integer"].includes(field.type)) { if (String(raw ?? "").trim() === "") return "请输入有效数字"; const value = Number(raw); if (!Number.isFinite(value)) return "请输入有效数字"; if ((field.type === "integer" || field.type === "nullable_integer") && !Number.isInteger(value)) return "请输入整数"; if (field.min !== undefined && value < field.min) return `不能小于 ${field.key === "portal.session_ttl_seconds" ? durationLimit(field.min) : field.min}`; if (field.max !== undefined && value > field.max) return `不能大于 ${field.key === "portal.session_ttl_seconds" ? durationLimit(field.max) : field.max}`; return ""; } if (field.type === "boolean" || field.type === "choice" || field.type === "domain_list") return ""; const value = String(raw ?? "").trim(); if (field.type === "proxy_url_secret" && value === "") return ""; if (["optional_text", "optional_image", "base_url"].includes(field.type) && value === "") return ""; if (!value) return "不能为空"; if (field.min_length !== undefined && [...value].length < field.min_length) return `至少输入 ${field.min_length} 个字符`; if (field.max_length !== undefined && [...value].length > field.max_length) return `最多输入 ${field.max_length} 个字符`; if (field.type === "key_prefix" && !/^[a-z][a-z0-9_]{1,30}_$/.test(value)) return "请输入 3-32 位小写前缀，并以下划线结尾"; if (field.type === "env_name" && !/^[A-Z][A-Z0-9_]{1,63}$/.test(value)) return "请输入有效的大写环境变量名"; if (field.type === "color" && !/^#[0-9a-fA-F]{6}$/.test(value)) return "请输入 #RRGGBB 颜色"; if (field.type === "duration") { const seconds = configurationDurationSeconds(field, value); if (seconds === null) return "请输入有效时长"; if (seconds < durationBounds.min) return `不能小于 ${durationLimit(durationBounds.min)}`; if (seconds > durationBounds.max) return `不能大于 ${durationLimit(durationBounds.max)}`; return ""; } if (field.type === "time_list" && !/^([01]?\d|2[0-3]):[0-5]\d(?:\s*[,，]\s*([01]?\d|2[0-3]):[0-5]\d)*$/.test(value)) return "请输入 HH:MM，多个时间使用逗号分隔"; if ((field.type === "base_url" || field.type === "proxy_url_secret") && !validConfigurationURL(value, field.type === "proxy_url_secret")) return "请输入有效的 HTTP(S) 或 SOCKS5 根地址"; if (field.type === "ip" && !validIPv4(value)) return "请输入有效 IPv4 地址"; if ((field.type === "image" || field.type === "optional_image") && !/^[A-Za-z0-9._:/@-]+$/.test(value)) return "镜像名称格式无效"; if (field.digest_required && !/^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$/.test(value)) return "必须使用 name:tag@sha256:digest 固定镜像"; return ""; }
function validConfigurationURL(value: string, proxy: boolean): boolean { try { const parsed = new URL(value); if (!["http:", "https:", ...(proxy ? ["socks5:"] : [])].includes(parsed.protocol)) return false; return Boolean(parsed.hostname) && parsed.pathname === "/" && !parsed.search && !parsed.hash && (proxy || (!parsed.username && !parsed.password)); } catch { return false; } }
function validIPv4(value: string): boolean { const parts = value.split("."); return parts.length === 4 && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) >= 0 && Number(part) <= 255); }
function applyModeLabel(mode: ConfigurationField["apply_mode"], key = ""): string { if (key === "runtime.cliproxy_image") return "镜像管理"; return ({ live: "立即生效", accounts: "重建业务 CPA", collector: "重启采集器", future: "仅新账号", deployment: "账号重建生效", quota: "下次采集生效" })[mode]; }
function configurationEffects(fields: EditorField[]): string[] { const modes = new Set(fields.map((field) => field.apply_mode)); return [modes.has("accounts") ? "业务 CPA 会依次重建" : "", modes.has("collector") ? "用量采集器会重启" : "", modes.has("quota") ? "用户额度下次采集后生效" : "", modes.has("deployment") ? "CPA 参数在账号重建后生效" : ""].filter(Boolean); }
function validateLogoFile(file: File): string { if (!supportedLogoTypes.has(file.type)) return "仅支持 PNG、JPEG、GIF、WebP 或 SVG 文件"; if (file.size < 1) return "Logo 文件不能为空"; if (file.size > maxLogoBytes) return "Logo 文件不能超过 2 MiB"; if ([...file.name].length > 128) return "Logo 文件名不能超过 128 个字符"; return ""; }
function reasoningEffortLabel(effort: string): string { return ({ none: "无", minimal: "最小", low: "低", medium: "中", high: "高", xhigh: "极高", max: "最大", ultra: "超高", auto: "自动", unknown: "未知" } as Record<string, string>)[effort] ?? effort; }
function reasoningColorPresentation(value: string, fallback = "#687287") { const color = /^#[0-9a-f]{6}$/i.test(value) ? value.toLowerCase() : fallback; const channels = [1, 3, 5].map((index) => Number.parseInt(color.slice(index, index + 2), 16) / 255).map((channel) => channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4); const luminance = 0.2126 * channels[0] + 0.7152 * channels[1] + 0.0722 * channels[2]; return { color, text: luminance > 0.179 ? "#171d2b" : "#ffffff" }; }
