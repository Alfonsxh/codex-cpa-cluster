import { Alert, Button, Empty, Result, Skeleton, Spin, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
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
import { useAdminToolbar } from "./AdminToolbarContext";
import {
  CustomUsageRangeModal,
  formatCustomUsageRange,
  type CustomUsageRange
} from "./components/CustomUsageRangeModal";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { LegacyUsageMultiSelect } from "./components/LegacyUsageMultiSelect";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { formatTokens } from "./formatters";

const { Text } = Typography;

type SeriesSortKey = "name" | "status" | "current" | "average" | "maximum" | "total";

type SortState = {
  key: SeriesSortKey;
  direction: "asc" | "desc";
};

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
    startAt: usageWindow === "custom" ? customRange?.startAt : undefined,
    endAt: usageWindow === "custom" ? customRange?.endAt : undefined
  }), [customRange, selectedAccounts, selectedUsers, usageWindow, userLimit]);
  const usageQueryKey = useMemo(() => [
      "overview-usage",
      usageWindow,
      selectedAccounts,
      selectedUsers,
      userLimit,
      customRange?.startAt,
      customRange?.endAt
    ] as const, [customRange?.endAt, customRange?.startAt, selectedAccounts, selectedUsers, usageWindow, userLimit]);
  const usage = useQuery({
    queryKey: usageQueryKey,
    queryFn: ({ signal }) => readOverviewUsage(usageOptions, signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
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
        <Metric label="有效用户" value={summary.active_users} detail="用户邮箱" />
        <Metric label="业务 CPA" value={summary.accounts} detail="可继续扩展" />
        <Metric label="有效 Key" value={summary.active_keys} detail={`跨 ${summary.accounts} 个 CPA`} />
        <Metric label="已授权 CPA" value={`${status.data.authorized_accounts}/${summary.accounts}`} detail="OAuth 文件" />
        <Metric label="运行服务" value={`${status.data.running_services}/${status.data.total_services}`} detail="Compose 服务" />
        <Metric label="5 分钟请求" value={status.data.requests_5m} detail="网关访问日志" />
      </div>

      <section className="overview-legacy-monitor" aria-labelledby="overview-token-monitor-title">
        <div className="overview-legacy-toolbar overview-legacy-panel">
          <div className="overview-legacy-toolbar-title usage-monitor-title">
            <h3 id="overview-token-monitor-title">Token 使用</h3>
            <p className="section-kicker">TOKEN MONITOR</p>
            <small>Dashboard</small>
          </div>
          <div className="overview-legacy-filters usage-monitor-filters" aria-label="Token Dashboard 变量">
            <fieldset className="overview-legacy-window-control usage-time-control">
              <legend>时间范围</legend>
              <div className="overview-legacy-window-segments usage-time-segments" aria-label="Token 使用时间范围">
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
                  title="选择自定义时间范围"
                  onClick={() => setCustomOpen(true)}
                >
                  {usageWindow === "custom" && customRange ? formatCustomUsageRange(customRange) : "自定义"}
                </button>
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
            <label className="overview-legacy-filter usage-variable-select">
              <span>用户范围</span>
              <select
                aria-label="用户范围"
                value={userLimit}
                onChange={(event) => setUserLimit(Number(event.currentTarget.value))}
              >
                {[10, 20, 50].map((value) => <option key={value} value={value}>Top {value}</option>)}
              </select>
            </label>
            <div className="overview-legacy-refresh-cluster usage-refresh-cluster">
              <label className="overview-legacy-filter usage-variable-select usage-refresh-control">
                <span>自动刷新</span>
                <select
                  aria-label="自动刷新"
                  value={refreshSeconds}
                  onChange={(event) => setRefreshSeconds(Number(event.currentTarget.value))}
                >
                  <option value={0}>关闭</option>
                  <option value={10}>10 秒</option>
                  <option value={30}>30 秒</option>
                  <option value={60}>1 分钟</option>
                  <option value={300}>5 分钟</option>
                </select>
              </label>
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
          <div className="overview-legacy-monitor-meta">
            <span className={`overview-status-chip ${collectorState(usage.data?.collector.status).tone}`}>
              {usage.isPending ? "正在加载" : collectorState(usage.data?.collector.status).label}
            </span>
            <time>{usage.data ? `更新于 ${formatTimestamp(usage.data.generated_at)}` : "—"}</time>
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
          <div className="overview-legacy-panel overview-legacy-loading"><Spin /><span>正在读取所选范围的 Token 桶</span></div>
        ) : usage.data ? (
          <UsageDashboard
            payload={usage.data}
            windowLabel={windowDisplayLabel(usageWindow, customRange)}
            includeDateLabels={usageWindow === "since_reset" || usageWindow === "custom" || Number(usageWindow) > 86_400}
            accountScope={selectedAccounts}
            userScope={selectedUsers}
            accountStatuses={new Map(catalog.data?.accounts.map((account) => [account.id, {
              label: account.operational_status.label,
              tone: account.operational_status.tone
            }]))}
            userStatuses={new Map(catalog.data?.users.map((user) => [user.email, user.status === "active"
              ? { label: "活跃", tone: "success" }
              : { label: "停用", tone: "neutral" }]))}
          />
        ) : null}
      </section>

      <RecentJobs jobs={jobs.data?.jobs ?? []} pending={jobs.isPending} error={jobs.error} />

      <CustomUsageRangeModal
        open={customOpen}
        title="Token 趋势自定义统计范围"
        range={customRange}
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
  windowLabel,
  includeDateLabels,
  accountScope,
  userScope,
  accountStatuses,
  userStatuses
}: {
  payload: Awaited<ReturnType<typeof readOverviewUsage>>;
  windowLabel: string;
  includeDateLabels: boolean;
  accountScope: string[];
  userScope: string[];
  accountStatuses: Map<string, SeriesStatus>;
  userStatuses: Map<string, SeriesStatus>;
}) {
  const aggregate = aggregateTokenSeries(payload.buckets, payload.accounts);
  const scope = `${accountScope.length ? `${accountScope.length} 个 CPA` : "全部 CPA"} · ${userScope.length ? `${userScope.length} 位用户` : "全部用户"}`;
  const interval = formatBucketInterval(payload.bucket_seconds);
  return (
    <>
      <article className="overview-legacy-panel overview-legacy-usage-panel">
        <header className="overview-legacy-panel-header overview-legacy-summary-header">
          <div>
            <h4>所有账号 Token 使用量</h4>
            <small>{scope} · {windowLabel} · 聚合间隔 {interval}</small>
          </div>
          <dl className="overview-legacy-summary-metrics" aria-label="账号 Token 使用量汇总值">
            <div><dt>范围内总量</dt><dd>{formatTokens(aggregate.total)}</dd></div>
            <div><dt>平均值</dt><dd>{formatTokens(aggregate.average)}</dd></div>
            <div><dt>最大值</dt><dd>{formatTokens(aggregate.maximum)}</dd></div>
          </dl>
        </header>
        <UsageChartLoader
          buckets={payload.buckets}
          series={[aggregate]}
          summary
          includeDateLabels={includeDateLabels}
          ariaLabel={`所有账号 Token 使用趋势：全部账号合计 ${formatTokens(aggregate.total)}`}
        />
        <footer className="overview-legacy-summary-footer">
          <span><i style={{ background: chartColors[0] }} />全部账号合计</span>
          <span>单位：Token / {interval}</span>
        </footer>
      </article>

      <SeriesPanel
        title="CPA 账号 Token 使用趋势"
        subtitle={`${windowLabel} · 聚合间隔 ${interval}`}
        subjectLabel="CPA"
        buckets={payload.buckets}
        series={payload.accounts}
        includeDateLabels={includeDateLabels}
        emptyText="所选范围内没有账号 Token 数据"
        statuses={accountStatuses}
      />
      <SeriesPanel
        title="用户 Token 使用趋势"
        subtitle={`${windowLabel} · 聚合间隔 ${interval} · Top ${payload.user_limit}`}
        subjectLabel="用户"
        buckets={payload.buckets}
        series={payload.users}
        includeDateLabels={includeDateLabels}
        emptyText="所选范围内没有用户 Token 数据"
        statuses={userStatuses}
      />
    </>
  );
}

function SeriesPanel({
  title,
  subtitle,
  subjectLabel,
  buckets,
  series,
  includeDateLabels,
  emptyText,
  statuses
}: {
  title: string;
  subtitle: string;
  subjectLabel: "CPA" | "用户";
  buckets: number[];
  series: TokenSeries[];
  includeDateLabels: boolean;
  emptyText: string;
  statuses: Map<string, SeriesStatus>;
}) {
  return (
    <article className="overview-legacy-panel overview-legacy-usage-panel">
      <header className="overview-legacy-panel-header">
        <div><h4>{title}</h4><small>{subtitle}</small></div>
        <span>单位：Token / 聚合间隔</span>
      </header>
      {series.length ? (
        <UsageChartLoader
          buckets={buckets}
          series={series}
          includeDateLabels={includeDateLabels}
          ariaLabel={`分项 Token 使用趋势：${series.map((item) => `${item.name} ${formatTokens(item.total)}`).join("，")}`}
        />
      ) : (
        <div className="overview-legacy-chart-empty"><strong>暂无趋势</strong><span>{emptyText}</span></div>
      )}
      <SeriesTable subjectLabel={subjectLabel} series={series} emptyText={emptyText} statuses={statuses} />
    </article>
  );
}

function UsageChartLoader({ buckets, series, summary = false, includeDateLabels, ariaLabel }: {
  buckets: number[];
  series: TokenSeries[];
  summary?: boolean;
  includeDateLabels: boolean;
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
        ariaLabel={ariaLabel}
      />
    </Suspense>
  );
}

function SeriesTable({ subjectLabel, series, emptyText, statuses }: {
  subjectLabel: "CPA" | "用户";
  series: TokenSeries[];
  emptyText: string;
  statuses: Map<string, SeriesStatus>;
}) {
  const [sort, setSort] = useState<SortState>({ key: "total", direction: "desc" });
  const sorted = useMemo(() => [...series].sort((left, right) => {
    const leftValue = sort.key === "status" ? seriesStatusRank(seriesStatus(left, statuses)) : left[sort.key as Exclude<SeriesSortKey, "status">];
    const rightValue = sort.key === "status" ? seriesStatusRank(seriesStatus(right, statuses)) : right[sort.key as Exclude<SeriesSortKey, "status">];
    const comparison = typeof leftValue === "number" && typeof rightValue === "number"
      ? leftValue - rightValue
      : String(leftValue).localeCompare(String(rightValue), "zh-CN");
    const directed = sort.direction === "asc" ? comparison : -comparison;
    return directed || left.name.localeCompare(right.name, "zh-CN");
  }), [series, sort, statuses]);
  const updateSort = (key: SeriesSortKey) => setSort((current) => ({
    key,
    direction: current.key === key
      ? current.direction === "desc" ? "asc" : "desc"
      : key === "name" || key === "status" ? "asc" : "desc"
  }));
  return (
    <NativeTableViewport className="overview-legacy-table-wrap" aria-label={`${subjectLabel}用量明细表格`}>
      <table className="overview-legacy-table">
        <thead>
          <tr>
            <SeriesTableHeader label={subjectLabel} sortKey="name" sort={sort} onSort={updateSort} />
            <SeriesTableHeader label="状态" sortKey="status" sort={sort} onSort={updateSort} />
            <SeriesTableHeader
              label="当前值"
              sortKey="current"
              sort={sort}
              onSort={updateSort}
              help="所选时间范围内，最新聚合间隔的 Token 使用值；该间隔可能尚未结束。"
            />
            <SeriesTableHeader
              label="平均值"
              sortKey="average"
              sort={sort}
              onSort={updateSort}
              help="所选时间范围内各聚合间隔的平均 Token 使用值；没有请求的间隔按 0 计算。"
            />
            <SeriesTableHeader
              label="最大值"
              sortKey="maximum"
              sort={sort}
              onSort={updateSort}
              help="所选时间范围内，单个聚合间隔的最高 Token 使用值。"
            />
            <SeriesTableHeader label="范围内总量" sortKey="total" sort={sort} onSort={updateSort} />
          </tr>
        </thead>
        <tbody>
          {sorted.length ? sorted.map((item) => {
            const status = seriesStatus(item, statuses);
            const colorIndex = Math.max(0, series.findIndex((candidate) => candidate.name === item.name));
            return (
              <tr key={item.name}>
                <td><span className="overview-series-name"><i style={{ background: chartColors[colorIndex % chartColors.length] }} /><strong>{item.name}</strong></span></td>
                <td><span className={`overview-status-chip ${status.tone}`}>{status.label}</span></td>
                <td><TokenValue value={item.current} /></td>
                <td><TokenValue value={item.average} /></td>
                <td><TokenValue value={item.maximum} /></td>
                <td className="usage-monitor-total"><TokenValue value={item.total} /></td>
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

function SeriesTableHeader({ label, sortKey, sort, onSort, help }: {
  label: string;
  sortKey: SeriesSortKey;
  sort: SortState;
  onSort: (key: SeriesSortKey) => void;
  help?: string;
}) {
  const active = sort.key === sortKey;
  const ariaSort = active ? (sort.direction === "asc" ? "ascending" : "descending") : "none";
  const button = <SortButton label={label} sortKey={sortKey} sort={sort} onSort={onSort} />;
  return (
    <th aria-sort={ariaSort}>
      {help ? (
        <span className="usage-monitor-heading">
          {button}
          <button
            className="usage-monitor-help"
            type="button"
            data-tooltip={help}
            aria-label={`${label}说明`}
          >?</button>
        </span>
      ) : button}
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
  const total = values.reduce((sum, value) => sum + value, 0);
  return {
    name: "全部账号合计",
    values,
    current: values.at(-1) ?? 0,
    average: values.length ? Math.round(total / values.length) : 0,
    maximum: Math.max(...values, 0),
    total
  };
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

function windowDisplayLabel(window: OverviewUsageWindow, customRange: CustomUsageRange | null) {
  if (window === "custom") return customRange ? formatCustomUsageRange(customRange) : "自定义";
  return standardWindows.find((item) => item.value === window)?.label ?? window;
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
