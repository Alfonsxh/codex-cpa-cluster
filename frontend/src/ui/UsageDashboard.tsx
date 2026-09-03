import { Alert, App as AntApp, Button, Form, Input, Modal, Skeleton, Space, Tooltip } from "antd";
import { ArrowDownOutlined, ArrowUpOutlined, CopyOutlined, QuestionCircleOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { flushSync } from "react-dom";

import { ApiError } from "../api/client";
import {
  autoAssignPortalAccount,
  portalAccountsQueryKey,
  portalAccountsQueryRoot,
  portalBreakdownQueryKey,
  portalProfileQueryKey,
  portalQuotaQueryKey,
  portalRouteQueryKey,
  readPortalAccounts,
  readPortalBreakdown,
  readPortalKey,
  readPortalProfile,
  readPortalQuota,
  readPortalRoute,
  rotatePortalKey,
  switchPortalAccount,
  type PortalAccount,
  type PortalQuota,
  type PortalUsageWindow
} from "../api/portal";
import type { UsageBreakdown, UsageCombination, UsageMetrics } from "../api/generated";
import { PortalClientConfigModal, type PortalClientConfigMode } from "./PortalClientConfigModal";
import { PortalDailyUsageTrend } from "./PortalDailyUsageTrend";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { formatTokenAmount, formatTokens } from "./formatters";

type SortField = "current" | "account" | "quota" | "active_users" | "status" | "requests" | "tokens" | "last_used";
type SortState = { field: SortField; direction: "asc" | "desc"; pinCurrent: boolean };
type PrimarySection = "trend" | "accounts";

const defaultSort: SortState = { field: "quota", direction: "asc", pinCurrent: true };

export function UsageDashboard({ onSessionExpired }: { onSessionExpired: () => void }) {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [window, setWindow] = useState<PortalUsageWindow>("today");
  const [sort, setSort] = useState<SortState>(defaultSort);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());
  const [primarySection, setPrimarySection] = useState<PrimarySection>("trend");
  const [showKey, setShowKey] = useState(false);
  const [keyOpen, setKeyOpen] = useState(false);
  const [keyValue, setKeyValue] = useState("");
  const [keyLoading, setKeyLoading] = useState(false);
  const [keyError, setKeyError] = useState("");
  const keyRequest = useRef<AbortController | null>(null);
  const autoAssignAttempted = useRef(false);
  const [rotationOpen, setRotationOpen] = useState(false);
  const [clientConfigMode, setClientConfigMode] = useState<PortalClientConfigMode | null>(null);
  const [switchTarget, setSwitchTarget] = useState<PortalAccount | null>(null);

  const profile = useQuery({
    queryKey: portalProfileQueryKey,
    queryFn: ({ signal }) => readPortalProfile(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: true
  });
  const accounts = useQuery({
    queryKey: portalAccountsQueryKey(window),
    queryFn: ({ signal }) => readPortalAccounts(window, signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: true
  });
  const quota = useQuery({
    queryKey: portalQuotaQueryKey,
    queryFn: ({ signal }) => readPortalQuota(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: true
  });
  const route = useQuery({
    queryKey: portalRouteQueryKey,
    queryFn: ({ signal }) => readPortalRoute(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: true,
    refetchInterval: 10_000
  });

  useEffect(() => {
    if ([profile.error, quota.error, accounts.error, route.error].some(isUnauthorized)) onSessionExpired();
  }, [accounts.error, onSessionExpired, profile.error, quota.error, route.error]);
  useEffect(() => () => keyRequest.current?.abort(), []);
  useEffect(() => setExpanded(new Set()), [window]);

  const currentGroup = route.data?.current_group ?? accounts.data?.current_group ?? profile.data?.current_group ?? "";
  const currentAccount = accounts.data?.accounts.find((item) => item.id === currentGroup);
  const sortedAccounts = useMemo(
    () => sortAccounts(accounts.data?.accounts ?? [], currentGroup, sort),
    [accounts.data?.accounts, currentGroup, sort]
  );

  const accountSwitch = useMutation({
    mutationFn: (account: PortalAccount) => switchPortalAccount(account.id),
    onSuccess: async (result) => {
      queryClient.setQueryData(portalRouteQueryKey, {
        current_group: result.current_group,
        generated_at: Math.floor(Date.now() / 1000)
      });
      setSwitchTarget(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: portalProfileQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: portalQuotaQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: portalAccountsQueryKey(window), exact: true })
      ]);
      void message.success(result.changed ? "账号已切换并完成 Gateway 激活确认" : "当前已使用该账号");
    }
  });
  const autoAssignment = useMutation({
    mutationFn: autoAssignPortalAccount,
    onSuccess: async (result) => {
      queryClient.setQueryData(portalRouteQueryKey, {
        current_group: result.current_group,
        generated_at: Math.floor(Date.now() / 1000)
      });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: portalProfileQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: portalQuotaQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: portalAccountsQueryRoot })
      ]);
      void message.success(result.changed ? `已自动分配 ${accountLabelByID(accounts.data?.accounts ?? [], result.current_group)}` : "已恢复当前 CPA 账号");
    }
  });
  const rotation = useMutation({
    gcTime: 0,
    mutationFn: rotatePortalKey,
    onSuccess: (result) => {
      setKeyValue(result.api_key);
      setKeyError("");
      setShowKey(true);
      setKeyOpen(true);
      setRotationOpen(false);
      rotation.reset();
      void message.success("API Key 已刷新；旧 Key 已立即失效");
    }
  });

  useEffect(() => {
    if (!route.isSuccess || !accounts.isSuccess || currentGroup || accounts.data.accounts.length === 0 || autoAssignAttempted.current) return;
    autoAssignAttempted.current = true;
    autoAssignment.mutate();
  }, [accounts.data, accounts.isSuccess, currentGroup, route.isSuccess]);

  const copyKey = async () => {
    if (!keyValue) return;
    try {
      await navigator.clipboard.writeText(keyValue);
      void message.success("API Key 已复制");
    } catch {
      void message.error("浏览器未允许复制，请展开后手动复制");
    }
  };
  const revealKey = async () => {
    keyRequest.current?.abort();
    const request = new AbortController();
    keyRequest.current = request;
    setKeyValue("");
    setKeyError("");
    setShowKey(false);
    setKeyLoading(true);
    setKeyOpen(true);
    try {
      const result = await readPortalKey(request.signal);
      if (keyRequest.current === request) setKeyValue(result.api_key);
    } catch (error) {
      if (!request.signal.aborted && keyRequest.current === request) setKeyError(errorMessage(error));
    } finally {
      if (keyRequest.current === request) {
        keyRequest.current = null;
        setKeyLoading(false);
      }
    }
  };
  const closeKey = () => {
    keyRequest.current?.abort();
    keyRequest.current = null;
    flushSync(() => {
      setKeyValue("");
      setKeyError("");
      setKeyLoading(false);
      setShowKey(false);
    });
    setKeyOpen(false);
  };
  const refresh = () => void Promise.all([profile.refetch(), quota.refetch(), accounts.refetch(), route.refetch()]);
  const toggleExpanded = (accountID: string) => {
    setExpanded((current) => {
      const next = new Set(current);
      if (next.has(accountID)) next.delete(accountID);
      else next.add(accountID);
      return next;
    });
  };
  const changeSort = (field: SortField) => {
    setSort((current) => current.field === field
      ? { field, direction: current.direction === "asc" ? "desc" : "asc", pinCurrent: field === "quota" }
      : { field, direction: field === "account" || field === "status" || field === "quota" ? "asc" : "desc", pinCurrent: field === "quota" });
  };

  return (
    <section className="usage-dashboard">
      <section className="usage-key-card" aria-label="个人凭据与用量摘要">
        <div className="usage-key-panel">
          <div className="usage-key-value">
            <span>我的 API Key</span>
            <code aria-label="API Key 安全状态">出于安全，仅在需要时读取</code>
          </div>
          <div className="usage-key-actions">
            <button className="usage-secondary-button usage-credential-entry" type="button" onClick={() => void revealKey()}>管理 API Key</button>
            <button className="usage-secondary-button" type="button" onClick={() => setClientConfigMode("codex")}>配置 Codex</button>
            <button className="usage-secondary-button" type="button" onClick={() => setClientConfigMode("claude")}>配置 Claude Code</button>
            <button className="usage-primary-button" type="button" onClick={() => setClientConfigMode("ccswitch")}>导入 CC Switch</button>
          </div>
        </div>

        <div className="usage-token-overview">
          <CurrentAccountSummary account={currentAccount} loading={route.isPending || accounts.isPending} />
          <PersonalQuotaSummary quota={quota.data} loading={quota.isPending} error={quota.error} onRetry={() => void quota.refetch()} />
          <RangeSummary window={window} metrics={accounts.data?.totals} loading={accounts.isPending} />
        </div>
      </section>

      {accounts.data?.warnings.map((warning) => (
        <section className="usage-route-notice" role="status" key={warning}><strong>账号提示</strong><span>{warning}</span></section>
      ))}
      {!currentGroup && !accounts.isPending ? (
        <Alert
          className="usage-route-alert"
          type={autoAssignment.isError ? "error" : "info"}
          showIcon
          role={autoAssignment.isError ? "alert" : "status"}
          title={autoAssignment.isPending ? "正在自动分配 CPA" : autoAssignment.isError ? "自动分配 CPA 失败" : "暂时无法自动分配 CPA"}
          description={autoAssignment.isPending
            ? "正在选择周额度使用最少的可用账号并完成 Gateway 激活确认。"
            : autoAssignment.isError
              ? `${errorMessage(autoAssignment.error)}；可以重试自动分配，也可以在账号明细中手动选择。`
              : accounts.data?.accounts.length
                ? "自动分配尚未完成，可以重试或在账号明细中手动选择。"
                : "当前没有可用账号；账号就绪后刷新页面会自动重试。"}
          action={autoAssignment.isError ? <Button size="small" onClick={() => { autoAssignment.reset(); autoAssignment.mutate(); }}>重试自动分配</Button> : undefined}
        />
      ) : null}

      <div className={`usage-detail-sections ${primarySection === "trend" ? "trend-primary" : "accounts-primary"}`}>
        <PortalDailyUsageTrend
          expanded={primarySection === "trend"}
          onExpandedChange={(next) => setPrimarySection(next ? "trend" : "accounts")}
          onSessionExpired={onSessionExpired}
        />

      <section className={`usage-account-section${primarySection === "accounts" ? "" : " collapsed"}`} aria-labelledby="usage-account-section-title">
        <div className="usage-section-toolbar">
          <h2 id="usage-account-section-title">账号明细</h2>
          {primarySection === "accounts" ? <div className="usage-toolbar-actions">
            <div className="usage-window-switcher" role="group" aria-label="统计时间范围">
              {portalWindowOptions.map((option) => (
                <button type="button" key={option.value} aria-pressed={window === option.value} onClick={() => setWindow(option.value)}>{option.label}</button>
              ))}
            </div>
            <button className="usage-refresh-button" type="button" disabled={profile.isFetching || quota.isFetching || accounts.isFetching || route.isFetching} onClick={refresh}>
              {accounts.isFetching && !accounts.isPending ? "刷新中…" : "刷新"}
            </button>
            <time className="usage-updated">额度更新 {formatTimestamp(accounts.data?.generated_at ?? quota.data?.generated_at ?? 0)}</time>
          </div> : <span className="usage-account-summary">{accounts.isPending ? "正在读取账号" : `${sortedAccounts.length} 个账号`}</span>}
        </div>

        {primarySection === "accounts" && accounts.isError ? (
          <div className="usage-error" role="alert">
            <span><strong>账号与用量加载失败</strong> · {errorMessage(accounts.error)}</span>
            <button className="usage-secondary-button" type="button" onClick={() => void accounts.refetch()}>重新加载</button>
          </div>
        ) : primarySection === "accounts" ? (
          <NativeTableViewport className="usage-table-wrap" aria-label="账号明细表格">
            <table className="usage-account-table">
              <thead>
                <tr>
                  <th className="table-index-column" scope="col">序号</th>
                  <SortableHeader field="current" label="当前账号" sort={sort} onSort={changeSort} />
                  <SortableHeader field="account" label="CPA 账号" sort={sort} onSort={changeSort} />
                  <SortableHeader field="quota" label="账号周额度" detail="所有用户共享 · 已用较少优先" sort={sort} onSort={changeSort} />
                  <SortableHeader field="active_users" label="活跃用户" detail="近 1 小时" sort={sort} onSort={changeSort} />
                  <SortableHeader field="status" label="账号状态" sort={sort} onSort={changeSort} />
                  <SortableHeader field="requests" label="我的请求" detail={windowLabel(window)} sort={sort} onSort={changeSort} />
                  <SortableHeader className="usage-token-header" field="tokens" label="我的 Token" detail={windowLabel(window)} sort={sort} onSort={changeSort} />
                  <SortableHeader field="last_used" label="最后使用" detail="我的记录" sort={sort} onSort={changeSort} />
                  <th scope="col"><span className="sr-only">使用明细</span></th>
                </tr>
              </thead>
              <tbody>
                {accounts.isPending ? <UsageTableSkeleton /> : null}
                {!accounts.isPending && sortedAccounts.map((account, index) => (
                  <AccountRows
                    key={account.id}
                    account={account}
                    index={index}
                    currentGroup={currentGroup}
                    window={window}
                    expanded={expanded.has(account.id)}
                    onToggle={() => toggleExpanded(account.id)}
                    onSwitch={() => { accountSwitch.reset(); setSwitchTarget(account); }}
                  />
                ))}
              </tbody>
            </table>
            {!accounts.isPending && sortedAccounts.length === 0 ? <div className="usage-empty">暂无可用账号</div> : null}
          </NativeTableViewport>
        ) : null}
      </section>

        <nav className="usage-section-switcher" aria-label="切换主要内容区域">
          <Button type="text" icon={<ArrowUpOutlined aria-hidden="true" />} aria-label="展开每日用量趋势" aria-pressed={primarySection === "trend"} onClick={() => setPrimarySection("trend")} />
          <Button type="text" icon={<ArrowDownOutlined aria-hidden="true" />} aria-label="展开账号明细" aria-pressed={primarySection === "accounts"} onClick={() => setPrimarySection("accounts")} />
        </nav>
      </div>

      <Modal title={`切换到 ${switchTarget ? accountLabel(switchTarget) : "目标账号"}`} open={Boolean(switchTarget)} okText="确认切换" cancelText="取消" confirmLoading={accountSwitch.isPending} onCancel={() => !accountSwitch.isPending && setSwitchTarget(null)} onOk={() => switchTarget && accountSwitch.mutate(switchTarget)} destroyOnHidden>
        <Space orientation="vertical" size={16} className="portal-form-stack">
          <Alert type="info" showIcon title="现有 API Key 不会改变" description="只更新你的目标 CPA。系统会原子写入路由、发布鉴权快照并等待 Gateway 确认；失败时自动恢复原路由。" />
          {accountSwitch.isError ? <Alert type="error" showIcon title="账号切换失败" description={errorMessage(accountSwitch.error)} /> : null}
        </Space>
      </Modal>

      <Modal title="管理 API Key" open={keyOpen} footer={<Button onClick={closeKey}>关闭</Button>} onCancel={closeKey} destroyOnHidden>
        <Space orientation="vertical" size={16} className="portal-form-stack">
          {keyLoading ? <Skeleton.Input active block /> : null}
          {keyError ? <Alert type="error" showIcon title="API Key 读取失败" description={keyError} /> : null}
          {keyValue ? (
            <Form.Item label="API Key">
              <Space.Compact block>
                <Input.Password value={keyValue} readOnly visibilityToggle={{ visible: showKey, onVisibleChange: setShowKey }} aria-label="API Key" autoComplete="off" />
                <Button type="primary" icon={<CopyOutlined aria-hidden="true" />} onClick={() => void copyKey()}>复制</Button>
              </Space.Compact>
            </Form.Item>
          ) : null}
          <section className="usage-key-danger-zone" aria-label="API Key 危险操作">
            <div><strong>刷新 API Key</strong><p>旧 Key 会在 Gateway 激活新鉴权快照后立即失效，需要同步更新所有客户端。</p></div>
            <Button danger onClick={() => { rotation.reset(); setRotationOpen(true); }}>刷新 API Key</Button>
          </section>
        </Space>
      </Modal>

      <Modal title="刷新个人 API Key" open={rotationOpen} okText="确认刷新并使旧 Key 失效" cancelText="取消" okButtonProps={{ danger: true }} confirmLoading={rotation.isPending} onCancel={() => !rotation.isPending && setRotationOpen(false)} onOk={() => rotation.mutate()} destroyOnHidden>
        <Space orientation="vertical" size={16} className="portal-form-stack">
          <Alert type="warning" showIcon title="旧 API Key 会立即失效" description="刷新成功后，请立刻把新 Key 更新到 Codex 客户端。系统仅在 Gateway 已激活新鉴权快照后返回成功。" />
          {rotation.isError ? <Alert type="error" showIcon title="API Key 刷新失败" description={errorMessage(rotation.error)} /> : null}
        </Space>
      </Modal>

      <PortalClientConfigModal open={clientConfigMode !== null} mode={clientConfigMode ?? "codex"} user={profile.data?.user ?? "user"} currentGroup={currentGroup} onClose={() => setClientConfigMode(null)} onSessionExpired={onSessionExpired} />
    </section>
  );
}

function CurrentAccountSummary({ account, loading }: { account?: PortalAccount; loading: boolean }) {
  const used = account ? accountUsedPercent(account) : 0;
  const remaining = account?.status.remaining_percent ?? (account ? Math.max(0, 100 - used) : 0);
  return (
    <div className="usage-current-account" aria-labelledby="current-account-label">
      <div className="usage-current-account-head">
        <span className="usage-current-account-label" id="current-account-label">当前账号</span>
        {loading && !account ? <span className="usage-status degraded">读取中</span> : account ? <StatusTag account={account} /> : <span className="usage-status degraded">待选择</span>}
      </div>
      <strong className="usage-current-account-name" title={account ? accountLabel(account) : undefined}>{account ? accountLabel(account) : loading ? "正在读取" : "尚未选择"}</strong>
      <div className={`usage-current-quota ${used >= 100 ? "exhausted" : used >= 80 ? "warning" : ""}`.trim()}>
        <div><span>{account ? `周额度 ${formatPercent(used)}` : "选择可用账号后显示"}</span><strong>{account ? `剩余 ${formatPercent(remaining)}` : "—"}</strong></div>
        <progress className="usage-quota-track" max="100" value={used} aria-label={account ? `当前账号周额度已使用 ${formatPercent(used)}` : "尚未选择当前账号"} />
      </div>
    </div>
  );
}

function PersonalQuotaSummary({ quota, loading, error, onRetry }: { quota?: PortalQuota; loading: boolean; error: unknown; onRetry: () => void }) {
  const weekly = quota?.weekly_quota;
  const percent = weekly?.unlimited ? 0 : clampPercent(weekly?.used_percent ?? 0);
  const remaining = weekly?.unlimited ? null : Math.max(0, 100 - percent);
  const quotaTooltip = weekly ? (
    <div className="usage-quota-tooltip">
      <strong>个人周额度</strong>
      <span><b>加权已用</b><em>{formatQuotaToken(weekly.weighted_used_tokens)}</em></span>
      <span><b>未加权已用</b><em>{formatQuotaToken(weekly.raw_used_tokens)}</em></span>
      <span><b>总额度</b><em>{weekly.unlimited ? "不限额" : formatQuotaToken(weekly.limit_tokens ?? 0)}</em></span>
      <span><b>剩余额度</b><em>{weekly.unlimited ? "不限额" : formatQuotaToken(weekly.remaining_tokens ?? 0)}</em></span>
    </div>
  ) : null;
  return (
    <div className="usage-personal-overview" aria-labelledby="personal-usage-label">
      <div className="usage-personal-overview-head"><span id="personal-usage-label">个人用量</span><small>{weekly ? quotaSourceLabel(weekly.source) : "组织默认"}</small></div>
      {error ? (
        <div className="usage-current-quota degraded">
          <div><span>个人周额度读取失败</span><button className="usage-inline-retry" type="button" onClick={onRetry}>重试</button></div>
          <progress className="usage-quota-track" max="100" value="0" aria-label="个人周额度暂不可用" />
        </div>
      ) : (
        <div className={`usage-current-quota ${weekly?.limit_reached ? "exhausted" : percent >= 90 ? "warning" : ""}`.trim()}>
          <div><span>{loading ? "周额度正在读取…" : weekly?.unlimited ? "周额度不限额" : `周额度 ${formatPercent(percent)}`}</span><strong>{loading ? "—" : weekly?.unlimited ? "剩余不限额" : `剩余 ${formatPercent(remaining ?? 0)}`}</strong></div>
          <progress className="usage-quota-track" max="100" value={percent} aria-label={weekly?.unlimited ? "个人周额度不限额" : `个人周额度已使用 ${formatPercent(percent)}`} />
          <div className="usage-personal-quota-detail">
            <span>{weekly ? weekly.unlimited ? `加权已用 ${formatTokens(weekly.weighted_used_tokens)}` : `加权已用 ${formatTokens(weekly.weighted_used_tokens)} / ${formatTokens(weekly.limit_tokens ?? 0)}` : "用量正在读取…"}{weekly ? <UsageHelp
              label="查看个人周额度 Token 说明"
              title={quotaTooltip}
            /> : null}</span>
            <span>{weekly ? `未加权累计 ${formatTokens(weekly.raw_used_tokens)}` : "未加权用量正在读取…"}</span>
            <time>{weekly ? `${formatTimestamp(weekly.week_end_at)} 重置` : "—"}</time>
          </div>
        </div>
      )}
    </div>
  );
}

function RangeSummary({ window, metrics, loading }: { window: PortalUsageWindow; metrics?: UsageMetrics; loading: boolean }) {
  return (
    <div className="usage-range-overview" aria-labelledby="usage-summary-label">
      <div className="usage-range-overview-head"><span id="usage-summary-label">{windowLabel(window)} Token</span><small>全部 CPA</small></div>
      <div className="usage-range-token-pair">
        <div><span>未加权</span><strong>{loading ? "—" : <TokenValue value={metrics?.total_tokens ?? 0} />}</strong></div>
        <div><span>加权</span><strong>{loading ? "—" : <TokenValue value={metrics?.weighted_tokens ?? metrics?.total_tokens ?? 0} />}</strong></div>
      </div>
      <span className="usage-range-requests">{windowLabel(window)}请求 {loading ? "—" : formatNumber(metrics?.request_count ?? 0)}</span>
    </div>
  );
}

function SortableHeader({ field, label, detail, className = "", sort, onSort }: { field: SortField; label: string; detail?: string; className?: string; sort: SortState; onSort: (field: SortField) => void }) {
  const active = sort.field === field;
  return (
    <th className={className} scope="col" aria-sort={active ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}>
      <button className={`usage-sort-button ${active ? "active" : ""}`.trim()} data-direction={active ? sort.direction : undefined} type="button" aria-label={`${label}${active ? `，当前${sort.direction === "asc" ? "升序" : "降序"}` : "，点击排序"}`} onClick={() => onSort(field)}>
        <span className="usage-sort-copy"><span>{label}</span>{detail ? <small>{detail}</small> : null}</span>
      </button>
    </th>
  );
}

function AccountRows({ account, index, currentGroup, window, expanded, onToggle, onSwitch }: { account: PortalAccount; index: number; currentGroup: string; window: PortalUsageWindow; expanded: boolean; onToggle: () => void; onSwitch: () => void }) {
  const current = account.id === currentGroup;
  const used = accountUsedPercent(account);
  const remaining = account.status.remaining_percent ?? Math.max(0, 100 - used);
  const rowKeyDown = (event: React.KeyboardEvent<HTMLTableRowElement>) => {
    if (event.target !== event.currentTarget || (event.key !== "Enter" && event.key !== " ")) return;
    event.preventDefault();
    onToggle();
  };
  return (
    <>
      <tr className={`usage-summary-row ${current ? "current" : ""}`.trim()} aria-expanded={expanded} tabIndex={0} onKeyDown={rowKeyDown} onClick={(event) => { if (!(event.target as HTMLElement).closest("button, a")) onToggle(); }}>
        <td className="table-index-cell" data-label="序号">{index + 1}</td>
        <td data-label="当前账号">
          {current ? <span className="usage-current-mark" title="当前账号">✓<span className="sr-only">当前账号</span></span> : <button className="usage-select-button" type="button" disabled={!account.selectable || !account.status.selectable} title={account.status.reason} onClick={onSwitch}>{currentGroup ? "切换" : "选择"}</button>}
        </td>
        <td data-label="CPA 账号"><strong className="usage-account-id" title={accountLabel(account)}>{accountLabel(account)}</strong></td>
        <td data-label="账号周额度">
          <div className={`usage-quota ${used >= 100 ? "exhausted" : used >= 80 ? "warning" : ""}`.trim()}>
            <div><strong>{formatPercent(used)}</strong><span>剩余 {formatPercent(remaining)}</span></div>
            <progress className="usage-quota-track" max="100" value={used} aria-label={`已使用 ${formatPercent(used)}`} />
            <small>{account.status.reset_at ? `${formatTimestamp(account.status.reset_at)} 重置` : "重置时间未知"}</small>
          </div>
        </td>
        <td data-label="活跃用户（近 1 小时）"><strong className="usage-cell-number">{formatNumber(account.active_users_1h)}</strong></td>
        <td data-label="账号状态"><StatusTag account={account} /></td>
        <td data-label={`我的请求（${windowLabel(window)}）`}><strong className="usage-cell-number" title={formatNumber(account.usage.request_count)}>{formatCompact(account.usage.request_count)}</strong></td>
        <td className="usage-token-cell" data-label={`我的 Token（${windowLabel(window)}）`}><div className="usage-token-content"><TokenPair metrics={account.usage} /></div></td>
        <td data-label="我的最后使用"><time className="usage-last-used">{formatTimestamp(account.usage.last_used_at)}</time></td>
        <td><button className="usage-expand-button" type="button" aria-label={expanded ? "收起使用明细" : "使用明细"} aria-expanded={expanded} onClick={onToggle}>{expanded ? "−" : "+"}</button></td>
      </tr>
      {expanded ? <UsageBreakdownRow account={account} window={window} /> : null}
    </>
  );
}

function UsageBreakdownRow({ account, window }: { account: PortalAccount; window: PortalUsageWindow }) {
  const query = useQuery({
    queryKey: portalBreakdownQueryKey(account.id, window),
    queryFn: ({ signal }) => readPortalBreakdown(account.id, window, signal),
    staleTime: 30_000,
    gcTime: 30_000,
    retry: false,
    refetchOnWindowFocus: false
  });
  return (
    <tr className="usage-detail-row" data-detail-for={account.id}>
      <td colSpan={10}>
        <div className="usage-account-detail">
          <div className="usage-detail-panel">
            <div className="usage-detail-heading"><strong>我的使用明细</strong><span>{windowLabel(window)}</span></div>
            {query.data ? <UsageTokenGrid metrics={query.data.totals} /> : <div className="usage-token-grid usage-token-grid-placeholder" aria-label="正在加载我的模型 Token 明细">{Array.from({ length: 8 }, (_, index) => <Skeleton.Input active key={index} />)}</div>}
          </div>
          <section className="account-model-usage" aria-label="我的模型与推理强度 Token 明细">
            <div className="account-model-usage-title"><span>我的模型 × 推理强度 Token 明细</span><small>{windowLabel(window)}</small></div>
            {query.isError ? <div className="account-model-usage-message error" role="alert"><span>{errorMessage(query.error)}</span><button className="usage-breakdown-retry" type="button" onClick={() => void query.refetch()}>重试</button></div> : query.data ? <ModelBreakdown data={query.data} /> : <div className="account-model-usage-skeleton" aria-label="正在加载我的模型 Token 明细"><span /><span /></div>}
          </section>
        </div>
      </td>
    </tr>
  );
}

function UsageTokenGrid({ metrics }: { metrics: UsageMetrics }) {
  const cacheRate = metrics.input_tokens > 0 ? formatPercent((metrics.cached_tokens / metrics.input_tokens) * 100) : "0%";
  return (
    <div className="usage-token-grid">
      <Metric label="成功请求" value={formatNumber(metrics.success_count)} />
      <Metric label="失败请求" value={formatNumber(metrics.failed_count)} />
      <Metric label="输入 Token" value={formatTokens(metrics.input_tokens)} />
      <Metric label="输出 Token" value={formatTokens(metrics.output_tokens)} />
      <Metric label="推理 Token" value={formatTokens(metrics.reasoning_tokens)} />
      <div><div className="usage-cache-head"><span>缓存 Token</span><small className="usage-cache-rate">缓存率 {cacheRate}</small></div><strong>{formatTokens(metrics.cached_tokens)}</strong></div>
      <Metric label="未加权 Token" value={formatTokens(metrics.total_tokens)} />
      <Metric label="加权 Token" value={formatTokens(metrics.weighted_tokens ?? metrics.total_tokens)} />
    </div>
  );
}

function Metric({ label, value }: { label: string; value: string }) {
  return <div><span>{label}</span><strong>{value}</strong></div>;
}

function UsageHelp({ label, title }: { label: string; title: ReactNode }) {
  return (
    <Tooltip title={title} trigger={["hover", "focus"]} placement="top">
      <button className="usage-help-button" type="button" aria-label={label}>
        <QuestionCircleOutlined aria-hidden="true" />
      </button>
    </Tooltip>
  );
}

function formatQuotaToken(value: number) {
  return `${formatTokenAmount(value)} Token`;
}

function ModelBreakdown({ data }: { data: UsageBreakdown }) {
  const models = groupModelCombinations(data.combinations);
  if (models.length === 0) return <div className="account-model-usage-message">当前范围暂无我的模型与推理强度 Token 数据。</div>;
  return (
    <div className="account-model-usage-list">
      {models.map((model) => (
        <div className="account-model-usage-row" key={model.name}>
          <div className="account-model-usage-head"><strong title={model.name}>{model.name}</strong><span>{formatTokens(model.total)}</span></div>
          <div className="account-model-progress" role="group" aria-label={`${model.name} 各推理强度 Token 占比`}>
            {model.efforts.map((effort) => (
              <button className={`account-model-progress-segment account-model-effort-${effortColorKey(effort.reasoning_effort)} ${effort.share < 18 ? "compact" : ""}`.trim()} style={{ flexGrow: Math.max(1, Math.round(effort.share)) }} type="button" key={effort.reasoning_effort} title={effortTooltip(model.name, effort)} aria-label={effortTooltip(model.name, effort)}>
                <span>{effort.reasoning_effort}</span><em>{formatPercent(effort.share)}</em>
              </button>
            ))}
          </div>
        </div>
      ))}
    </div>
  );
}

function groupModelCombinations(combinations: UsageCombination[]) {
  const grouped = new Map<string, UsageCombination[]>();
  for (const item of combinations) grouped.set(item.model, [...(grouped.get(item.model) ?? []), item]);
  return [...grouped.entries()].map(([name, efforts]) => {
    const total = efforts.reduce((sum, effort) => sum + (effort.weighted_tokens ?? effort.total_tokens), 0);
    return { name, total, efforts: efforts.map((effort) => ({ ...effort, share: total > 0 ? ((effort.weighted_tokens ?? effort.total_tokens) / total) * 100 : 0 })) };
  }).sort((left, right) => right.total - left.total || left.name.localeCompare(right.name));
}

function effortTooltip(model: string, effort: UsageCombination & { share: number }) {
  return [`${model} · ${effort.reasoning_effort}`, `调用：${formatNumber(effort.request_count)}`, `输入：${formatNumber(effort.input_tokens)}`, `输出：${formatNumber(effort.output_tokens)}`, `推理：${formatNumber(effort.reasoning_tokens)}`, `缓存：${formatNumber(effort.cached_tokens)}`, `总 Token：${formatNumber(effort.total_tokens)}`, `加权 Token：${formatNumber(effort.weighted_tokens ?? effort.total_tokens)}`].join("，");
}

function TokenPair({ metrics }: { metrics: UsageMetrics }) {
  return <div className="usage-user-token-pair"><div><small>加权</small><TokenValue value={metrics.weighted_tokens ?? metrics.total_tokens} /></div><div><small>未加权</small><TokenValue value={metrics.total_tokens} /></div></div>;
}

function TokenValue({ value }: { value: number }) {
  const safe = Math.max(0, Math.floor(Number.isFinite(value) ? value : 0));
  const compact = formatTokenAmount(safe);
  const [amount, unit = "Token"] = compact.split(" ");
  const exact = `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 }).format(safe)} Token`;
  return (
    <span className="token-usage">
      <span className="token-usage-main" aria-hidden="true"><span className="token-usage-value">{amount}</span><small className="token-usage-unit">{unit}</small></span>
      {safe >= 1_000 ? <small className="token-usage-exact" aria-hidden="true">{exact}</small> : null}
      <span className="token-usage-sr-only">{exact}</span>
    </span>
  );
}

function StatusTag({ account }: { account: PortalAccount }) {
  const className = account.status.tone === "success" ? "available" : account.status.tone === "warning" ? "warning" : account.status.tone === "danger" ? "unavailable" : "degraded";
  return <span className={`usage-status ${className}`} title={account.status.reason}>{account.status.label}</span>;
}

function UsageTableSkeleton() {
  return <>{Array.from({ length: 3 }, (_, row) => <tr className="usage-summary-row usage-skeleton-row" key={row} aria-label="正在加载账号与用量">{Array.from({ length: 10 }, (_item, column) => <td key={column}><span /></td>)}</tr>)}</>;
}

function accountLabel(account: PortalAccount) {
  return account.email.trim() || account.display_name;
}

function accountLabelByID(accounts: PortalAccount[], accountID: string) {
  const account = accounts.find((item) => item.id === accountID);
  return account ? accountLabel(account) : "可用 CPA 账号";
}

function sortAccounts(accounts: PortalAccount[], currentGroup: string, sort: SortState) {
  const direction = sort.direction === "asc" ? 1 : -1;
  return [...accounts].sort((left, right) => {
    if (sort.pinCurrent && (left.id === currentGroup) !== (right.id === currentGroup)) return left.id === currentGroup ? -1 : 1;
    const compared = compareSortValue(sortValue(left, currentGroup, sort.field), sortValue(right, currentGroup, sort.field));
    return compared * direction || accountLabel(left).localeCompare(accountLabel(right), "zh-CN", { numeric: true });
  });
}

function sortValue(account: PortalAccount, currentGroup: string, field: SortField): number | string | null {
  if (field === "current") return account.id === currentGroup ? 0 : 1;
  if (field === "account") return accountLabel(account);
  if (field === "quota") return Number.isFinite(accountUsedPercent(account)) ? accountUsedPercent(account) : null;
  if (field === "active_users") return account.active_users_1h;
  if (field === "status") return statusRank(account);
  if (field === "requests") return account.usage.request_count;
  if (field === "tokens") return account.usage.weighted_tokens ?? account.usage.total_tokens;
  return account.usage.last_used_at || null;
}

function compareSortValue(left: number | string | null, right: number | string | null) {
  if (left === null && right === null) return 0;
  if (left === null) return 1;
  if (right === null) return -1;
  if (typeof left === "string" || typeof right === "string") return String(left).localeCompare(String(right), "zh-CN", { numeric: true });
  return left - right;
}

function accountUsedPercent(account: PortalAccount) {
  if (account.status.used_percent !== undefined) return clampPercent(account.status.used_percent);
  if (account.status.remaining_percent !== undefined) return 100 - clampPercent(account.status.remaining_percent);
  return Number.POSITIVE_INFINITY;
}

function statusRank(account: PortalAccount) {
  return ({ available: 0, quota_warning: 1, transient_cooldown: 2, rate_limited: 3, degraded: 4, quota_unknown: 5, unknown: 6, quota_exhausted: 7, credential_unavailable: 8, auth_missing: 9, stopped: 10, disabled: 11 } as Record<string, number>)[account.status.code] ?? 12;
}

function quotaSourceLabel(source: string) {
  return ({ default: "组织默认", user_unlimited: "单独不限额", user_custom: "用户自定义" } as Record<string, string>)[source] ?? "状态未知";
}

function effortColorKey(value: string) {
  return ["none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra", "auto"].includes(value) ? value : "unknown";
}

function isUnauthorized(error: unknown) {
  return error instanceof ApiError && error.status === 401;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "请稍后重试";
}

function windowLabel(window: PortalUsageWindow) {
  return portalWindowOptions.find((item) => item.value === window)?.label ?? "当前范围";
}

function clampPercent(value: number) {
  return Math.max(0, Math.min(100, Number.isFinite(value) ? value : 0));
}

function formatPercent(value: number) {
  return `${new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format(value)}%`;
}

function formatNumber(value: number) {
  return new Intl.NumberFormat("zh-CN").format(Number(value) || 0);
}

function formatCompact(value: number) {
  return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 1 }).format(Number(value) || 0);
}

function formatTimestamp(timestamp: number) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(timestamp * 1000));
}

const portalWindowOptions: Array<{ value: PortalUsageWindow; label: string }> = [
  { value: "3600", label: "1 小时" },
  { value: "today", label: "今日" },
  { value: "86400", label: "24 小时" },
  { value: "604800", label: "7 天" }
];
