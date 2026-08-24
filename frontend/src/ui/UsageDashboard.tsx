import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Col,
  Empty,
  Modal,
  Progress,
  Result,
  Row,
  Select,
  Skeleton,
  Space,
  Statistic,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import {
  BarChartOutlined,
  CheckCircleOutlined,
  CopyOutlined,
  EyeInvisibleOutlined,
  EyeOutlined,
  KeyOutlined,
  LockOutlined,
  ReloadOutlined,
  SwapOutlined
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { ApiError } from "../api/client";
import {
  portalAccountsQueryKey,
  portalProfileQueryKey,
  portalRouteQueryKey,
  readPortalAccounts,
  readPortalProfile,
  readPortalRoute,
  rotatePortalKey,
  switchPortalAccount,
  type PortalAccount,
  type PortalProfile,
  type PortalUsageWindow
} from "../api/portal";
import { PortalPasswordModal } from "./PortalPasswordModal";
import { PortalUsageBreakdownDrawer } from "./PortalUsageBreakdownDrawer";

const { Paragraph, Text, Title } = Typography;

type BreakdownTarget = { account: string; displayName: string };

export function UsageDashboard({ onSessionExpired }: { onSessionExpired: () => void }) {
  const queryClient = useQueryClient();
  const { message } = AntApp.useApp();
  const [window, setWindow] = useState<PortalUsageWindow>("today");
  const [showKey, setShowKey] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const [rotationOpen, setRotationOpen] = useState(false);
  const [switchTarget, setSwitchTarget] = useState<PortalAccount | null>(null);
  const [breakdownTarget, setBreakdownTarget] = useState<BreakdownTarget | null>(null);

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
  const route = useQuery({
    queryKey: portalRouteQueryKey,
    queryFn: ({ signal }) => readPortalRoute(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: true,
    refetchInterval: 10_000
  });

  useEffect(() => {
    if ([profile.error, accounts.error, route.error].some(isUnauthorized)) {
      onSessionExpired();
    }
  }, [accounts.error, onSessionExpired, profile.error, route.error]);

  const currentGroup = route.data?.current_group ?? accounts.data?.current_group ?? profile.data?.current_group ?? "";
  const currentAccount = accounts.data?.accounts.find((item) => item.id === currentGroup);

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
        queryClient.invalidateQueries({ queryKey: portalAccountsQueryKey(window), exact: true })
      ]);
      void message.success(result.changed ? "账号已切换并完成 Gateway 激活确认" : "当前已使用该账号");
    }
  });
  const rotation = useMutation({
    mutationFn: rotatePortalKey,
    onSuccess: (result) => {
      queryClient.setQueryData<PortalProfile>(portalProfileQueryKey, (current) => current ? {
        ...current,
        api_key: result.api_key,
        generated_at: Math.floor(Date.now() / 1000)
      } : current);
      setShowKey(true);
      setRotationOpen(false);
      void message.success("API Key 已刷新；旧 Key 已立即失效");
    }
  });

  const copyKey = async () => {
    const key = profile.data?.api_key;
    if (!key) return;
    try {
      await navigator.clipboard.writeText(key);
      void message.success("API Key 已复制");
    } catch {
      void message.error("浏览器未允许复制，请展开后手动复制");
    }
  };

  return (
    <section className="usage-dashboard">
      <div className="usage-hero">
        <div>
          <span className="eyebrow">YOUR CPA WORKSPACE</span>
          <Title level={2}>你好，{profile.data?.user ?? "正在确认身份"}</Title>
          <Paragraph>这里仅展示你的凭据、当前路由与请求用量。页面关闭后，API Key 不会留在浏览器存储中。</Paragraph>
        </div>
        <Space wrap>
          <Button icon={<LockOutlined aria-hidden="true" />} onClick={() => setPasswordOpen(true)}>修改密码</Button>
          <Button
            icon={<ReloadOutlined aria-hidden="true" />}
            loading={profile.isFetching || accounts.isFetching || route.isFetching}
            onClick={() => void Promise.all([profile.refetch(), accounts.refetch(), route.refetch()])}
          >
            刷新当前页
          </Button>
        </Space>
      </div>

      {accounts.data?.warnings.map((warning) => (
        <Alert key={warning} className="page-alert" type="warning" showIcon message={warning} />
      ))}

      <Row gutter={[16, 16]} className="portal-summary-grid">
        <Col xs={24} xl={12}>
          <Card className="portal-key-card" loading={profile.isPending}>
            {profile.isError ? (
              <Result
                status="warning"
                title="API Key 加载失败"
                subTitle={errorMessage(profile.error)}
                extra={<Button onClick={() => void profile.refetch()}>重新加载</Button>}
              />
            ) : profile.data ? (
              <Space orientation="vertical" size={18} className="portal-card-stack">
                <Space className="portal-card-title"><KeyOutlined aria-hidden="true" /><Text strong>我的 API Key</Text></Space>
                <div className="portal-key-value" aria-label="API Key">
                  {showKey ? profile.data.api_key : maskAPIKey(profile.data.api_key)}
                </div>
                <Space wrap>
                  <Button
                    icon={showKey ? <EyeInvisibleOutlined aria-hidden="true" /> : <EyeOutlined aria-hidden="true" />}
                    onClick={() => setShowKey((value) => !value)}
                  >
                    {showKey ? "隐藏" : "显示"}
                  </Button>
                  <Button type="primary" icon={<CopyOutlined aria-hidden="true" />} onClick={() => void copyKey()}>
                    复制 API Key
                  </Button>
                  <Button danger icon={<SwapOutlined aria-hidden="true" />} onClick={() => {
                    rotation.reset();
                    setRotationOpen(true);
                  }}>
                    刷新 Key
                  </Button>
                </Space>
              </Space>
            ) : null}
          </Card>
        </Col>
        <Col xs={24} md={12} xl={6}>
          <Card className="portal-route-card">
            <Space orientation="vertical" size={12} className="portal-card-stack">
              <Text type="secondary">当前路由</Text>
              {route.isPending && !currentAccount ? <Skeleton.Input active /> : (
                <>
                  <Title level={3}>{currentAccount?.display_name ?? (currentGroup ? "当前 CPA" : "尚未分配")}</Title>
                  {currentAccount ? <AccountStatusTag account={currentAccount} /> : <Tag>等待分配</Tag>}
                </>
              )}
            </Space>
          </Card>
        </Col>
        <Col xs={24} md={12} xl={6}>
          <Card className="portal-usage-card" loading={accounts.isPending}>
            <Statistic
              title={windowLabel(window)}
              value={accounts.data?.totals.weighted_tokens ?? 0}
              formatter={(value) => formatTokens(Number(value))}
              suffix="加权 Token"
            />
            <Button
              type="link"
              icon={<BarChartOutlined aria-hidden="true" />}
              onClick={() => setBreakdownTarget({ account: "", displayName: "全部账号" })}
            >
              查看全部明细
            </Button>
          </Card>
        </Col>
      </Row>

      <Card
        className="portal-account-card"
        title="可用账号与个人用量"
        extra={(
          <Space wrap>
            <Select<PortalUsageWindow>
              aria-label="账号用量时间范围"
              value={window}
              options={portalWindowOptions}
              onChange={setWindow}
            />
            <Text type="secondary">数据时间：{formatTimestamp(accounts.data?.generated_at ?? 0)}</Text>
          </Space>
        )}
      >
        {accounts.isPending ? <Skeleton active paragraph={{ rows: 5 }} /> : null}
        {accounts.isError ? (
          <Result
            status="warning"
            title="账号与用量加载失败"
            subTitle={errorMessage(accounts.error)}
            extra={<Button type="primary" onClick={() => void accounts.refetch()}>重新加载</Button>}
          />
        ) : null}
        {accounts.data && accounts.data.accounts.length === 0 ? <Empty description="尚未分配可用账号" /> : null}
        {accounts.data?.accounts.length ? (
          <Table<PortalAccount>
            rowKey="id"
            columns={accountColumns({
              currentGroup,
              onBreakdown: (account) => setBreakdownTarget({ account: account.id, displayName: account.display_name }),
              onSwitch: (account) => {
                accountSwitch.reset();
                setSwitchTarget(account);
              }
            })}
            dataSource={accounts.data.accounts}
            pagination={false}
            scroll={{ x: 820 }}
          />
        ) : null}
      </Card>

      <Modal
        title={`切换到 ${switchTarget?.display_name ?? "目标账号"}`}
        open={Boolean(switchTarget)}
        okText="确认切换"
        cancelText="取消"
        confirmLoading={accountSwitch.isPending}
        onCancel={() => !accountSwitch.isPending && setSwitchTarget(null)}
        onOk={() => switchTarget && accountSwitch.mutate(switchTarget)}
        destroyOnHidden
      >
        <Space orientation="vertical" size={16} className="portal-form-stack">
          <Alert
            type="info"
            showIcon
            message="现有 API Key 不会改变"
            description="只更新你的目标 CPA。系统会原子写入路由、发布鉴权快照并等待 Gateway 确认；失败时自动恢复原路由。"
          />
          {accountSwitch.isError ? <Alert type="error" showIcon message="账号切换失败" description={errorMessage(accountSwitch.error)} /> : null}
        </Space>
      </Modal>

      <Modal
        title="刷新个人 API Key"
        open={rotationOpen}
        okText="确认刷新并使旧 Key 失效"
        cancelText="取消"
        okButtonProps={{ danger: true }}
        confirmLoading={rotation.isPending}
        onCancel={() => !rotation.isPending && setRotationOpen(false)}
        onOk={() => rotation.mutate()}
        destroyOnHidden
      >
        <Space orientation="vertical" size={16} className="portal-form-stack">
          <Alert
            type="warning"
            showIcon
            message="旧 API Key 会立即失效"
            description="刷新成功后，请立刻把新 Key 更新到 Codex 客户端。系统仅在 Gateway 已激活新鉴权快照后返回成功。"
          />
          {rotation.isError ? <Alert type="error" showIcon message="API Key 刷新失败" description={errorMessage(rotation.error)} /> : null}
        </Space>
      </Modal>

      <PortalPasswordModal
        open={passwordOpen}
        onClose={() => setPasswordOpen(false)}
        onSuccess={() => {
          setPasswordOpen(false);
          void message.success("密码已修改，其他会话已撤销");
        }}
      />
      <PortalUsageBreakdownDrawer
        open={Boolean(breakdownTarget)}
        account={breakdownTarget?.account ?? ""}
        displayName={breakdownTarget?.displayName ?? ""}
        onClose={() => setBreakdownTarget(null)}
      />
    </section>
  );
}

function accountColumns({
  currentGroup,
  onBreakdown,
  onSwitch
}: {
  currentGroup: string;
  onBreakdown: (account: PortalAccount) => void;
  onSwitch: (account: PortalAccount) => void;
}): TableColumnsType<PortalAccount> {
  return [
    {
      title: "账号",
      dataIndex: "display_name",
      fixed: "left",
      align: "center",
      width: 150,
      render: (value: string, account) => (
        <Space>
          <Text strong>{value}</Text>
          {account.id === currentGroup ? <Tag icon={<CheckCircleOutlined aria-hidden="true" />} color="green">当前</Tag> : null}
        </Space>
      )
    },
    { title: "状态", align: "center", width: 145, render: (_, account) => <AccountStatusTag account={account} /> },
    {
      title: "剩余额度",
      align: "right",
      width: 180,
      render: (_, account) => account.status.remaining_percent === undefined ? (
        <Text type="secondary">状态未知</Text>
      ) : (
        <Progress
          percent={Math.max(0, Math.min(100, account.status.remaining_percent))}
          size="small"
          status={account.status.remaining_percent <= 5 ? "exception" : "normal"}
          format={(value) => `${Number(value ?? 0).toFixed(1)}%`}
        />
      )
    },
    { title: "1h 活跃用户", dataIndex: "active_users_1h", align: "right", width: 120 },
    {
      title: "个人加权 Token",
      align: "right",
      width: 145,
      render: (_, account) => formatTokens(account.usage.weighted_tokens ?? 0)
    },
    {
      title: "操作",
      fixed: "right",
      align: "center",
      width: 205,
      render: (_, account) => (
        <Space size={4}>
          <Button type="link" onClick={() => onBreakdown(account)}>用量明细</Button>
          <Button
            type="link"
            disabled={account.id === currentGroup || !account.selectable}
            onClick={() => onSwitch(account)}
          >
            {account.id === currentGroup ? "当前账号" : "切换"}
          </Button>
        </Space>
      )
    }
  ];
}

function AccountStatusTag({ account }: { account: PortalAccount }) {
  const color = account.status.tone === "success"
    ? "green"
    : account.status.tone === "warning"
      ? "gold"
      : account.status.tone === "danger"
        ? "red"
        : "default";
  return <Tag color={color} title={account.status.reason}>{account.status.label}</Tag>;
}

function isUnauthorized(error: unknown) {
  return error instanceof ApiError && error.status === 401;
}

function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : "请稍后重试";
}

function maskAPIKey(value: string) {
  if (value.length <= 12) return "••••••••••••";
  return `${value.slice(0, 10)}••••••••${value.slice(-4)}`;
}

function windowLabel(window: PortalUsageWindow) {
  return portalWindowOptions.find((item) => item.value === window)?.label ?? "当前范围";
}

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

const portalWindowOptions: Array<{ value: PortalUsageWindow; label: string }> = [
  { value: "today", label: "今天" },
  { value: "3600", label: "近 1 小时" },
  { value: "86400", label: "近 24 小时" },
  { value: "604800", label: "近 7 天" },
  { value: "2592000", label: "近 30 天" }
];
