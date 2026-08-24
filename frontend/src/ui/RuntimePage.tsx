import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Drawer,
  Modal,
  Result,
  Row,
  Skeleton,
  Space,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import {
  CloseCircleOutlined,
  FileTextOutlined,
  PauseCircleOutlined,
  PlayCircleOutlined,
  ReloadOutlined,
  SyncOutlined
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState } from "react";

import {
  cancelRuntimeJob,
  cpaImageStatusQueryKey,
  isActiveRuntimeJob,
  listRuntimeJobs,
  listRuntimeServices,
  operationImpactQueryKey,
  readCPAImageStatus,
  readOperationImpact,
  readRuntimeLogs,
  runtimeJobsQueryKey,
  runtimeLogsQueryKey,
  runtimeServicesQueryKey,
  submitRuntimeJob,
  type CpaAccountImage,
  type CpaImageStatus,
  type OperationImpact,
  type RuntimeJob,
  type RuntimeJobRequest,
  type RuntimeService
} from "../api/runtime";
import { AdminTable } from "./components/AdminTable";
import { MetricCard } from "./components/MetricCard";
import { PageState } from "./components/PageState";
import { PageToolbar } from "./components/PageToolbar";

const { Text } = Typography;
type RuntimeAction = RuntimeJobRequest["action"];
type PendingOperation = { action: RuntimeAction; target: string; service: RuntimeService };

export function RuntimePage({ csrfToken }: { csrfToken: string }) {
  const queryClient = useQueryClient();
  const [pendingOperation, setPendingOperation] = useState<PendingOperation | null>(null);
  const [logTarget, setLogTarget] = useState<string | null>(null);
  const impactTarget = pendingOperation?.action === "stop" ? pendingOperation.target : "";
  const services = useQuery({
    queryKey: runtimeServicesQueryKey,
    queryFn: ({ signal }) => listRuntimeServices(signal),
    refetchInterval: 5_000
  });
  const jobs = useQuery({
    queryKey: runtimeJobsQueryKey,
    queryFn: ({ signal }) => listRuntimeJobs(signal),
    refetchInterval: (query) => query.state.data?.jobs.some(isActiveRuntimeJob) ? 1_500 : 10_000
  });
  const images = useQuery({
    queryKey: cpaImageStatusQueryKey,
    queryFn: ({ signal }) => readCPAImageStatus(signal),
    refetchInterval: 15_000
  });
  const impact = useQuery({
    queryKey: operationImpactQueryKey("stop", impactTarget),
    queryFn: ({ signal }) => readOperationImpact(impactTarget, signal),
    enabled: impactTarget !== "",
    staleTime: 0,
    gcTime: 0,
    retry: false
  });
  const logs = useQuery({
    queryKey: runtimeLogsQueryKey(logTarget ?? ""),
    queryFn: ({ signal }) => readRuntimeLogs(logTarget ?? "", signal),
    enabled: logTarget !== null,
    gcTime: 0,
    refetchInterval: logTarget ? 5_000 : false
  });
  const submit = useMutation({
    mutationFn: (operation: PendingOperation) => submitRuntimeJob(operation.action, operation.target, csrfToken),
    onSuccess: async () => {
      setPendingOperation(null);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: runtimeServicesQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: cpaImageStatusQueryKey, exact: true })
      ]);
    }
  });
  const cancel = useMutation({
    mutationFn: (jobID: string) => cancelRuntimeJob(jobID, csrfToken),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: runtimeJobsQueryKey, exact: true });
    }
  });

  const columns = useMemo(
    () => runtimeServiceColumns(setPendingOperation, setLogTarget),
    []
  );

  if (services.isPending || jobs.isPending) {
    return (
      <section className="page-content runtime-page" aria-label="正在加载运行维护">
        <Skeleton active paragraph={{ rows: 7 }} />
      </section>
    );
  }
  if (services.isError) {
    return (
      <section className="page-content">
        <PageState
          kind="error"
          title="Docker 运行状态加载失败"
          detail={services.error instanceof Error ? services.error.message : "请稍后重试"}
          onAction={() => void services.refetch()}
        />
      </section>
    );
  }

  return (
    <section className="page-content runtime-page">
      <PageToolbar
        description="本页只请求当前 Compose 项目的服务、任务和已打开的日志。操作通过有界队列串行执行；Edge、Gateway 与 Admin 仅允许查看日志，不能从管理 API 停止。"
        actions={(
          <Button
            icon={<ReloadOutlined aria-hidden="true" />}
            loading={services.isFetching || jobs.isFetching || images.isFetching}
            onClick={() => void Promise.all([services.refetch(), jobs.refetch(), images.refetch()])}
          >
            刷新当前页
          </Button>
        )}
      />

      {jobs.isError ? (
        <Alert
          className="page-alert"
          type="warning"
          showIcon
          message="任务状态暂时不可用"
          description={jobs.error instanceof Error ? jobs.error.message : "服务状态仍可查看"}
        />
      ) : null}
      {submit.isError ? (
        <Alert
          className="page-alert"
          type="error"
          showIcon
          closable
          message="运行任务未提交"
          description={submit.error instanceof Error ? submit.error.message : "请稍后重试"}
          onClose={() => submit.reset()}
        />
      ) : null}

      <Card className="runtime-card runtime-image-card" title="CLIProxyAPI 镜像状态">
        {images.isPending ? <Skeleton active paragraph={{ rows: 5 }} /> : null}
        {images.isError ? (
          <Result
            status="warning"
            title="CPA 镜像状态加载失败"
            subTitle={images.error instanceof Error ? images.error.message : "服务和日志仍可继续查看"}
            extra={<Button icon={<ReloadOutlined aria-hidden="true" />} onClick={() => void images.refetch()}>重试镜像查询</Button>}
          />
        ) : null}
        {images.data ? <CPAImagePanel status={images.data} /> : null}
      </Card>

      <Card className="runtime-card" title="Compose 服务">
        <AdminTable<RuntimeService>
          rowKey="service"
          columns={columns}
          dataSource={services.data.services}
          minWidth={940}
          maxBodyHeight="min(44vh, 440px)"
          emptyText="当前 Compose 项目没有可见容器"
        />
      </Card>

      <Card className="runtime-card runtime-job-card" title="最近任务">
        <AdminTable<RuntimeJob>
          rowKey="id"
          columns={runtimeJobColumns((jobID) => cancel.mutate(jobID), cancel.isPending)}
          dataSource={jobs.data?.jobs ?? []}
          minWidth={820}
          maxBodyHeight="min(40vh, 400px)"
          size="small"
          emptyText="暂无运行维护任务"
        />
      </Card>

      <Modal
        title={pendingOperation ? operationTitle(pendingOperation.action, pendingOperation.service.service) : "确认运行操作"}
        open={pendingOperation !== null}
        confirmLoading={submit.isPending}
        okText="确认提交"
        cancelText="取消"
        okButtonProps={{
          danger: pendingOperation?.action === "stop",
          disabled: pendingOperation?.action === "stop" && (!impact.data || impact.isPending || impact.isError)
        }}
        onCancel={() => !submit.isPending && setPendingOperation(null)}
        onOk={() => pendingOperation && submit.mutate(pendingOperation)}
        destroyOnHidden
      >
        {pendingOperation?.action === "stop" ? (
          <OperationImpactPanel
            operation={pendingOperation}
            impact={impact.data}
            pending={impact.isPending || impact.isFetching}
            error={impact.isError ? impact.error : null}
            onRetry={() => void impact.refetch()}
          />
        ) : (
          <Alert
            type="info"
            showIcon
            message="任务将进入串行有界队列"
            description={pendingOperation ? operationDescription(pendingOperation) : ""}
          />
        )}
      </Modal>

      <Drawer
        title={logTarget ? `${logTarget} · 最近 200 行` : "运行日志"}
        open={logTarget !== null}
        size="min(860px, 92vw)"
        onClose={() => setLogTarget(null)}
        extra={logTarget ? (
          <Button icon={<ReloadOutlined aria-hidden="true" />} onClick={() => void logs.refetch()} loading={logs.isFetching}>
            刷新
          </Button>
        ) : null}
        destroyOnHidden
      >
        {logs.isPending ? <Skeleton active paragraph={{ rows: 12 }} /> : null}
        {logs.isError ? (
          <Result
            status="warning"
            title="日志读取失败"
            subTitle={logs.error instanceof Error ? logs.error.message : "请稍后重试"}
          />
        ) : null}
        {logs.data ? (
          <>
            {logs.data.truncated ? <Alert className="page-alert" type="warning" showIcon message="输出已按 2 MiB 上限截断" /> : null}
            <pre className="runtime-log-output">{logs.data.output || "当前没有日志输出"}</pre>
          </>
        ) : null}
      </Drawer>
    </section>
  );
}

function CPAImagePanel({ status }: { status: CpaImageStatus }) {
  const candidateVersion = metadataString(status.candidate, "version");
  const appliedVersion = metadataString(status.applied, "version");
  return (
    <>
      <Row gutter={[16, 16]} className="runtime-image-summary">
        <Col xs={12} lg={12} xl={6}><MetricCard title="运行账号" value={status.running_count} /></Col>
        <Col xs={12} lg={12} xl={6}><MetricCard title="当前镜像" value={status.current_count} /></Col>
        <Col xs={12} lg={12} xl={6}><MetricCard title="待更新" value={status.outdated_count} tone={status.outdated_count ? "warning" : "default"} /></Col>
        <Col xs={12} lg={12} xl={6}><MetricCard title="本地镜像" value={status.local_image.available ? "已就绪" : "未拉取"} /></Col>
      </Row>
      <Descriptions
        className="runtime-image-details"
        bordered
        size="small"
        column={{ xs: 1, sm: 1, md: 1, lg: 1, xl: 2, xxl: 2 }}
      >
        <Descriptions.Item label="目标镜像"><Text code copyable>{status.target_image}</Text></Descriptions.Item>
        <Descriptions.Item label="更新通道"><Text code>{status.update_channel || "—"}</Text></Descriptions.Item>
        <Descriptions.Item label="候选版本">{candidateVersion || "—"}</Descriptions.Item>
        <Descriptions.Item label="已应用版本">{appliedVersion || "—"}</Descriptions.Item>
        <Descriptions.Item label="本地镜像 ID"><Text code>{status.local_image.short_id || "—"}</Text></Descriptions.Item>
        <Descriptions.Item label="构建提交"><Text code>{status.local_image.commit || "—"}</Text></Descriptions.Item>
      </Descriptions>
      <AdminTable<CpaAccountImage>
        className="runtime-image-table"
        rowKey="account"
        columns={cpaImageColumns}
        dataSource={status.accounts}
        minWidth={900}
        maxBodyHeight="min(40vh, 400px)"
        size="small"
        emptyText="暂无业务账号镜像记录"
      />
    </>
  );
}

function OperationImpactPanel({
  operation,
  impact,
  pending,
  error,
  onRetry
}: {
  operation: PendingOperation;
  impact?: OperationImpact;
  pending: boolean;
  error: unknown;
  onRetry: () => void;
}) {
  if (pending) return <Skeleton active paragraph={{ rows: 3 }} />;
  if (error) {
    return (
      <Alert
        type="error"
        showIcon
        message="无法确认停止影响，操作已锁定"
        description={error instanceof Error ? error.message : "请重试影响查询"}
        action={<Button size="small" onClick={onRetry}>重新查询</Button>}
      />
    );
  }
  const routedUsers = impact?.routed_users;
  return (
    <Space orientation="vertical" size={12} className="runtime-impact-confirmation">
      <Alert
        type="warning"
        showIcon
        message={routedUsers === null || routedUsers === undefined ? "该服务不承载账号路由" : `将影响 ${routedUsers} 个已路由用户`}
        description={operationDescription(operation)}
      />
      <Text type="secondary">影响数据已通过专用只读接口实时确认；提交后任务进入串行有界队列。</Text>
    </Space>
  );
}

function runtimeServiceColumns(
  onOperate: (operation: PendingOperation) => void,
  onLogs: (target: string) => void
): TableColumnsType<RuntimeService> {
  return [
    {
      title: "服务",
      dataIndex: "service",
      fixed: "left",
      width: 190,
      render: (value: string, service) => (
        <Space orientation="vertical" size={1}>
          <Text strong>{value}</Text>
          <Text type="secondary">{service.name || service.container_id}</Text>
        </Space>
      )
    },
    {
      title: "状态",
      dataIndex: "state",
      width: 120,
      render: (value: string) => <Tag color={value === "running" ? "success" : value === "exited" ? "default" : "warning"}>{runtimeStateLabel(value)}</Tag>
    },
    { title: "运行详情", dataIndex: "status", width: 190, render: (value: string) => value || "—" },
    {
      title: "镜像",
      dataIndex: "image",
      ellipsis: true,
      width: 280,
      render: (value: string) => <Text code title={value}>{value}</Text>
    },
    {
      title: "操作",
      fixed: "right",
      width: 260,
      render: (_, service) => {
        const target = serviceTarget(service.service);
        const mutable = isMutableService(service.service);
        return (
          <Space size={4} wrap>
            <Button type="link" icon={<FileTextOutlined aria-hidden="true" />} onClick={() => onLogs(target)}>
              日志
            </Button>
            {mutable && service.state === "running" ? (
              <>
                <Button type="link" icon={<SyncOutlined aria-hidden="true" />} onClick={() => onOperate({ action: "restart", target, service })}>
                  重启
                </Button>
                <Button danger type="link" icon={<PauseCircleOutlined aria-hidden="true" />} onClick={() => onOperate({ action: "stop", target, service })}>
                  停止
                </Button>
              </>
            ) : null}
            {mutable && service.state !== "running" ? (
              <Button type="link" icon={<PlayCircleOutlined aria-hidden="true" />} onClick={() => onOperate({ action: "start", target, service })}>
                启动
              </Button>
            ) : null}
          </Space>
        );
      }
    }
  ];
}

function runtimeJobColumns(onCancel: (jobID: string) => void, cancelling: boolean): TableColumnsType<RuntimeJob> {
  return [
    { title: "任务", dataIndex: "name", width: 150 },
    { title: "目标", dataIndex: "target", width: 160 },
    {
      title: "状态",
      dataIndex: "status",
      width: 120,
      render: (value: RuntimeJob["status"]) => <Tag color={jobStatusColor[value]}>{jobStatusLabel[value]}</Tag>
    },
    { title: "提交时间", dataIndex: "created_at", width: 180, render: formatTimestamp },
    {
      title: "结果",
      render: (_, job) => job.error
        ? <Text type="danger">{job.error}</Text>
        : job.result
          ? <Text type="secondary">处理 {job.result.services.length} 个服务</Text>
          : <Text type="secondary">—</Text>
    },
    {
      title: "操作",
      fixed: "right",
      width: 100,
      render: (_, job) => isActiveRuntimeJob(job) ? (
        <Button
          danger
          type="link"
          icon={<CloseCircleOutlined aria-hidden="true" />}
          disabled={cancelling || job.status === "cancelling"}
          onClick={() => onCancel(job.id)}
        >
          取消
        </Button>
      ) : null
    }
  ];
}

function isMutableService(service: string) {
  return service.startsWith("cliproxy-") || ["web", "management", "usage-collector", "log-maintenance"].includes(service);
}

function serviceTarget(service: string) {
  return service.startsWith("cliproxy-") ? service.slice("cliproxy-".length) : service;
}

function operationTitle(action: RuntimeAction, service: string) {
  return `${actionLabels[action]} ${service}？`;
}

function operationDescription(operation: PendingOperation) {
  if (operation.action === "stop") {
    return `停止 ${operation.service.service} 会中断分配到该服务的请求；API Key 本身不会被修改。`;
  }
  if (operation.action === "restart") {
    return `Docker Engine 将优雅重启 ${operation.service.service}，其他服务不会被操作。`;
  }
  return `Docker Engine 将启动已有的 ${operation.service.service} 容器，不创建或重写账号状态。`;
}

function metadataString(metadata: Record<string, unknown>, key: string) {
  const value = metadata[key];
  return typeof value === "string" ? value : "";
}

function runtimeStateLabel(state: string) {
  return ({ running: "运行中", exited: "已停止", created: "已创建", restarting: "重启中", paused: "已暂停" } as Record<string, string>)[state] ?? state;
}

function formatTimestamp(value?: number) {
  return value ? new Date(value * 1000).toLocaleString("zh-CN", { hour12: false }) : "—";
}

const actionLabels: Record<RuntimeAction, string> = {
  start: "启动",
  up: "启动",
  stop: "停止",
  restart: "重启"
};

const jobStatusLabel: Record<RuntimeJob["status"], string> = {
  queued: "排队中",
  running: "执行中",
  cancelling: "取消中",
  succeeded: "成功",
  failed: "失败",
  cancelled: "已取消"
};

const jobStatusColor: Record<RuntimeJob["status"], string> = {
  queued: "processing",
  running: "processing",
  cancelling: "warning",
  succeeded: "success",
  failed: "error",
  cancelled: "default"
};

const cpaImageColumns: TableColumnsType<CpaAccountImage> = [
  { title: "账号", dataIndex: "account", fixed: "left", width: 150, render: (value: string) => <Text strong>{value}</Text> },
  {
    title: "容器",
    dataIndex: "state",
    width: 120,
    render: (value: string, record) => <Tag color={record.running ? "success" : record.container_exists ? "warning" : "default"}>{runtimeStateLabel(value)}</Tag>
  },
  { title: "版本", dataIndex: "version", width: 130, render: (value: string) => value || "—" },
  { title: "镜像 ID", dataIndex: "image_short_id", width: 150, render: (value: string) => <Text code>{value || "—"}</Text> },
  {
    title: "目标一致",
    dataIndex: "using_target",
    width: 110,
    render: (value: boolean) => <Tag color={value ? "success" : "warning"}>{value ? "一致" : "待更新"}</Tag>
  },
  {
    title: "回滚镜像",
    dataIndex: "rollback_available",
    width: 120,
    render: (value: boolean) => <Tag color={value ? "blue" : "default"}>{value ? "可用" : "不可用"}</Tag>
  },
  { title: "镜像引用", dataIndex: "image_ref", ellipsis: true, width: 280, render: (value: string) => <Text code title={value}>{value || "—"}</Text> }
];
