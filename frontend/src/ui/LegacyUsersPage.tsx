import {
  Button,
  Checkbox,
  Drawer,
  Form,
  Modal,
  Skeleton,
  Space,
  Tag,
  Tooltip,
  Typography,
  type TableColumnsType
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";
import { useNavigate } from "react-router-dom";

import { ApiError } from "../api/client";
import {
  applyUserQuotaAction,
  assignUserTeam,
  assignUsersTeam,
  clearUserQuota,
  createUser,
  deleteUser,
  listUsers,
  readUserDetail,
  readUserQuota,
  resetUserPassword,
  revokeUser,
  rotateUserKey,
  updateUserQuota,
  userDetailQueryKey,
  userQuotaQueryKey,
  usersQueryKey,
  usersQueryRoot,
  type UserAccountDetail,
  type UserDetail,
  type UserListParams,
  type UserOneTimeKey,
  type UserQuotaActionInput,
  type UserQuotaMode,
  type UserQuotaResult,
  type UserSummary,
  type UserWeeklyQuota
} from "../api/users";
import {
  readTeamUsage,
  readTeamUsageBreakdown,
  teamUsageQueryKey,
  type TeamCombinationUsage,
  type TeamUsageBreakdownResponse,
  type TeamUsageRow,
  type TeamUsageSeries
} from "../api/teams";
import {
  readUsageBreakdown,
  usageBreakdownQueryKey,
  usageBreakdownQueryRoot,
  type UsageBreakdown,
  type UsageCombination,
  type UsageRange,
  type UsageWindow
} from "../api/usage";
import { AdminTable } from "./components/AdminTable";
import { NativeTableViewport } from "./components/NativeTableViewport";
import {
  CustomUsageRangeModal,
  formatCustomUsageRange,
  type CustomUsageRange
} from "./components/CustomUsageRangeModal";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { useAdminToolbar } from "./AdminToolbarContext";
import {
  formatTokenAmount,
  tokenInputPresentation,
  tokenReadableText
} from "./formatters";
import { useDebouncedValue } from "./hooks/useDebouncedValue";

const { Paragraph, Text } = Typography;

type UserSortField = "email" | "requests" | "tokens" | "quota" | "last_used";
type SortDirection = "asc" | "desc";
type UserAccountSortField =
  | "account"
  | "status"
  | "requests"
  | "input_tokens"
  | "output_tokens"
  | "reasoning_tokens"
  | "cached_tokens"
  | "total_tokens"
  | "weighted_tokens"
  | "last_used_at";
type BreakdownSortField =
  | "account"
  | "combination"
  | "success_count"
  | "share"
  | "total_tokens"
  | "multiplier"
  | "weighted_tokens"
  | "average_total"
  | "last_used_at";
type TeamAssignment = {
  users: string[];
  targetTeamID: string | null;
};
type LifecycleAction = {
  kind: "rotate" | "reset-password" | "revoke" | "delete";
  user: UserSummary;
  keyLabel?: string;
};
type SecretReveal = {
  message: string;
  keys: UserOneTimeKey[];
  password?: string;
  passwordUser?: string;
};
type QuotaActionDraft = {
  action: "add_bonus" | "reset_usage";
  scope: "selected" | "all";
  users: string[];
};

const usageWindowOptions: Array<{ value: UsageWindow; label: string }> = [
  { value: "3600", label: "1 小时" },
  { value: "today", label: "今日" },
  { value: "86400", label: "24 小时" },
  { value: "604800", label: "7 天" },
  { value: "2592000", label: "30 天" },
  { value: "all", label: "全部" },
  { value: "custom", label: "自定义…" }
];

export function LegacyUsersPage({ csrfToken }: { csrfToken: string }) {
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const reportedError = useRef<unknown>(null);
  const [searchDraft, setSearchDraft] = useState("");
  const [query, setQuery] = useState("");
  const [teamID, setTeamID] = useState("");
  const [usageWindow, setUsageWindow] = useState<UsageWindow>("today");
  const [customRange, setCustomRange] = useState<CustomUsageRange | null>(null);
  const [customRangeOpen, setCustomRangeOpen] = useState(false);
  const [sortField, setSortField] = useState<UserSortField>("tokens");
  const [sortDirection, setSortDirection] = useState<SortDirection>("desc");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(50);
  const [selectedUsers, setSelectedUsers] = useState<string[]>([]);
  const [expandedUsers, setExpandedUsers] = useState<string[]>([]);
  const [assignment, setAssignment] = useState<TeamAssignment | null>(null);
  const [createOpen, setCreateOpen] = useState(false);
  const [quotaUser, setQuotaUser] = useState<string | null>(null);
  const [lifecycleAction, setLifecycleAction] = useState<LifecycleAction | null>(null);
  const [secretReveal, setSecretReveal] = useState<SecretReveal | null>(null);
  const [quotaAction, setQuotaAction] = useState<QuotaActionDraft | null>(null);
  const [restoreQuotaUsers, setRestoreQuotaUsers] = useState<string[] | null>(null);
  const [teamUsageOpen, setTeamUsageOpen] = useState(false);
  const lifecycleSubmitRef = useRef(false);
  const debouncedSearch = useDebouncedValue(searchDraft.trim(), 250);

  useEffect(() => {
    if (debouncedSearch === query) return;
    setQuery(debouncedSearch);
    setPage(1);
    setExpandedUsers([]);
  }, [debouncedSearch, query]);

  const usageRange = useMemo<UsageRange>(() => ({
    window: usageWindow,
    startAt: usageWindow === "custom" ? customRange?.startAt : undefined,
    endAt: usageWindow === "custom" ? customRange?.endAt : undefined
  }), [customRange?.endAt, customRange?.startAt, usageWindow]);
  const listParams = useMemo<UserListParams>(() => ({
    query,
    teamId: teamID,
    usageState: "all",
    window: String(usageWindow),
    startAt: usageRange.startAt,
    endAt: usageRange.endAt,
    sort: sortField,
    direction: sortDirection,
    page,
    pageSize
  }), [
    page,
    pageSize,
    query,
    sortDirection,
    sortField,
    teamID,
    usageRange.endAt,
    usageRange.startAt,
    usageWindow
  ]);
  const users = useQuery({
    queryKey: usersQueryKey(listParams),
    queryFn: ({ signal }) => listUsers(listParams, signal),
    enabled: usageWindow !== "custom" || customRange !== null,
    placeholderData: (previous) => previous,
    retry: false,
    refetchOnWindowFocus: false
  });
  const teamUsage = useQuery({
    queryKey: teamUsageQueryKey(usageRange),
    queryFn: ({ signal }) => readTeamUsage(usageRange, signal),
    enabled: usageWindow !== "custom" || customRange !== null,
    placeholderData: (previous) => previous,
    retry: false,
    refetchOnWindowFocus: false
  });

  const refreshUsers = useCallback(async () => {
    try {
      const [catalog, teamCatalog] = await Promise.all([
        listUsers({ ...listParams, fresh: true }),
        readTeamUsage({ ...usageRange, fresh: true })
      ]);
      queryClient.setQueryData(usersQueryKey(listParams), catalog);
      queryClient.setQueryData(teamUsageQueryKey(usageRange), teamCatalog);
      await Promise.all([
        queryClient.refetchQueries({ queryKey: [...usersQueryRoot, "detail"], type: "active" }),
        queryClient.refetchQueries({ queryKey: [...usageBreakdownQueryRoot, "user"], type: "active" })
      ]);
      setRefreshLabel(userRefreshLabel(catalog.summary_generated_at || catalog.generated_at, catalog.summary_cached));
      showToast("数据已刷新");
    } catch (error) {
      setRefreshLabel("刷新失败");
      throw error;
    }
  }, [listParams, queryClient, setRefreshLabel, showToast, usageRange]);

  useEffect(() => {
    setRefreshAction(refreshUsers);
    return () => setRefreshAction(null);
  }, [refreshUsers, setRefreshAction]);
  useEffect(() => setRefreshing(users.isFetching), [setRefreshing, users.isFetching]);
  useEffect(() => {
    if (!users.data) return;
    reportedError.current = null;
    setRefreshLabel(userRefreshLabel(users.data.summary_generated_at || users.data.generated_at, users.data.summary_cached));
  }, [setRefreshLabel, users.data]);
  useEffect(() => {
    if (!users.isError || reportedError.current === users.error) return;
    reportedError.current = users.error;
    setRefreshLabel("刷新失败");
    showToast(errorMessage(users.error), "error");
  }, [setRefreshLabel, showToast, users.error, users.isError]);
  useEffect(() => () => {
    setRefreshing(false);
    setRefreshLabel("");
  }, [setRefreshLabel, setRefreshing]);

  const refreshAfterMutation = useCallback(async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: usersQueryRoot }),
      queryClient.invalidateQueries({ queryKey: ["teams"] }),
      queryClient.invalidateQueries({ queryKey: ["overview"] })
    ]);
  }, [queryClient]);

  const assignmentMutation = useMutation({
    mutationFn: (input: TeamAssignment) => (
      input.users.length === 1
        ? assignUserTeam(input.users[0], input.targetTeamID, csrfToken)
        : assignUsersTeam(input.users, input.targetTeamID, csrfToken)
    ),
    onSuccess: async (result) => {
      setAssignment(null);
      showToast(result.message);
      await refreshAfterMutation();
    }
  });
  const createMutation = useMutation({
    mutationFn: (input: { email: string; teamID: string | null }) => createUser(input.email, input.teamID, csrfToken),
    gcTime: 0,
    onSuccess: async (result, input) => {
      setCreateOpen(false);
      setSecretReveal({
        message: result.message,
        keys: result.keys,
        password: result.initial_password,
        passwordUser: input.email.trim().toLowerCase()
      });
      createMutation.reset();
      await refreshAfterMutation();
    }
  });
  const lifecycleMutation = useMutation({
    mutationFn: async (action: LifecycleAction) => {
      if (action.kind === "rotate") {
        const result = await rotateUserKey(action.keyLabel || "", csrfToken);
        return { kind: action.kind, message: result.message, keys: result.keys };
      }
      if (action.kind === "reset-password") {
        const result = await resetUserPassword(action.user.email, csrfToken);
        return {
          kind: action.kind,
          message: result.message,
          keys: [] as UserOneTimeKey[],
          password: result.initial_password
        };
      }
      if (action.kind === "revoke") {
        const result = await revokeUser(action.user.email, csrfToken);
        return { kind: action.kind, message: result.message, keys: [] as UserOneTimeKey[] };
      }
      const result = await deleteUser(action.user.email, true, csrfToken);
      return { kind: action.kind, message: result.message, keys: [] as UserOneTimeKey[] };
    },
    gcTime: 0,
    onSuccess: async (result, action) => {
      if (result.keys.length || result.password) {
        setSecretReveal({
          message: result.message,
          keys: result.keys,
          password: result.password,
          passwordUser: action.user.email
        });
      } else {
        showToast(result.message);
      }
      lifecycleMutation.reset();
      await refreshAfterMutation();
    },
    onError: (error) => showToast(errorMessage(error), "error"),
    onSettled: () => {
      lifecycleSubmitRef.current = false;
    }
  });
  const quotaActionMutation = useMutation({
    mutationFn: (input: UserQuotaActionInput) => applyUserQuotaAction(input, csrfToken),
    onSuccess: async (result) => {
      setQuotaAction(null);
      setQuotaUser(null);
      setRestoreQuotaUsers(null);
      setSelectedUsers([]);
      showToast(result.message);
      await refreshAfterMutation();
    }
  });

  const catalog = users.data;
  const pageUsers = catalog?.users ?? [];
  useEffect(() => {
    const visible = new Set(pageUsers.map((user) => user.email));
    setSelectedUsers((current) => {
      const next = current.filter((email) => visible.has(email));
      return next.length === current.length && next.every((email, index) => email === current[index])
        ? current
        : next;
    });
  }, [pageUsers]);
  const allSelected = pageUsers.length > 0 && pageUsers.every((user) => selectedUsers.includes(user.email));
  const partiallySelected = !allSelected && pageUsers.some((user) => selectedUsers.includes(user.email));
  const toggleSelected = useCallback((email: string, checked: boolean) => {
    setSelectedUsers((current) => checked
      ? Array.from(new Set([...current, email]))
      : current.filter((item) => item !== email));
  }, []);
  const toggleExpanded = useCallback((email: string) => {
    setExpandedUsers((current) => current.includes(email) ? [] : [email]);
  }, []);
  const changeSort = useCallback((field: UserSortField) => {
    setSortDirection((currentDirection) => (
      sortField === field
        ? (currentDirection === "asc" ? "desc" : "asc")
        : (field === "email" ? "asc" : "desc")
    ));
    setSortField(field);
    setPage(1);
    setExpandedUsers([]);
  }, [sortField]);

  const columns = useMemo(() => userColumns({
    page,
    pageSize,
    selectedUsers,
    allSelected,
    partiallySelected,
    sortField,
    sortDirection,
    usageWindow,
    onSort: changeSort,
    onSelectPage: (checked) => setSelectedUsers(checked ? pageUsers.map((user) => user.email) : []),
    onSelect: toggleSelected,
    onTeam: (user) => setAssignment({
      users: [user.email],
      targetTeamID: user.team_id
    }),
    onQuota: (user) => setQuotaUser(user.email)
  }), [
    allSelected,
    changeSort,
    page,
    pageSize,
    pageUsers,
    partiallySelected,
    selectedUsers,
    sortDirection,
    sortField,
    usageWindow,
    toggleSelected
  ]);
  const teamOptions = [
    { value: "unassigned", label: "未分组" },
    ...(catalog?.teams ?? []).map((team) => ({ value: team.id, label: team.name }))
  ];
  const displayedUsageOptions = usageWindowOptions.map((option) => (
    option.value === "custom" && customRange
      ? { ...option, label: formatCustomUsageRange(customRange) }
      : option
  ));
  const total = catalog?.pagination.total ?? 0;
  const selectedTeamUsage = teamUsage.data?.teams.find((team) => team.id === teamID) ?? null;
  const totalPages = Math.max(1, catalog?.pagination.total_pages ?? 1);
  const startIndex = total ? (page - 1) * pageSize + 1 : 0;
  const endIndex = Math.min(page * pageSize, total);

  return (
    <section className="page-content legacy-user-page">
      <div className="legacy-user-management-panel">
        <div className="management-toolbar user-management-toolbar">
          <label className="search-field user-search-input">
            <span aria-hidden="true">⌕</span>
            <input
              type="search"
              aria-label="搜索用户"
              placeholder="搜索用户邮箱"
              value={searchDraft}
              onChange={(event) => setSearchDraft(event.target.value)}
            />
          </label>
          <div className="user-toolbar-actions management-toolbar-controls">
            <div className="management-filter-grid user-filter-grid">
              <label className="window-field filter-field user-filter-field">
                <span>团队</span>
                <LegacyEnhancedSelect
                  id="user-team-filter"
                  label="团队"
                  value={teamID}
                  options={[{ value: "", label: "全部团队" }, ...teamOptions]}
                  onChange={(value) => {
                    setTeamID(value);
                    setPage(1);
                    setExpandedUsers([]);
                  }}
                />
              </label>
              <label className="window-field filter-field user-filter-field usage-window-field">
                <span>统计范围</span>
                <LegacyEnhancedSelect
                  id="user-usage-window"
                  label="统计范围"
                  value={usageWindow}
                  options={displayedUsageOptions}
                  onChange={(value) => {
                    if (value === "custom") {
                      setCustomRangeOpen(true);
                      return;
                    }
                    setUsageWindow(value);
                    setExpandedUsers([]);
                  }}
                />
              </label>
            </div>
            <div className="management-primary-actions user-primary-actions">
              <Button
                className="team-usage-trigger"
                disabled={!selectedTeamUsage}
                onClick={() => setTeamUsageOpen(true)}
              >
                <span className="team-usage-trigger-icon" aria-hidden="true">↗</span>
                {selectedTeamUsage ? selectedTeamUsage.name + " Token 用量" : "选择团队后查看用量"}
              </Button>
              <Button onClick={() => navigate("/teams")}>进入团队管理</Button>
              <Button type="primary" onClick={() => {
                createMutation.reset();
                setCreateOpen(true);
              }}>添加用户</Button>
            </div>
          </div>
        </div>

        {catalog?.collector.status && catalog.collector.status !== "healthy" ? (
          <div className="notice user-usage-notice" role="status">
            {catalog.collector.status === "starting" ? "用量采集器正在启动" : "用量采集暂不可用，用户管理不受影响"}
          </div>
        ) : null}

        {selectedUsers.length ? (
          <div className="user-selection-bar">
            <div className="user-selection-summary">
              <span className="selection-count">已选择 {selectedUsers.length} 位用户</span>
              <small>批量操作只影响已勾选用户，并保留原始用量和审计记录。</small>
            </div>
            <div className="user-selection-actions">
              <button className="button ghost" type="button" onClick={() => setSelectedUsers([])}>取消选择</button>
              <button className="button secondary" type="button" onClick={() => setAssignment({ users: selectedUsers, targetTeamID: null })}>分配团队</button>
              <button className="button secondary" type="button" onClick={() => {
                quotaActionMutation.reset();
                setRestoreQuotaUsers([...selectedUsers]);
              }}>恢复组织默认</button>
              <button className="button danger-outline" type="button" onClick={() => setQuotaAction({
                action: "reset_usage",
                scope: "selected",
                users: selectedUsers
              })}>清零本周已用量</button>
            </div>
          </div>
        ) : null}

        <div className={"legacy-user-table-state" + (total ? "" : " is-empty")}>
          <AdminTable<UserSummary>
            rowKey="email"
            className="user-legacy-table"
            columns={columns}
            dataSource={pageUsers}
            loading={users.isPending && !catalog}
            minWidth="100%"
            fillAvailable
            tableLayout="fixed"
            size="small"
            pagination={false}
            locale={{ emptyText: <span className="user-empty-placeholder" aria-hidden="true" /> }}
            rowClassName={(user) => expandedUsers.includes(user.email) ? "user-summary-row expanded" : "user-summary-row"}
            onRow={(user) => ({
              tabIndex: 0,
              "data-user-row": user.email,
              "aria-expanded": expandedUsers.includes(user.email),
              onClick: (event) => {
                if (!isInteractiveRowTarget(event.target)) toggleExpanded(user.email);
              },
              onKeyDown: (event) => {
                if ((event.key === "Enter" || event.key === " ") && !isInteractiveRowTarget(event.target)) {
                  event.preventDefault();
                  toggleExpanded(user.email);
                }
              }
            })}
            expandable={{
              expandedRowKeys: expandedUsers,
              showExpandColumn: false,
              expandedRowClassName: () => "user-detail-row",
              expandedRowRender: (user) => expandedUsers.includes(user.email) ? (
                <UserExpandedRow
                  user={user}
                  range={usageRange}
                  csrfToken={csrfToken}
                  onTeam={() => setAssignment({
                    users: [user.email],
                    targetTeamID: user.team_id
                  })}
                  onQuota={() => setQuotaUser(user.email)}
                  onLifecycle={setLifecycleAction}
                />
              ) : null
            }}
          />

          {!users.isPending && !users.isError && !total ? (
            <div className="user-empty-state">
              <div className="empty-icon" aria-hidden="true">◎</div>
              <h3>{query || teamID ? "没有匹配的用户" : "还没有用户"}</h3>
              <p>{query || teamID ? "请调整搜索条件。" : "添加用户邮箱后，将创建一个统一 API Key 并关联全部 CPA。"}</p>
              <Button type="primary" onClick={() => setCreateOpen(true)}>添加第一个用户</Button>
            </div>
          ) : null}
        </div>

        {total ? (
          <div className="table-pagination user-pagination" aria-label="用户分页">
            <span className="pagination-summary">共 {formatNumber(total)} 位用户 · {formatNumber(startIndex)}–{formatNumber(endIndex)}</span>
            <div className="pagination-actions">
              <label className="pagination-size">
                <span>每页</span>
                <select
                  aria-label="每页条数"
                  value={pageSize}
                  onChange={(event) => {
                    setPageSize(Number(event.target.value));
                    setPage(1);
                    setExpandedUsers([]);
                  }}
                >
                  <option value="25">25</option>
                  <option value="50">50</option>
                  <option value="100">100</option>
                </select>
                <span>条</span>
              </label>
              <nav className="pagination-controls" aria-label="用户列表页码">
                <Button disabled={page <= 1} onClick={() => {
                  setPage((current) => Math.max(1, current - 1));
                  setExpandedUsers([]);
                }}>上一页</Button>
                <div className="pagination-pages">
                  {paginationItems(page, totalPages).map((item, index) => (
                    item === "…"
                      ? <span key={"ellipsis-" + index} className="pagination-ellipsis" aria-hidden="true">…</span>
                      : <Button
                          key={item}
                          className={item === page ? "pagination-page active" : "pagination-page"}
                          aria-current={item === page ? "page" : undefined}
                          onClick={() => {
                            setPage(item);
                            setExpandedUsers([]);
                          }}
                        >{item}</Button>
                  ))}
                </div>
                <Button disabled={page >= totalPages} onClick={() => {
                  setPage((current) => Math.min(totalPages, current + 1));
                  setExpandedUsers([]);
                }}>下一页</Button>
              </nav>
            </div>
          </div>
        ) : null}
      </div>

      <CustomUsageRangeModal
        open={customRangeOpen}
        title="用户信息自定义统计范围"
        range={customRange}
        onCancel={() => setCustomRangeOpen(false)}
        onApply={(range) => {
          setCustomRange(range);
          setUsageWindow("custom");
          setCustomRangeOpen(false);
          setExpandedUsers([]);
        }}
      />
      <UserAssignmentModal
        assignment={assignment}
        teams={teamOptions}
        pending={assignmentMutation.isPending}
        error={assignmentMutation.error}
        onCancel={() => setAssignment(null)}
        onChange={(targetTeamID) => setAssignment((current) => current ? { ...current, targetTeamID } : null)}
        onSubmit={() => assignment && assignmentMutation.mutate(assignment)}
      />
      <CreateUserModal
        open={createOpen}
        teams={teamOptions}
        initialTeamID={catalog?.teams.some((team) => team.id === teamID) ? teamID : ""}
        pending={createMutation.isPending}
        error={createMutation.error}
        onCancel={() => setCreateOpen(false)}
        onSubmit={(input) => createMutation.mutate(input)}
      />
      <UserLifecycleConfirm
        action={lifecycleAction}
        pending={lifecycleMutation.isPending}
        onCancel={() => setLifecycleAction(null)}
        onConfirm={() => {
          if (!lifecycleAction || lifecycleSubmitRef.current) return;
          lifecycleSubmitRef.current = true;
          const action = lifecycleAction;
          setLifecycleAction(null);
          lifecycleMutation.mutate(action);
        }}
      />
      <SecretRevealModal value={secretReveal} onClose={() => setSecretReveal(null)} />
      <UserQuotaDrawer
        user={quotaUser}
        summaryQuota={pageUsers.find((candidate) => candidate.email === quotaUser)?.weekly_quota ?? null}
        csrfToken={csrfToken}
        onClose={() => setQuotaUser(null)}
        onSaved={async (message) => {
          showToast(message);
          await refreshAfterMutation();
        }}
        onFailed={(message) => showToast(message, "error")}
        onAction={(action) => setQuotaAction(action)}
      />
      <QuotaActionModal
        draft={quotaAction}
        users={pageUsers}
        pending={quotaActionMutation.isPending}
        error={quotaActionMutation.error}
        onCancel={() => setQuotaAction(null)}
        onSubmit={(input) => quotaActionMutation.mutate(input)}
      />
      <LegacyConfirmModal
        title={`恢复 ${restoreQuotaUsers?.length ?? 0} 位用户的组织默认额度？`}
        open={restoreQuotaUsers !== null}
        okText="恢复组织默认"
        pending={quotaActionMutation.isPending}
        onCancel={() => {
          if (!quotaActionMutation.isPending) {
            setRestoreQuotaUsers(null);
            quotaActionMutation.reset();
          }
        }}
        onConfirm={() => {
          if (!restoreQuotaUsers) return;
          const usersToRestore = restoreQuotaUsers;
          setRestoreQuotaUsers(null);
          quotaActionMutation.mutate({
            action: "restore_default",
            scope: "selected",
            users: usersToRestore,
            confirm: "restore_default"
          }, {
            onError: (error) => showToast(errorMessage(error), "error")
          });
        }}
      >
        <>
          {restoreQuotaUsers && pageUsers.filter((user) => (
            restoreQuotaUsers.includes(user.email) && user.weekly_quota.policy_mode !== "inherit"
          )).length
            ? `将删除所选用户的个人额度策略；当前周追加额度与用量调整保持不变。`
            : "所选用户已经继承组织默认额度，不会修改当前周追加额度或用量调整。"}
        </>
      </LegacyConfirmModal>
      <TeamUsageDrawer
        open={teamUsageOpen}
        team={selectedTeamUsage}
        range={usageRange}
        onClose={() => setTeamUsageOpen(false)}
      />
      <LegacyToastRegion toasts={toasts} />
    </section>
  );
}

function userColumns({
  page,
  pageSize,
  selectedUsers,
  allSelected,
  partiallySelected,
  sortField,
  sortDirection,
  usageWindow,
  onSort,
  onSelectPage,
  onSelect,
  onTeam,
  onQuota
}: {
  page: number;
  pageSize: number;
  selectedUsers: string[];
  allSelected: boolean;
  partiallySelected: boolean;
  sortField: UserSortField;
  sortDirection: SortDirection;
  usageWindow: UsageWindow;
  onSort: (field: UserSortField) => void;
  onSelectPage: (checked: boolean) => void;
  onSelect: (email: string, checked: boolean) => void;
  onTeam: (user: UserSummary) => void;
  onQuota: (user: UserSummary) => void;
}): TableColumnsType<UserSummary> {
  const sortable = (field: UserSortField, label: string) => ({
    title: (
      <button
        className={"legacy-sort-button" + (sortField === field ? " active" : "")}
        type="button"
        aria-label={sortField === field
          ? label + "，当前" + (sortDirection === "asc" ? "升序" : "降序") + "，点击切换排序方向"
          : label + "，点击排序"}
        onClick={(event) => {
          event.stopPropagation();
          onSort(field);
        }}
      >{label}</button>
    ),
    onHeaderCell: () => {
      const ariaSort: "ascending" | "descending" | "none" = sortField === field
        ? (sortDirection === "asc" ? "ascending" : "descending")
        : "none";
      return { "aria-sort": ariaSort };
    }
  });
  return [
    {
      title: "序号",
      key: "index",
      className: "table-index-column",
      width: "4%",
      render: (_, _user, index) => <span className="table-index-cell">{(page - 1) * pageSize + index + 1}</span>
    },
    {
      title: (
        <IndeterminateCheckbox
          ariaLabel="选择本页用户"
          checked={allSelected}
          indeterminate={partiallySelected}
          onChange={onSelectPage}
        />
      ),
      key: "select",
      className: "user-select-column",
      width: "3%",
      render: (_, user) => (
        <input
          type="checkbox"
          aria-label={"选择 " + user.email}
          checked={selectedUsers.includes(user.email)}
          onClick={(event) => event.stopPropagation()}
          onChange={(event) => onSelect(user.email, event.target.checked)}
        />
      )
    },
    {
      title: null,
      key: "toggle",
      className: "user-toggle-column",
      width: "3%",
      onHeaderCell: () => ({ "aria-label": "展开" }),
      render: () => <span className="user-chevron" aria-hidden="true">›</span>
    },
    {
      ...sortable("email", "用户"),
      key: "email",
      width: "15%",
      render: (_, user) => (
        <>
          <span className="table-primary">{user.email}</span>
          <span className="table-secondary">{user.total_records} 条历史记录</span>
        </>
      )
    },
    {
      title: "团队",
      key: "team",
      width: "9%",
      render: (_, user) => (
        <button
          className="classification-button"
          type="button"
          aria-label={"设置 " + user.email + " 的团队"}
          onClick={(event) => {
            event.stopPropagation();
            onTeam(user);
          }}
        >
          <span className={"team-chip" + (user.team ? "" : " unassigned")}>
            {user.team?.name ?? "未分组"}
          </span>
        </button>
      )
    },
    {
      title: "状态",
      key: "status",
      width: "6%",
      render: (_, user) => (
        <span className={"status-chip " + (user.status === "active" ? "success" : "neutral")}>
          {statusLabel(user.status)}
        </span>
      )
    },
    {
      title: "CPA",
      key: "accounts",
      width: "6%",
      render: (_, user) => <UserCoverage user={user} />
    },
    {
      ...sortable("requests", "使用次数"),
      key: "requests",
      className: "number-cell",
      width: "7%",
      render: (_, user) => (
        <>
          {formatNumber(user.usage.request_count)}
          {user.usage.failed_count ? <span className="usage-failed">{formatNumber(user.usage.failed_count)} 失败</span> : null}
        </>
      )
    },
    {
      ...sortable("tokens", "Token 用量"),
      key: "tokens",
      className: "user-token-column user-token-cell",
      width: "14%",
      render: (_, user) => <UserTokenCell user={user} window={usageWindow} />
    },
    {
      ...sortable("quota", "周额度状态"),
      key: "quota",
      className: "user-quota-column",
      width: "23%",
      render: (_, user) => <UserQuotaCell user={user} onOpen={() => onQuota(user)} />
    },
    {
      ...sortable("last_used", "最后使用"),
      key: "last-used",
      width: "10%",
      render: (_, user) => formatLastUsed(user.usage.last_used_at)
    }
  ];
}

function IndeterminateCheckbox({
  ariaLabel,
  checked,
  indeterminate,
  onChange
}: {
  ariaLabel: string;
  checked: boolean;
  indeterminate: boolean;
  onChange: (checked: boolean) => void;
}) {
  const input = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (input.current) input.current.indeterminate = indeterminate;
  }, [indeterminate]);
  return (
    <input
      ref={input}
      type="checkbox"
      aria-label={ariaLabel}
      checked={checked}
      onClick={(event) => event.stopPropagation()}
      onChange={(event) => onChange(event.target.checked)}
    />
  );
}

function UserCoverage({ user }: { user: UserSummary }) {
  const slots = Math.min(12, user.account_count);
  const activeSlots = user.account_count
    ? Math.round(slots * user.active_accounts / user.account_count)
    : 0;
  return (
    <>
      <span className="coverage" aria-hidden="true">
        {Array.from({ length: slots }, (_, index) => <i key={index} className={index < activeSlots ? "active" : ""} />)}
      </span>
      {user.active_accounts}/{user.account_count}
    </>
  );
}

function UserTokenCell({ user, window }: { user: UserSummary; window: UsageWindow }) {
  return (
    <div className="user-token-summary">
      <div className="user-token-stat user-token-weighted">
        <span>{usageWindowLabel(window)}加权</span>
        <LegacyTokenValue value={user.usage.weighted_tokens} />
      </div>
      <div className="user-token-stat user-token-current">
        <span>{usageWindowLabel(window)}未加权</span>
        <LegacyTokenValue value={user.usage.total_tokens} />
      </div>
    </div>
  );
}

function UserQuotaCell({ user, onOpen }: { user: UserSummary; onOpen: () => void }) {
  const quota = user.weekly_quota;
  if (!quota.period) return <span className="quota-unavailable">暂不可用</span>;
  const weightedUsed = quota.weighted_used_tokens ?? quota.used_tokens;
  const rawUsed = quota.raw_used_tokens ?? 0;
  const progress = Math.min(100, Math.max(0, Number(quota.used_percent) || 0));
  const adjustments = [
    quota.bonus_tokens > 0 ? "本周已追加 " + tokenText(quota.bonus_tokens) : "",
    quota.usage_reset_tokens > 0 ? "本周已重置 " + tokenText(quota.usage_reset_tokens) : ""
  ].filter(Boolean);
  return (
    <div className="user-quota-cell">
      <div className="user-quota-primary">
        <span className="user-quota-source">{quotaSourceLabel(quota)}</span>
        <strong>上限 {quota.unlimited ? "不限额" : tokenText(quota.limit_tokens)}</strong>
        <button
          className="inline-action"
          type="button"
          onClick={(event) => {
            event.stopPropagation();
            onOpen();
          }}
        >配置</button>
      </div>
      <div className="user-quota-meter-copy">
        <span>本周加权用量</span>
        <LegacyTokenValue value={weightedUsed} />
      </div>
      {quota.unlimited ? null : (
        <progress aria-label="本周额度使用比例" className="user-quota-progress" max={100} value={progress} />
      )}
      <div className="user-quota-progress-copy">
        <span>{quota.unlimited ? "无比例限制" : "已用 " + formatPercent(quota.used_percent)}</span>
        <span>{quota.unlimited ? "剩余不限" : "剩余 " + tokenText(quota.remaining_tokens)}</span>
      </div>
      <div className="user-quota-raw-copy"><span>本周未加权</span><LegacyTokenValue value={rawUsed} /></div>
      {adjustments.length ? <span className="user-quota-adjustment-copy">{adjustments.join(" · ")}</span> : null}
    </div>
  );
}

function LegacyTokenValue({ value }: { value: number | null | undefined }) {
  const amount = Number(value) || 0;
  const [formatted, unit = "Token"] = formatTokenAmount(amount).split(" ");
  const compacted = Math.abs(amount) >= 1_000;
  return (
    <Tooltip title={formatNumber(amount) + " Token"}>
      <span className="token-usage">
        <span className="token-usage-main" aria-hidden="true">
          <span className="token-usage-value">{formatted}</span>
          <small className="token-usage-unit">{unit}</small>
        </span>
        {compacted ? <small className="token-usage-exact" aria-hidden="true">{formatNumber(amount)} Token</small> : null}
        <span className="token-usage-sr-only">{formatNumber(amount)} Token</span>
      </span>
    </Tooltip>
  );
}

function UserExpandedRow({
  user,
  range,
  csrfToken,
  onTeam,
  onQuota,
  onLifecycle
}: {
  user: UserSummary;
  range: UsageRange;
  csrfToken: string;
  onTeam: () => void;
  onQuota: () => void;
  onLifecycle: (action: LifecycleAction) => void;
}) {
  void csrfToken;
  const [accountFilter, setAccountFilter] = useState("");
  const [accountSort, setAccountSort] = useState<{ field: UserAccountSortField; direction: SortDirection }>({
    field: "total_tokens",
    direction: "desc"
  });
  const [breakdownSort, setBreakdownSort] = useState<{ field: BreakdownSortField; direction: SortDirection }>({
    field: "total_tokens",
    direction: "desc"
  });
  const detailParams = { window: String(range.window), startAt: range.startAt, endAt: range.endAt };
  const detail = useQuery({
    queryKey: userDetailQueryKey(user.email, detailParams),
    queryFn: ({ signal }) => readUserDetail(user.email, detailParams, signal),
    staleTime: 30_000,
    gcTime: 30_000,
    retry: false
  });
  const breakdownRange = { ...range, account: accountFilter || undefined };
  const breakdown = useQuery({
    queryKey: usageBreakdownQueryKey("user", user.email, breakdownRange),
    queryFn: ({ signal }) => readUsageBreakdown("user", user.email, breakdownRange, signal),
    staleTime: 30_000,
    gcTime: 30_000,
    retry: false
  });
  if (detail.isPending) {
    return (
      <div className="user-detail-panel">
        <div className="account-model-usage-skeleton" aria-label="正在加载用户详情">
          <span />
          <span />
        </div>
      </div>
    );
  }
  if (detail.isError || !detail.data) {
    return (
      <div className="user-detail-panel">
        <div className="account-model-usage-message error" role="alert">
          <span>{errorMessage(detail.error)}</span>
          <button className="inline-action" type="button" onClick={() => void detail.refetch()}>重试</button>
        </div>
      </div>
    );
  }
  const detailedUser = detail.data.user;
  const accounts = [...detailedUser.accounts].sort((left, right) => compareRows(
    accountSortValue(left, accountSort.field),
    accountSortValue(right, accountSort.field),
    accountSort.direction,
    left.account,
    right.account
  ));
  const keyLabel = detailedUser.accounts.find((account) => account.key)?.key?.label;
  return (
    <div className="user-detail-panel">
      <UserUsageAnalysis
        user={detailedUser}
        query={breakdown.data}
        pending={breakdown.isPending}
        error={breakdown.error}
        accountFilter={accountFilter}
        sort={breakdownSort}
        onAccountFilter={setAccountFilter}
        onSort={(field) => setBreakdownSort((current) => ({
          field,
          direction: current.field === field
            ? (current.direction === "asc" ? "desc" : "asc")
            : (field === "account" || field === "combination" ? "asc" : "desc")
        }))}
        onRetry={() => void breakdown.refetch()}
      />
      <NativeTableViewport className="user-account-table-wrap" aria-label="用户账号明细表格">
        <table className="user-account-table">
          <thead>
            <tr>
              <th className="table-index-column">序号</th>
              <LegacyNativeSortHeader label="CPA 账号" field="account" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="Key 状态" field="status" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="次数" field="requests" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="输入 Token" field="input_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="输出 Token" field="output_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="推理 Token" field="reasoning_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="缓存 Token" field="cached_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="未加权 Token" field="total_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="加权 Token" field="weighted_tokens" sort={accountSort} onSort={setAccountSort} />
              <LegacyNativeSortHeader label="最后使用" field="last_used_at" sort={accountSort} onSort={setAccountSort} />
            </tr>
          </thead>
          <tbody>
            {accounts.map((account, index) => (
              <tr key={account.account}>
                <td className="table-index-cell">{index + 1}</td>
                <td><span className="table-primary">{account.account}</span></td>
                <td><span className={"status-chip " + statusTone(account.status)}>{statusLabel(account.status)}</span></td>
                <td className="number-cell">{formatNumber(account.usage.request_count)}</td>
                <td className="number-cell"><LegacyTokenValue value={account.usage.input_tokens} /></td>
                <td className="number-cell"><LegacyTokenValue value={account.usage.output_tokens} /></td>
                <td className="number-cell"><LegacyTokenValue value={account.usage.reasoning_tokens} /></td>
                <td className="number-cell"><LegacyTokenValue value={account.usage.cached_tokens} /></td>
                <td className="number-cell token-total"><LegacyTokenValue value={account.usage.total_tokens} /></td>
                <td className="number-cell token-total"><LegacyTokenValue value={account.usage.weighted_tokens} /></td>
                <td>{formatLastUsed(account.usage.last_used_at)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </NativeTableViewport>
      <div className="user-detail-actions">
        <button className="inline-action" type="button" onClick={onTeam}>设置团队</button>
        <button className="inline-action" type="button" onClick={onQuota}>配置周额度</button>
        <button className="inline-action" type="button" onClick={() => onLifecycle({ kind: "reset-password", user })}>重置密码</button>
        <button className="inline-action danger-text" type="button" onClick={() => onLifecycle({ kind: "delete", user })}>删除用户</button>
        {user.active_keys && keyLabel ? (
          <>
            <button className="inline-action" type="button" onClick={() => onLifecycle({ kind: "rotate", user, keyLabel })}>轮换唯一 Key</button>
            <button className="inline-action danger-text" type="button" onClick={() => onLifecycle({ kind: "revoke", user })}>停用唯一 Key</button>
          </>
        ) : null}
      </div>
    </div>
  );
}

function UserUsageAnalysis({
  user,
  query,
  pending,
  error,
  accountFilter,
  sort,
  onAccountFilter,
  onSort,
  onRetry
}: {
  user: UserDetail;
  query: UsageBreakdown | undefined;
  pending: boolean;
  error: unknown;
  accountFilter: string;
  sort: { field: BreakdownSortField; direction: SortDirection };
  onAccountFilter: (account: string) => void;
  onSort: (field: BreakdownSortField) => void;
  onRetry: () => void;
}) {
  const header = (successCount?: number) => (
    <div className="usage-analysis-header">
      <div className="usage-analysis-title">
        <strong>模型与推理分析</strong>
        {successCount === undefined ? null : <span>成功调用 <b>{formatNumber(successCount)}</b></span>}
      </div>
      <label className="compact-select usage-analysis-filter">
        <span>CPA</span>
        <LegacyEnhancedSelect
          label="CPA"
          value={accountFilter}
          options={[
            { value: "", label: "全部 CPA" },
            ...user.accounts.map((account) => ({ value: account.account, label: account.account }))
          ]}
          onChange={onAccountFilter}
        />
      </label>
    </div>
  );
  if (pending && !query) {
    return (
      <section className="user-usage-analysis">
        {header()}
        <div className="usage-analysis-skeleton" aria-label="正在加载模型分析"><span /><span /><span /></div>
      </section>
    );
  }
  if (!query) {
    return (
      <section className="user-usage-analysis">
        {header()}
        <div className="usage-analysis-message error" role="alert">
          <strong>模型分析加载失败</strong>
          <span>{errorMessage(error)}</span>
          <button className="inline-action" type="button" onClick={onRetry}>重试</button>
        </div>
      </section>
    );
  }
  if (!query.collection_started_at) {
    return (
      <section className="user-usage-analysis">
        {header()}
        <div className="usage-analysis-message">
          <strong>等待新统计开始</strong>
          <span>用量采集器启动后，将从该时刻开始记录模型和推理强度。</span>
        </div>
      </section>
    );
  }
  const successCount = query.totals.success_count ?? 0;
  const failedCount = query.totals.failed_count ?? 0;
  const totalWeighted = query.totals.weighted_tokens ?? query.totals.total_tokens;
  const rows = [...query.combinations].sort((left, right) => compareRows(
    breakdownSortValue(left, sort.field, totalWeighted),
    breakdownSortValue(right, sort.field, totalWeighted),
    sort.direction,
    String(left.account || "") + left.model + left.reasoning_effort,
    String(right.account || "") + right.model + right.reasoning_effort
  ));
  const models = groupUserModels(rows);
  const summary = (
    <div className="usage-analysis-summary">
      <div><span>失败调用</span><strong>{formatNumber(failedCount)}</strong></div>
      <div><span>强度覆盖率</span><strong>{formatUsageRatio(query.totals.known_effort_count ?? 0, successCount)}</strong></div>
      <div className="usage-analysis-token-stat"><span>未加权 Token</span><strong><LegacyTokenValue value={query.totals.total_tokens} /></strong></div>
      <div className="usage-analysis-token-stat"><span>加权 Token</span><strong><LegacyTokenValue value={totalWeighted} /></strong></div>
      <div className="usage-analysis-time-stat"><span>统计开始</span><strong>{formatFullTimestamp(query.collection_started_at)}</strong></div>
    </div>
  );
  if (!successCount) {
    return (
      <section className="user-usage-analysis">
        {header(successCount)}
        <div className="usage-analysis-layout usage-analysis-layout-empty">
          <div className="usage-analysis-message compact">
            <strong>当前范围暂无成功调用</strong>
            <span>{failedCount ? `有 ${formatNumber(failedCount)} 次失败调用，未计入占比。` : "产生新调用后将在这里显示模型与推理强度组合。"}</span>
          </div>
          {summary}
        </div>
      </section>
    );
  }
  return (
    <section className="user-usage-analysis">
      {header(successCount)}
      <NativeTableViewport className="usage-model-table-wrap" aria-label="模型用量表格">
        <table className="usage-model-table">
          <thead><tr><th className="table-index-column">序号</th><th>模型</th><th>使用量</th><th>推理强度构成</th><th>Token 明细</th><th>调用</th></tr></thead>
          <tbody>
            {models.map((model, index) => (
              <tr key={model.model}>
                <td className="table-index-cell">{index + 1}</td>
                <td><span className="table-primary model-name">{model.model}</span></td>
                <td className="number-cell"><LegacyTokenValue value={model.totalTokens} /></td>
                <td><UserModelEffortProgress model={model} /></td>
                <td>
                  <dl className="usage-model-token-details">
                    <div><dt>输入</dt><dd><LegacyTokenValue value={model.inputTokens} /></dd></div>
                    <div><dt>输出</dt><dd><LegacyTokenValue value={model.outputTokens} /></dd></div>
                    <div><dt>推理</dt><dd><LegacyTokenValue value={model.reasoningTokens} /></dd></div>
                    <div><dt>缓存</dt><dd><LegacyTokenValue value={model.cachedTokens} /></dd></div>
                  </dl>
                </td>
                <td className="number-cell">{formatNumber(model.successCount)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </NativeTableViewport>
      {summary}
      <NativeTableViewport className="usage-breakdown-table-wrap" aria-label="推理强度用量明细表格">
        <table className="usage-breakdown-table">
          <thead><tr>
            <th className="table-index-column">序号</th>
            {([
              ["account", "CPA"],
              ["combination", "模型 × 推理强度"],
              ["success_count", "调用"],
              ["share", "占比"],
              ["total_tokens", "未加权 Token"],
              ["multiplier", "实际倍率"],
              ["weighted_tokens", "加权 Token"],
              ["average_total", "平均/次"],
              ["last_used_at", "最后使用"]
            ] as Array<[BreakdownSortField, string]>).map(([field, label]) => (
              <th key={field} aria-sort={sort.field === field ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}>
                <button className={"legacy-sort-button" + (sort.field === field ? " active" : "")} type="button" onClick={() => onSort(field)}>{label}</button>
              </th>
            ))}
          </tr></thead>
          <tbody>
            {rows.map((row, index) => {
              const weighted = row.weighted_tokens ?? row.total_tokens;
              const multiplier = row.total_tokens > 0 ? weighted / row.total_tokens : 0;
              const share = successCount > 0 ? row.success_count / successCount * 100 : 0;
              return (
                <tr key={String(row.account || "") + ":" + row.model + ":" + row.reasoning_effort}>
                  <td className="table-index-cell">{index + 1}</td>
                  <td><span className="table-primary">{row.account ?? ""}</span></td>
                  <td><span className="table-primary model-name">{usageCombinationLabel(row)}</span></td>
                  <td className="number-cell">{formatNumber(row.success_count)}</td>
                  <td className="number-cell usage-percentage">{share.toFixed(1)}%</td>
                  <td className="number-cell"><LegacyTokenValue value={row.total_tokens} /></td>
                  <td className="number-cell">×{multiplier.toFixed(2)}</td>
                  <td className="number-cell token-total"><LegacyTokenValue value={weighted} /></td>
                  <td className="number-cell"><LegacyTokenValue value={row.success_count ? Math.round(row.total_tokens / row.success_count) : 0} /></td>
                  <td>{formatLastUsed(row.last_used_at)}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </NativeTableViewport>
      {error ? <div className="usage-analysis-stale">刷新失败，当前展示上一次成功数据：{errorMessage(error)}</div> : null}
    </section>
  );
}

type UserModelEffort = UsageCombination & { sharePercent: number };
type UserModelRow = {
  model: string;
  totalTokens: number;
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  cachedTokens: number;
  successCount: number;
  efforts: UserModelEffort[];
};

function groupUserModels(combinations: UsageCombination[]): UserModelRow[] {
  const grouped = new Map<string, UsageCombination[]>();
  combinations.forEach((item) => {
    if (item.total_tokens <= 0) return;
    const model = item.model && item.model !== "unknown" ? item.model : "未上报模型";
    grouped.set(model, [...(grouped.get(model) ?? []), item]);
  });
  return [...grouped.entries()].map(([model, items]) => {
    const effortsByName = new Map<string, UsageCombination>();
    items.forEach((item) => {
      const name = item.reasoning_effort && item.reasoning_effort !== "unknown" ? item.reasoning_effort : "未上报强度";
      const current = effortsByName.get(name);
      effortsByName.set(name, current ? {
        ...current,
        request_count: current.request_count + item.request_count,
        success_count: current.success_count + item.success_count,
        failed_count: current.failed_count + item.failed_count,
        input_tokens: current.input_tokens + item.input_tokens,
        output_tokens: current.output_tokens + item.output_tokens,
        reasoning_tokens: current.reasoning_tokens + item.reasoning_tokens,
        cached_tokens: current.cached_tokens + item.cached_tokens,
        total_tokens: current.total_tokens + item.total_tokens,
        weighted_tokens: Number(current.weighted_tokens ?? current.total_tokens) + Number(item.weighted_tokens ?? item.total_tokens),
        last_used_at: Math.max(current.last_used_at, item.last_used_at)
      } : { ...item, reasoning_effort: name });
    });
    const efforts = [...effortsByName.values()];
    const totalTokens = efforts.reduce((total, item) => total + item.total_tokens, 0);
    let allocated = 0;
    const normalized = efforts
      .sort((left, right) => right.total_tokens - left.total_tokens || left.reasoning_effort.localeCompare(right.reasoning_effort, "zh-CN"))
      .map((item, index, sorted) => {
        const sharePercent = index === sorted.length - 1
          ? Math.max(0, 100 - allocated)
          : Math.round(item.total_tokens * 10_000 / totalTokens) / 100;
        allocated += sharePercent;
        return { ...item, sharePercent };
      });
    return {
      model,
      totalTokens,
      inputTokens: efforts.reduce((total, item) => total + item.input_tokens, 0),
      outputTokens: efforts.reduce((total, item) => total + item.output_tokens, 0),
      reasoningTokens: efforts.reduce((total, item) => total + item.reasoning_tokens, 0),
      cachedTokens: efforts.reduce((total, item) => total + item.cached_tokens, 0),
      successCount: efforts.reduce((total, item) => total + item.success_count, 0),
      efforts: normalized
    };
  }).sort((left, right) => right.totalTokens - left.totalTokens || left.model.localeCompare(right.model, "zh-CN"));
}

function UserModelEffortProgress({ model }: { model: UserModelRow }) {
  return (
    <div className="account-model-progress" role="group" aria-label={`${model.model} 各推理强度 Token 占比`}>
      {model.efforts.map((effort) => {
        const lines = modelEffortTooltipLines(model.model, effort);
        const shareUnits = Math.max(1, Math.min(100, Math.round(effort.sharePercent)));
        return (
          <LegacyUsageTooltip key={effort.reasoning_effort} lines={lines}>
            {(events) => (
              <button
                {...events}
                className={`account-model-progress-segment account-model-effort-${effortColorKey(effort.reasoning_effort)} account-model-share-tens-${Math.floor(shareUnits / 10)} account-model-share-ones-${shareUnits % 10}${effort.sharePercent < 18 ? " compact" : ""}`}
                type="button"
                aria-label={lines.join("，")}
              >
                <span>{effort.reasoning_effort}</span>
                <em>{formatUsageRatio(effort.total_tokens, model.totalTokens)}</em>
              </button>
            )}
          </LegacyUsageTooltip>
        );
      })}
    </div>
  );
}

function modelEffortTooltipLines(model: string, effort: UserModelEffort | TeamModelEffort) {
  return [
    `${model} · ${effort.reasoning_effort}`,
    `调用：${formatNumber(effort.request_count)}`,
    `输入：${formatNumber(effort.input_tokens)}`,
    `输出：${formatNumber(effort.output_tokens)}`,
    `推理：${formatNumber(effort.reasoning_tokens)}`,
    `缓存：${formatNumber(effort.cached_tokens)}`,
    `总 Token：${formatNumber(effort.total_tokens)}`,
    `加权 Token：${formatNumber(effort.weighted_tokens ?? effort.total_tokens)}`
  ];
}

type LegacyUsageTooltipEvents = {
  onPointerEnter: (event: { currentTarget: HTMLElement }) => void;
  onPointerLeave: () => void;
  onFocus: (event: { currentTarget: HTMLElement }) => void;
  onBlur: () => void;
};

function LegacyUsageTooltip({
  lines,
  children
}: {
  lines: string[];
  children: (events: LegacyUsageTooltipEvents) => ReactNode;
}) {
  const trigger = useRef<HTMLElement | null>(null);
  const layer = useRef<HTMLDivElement | null>(null);
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState<{ left: number; top: number } | null>(null);
  const text = lines.join("\n");
  const show = (element: HTMLElement) => {
    trigger.current = element;
    setPosition(null);
    setOpen(true);
  };
  const hide = () => setOpen(false);
  useLayoutEffect(() => {
    if (!open || !trigger.current || !layer.current) return;
    const rect = trigger.current.getBoundingClientRect();
    const layerRect = layer.current.getBoundingClientRect();
    setPosition({
      left: Math.min(window.innerWidth - layerRect.width - 12, Math.max(12, rect.left + rect.width / 2 - layerRect.width / 2)),
      top: Math.max(12, rect.top - layerRect.height - 8)
    });
  }, [open, text]);
  const events: LegacyUsageTooltipEvents = {
    onPointerEnter: (event) => show(event.currentTarget),
    onPointerLeave: hide,
    onFocus: (event) => show(event.currentTarget),
    onBlur: hide
  };
  return (
    <>
      {children(events)}
      {open ? createPortal(
        <div
          ref={layer}
          className="user-usage-tooltip-layer"
          style={{
            left: position?.left ?? 0,
            top: position?.top ?? 0,
            visibility: position ? "visible" : "hidden"
          }}
        >{text}</div>,
        document.body
      ) : null}
    </>
  );
}

function LegacyNativeSortHeader({
  label,
  field,
  sort,
  onSort
}: {
  label: string;
  field: UserAccountSortField;
  sort: { field: UserAccountSortField; direction: SortDirection };
  onSort: (sort: { field: UserAccountSortField; direction: SortDirection }) => void;
}) {
  const active = sort.field === field;
  return (
    <th aria-sort={active ? (sort.direction === "asc" ? "ascending" : "descending") : "none"}>
      <button
        className={"legacy-sort-button" + (active ? " active" : "")}
        type="button"
        onClick={() => onSort({
          field,
          direction: active
            ? (sort.direction === "asc" ? "desc" : "asc")
            : (field === "account" || field === "status" ? "asc" : "desc")
        })}
      >{label}</button>
    </th>
  );
}

function UserAssignmentModal({
  assignment,
  teams,
  pending,
  error,
  onCancel,
  onChange,
  onSubmit
}: {
  assignment: TeamAssignment | null;
  teams: Array<{ value: string; label: string }>;
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onChange: (teamID: string | null) => void;
  onSubmit: () => void;
}) {
  return (
    <Modal
      className="legacy-user-form-modal"
      title={<LegacyDialogTitle title={(assignment?.users.length ?? 0) > 1 ? "批量分配团队" : "设置团队"} kicker="TEAM ASSIGNMENT" subtitle={assignment?.users.length === 1 ? assignment.users[0] : "已选择 " + (assignment?.users.length ?? 0) + " 位用户"} />}
      open={assignment !== null}
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText="保存团队"
      cancelText="取消"
      confirmLoading={pending}
      onCancel={onCancel}
      onOk={onSubmit}
      destroyOnHidden
      mask={{ closable: false }}
    >
      <div className="legacy-user-form-body">
        <label className="field">
          <span>统计团队</span>
          <select
            aria-label="统计团队"
            value={assignment?.targetTeamID ?? ""}
            onChange={(event) => onChange(event.target.value || null)}
          >
            {teams.map((team) => <option key={team.value} value={team.value === "unassigned" ? "" : team.value}>{team.label}</option>)}
          </select>
          <small>每位用户只能属于一个团队，用于团队用量统计。</small>
        </label>
        <div className="inline-notice">保存后，团队报表会按当前成员动态汇总所选范围内的 Token；历史事件本身不会改写。</div>
        <LegacyFormError error={error} />
      </div>
    </Modal>
  );
}

function CreateUserModal({
  open,
  teams,
  initialTeamID,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  open: boolean;
  teams: Array<{ value: string; label: string }>;
  initialTeamID: string;
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (input: { email: string; teamID: string | null }) => void;
}) {
  const [email, setEmail] = useState("");
  const [teamID, setTeamID] = useState("");
  const emailInputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (!open) return;
    setEmail("");
    setTeamID(initialTeamID);
  }, [initialTeamID, open]);
  const submit = () => {
    if (!emailInputRef.current?.reportValidity()) return;
    onSubmit({ email, teamID: teamID || null });
  };
  return (
    <Modal
      className="legacy-user-form-modal"
      title={<LegacyDialogTitle title="添加用户" kicker="NEW USER" />}
      open={open}
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText="创建用户"
      cancelText="取消"
      okButtonProps={{ disabled: pending }}
      onCancel={onCancel}
      onOk={submit}
      afterOpenChange={(opened) => {
        if (opened) emailInputRef.current?.focus();
      }}
      destroyOnHidden
      mask={{ closable: false }}
    >
      <div className="legacy-user-form-body">
        <label className="field">
          <span>用户邮箱</span>
          <input
            type="email"
            ref={emailInputRef}
            aria-label="用户邮箱"
            placeholder="name@example.com"
            value={email}
            autoFocus
            required
            onChange={(event) => setEmail(event.target.value)}
          />
        </label>
        <label className="field add-user-team-field">
          <span>所属团队</span>
          <select aria-label="所属团队" value={teamID} onChange={(event) => setTeamID(event.target.value)}>
            {teams.map((team) => <option key={team.value} value={team.value === "unassigned" ? "" : team.value}>{team.label}</option>)}
          </select>
          <small>可选；团队仅用于用量统计，不影响 CPA 自动分配。</small>
        </label>
        <div className="inline-notice">系统会创建统一 API Key，并为用户设置系统默认初始密码。API Key 只显示一次；用户首次登录必须修改默认密码。</div>
        <LegacyFormError error={error} />
      </div>
    </Modal>
  );
}

function UserLifecycleConfirm({
  action,
  pending,
  onCancel,
  onConfirm
}: {
  action: LifecycleAction | null;
  pending: boolean;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  if (!action) return null;
  const copy = lifecycleCopy(action);
  return (
    <LegacyConfirmModal
      title={copy.title}
      open
      okText={copy.okText}
      danger={copy.danger}
      pending={pending}
      onCancel={onCancel}
      onConfirm={onConfirm}
    >
      {copy.message}
    </LegacyConfirmModal>
  );
}

function LegacyConfirmModal({
  title,
  open,
  okText,
  danger,
  pending,
  children,
  onCancel,
  onConfirm
}: {
  title: string;
  open: boolean;
  okText: string;
  danger?: boolean;
  pending?: boolean;
  children: ReactNode;
  onCancel: () => void;
  onConfirm: () => void;
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
      mask={{ closable: false }}
      footer={[
        <Button key="cancel" disabled={pending} onClick={onCancel}>取消</Button>,
        <Button key="confirm" danger={danger} type={danger ? "default" : "primary"} loading={pending} onClick={onConfirm}>{okText}</Button>
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

function SecretRevealModal({
  value,
  onClose
}: {
  value: SecretReveal | null;
  onClose: () => void;
}) {
  const secrets = value
    ? [
        ...value.keys.map((key) => ({ account: key.account || "全部 CPA", user: key.user, value: key.key })),
        ...(value.password ? [{ account: "使用中心默认初始密码", user: value.passwordUser ?? value.keys[0]?.user ?? "", value: value.password }] : [])
      ]
    : [];
  const copyAll = () => {
    if (!value) return;
    const rows = [
      ...value.keys.map((key) => `${key.label}\t${key.key}`),
      ...(value.password ? [`${value.passwordUser ?? value.keys[0]?.user ?? "user"}:usage-password\t${value.password}`] : [])
    ];
    void copyText(rows.join("\n"));
  };
  return (
    <Modal
      className="legacy-secret-modal"
      title={<LegacyDialogTitle title="保存新生成的凭据" kicker="ONE-TIME SECRET" />}
      open={value !== null}
      width={660}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      onCancel={onClose}
      destroyOnHidden
      mask={{ closable: false }}
      footer={[
        <Button key="copy-all" onClick={copyAll}>复制全部</Button>,
        <Button key="saved" type="primary" onClick={onClose}>我已保存</Button>
      ]}
    >
      <div className="warning-banner">{value?.message || "关闭后平台不会再次展示完整凭据。"}</div>
      <div className="secret-list">
        {secrets.map((secret, index) => (
          <div className="secret-item" key={secret.account + ":" + index}>
            <div className="secret-item-head"><strong>{secret.account}</strong><span>{secret.user}</span></div>
            <div className="secret-value">
              <code>{secret.value}</code>
              <button className="secret-copy" type="button" onClick={() => void copyText(secret.value)}>复制</button>
            </div>
          </div>
        ))}
      </div>
    </Modal>
  );
}

function LegacyDialogTitle({
  title,
  kicker,
  subtitle
}: {
  title: string;
  kicker: string;
  subtitle?: string;
}) {
  return (
    <div className="legacy-dialog-title">
      <strong>{title}</strong>
      <span>{kicker}</span>
      {subtitle ? <small>{subtitle}</small> : null}
    </div>
  );
}

function LegacyFormError({ error }: { error: unknown }) {
  return <p className="form-error" role="alert">{error ? errorMessage(error) : ""}</p>;
}

function UserQuotaDrawer({
  user,
  summaryQuota,
  csrfToken,
  onClose,
  onSaved,
  onFailed,
  onAction
}: {
  user: string | null;
  summaryQuota: UserWeeklyQuota | null;
  csrfToken: string;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
  onFailed: (message: string) => void;
  onAction: (draft: QuotaActionDraft) => void;
}) {
  const queryClient = useQueryClient();
  const [mode, setMode] = useState<UserQuotaMode>("inherit");
  const [tokens, setTokens] = useState("");
  const [validationError, setValidationError] = useState("");
  const [restoreConfirm, setRestoreConfirm] = useState(false);
  const tokenInputRef = useRef<HTMLInputElement>(null);
  const quotaKey = userQuotaQueryKey(user || "");
  const query = useQuery({
    queryKey: quotaKey,
    queryFn: ({ signal }) => readUserQuota(user || "", signal),
    enabled: Boolean(user),
    staleTime: 0,
    gcTime: 0,
    retry: false
  });
  useEffect(() => {
    if (!query.data) return;
    setMode(query.data.weekly_quota.policy_mode);
    setTokens(query.data.weekly_quota.policy_tokens == null ? "" : String(query.data.weekly_quota.policy_tokens));
    setValidationError("");
  }, [query.data]);
  const finish = async (message: string) => {
    queryClient.removeQueries({ queryKey: quotaKey, exact: true });
    onClose();
    await onSaved(message);
  };
  const update = useMutation({
    mutationFn: () => updateUserQuota(user || "", mode, mode === "custom" ? Number(tokens) : null, csrfToken),
    onSuccess: (result) => void finish(result.message || "用户周额度策略已保存")
  });
  const restore = useMutation({
    mutationFn: () => applyUserQuotaAction({
      action: "restore_default",
      scope: "selected",
      users: user ? [user] : [],
      confirm: "restore_default"
    }, csrfToken),
    onSuccess: (result) => {
      void finish(result.message);
    },
    onError: (error) => onFailed(errorMessage(error))
  });
  const quota = query.data?.weekly_quota ?? summaryQuota ?? undefined;
  const adjustments = query.data?.adjustments ?? [];
  const pending = update.isPending || restore.isPending;
  const save = () => {
    setValidationError("");
    if (mode === "custom" && tokenInputRef.current && !tokenInputRef.current.checkValidity()) {
      tokenInputRef.current.reportValidity();
      tokenInputRef.current.focus();
      return;
    }
    if (mode === "custom" && (!/^\d+$/.test(tokens.trim()) || Number(tokens) <= 0)) {
      setValidationError("自定义周额度必须为正整数");
      tokenInputRef.current?.focus();
      return;
    }
    update.mutate();
  };
  return (
    <>
      <Drawer
        className="legacy-user-quota-drawer"
        title={<LegacyDialogTitle title="配置用户周额度" kicker="USER WEEKLY QUOTA" subtitle={user || ""} />}
        placement="right"
        size={500}
        open={Boolean(user)}
        closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
        onClose={() => {
          setValidationError("");
          onClose();
        }}
        destroyOnHidden
        footer={(
          <div className="legacy-drawer-footer">
            <Button onClick={onClose}>取消</Button>
            <Button
              type="primary"
              disabled={!quota || pending}
              onClick={save}
            >{update.isPending ? "正在保存…" : "保存额度策略"}</Button>
          </div>
        )}
      >
        {query.isPending && !summaryQuota ? <Skeleton active paragraph={{ rows: 12 }} /> : null}
        {quota ? (
          <div className="user-quota-drawer-content">
            <dl className="user-quota-summary">
              <QuotaFact label="本周加权已用" value={<LegacyTokenValue value={quota.weighted_used_tokens ?? quota.used_tokens} />} emphasize={false} />
              <QuotaFact label="本周未加权" value={<LegacyTokenValue value={quota.raw_used_tokens} />} emphasize={false} />
              <QuotaFact
                label="当前加权上限"
                value={quota.unlimited ? "不限额" : tokenReadableText(quota.limit_tokens)}
                detail={quota.bonus_tokens > 0 ? `（含追加 ${tokenReadableText(quota.bonus_tokens)}）` : undefined}
              />
              <QuotaFact label="基础额度" value={quota.base_limit_tokens == null ? "不限额" : tokenReadableText(quota.base_limit_tokens)} />
              <QuotaFact label="加权剩余额度" value={quota.unlimited ? "不限额" : tokenReadableText(quota.remaining_tokens)} />
              <QuotaFact label="下次重置" value={formatTimestamp(quota.week_end_at)} />
            </dl>
            <div className="inline-notice">额度按该用户在全部 CPA 的 Token 总量汇总。达到额度后只拒绝新请求，已经开始的请求（含流式输出）可以完成。</div>
            <fieldset className="quota-policy-options">
              <legend>额度策略</legend>
              <label><input type="radio" name="user-quota-mode" value="inherit" checked={mode === "inherit"} onChange={() => {
                setMode("inherit");
                setValidationError("");
              }} /><span><strong>继承组织默认</strong><small>{quota.default_limit_tokens == null ? "当前组织默认不限额" : "当前组织默认 " + tokenReadableText(quota.default_limit_tokens)}</small></span></label>
              <label><input type="radio" name="user-quota-mode" value="unlimited" checked={mode === "unlimited"} onChange={() => {
                setMode("unlimited");
                setValidationError("");
              }} /><span><strong>单独不限额</strong><small>不受以后组织默认值变化影响</small></span></label>
              <label><input type="radio" name="user-quota-mode" value="custom" checked={mode === "custom"} onChange={() => {
                setMode("custom");
                setValidationError("");
              }} /><span><strong>自定义额度</strong><small>每个自然周一 00:00 重新计算</small></span></label>
            </fieldset>
            <label className={"field" + (mode === "custom" ? "" : " disabled")}>
              <span>每周 Token</span>
              <div className="token-input-control">
                <input
                  ref={tokenInputRef}
                  aria-label="每周 Token"
                  type="number"
                  inputMode="numeric"
                  value={tokens}
                  min={1}
                  max={1_000_000_000_000}
                  step={1}
                  placeholder="例如 100000000"
                  disabled={mode !== "custom"}
                  onChange={(event) => {
                    setTokens(event.target.value);
                    setValidationError("");
                  }}
                />
                <TokenInputPreview value={tokens} emptyLabel="请输入自定义周额度" />
              </div>
            </label>
            <section className="user-quota-operations">
              <div className="user-quota-operations-head">
                <div><strong>本周额度操作</strong><small>仅作用于当前自然周，不修改原始用量记录。</small></div>
                <span className={"status-chip " + (adjustments.length ? "success" : "neutral")}>
                  {adjustments.length ? adjustments.length + " 条调整" : "暂无调整"}
                </span>
              </div>
              <div className="user-quota-operation-grid">
                <button
                  className="quota-operation-card"
                  type="button"
                  disabled={quota.unlimited}
                  onClick={() => {
                    onAction({ action: "add_bonus", scope: "selected", users: user ? [user] : [] });
                  }}
                ><span>追加本周额度</span><small>临时增加可用额度，下周自动失效</small></button>
                <button
                  className="quota-operation-card"
                  type="button"
                  disabled={quota.policy_mode === "inherit"}
                  onClick={() => setRestoreConfirm(true)}
                ><span>恢复组织默认</span><small>删除个人策略，当前周临时调整保持不变</small></button>
                <button
                  className="quota-operation-card danger"
                  type="button"
                  disabled={!(quota.used_tokens > 0)}
                  onClick={() => {
                    onAction({ action: "reset_usage", scope: "selected", users: user ? [user] : [] });
                  }}
                ><span>清零本周已用量</span><small>保留历史事件，以调整账本抵扣当前用量</small></button>
              </div>
              <div className="quota-adjustment-history">
                {adjustments.slice(0, 4).map((adjustment, index) => (
                  <div className="quota-adjustment-history-row" key={adjustment.created_at + ":" + index}>
                    <strong>{adjustment.action === "bonus" ? "追加本周额度" : "清零本周已用量"} · {tokenText(adjustment.token_amount)}</strong>
                    <time>{formatTimestamp(adjustment.created_at)}</time>
                    <p title={adjustment.reason}>{adjustment.reason}</p>
                  </div>
                ))}
              </div>
            </section>
            <LegacyFormError error={validationError ? new Error(validationError) : query.error || update.error} />
          </div>
        ) : null}
      </Drawer>
      <LegacyConfirmModal
        title="恢复 1 位用户的组织默认额度？"
        open={restoreConfirm}
        okText="恢复组织默认"
        pending={restore.isPending}
        onCancel={() => setRestoreConfirm(false)}
        onConfirm={() => {
          setRestoreConfirm(false);
          restore.mutate();
        }}
      >
        {summaryQuota?.policy_mode !== "inherit"
          ? "将删除 1 位用户的个人额度策略；当前周追加额度与用量调整保持不变。"
          : "所选用户已经继承组织默认额度，不会修改当前周追加额度或用量调整。"}
      </LegacyConfirmModal>
    </>
  );
}

function QuotaFact({
  label,
  value,
  detail,
  emphasize = true
}: {
  label: string;
  value: ReactNode;
  detail?: string;
  emphasize?: boolean;
}) {
  return <div><dt>{label}</dt><dd>{emphasize ? <strong>{value}</strong> : value}{detail ? <small>{detail}</small> : null}</dd></div>;
}

function TokenInputPreview({
  value,
  emptyLabel
}: {
  value: string | number | null | undefined;
  emptyLabel?: string;
}) {
  const presentation = tokenInputPresentation(value, emptyLabel);
  return (
    <div className="token-input-preview" data-state={presentation.state} aria-live="polite">
      {presentation.state === "empty" ? <small>{presentation.emptyLabel}</small> : null}
      {presentation.state === "invalid" ? <small>请输入正整数 Token</small> : null}
      {presentation.state === "ready" ? (
        <>
          <strong>{presentation.compact}</strong>
          {presentation.localized ? <> <span>{presentation.localized}</span></> : null}
          {presentation.compacted ? <> <small>精确值 {presentation.exact}</small></> : null}
        </>
      ) : null}
    </div>
  );
}

function QuotaActionModal({
  draft,
  users,
  pending,
  error,
  onCancel,
  onSubmit
}: {
  draft: QuotaActionDraft | null;
  users: UserSummary[];
  pending: boolean;
  error: unknown;
  onCancel: () => void;
  onSubmit: (input: UserQuotaActionInput) => void;
}) {
  const [tokenAmount, setTokenAmount] = useState("");
  const [reason, setReason] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [validationError, setValidationError] = useState("");
  const tokenInputRef = useRef<HTMLInputElement>(null);
  const reasonInputRef = useRef<HTMLTextAreaElement>(null);
  const confirmationInputRef = useRef<HTMLInputElement>(null);
  useEffect(() => {
    if (!draft) return;
    setTokenAmount("");
    setReason("");
    setConfirmation("");
    setValidationError("");
  }, [draft]);
  const selected = draft
    ? users.filter((user) => draft.users.includes(user.email))
    : [];
  const targetCount = draft?.scope === "all" ? users.length : selected.length;
  const usedCount = selected.filter((user) => user.weekly_quota.used_tokens > 0).length;
  const totalUsed = selected.reduce((total, user) => total + user.weekly_quota.used_tokens, 0);
  const totalRaw = selected.reduce((total, user) => total + user.weekly_quota.raw_used_tokens, 0);
  const confirmPhrase = draft?.action === "reset_usage"
    ? (draft.scope === "all" ? "确认清零全部" : "确认清零")
    : "";
  const submit = () => {
    if (!draft) return;
    setValidationError("");
    if (draft.action === "add_bonus" && tokenInputRef.current && !tokenInputRef.current.checkValidity()) {
      tokenInputRef.current.reportValidity();
      tokenInputRef.current.focus();
      return;
    }
    if (reasonInputRef.current && !reasonInputRef.current.checkValidity()) {
      reasonInputRef.current.reportValidity();
      reasonInputRef.current.focus();
      return;
    }
    if (confirmationInputRef.current && !confirmationInputRef.current.checkValidity()) {
      confirmationInputRef.current.reportValidity();
      confirmationInputRef.current.focus();
      return;
    }
    if (!reason.trim()) {
      setValidationError("请填写额度操作原因");
      reasonInputRef.current?.focus();
      return;
    }
    if (draft.action === "add_bonus" && (!/^\d+$/.test(tokenAmount.trim()) || Number(tokenAmount) <= 0)) {
      setValidationError("追加额度必须为正整数");
      tokenInputRef.current?.focus();
      return;
    }
    if (confirmPhrase && confirmation.trim() !== confirmPhrase) {
      setValidationError(`请输入“${confirmPhrase}”`);
      confirmationInputRef.current?.focus();
      return;
    }
    onSubmit({
      action: draft.action,
      scope: draft.scope,
      users: draft.users,
      tokenAmount: draft.action === "add_bonus" ? Number(tokenAmount) : undefined,
      reason: reason.trim(),
      confirm: draft.action === "add_bonus"
        ? "add_bonus"
        : (draft.scope === "all" ? "reset_all_current_week_usage" : "reset_current_week_usage")
    });
  };
  return (
    <Modal
      className="legacy-user-form-modal quota-action-modal"
      title={<LegacyDialogTitle
        title={draft?.action === "add_bonus"
          ? "追加本周额度"
          : (draft?.scope === "all" ? "清零全部用户本周已用量" : "清零本周已用量")}
        kicker="QUOTA ADJUSTMENT"
        subtitle={draft?.scope === "all"
          ? `全部 ${formatNumber(targetCount)} 位用户`
          : (targetCount === 1 ? draft?.users[0] : "已选择 " + formatNumber(targetCount) + " 位用户")}
      />}
      open={draft !== null}
      width={520}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={pending ? "正在处理…" : (draft?.action === "add_bonus" ? "确认追加" : "确认清零")}
      okButtonProps={{ danger: draft?.action === "reset_usage", disabled: !draft || pending }}
      onCancel={onCancel}
      onOk={submit}
      afterOpenChange={(opened) => {
        if (!opened) return;
        if (draft?.action === "add_bonus") tokenInputRef.current?.focus();
        else reasonInputRef.current?.focus();
      }}
      destroyOnHidden
      mask={{ closable: false }}
    >
      <div className="legacy-user-form-body">
        <div className="quota-action-impact">
          <strong>{draft?.action === "add_bonus" ? "增加本周可用额度，基础策略保持不变" : "清零计费用量，原始 Token 事件与统计历史保持不变"}</strong>
          <dl>
            <div><dt>影响用户</dt><dd>{formatNumber(targetCount)} 位</dd></div>
            <div><dt>有本周用量</dt><dd>{formatNumber(usedCount)} 位</dd></div>
            <div><dt>当前加权已用</dt><dd>{tokenText(totalUsed)}</dd></div>
            <div><dt>未加权累计</dt><dd>{tokenText(totalRaw)}</dd></div>
          </dl>
        </div>
        {draft?.action === "add_bonus" ? (
          <label className="field">
            <span>追加 Token</span>
            <div className="token-input-control">
              <input
                ref={tokenInputRef}
                aria-label="追加 Token"
                type="number"
                inputMode="numeric"
                min={1}
                max={1_000_000_000_000}
                step={1}
                required
                placeholder="例如 100000000"
                value={tokenAmount}
                onChange={(event) => {
                  setTokenAmount(event.target.value);
                  setValidationError("");
                }}
              />
              <TokenInputPreview value={tokenAmount} emptyLabel="请输入本周追加额度" />
            </div>
          </label>
        ) : null}
        <label className="field">
          <span>操作原因</span>
          <textarea
            ref={reasonInputRef}
            aria-label="操作原因"
            maxLength={200}
            rows={3}
            required
            value={reason}
            placeholder="说明业务原因或异常情况，最多 200 字"
            onChange={(event) => setReason(event.target.value)}
          />
        </label>
        {confirmPhrase ? (
          <label className="field confirmation-field">
            <span>输入“{confirmPhrase}”后继续</span>
            <input
              ref={confirmationInputRef}
              value={confirmation}
              required
              autoComplete="off"
              onChange={(event) => setConfirmation(event.target.value)}
            />
          </label>
        ) : null}
        <div className="inline-notice">
          {draft?.action === "add_bonus"
            ? "追加额度只在当前自然周有效，下周一 00:00 自动回到基础额度。"
            : "系统会记录本次抵扣基准；后续新增 Token 仍会继续计入本周已用量。"}
        </div>
        <LegacyFormError error={validationError ? new Error(validationError) : error} />
      </div>
    </Modal>
  );
}

function TeamUsageDrawer({
  open,
  team,
  range,
  onClose
}: {
  open: boolean;
  team: TeamUsageRow | null;
  range: UsageRange;
  onClose: () => void;
}) {
  const query = useQuery({
    queryKey: [
      ...teamUsageQueryKey(range),
      "breakdown",
      team?.id ?? ""
    ],
    queryFn: ({ signal }) => readTeamUsageBreakdown(team?.id ?? "", range, signal),
    enabled: open && Boolean(team),
    gcTime: 0,
    retry: false
  });
  return (
    <Drawer
      className="legacy-team-usage-drawer"
      title={<LegacyDialogTitle
        title={team ? team.name + " · Token 用量" : "团队 Token 用量"}
        kicker="TEAM TOKEN ANALYTICS"
        subtitle={usageWindowLabel(range.window) + " · 模型 × 推理强度"}
      />}
      placement="right"
      size={780}
      open={open}
      onClose={onClose}
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      destroyOnHidden
      mask={{ closable: false }}
      footer={<Button onClick={onClose}>关闭</Button>}
    >
      {query.isPending ? (
        <div className="team-usage-skeleton" aria-label="正在加载团队 Token 用量">
          <span /><span /><span /><span />
        </div>
      ) : null}
      {query.isError ? (
        <div className="team-usage-state error">团队用量加载失败：{errorMessage(query.error)}</div>
      ) : null}
      {team && query.data ? <TeamUsageContent team={team} payload={query.data} range={range} /> : null}
    </Drawer>
  );
}

function TeamUsageContent({
  team,
  payload,
  range
}: {
  team: TeamUsageRow;
  payload: TeamUsageBreakdownResponse;
  range: UsageRange;
}) {
  const rawTokens = Number(payload.totals.total_tokens) || 0;
  const weightedTokens = Number(payload.totals.weighted_tokens) || 0;
  const multiplier = rawTokens > 0 ? weightedTokens / rawTokens : 1;
  const models = groupTeamModels(payload.combinations);
  return (
    <div className="team-usage-content">
      <section className="team-detail-summary">
        <div className="team-detail-primary">
          <span>{usageWindowLabel(range.window)}加权 Token</span>
          <strong><LegacyTokenValue value={weightedTokens} /></strong>
          <small>{formatNumber(payload.totals.request_count)} 次调用 · {formatNumber(payload.totals.failed_count)} 次失败</small>
        </div>
        <div className="team-detail-facts">
          <div><span>未加权 Token</span><strong><LegacyTokenValue value={rawTokens} /></strong></div>
          <div><span>平均倍率</span><strong>×{multiplier.toFixed(2)}</strong></div>
          <div><span>当前成员</span><strong>{formatNumber(team.current_user_count)}</strong></div>
          <div><span>活跃成员</span><strong>{formatNumber(team.usage.active_users)}</strong></div>
        </div>
      </section>
      <TeamUsageTrend series={payload.series} />
      <section className="team-combination-section">
        <div className="team-detail-heading">
          <div><h4>模型与推理强度</h4><p className="section-kicker">MODEL × EFFORT</p></div>
          <span>色块表示该模型各推理强度 Token 占比</span>
        </div>
        <div className="team-combination-list">
          {models.length ? models.map((model) => (
            <div className="team-combination-row" key={model.model}>
              <span className="team-combination-label">
                <strong title={model.model}>{model.model}</strong>
                <small>{formatNumber(model.requestCount)} 次调用</small>
              </span>
              <span className="team-combination-progress">
                <span className="account-model-progress" role="group" aria-label={model.model + " 各推理强度 Token 占比"}>
                  {model.efforts.map((effort) => {
                    const lines = modelEffortTooltipLines(model.model, effort);
                    const shareUnits = Math.max(1, Math.min(100, Math.round(effort.sharePercent)));
                    return (
                      <LegacyUsageTooltip key={effort.reasoning_effort} lines={lines}>
                        {(events) => (
                          <button
                            {...events}
                            className={`account-model-progress-segment account-model-effort-${effortColorKey(effort.reasoning_effort)} account-model-share-tens-${Math.floor(shareUnits / 10)} account-model-share-ones-${shareUnits % 10}${effort.sharePercent < 18 ? " compact" : ""}`}
                            type="button"
                            aria-label={lines.join("，")}
                          >
                            <span>{effort.reasoning_effort}</span>
                            <em>{effort.sharePercent.toFixed(1)}%</em>
                          </button>
                        )}
                      </LegacyUsageTooltip>
                    );
                  })}
                </span>
              </span>
              <span className="team-combination-value">
                <strong><LegacyTokenValue value={model.weightedTokens} /></strong>
                <small>加权 Token</small>
              </span>
            </div>
          )) : (
            <div className="team-usage-state">
              <strong>暂无模型明细</strong>
              <span>当前范围内没有成功记录模型与推理强度的调用。</span>
            </div>
          )}
        </div>
      </section>
      <section className="team-member-section">
        <div className="team-detail-heading">
          <div><h4>活跃成员排行</h4><p className="section-kicker">MEMBERS</p></div>
          <span>前 8 位</span>
        </div>
        <div className="team-member-ranking">
          {payload.users.length ? payload.users.slice(0, 8).map((user, index) => (
            <div key={user.user}>
              <span>{String(index + 1).padStart(2, "0")}</span>
              <strong title={user.user}>{user.user}</strong>
              <em>{tokenText(user.weighted_tokens)}</em>
            </div>
          )) : <div className="team-usage-state"><span>当前范围暂无活跃成员</span></div>}
        </div>
      </section>
    </div>
  );
}

function TeamUsageTrend({ series }: { series: TeamUsageSeries }) {
  const values = Array.isArray(series.values) ? series.values.map((value) => Number(value) || 0) : [];
  const buckets = Array.isArray(series.buckets) ? series.buckets : [];
  if (!values.length || !buckets.length) {
    return <div className="team-trend-empty">当前范围暂无趋势数据</div>;
  }
  const width = 640;
  const height = 120;
  const paddingX = 8;
  const paddingY = 10;
  const maximum = Math.max(...values, 0);
  const scaleMaximum = Math.max(maximum, 1);
  const points = values.map((value, index) => ({
    x: values.length === 1 ? width / 2 : paddingX + index * (width - paddingX * 2) / (values.length - 1),
    y: height - paddingY - value * (height - paddingY * 2) / scaleMaximum
  }));
  const lastPoint = points.at(-1) ?? { x: width / 2, y: height - paddingY };
  return (
    <section className="team-trend">
      <div className="team-trend-head"><h4>加权 Token 趋势</h4><span>每 {Math.max(1, Math.round(series.bucket_seconds / 60))} 分钟</span></div>
      <svg viewBox={`0 0 ${width} ${height}`} role="img" aria-label={`团队加权 Token 趋势，最高 ${formatNumber(maximum)} Token`}>
        <line x1={paddingX} y1={height - paddingY} x2={width - paddingX} y2={height - paddingY} />
        <polyline points={points.map((point) => `${point.x.toFixed(2)},${point.y.toFixed(2)}`).join(" ")} />
        <circle cx={lastPoint.x} cy={lastPoint.y} r="4" />
      </svg>
      <div className="team-trend-axis">
        <span>{formatFullTimestamp(series.start_at)}</span>
        <strong>峰值 <LegacyTokenValue value={maximum} /></strong>
        <span>{formatFullTimestamp(series.end_at)}</span>
      </div>
    </section>
  );
}

type TeamModelEffort = TeamCombinationUsage & { sharePercent: number };
type TeamModelRow = {
  model: string;
  requestCount: number;
  weightedTokens: number;
  efforts: TeamModelEffort[];
};

function groupTeamModels(combinations: TeamCombinationUsage[]): TeamModelRow[] {
  const grouped = new Map<string, TeamCombinationUsage[]>();
  combinations.forEach((item) => grouped.set(item.model, [...(grouped.get(item.model) ?? []), item]));
  return [...grouped.entries()].map(([model, efforts]) => {
    const weightedTokens = efforts.reduce((total, effort) => total + Number(effort.weighted_tokens ?? effort.total_tokens), 0);
    return {
      model,
      weightedTokens,
      requestCount: efforts.reduce((total, effort) => total + Number(effort.request_count), 0),
      efforts: efforts.map((effort) => {
        const effortTokens = Number(effort.weighted_tokens ?? effort.total_tokens);
        return {
          ...effort,
          sharePercent: weightedTokens > 0 ? effortTokens * 100 / weightedTokens : 0
        };
      }).sort((left, right) => right.sharePercent - left.sharePercent || left.reasoning_effort.localeCompare(right.reasoning_effort))
    };
  }).sort((left, right) => right.weightedTokens - left.weightedTokens || left.model.localeCompare(right.model));
}

function lifecycleCopy(action: LifecycleAction) {
  if (action.kind === "rotate") {
    return {
      title: "轮换 Key？",
      message: "旧 Key 将立即失效，新 Key 只展示一次。",
      okText: "确认轮换",
      danger: false
    };
  }
  if (action.kind === "reset-password") {
    return {
      title: "重置用户密码？",
      message: action.user.email + " 将恢复为系统默认初始密码，现有登录会话会立即失效；下次登录必须修改密码。",
      okText: "确认重置",
      danger: true
    };
  }
  if (action.kind === "revoke") {
    return {
      title: "停用用户的 API Key？",
      message: action.user.email + " 的统一 API Key 会立即失效。",
      okText: "全部停用",
      danger: true
    };
  }
  return {
    title: "删除用户与 API Key？",
    message: action.user.active_keys
      ? `${action.user.email} 将从管理列表移除，其 ${action.user.active_keys} 个有效 Key 会立即失效。历史用量与签发审计仍会保留。`
      : `${action.user.email} 将从管理列表移除。历史用量与签发审计仍会保留。`,
    okText: "删除用户",
    danger: true
  };
}

function accountSortValue(account: UserAccountDetail, field: UserAccountSortField): string | number | null {
  if (field === "account") return account.account;
  if (field === "status") return statusRank(account.status);
  if (field === "requests") return account.usage.request_count;
  if (field === "input_tokens") return account.usage.input_tokens;
  if (field === "output_tokens") return account.usage.output_tokens;
  if (field === "reasoning_tokens") return account.usage.reasoning_tokens;
  if (field === "cached_tokens") return account.usage.cached_tokens;
  if (field === "total_tokens") return account.usage.total_tokens;
  if (field === "weighted_tokens") return account.usage.weighted_tokens;
  return account.usage.last_used_at || null;
}

function breakdownSortValue(row: UsageCombination, field: BreakdownSortField, totalWeighted: number): string | number | null {
  const weighted = row.weighted_tokens ?? row.total_tokens;
  if (field === "account") return row.account || "";
  if (field === "combination") return row.model + ":" + row.reasoning_effort;
  if (field === "success_count") return row.success_count;
  if (field === "share") return totalWeighted > 0 ? weighted / totalWeighted : 0;
  if (field === "total_tokens") return row.total_tokens;
  if (field === "multiplier") return row.total_tokens > 0 ? weighted / row.total_tokens : 0;
  if (field === "weighted_tokens") return weighted;
  if (field === "average_total") return row.success_count > 0 ? row.total_tokens / row.success_count : 0;
  return row.last_used_at || null;
}

function compareRows(
  left: string | number | null,
  right: string | number | null,
  direction: SortDirection,
  leftFallback: string,
  rightFallback: string
) {
  if (left == null && right != null) return 1;
  if (left != null && right == null) return -1;
  let comparison = 0;
  if (typeof left === "number" && typeof right === "number") comparison = left - right;
  else comparison = String(left ?? "").localeCompare(String(right ?? ""), "zh-CN");
  if (comparison === 0) comparison = leftFallback.localeCompare(rightFallback, "zh-CN");
  return direction === "desc" ? -comparison : comparison;
}

function statusRank(status: string) {
  return { active: 1, inactive: 2, revoked: 3, missing: 4 }[status] ?? 5;
}

function statusTone(status: string) {
  return status === "active" ? "success" : status === "missing" ? "warning" : "neutral";
}

function statusLabel(status: string) {
  return { active: "启用", inactive: "已停用", revoked: "已吊销", missing: "未创建" }[status] ?? status;
}

function quotaSourceLabel(quota: UserWeeklyQuota) {
  return {
    default: "组织默认",
    user_unlimited: "单独不限额",
    user_custom: "用户自定义"
  }[quota.source] || "额度未知";
}

function quotaPolicyLifetime(quota: UserWeeklyQuota) {
  return quota.personal_policy_reset_enabled
    ? "仅本周生效，下周恢复组织默认"
    : "持续生效，直到手动恢复组织默认";
}

function tokenText(value: number | null | undefined) {
  if (value == null) return "不限额";
  const formatted = formatTokenAmount(Number(value) || 0);
  return formatted.includes(" ") ? formatted : formatted + " Token";
}

function formatPercent(value: number | null | undefined) {
  return value == null ? "—" : Math.max(0, value).toFixed(1) + "%";
}

function formatUsageRatio(value: number | null | undefined, total: number | null | undefined) {
  const denominator = Number(total) || 0;
  if (denominator <= 0) return "0%";
  return new Intl.NumberFormat("zh-CN", { maximumFractionDigits: 1 }).format((Number(value) || 0) * 100 / denominator) + "%";
}

function usageCombinationLabel(row: UsageCombination) {
  const model = row.model && row.model !== "unknown" ? row.model : "未上报模型";
  const effort = row.reasoning_effort && row.reasoning_effort !== "unknown" ? row.reasoning_effort : "未上报";
  return `${model}-${effort}`;
}

function usageWindowLabel(window: UsageWindow) {
  return {
    "3600": "1 小时",
    "21600": "6 小时",
    today: "今日",
    "86400": "24 小时",
    "604800": "7 天",
    "2592000": "30 天",
    current_week: "本周",
    since_reset: "本周期",
    all: "累计",
    custom: "自定义范围"
  }[window] || "当前范围";
}

function formatNumber(value: number | null | undefined) {
  return new Intl.NumberFormat("zh-CN").format(Number(value) || 0);
}

function formatTimestamp(timestamp: number | null | undefined) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  }).format(new Date(timestamp * 1000));
}

function formatFullTimestamp(timestamp: number | null | undefined) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit"
  }).format(new Date(timestamp * 1000));
}

function formatLastUsed(timestamp: number | null | undefined) {
  if (!timestamp) return "从未使用";
  return new Intl.DateTimeFormat("zh-CN", {
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    hour12: false
  }).format(new Date(timestamp * 1000));
}

function effortLabel(value: string) {
  return {
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
  }[value] ?? value;
}

function effortColorKey(effort: string) {
  return ["none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max", "auto"].includes(effort)
    ? effort
    : "unknown";
}

function paginationItems(current: number, total: number): Array<number | "…"> {
  if (total <= 7) return Array.from({ length: total }, (_, index) => index + 1);
  const items: Array<number | "…"> = [1];
  if (current > 4) items.push("…");
  const start = Math.max(2, current - 1);
  const end = Math.min(total - 1, current + 1);
  for (let page = start; page <= end; page += 1) items.push(page);
  if (current < total - 3) items.push("…");
  items.push(total);
  return items;
}

function userRefreshLabel(timestamp: number, cached: boolean) {
  return "用户数据更新于 " + formatLastUsed(timestamp) + (cached ? "（缓存）" : "");
}

function errorMessage(error: unknown) {
  return error instanceof ApiError || error instanceof Error ? error.message : "请刷新后重试";
}

function isInteractiveRowTarget(target: EventTarget | null) {
  return target instanceof Element && Boolean(target.closest("button, a, input, select, textarea, label, [role='button']"));
}

async function copyText(value: string) {
  if (!navigator.clipboard) return;
  await navigator.clipboard.writeText(value);
}
