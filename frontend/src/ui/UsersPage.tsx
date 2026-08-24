import { zodResolver } from "@hookform/resolvers/zod";
import {
  Alert,
  Button,
  Card,
  Checkbox,
  Descriptions,
  Dropdown,
  Form,
  Input,
  InputNumber,
  List,
  Modal,
  Radio,
  Result,
  Space,
  Spin,
  Tag,
  Typography,
  type TableColumnsType,
  type TablePaginationConfig
} from "antd";
import {
  BarChartOutlined,
  CopyOutlined,
  DeleteOutlined,
  DashboardOutlined,
  KeyOutlined,
  LockOutlined,
  MoreOutlined,
  PlusOutlined,
  ReloadOutlined,
  StopOutlined,
  TeamOutlined,
  UserOutlined
} from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Controller, useForm, useWatch } from "react-hook-form";
import { useEffect, useMemo, useState } from "react";
import { z } from "zod";

import { ApiError } from "../api/client";
import { listTeams, teamsQueryKey } from "../api/teams";
import {
  assignUserTeam,
  assignUsersTeam,
  clearUserQuota,
  createUser,
  deleteUser,
  listUsers,
  readUserQuota,
  resetUserPassword,
  revokeUser,
  rotateUserKey,
  updateUserQuota,
  userQuotaQueryKey,
  usersQueryKey,
  usersQueryRoot,
  type UserListParams,
  type UserQuotaMode,
  type UserQuotaResult,
  type UserSummary
} from "../api/users";
import { AdminTable } from "./components/AdminTable";
import { PageState } from "./components/PageState";
import { PageToolbar } from "./components/PageToolbar";
import { TokenValue } from "./components/TokenValue";
import { WideSelect } from "./components/WideSelect";
import { UsageBreakdownDrawer } from "./UsageBreakdownDrawer";
import { formatTokens } from "./formatters";
import { useDebouncedValue } from "./hooks/useDebouncedValue";

const { Paragraph, Text } = Typography;

type TeamAssignment = {
  users: string[];
  targetTeamId: string | null;
  expectedTeamId?: string | null;
};

type LifecycleAction = {
  kind: "rotate" | "reset-password" | "revoke" | "delete";
  user: UserSummary;
};

type SecretReveal = {
  title: string;
  apiKey?: string;
  password?: string;
};

export function UsersPage({ csrfToken }: { csrfToken: string }) {
  const queryClient = useQueryClient();
  const [searchDraft, setSearchDraft] = useState("");
  const [query, setQuery] = useState("");
  const [teamId, setTeamID] = useState("");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(50);
  const [selectedUsers, setSelectedUsers] = useState<string[]>([]);
  const [assignment, setAssignment] = useState<TeamAssignment | null>(null);
  const [notice, setNotice] = useState("");
  const [usageUser, setUsageUser] = useState<string | null>(null);
  const [quotaUser, setQuotaUser] = useState<string | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [lifecycleAction, setLifecycleAction] = useState<LifecycleAction | null>(null);
  const [deleteConfirmation, setDeleteConfirmation] = useState("");
  const [revokeOnDelete, setRevokeOnDelete] = useState(false);
  const [secretReveal, setSecretReveal] = useState<SecretReveal | null>(null);
  const debouncedSearch = useDebouncedValue(searchDraft.trim(), 300);

  useEffect(() => {
    if (debouncedSearch === query) return;
    setQuery(debouncedSearch);
    setPage(1);
  }, [debouncedSearch, query]);

  const params = useMemo<UserListParams>(
    () => ({ query, teamId, page, pageSize }),
    [page, pageSize, query, teamId]
  );
  const users = useQuery({
    queryKey: usersQueryKey(params),
    queryFn: ({ signal }) => listUsers(params, signal)
  });
  const teams = useQuery({
    queryKey: teamsQueryKey,
    queryFn: ({ signal }) => listTeams(signal)
  });
  const assignMutation = useMutation({
    mutationFn: async (input: TeamAssignment) => {
      if (input.users.length === 1 && input.expectedTeamId !== undefined) {
        return assignUserTeam(
          input.users[0],
          input.targetTeamId,
          input.expectedTeamId,
          csrfToken
        );
      }
      return assignUsersTeam(input.users, input.targetTeamId, csrfToken);
    },
    onSuccess: async (result) => {
      setAssignment(null);
      setSelectedUsers([]);
      setNotice(result.message);
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: usersQueryRoot }),
        queryClient.invalidateQueries({ queryKey: teamsQueryKey, exact: true })
      ]);
    }
  });
  const createMutation = useMutation({
    mutationFn: (input: { email: string; teamId: string | null }) =>
      createUser(input.email, input.teamId, csrfToken),
    gcTime: 0,
    onSuccess: async (result) => {
      setCreateOpen(false);
      setNotice(result.message);
      setSecretReveal({
        title: `已创建 ${result.user.user}`,
        apiKey: result.user.api_key,
        password: result.user.initial_password
      });
      createMutation.reset();
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: usersQueryRoot }),
        queryClient.invalidateQueries({ queryKey: teamsQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: ["overview"], exact: false })
      ]);
    }
  });
  const lifecycleMutation = useMutation({
    mutationFn: async (action: LifecycleAction) => {
      switch (action.kind) {
        case "rotate": {
          const result = await rotateUserKey(action.user.email, csrfToken);
          return { message: result.message, apiKey: result.key.api_key };
        }
        case "reset-password": {
          const result = await resetUserPassword(action.user.email, csrfToken);
          return { message: result.message, password: result.password.initial_password };
        }
        case "revoke":
          return revokeUser(action.user.email, csrfToken);
        case "delete":
          return deleteUser(action.user.email, revokeOnDelete, csrfToken);
      }
    },
    gcTime: 0,
    onSuccess: async (result, action) => {
      setNotice(result.message);
      if ("apiKey" in result && result.apiKey) {
        setSecretReveal({ title: `已轮换 ${action.user.email}`, apiKey: result.apiKey });
      } else if ("password" in result && result.password) {
        setSecretReveal({ title: `已重置 ${action.user.email}`, password: result.password });
      }
      setLifecycleAction(null);
      setDeleteConfirmation("");
      setRevokeOnDelete(false);
      lifecycleMutation.reset();
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: usersQueryRoot }),
        queryClient.invalidateQueries({ queryKey: teamsQueryKey, exact: true }),
        queryClient.invalidateQueries({ queryKey: ["overview"], exact: false })
      ]);
    }
  });
  const resetAssignment = assignMutation.reset;
  const resetLifecycle = lifecycleMutation.reset;
  const columns = useMemo(() => userColumns({
    onOpenUsage: setUsageUser,
    onAdjustTeam: (user) => {
      resetAssignment();
      setAssignment({
        users: [user.email],
        targetTeamId: user.team_id,
        expectedTeamId: user.team_id
      });
    },
    onManage: (key, user) => {
      if (key === "quota") {
        setQuotaUser(user.email);
        return;
      }
      setDeleteConfirmation("");
      setRevokeOnDelete(false);
      resetLifecycle();
      setLifecycleAction({ kind: key, user });
    }
  }), [resetAssignment, resetLifecycle]);

  if (users.isPending || teams.isPending) {
    return <UsersPageSkeleton />;
  }
  if (users.isError || teams.isError) {
    const error = users.error ?? teams.error;
    return (
      <section className="page-content">
        <PageState
          kind="error"
          title="用户目录加载失败"
          detail={error instanceof Error ? error.message : "请稍后重试"}
          onAction={() => void Promise.all([users.refetch(), teams.refetch()])}
        />
      </section>
    );
  }

  const teamOptions = [
    { value: "unassigned", label: "未分配团队" },
    ...teams.data.teams.map((team) => ({ value: team.id, label: team.name }))
  ];

  const pagination: TablePaginationConfig = {
    current: users.data.pagination.page,
    pageSize: users.data.pagination.page_size,
    total: users.data.pagination.total,
    showSizeChanger: true,
    pageSizeOptions: [25, 50, 100],
    showTotal: (total) => `共 ${total} 位用户`
  };

  return (
    <section className="page-content user-page">
      <PageToolbar
        className="account-page-intro"
        description="初始只读取控制面用户目录、路由和团队归属；Token 用量仅在点击单个用户的“用量”后查询。"
        actions={(
          <>
          <Button
            type="primary"
            icon={<PlusOutlined aria-hidden="true" />}
            onClick={() => {
              createMutation.reset();
              setCreateOpen(true);
            }}
          >
            新增用户
          </Button>
          <Button
            icon={<ReloadOutlined aria-hidden="true" />}
            loading={users.isFetching || teams.isFetching}
            onClick={() => void Promise.all([users.refetch(), teams.refetch()])}
          >
            刷新当前页
          </Button>
          <Button
            icon={<TeamOutlined aria-hidden="true" />}
            disabled={selectedUsers.length === 0}
            onClick={() => {
              assignMutation.reset();
              setAssignment({ users: selectedUsers, targetTeamId: null });
            }}
          >
            批量分配团队{selectedUsers.length ? `（${selectedUsers.length}）` : ""}
          </Button>
          </>
        )}
      />

      {notice ? <Alert className="page-alert" type="success" showIcon closable message={notice} onClose={() => setNotice("")} /> : null}

      <Card className="account-table-card" title="用户目录" extra={<Text type="secondary">数据时间：{formatTimestamp(users.data.generated_at)}</Text>}>
        <Space wrap className="user-filters">
          <Input.Search
            allowClear
            value={searchDraft}
            placeholder="搜索邮箱或团队"
            aria-label="搜索用户"
            onChange={(event) => setSearchDraft(event.target.value)}
            onSearch={(value) => {
              setQuery(value.trim());
              setPage(1);
            }}
          />
          <WideSelect
            value={teamId || "all"}
            aria-label="按团队筛选"
            options={[{ value: "all", label: "全部团队" }, ...teamOptions]}
            onChange={(value) => {
              setTeamID(value === "all" ? "" : value);
              setPage(1);
            }}
          />
        </Space>
        <AdminTable<UserSummary>
          rowKey="email"
          columns={columns}
          dataSource={users.data.users}
          loading={users.isFetching}
          rowSelection={{
            selectedRowKeys: selectedUsers,
            preserveSelectedRowKeys: false,
            onChange: (keys) => setSelectedUsers(keys.map(String))
          }}
          pagination={pagination}
          minWidth={1160}
          emptyText="当前条件下没有用户"
          onChange={(next) => {
            setPage(next.current ?? 1);
            setPageSize(next.pageSize ?? 50);
          }}
        />
      </Card>

      <Modal
        title={assignment && assignment.users.length > 1 ? `批量分配 ${assignment.users.length} 位用户` : "调整用户团队"}
        open={assignment !== null}
        okText="保存团队归属"
        cancelText="取消"
        confirmLoading={assignMutation.isPending}
        onCancel={() => !assignMutation.isPending && setAssignment(null)}
        onOk={() => assignment && assignMutation.mutate(assignment)}
        destroyOnHidden
      >
        <Space orientation="vertical" size={16} className="team-assignment-form">
          <Paragraph>
            团队报表按当前成员动态聚合；修改归属不会改写历史用量事件。
          </Paragraph>
          <WideSelect
            value={assignment?.targetTeamId ?? "unassigned"}
            aria-label="目标团队"
            options={teamOptions}
            onChange={(value) => setAssignment((current) => current ? {
              ...current,
              targetTeamId: value === "unassigned" ? null : value
            } : null)}
          />
          {assignMutation.isError ? (
            <Alert
              type="error"
              showIcon
              message="团队归属保存失败"
              description={assignMutation.error instanceof ApiError ? assignMutation.error.message : "请刷新后重试"}
            />
          ) : null}
        </Space>
      </Modal>
      <CreateUserModal
        open={createOpen}
        teams={teamOptions}
        pending={createMutation.isPending}
        error={createMutation.error}
        onCancel={() => !createMutation.isPending && setCreateOpen(false)}
        onSubmit={(input) => createMutation.mutate(input)}
      />
      <Modal
        title={lifecycleAction ? lifecycleTitle(lifecycleAction) : "用户操作"}
        open={lifecycleAction !== null}
        okText={lifecycleAction?.kind === "delete" ? "确认删除" : "确认执行"}
        cancelText="取消"
        okButtonProps={{
          danger: lifecycleAction?.kind === "delete" || lifecycleAction?.kind === "revoke",
          disabled: lifecycleAction?.kind === "delete" && (
            deleteConfirmation !== lifecycleAction.user.email ||
            (lifecycleAction.user.status === "active" && !revokeOnDelete)
          )
        }}
        confirmLoading={lifecycleMutation.isPending}
        onCancel={() => {
          if (lifecycleMutation.isPending) return;
          setLifecycleAction(null);
          setDeleteConfirmation("");
          setRevokeOnDelete(false);
          lifecycleMutation.reset();
        }}
        onOk={() => lifecycleAction && lifecycleMutation.mutate(lifecycleAction)}
        destroyOnHidden
      >
        {lifecycleAction ? (
          <Space orientation="vertical" size={16} className="team-assignment-form">
            <Alert
              type={lifecycleAction.kind === "reset-password" ? "warning" : "error"}
              showIcon
              message={lifecycleDescription(lifecycleAction)}
            />
            {lifecycleAction.kind === "delete" ? (
              <>
                <Form.Item label={`输入 ${lifecycleAction.user.email} 确认删除`}>
                  <Input
                    value={deleteConfirmation}
                    aria-label="删除用户确认邮箱"
                    autoComplete="off"
                    onChange={(event) => setDeleteConfirmation(event.target.value)}
                  />
                </Form.Item>
                {lifecycleAction.user.status === "active" ? (
                  <Checkbox checked={revokeOnDelete} onChange={(event) => setRevokeOnDelete(event.target.checked)}>
                    同时停用当前有效 API Key
                  </Checkbox>
                ) : null}
              </>
            ) : null}
            {lifecycleMutation.isError ? (
              <Alert
                type="error"
                showIcon
                message="操作未完成"
                description={lifecycleMutation.error instanceof ApiError ? lifecycleMutation.error.message : "请刷新后重试"}
              />
            ) : null}
          </Space>
        ) : null}
      </Modal>
      <Modal
        title={secretReveal?.title ?? "一次性凭据"}
        open={secretReveal !== null}
        closable={false}
        mask={{ closable: false }}
        keyboard={false}
        footer={[
          <Button key="done" type="primary" onClick={() => setSecretReveal(null)}>我已安全保存</Button>
        ]}
        destroyOnHidden
      >
        <Alert
          type="warning"
          showIcon
          message="以下凭据关闭后不会再次显示。请立即保存到安全位置，不要通过聊天或工单传递。"
        />
        {secretReveal?.apiKey ? <SecretField label="API Key" value={secretReveal.apiKey} /> : null}
        {secretReveal?.password ? <SecretField label="初始密码" value={secretReveal.password} /> : null}
      </Modal>
      <UsageBreakdownDrawer
        kind="user"
        subject={usageUser}
        onClose={() => setUsageUser(null)}
      />
      {quotaUser ? (
        <UserQuotaModal
          user={quotaUser}
          csrfToken={csrfToken}
          onClose={() => setQuotaUser(null)}
          onSaved={(message) => setNotice(message)}
        />
      ) : null}
    </section>
  );
}

function userColumns({
  onOpenUsage,
  onAdjustTeam,
  onManage
}: {
  onOpenUsage: (email: string) => void;
  onAdjustTeam: (user: UserSummary) => void;
  onManage: (key: "quota" | LifecycleAction["kind"], user: UserSummary) => void;
}): TableColumnsType<UserSummary> {
  return [
    {
      title: "用户",
      dataIndex: "email",
      fixed: "left",
      width: 260,
      render: (email: string) => (
        <Space>
          <UserOutlined aria-hidden="true" />
          <Text strong>{email}</Text>
        </Space>
      )
    },
    {
      title: "状态",
      dataIndex: "status",
      align: "center",
      width: 100,
      render: (status: UserSummary["status"]) =>
        status === "active" ? <Tag color="success">有效</Tag> : <Tag>已停用</Tag>
    },
    { title: "有效账号", dataIndex: "active_accounts", align: "center", width: 110 },
    {
      title: "当前路由",
      dataIndex: "route_account_id",
      align: "center",
      width: 150,
      render: (route: string | null) => route ?? <Text type="secondary">未分配</Text>
    },
    {
      title: "团队",
      align: "center",
      width: 170,
      render: (_, user) => user.team?.name ?? <Text type="secondary">未分配</Text>
    },
    { title: "最近更新", dataIndex: "updated_at", align: "center", width: 170, render: formatTimestamp },
    {
      title: "操作",
      fixed: "right",
      align: "center",
      width: 260,
      render: (_, user) => (
        <Space size={0}>
          <Button
            type="link"
            icon={<BarChartOutlined aria-hidden="true" />}
            aria-label={`查看 ${user.email} 的用量`}
            onClick={() => onOpenUsage(user.email)}
          >
            用量
          </Button>
          <Button type="link" aria-label={`调整 ${user.email} 的团队`} onClick={() => onAdjustTeam(user)}>
            调整团队
          </Button>
          <Dropdown
            trigger={["click"]}
            menu={{
              items: [
                { key: "quota", icon: <DashboardOutlined />, label: "额度策略" },
                { key: "rotate", icon: <KeyOutlined />, label: "轮换 API Key", disabled: user.status !== "active" },
                { key: "reset-password", icon: <LockOutlined />, label: "重置密码" },
                { key: "revoke", icon: <StopOutlined />, label: "停用 API Key", disabled: user.status !== "active", danger: true },
                { type: "divider" },
                { key: "delete", icon: <DeleteOutlined />, label: "删除用户", danger: true }
              ],
              onClick: ({ key }) => onManage(key as "quota" | LifecycleAction["kind"], user)
            }}
          >
            <Button type="link" icon={<MoreOutlined aria-hidden="true" />} aria-label={`管理 ${user.email}`}>
              更多
            </Button>
          </Dropdown>
        </Space>
      )
    }
  ];
}

const userQuotaSchema = z.object({
  mode: z.enum(["inherit", "unlimited", "custom"]),
  weeklyTokens: z.number().int("请输入整数").min(1, "额度至少为 1 Token").max(1_000_000_000_000, "额度不能超过 1 万亿 Token").nullable()
}).superRefine((value, context) => {
  if (value.mode === "custom" && value.weeklyTokens === null) {
    context.addIssue({
      code: "custom",
      path: ["weeklyTokens"],
      message: "请输入自定义周额度"
    });
  }
});

type UserQuotaForm = z.infer<typeof userQuotaSchema>;

function UserQuotaModal({
  user,
  csrfToken,
  onClose,
  onSaved
}: {
  user: string;
  csrfToken: string;
  onClose: () => void;
  onSaved: (message: string) => void;
}) {
  const queryClient = useQueryClient();
  const quotaKey = userQuotaQueryKey(user);
  const quota = useQuery({
    queryKey: quotaKey,
    queryFn: ({ signal }) => readUserQuota(user, signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnMount: "always"
  });
  const form = useForm<UserQuotaForm>({
    resolver: zodResolver(userQuotaSchema),
    defaultValues: { mode: "inherit", weeklyTokens: null }
  });
  const selectedMode = useWatch({ control: form.control, name: "mode" });
  useEffect(() => {
    if (!quota.data) return;
    form.reset({
      mode: quota.data.weekly_quota.policy_mode,
      weeklyTokens: quota.data.weekly_quota.policy_tokens
    });
  }, [form, quota.data]);

  const finish = (result: UserQuotaResult) => {
    onSaved(result.message ?? "用户周额度策略已更新");
    queryClient.removeQueries({ queryKey: quotaKey, exact: true });
    onClose();
  };
  const updateMutation = useMutation({
    mutationFn: (input: UserQuotaForm) => updateUserQuota(
      user,
      input.mode,
      input.mode === "custom" ? input.weeklyTokens : null,
      csrfToken
    ),
    gcTime: 0,
    onSuccess: (result) => {
      updateMutation.reset();
      finish(result);
    }
  });
  const clearMutation = useMutation({
    mutationFn: () => clearUserQuota(user, csrfToken),
    gcTime: 0,
    onSuccess: (result) => {
      clearMutation.reset();
      finish(result);
    }
  });
  const pending = updateMutation.isPending || clearMutation.isPending;

  return (
    <Modal
      title={`额度策略 · ${user}`}
      open
      width={620}
      okText="保存额度策略"
      cancelText="取消"
      confirmLoading={updateMutation.isPending}
      okButtonProps={{ disabled: quota.isPending || quota.isError || pending }}
      cancelButtonProps={{ disabled: pending }}
      onCancel={() => !pending && onClose()}
      onOk={() => void form.handleSubmit((values) => updateMutation.mutate(values))()}
      destroyOnHidden
    >
      {quota.isPending ? (
        <div className="quota-loading"><Spin tip="正在读取当前自然周额度" /></div>
      ) : quota.isError ? (
        <Result
          status="warning"
          title="额度策略加载失败"
          subTitle={quota.error instanceof ApiError ? quota.error.message : "请稍后重试"}
          extra={<Button onClick={() => void quota.refetch()}>重新加载</Button>}
        />
      ) : quota.data ? (
        <Space orientation="vertical" size={18} className="quota-policy-form">
          <Descriptions
            size="small"
            column={2}
            items={quotaDescriptionItems(quota.data)}
          />
          <div>
            <Text strong>本周额度调整</Text>
            {quota.data.adjustments.length ? (
              <List
                size="small"
                dataSource={quota.data.adjustments}
                renderItem={(item) => (
                  <List.Item>
                    <List.Item.Meta
                      title={item.action === "bonus" ? `追加 ${formatTokens(item.token_amount)}` : `重置 ${formatTokens(item.token_amount)} 用量`}
                      description={`${item.reason} · ${item.created_by} · ${formatTimestamp(item.created_at)}`}
                    />
                  </List.Item>
                )}
              />
            ) : (
              <Paragraph type="secondary" className="quota-empty-history">本周暂无额度调整记录</Paragraph>
            )}
          </div>
          <Alert
            type="info"
            showIcon
            message="额度按加权 Token 统计；保存后由额度采集器生成新快照并发布到 Gateway。"
          />
          <Controller
            control={form.control}
            name="mode"
            render={({ field }) => (
              <Form.Item label="策略模式">
                <Radio.Group
                  {...field}
                  aria-label="额度策略模式"
                  options={[
                    { value: "inherit", label: "继承组织默认" },
                    { value: "unlimited", label: "不限额" },
                    { value: "custom", label: "自定义" }
                  ]}
                />
              </Form.Item>
            )}
          />
          {selectedMode === "custom" ? (
            <Controller
              control={form.control}
              name="weeklyTokens"
              render={({ field, fieldState }) => (
                <Form.Item
                  label="每周 Token"
                  validateStatus={fieldState.error ? "error" : undefined}
                  help={fieldState.error?.message}
                >
                  <InputNumber
                    value={field.value}
                    min={1}
                    max={1_000_000_000_000}
                    precision={0}
                    controls={false}
                    aria-label="自定义每周 Token"
                    onBlur={field.onBlur}
                    onChange={(value) => field.onChange(typeof value === "number" ? value : null)}
                  />
                </Form.Item>
              )}
            />
          ) : null}
          {quota.data.weekly_quota.policy_mode !== "inherit" ? (
            <Button
              disabled={pending}
              loading={clearMutation.isPending}
              onClick={() => clearMutation.mutate()}
            >
              恢复继承组织默认
            </Button>
          ) : null}
          {updateMutation.isError || clearMutation.isError ? (
            <Alert
              type="error"
              showIcon
              message="额度策略保存失败"
              description={quotaMutationMessage(updateMutation.error ?? clearMutation.error)}
            />
          ) : null}
        </Space>
      ) : null}
    </Modal>
  );
}

function quotaDescriptionItems(result: UserQuotaResult) {
  const current = result.weekly_quota;
  return [
    { key: "mode", label: "当前策略", children: quotaModeLabel(current.policy_mode) },
    { key: "timezone", label: "自然周时区", children: current.timezone },
    { key: "used", label: "本周已用", children: <TokenValue value={current.used_tokens} suffix="加权 Token" /> },
    {
      key: "remaining",
      label: "本周剩余",
      children: current.remaining_tokens === null ? "不限额" : <TokenValue value={current.remaining_tokens} />
    },
    { key: "week-end", label: "本周结束", children: formatTimestamp(current.week_end_at) },
    {
      key: "reset",
      label: "个人策略周期",
      children: current.personal_policy_reset_enabled ? "下个自然周恢复默认" : "持续有效"
    }
  ];
}

function quotaModeLabel(mode: UserQuotaMode) {
  return { inherit: "继承组织默认", unlimited: "不限额", custom: "自定义" }[mode];
}

function quotaMutationMessage(error: unknown) {
  return error instanceof ApiError ? error.message : "请刷新当前策略后重试";
}

const createUserSchema = z.object({
  email: z.string().trim().email("请输入有效邮箱").max(254, "邮箱过长"),
  teamId: z.string()
});

type CreateUserForm = z.infer<typeof createUserSchema>;

function CreateUserModal({
  open,
  teams,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  open: boolean;
  teams: Array<{ value: string; label: string }>;
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (input: { email: string; teamId: string | null }) => void;
}) {
  const form = useForm<CreateUserForm>({
    resolver: zodResolver(createUserSchema),
    defaultValues: { email: "", teamId: "unassigned" }
  });
  useEffect(() => {
    if (open) form.reset({ email: "", teamId: "unassigned" });
  }, [form, open]);
  return (
    <Modal
      title="新增用户"
      open={open}
      okText="创建并发布 API Key"
      cancelText="取消"
      confirmLoading={pending}
      onCancel={onCancel}
      onOk={() => void form.handleSubmit((values) => onSubmit({
        email: values.email.trim(),
        teamId: values.teamId === "unassigned" ? null : values.teamId
      }))()}
      destroyOnHidden
    >
      <Paragraph>
        服务端会为所有当前账号创建同一个统一 Key，写入初始登录凭据，并等待 Gateway 激活新快照后才返回。
      </Paragraph>
      <Controller
        control={form.control}
        name="email"
        render={({ field, fieldState }) => (
          <Form.Item label="用户邮箱" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
            <Input {...field} aria-label="新增用户邮箱" autoComplete="off" placeholder="name@example.com" />
          </Form.Item>
        )}
      />
      <Controller
        control={form.control}
        name="teamId"
        render={({ field }) => (
          <Form.Item label="团队">
            <WideSelect
              {...field}
              aria-label="新增用户团队"
              options={teams}
            />
          </Form.Item>
        )}
      />
      {error ? (
        <Alert
          type="error"
          showIcon
          message="用户创建失败"
          description={error instanceof ApiError ? error.message : "请检查配置后重试"}
        />
      ) : null}
    </Modal>
  );
}

function SecretField({ label, value }: { label: string; value: string }) {
  return (
    <Form.Item className="one-time-secret" label={label}>
      <Space.Compact block>
        <Input.Password value={value} readOnly aria-label={label} autoComplete="off" />
        <Button
          icon={<CopyOutlined aria-hidden="true" />}
          aria-label={`复制${label}`}
          onClick={() => void copySecret(value)}
        >
          复制
        </Button>
      </Space.Compact>
    </Form.Item>
  );
}

async function copySecret(value: string) {
  if (!navigator.clipboard) return;
  await navigator.clipboard.writeText(value);
}

function lifecycleTitle(action: LifecycleAction) {
  const labels: Record<LifecycleAction["kind"], string> = {
    rotate: "轮换 API Key",
    "reset-password": "重置使用中心密码",
    revoke: "停用用户 API Key",
    delete: "删除用户"
  };
  return `${labels[action.kind]} · ${action.user.email}`;
}

function lifecycleDescription(action: LifecycleAction) {
  switch (action.kind) {
    case "rotate":
      return "新 Key 的 Gateway 快照激活后旧 Key 会立即失效；失败时服务端会恢复旧 Key 和旧快照。";
    case "reset-password":
      return "现有使用中心会话会失效，用户下次登录必须修改初始密码。";
    case "revoke":
      return "Gateway 激活撤销快照后，该用户当前 API Key 将立即不可用。";
    case "delete":
      return "删除当前控制面身份、路由、登录凭据和配额策略；历史用量事件仍保留。";
  }
}

function UsersPageSkeleton() {
  return (
    <section className="page-content" aria-label="正在加载用户目录">
      <div className="skeleton skeleton-title" />
      <div className="skeleton skeleton-line" />
      <div className="skeleton skeleton-table" />
    </section>
  );
}

function formatTimestamp(timestamp: number) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  }).format(new Date(timestamp * 1000));
}
