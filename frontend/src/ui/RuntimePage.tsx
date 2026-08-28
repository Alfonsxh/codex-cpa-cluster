import { Button, Modal, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode
} from "react";

import {
  cancelLegacyRuntimeJob,
  isActiveRuntimeJob,
  listLegacyRuntimeJobs,
  listRuntimeServices,
  readLegacyRuntimeJob,
  readLegacyRuntimeLogs,
  readOperationImpact,
  runtimeJobsQueryKey,
  runtimeLogsQueryKey,
  runtimeServicesQueryKey,
  submitLegacyRuntimeJob,
  type LegacyRuntimeAction,
  type LegacyRuntimeJobView,
  type OperationImpact,
  type RuntimeLogs,
  type RuntimeService
} from "../api/runtime";
import { useAdminToolbar } from "./AdminToolbarContext";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";

const { Paragraph } = Typography;

type PendingOperation = {
  action: "stop" | "restart";
  target: string;
  impact?: OperationImpact;
  impactError?: unknown;
};

const serviceStateRank: Record<string, number> = {
  dead: 0,
  failed: 0,
  exited: 1,
  restarting: 2,
  paused: 3,
  created: 4,
  unknown: 5,
  running: 6
};

export function RuntimePage({ csrfToken }: { csrfToken: string }) {
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const reportedServiceError = useRef<unknown>(null);
  const reportedJobError = useRef<unknown>(null);
  const operationLock = useRef(false);
  const operationPreparationLock = useRef(false);
  const cancelLock = useRef(false);
  const stopImpactRequest = useRef(0);
  const [pendingOperation, setPendingOperation] = useState<PendingOperation | null>(null);
  const [preparingStop, setPreparingStop] = useState("");
  const [taskJob, setTaskJob] = useState<LegacyRuntimeJobView | null>(null);
  const [taskPollError, setTaskPollError] = useState<unknown>(null);
  const [completedTaskJobID, setCompletedTaskJobID] = useState("");
  const [logTarget, setLogTarget] = useState<string | null>(null);

  const services = useQuery({
    queryKey: runtimeServicesQueryKey,
    queryFn: ({ signal }) => listRuntimeServices(signal),
    retry: false,
    refetchOnWindowFocus: false
  });
  const jobs = useQuery({
    queryKey: runtimeJobsQueryKey,
    queryFn: ({ signal }) => listLegacyRuntimeJobs(signal),
    retry: false,
    refetchOnWindowFocus: false
  });
  const logs = useQuery({
    queryKey: runtimeLogsQueryKey(logTarget ?? ""),
    queryFn: ({ signal }) => readLegacyRuntimeLogs(logTarget ?? "", signal),
    enabled: logTarget !== null,
    retry: false,
    gcTime: 0,
    refetchOnWindowFocus: false
  });

  const sortedServices = useMemo(() => [...(services.data?.services ?? [])].sort((left, right) => (
    (serviceStateRank[left.state] ?? 5) - (serviceStateRank[right.state] ?? 5)
      || left.service.localeCompare(right.service, "zh-CN", { numeric: true, sensitivity: "base" })
  )), [services.data?.services]);

  const refreshRuntime = useCallback(async (feedback = true) => {
    setRefreshing(true);
    try {
      const [serviceCatalog, jobCatalog] = await Promise.all([
        listRuntimeServices(),
        listLegacyRuntimeJobs()
      ]);
      queryClient.setQueryData(runtimeServicesQueryKey, serviceCatalog);
      queryClient.setQueryData(runtimeJobsQueryKey, jobCatalog);
      setRefreshLabel(`运行状态更新于 ${formatCompactTimestamp(Date.now() / 1_000)}`);
      if (feedback) showToast("数据已刷新");
    } catch (error) {
      setRefreshLabel("刷新失败");
      if (feedback) showToast(errorMessage(error), "error");
      throw error;
    } finally {
      setRefreshing(false);
    }
  }, [queryClient, setRefreshLabel, setRefreshing, showToast]);

  useEffect(() => {
    setRefreshAction(() => refreshRuntime(true));
    return () => setRefreshAction(null);
  }, [refreshRuntime, setRefreshAction]);
  useEffect(() => {
    setRefreshing(services.isFetching || jobs.isFetching);
  }, [jobs.isFetching, services.isFetching, setRefreshing]);
  useEffect(() => {
    if (!services.data) return;
    reportedServiceError.current = null;
    setRefreshLabel(`运行状态更新于 ${formatCompactTimestamp(Date.now() / 1_000)}`);
  }, [services.data, setRefreshLabel]);
  useEffect(() => {
    if (!services.isError || reportedServiceError.current === services.error) return;
    reportedServiceError.current = services.error;
    setRefreshLabel("刷新失败");
    showToast(errorMessage(services.error, "运行状态加载失败"), "error");
  }, [services.error, services.isError, setRefreshLabel, showToast]);
  useEffect(() => {
    if (!jobs.isError || reportedJobError.current === jobs.error) return;
    reportedJobError.current = jobs.error;
    showToast(errorMessage(jobs.error, "任务列表加载失败"), "error");
  }, [jobs.error, jobs.isError, showToast]);
  useEffect(() => () => {
    stopImpactRequest.current += 1;
    setRefreshing(false);
    setRefreshLabel("");
  }, [setRefreshLabel, setRefreshing]);

  const operation = useMutation({
    gcTime: 0,
    mutationFn: ({ action, target }: { action: LegacyRuntimeAction; target: string }) => (
      submitLegacyRuntimeJob(action, target, csrfToken)
    ),
    onSuccess: async (result) => {
      showToast(result.message);
      setTaskPollError(null);
      setCompletedTaskJobID("");
      setLogTarget(null);
      setTaskJob(result.job);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true })
      ]);
    },
    onError: (error) => showToast(errorMessage(error), "error"),
    onSettled: () => {
      operationLock.current = false;
    }
  });

  const submitOperation = useCallback((action: LegacyRuntimeAction, target: string) => {
    if (operationLock.current) return;
    operationLock.current = true;
    operation.mutate({ action, target });
  }, [operation]);

  const prepareOperation = useCallback(async (action: "stop" | "restart", target: string) => {
    if (operationPreparationLock.current || operationLock.current) return;
    if (action === "restart" || target === "all") {
      setPendingOperation({ action, target });
      return;
    }
    operationPreparationLock.current = true;
    const requestID = ++stopImpactRequest.current;
    setPreparingStop(target);
    try {
      const impact = await readOperationImpact(target);
      if (requestID === stopImpactRequest.current) setPendingOperation({ action, target, impact });
    } catch (error) {
      if (requestID === stopImpactRequest.current) setPendingOperation({ action, target, impactError: error });
    } finally {
      if (requestID === stopImpactRequest.current) {
        setPreparingStop("");
        operationPreparationLock.current = false;
      }
    }
  }, []);

  const taskCancel = useMutation({
    gcTime: 0,
    mutationFn: (jobID: string) => cancelLegacyRuntimeJob(jobID, csrfToken),
    onSuccess: (result) => {
      setTaskJob(result.job);
      showToast(result.message);
      if (!isActiveRuntimeJob(result.job)) cancelLock.current = false;
    },
    onError: (error) => {
      cancelLock.current = false;
      showToast(errorMessage(error), "error");
    }
  });
  const cancelTask = useCallback(() => {
    if (!taskJob || taskJob.status === "cancelling" || cancelLock.current) return;
    cancelLock.current = true;
    taskCancel.mutate(taskJob.id);
  }, [taskCancel, taskJob]);

  useEffect(() => {
    if (!taskJob || !isActiveRuntimeJob(taskJob)) return;
    const controller = new AbortController();
    let timer = 0;
    const poll = () => {
      timer = window.setTimeout(() => {
        void readLegacyRuntimeJob(taskJob.id, controller.signal)
          .then((result) => {
            setTaskPollError(null);
            setTaskJob(result.job);
          })
          .catch((error: unknown) => {
            if (controller.signal.aborted) return;
            setTaskPollError(error);
            poll();
          });
      }, 1_200);
    };
    poll();
    return () => {
      window.clearTimeout(timer);
      controller.abort();
    };
  }, [taskJob]);
  useEffect(() => {
    if (!taskJob || isActiveRuntimeJob(taskJob) || completedTaskJobID === taskJob.id) return;
    cancelLock.current = false;
    setCompletedTaskJobID(taskJob.id);
    showToast(
      taskJob.status === "succeeded" ? "任务执行成功" : "任务执行失败",
      taskJob.status === "succeeded" ? "success" : "error"
    );
    void Promise.all([
      queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
      queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true })
    ]);
  }, [completedTaskJobID, queryClient, showToast, taskJob]);

  const openExistingJob = useCallback(async (jobID: string) => {
    try {
      const result = await readLegacyRuntimeJob(jobID);
      setTaskPollError(null);
      setCompletedTaskJobID("");
      setLogTarget(null);
      setTaskJob(result.job);
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }, [showToast]);
  const refreshJobs = useCallback(async () => {
    try {
      const catalog = await listLegacyRuntimeJobs();
      queryClient.setQueryData(runtimeJobsQueryKey, catalog);
      showToast("任务列表已刷新");
    } catch (error) {
      showToast(errorMessage(error), "error");
    }
  }, [queryClient, showToast]);
  const closeOutput = useCallback(() => {
    cancelLock.current = false;
    setTaskJob(null);
    setTaskPollError(null);
    setCompletedTaskJobID("");
    setLogTarget(null);
    taskCancel.reset();
  }, [taskCancel]);

  return (
    <section className="page-content legacy-runtime-page">
      <div className="bulk-actions">
        <div><h3>批量操作</h3><p className="section-kicker">STACK CONTROL</p></div>
        <div className="button-group">
          <button className="button secondary" type="button" disabled={operation.isPending} onClick={() => submitOperation("up", "all")}>启动全部</button>
          <button className="button secondary" type="button" disabled={operation.isPending} onClick={() => void prepareOperation("restart", "all")}>重启全部</button>
          <button className="button secondary" type="button" onClick={() => { setTaskJob(null); setLogTarget("all"); }}>全部日志</button>
          <button className="button danger-outline" type="button" disabled={operation.isPending} onClick={() => void prepareOperation("stop", "all")}>停止业务服务</button>
        </div>
      </div>

      <div className="panel table-panel runtime-service-panel">
        <div className="panel-title"><div><h3>容器服务</h3><p className="section-kicker">SERVICES</p></div></div>
        <NativeTableViewport className="table-wrap" aria-label="容器服务表格">
          <table className="service-table">
            <thead><tr><th className="table-index-column">序号</th><th>服务</th><th>容器</th><th>状态</th><th>说明</th><th>操作</th></tr></thead>
            <tbody>
              {services.isPending ? <RuntimeServiceSkeleton /> : null}
              {!services.isPending && sortedServices.map((service, index) => (
                <RuntimeServiceRow
                  key={service.service}
                  index={index}
                  service={service}
                  busy={operation.isPending || preparingStop === serviceTarget(service.service)}
                  onOperate={(action, target) => {
                    if (action === "up") submitOperation(action, target);
                    else void prepareOperation(action, target);
                  }}
                  onLogs={(target) => { setTaskJob(null); setLogTarget(target); }}
                />
              ))}
              {!services.isPending && sortedServices.length === 0 ? (
                <tr className="runtime-empty-row"><td colSpan={6}>{services.isError ? "运行状态加载失败，请使用顶部刷新重试" : "当前没有可见容器服务"}</td></tr>
              ) : null}
            </tbody>
          </table>
        </NativeTableViewport>
      </div>

      <div className="section-heading">
        <div><h3>诊断工具</h3><p className="section-kicker">DIAGNOSTICS</p></div>
        <p>对应原有终端检查命令</p>
      </div>
      <div className="diagnostic-grid">
        <DiagnosticCard index="01" title="健康检查" description="检查 Key、OAuth 文件和可用模型" disabled={operation.isPending} onClick={() => submitOperation("health", "all")} />
        <DiagnosticCard index="02" title="路由验证" description="逐个验证有效 Key 的目标 CPA" disabled={operation.isPending} onClick={() => submitOperation("verify-routing", "all")} />
        <DiagnosticCard index="03" title="配置校验" description="重新渲染并校验 Compose 配置" disabled={operation.isPending} onClick={() => submitOperation("render", "all")} />
      </div>

      <div className="section-heading">
        <div><h3>任务记录</h3><p className="section-kicker">JOB HISTORY</p></div>
        <button className="text-button" type="button" disabled={jobs.isFetching} onClick={() => void refreshJobs()}>{jobs.isFetching ? "正在刷新…" : "刷新任务"}</button>
      </div>
      <div className="panel runtime-job-panel">
        {jobs.isPending ? <RuntimeJobSkeleton /> : <RuntimeJobList jobs={jobs.data?.jobs ?? []} onOpen={(jobID) => void openExistingJob(jobID)} />}
      </div>

      <LegacyConfirmModal
        title={pendingOperation?.action === "stop" ? "停止服务？" : "重启服务？"}
        open={pendingOperation !== null}
        okText={pendingOperation?.action === "stop" ? "确认停止" : "确认重启"}
        danger={pendingOperation?.action === "stop"}
        confirmLoading={operation.isPending}
        okDisabled={Boolean(pendingOperation?.impactError)}
        onCancel={() => !operation.isPending && setPendingOperation(null)}
        onOk={() => {
          const current = pendingOperation;
          setPendingOperation(null);
          if (current) submitOperation(current.action, current.target);
        }}
      >
        <Paragraph>{pendingOperation ? operationMessage(pendingOperation) : ""}</Paragraph>
      </LegacyConfirmModal>

      <RuntimeOutputModal
        job={taskJob}
        logTarget={logTarget}
        logs={logs}
        pollError={taskPollError}
        cancelling={taskCancel.isPending}
        onCancelJob={cancelTask}
        onClose={closeOutput}
        onToast={showToast}
      />
      <LegacyToastRegion toasts={toasts} />
    </section>
  );
}

function RuntimeServiceRow({
  index,
  service,
  busy,
  onOperate,
  onLogs
}: {
  index: number;
  service: RuntimeService;
  busy: boolean;
  onOperate: (action: "up" | "stop" | "restart", target: string) => void;
  onLogs: (target: string) => void;
}) {
  const target = serviceTarget(service.service);
  const logOnly = service.service === "admin" || service.service === "edge" || service.service.startsWith("gateway-");
  return (
    <tr>
      <td className="table-index-cell">{index + 1}</td>
      <td><span className="table-primary">{service.service}</span></td>
      <td><span className="table-secondary">{service.name}</span></td>
      <td><span className={`status-chip ${statusTone(service.state)}`}>{statusLabel(service.state)}</span></td>
      <td>{serviceDescription(service.service)}</td>
      <td>
        <div className="table-actions">
          {!logOnly ? (
            <button className="button ghost" type="button" disabled={busy} onClick={() => onOperate(service.state === "running" ? "restart" : "up", target)}>
              {service.state === "running" ? "重启" : "启动"}
            </button>
          ) : null}
          <button className="button ghost" type="button" onClick={() => onLogs(target)}>日志</button>
          {!logOnly ? <button className="button danger-outline" type="button" disabled={busy} onClick={() => onOperate("stop", target)}>停止</button> : null}
        </div>
      </td>
    </tr>
  );
}

function RuntimeServiceSkeleton() {
  return Array.from({ length: 5 }, (_, index) => (
    <tr className="runtime-skeleton-row" key={index} aria-hidden="true">
      <td>{index + 1}</td><td><i /></td><td><i /></td><td><i /></td><td><i /></td><td><i /></td>
    </tr>
  ));
}

function DiagnosticCard({ index, title, description, disabled, onClick }: {
  index: string;
  title: string;
  description: string;
  disabled: boolean;
  onClick: () => void;
}) {
  return (
    <button className="diagnostic-card" type="button" disabled={disabled} onClick={onClick}>
      <span>{index}</span><strong>{title}</strong><small>{description}</small>
    </button>
  );
}

function RuntimeJobList({ jobs, onOpen }: { jobs: LegacyRuntimeJobView[]; onOpen: (jobID: string) => void }) {
  if (jobs.length === 0) {
    return <div className="empty-state"><div className="empty-icon">⌘</div><h3>暂无任务</h3><p>启动、授权和诊断任务会显示在这里。</p></div>;
  }
  return (
    <div className="job-list">
      {jobs.map((job) => (
        <div className="job-row" key={job.id}>
          <div><div className="job-name">{job.name}</div><div className="job-target">{job.id}</div></div>
          <div className="job-target">{job.target}</div>
          <div className="job-time">{formatCompactTimestamp(job.created_at)}</div>
          <button className="button ghost" type="button" onClick={() => onOpen(job.id)}>
            <span className={`status-chip ${statusTone(job.status)}`}>{statusLabel(job.status)}</span>
          </button>
        </div>
      ))}
    </div>
  );
}

function RuntimeJobSkeleton() {
  return <div className="job-list runtime-job-skeleton" aria-label="正在加载任务记录">{Array.from({ length: 3 }, (_, index) => <div className="job-row" key={index}><i /><i /><i /><i /></div>)}</div>;
}

function LegacyConfirmModal({
  title,
  open,
  children,
  okText,
  danger = false,
  confirmLoading = false,
  okDisabled = false,
  onCancel,
  onOk
}: {
  title: string;
  open: boolean;
  children: ReactNode;
  okText: string;
  danger?: boolean;
  confirmLoading?: boolean;
  okDisabled?: boolean;
  onCancel: () => void;
  onOk: () => void;
}) {
  return (
    <Modal
      className="legacy-confirm-modal"
      title={null}
      open={open}
      width={430}
      centered
      closable={false}
      transitionName=""
      maskTransitionName=""
      onCancel={onCancel}
      destroyOnHidden
      footer={[
        <Button key="cancel" disabled={confirmLoading} onClick={onCancel}>取消</Button>,
        <Button key="confirm" type={danger ? "default" : "primary"} danger={danger} loading={confirmLoading} disabled={okDisabled} onClick={onOk}>{okText}</Button>
      ]}
    >
      <div className="legacy-confirm-body">
        <div className="legacy-confirm-icon" aria-hidden="true">!</div>
        <h3>{title}</h3>
        <div className="legacy-confirm-message">{children}</div>
      </div>
    </Modal>
  );
}

function RuntimeOutputModal({
  job,
  logTarget,
  logs,
  pollError,
  cancelling,
  onCancelJob,
  onClose,
  onToast
}: {
  job: LegacyRuntimeJobView | null;
  logTarget: string | null;
  logs: { isPending: boolean; isError: boolean; error: unknown; data?: RuntimeLogs };
  pollError: unknown;
  cancelling: boolean;
  onCancelJob: () => void;
  onClose: () => void;
  onToast: (message: string, kind?: "success" | "error") => void;
}) {
  if (!job && !logTarget) return null;
  const isJob = Boolean(job);
  const output = job
    ? job.output || "任务正在排队…"
    : logs.isPending
      ? "正在读取…"
      : logs.isError
        ? errorMessage(logs.error, "日志读取失败")
        : logs.data?.output || "暂无日志";
  const active = job ? isActiveRuntimeJob(job) : false;
  const copy = async () => {
    const copied = await copyText(output);
    onToast(copied ? "已复制到剪贴板" : "浏览器拒绝复制，请手动选择文本", copied ? "success" : "error");
  };
  return (
    <Modal
      className="legacy-output-modal runtime-output-modal"
      title={<LegacyDialogTitle title={job?.name || `${logTarget} 日志`} kicker={isJob ? "TASK OUTPUT" : "SERVICE LOGS"} />}
      open
      width={900}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      onCancel={onClose}
      destroyOnHidden
      footer={[
        <Button className="legacy-output-secondary" key="copy" onClick={() => void copy()}>复制完整输出</Button>,
        active ? <Button key="cancel-job" danger loading={cancelling} onClick={onCancelJob}>取消任务</Button> : null,
        <Button className="legacy-output-ghost" key="close" onClick={onClose}>关闭</Button>
      ]}
    >
      <div className="job-meta">
        {job ? <><span>{job.target}</span><span>{statusLabel(job.status)}</span><span>{formatCompactTimestamp(job.started_at || job.created_at)}</span></> : <><span>最近 200 行</span><span>{logTarget}</span></>}
      </div>
      {pollError ? <div className="runtime-output-notice error">{errorMessage(pollError, "任务状态刷新失败，正在重试")}</div> : null}
      {logs.data?.truncated && !isJob ? <div className="runtime-output-notice">输出已按 2 MiB 上限截断</div> : null}
      <pre className={isJob ? "oauth-task-output" : "runtime-log-output"}>{output}</pre>
    </Modal>
  );
}

function LegacyDialogTitle({ title, kicker }: { title: string; kicker: string }) {
  return <span className="legacy-dialog-title"><strong>{title}</strong><span className="section-kicker">{kicker}</span></span>;
}

function operationMessage(operation: PendingOperation) {
  if (operation.action === "restart") {
    return operation.target === "all" ? "将依次重启所有业务服务，短时间内可能无法调用。" : `将重启 ${operation.target}。`;
  }
  if (operation.target === "all") {
    return "将停止全部业务 CPA 和插件资源服务；网关与本管理界面会保留，方便恢复。";
  }
  if (operation.impactError) return "无法确认停止影响，操作已锁定；请取消后重试。";
  if (operation.impact?.target_type !== "account") return `将停止 ${operation.target}。`;
  const routedUsers = operation.impact.routed_users;
  if (!Number.isInteger(routedUsers) || Number(routedUsers) < 0) return `将停止 ${operation.target}；影响范围暂不可确认。`;
  return routedUsers
    ? `将停止 ${operation.target}，当前有 ${routedUsers} 个用户路由到该账号。`
    : `将停止 ${operation.target}，当前没有用户路由到该账号。`;
}

function statusTone(status: string) {
  if (["active", "configured", "running", "succeeded"].includes(status)) return "success";
  if (["pending", "queued", "cancelling", "running-job", "restarting"].includes(status)) return "warning";
  if (["failed", "exited", "dead"].includes(status)) return "danger";
  return "neutral";
}

function statusLabel(status: string) {
  return ({
    active: "启用",
    inactive: "已停用",
    configured: "已授权",
    pending: "待授权",
    running: "运行中",
    exited: "已停止",
    missing: "未创建",
    succeeded: "成功",
    failed: "失败",
    queued: "排队中",
    cancelling: "取消中",
    cancelled: "已取消"
  } as Record<string, string>)[status] || status || "未知";
}

function serviceDescription(service: string) {
  if (service === "edge") return "稳定 API 入口与无中断路由切换";
  if (service === "web") return "Portal、使用中心与管理页面静态资源";
  if (service === "gateway-blue" || service === "gateway-green") return "API Key 鉴权、额度与 CPA 路由数据面";
  if (service === "management") return "插件与原生界面资源服务";
  if (service === "usage-collector") return "用户请求与 Token 用量采集";
  if (service === "log-maintenance") return "宿主机日志容量与备份控制";
  if (service === "admin") return "当前综合管理界面";
  return "独立 Codex 账号代理";
}

function serviceTarget(service: string) {
  return service.startsWith("cliproxy-") ? service.slice("cliproxy-".length) : service;
}

function formatCompactTimestamp(timestamp?: number | null) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false
  }).format(new Date(timestamp * 1_000));
}

function errorMessage(error: unknown, fallback = "操作失败，请稍后重试") {
  return error instanceof Error && error.message ? error.message : fallback;
}

async function copyText(value: string) {
  if (!value) return false;
  if (window.isSecureContext && navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(value);
      return true;
    } catch {
      // Use the same legacy selection fallback below.
    }
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.setAttribute("readonly", "");
  textarea.style.position = "fixed";
  textarea.style.opacity = "0";
  document.body.append(textarea);
  textarea.select();
  let copied = false;
  try {
    copied = document.execCommand("copy");
  } catch {
    copied = false;
  }
  textarea.remove();
  return copied;
}
