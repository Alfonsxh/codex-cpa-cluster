import {
  Alert,
  Button,
  Card,
  Col,
  Drawer,
  Empty,
  Result,
  Row,
  Space,
  Statistic,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import {
  portalBreakdownQueryKey,
  readPortalBreakdown,
  type PortalUsageWindow
} from "../api/portal";
import type { UsageMetrics } from "../api/usage";
import { AdminTable } from "./components/AdminTable";
import { WideSelect } from "./components/WideSelect";
import { formatTokens } from "./formatters";

type ModelRow = UsageMetrics & { model: string };

export function PortalUsageBreakdownDrawer({
  open,
  account,
  displayName,
  onClose
}: {
  open: boolean;
  account: string;
  displayName: string;
  onClose: () => void;
}) {
  const [window, setWindow] = useState<PortalUsageWindow>("86400");
  const query = useQuery({
    queryKey: portalBreakdownQueryKey(account, window),
    queryFn: ({ signal }) => readPortalBreakdown(account, window, signal),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: true,
    refetchInterval: open ? 10_000 : false
  });

  return (
    <Drawer
      title={`${displayName || "全部账号"} · 用量明细`}
      open={open}
      size={760}
      onClose={onClose}
      destroyOnHidden
      extra={(
        <Space wrap>
          <WideSelect<PortalUsageWindow>
            aria-label="用量时间范围"
            value={window}
            options={portalWindowOptions}
            onChange={setWindow}
          />
          <Button
            icon={<ReloadOutlined aria-hidden="true" />}
            loading={query.isFetching}
            onClick={() => void query.refetch()}
          >
            立即刷新
          </Button>
        </Space>
      )}
    >
      <Space orientation="vertical" size={16} className="usage-drawer-content">
        <Alert
          type="info"
          showIcon
          title="按需实时查询"
          description="仅在抽屉打开时读取当前 SQLite 快照；关闭后停止轮询并释放前端 Query。"
        />
        {query.isPending ? <Card loading /> : null}
        {query.isError ? (
          <Result
            status="warning"
            title="用量明细加载失败"
            subTitle={query.error instanceof Error ? query.error.message : "请稍后重试"}
            extra={<Button type="primary" onClick={() => void query.refetch()}>重新加载</Button>}
          />
        ) : null}
        {query.data ? <PortalBreakdownContent data={query.data} /> : null}
      </Space>
    </Drawer>
  );
}

function PortalBreakdownContent({ data }: { data: Awaited<ReturnType<typeof readPortalBreakdown>> }) {
  if (!data.collection_started_at) {
    return <Empty description="明细采集尚未开始" />;
  }
  return (
    <>
      <Row gutter={[12, 12]}>
        <Col xs={12} lg={6}><Card><Statistic title="请求数" value={data.totals.request_count} /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title="成功" value={data.totals.success_count} /></Card></Col>
        <Col xs={12} lg={6}><Card><Statistic title="失败" value={data.totals.failed_count} /></Card></Col>
        <Col xs={12} lg={6}>
          <Card>
            <Statistic
              title="加权 Token"
              value={data.totals.weighted_tokens ?? 0}
              formatter={(value) => formatTokens(Number(value))}
            />
          </Card>
        </Col>
      </Row>
      <Card
        title="模型用量"
        extra={<Typography.Text type="secondary">数据时间：{formatTimestamp(data.generated_at)}</Typography.Text>}
      >
        <AdminTable<ModelRow>
          rowKey="model"
          columns={modelColumns}
          dataSource={data.models}
          pagination={false}
          locale={{ emptyText: "当前范围没有模型明细" }}
          scroll={{ x: 620 }}
          maxBodyHeight="min(48vh, 480px)"
          size="small"
        />
      </Card>
      <Card title="推理强度">
        <Space wrap>
          {data.reasoning_efforts.length === 0 ? (
            <Typography.Text type="secondary">当前范围没有推理强度明细</Typography.Text>
          ) : data.reasoning_efforts.map((item) => (
            <Tag key={item.reasoning_effort}>
              {effortLabels[item.reasoning_effort] ?? item.reasoning_effort} · {formatTokens(item.weighted_tokens ?? 0)}
            </Tag>
          ))}
        </Space>
      </Card>
    </>
  );
}

const modelColumns: TableColumnsType<ModelRow> = [
  { title: "模型", dataIndex: "model", width: 220 },
  { title: "请求", dataIndex: "request_count", align: "right", width: 90 },
  {
    title: "加权 Token",
    align: "right",
    width: 140,
    render: (_, item) => formatTokens(item.weighted_tokens ?? 0)
  },
  { title: "最后使用", dataIndex: "last_used_at", width: 170, render: formatTimestamp }
];

const portalWindowOptions: Array<{ value: PortalUsageWindow; label: string }> = [
  { value: "today", label: "今天" },
  { value: "3600", label: "近 1 小时" },
  { value: "86400", label: "近 24 小时" },
  { value: "604800", label: "近 7 天" },
  { value: "2592000", label: "近 30 天" }
];

const effortLabels: Record<string, string> = {
  none: "无",
  minimal: "最小",
  low: "低",
  medium: "中",
  high: "高",
  xhigh: "超高",
  max: "最大",
  ultra: "极高",
  auto: "自动",
  unknown: "未知"
};

function formatTimestamp(timestamp: number) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit"
  }).format(new Date(timestamp * 1000));
}
