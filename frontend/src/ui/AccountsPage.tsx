import { zodResolver } from "@hookform/resolvers/zod";
import {
  BarChartOutlined,
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  MoreOutlined,
  PlusOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined,
  SwapOutlined
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Col,
  Dropdown,
  Form,
  Input,
  Modal,
  Progress,
  Row,
  Skeleton,
  Space,
  Switch,
  Tag,
  Typography,
  type MenuProps,
  type TableColumnsType
} from "antd";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import {
  accountsQueryKey,
  clearAccountAuth,
  createAccount,
  deleteAccount,
  listAccounts,
  rebalanceAllAccounts,
  updateAccount,
  type Account,
  type AccountClearAuthResponse,
  type AccountCreateRequestWritable,
  type AccountCreateResponse,
  type AccountDeleteRequest,
  type AccountDeleteResponse,
  type AccountUpdateRequestWritable,
  type AccountUpdateResponse,
  type RebalanceResponse
} from "../api/accounts";
import { AdminTable } from "./components/AdminTable";
import { MetricCard } from "./components/MetricCard";
import { PageState } from "./components/PageState";
import { PageToolbar } from "./components/PageToolbar";
import { WideSelect } from "./components/WideSelect";
import { UsageBreakdownDrawer } from "./UsageBreakdownDrawer";

const { Paragraph, Text } = Typography;

type AccountLifecycleCommand =
  | { kind: "create"; request: AccountCreateRequestWritable }
  | { kind: "update"; request: AccountUpdateRequestWritable }
  | { kind: "clear-auth"; request: { id: string; confirm: string } }
  | { kind: "delete"; request: AccountDeleteRequest };

type AccountLifecycleResponse =
  | AccountCreateResponse
  | AccountUpdateResponse
  | AccountClearAuthResponse
  | AccountDeleteResponse;

type DestructiveAction = { kind: "clear-auth" | "delete"; account: Account };

export function AccountsPage({ csrfToken }: { csrfToken: string }) {
  const queryClient = useQueryClient();
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [lastResult, setLastResult] = useState<RebalanceResponse | null>(null);
  const [notice, setNotice] = useState("");
  const [usageAccount, setUsageAccount] = useState<string | null>(null);
  const [editorAccount, setEditorAccount] = useState<Account | "create" | null>(null);
  const [destructiveAction, setDestructiveAction] = useState<DestructiveAction | null>(null);
  const accounts = useQuery({
    queryKey: accountsQueryKey,
    queryFn: ({ signal }) => listAccounts(signal)
  });
  const lifecycle = useMutation({
    gcTime: 0,
    mutationFn: (command: AccountLifecycleCommand): Promise<AccountLifecycleResponse> => {
      switch (command.kind) {
        case "create": return createAccount(command.request, csrfToken);
        case "update": return updateAccount(command.request, csrfToken);
        case "clear-auth": return clearAccountAuth(command.request, csrfToken);
        case "delete": return deleteAccount(command.request, csrfToken);
      }
    },
    onSuccess: async (result) => {
      setEditorAccount(null);
      setDestructiveAction(null);
      setNotice(result.message);
      lifecycle.reset();
      await queryClient.invalidateQueries({ queryKey: accountsQueryKey, exact: true });
    }
  });
  const rebalance = useMutation({
    mutationFn: () => rebalanceAllAccounts(csrfToken),
    onSuccess: async (result) => {
      setConfirmOpen(false);
      setLastResult(result);
      await queryClient.invalidateQueries({ queryKey: accountsQueryKey, exact: true });
    }
  });
  const resetLifecycle = lifecycle.reset;
  const openEditor = useCallback((account: Account | "create") => {
    resetLifecycle();
    setEditorAccount(account);
  }, [resetLifecycle]);
  const openDestructiveAction = useCallback((action: DestructiveAction) => {
    resetLifecycle();
    setDestructiveAction(action);
  }, [resetLifecycle]);
  const columns = useMemo(() => accountColumns({
    onOpenUsage: setUsageAccount,
    onEdit: openEditor,
    onDestructiveAction: openDestructiveAction
  }), [openDestructiveAction, openEditor]);

  if (accounts.isPending) return <AccountPageSkeleton />;
  if (accounts.isError) {
    return (
      <section className="page-content">
        <PageState
          kind="error"
          title="账号数据加载失败"
          detail={accounts.error instanceof Error ? accounts.error.message : "请稍后重试"}
          onAction={() => void accounts.refetch()}
        />
      </section>
    );
  }

  const enabledAccounts = accounts.data.accounts.filter((account) => account.enabled).length;
  const routedUsers = accounts.data.accounts.reduce((total, account) => total + account.routed_users, 0);
  const activeUsers = accounts.data.accounts.reduce((total, account) => total + (account.active_users_1h ?? 0), 0);

  return (
    <section className="page-content account-page">
      <PageToolbar
        className="account-page-intro"
        description="仅在进入本页时请求账号目录、额度状态和近 1 小时活跃数；创建、更新和删除均在 Gateway 新快照激活后才返回成功。"
        actions={(
          <>
          <Button icon={<ReloadOutlined aria-hidden="true" />} onClick={() => void accounts.refetch()} loading={accounts.isFetching}>
            刷新当前页
          </Button>
          <Button icon={<PlusOutlined aria-hidden="true" />} onClick={() => openEditor("create")}>
            创建账号
          </Button>
          <Button
            type="primary"
            icon={<SwapOutlined aria-hidden="true" />}
            disabled={enabledAccounts < 2}
            onClick={() => {
              rebalance.reset();
              setConfirmOpen(true);
            }}
          >
            一键负载均衡
          </Button>
          </>
        )}
      />

      {notice ? (
        <Alert className="page-alert" type="success" showIcon closable message={notice} onClose={() => setNotice("")} />
      ) : null}
      {accounts.data.warnings.map((warning) => (
        <Alert key={warning} className="page-alert" type="warning" showIcon message={warning} />
      ))}
      {lastResult ? (
        <Alert
          className="page-alert"
          type={lastResult.rebalance.warning ? "warning" : "success"}
          showIcon
          closable
          onClose={() => setLastResult(null)}
          message={lastResult.message}
          description={<RebalanceSummary result={lastResult} />}
        />
      ) : null}

      <Row gutter={[16, 16]} className="account-stat-grid">
        <Col xs={24} sm={8}><MetricCard title="启用账号" value={enabledAccounts} suffix={`/ ${accounts.data.accounts.length}`} /></Col>
        <Col xs={24} sm={8}><MetricCard title="已路由用户" value={routedUsers} /></Col>
        <Col xs={24} sm={8}><MetricCard title="1h 活跃数合计" value={activeUsers} suffix={accounts.data.warnings.length ? "*" : ""} /></Col>
      </Row>

      <Card
        className="account-table-card"
        title="账号负载与额度"
        extra={<Text type="secondary">数据时间：{formatTimestamp(accounts.data.generated_at)}</Text>}
      >
        <AdminTable<Account>
          rowKey="id"
          columns={columns}
          dataSource={accounts.data.accounts}
          minWidth={1180}
          emptyText="还没有业务账号"
          emptyAction={<Button type="primary" icon={<PlusOutlined />} onClick={() => openEditor("create")}>创建第一个账号</Button>}
        />
      </Card>

      <AccountEditorModal
        open={editorAccount !== null}
        account={editorAccount === "create" ? null : editorAccount}
        accounts={accounts.data.accounts}
        pending={lifecycle.isPending}
        error={lifecycle.error}
        onCancel={() => !lifecycle.isPending && setEditorAccount(null)}
        onSubmit={(command) => lifecycle.mutate(command)}
      />
      <AccountDestructiveModal
        action={destructiveAction}
        accounts={accounts.data.accounts}
        pending={lifecycle.isPending}
        error={lifecycle.error}
        onCancel={() => !lifecycle.isPending && setDestructiveAction(null)}
        onSubmit={(command) => lifecycle.mutate(command)}
      />
      <Modal
        title="一键负载均衡所有账号"
        open={confirmOpen}
        confirmLoading={rebalance.isPending}
        okText="确认开始均衡"
        cancelText="取消"
        okButtonProps={{ danger: true }}
        onCancel={() => !rebalance.isPending && setConfirmOpen(false)}
        onOk={() => rebalance.mutate()}
        destroyOnHidden
      >
        <Space orientation="vertical" size={16} className="rebalance-confirmation">
          <Alert
            type="warning"
            showIcon
            message="这会修改用户当前路由"
            description="系统会按账号可用额度重新分布全部有效用户，并尽量减少迁移数量。任一用户不满足统一 Key 安全条件时，整批操作都会拒绝。"
          />
          <Paragraph>
            路由写入后必须等待 Gateway 激活新的鉴权快照；失败时自动恢复原路由并发布回滚快照。成功后会立即重新查询近 1 小时活跃用户数。
          </Paragraph>
          {rebalance.isError ? <MutationError error={rebalance.error} title="负载均衡未执行" /> : null}
        </Space>
      </Modal>
      <UsageBreakdownDrawer kind="account" subject={usageAccount} onClose={() => setUsageAccount(null)} />
    </section>
  );
}

function accountColumns({
  onOpenUsage,
  onEdit,
  onDestructiveAction
}: {
  onOpenUsage: (account: string) => void;
  onEdit: (account: Account) => void;
  onDestructiveAction: (action: DestructiveAction) => void;
}): TableColumnsType<Account> {
  return [
    {
      title: "账号",
      dataIndex: "id",
      fixed: "left",
      align: "center",
      width: 210,
      render: (_, account) => (
        <Space orientation="vertical" size={1}>
          <Space size={6}>
            <Text strong>{account.id}</Text>
            {account.default ? <Tag color="blue">默认</Tag> : null}
          </Space>
          <Text type="secondary">{account.email}</Text>
        </Space>
      )
    },
    { title: "运行状态", align: "center", width: 150, render: (_, account) => <AccountStatus account={account} /> },
    {
      title: "剩余额度",
      align: "right",
      width: 180,
      render: (_, account) => {
        const remaining = account.account_state.remaining_percent;
        if (!account.state_available || remaining === null) return <Text type="secondary">状态未知</Text>;
        return (
          <Progress
            percent={Math.max(0, Math.min(100, remaining))}
            size="small"
            status={remaining <= 5 ? "exception" : "normal"}
            format={(value) => `${Number(value ?? 0).toFixed(1)}%`}
          />
        );
      }
    },
    { title: "已路由用户", dataIndex: "routed_users", align: "right", width: 120 },
    {
      title: "1h 活跃用户",
      dataIndex: "active_users_1h",
      align: "right",
      width: 130,
      render: (value: number | null) => value ?? <Text type="secondary">—</Text>
    },
    {
      title: "出口策略",
      dataIndex: "proxy_mode",
      align: "center",
      width: 135,
      render: (value: string, account) => (
        <Space size={6}>
          <Text>{proxyModeLabel[value] ?? value}</Text>
          {value === "custom" ? (
            <Tag color={account.proxy_configured ? "success" : "error"}>{account.proxy_configured ? "已配置" : "缺失"}</Tag>
          ) : null}
        </Space>
      )
    },
    { title: "数据时间", align: "center", width: 165, render: (_, account) => formatTimestamp(account.account_state.observed_at) },
    {
      title: "操作",
      fixed: "right",
      align: "center",
      width: 225,
      render: (_, account) => {
        const items: MenuProps["items"] = [
          { key: "clear-auth", icon: <KeyOutlined aria-hidden="true" />, label: "清除 OAuth 授权" },
          { key: "delete", icon: <DeleteOutlined aria-hidden="true" />, label: "删除账号", danger: true }
        ];
        return (
          <Space size={0}>
            <Button type="link" icon={<BarChartOutlined aria-hidden="true" />} aria-label={`查看 ${account.id} 的用量`} onClick={() => onOpenUsage(account.id)}>用量</Button>
            <Button type="link" icon={<EditOutlined aria-hidden="true" />} aria-label={`编辑 ${account.id}`} onClick={() => onEdit(account)}>编辑</Button>
            <Dropdown
              trigger={["click"]}
              menu={{
                items,
                onClick: ({ key }) => onDestructiveAction({ kind: key as DestructiveAction["kind"], account })
              }}
            >
              <Button type="link" icon={<MoreOutlined aria-hidden="true" />} aria-label={`${account.id} 更多操作`}>更多</Button>
            </Dropdown>
          </Space>
        );
      }
    }
  ];
}

const proxyModeSchema = z.enum(["inherit", "custom", "direct"]);
const accountEditorSchema = z.object({
  id: z.string().trim().regex(/^[A-Za-z][A-Za-z0-9-]{1,31}$/, "请输入 2-32 位字母、数字或连字符，且以字母开头"),
  email: z.string().trim().email("请输入有效邮箱地址"),
  proxy_mode: proxyModeSchema,
  proxy_url: z.string().trim().refine((value) => !value || validProxyURL(value), "仅支持无路径的 HTTP、HTTPS 或 SOCKS5 代理地址"),
  group_enabled: z.boolean(),
  default_group: z.boolean(),
  fallback_account: z.string(),
  confirm: z.string(),
  clear_proxy: z.boolean()
});

type AccountEditorValues = z.infer<typeof accountEditorSchema>;

function AccountEditorModal({
  open,
  account,
  accounts,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  open: boolean;
  account: Account | null;
  accounts: Account[];
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (command: AccountLifecycleCommand) => void;
}) {
  const form = useForm<AccountEditorValues>({ resolver: zodResolver(accountEditorSchema), defaultValues: emptyAccountEditorValues() });
  useEffect(() => {
    if (open) form.reset(account ? accountEditorValues(account) : emptyAccountEditorValues());
  }, [account, form, open]);
  const proxyMode = form.watch("proxy_mode");
  const accountID = form.watch("id").trim().toLowerCase();
  const enabled = form.watch("group_enabled");
  const isDefault = form.watch("default_group");
  const clearProxy = form.watch("clear_proxy");
  const fallbackRequired = account !== null && (!enabled || (account.default && !isDefault));
  const fallbackOptions = accounts
    .filter((candidate) => candidate.enabled && candidate.id !== account?.id)
    .map((candidate) => ({ value: candidate.id, label: `${candidate.id} · ${candidate.email}` }));
  const renamed = account !== null && accountID !== account.id;

  const submit = form.handleSubmit((values) => {
    const proxyURL = values.proxy_url.trim();
    const existingProxyCanBeRetained = account?.proxy_configured && !values.clear_proxy;
    if (values.proxy_mode === "custom" && !proxyURL && !existingProxyCanBeRetained) {
      form.setError("proxy_url", { message: "独立代理模式必须输入代理地址" });
      return;
    }
    if (renamed && values.confirm.trim() !== account?.id) {
      form.setError("confirm", { message: `请输入 ${account?.id} 确认重命名` });
      return;
    }
    if (fallbackRequired && !values.fallback_account) {
      form.setError("fallback_account", { message: "请选择接收用户和默认路由的备用账号" });
      return;
    }
    if (!account) {
      onSubmit({
        kind: "create",
        request: {
          id: values.id.trim().toLowerCase(),
          email: values.email.trim().toLowerCase(),
          proxy_mode: values.proxy_mode,
          ...(proxyURL ? { proxy_url: proxyURL } : {})
        }
      });
      return;
    }
    const request: AccountUpdateRequestWritable = {
      id: account.id,
      new_id: values.id.trim().toLowerCase(),
      email: values.email.trim().toLowerCase(),
      proxy_mode: values.proxy_mode,
      group_enabled: values.group_enabled,
      default_group: values.default_group,
      ...(values.fallback_account ? { fallback_account: values.fallback_account } : {}),
      ...(renamed ? { confirm: account.id } : {})
    };
    if (values.clear_proxy) request.proxy_url = "";
    else if (proxyURL) request.proxy_url = proxyURL;
    onSubmit({ kind: "update", request });
  });

  return (
    <Modal
      title={account ? `编辑 CPA · ${account.id}` : "创建业务 CPA"}
      open={open}
      width={680}
      okText={account ? "保存并重建" : "创建并探测"}
      cancelText="取消"
      confirmLoading={pending}
      onCancel={onCancel}
      onOk={() => void submit()}
      destroyOnHidden
    >
      <Form className="account-editor-form" layout="vertical" requiredMark={false}>
        <Alert
          className="account-editor-note"
          type="info"
          showIcon
          message={account ? "变更通过补偿式事务发布" : "新账号会复用当前统一 API Key"}
          description={account
            ? "系统会依次更新 SQLite、加密配置、容器和 Gateway 快照；任一步失败都会恢复原状态。"
            : "创建过程不会轮换任何用户 API Key；容器探针和 Gateway 快照激活成功后才会返回。"}
        />
        <Row gutter={16}>
          <Col xs={24} md={12}><EditorInput control={form.control} name="id" label="CPA 标识" placeholder="例如 account-a" /></Col>
          <Col xs={24} md={12}><EditorInput control={form.control} name="email" label="上游账号邮箱" placeholder="name@example.com" /></Col>
          <Col xs={24} md={12}>
            <Controller
              control={form.control}
              name="proxy_mode"
              render={({ field, fieldState }) => (
                <Form.Item label="出口策略" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
                  <WideSelect
                    {...field}
                    aria-label="出口策略"
                    options={[
                      { value: "inherit", label: "继承系统默认代理" },
                      { value: "custom", label: "账号独立代理" },
                      { value: "direct", label: "直接连接" }
                    ]}
                  />
                </Form.Item>
              )}
            />
          </Col>
          {account ? (
            <Col xs={24} md={12}>
              <Form.Item label="账号状态">
                <Space size="large">
                  <Controller
                    control={form.control}
                    name="group_enabled"
                    render={({ field }) => (
                      <Space size={8}>
                        <Switch aria-label="启用业务账号" checked={field.value} onChange={field.onChange} />
                        <Text>{field.value ? "已启用" : "已停用"}</Text>
                      </Space>
                    )}
                  />
                  <Controller
                    control={form.control}
                    name="default_group"
                    render={({ field }) => (
                      <Space size={8}>
                        <Switch aria-label="设为默认账号" checked={field.value} disabled={!enabled} onChange={field.onChange} />
                        <Text>默认账号</Text>
                      </Space>
                    )}
                  />
                </Space>
              </Form.Item>
            </Col>
          ) : null}
          {proxyMode === "custom" ? (
            <Col xs={24}>
              <Controller
                control={form.control}
                name="proxy_url"
                render={({ field, fieldState }) => (
                  <Form.Item
                    label="独立代理地址"
                    validateStatus={fieldState.error ? "error" : undefined}
                    help={fieldState.error?.message ?? (account?.proxy_configured
                      ? "已配置的地址不会回显；留空会保留原值，输入新值才会覆盖。"
                      : "支持 http://、https://、socks5://，可包含账号密码，不允许路径、查询参数或片段。")}
                  >
                    <Input.Password
                      {...field}
                      aria-label="独立代理地址"
                      autoComplete="new-password"
                      disabled={clearProxy}
                      placeholder={account?.proxy_configured ? "已安全配置（留空保持不变）" : "socks5://user:password@proxy.example.com:1080"}
                    />
                  </Form.Item>
                )}
              />
            </Col>
          ) : null}
          {account?.proxy_configured ? (
            <Col xs={24}>
              <Controller
                control={form.control}
                name="clear_proxy"
                render={({ field }) => (
                  <Checkbox checked={field.value} disabled={proxyMode === "custom"} onChange={(event) => field.onChange(event.target.checked)}>
                    清除已加密保存的独立代理地址
                  </Checkbox>
                )}
              />
            </Col>
          ) : null}
          {fallbackRequired ? (
            <Col xs={24}>
              <Controller
                control={form.control}
                name="fallback_account"
                render={({ field, fieldState }) => (
                  <Form.Item
                    label="备用账号"
                    validateStatus={fieldState.error ? "error" : undefined}
                    help={fieldState.error?.message ?? "当前账号上的用户和默认路由会原子迁移到该账号。"}
                  >
                    <WideSelect
                      {...field}
                      aria-label="备用账号"
                      options={fallbackOptions}
                      placeholder="选择已启用账号"
                      notFoundContent="没有可用的备用账号"
                    />
                  </Form.Item>
                )}
              />
            </Col>
          ) : null}
          {renamed ? (
            <Col xs={24}><EditorInput control={form.control} name="confirm" label={`输入 ${account?.id} 确认重命名`} placeholder={account?.id} /></Col>
          ) : null}
        </Row>
        {error ? <MutationError error={error} title={account ? "账号更新未完成" : "账号创建未完成"} /> : null}
      </Form>
    </Modal>
  );
}

const destructiveSchema = z.object({ confirm: z.string(), fallback_account: z.string(), revoke_keys: z.boolean() });
type DestructiveValues = z.infer<typeof destructiveSchema>;

function AccountDestructiveModal({
  action,
  accounts,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  action: DestructiveAction | null;
  accounts: Account[];
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (command: AccountLifecycleCommand) => void;
}) {
  const form = useForm<DestructiveValues>({
    resolver: zodResolver(destructiveSchema),
    defaultValues: { confirm: "", fallback_account: "", revoke_keys: false }
  });
  useEffect(() => {
    if (action) form.reset({ confirm: "", fallback_account: "", revoke_keys: false });
  }, [action, form]);
  if (!action) return null;

  const account = action.account;
  const deleting = action.kind === "delete";
  const fallbackOptions = accounts
    .filter((candidate) => candidate.enabled && candidate.id !== account.id)
    .map((candidate) => ({ value: candidate.id, label: `${candidate.id} · ${candidate.email}` }));
  const submit = form.handleSubmit((values) => {
    if (values.confirm.trim() !== account.id) {
      form.setError("confirm", { message: `请输入 ${account.id} 完成确认` });
      return;
    }
    if (deleting && accounts.length > 1 && !values.fallback_account) {
      form.setError("fallback_account", { message: "请选择接收现有用户路由的备用账号" });
      return;
    }
    if (deleting) {
      onSubmit({
        kind: "delete",
        request: {
          id: account.id,
          confirm: values.confirm.trim(),
          revoke_keys: values.revoke_keys,
          ...(values.fallback_account ? { fallback_account: values.fallback_account } : {})
        }
      });
    } else {
      onSubmit({ kind: "clear-auth", request: { id: account.id, confirm: values.confirm.trim() } });
    }
  });

  return (
    <Modal
      title={deleting ? `删除 CPA · ${account.id}` : `清除 OAuth · ${account.id}`}
      open
      okText={deleting ? "确认删除并归档" : "确认清除并重启"}
      cancelText="取消"
      confirmLoading={pending}
      okButtonProps={{ danger: true, disabled: deleting && accounts.length <= 1 }}
      onCancel={onCancel}
      onOk={() => void submit()}
      destroyOnHidden
    >
      <Form layout="vertical" requiredMark={false}>
        <Alert
          className="account-editor-note"
          type={deleting ? "error" : "warning"}
          showIcon
          message={deleting ? "账号配置、OAuth 和日志会先安全归档" : "CPA 将在 OAuth 文件归档后重新启动"}
          description={deleting
            ? "系统会先迁移用户路由并等待 Gateway 激活，再删除业务容器；失败时恢复账号、路由、文件和容器。"
            : "此操作不会修改用户 API Key；重启失败时会恢复 OAuth 文件并再次启动原账号。"}
        />
        {deleting && accounts.length <= 1 ? <Alert className="account-editor-note" type="error" showIcon message="不能删除最后一个业务账号" /> : null}
        {deleting && fallbackOptions.length ? (
          <Controller
            control={form.control}
            name="fallback_account"
            render={({ field, fieldState }) => (
              <Form.Item
                label="备用账号"
                validateStatus={fieldState.error ? "error" : undefined}
                help={fieldState.error?.message ?? `将迁移 ${account.routed_users} 个已路由用户。`}
              >
                <WideSelect
                  {...field}
                  aria-label="删除备用账号"
                  options={fallbackOptions}
                  placeholder="选择已启用账号"
                />
              </Form.Item>
            )}
          />
        ) : null}
        {deleting ? (
          <Controller
            control={form.control}
            name="revoke_keys"
            render={({ field }) => (
              <Form.Item extra="仅当某些有效 Key 只存在于该账号时需要启用；启用后这些独占 Key 会立即失效。">
                <Checkbox checked={field.value} onChange={(event) => field.onChange(event.target.checked)}>
                  同意停用无法保留的独占 API Key
                </Checkbox>
              </Form.Item>
            )}
          />
        ) : null}
        <DestructiveInput control={form.control} name="confirm" label={`输入 ${account.id} 确认`} placeholder={account.id} />
        {error ? <MutationError error={error} title={deleting ? "账号删除未执行" : "OAuth 清理未执行"} /> : null}
      </Form>
    </Modal>
  );
}

function EditorInput({
  control,
  name,
  label,
  placeholder
}: {
  control: ReturnType<typeof useForm<AccountEditorValues>>["control"];
  name: "id" | "email" | "confirm";
  label: string;
  placeholder?: string;
}) {
  return (
    <Controller
      control={control}
      name={name}
      render={({ field, fieldState }) => (
        <Form.Item label={label} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
          <Input {...field} aria-label={label} placeholder={placeholder} autoComplete="off" />
        </Form.Item>
      )}
    />
  );
}

function DestructiveInput({
  control,
  name,
  label,
  placeholder
}: {
  control: ReturnType<typeof useForm<DestructiveValues>>["control"];
  name: "confirm";
  label: string;
  placeholder?: string;
}) {
  return (
    <Controller
      control={control}
      name={name}
      render={({ field, fieldState }) => (
        <Form.Item label={label} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
          <Input {...field} aria-label={label} placeholder={placeholder} autoComplete="off" />
        </Form.Item>
      )}
    />
  );
}

function MutationError({ error, title }: { error: unknown; title: string }) {
  return (
    <Alert
      type="error"
      showIcon
      message={title}
      description={error instanceof ApiError || error instanceof Error ? error.message : "请求失败，请稍后重试"}
    />
  );
}

function AccountStatus({ account }: { account: Account }) {
  if (!account.enabled) return <Tag>已停用</Tag>;
  if (!account.state_available) return <Tag>状态未知</Tag>;
  if (account.account_state.exhausted) return <Tag color="error">额度耗尽</Tag>;
  if (runtimeStateReasons.has(account.account_state.reason)) {
    return (
      <Tag color={account.account_state.reason === "credential_unavailable" ? "error" : "warning"}>
        {stateLabels[account.account_state.reason]}
      </Tag>
    );
  }
  if (account.account_state.eligible) return <Tag color="success">可接收迁入</Tag>;
  const state = stateLabels[account.account_state.reason] ?? "暂不可迁入";
  return <Tag color={account.account_state.reason === "quota_stale" ? "warning" : "default"}>{state}</Tag>;
}

function RebalanceSummary({ result }: { result: RebalanceResponse }) {
  const destinations = Object.entries(result.rebalance.destinations);
  return (
    <Space orientation="vertical" size={4}>
      <Text>迁移用户：{result.rebalance.moved_users}</Text>
      {destinations.length ? <Text>迁入分布：{destinations.map(([account, count]) => `${account} ${count}`).join("，")}</Text> : null}
      {result.rebalance.snapshot_generation ? (
        <Text type="secondary"><SafetyCertificateOutlined aria-hidden="true" /> 鉴权快照 {result.rebalance.snapshot_generation.slice(0, 12)}</Text>
      ) : null}
      {result.rebalance.warning ? <Text type="warning">{result.rebalance.warning}</Text> : null}
    </Space>
  );
}

function AccountPageSkeleton() {
  return (
    <section className="page-content account-page" aria-label="正在加载账号数据">
      <Skeleton active paragraph={{ rows: 2 }} />
      <Row gutter={[16, 16]} className="account-stat-grid">
        {[0, 1, 2].map((item) => <Col xs={24} sm={8} key={item}><Card loading /></Col>)}
      </Row>
      <Card><Skeleton active paragraph={{ rows: 8 }} /></Card>
    </section>
  );
}

function emptyAccountEditorValues(): AccountEditorValues {
  return {
    id: "",
    email: "",
    proxy_mode: "inherit",
    proxy_url: "",
    group_enabled: true,
    default_group: false,
    fallback_account: "",
    confirm: "",
    clear_proxy: false
  };
}

function accountEditorValues(account: Account): AccountEditorValues {
  return {
    ...emptyAccountEditorValues(),
    id: account.id,
    email: account.email,
    proxy_mode: proxyModeSchema.safeParse(account.proxy_mode).success
      ? account.proxy_mode as AccountEditorValues["proxy_mode"]
      : "inherit",
    group_enabled: account.enabled,
    default_group: account.default
  };
}

function validProxyURL(value: string) {
  try {
    const parsed = new URL(value);
    return ["http:", "https:", "socks5:"].includes(parsed.protocol) &&
      Boolean(parsed.hostname) &&
      (parsed.pathname === "" || parsed.pathname === "/") &&
      !parsed.search && !parsed.hash && !/\s/.test(value);
  } catch {
    return false;
  }
}

const stateLabels: Record<string, string> = {
	credential_unavailable: "凭据不可用",
	transient_cooldown: "临时冷却",
	rate_limited: "限流中",
	degraded: "近期异常",
	runtime_unknown: "原生状态未知",
  quota_stale: "额度数据过期",
  quota_unavailable: "额度状态未知",
  reserve_reached: "达到安全余量",
  oauth_missing: "OAuth 未配置",
  container_not_running: "服务未运行",
  upstream_disallowed: "上游暂不可用",
  account_disabled: "已停用"
};

const runtimeStateReasons = new Set([
  "credential_unavailable",
  "transient_cooldown",
  "rate_limited",
  "degraded",
  "runtime_unknown"
]);

const proxyModeLabel: Record<string, string> = {
  inherit: "继承默认",
  custom: "独立代理",
  direct: "直连"
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
