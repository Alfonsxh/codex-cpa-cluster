import { Alert, Button, Empty, Modal, Result, Select, Skeleton, Spin, Typography } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import {
  lazy,
  Suspense,
  useEffect,
  useMemo,
  useState
} from "react";

import {
  overviewSummaryQueryKey,
  readOverviewSummary,
  readOverviewUsage,
  type OverviewUsageOptions,
  type OverviewUsageWindow,
  type TokenSeries
} from "../api/overview";
import { listRuntimeJobs, runtimeJobsQueryKey, type RuntimeJob } from "../api/runtime";
import { useAdminToolbar } from "./AdminToolbarContext";
import { formatTokens } from "./formatters";

const { Text } = Typography;

type CustomRange = {
  startAt: number;
  endAt: number;
  label: string;
};

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
  const [usageWindow, setUsageWindow] = useState<OverviewUsageWindow>("today");
  const [selectedAccounts, setSelectedAccounts] = useState<string[]>([]);
  const [selectedUsers, setSelectedUsers] = useState<string[]>([]);
  const [userLimit, setUserLimit] = useState(10);
  const [refreshSeconds, setRefreshSeconds] = useState(30);
  const [accountOptions, setAccountOptions] = useState<string[]>([]);
  const [userOptions, setUserOptions] = useState<string[]>([]);
  const [customRange, setCustomRange] = useState<CustomRange | null>(null);
  const [customOpen, setCustomOpen] = useState(false);
  const [customStart, setCustomStart] = useState(() => toDatetimeLocal(Math.floor(Date.now() / 1000) - 86400));
  const [customEnd, setCustomEnd] = useState(() => toDatetimeLocal(Math.floor(Date.now() / 1000)));
  const [customError, setCustomError] = useState("");
  const { refreshRevision, setRefreshing } = useAdminToolbar();

  const overview = useQuery({
    queryKey: overviewSummaryQueryKey,
    queryFn: ({ signal }) => readOverviewSummary(signal),
    staleTime: 0,
    gcTime: 0,
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
  const usage = useQuery({
    queryKey: [
      "overview-usage",
      usageWindow,
      selectedAccounts,
      selectedUsers,
      userLimit,
      customRange?.startAt,
      customRange?.endAt
    ],
    queryFn: ({ signal }) => readOverviewUsage(usageOptions, signal),
    staleTime: 0,
    gcTime: 0,
    retry: false,
    refetchInterval: refreshSeconds > 0 ? refreshSeconds * 1000 : false,
    refetchIntervalInBackground: false,
    refetchOnWindowFocus: false
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
    if (!usage.data) return;
    setAccountOptions((current) => mergeOptions(current, usage.data.accounts.map((series) => series.name)));
    setUserOptions((current) => mergeOptions(current, usage.data.users.map((series) => series.name)));
  }, [usage.data]);
  useEffect(() => {
    if (refreshRevision < 1) return;
    void overview.refetch();
    void usage.refetch();
    void jobs.refetch();
  }, [refreshRevision]);
  const refreshing = overview.isFetching || usage.isFetching || jobs.isFetching;
  useEffect(() => setRefreshing(refreshing), [refreshing, setRefreshing]);
  useEffect(() => () => setRefreshing(false), [setRefreshing]);

  if (overview.isPending) {
    return (
      <section className="page-content overview-legacy-page" aria-label="正在加载总览">
        <div className="overview-legacy-metrics overview-legacy-metrics-loading">
          {Array.from({ length: 6 }, (_, index) => <Skeleton.Node key={index} active />)}
        </div>
        <Skeleton active paragraph={{ rows: 10 }} />
      </section>
    );
  }
  if (overview.isError) {
    return (
      <section className="page-content">
        <Result
          status="warning"
          title="总览数据加载失败"
          subTitle={overview.error instanceof Error ? overview.error.message : "请稍后重试"}
          extra={<Button type="primary" onClick={() => void overview.refetch()}>重新加载</Button>}
        />
      </section>
    );
  }

  const summary = overview.data.summary;
  const applyCustomRange = () => {
    const startAt = Math.floor(new Date(customStart).getTime() / 1000);
    const endAt = Math.floor(new Date(customEnd).getTime() / 1000);
    if (!Number.isFinite(startAt) || !Number.isFinite(endAt) || startAt < 0 || endAt <= startAt) {
      setCustomError("结束时间必须晚于开始时间");
      return;
    }
    const maximumEnd = Math.floor(Date.now() / 1000) + 60;
    if (endAt > maximumEnd) {
      setCustomError("结束时间不能晚于当前时间");
      return;
    }
    setCustomRange({ startAt, endAt, label: formatCustomRange(startAt, endAt) });
    setUsageWindow("custom");
    setCustomError("");
    setCustomOpen(false);
  };

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

      <div className="overview-legacy-metrics" aria-label="关键指标">
        <Metric label="有效用户" value={summary.active_users} detail={`共 ${summary.users} 位用户`} />
        <Metric label="业务 CPA" value={summary.accounts} detail={`启用 ${summary.enabled_accounts}`} />
        <Metric label="有效 Key" value={summary.active_keys} detail="统一 API Key" />
        <Metric label="启用 CPA" value={`${summary.enabled_accounts}/${summary.accounts}`} detail="控制面状态" />
        <Metric label="已路由用户" value={summary.routed_users} detail="已绑定业务账号" />
        <Metric label="团队" value={summary.teams} detail={`${summary.unassigned_users} 位用户未分配`} />
      </div>

      <section className="overview-legacy-monitor" aria-labelledby="overview-token-monitor-title">
        <div className="overview-legacy-toolbar overview-legacy-panel">
          <div className="overview-legacy-toolbar-title">
            <h2 id="overview-token-monitor-title">Token 使用</h2>
            <span>TOKEN MONITOR</span>
            <small>Dashboard</small>
          </div>
          <div className="overview-legacy-filters" aria-label="Token Dashboard 变量">
            <fieldset className="overview-legacy-window-control">
              <legend>时间范围</legend>
              <div className="overview-legacy-window-segments" aria-label="Token 使用时间范围">
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
                  {usageWindow === "custom" && customRange ? customRange.label : "自定义"}
                </button>
              </div>
            </fieldset>
            <label className="overview-legacy-filter">
              <span>CPA</span>
              <Select
                mode="multiple"
                allowClear
                maxTagCount="responsive"
                popupMatchSelectWidth={360}
                classNames={{ popup: { root: "overview-identity-select-popup" } }}
                aria-label="CPA"
                placeholder="全部 CPA"
                value={selectedAccounts}
                options={accountOptions.map((account) => ({ value: account, label: account }))}
                onChange={setSelectedAccounts}
              />
            </label>
            <label className="overview-legacy-filter">
              <span>用户</span>
              <Select
                mode="multiple"
                allowClear
                maxTagCount="responsive"
                popupMatchSelectWidth={420}
                classNames={{ popup: { root: "overview-identity-select-popup" } }}
                aria-label="用户"
                placeholder="全部用户"
                value={selectedUsers}
                options={userOptions.map((user) => ({ value: user, label: user }))}
                onChange={setSelectedUsers}
              />
            </label>
            <label className="overview-legacy-filter">
              <span>用户范围</span>
              <Select<number>
                aria-label="用户范围"
                value={userLimit}
                options={[10, 20, 50].map((value) => ({ value, label: `Top ${value}` }))}
                onChange={setUserLimit}
              />
            </label>
            <div className="overview-legacy-refresh-cluster">
              <label className="overview-legacy-filter">
                <span>自动刷新</span>
                <Select<number>
                  aria-label="自动刷新"
                  value={refreshSeconds}
                  options={[
                    { value: 0, label: "关闭" },
                    { value: 10, label: "10 秒" },
                    { value: 30, label: "30 秒" },
                    { value: 60, label: "1 分钟" },
                    { value: 300, label: "5 分钟" }
                  ]}
                  onChange={setRefreshSeconds}
                />
              </label>
              <Button
                className="overview-legacy-refresh-button"
                icon={<ReloadOutlined aria-hidden="true" />}
                loading={usage.isFetching}
                aria-label="刷新 Token Dashboard"
                onClick={() => void usage.refetch()}
              >
                刷新
              </Button>
            </div>
          </div>
          <div className="overview-legacy-monitor-meta">
            <span className={`overview-status-chip ${collectorState(usage.data?.collector.status).tone}`}>
              {usage.isPending ? "正在加载" : collectorState(usage.data?.collector.status).label}
            </span>
            <time>{usage.data ? `更新于 ${formatTimestamp(usage.data.generated_at)}` : "—"}</time>
          </div>
        </div>

        {usage.isError ? (
          <Alert
            type="error"
            showIcon
            title="Token Dashboard 加载失败"
            description={usage.error instanceof Error ? usage.error.message : "请稍后重试"}
            action={<Button size="small" onClick={() => void usage.refetch()}>重试</Button>}
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
            accountScope={selectedAccounts}
            userScope={selectedUsers}
          />
        ) : null}
      </section>

      <RecentJobs jobs={jobs.data?.jobs ?? []} pending={jobs.isPending} error={jobs.error} />

      <Modal
        title="自定义 Token 统计范围"
        open={customOpen}
        okText="应用范围"
        cancelText="取消"
        onCancel={() => { setCustomOpen(false); setCustomError(""); }}
        onOk={applyCustomRange}
      >
        <div className="overview-custom-range">
          <label><span>开始时间</span><input type="datetime-local" value={customStart} onChange={(event) => setCustomStart(event.target.value)} /></label>
          <label><span>结束时间</span><input type="datetime-local" value={customEnd} onChange={(event) => setCustomEnd(event.target.value)} /></label>
          {customError ? <Alert type="error" showIcon message={customError} /> : null}
        </div>
      </Modal>
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
  accountScope,
  userScope
}: {
  payload: Awaited<ReturnType<typeof readOverviewUsage>>;
  windowLabel: string;
  accountScope: string[];
  userScope: string[];
}) {
  const aggregate = aggregateTokenSeries(payload.buckets, payload.accounts);
  const scope = `${accountScope.length ? `${accountScope.length} 个 CPA` : "全部 CPA"} · ${userScope.length ? `${userScope.length} 位用户` : "全部用户"}`;
  const interval = formatBucketInterval(payload.bucket_seconds);
  return (
    <>
      <article className="overview-legacy-panel overview-legacy-usage-panel">
        <header className="overview-legacy-panel-header overview-legacy-summary-header">
          <div>
            <h3>所有账号 Token 使用量</h3>
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
        emptyText="所选范围内没有账号 Token 数据"
      />
      <SeriesPanel
        title="用户 Token 使用趋势"
        subtitle={`${windowLabel} · 聚合间隔 ${interval} · Top ${payload.user_limit}`}
        subjectLabel="用户"
        buckets={payload.buckets}
        series={payload.users}
        emptyText="所选范围内没有用户 Token 数据"
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
  emptyText
}: {
  title: string;
  subtitle: string;
  subjectLabel: "CPA" | "用户";
  buckets: number[];
  series: TokenSeries[];
  emptyText: string;
}) {
  return (
    <article className="overview-legacy-panel overview-legacy-usage-panel">
      <header className="overview-legacy-panel-header">
        <div><h3>{title}</h3><small>{subtitle}</small></div>
        <span>单位：Token / 聚合间隔</span>
      </header>
      {series.length ? (
        <UsageChartLoader
          buckets={buckets}
          series={series}
          ariaLabel={`分项 Token 使用趋势：${series.map((item) => `${item.name} ${formatTokens(item.total)}`).join("，")}`}
        />
      ) : (
        <div className="overview-legacy-chart-empty"><strong>暂无趋势</strong><span>{emptyText}</span></div>
      )}
      <SeriesTable subjectLabel={subjectLabel} series={series} emptyText={emptyText} />
    </article>
  );
}

function UsageChartLoader({ buckets, series, summary = false, ariaLabel }: {
  buckets: number[];
  series: TokenSeries[];
  summary?: boolean;
  ariaLabel: string;
}) {
  return (
    <Suspense fallback={(
      <div className={`overview-legacy-chart overview-legacy-chart-loading${summary ? " summary" : ""}`} role="status">
        <Spin size="small" /><span>正在加载趋势图</span>
      </div>
    )}>
      <EChartsUsageChart buckets={buckets} series={series} summary={summary} ariaLabel={ariaLabel} />
    </Suspense>
  );
}

function SeriesTable({ subjectLabel, series, emptyText }: {
  subjectLabel: "CPA" | "用户";
  series: TokenSeries[];
  emptyText: string;
}) {
  const [sort, setSort] = useState<SortState>({ key: "total", direction: "desc" });
  const sorted = useMemo(() => [...series].sort((left, right) => {
    const leftValue = sort.key === "status" ? seriesStatus(left).label : left[sort.key as Exclude<SeriesSortKey, "status">];
    const rightValue = sort.key === "status" ? seriesStatus(right).label : right[sort.key as Exclude<SeriesSortKey, "status">];
    const comparison = typeof leftValue === "number" && typeof rightValue === "number"
      ? leftValue - rightValue
      : String(leftValue).localeCompare(String(rightValue), "zh-CN");
    return sort.direction === "asc" ? comparison : -comparison;
  }), [series, sort]);
  const updateSort = (key: SeriesSortKey) => setSort((current) => ({
    key,
    direction: current.key === key && current.direction === "desc" ? "asc" : "desc"
  }));
  return (
    <div className="overview-legacy-table-wrap">
      <table className="overview-legacy-table">
        <thead>
          <tr>
            <th><SortButton label={subjectLabel} sortKey="name" sort={sort} onSort={updateSort} /></th>
            <th><SortButton label="状态" sortKey="status" sort={sort} onSort={updateSort} /></th>
            <th><SortButton label="当前值" sortKey="current" sort={sort} onSort={updateSort} /></th>
            <th><SortButton label="平均值" sortKey="average" sort={sort} onSort={updateSort} /></th>
            <th><SortButton label="最大值" sortKey="maximum" sort={sort} onSort={updateSort} /></th>
            <th><SortButton label="范围内总量" sortKey="total" sort={sort} onSort={updateSort} /></th>
          </tr>
        </thead>
        <tbody>
          {sorted.length ? sorted.map((item) => {
            const status = seriesStatus(item);
            const colorIndex = Math.max(0, series.findIndex((candidate) => candidate.name === item.name));
            return (
              <tr key={item.name}>
                <td><span className="overview-series-name"><i style={{ background: chartColors[colorIndex % chartColors.length] }} /><strong>{item.name}</strong></span></td>
                <td><span className={`overview-status-chip ${status.tone}`}>{status.label}</span></td>
                <td><TokenValue value={item.current} /></td>
                <td><TokenValue value={item.average} /></td>
                <td><TokenValue value={item.maximum} /></td>
                <td><TokenValue value={item.total} emphasized /></td>
              </tr>
            );
          }) : (
            <tr><td className="overview-legacy-table-empty" colSpan={6}>{emptyText}</td></tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

function SortButton({ label, sortKey, sort, onSort }: {
  label: string;
  sortKey: SeriesSortKey;
  sort: SortState;
  onSort: (key: SeriesSortKey) => void;
}) {
  const active = sort.key === sortKey;
  const arrow = active ? (sort.direction === "desc" ? "↓" : "↑") : "↕";
  return <button type="button" className={active ? "active" : ""} onClick={() => onSort(sortKey)}>{label} {arrow}</button>;
}

function TokenValue({ value, emphasized = false }: { value: number; emphasized?: boolean }) {
  return (
    <span className={`overview-token-value${emphasized ? " emphasized" : ""}`}>
      <strong>{formatTokens(value)}</strong>
      <small>{new Intl.NumberFormat("zh-CN").format(value)} Token</small>
    </span>
  );
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

function seriesStatus(series: TokenSeries) {
  if (series.current > 0) return { label: "有流量", tone: "active" };
  if (series.total > 0) return { label: "近期活跃", tone: "warning" };
  return { label: "无流量", tone: "neutral" };
}

function collectorState(status?: string) {
  if (["ok", "healthy"].includes(status ?? "")) return { label: "采集正常", tone: "active" };
  if (["starting", "degraded"].includes(status ?? "")) return { label: status === "starting" ? "采集启动中" : "采集降级", tone: "warning" };
  if (!status) return { label: "等待采集", tone: "neutral" };
  return { label: "采集异常", tone: "danger" };
}

function jobStatus(status: RuntimeJob["status"]) {
  const labels: Record<RuntimeJob["status"], { label: string; tone: string }> = {
    queued: { label: "排队中", tone: "neutral" },
    running: { label: "运行中", tone: "warning" },
    cancelling: { label: "取消中", tone: "warning" },
    succeeded: { label: "成功", tone: "active" },
    failed: { label: "失败", tone: "danger" },
    cancelled: { label: "已取消", tone: "neutral" }
  };
  return labels[status];
}

function actionLabel(action: RuntimeJob["action"]) {
  return { start: "启动服务", stop: "停止服务", restart: "重启服务" }[action];
}

function windowDisplayLabel(window: OverviewUsageWindow, customRange: CustomRange | null) {
  if (window === "custom") return customRange?.label ?? "自定义";
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

function toDatetimeLocal(timestamp: number) {
  const date = new Date(timestamp * 1000);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000);
  return local.toISOString().slice(0, 16);
}

function formatCustomRange(startAt: number, endAt: number) {
  const formatter = new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit" });
  return `${formatter.format(new Date(startAt * 1000))}–${formatter.format(new Date(endAt * 1000))}`;
}
