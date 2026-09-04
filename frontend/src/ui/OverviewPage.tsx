import { Alert, Button, Empty, Result, Skeleton, Spin, Typography } from "antd";
import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  lazy,
  Suspense,
  useEffect,
  useMemo,
  useState
} from "react";

import {
  overviewCatalogQueryKey,
  overviewStatusQueryKey,
  overviewSummaryQueryKey,
  readOverviewCatalog,
  readOverviewStatus,
  readOverviewSummary,
  readOverviewUsage,
  type OverviewUsageOptions,
  type OverviewUsageWindow,
  type TokenSeries
} from "../api/overview";
import { listRuntimeJobs, runtimeJobsQueryKey, type RuntimeJob } from "../api/runtime";
import { onboardingQueryKey, readOnboarding } from "../api/onboarding";
import { useAdminToolbar } from "./AdminToolbarContext";
import { AccountQuotaOverview } from "./AccountQuotaOverview";
import {
  CustomUsageRangeModal,
  type CustomUsageRange
} from "./components/CustomUsageRangeModal";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { LegacyUsageMultiSelect } from "./components/LegacyUsageMultiSelect";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { formatTokens } from "./formatters";
import { OnboardingCard } from "./OnboardingCard";

const { Text } = Typography;

type SeriesSortKey = "name" | "status" | "current" | "average" | "maximum" | "total";

type SortState = {
  key: SeriesSortKey;
  direction: "asc" | "desc";
};

type TokenMode = "unweighted" | "weighted";
type UsageSeriesView = "aggregate" | "account" | "user";

const overviewDisplayTimezone = "Asia/Shanghai";

const standardWindows: Array<{ value: Exclude<OverviewUsageWindow, "custom">; label: string }> = [
  { value: "3600", label: "1 小时" },
  { value: "21600", label: "6 小时" },
  { value: "today", label: "今日" },
  { value: "86400", label: "24 小时" },
  { value: "604800", label: "7 天" },
  { value: "2592000", label: "30 天" },
  { value: "since_reset", label: "本周期" }
];

const chartColors = [
  "#6374d8", "#4b8ccf", "#c58a34", "#9070c5", "#5263aa",
  "#c45757", "#d16f4f", "#b96894", "#447a9d", "#8b6d48"
];

const EChartsUsageChart = lazy(() => import("./components/UsageChart").then((module) => ({
  default: module.UsageChart
})));

export function OverviewPage() {
  const queryClient = useQueryClient();
  const [usageWindow, setUsageWindow] = useState<OverviewUsageWindow>("today");
  const [selectedAccounts, setSelectedAccounts] = useState<string[]>([]);
  const [selectedUsers, setSelectedUsers] = useState<string[]>([]);
  const [userLimit, setUserLimit] = useState(10);
  const [refreshSeconds, setRefreshSeconds] = useState(30);
  const [tokenMode, setTokenMode] = useState<TokenMode>("unweighted");
  const [usageView, setUsageView] = useState<UsageSeriesView>("aggregate");
  const [accountOptions, setAccountOptions] = useState<string[]>([]);
  const [userOptions, setUserOptions] = useState<string[]>([]);
  const [customRange, setCustomRange] = useState<CustomUsageRange | null>(null);
  const [customOpen, setCustomOpen] = useState(false);
  const { setRefreshing, setRefreshLabel, setRefreshAction } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();

  const overview = useQuery({
    queryKey: overviewSummaryQueryKey,
    queryFn: ({ signal }) => readOverviewSummary(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });
  const catalog = useQuery({
    queryKey: overviewCatalogQueryKey,
    queryFn: ({ signal }) => readOverviewCatalog(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const status = useQuery({
    queryKey: overviewStatusQueryKey,
    queryFn: ({ signal }) => readOverviewStatus(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const usageOptions = useMemo<OverviewUsageOptions>(() => ({
    window: usageWindow,
    accounts: selectedAccounts,
    users: selectedUsers,
    userLimit,
    tokenMode,
    startAt: usageWindow === "custom" ? customRange?.startAt : undefined,
    endAt: usageWindow === "custom" ? customRange?.endAt : undefined
  }), [customRange, selectedAccounts, selectedUsers, tokenMode, usageWindow, userLimit]);
  const usageQueryKey = useMemo(() => [
      "overview-usage",
      usageWindow,
      selectedAccounts,
      selectedUsers,
      userLimit,
      tokenMode,
      customRange?.startAt,
      customRange?.endAt
    ] as const, [customRange?.endAt, customRange?.startAt, selectedAccounts, selectedUsers, tokenMode, usageWindow, userLimit]);
  const usage = useQuery({
    queryKey: usageQueryKey,
    queryFn: ({ signal }) => readOverviewUsage(usageOptions, signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    placeholderData: keepPreviousData,
    refetchInterval: refreshSeconds > 0 ? refreshSeconds * 1000 : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false
  });
  const usageRefresh = useMutation({
    mutationFn: () => readOverviewUsage({ ...usageOptions, fresh: true }),
    onSuccess: (payload) => queryClient.setQueryData(usageQueryKey, payload)
  });
  const jobs = useQuery({
    queryKey: runtimeJobsQueryKey,
    queryFn: ({ signal }) => listRuntimeJobs(signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchOnWindowFocus: false
  });
  const onboarding = useQuery({
    queryKey: onboardingQueryKey,
    queryFn: ({ signal }) => readOnboarding(signal),
    staleTime: 30_000,
    retry: false,
    refetchOnWindowFocus: false
  });

  useEffect(() => {
    setRefreshAction(async () => {
      const [nextOverview, nextStatus, nextCatalog, nextUsage, nextJobs] = await Promise.all([
        readOverviewSummary(),
        readOverviewStatus(),
        readOverviewCatalog(),
        readOverviewUsage({ ...usageOptions, fresh: true }),
        listRuntimeJobs()
      ]);
      queryClient.setQueryData(overviewSummaryQueryKey, nextOverview);
      queryClient.setQueryData(overviewStatusQueryKey, nextStatus);
      queryClient.setQueryData(overviewCatalogQueryKey, nextCatalog);
      queryClient.setQueryData(usageQueryKey, nextUsage);
      queryClient.setQueryData(runtimeJobsQueryKey, nextJobs);
      showToast("总览与 Token 趋势已刷新");
    });
    return () => setRefreshAction(null);
  }, [queryClient, setRefreshAction, showToast, usageOptions, usageQueryKey]);

  useEffect(() => {
    if (catalog.data) {
      const accounts = catalog.data.accounts.map((account) => account.id);
      const users = catalog.data.users.map((user) => user.email);
      setAccountOptions(accounts);
      setUserOptions(users);
      const availableAccounts = new Set(accounts);
      const availableUsers = new Set(users);
      setSelectedAccounts((current) => current.filter((value) => availableAccounts.has(value)));
      setSelectedUsers((current) => current.filter((value) => availableUsers.has(value)));
      return;
    }
    if (!usage.data) return;
    setAccountOptions((current) => mergeOptions(current, usage.data.accounts.map((series) => series.name)));
    setUserOptions((current) => mergeOptions(current, usage.data.users.map((series) => series.name)));
  }, [catalog.data, usage.data]);
  const refreshing = overview.isFetching || status.isFetching || catalog.isFetching || usage.isFetching || usageRefresh.isPending || jobs.isFetching;
  useEffect(() => setRefreshing(refreshing), [refreshing, setRefreshing]);
  useEffect(() => {
    const generatedAt = Math.max(overview.data?.generated_at ?? 0, status.data?.generated_at ?? 0, usage.data?.generated_at ?? 0);
    if (generatedAt > 0) setRefreshLabel(`总览更新于 ${formatToolbarTime(generatedAt)}`);
  }, [overview.data?.generated_at, setRefreshLabel, status.data?.generated_at, usage.data?.generated_at]);
  useEffect(() => () => {
    setRefreshing(false);
    setRefreshLabel("");
  }, [setRefreshLabel, setRefreshing]);

  if (overview.isPending || status.isPending) {
    return (
      <section className="page-content overview-legacy-page" aria-label="正在加载总览">
        <div className="overview-legacy-metrics overview-legacy-metrics-loading">
          {Array.from({ length: 6 }, (_, index) => <Skeleton.Node key={index} active />)}
        </div>
        <Skeleton active paragraph={{ rows: 10 }} />
      </section>
    );
  }
  if (overview.isError || status.isError) {
    const loadError = overview.error ?? status.error;
    return (
      <section className="page-content">
        <Result
          status="warning"
          title="总览数据加载失败"
          subTitle={loadError instanceof Error ? loadError.message : "请稍后重试"}
          extra={<Button type="primary" onClick={() => void Promise.all([overview.refetch(), status.refetch()])}>重新加载</Button>}
        />
      </section>
    );
  }

  const summary = overview.data.summary;
  return (
    <section className="page-content overview-legacy-page">
      {onboarding.data ? <OnboardingCard status={onboarding.data} /> : null}
      {summary.incomplete_key_matrices > 0 ? (
        <Alert
          className="page-alert"
          type="warning"
          showIcon
          title={`${summary.incomplete_key_matrices} 个用户的统一 Key 账号矩阵不完整`}
          description="这些用户不能参与跨账号迁移；修复前负载均衡会整批拒绝。"
        />
      ) : null}
      {status.data.warnings.length ? (
        <Alert
          className="page-alert"
          type="warning"
          showIcon
          title="部分运行状态暂不可用"
          description={status.data.warnings.join("；")}
        />
      ) : null}

      <div className="overview-legacy-metrics" aria-label="关键指标">
        <Metric label="CPA 账号" value={summary.accounts} detail={`启用 ${summary.enabled_accounts} · 已授权 ${status.data.authorized_accounts}`} />
        <Metric label="用户状态" value={`${summary.active_users}/${summary.users}`} detail={`已路由 ${summary.routed_users}`} />
        <Metric label="Key 健康" value={summary.active_keys} detail={`矩阵异常 ${summary.incomplete_key_matrices}`} />
        <Metric label="团队覆盖" value={summary.teams} detail={`未分配 ${summary.unassigned_users} 人`} />
        <Metric label="服务状态" value={`${status.data.running_services}/${status.data.total_services}`} detail="Compose 服务" />
        <Metric label="5 分钟请求" value={status.data.requests_5m} detail="网关访问日志" />
      </div>

      <AccountQuotaOverview quota={status.data.account_quota} />

      <section className="overview-legacy-monitor overview-token-monitor-card" aria-labelledby="overview-token-monitor-title">
        <div className="overview-legacy-toolbar overview-token-monitor-toolbar">
          <div className="overview-token-heading-row">
            <div className="overview-legacy-toolbar-title usage-monitor-title">
              <h3 id="overview-token-monitor-title">Token 使用</h3>
              <small>按时间与使用主体查看趋势</small>
            </div>
            <div className="overview-collector-meta" aria-live="polite">
              <span className={`overview-collector-state ${collectorState(usage.data?.collector.status).tone}`}>
                {usage.isPending ? "正在加载" : collectorState(usage.data?.collector.status).label}
              </span>
              <time aria-label="最近采集时间">
                {usage.data?.collector.heartbeat_at
                  ? formatOverviewCollectorTime(usage.data.collector.heartbeat_at, overviewDisplayTimezone)
                  : "—"}
              </time>
            </div>
          </div>
          <div className="overview-legacy-filters usage-monitor-filters" aria-label="Token Dashboard 变量">
            <fieldset className="overview-legacy-window-control usage-time-control">
              <legend>时间范围</legend>
              <div className="overview-token-window-row">
                <div className="overview-legacy-window-segments usage-time-segments" role="group" aria-label="Token 使用时间范围">
                  {standardWindows.map((window) => (
                    <button
                      key={window.value}
                      type="button"
                      aria-pressed={usageWindow === window.value}
                      onClick={() => setUsageWindow(window.value)}
                    >
                      {window.label}
                    </button>
                  ))}
                  <button
                    type="button"
                    aria-pressed={usageWindow === "custom"}
                    title="选择时间范围"
                    onClick={() => setCustomOpen(true)}
                  >
                    时间选择
                  </button>
                </div>
                <div className="overview-token-window-boundaries" aria-label="Token 使用时间边界" aria-live="polite">
                  <span className="overview-token-window-value">
                    <small>起始时间</small>
                    <strong>{usage.data ? formatOverviewUsageBoundary(usage.data.window_start_at, overviewDisplayTimezone) : "—"}</strong>
                  </span>
                  <span className="overview-token-window-value">
                    <small>结束时间</small>
                    <strong>{usage.data
                      ? formatOverviewUsageBoundary(
                          usageWindow === "custom" && customRange ? customRange.endAt : usage.data.generated_at,
                          overviewDisplayTimezone
                        )
                      : "—"}</strong>
                  </span>
                </div>
              </div>
            </fieldset>
            <div className="overview-token-scope-filters">
              <fieldset className="overview-token-mode-control">
                <legend>Token 口径</legend>
                <div className="overview-token-mode-segments" role="group" aria-label="Token 统计口径">
                  <button
                    type="button"
                    className="unweighted"
                    aria-pressed={tokenMode === "unweighted"}
                    onClick={() => setTokenMode("unweighted")}
                  ><i aria-hidden="true" />未加权</button>
                  <button
                    type="button"
                    className="weighted"
                    aria-pressed={tokenMode === "weighted"}
                    onClick={() => setTokenMode("weighted")}
                  ><i aria-hidden="true" />加权</button>
                </div>
              </fieldset>
              <LegacyUsageMultiSelect
                id="overview-usage-account-react"
                label="CPA"
                allLabel="全部 CPA"
                searchPlaceholder="搜索 CPA"
                value={selectedAccounts}
                options={accountOptions.map((account) => ({ value: account, label: account }))}
                loading={catalog.isPending}
                error={catalog.isError}
                onChange={setSelectedAccounts}
              />
              <LegacyUsageMultiSelect
                id="overview-usage-user-react"
                label="用户"
                allLabel="全部用户"
                searchPlaceholder="搜索用户邮箱"
                value={selectedUsers}
                options={userOptions.map((user) => ({ value: user, label: user }))}
                loading={catalog.isPending}
                error={catalog.isError}
                onChange={setSelectedUsers}
              />
              <div className="overview-legacy-refresh-cluster usage-refresh-cluster">
                <span className="overview-refresh-label">自动刷新</span>
                <div className="overview-refresh-actions">
                  <div className="overview-legacy-filter usage-variable-select usage-refresh-control">
                    <span className="sr-only">刷新间隔</span>
                    <LegacyEnhancedSelect
                      label="自动刷新"
                      value={String(refreshSeconds)}
                      options={[
                        { value: "0", label: "关闭" },
                        { value: "10", label: "10 秒" },
                        { value: "30", label: "30 秒" },
                        { value: "60", label: "1 分钟" },
                        { value: "300", label: "5 分钟" }
                      ]}
                      onChange={(nextValue) => setRefreshSeconds(Number(nextValue))}
                    />
                  </div>
                  <button
                    type="button"
                    className="button ghost usage-monitor-refresh overview-legacy-refresh-button"
                    disabled={usage.isFetching || usageRefresh.isPending}
                    aria-label="刷新 Token Dashboard"
                    onClick={() => usageRefresh.mutate()}
                  >
                    <span aria-hidden="true">↻</span><span>刷新</span>
                  </button>
                </div>
              </div>
            </div>
          </div>
        </div>

        {usage.isError || usageRefresh.isError ? (
          <Alert
            type="error"
            showIcon
            title="Token Dashboard 加载失败"
            description={(usageRefresh.error ?? usage.error) instanceof Error
              ? (usageRefresh.error ?? usage.error as Error).message
              : "请稍后重试"}
            action={<Button size="small" onClick={() => usageRefresh.mutate()}>重试</Button>}
          />
        ) : null}
        {usage.data?.unavailable_accounts.length ? (
          <Alert
            type="warning"
            showIcon
            title={`${usage.data.unavailable_accounts.length} 个账号缺少当前额度周期起点`}
            description="本周期趋势仅聚合拥有有效周期起点的账号。"
          />
        ) : null}

        {usage.isPending ? (
          <div className="overview-legacy-loading"><Spin /><span>正在读取所选范围的 Token 桶</span></div>
        ) : usage.data ? (
          <UsageDashboard
            payload={usage.data}
            includeDateLabels={usageWindow === "since_reset" || usageWindow === "custom" || Number(usageWindow) > 86_400}
            accountStatuses={new Map(catalog.data?.accounts.map((account) => [account.id, {
              label: account.operational_status.label,
              tone: account.operational_status.tone
            }]))}
            userStatuses={new Map(catalog.data?.users.map((user) => [user.email, user.status === "active"
              ? { label: "活跃", tone: "success" }
              : { label: "停用", tone: "neutral" }]))}
            tokenMode={tokenMode}
            view={usageView}
            onViewChange={setUsageView}
            canLoadMoreUsers={selectedUsers.length === 0
              && userLimit < Math.min(500, userOptions.length)
              && usage.data.users.length >= usage.data.user_limit}
            onLoadMoreUsers={() => setUserLimit((current) => Math.min(500, userOptions.length, current + 10))}
          />
        ) : null}
      </section>

      <RecentJobs jobs={jobs.data?.jobs ?? []} pending={jobs.isPending} error={jobs.error} />

      <CustomUsageRangeModal
        open={customOpen}
        title="时间选择"
        range={customRange}
        timezone={overviewDisplayTimezone}
        onCancel={() => setCustomOpen(false)}
        onApply={(range) => {
          setCustomRange(range);
          setUsageWindow("custom");
          setCustomOpen(false);
        }}
      />
      <LegacyToastRegion toasts={toasts} />
    </section>
  );
}

function Metric({ label, value, detail }: { label: string; value: string | number; detail: string }) {
  return (
    <article className="overview-legacy-metric">
      <span>{label}</span>
      <strong>{value}</strong>
      <small>{detail}</small>
    </article>
  );
}

function UsageDashboard({
  payload,
  includeDateLabels,
  accountStatuses,
  userStatuses,
  tokenMode,
  view,
  onViewChange,
  canLoadMoreUsers,
  onLoadMoreUsers
}: {
  payload: Awaited<ReturnType<typeof readOverviewUsage>>;
  includeDateLabels: boolean;
  accountStatuses: Map<string, SeriesStatus>;
  userStatuses: Map<string, SeriesStatus>;
  tokenMode: TokenMode;
  view: UsageSeriesView;
  onViewChange: (view: UsageSeriesView) => void;
  canLoadMoreUsers: boolean;
  onLoadMoreUsers: () => void;
}) {
  const aggregate = aggregateTokenSeries(payload.buckets, payload.accounts);
  const interval = formatBucketInterval(payload.bucket_seconds);
  const baseSeries = view === "aggregate"
    ? payload.accounts.length ? [aggregate] : []
    : view === "account" ? payload.accounts : payload.users;
  const selectedSeries = tokenMode === "weighted" ? baseSeries.map(asWeightedSeries) : baseSeries;
  const chartSeries = view === "aggregate" ? selectedSeries : topTokenSeries(selectedSeries, 10);
  const metrics = tokenMode === "weighted" ? asWeightedSeries(aggregate) : aggregate;
  const modeLabel = tokenMode === "weighted" ? "加权" : "未加权";
  const viewLabel = view === "aggregate" ? "全部账号" : view === "account" ? "CPA 账号" : "用户";
  const chartAriaDetails = view === "aggregate"
    ? `当前值 ${formatTokens(metrics.current)}，范围内总量 ${formatTokens(metrics.total)}，平均值 ${formatTokens(metrics.average)}，最大值 ${formatTokens(metrics.maximum)}`
    : chartSeries.map((item) => `${item.name} ${formatTokens(item.total)}`).join("，");
  const activeViewTabID = `overview-token-tab-${view}`;
  const emptyText = view === "user" ? "所选范围内没有用户 Token 数据" : "所选范围内没有账号 Token 数据";
  return (
    <article className="overview-legacy-usage-panel overview-token-workspace">
      <header className="overview-token-workspace-header">
        <div className="overview-token-view-region">
          <div className="overview-token-view-switch" role="tablist" aria-label="Token 使用数据视角">
            <button id="overview-token-tab-aggregate" type="button" role="tab" aria-selected={view === "aggregate"} aria-controls="overview-token-series" onClick={() => onViewChange("aggregate")}>全部账号</button>
            <button id="overview-token-tab-account" type="button" role="tab" aria-selected={view === "account"} aria-controls="overview-token-series" onClick={() => onViewChange("account")}>CPA 账号 Token 统计</button>
            <button id="overview-token-tab-user" type="button" role="tab" aria-selected={view === "user"} aria-controls="overview-token-series" onClick={() => onViewChange("user")}>用户 Token 统计</button>
          </div>
        </div>
      </header>

      <section
        id="overview-token-series"
        className="overview-token-data-scroll"
        role="tabpanel"
        aria-labelledby={activeViewTabID}
        aria-label={`${viewLabel} Token 趋势与明细`}
        tabIndex={0}
      >
        {chartSeries.length ? (
          <UsageChartLoader
            buckets={payload.buckets}
            series={chartSeries}
            summary={view === "aggregate"}
            includeDateLabels={includeDateLabels}
            valueLabel={modeLabel}
            timezone={payload.window_timezone}
            summaryMetrics={view === "aggregate" ? metrics : undefined}
            ariaLabel={`${viewLabel}${modeLabel} Token 使用趋势：${chartAriaDetails}`}
          />
        ) : (
          <div className="overview-legacy-chart-empty"><strong>暂无趋势</strong><span>{emptyText}</span></div>
        )}
        <footer className="overview-legacy-summary-footer overview-token-workspace-footer">
          <span>单位：{modeLabel} Token / {interval}</span>
        </footer>
        {view !== "aggregate" ? <SeriesTable
          subjectLabel={view === "user" ? "用户" : "CPA"}
          series={view === "user" ? payload.users : payload.accounts}
          emptyText={emptyText}
          statuses={view === "user" ? userStatuses : accountStatuses}
          tokenMode={tokenMode}
          canLoadMore={view === "user" && canLoadMoreUsers}
          onLoadMore={view === "user" ? onLoadMoreUsers : undefined}
          resetKey={`${view}:${tokenMode}:${payload.window_start_at}:${payload.selected_accounts.join(",")}:${payload.selected_users.join(",")}`}
        /> : null}
      </section>
    </article>
  );
}

function UsageChartLoader({ buckets, series, summary = false, includeDateLabels, valueLabel, timezone, summaryMetrics, ariaLabel }: {
  buckets: number[];
  series: TokenSeries[];
  summary?: boolean;
  includeDateLabels: boolean;
  valueLabel: string;
  timezone: string;
  summaryMetrics?: TokenSeries;
  ariaLabel: string;
}) {
  return (
    <Suspense fallback={(
      <div className={`overview-legacy-chart overview-legacy-chart-loading${summary ? " summary" : ""}`} role="status">
        <Spin size="small" /><span>正在加载趋势图</span>
      </div>
    )}>
      <EChartsUsageChart
        buckets={buckets}
        series={series}
        summary={summary}
        includeDateLabels={includeDateLabels}
        valueLabel={valueLabel}
        timezone={timezone}
        summaryMetrics={summaryMetrics}
        ariaLabel={ariaLabel}
      />
    </Suspense>
  );
}

function SeriesTable({ subjectLabel, series, emptyText, statuses, tokenMode, canLoadMore, onLoadMore, resetKey }: {
  subjectLabel: "CPA" | "用户";
  series: TokenSeries[];
  emptyText: string;
  statuses: Map<string, SeriesStatus>;
  tokenMode: TokenMode;
  canLoadMore: boolean;
  onLoadMore?: () => void;
  resetKey: string;
}) {
  const [sort, setSort] = useState<SortState>({ key: "total", direction: "desc" });
  const [visibleRows, setVisibleRows] = useState(10);
  useEffect(() => setVisibleRows(10), [resetKey]);
  const sorted = useMemo(() => [...series].sort((left, right) => {
    const leftValue = seriesSortValue(left, sort.key, statuses, tokenMode);
    const rightValue = seriesSortValue(right, sort.key, statuses, tokenMode);
    const comparison = typeof leftValue === "number" && typeof rightValue === "number"
      ? leftValue - rightValue
      : String(leftValue).localeCompare(String(rightValue), "zh-CN");
    const directed = sort.direction === "asc" ? comparison : -comparison;
    return directed || left.name.localeCompare(right.name, "zh-CN");
  }), [series, sort, statuses, tokenMode]);
  const colorByName = useMemo(() => new Map(
    topTokenSeries(tokenMode === "weighted" ? series.map(asWeightedSeries) : series, series.length)
      .map((item, index) => [item.name, chartColors[index % chartColors.length]])
  ), [series, tokenMode]);
  const updateSort = (key: SeriesSortKey) => setSort((current) => ({
    key,
    direction: current.key === key
      ? current.direction === "desc" ? "asc" : "desc"
      : key === "name" || key === "status" ? "asc" : "desc"
  }));
  const loadNextPage = () => {
    const nextVisibleRows = visibleRows + 10;
    setVisibleRows(nextVisibleRows);
    if (nextVisibleRows > sorted.length && canLoadMore) onLoadMore?.();
  };
  return (
    <NativeTableViewport
      className="overview-legacy-table-wrap overview-token-detail-table"
      aria-label={`${subjectLabel}用量明细表格`}
      onScroll={(event) => {
        const viewport = event.currentTarget;
        if (viewport.scrollTop + viewport.clientHeight >= viewport.scrollHeight - 48
          && (visibleRows < sorted.length || canLoadMore)) loadNextPage();
      }}
    >
      <table className="overview-legacy-table">
        <thead>
          <tr>
            <SeriesTableHeader label={subjectLabel} sortKey="name" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label="状态" sortKey="status" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label="当前值" sortKey="current" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label="平均值" sortKey="average" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label="最大值" sortKey="maximum" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label="范围内总量" sortKey="total" sort={sort} onSort={updateSort} />
          </tr>
        </thead>
        <tbody>
          {sorted.length ? sorted.slice(0, visibleRows).map((item) => {
            const status = seriesStatus(item, statuses);
            return (
              <tr key={item.name}>
                <td><span className="overview-series-name"><i style={{ background: colorByName.get(item.name) ?? chartColors[0] }} /><strong>{item.name}</strong></span></td>
                <td><span className={`overview-status-chip ${status.tone}`}>{status.label}</span></td>
                <td><SeriesTokenValue series={item} metric="current" tokenMode={tokenMode} /></td>
                <td><SeriesTokenValue series={item} metric="average" tokenMode={tokenMode} /></td>
                <td><SeriesTokenValue series={item} metric="maximum" tokenMode={tokenMode} /></td>
                <td className="usage-monitor-total"><SeriesTokenValue series={item} metric="total" tokenMode={tokenMode} /></td>
              </tr>
            );
          }) : (
            <tr><td className="overview-legacy-table-empty" colSpan={6}>{emptyText}</td></tr>
          )}
        </tbody>
      </table>
    </NativeTableViewport>
  );
}

function SeriesTableHeader({ label, sortKey, sort, onSort }: {
  label: string;
  sortKey: SeriesSortKey;
  sort: SortState;
  onSort: (key: SeriesSortKey) => void;
}) {
  const active = sort.key === sortKey;
  const ariaSort = active ? (sort.direction === "asc" ? "ascending" : "descending") : "none";
  return (
    <th aria-sort={ariaSort}>
      <SortButton label={label} sortKey={sortKey} sort={sort} onSort={onSort} />
    </th>
  );
}

function SortButton({ label, sortKey, sort, onSort }: {
  label: string;
  sortKey: SeriesSortKey;
  sort: SortState;
  onSort: (key: SeriesSortKey) => void;
}) {
  const active = sort.key === sortKey;
  const ariaLabel = active
    ? `${label}，当前${sort.direction === "asc" ? "升序" : "降序"}，点击切换排序方向`
    : `${label}，点击排序`;
  return (
    <button
      type="button"
      className={`sort-button${active ? " active" : ""}`}
      data-monitor-sort={sortKey}
      data-direction={active ? sort.direction : undefined}
      aria-label={ariaLabel}
      onClick={() => onSort(sortKey)}
    >{label}</button>
  );
}

function TokenValue({ value }: { value: number }) {
  const token = legacyTokenParts(value);
  return (
    <span className="token-usage">
      <span className="token-usage-main" aria-hidden="true">
        <span className="token-usage-value">{token.amount}</span>
        <small className="token-usage-unit">{token.unit}</small>
      </span>
      {token.compacted ? <small className="token-usage-exact" aria-hidden="true">{token.label}</small> : null}
      <span className="token-usage-sr-only">{token.label}</span>
    </span>
  );
}

function SeriesTokenValue({ series, metric, tokenMode }: {
  series: TokenSeries;
  metric: Exclude<SeriesSortKey, "name" | "status">;
  tokenMode: TokenMode;
}) {
  const value = tokenMode === "weighted" ? weightedMetric(series, metric) : series[metric];
  return <TokenValue value={value} />;
}

function legacyTokenParts(value: number) {
  const normalized = Number.isFinite(value) && value >= 0 ? Math.floor(value) : 0;
  let divisor = 1;
  let unit = "Token";
  if (normalized >= 1_000_000_000) [divisor, unit] = [1_000_000_000, "B"];
  else if (normalized >= 1_000_000) [divisor, unit] = [1_000_000, "M"];
  else if (normalized >= 1_000) [divisor, unit] = [1_000, "K"];
  let rounded = Math.round((normalized / divisor) * 10) / 10;
  if (unit === "K" && rounded >= 1_000) {
    [divisor, unit] = [1_000_000, "M"];
    rounded = Math.round((normalized / divisor) * 10) / 10;
  }
  if (unit === "M" && rounded >= 1_000) {
    [divisor, unit] = [1_000_000_000, "B"];
    rounded = Math.round((normalized / divisor) * 10) / 10;
  }
  return {
    amount: new Intl.NumberFormat("en-US", { maximumFractionDigits: 1 }).format(rounded),
    unit,
    label: `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 }).format(normalized)} Token`,
    compacted: divisor > 1
  };
}

function RecentJobs({ jobs, pending, error }: { jobs: RuntimeJob[]; pending: boolean; error: unknown }) {
  return (
    <section className="overview-legacy-jobs" aria-labelledby="overview-recent-jobs-title">
      <header>
        <div><h2 id="overview-recent-jobs-title">最近任务</h2><span>ACTIVITY</span></div>
        <a href="/admin/runtime">查看全部任务 →</a>
      </header>
      <div className="overview-legacy-panel overview-legacy-job-list">
        {pending ? <div className="overview-legacy-job-state"><Spin size="small" /> 正在读取任务</div> : null}
        {error ? <Alert type="error" showIcon message="最近任务加载失败" /> : null}
        {!pending && !error && jobs.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="暂无运行任务" /> : null}
        {jobs.slice(0, 8).map((job) => {
          const status = jobStatus(job.status);
          return (
            <div className="overview-legacy-job-row" key={job.id}>
              <strong>{job.name || actionLabel(job.action)}</strong>
              <span>{job.target}</span>
              <time>{formatTimestamp(job.created_at)}</time>
              <span className={`overview-status-chip ${status.tone}`}>{status.label}</span>
            </div>
          );
        })}
      </div>
    </section>
  );
}

function aggregateTokenSeries(buckets: number[], series: TokenSeries[]): TokenSeries {
  const values = buckets.map((_, index) => series.reduce((sum, item) => sum + (item.values[index] ?? 0), 0));
  const weightedValues = buckets.map((_, index) => series.reduce((sum, item) => (
    sum + (item.weighted_values?.[index] ?? item.values[index] ?? 0)
  ), 0));
  const total = values.reduce((sum, value) => sum + value, 0);
  const weightedTotal = weightedValues.reduce((sum, value) => sum + value, 0);
  return {
    name: "全部账号合计",
    values,
    current: values.at(-1) ?? 0,
    average: values.length ? Math.round(total / values.length) : 0,
    maximum: Math.max(...values, 0),
    total,
    weighted_values: weightedValues,
    weighted_current: weightedValues.at(-1) ?? 0,
    weighted_average: weightedValues.length ? Math.round(weightedTotal / weightedValues.length) : 0,
    weighted_maximum: Math.max(...weightedValues, 0),
    weighted_total: weightedTotal
  };
}

function asWeightedSeries(series: TokenSeries): TokenSeries {
  return {
    ...series,
    values: series.weighted_values ?? series.values,
    current: series.weighted_current ?? series.current,
    average: series.weighted_average ?? series.average,
    maximum: series.weighted_maximum ?? series.maximum,
    total: series.weighted_total ?? series.total
  };
}

function topTokenSeries(series: TokenSeries[], limit: number) {
  return [...series]
    .sort((left, right) => right.total - left.total || left.name.localeCompare(right.name, "zh-CN"))
    .slice(0, limit);
}

function seriesSortValue(
  series: TokenSeries,
  key: SeriesSortKey,
  statuses: Map<string, SeriesStatus>,
  tokenMode: TokenMode
) {
  if (key === "status") return seriesStatusRank(seriesStatus(series, statuses));
  if (key === "name") return series.name;
  return tokenMode === "weighted" ? weightedMetric(series, key) : series[key];
}

function weightedMetric(series: TokenSeries, metric: Exclude<SeriesSortKey, "name" | "status">) {
  if (metric === "current") return series.weighted_current ?? series.current;
  if (metric === "average") return series.weighted_average ?? series.average;
  if (metric === "maximum") return series.weighted_maximum ?? series.maximum;
  return series.weighted_total ?? series.total;
}

function mergeOptions(current: string[], incoming: string[]) {
  const next = Array.from(new Set([...current, ...incoming]));
  return next.length === current.length && next.every((value, index) => value === current[index]) ? current : next;
}

type SeriesStatus = { label: string; tone: string };

function seriesStatus(series: TokenSeries, statuses: Map<string, SeriesStatus>): SeriesStatus {
  return statuses.get(series.name) ?? { label: "状态未知", tone: "neutral" };
}

function seriesStatusRank(status: SeriesStatus) {
  return ({ success: 0, warning: 1, danger: 2, neutral: 3 } as Record<string, number>)[status.tone] ?? 9;
}

function collectorState(status?: string) {
  if (["ok", "healthy"].includes(status ?? "")) return { label: "采集正常", tone: "success" };
  if (["starting", "degraded"].includes(status ?? "")) return { label: status === "starting" ? "采集启动中" : "采集降级", tone: "warning" };
  if (!status) return { label: "等待采集", tone: "neutral" };
  return { label: "采集异常", tone: "danger" };
}

function jobStatus(status: RuntimeJob["status"]) {
  const labels: Record<RuntimeJob["status"], { label: string; tone: string }> = {
    queued: { label: "排队中", tone: "neutral" },
    running: { label: "运行中", tone: "warning" },
    cancelling: { label: "取消中", tone: "warning" },
    succeeded: { label: "成功", tone: "success" },
    failed: { label: "失败", tone: "danger" },
    cancelled: { label: "已取消", tone: "neutral" }
  };
  return labels[status];
}

function actionLabel(action: RuntimeJob["action"]) {
  const labels: Record<RuntimeJob["action"], string> = {
    start: "启动服务",
    stop: "停止服务",
    restart: "重启服务",
    login: "OAuth 授权",
    "image-pull": "拉取镜像",
    "image-update": "更新 CPA 镜像"
  };
  return labels[action];
}

export function formatOverviewUsageRange(startAt: number, endAt: number, timezone = overviewDisplayTimezone) {
  if (!Number.isFinite(startAt) || !Number.isFinite(endAt) || startAt <= 0 || endAt < startAt) return "统计边界暂不可用";
  return `${formatOverviewUsageBoundary(startAt, timezone)} — ${formatOverviewUsageBoundary(endAt, timezone)}`;
}

export function formatOverviewUsageBoundary(timestamp: number, timezone = overviewDisplayTimezone) {
  if (!Number.isFinite(timestamp) || timestamp <= 0) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false,
    ...(timezone ? { timeZone: timezone } : {})
  }).format(new Date(timestamp * 1000));
}

function formatOverviewCollectorTime(timestamp: number, timezone = overviewDisplayTimezone) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false,
    ...(timezone ? { timeZone: timezone } : {})
  }).format(new Date(timestamp * 1000));
}

function formatBucketInterval(seconds: number) {
  if (seconds < 60) return `${seconds} 秒`;
  if (seconds < 3600) return `${Math.round(seconds / 60)} 分钟`;
  if (seconds < 86400) return `${Math.round(seconds / 3600)} 小时`;
  return `${Math.round(seconds / 86400)} 天`;
}

function formatTimestamp(timestamp: number) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit"
  }).format(new Date(timestamp * 1000));
}

function formatToolbarTime(timestamp: number) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false
  }).format(new Date(timestamp * 1000));
}
