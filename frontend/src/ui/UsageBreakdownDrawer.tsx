import {
  Alert,
  Button,
  Card,
  Col,
  Drawer,
  Empty,
  Result,
  Row,
  Select,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { useState } from "react";

import {
  readUsageBreakdown,
  usageBreakdownQueryKey,
  type UsageCombination,
  type UsageWindow
} from "../api/usage";

const { Text } = Typography;

export function UsageBreakdownDrawer({
  kind,
  subject,
  onClose
}: {
  kind: "account" | "user";
  subject: string | null;
  onClose: () => void;
}) {
  const [window, setWindow] = useState<UsageWindow>("86400");
  const open = Boolean(subject);
  const query = useQuery({
    queryKey: usageBreakdownQueryKey(kind, subject ?? "", window),
    queryFn: ({ signal }) => readUsageBreakdown(kind, subject ?? "", window, signal),
    enabled: open,
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always",
    refetchOnWindowFocus: true,
    refetchInterval: open ? 10_000 : false
  });

  return (
    <Drawer
      title={`${kind === "account" ? "账号" : "用户"}用量详情`}
      open={open}
      size={760}
      onClose={onClose}
      destroyOnHidden
      extra={(
        <Space wrap>
          <Select<UsageWindow>
            aria-label="用量时间范围"
            value={window}
            options={usageWindowOptions}
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
          message="按需实时查询"
          description="打开后每 10 秒直接读取当前 SQLite 快照；关闭后停止查询并释放前端缓存，不获取控制面写锁。"
        />
        <Text strong>{subject}</Text>
        {query.isPending ? <Card loading /> : null}
        {query.isError ? (
          <Result
            status="warning"
            title="用量详情加载失败"
            subTitle={query.error instanceof Error ? query.error.message : "请稍后重试"}
            extra={<Button type="primary" onClick={() => void query.refetch()}>重新加载</Button>}
          />
        ) : null}
        {query.data ? <UsageBreakdownContent kind={kind} data={query.data} /> : null}
      </Space>
    </Drawer>
  );
}

function UsageBreakdownContent({
  kind,
  data
}: {
  kind: "account" | "user";
  data: Awaited<ReturnType<typeof readUsageBreakdown>>;
}) {
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
              title={kind === "user" ? "加权 Token" : "Token"}
              value={kind === "user" ? data.totals.weighted_tokens ?? 0 : data.totals.total_tokens}
              formatter={(value) => formatTokens(Number(value))}
            />
          </Card>
        </Col>
      </Row>
      <Card
        title="模型与推理强度"
        extra={<Text type="secondary">数据时间：{formatTimestamp(data.generated_at)}</Text>}
      >
        <Table<UsageCombination>
          rowKey={(item) => `${item.account ?? "all"}:${item.model}:${item.reasoning_effort}`}
          columns={combinationColumns(kind)}
          dataSource={data.combinations}
          pagination={false}
          locale={{ emptyText: "当前范围没有模型明细" }}
          scroll={{ x: 700 }}
          size="small"
        />
      </Card>
    </>
  );
}

function combinationColumns(kind: "account" | "user"): TableColumnsType<UsageCombination> {
  return [
    ...(kind === "user" ? [{
      title: "账号",
      dataIndex: "account" as const,
      width: 120
    }] : []),
    { title: "模型", dataIndex: "model", width: 190 },
    {
      title: "推理强度",
      dataIndex: "reasoning_effort",
      width: 110,
      render: (value: string) => <Tag>{effortLabels[value] ?? value}</Tag>
    },
    { title: "请求", dataIndex: "request_count", align: "right", width: 80 },
    {
      title: kind === "user" ? "加权 Token" : "Token",
      align: "right",
      width: 125,
      render: (_, item) => formatTokens(kind === "user" ? item.weighted_tokens ?? 0 : item.total_tokens)
    },
    { title: "最后使用", dataIndex: "last_used_at", width: 165, render: formatTimestamp }
  ];
}

const usageWindowOptions: Array<{ value: UsageWindow; label: string }> = [
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

function formatTokens(value: number) {
  return new Intl.NumberFormat("zh-CN", { notation: "compact", maximumFractionDigits: 2 }).format(value);
}

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
