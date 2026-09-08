import { useSiteTimezone, formatSiteTimestamp } from "./site-time";
import { Button, Empty, Modal, Spin, Tooltip } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode
} from "react";

import "./teams-page.css";
import { recentUsageWindows, UsageTimeRangeControl } from "./components/UsageTimeRangeControl";

import { ApiError } from "../api/client";
import {
  createTeam,
  deleteTeam,
  listTeams,
  readTeamUsage,
  teamUsageQueryKey,
  teamsQueryKey,
  updateTeam,
  type Team,
  type TeamUsageRange,
  type TeamUsageRow
} from "../api/teams";
import {
  assignUsersTeam,
  listTeamMembers,
  usersQueryRoot,
  type TeamMemberListParams,
  type UserSummary
} from "../api/users";
import { useAdminToolbar } from "./AdminToolbarContext";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";
import { NativeTableViewport } from "./components/NativeTableViewport";
import { LegacyToastRegion, useLegacyToasts } from "./components/LegacyToast";
import { formatTokenAmount } from "./formatters";
import { teamTagClassName } from "./teamTagStyles";

const teamUsageWindows = [
  ...recentUsageWindows.filter((option) => option.value !== "21600"),
  { value: "current_week", label: "本周", title: "系统时区本周一 00:00 起的团队用量" },
  { value: "all", label: "全部" }
] as const;
type TeamWindow = (typeof teamUsageWindows)[number]["value"];

type TeamStatus = "all" | "active" | "empty";
type TeamSortField = "name" | "members" | "active_users" | "weighted_tokens" | "updated_at";
type TeamSort = { field: TeamSortField; direction: "asc" | "desc" };
const teamNameCollator = new Intl.Collator("zh-CN", { numeric: true, sensitivity: "base" });
type MemberScope = "current" | "unassigned" | "all";
type UsageState = "all" | "used" | "unused";
type MemberWindow = "today" | "604800" | "2592000" | "all";
type AssignmentMode = "join" | "remove" | "move";

type TeamEditorState = { mode: "create" | "edit"; team?: Team } | null;
type MemberCriteria = {
  query: string;
  scope: MemberScope;
  usageState: UsageState;
  window: MemberWindow;
};
type AssignmentConfirm = {
  mode: AssignmentMode;
  team: Team;
  users: Array<[string, string | null]>;
};

const defaultMemberCriteria: MemberCriteria = {
  query: "",
  scope: "current",
  usageState: "all",
  window: "all"
};

function initialMemberCriteria(team: Team | null): MemberCriteria {
  return {
    ...defaultMemberCriteria,
    scope: team?.user_count === 0 ? "unassigned" : "current"
  };
}

export function TeamsPage({ csrfToken }: { csrfToken: string }) {
  const siteTimezone = useSiteTimezone();
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const [search, setSearch] = useState("");
  const [usageWindow, setUsageWindow] = useState<TeamWindow>("all");
  const [status, setStatus] = useState<TeamStatus>("all");
  const [sort, setSort] = useState<TeamSort | null>(null);
  const [editor, setEditor] = useState<TeamEditorState>(null);
  const [deleting, setDeleting] = useState<Team | null>(null);
  const [memberTeam, setMemberTeam] = useState<Team | null>(null);
  const reportedCatalogError = useRef<unknown>(null);
  const deletingRef = useRef(false);

  const teams = useQuery({
    queryKey: teamsQueryKey,
    queryFn: ({ signal }) => listTeams(signal),
    retry: false,
    refetchOnWindowFocus: false
  });
  const selectedUsageRange = useMemo<TeamUsageRange>(() => ({ window: usageWindow }), [usageWindow]);
  const teamUsage = useQuery({
    queryKey: [...teamUsageQueryKey(selectedUsageRange), siteTimezone],
    queryFn: ({ signal }) => readTeamUsage(selectedUsageRange, signal),
    enabled: teams.isSuccess,
    retry: false,
    refetchOnWindowFocus: false
  });

  const refreshWorkspace = useCallback(async (fresh = true) => {
    setRefreshing(true);
    try {
      const usageRange = { ...selectedUsageRange, fresh };
      const teamResult = await queryClient.fetchQuery({
        queryKey: teamsQueryKey,
        queryFn: ({ signal }) => listTeams(signal),
        staleTime: 0
      });
      queryClient.setQueryData(teamsQueryKey, teamResult);
      const usageResult = await queryClient.fetchQuery({
        queryKey: [...teamUsageQueryKey(usageRange), siteTimezone],
        queryFn: ({ signal }) => readTeamUsage(usageRange, signal),
        staleTime: 0
      });
      queryClient.setQueryData([...teamUsageQueryKey(selectedUsageRange), siteTimezone], usageResult);
      setRefreshLabel("团队数据已刷新");
      showToast("数据已刷新");
    } catch (error) {
      setRefreshLabel("刷新失败");
      throw error;
    } finally {
      setRefreshing(false);
    }
  }, [selectedUsageRange, siteTimezone, queryClient, setRefreshLabel, setRefreshing, showToast]);

  useEffect(() => {
    setRefreshAction(() => refreshWorkspace(true));
    return () => setRefreshAction(null);
  }, [refreshWorkspace, setRefreshAction]);
  useEffect(() => {
    setRefreshing(teams.isFetching || teamUsage.isFetching);
  }, [teamUsage.isFetching, setRefreshing, teams.isFetching]);
  useEffect(() => {
    if (!teams.isSuccess || teamUsage.isFetching) return;
    reportedCatalogError.current = null;
    setRefreshLabel(teamUsage.isError ? "团队用量加载失败" : "团队数据已刷新");
  }, [teamUsage.isError, teamUsage.isFetching, teamUsage.status, setRefreshLabel, teams.isSuccess]);
  useEffect(() => {
    if (!teams.isError || reportedCatalogError.current === teams.error) return;
    reportedCatalogError.current = teams.error;
    setRefreshLabel("刷新失败");
    showToast(errorMessage(teams.error), "error");
  }, [setRefreshLabel, showToast, teams.error, teams.isError]);
  useEffect(() => () => {
    setRefreshing(false);
    setRefreshLabel("");
  }, [setRefreshLabel, setRefreshing]);

  const usageByTeam = useMemo(
    () => new Map((teamUsage.isError ? [] : teamUsage.data?.teams ?? []).map((team) => [team.id, team])),
    [teamUsage.data?.teams, teamUsage.isError]
  );
  const visibleTeams = useMemo(() => {
    const normalized = search.trim().toLowerCase();
    const filtered = (teams.data?.teams ?? []).filter((team) => {
      const matchesSearch = !normalized || `${team.name} ${team.description || ""}`.toLowerCase().includes(normalized);
      const matchesStatus = status === "all" || (status === "active" ? team.user_count > 0 : team.user_count === 0);
      return matchesSearch && matchesStatus;
    });
    if (!sort) return filtered;
    const valueForTeam = (team: Team) => {
      switch (sort.field) {
        case "name": return team.name;
        case "members": return team.user_count;
        case "updated_at": return team.updated_at;
        default: return usageByTeam.get(team.id)?.usage[sort.field];
      }
    };
    return filtered.sort((left, right) => {
      const leftValue = valueForTeam(left);
      const rightValue = valueForTeam(right);
      const tieBreak = teamNameCollator.compare(left.name, right.name) || left.id.localeCompare(right.id);
      // Missing usage stays at the bottom in either direction.
      if (leftValue == null) return rightValue == null ? tieBreak : 1;
      if (rightValue == null) return -1;
      const comparison = typeof leftValue === "string" && typeof rightValue === "string"
        ? teamNameCollator.compare(leftValue, rightValue)
        : Number(leftValue) - Number(rightValue);
      return comparison * (sort.direction === "asc" ? 1 : -1) || tieBreak;
    });
  }, [search, status, teams.data?.teams, usageByTeam, sort]);

  const changeSort = (field: TeamSortField) => {
    setSort((current) => ({
      field,
      direction: current?.field === field
        ? current.direction === "asc" ? "desc" : "asc"
        : field === "name" ? "asc" : "desc"
    }));
  };

  const rangeLabel = usageWindow === "all" ? "全部历史" : teamUsageWindows.find((option) => option.value === usageWindow)?.label;
  const rangeBoundary = (timestamp: number | null | undefined, unbounded = false) => {
    if (teamUsage.isPending) return "…";
    if (teamUsage.isError || !teamUsage.data) return "—";
    if (timestamp == null) return unbounded ? "不限" : "—";
    return formatSiteTimestamp(timestamp);
  };

  const refreshAfterCatalogMutation = async () => {
    const teamResult = await queryClient.fetchQuery({
      queryKey: teamsQueryKey,
      queryFn: ({ signal }) => listTeams(signal),
      staleTime: 0
    });
    queryClient.setQueryData(teamsQueryKey, teamResult);
    const usageResult = await queryClient.fetchQuery({
      queryKey: [...teamUsageQueryKey(selectedUsageRange), siteTimezone],
      queryFn: ({ signal }) => readTeamUsage(selectedUsageRange, signal),
      staleTime: 0
    });
    queryClient.setQueryData([...teamUsageQueryKey(selectedUsageRange), siteTimezone], usageResult);
    await queryClient.invalidateQueries({ queryKey: usersQueryRoot, refetchType: "none" });
  };

  const deleteMutation = useMutation({
    mutationFn: (team: Team) => deleteTeam(team.id, csrfToken),
    onSuccess: async (result) => {
      setDeleting(null);
      showToast(result.message);
      await refreshAfterCatalogMutation();
    },
    onError: (error) => {
      setDeleting(null);
      showToast(errorMessage(error), "error");
    },
    onSettled: () => { deletingRef.current = false; }
  });

  return (
    <section className="page-content legacy-team-page" aria-label="团队管理">
      <div className="panel table-panel organization-catalog-panel" id="organization-teams-panel">
        <div className="organization-table-toolbar">
          <div className="team-time-filter">
            <UsageTimeRangeControl
              className="team-usage-time-control"
              label="团队用量时间范围"
              value={usageWindow}
              options={teamUsageWindows}
              onChange={setUsageWindow}
            />
            <div className="overview-token-window-boundaries" aria-label="团队用量时间边界" aria-live="polite" aria-busy={teamUsage.isPending}>
              <div className="overview-token-window-value"><small>起始时间</small><strong>{rangeBoundary(teamUsage.data?.window_start_at, usageWindow === "all")}</strong></div>
              <div className="overview-token-window-value"><small>结束时间</small><strong>{rangeBoundary(teamUsage.data?.window_end_at)}</strong></div>
            </div>
          </div>
          <div className="team-catalog-controls">
            <div className="organization-table-filters">
              <div className="team-search-filter">
                <label htmlFor="team-search">团队</label>
                <div className="search-field">
                  <span aria-hidden="true">⌕</span>
                  <input
                    id="team-search"
                    type="search"
                    aria-label="搜索团队名称或说明"
                    placeholder="搜索团队名称或说明"
                    value={search}
                    onChange={(event) => setSearch(event.target.value)}
                  />
                </div>
              </div>
              <div className="organization-status-filter">
                <span className="team-filter-label">团队状态</span>
                <LegacyEnhancedSelect
                  label="团队状态"
                  value={status}
                  options={[
                    { value: "all", label: "全部团队" },
                    { value: "active", label: "有成员" },
                    { value: "empty", label: "空团队" }
                  ]}
                  onChange={setStatus}
                />
              </div>
            </div>
            <div className="team-catalog-actions">
              <button className="button primary" type="button" onClick={() => setEditor({ mode: "create" })}>创建团队</button>
            </div>
          </div>
        </div>
        {teamUsage.isError ? <p className="organization-error form-error" role="alert">团队用量加载失败，请刷新重试</p> : null}
        <NativeTableViewport className="table-wrap organization-catalog-table-wrap" aria-label="团队目录表格">
          <table className="organization-catalog-table">
            <thead>
              <tr>
                <th className="table-index-column">序号</th>
                <TeamSortHeader field="name" label="团队" sort={sort} onSort={changeSort} />
                <TeamSortHeader field="members" label="当前成员" sort={sort} onSort={changeSort} />
                <TeamSortHeader field="active_users" label="活跃成员" sort={sort} onSort={changeSort} />
                <TeamSortHeader field="weighted_tokens" label="Token 用量" sort={sort} onSort={changeSort} />
                <TeamSortHeader field="updated_at" label="更新时间" sort={sort} onSort={changeSort} />
                <th className="organization-action-column">操作</th>
              </tr>
            </thead>
            <tbody>
              {visibleTeams.map((team, index) => (
                <TeamRow
                  key={team.id}
                  index={index + 1}
                  team={team}
                  usage={usageByTeam.get(team.id)}
                  usagePending={teamUsage.isPending}
                  rangeLabel={rangeLabel}
                  onMembers={() => setMemberTeam(team)}
                  onEdit={() => setEditor({ mode: "edit", team })}
                  onDelete={() => {
                    deletingRef.current = false;
                    deleteMutation.reset();
                    setDeleting(team);
                  }}
                />
              ))}
              {teams.isSuccess && visibleTeams.length === 0 ? (
                <tr><td colSpan={7} className="team-usage-state">没有匹配的团队</td></tr>
              ) : null}
            </tbody>
          </table>
        </NativeTableViewport>
      </div>

      <TeamEditorModal
        state={editor}
        csrfToken={csrfToken}
        onClose={() => setEditor(null)}
        onSaved={async (message) => {
          setEditor(null);
          showToast(message);
          await refreshAfterCatalogMutation();
        }}
      />
      <LegacyConfirmModal
        title={deleting ? `删除“${deleting.name}”` : "删除团队"}
        open={deleting !== null}
        okText="确认删除"
        danger
        pending={deleteMutation.isPending}
        onCancel={() => !deleteMutation.isPending && setDeleting(null)}
        onConfirm={() => {
          if (!deleting || deletingRef.current) return;
          const target = deleting;
          deletingRef.current = true;
          setDeleting(null);
          deleteMutation.mutate(target);
        }}
      >
        空团队删除后无法恢复。
      </LegacyConfirmModal>
      <TeamMembersModal
        key={memberTeam?.id ?? "closed"}
        team={memberTeam}
        csrfToken={csrfToken}
        onClose={() => setMemberTeam(null)}
        onCatalogRefresh={refreshAfterCatalogMutation}
        onToast={showToast}
      />
      <LegacyToastRegion toasts={toasts} />
    </section>
  );
}

function TeamSortHeader({ field, label, sort, onSort }: {
  field: TeamSortField;
  label: string;
  sort: TeamSort | null;
  onSort: (field: TeamSortField) => void;
}) {
  const active = sort?.field === field;
  const tokenColumn = field === "weighted_tokens";
  const description = active
    ? `${label}，当前${sort.direction === "asc" ? "升序" : "降序"}，点击切换排序方向`
    : `${label}，点击排序`;
  return (
    <th scope="col" aria-sort={active ? sort.direction === "asc" ? "ascending" : "descending" : "none"} className={tokenColumn ? "team-token-column" : undefined}>
      <button type="button" className={`legacy-sort-button${active ? " active" : ""}`} title={description} aria-label={description} onClick={() => onSort(field)}>
        <span>{label}</span>
      </button>
    </th>
  );
}

function TeamRow({ index, team, usage, usagePending, rangeLabel, onMembers, onEdit, onDelete }: {
  index: number;
  team: Team;
  usage?: TeamUsageRow;
  usagePending: boolean;
  rangeLabel?: string;
  onMembers: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  return (
    <tr>
      <td className="table-index-cell">{index}</td>
      <td><span className="organization-catalog-name"><strong title={team.name}><span className={teamTagClassName(team.tag_style)}>{team.name}</span></strong>{team.description?.trim() ? <small title={team.description}>{team.description}</small> : null}</span></td>
      <td className="number-cell">{formatNumber(team.user_count)}</td>
      <td className="number-cell">{usage ? formatNumber(usage.usage.active_users) : <span className="team-usage-placeholder">{usagePending ? "…" : "—"}</span>}</td>
      <td className="number-cell token-total team-token-cell">
        <div className="user-token-summary">
          <div className="user-token-stat user-token-weighted">
            <span>{rangeLabel}加权</span>
            {usage ? <LegacyTokenValue value={usage.usage.weighted_tokens} /> : <span className="team-usage-placeholder">{usagePending ? "…" : "—"}</span>}
          </div>
          <div className="user-token-stat user-token-current">
            <span>{rangeLabel}未加权</span>
            {usage ? <LegacyTokenValue value={usage.usage.total_tokens} /> : <span className="team-usage-placeholder">{usagePending ? "…" : "—"}</span>}
          </div>
        </div>
      </td>
      <td>{formatSiteTimestamp(team.updated_at)}</td>
      <td>
        <div className="organization-row-actions">
          <button className="inline-action" type="button" onClick={onMembers}>成员</button>
          <button className="inline-action" type="button" onClick={onEdit}>编辑</button>
          <button className="inline-action danger-text" type="button" disabled={team.user_count > 0} title={team.user_count > 0 ? "请先移出团队成员" : undefined} onClick={onDelete}>删除</button>
        </div>
      </td>
    </tr>
  );
}

function TeamEditorModal({ state, csrfToken, onClose, onSaved }: {
  state: TeamEditorState;
  csrfToken: string;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const nameRef = useRef<HTMLInputElement>(null);
  const submittingRef = useRef(false);
  const mutation = useMutation({
    mutationFn: () => state?.mode === "edit" && state.team
      ? updateTeam({ id: state.team.id, name, description }, csrfToken)
      : createTeam({ name, description }, csrfToken),
    onSuccess: (result) => onSaved(result.message),
    onSettled: () => { submittingRef.current = false; }
  });
  useEffect(() => {
    if (!state) return;
    setName(state.team?.name ?? "");
    setDescription(state.team?.description ?? "");
    submittingRef.current = false;
    mutation.reset();
    const focusTimer = window.setTimeout(() => nameRef.current?.focus(), 0);
    return () => window.clearTimeout(focusTimer);
  }, [state?.mode, state?.team?.id]); // eslint-disable-line react-hooks/exhaustive-deps
  const editing = state?.mode === "edit";
  const submit = () => {
    if (submittingRef.current || !nameRef.current?.reportValidity()) return;
    submittingRef.current = true;
    mutation.mutate();
  };
  return (
    <Modal
      className="legacy-user-form-modal legacy-team-editor-modal"
      title={<LegacyDialogTitle title={`${editing ? "编辑" : "创建"}团队`} kicker="TEAM CATALOG" />}
      open={state !== null}
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      okText={`${editing ? "保存" : "创建"}团队`}
      cancelText="取消"
      okButtonProps={{ disabled: mutation.isPending }}
      onCancel={() => !mutation.isPending && onClose()}
      onOk={submit}
      afterOpenChange={(open) => open && nameRef.current?.focus()}
      destroyOnHidden
      mask={{ closable: false }}
    >
      <div className="legacy-user-form-body legacy-team-editor-body">
        <label className="field"><span>团队名称</span><input ref={nameRef} aria-label="团队名称" maxLength={64} required value={name} onChange={(event) => setName(event.target.value)} /></label>
        <label className="field"><span>团队说明</span><textarea aria-label="团队说明" maxLength={200} rows={3} placeholder="说明团队职责或成员范围（可选）" value={description} onChange={(event) => setDescription(event.target.value)} /></label>
        <div className="inline-notice">每位用户只能属于一个团队；报表按当前成员动态汇总所选范围内的 Token。</div>
        <LegacyFormError error={mutation.error} />
      </div>
    </Modal>
  );
}

function TeamMembersModal({ team, csrfToken, onClose, onCatalogRefresh, onToast }: {
  team: Team | null;
  csrfToken: string;
  onClose: () => void;
  onCatalogRefresh: () => Promise<void>;
  onToast: (message: string, kind?: "success" | "error") => void;
}) {
  const [draftCriteria, setDraftCriteria] = useState<MemberCriteria>(() => initialMemberCriteria(team));
  const [criteria, setCriteria] = useState<MemberCriteria>(() => initialMemberCriteria(team));
  const [page, setPage] = useState(1);
  const selectedRef = useRef<Map<string, string | null>>(new Map());
  const [renderedSelected, setRenderedSelected] = useState<Map<string, string | null>>(new Map());
  const [error, setError] = useState("");
  const [confirm, setConfirm] = useState<AssignmentConfirm | null>(null);
  const [assigning, setAssigning] = useState(false);
  const assigningRef = useRef(false);
  const [committedMembers, setCommittedMembers] = useState<Awaited<ReturnType<typeof listTeamMembers>> | null>(null);
  const openTeamID = team?.id ?? "";

  useEffect(() => {
    if (!team) return;
    const emptySelection = new Map<string, string | null>();
    selectedRef.current = emptySelection;
    setRenderedSelected(emptySelection);
    setError("");
    setConfirm(null);
    assigningRef.current = false;
  }, [openTeamID]);
  useEffect(() => {
    if (!team || sameCriteria(draftCriteria, criteria)) return;
    const timer = window.setTimeout(() => {
      setCriteria(draftCriteria);
      setPage(1);
      const emptySelection = new Map<string, string | null>();
      selectedRef.current = emptySelection;
      setRenderedSelected(emptySelection);
      setError("");
    }, 220);
    return () => window.clearTimeout(timer);
  }, [criteria, draftCriteria, team]);

  const memberParams = useMemo<TeamMemberListParams>(() => ({
    query: criteria.query.trim(),
    teamId: criteria.scope === "current" ? openTeamID : criteria.scope === "unassigned" ? "unassigned" : "",
    usageState: criteria.usageState,
    window: criteria.window,
    page,
    pageSize: 50
  }), [criteria, openTeamID, page]);
  const memberWorkspace = useQuery({
    queryKey: [
      "teams",
      "member-workspace",
      openTeamID,
      memberParams.query,
      memberParams.teamId,
      memberParams.usageState,
      memberParams.window,
      memberParams.page,
      memberParams.pageSize
    ],
    queryFn: ({ signal }) => listTeamMembers(memberParams, signal),
    enabled: Boolean(team),
    placeholderData: (previous) => previous,
    retry: false,
    refetchOnWindowFocus: false
  });
  useEffect(() => {
    if (!memberWorkspace.isFetching) return;
    setError("");
  }, [memberWorkspace.dataUpdatedAt, memberWorkspace.isFetching]);
  useEffect(() => {
    if (memberWorkspace.isError) setError(errorMessage(memberWorkspace.error));
  }, [memberWorkspace.error, memberWorkspace.isError]);
  useEffect(() => {
    if (!memberWorkspace.isSuccess || memberWorkspace.isPlaceholderData) return;
    setCommittedMembers(memberWorkspace.data);
  }, [memberWorkspace.data, memberWorkspace.isPlaceholderData, memberWorkspace.isSuccess]);

  const displayedMembers = memberWorkspace.data ?? committedMembers;
  const visibleUsers = displayedMembers?.users ?? [];
  const pagination = displayedMembers?.pagination ?? { page: 1, page_size: 50, total: 0, total_pages: 1 };
  const showInitialMemberLoading = memberWorkspace.isPending && committedMembers === null;
  const everyVisible = visibleUsers.length > 0 && visibleUsers.every((user) => renderedSelected.has(user.email));
  const anyVisible = visibleUsers.some((user) => renderedSelected.has(user.email));

  const toggleVisible = (checked: boolean) => {
    const next = new Map(selectedRef.current);
    visibleUsers.forEach((user) => checked ? next.set(user.email, user.team_id ?? null) : next.delete(user.email));
    selectedRef.current = next;
    setRenderedSelected(next);
  };
  const toggleUser = (user: UserSummary, checked: boolean) => {
    const next = new Map(selectedRef.current);
    if (checked) next.set(user.email, user.team_id ?? null);
    else next.delete(user.email);
    selectedRef.current = next;
    setRenderedSelected(next);
  };

  const beginAssignment = (mode: AssignmentMode) => {
    if (!team || selectedRef.current.size === 0 || assigningRef.current) return;
    const users = [...selectedRef.current.entries()];
    const conflicts = users.filter(([, currentTeam]) => currentTeam && currentTeam !== team.id);
    if (mode === "join" && conflicts.length) {
      setError(`有 ${formatNumber(conflicts.length)} 位用户已在其他团队；请将用户范围切换为“仅未分组”，或先移出原团队。`);
      return;
    }
    const eligible = users.filter(([, currentTeam]) => mode === "remove" ? currentTeam === team.id : mode === "move" ? Boolean(currentTeam && currentTeam !== team.id) : currentTeam === null);
    if (!eligible.length) {
      setError(mode === "remove" ? "所选用户已不在当前团队" : mode === "move" ? "没有属于其他团队的用户" : "没有可直接加入的未分组用户");
      return;
    }
    setError("");
    setConfirm({ mode, team, users: eligible });
  };

  const submitAssignment = async () => {
    if (!confirm || assigningRef.current) return;
    const assignment = confirm;
    assigningRef.current = true;
    setAssigning(true);
    setConfirm(null);
    try {
      const groups = new Map<string | null, string[]>();
      for (const [email, expectedTeam] of assignment.users) {
        const emails = groups.get(expectedTeam) ?? [];
        emails.push(email);
        groups.set(expectedTeam, emails);
      }
      for (const [expectedTeam, emails] of groups) {
        for (let offset = 0; offset < emails.length; offset += 500) {
          await assignUsersTeam(emails.slice(offset, offset + 500), assignment.mode === "remove" ? null : assignment.team.id, csrfToken, expectedTeam);
        }
      }
      const count = assignment.users.length;
      const emptySelection = new Map<string, string | null>();
      selectedRef.current = emptySelection;
      setRenderedSelected(emptySelection);
      onToast(`已更新 ${count} 位用户的团队归属`);

      const refreshes: Array<Promise<unknown>> = [onCatalogRefresh()];
      if (assignment.mode === "join" && criteria.scope === "unassigned") {
        // Empty teams initially show joinable users. After a successful join,
        // move to the member view so the updated relationship remains visible
        // instead of making the row appear to vanish from the old filter.
        const nextCriteria = { ...draftCriteria, scope: "current" as const };
        setDraftCriteria(nextCriteria);
        setCriteria(nextCriteria);
        setPage(1);
      } else {
        refreshes.push(memberWorkspace.refetch().then((result) => {
          if (result.isError) throw result.error;
        }));
      }
      const refreshResults = await Promise.allSettled(refreshes);
      if (refreshResults.some((result) => result.status === "rejected")) {
        setError("团队归属已更新，但最新状态刷新失败，请刷新页面后确认。");
      }
    } catch (assignmentError) {
      setError(errorMessage(assignmentError));
    } finally {
      assigningRef.current = false;
      setAssigning(false);
    }
  };

  const actionLabel = confirm?.mode === "remove" ? "移出" : confirm?.mode === "move" ? "移动到" : "加入";
  return (
    <>
      <Modal
        className="legacy-user-form-modal legacy-organization-members-modal"
        title={<LegacyDialogTitle title={team ? `${team.name} · 成员管理` : "团队成员"} kicker="TEAM MEMBERS" />}
        open={team !== null}
        width={1240}
        centered
        closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
        transitionName=""
        maskTransitionName=""
        onCancel={() => !assigning && onClose()}
        destroyOnHidden
        mask={{ closable: false }}
        footer={[<Button key="done" onClick={onClose}>完成</Button>]}
      >
        <div className="organization-members-body">
          <div className="organization-member-toolbar">
            <label className="search-field"><span aria-hidden="true">⌕</span><input type="search" aria-label="搜索用户邮箱" placeholder="搜索用户邮箱" value={draftCriteria.query} onChange={(event) => setDraftCriteria((current) => ({ ...current, query: event.target.value }))} onKeyDown={(event) => { if (event.key !== "Enter") return; event.preventDefault(); const nextCriteria = { ...draftCriteria, query: event.currentTarget.value }; setCriteria(nextCriteria); setPage(1); const emptySelection = new Map<string, string | null>(); selectedRef.current = emptySelection; setRenderedSelected(emptySelection); setError(""); }} /></label>
            <label className="window-field filter-field"><span>成员范围</span><LegacyEnhancedSelect id="organization-user-scope-react" label="成员范围" value={draftCriteria.scope} options={[{ value: "current", label: "当前团队成员" }, { value: "unassigned", label: "未分组用户" }, { value: "all", label: "全部用户" }]} onChange={(scope) => setDraftCriteria((current) => ({ ...current, scope }))} /></label>
            <label className="window-field filter-field"><span>Token 状态</span><LegacyEnhancedSelect id="organization-usage-state-react" label="Token 状态" value={draftCriteria.usageState} options={[{ value: "all", label: "不限用量" }, { value: "used", label: "已产生 Token" }, { value: "unused", label: "未产生 Token" }]} onChange={(usageState) => setDraftCriteria((current) => ({ ...current, usageState }))} /></label>
            <label className="window-field filter-field"><span>统计范围</span><LegacyEnhancedSelect id="organization-usage-window-react" label="统计范围" value={draftCriteria.window} options={[{ value: "today", label: "今日" }, { value: "604800", label: "近 7 天" }, { value: "2592000", label: "近 30 天" }, { value: "all", label: "全部历史" }]} onChange={(window) => setDraftCriteria((current) => ({ ...current, window }))} /></label>
          </div>
          <NativeTableViewport className="organization-member-table-wrap" aria-label="团队成员表格">
            <table className={`organization-member-table${showInitialMemberLoading || visibleUsers.length === 0 ? " is-state" : ""}`}>
              <thead><tr><th className="table-index-column">序号</th><th className="user-select-column"><IndeterminateCheckbox ariaLabel="选择本页用户" checked={everyVisible} indeterminate={!everyVisible && anyVisible} onChange={toggleVisible} /></th><th>用户</th><th>团队归属</th><th>Token 用量</th><th>{team ? `与“${team.name}”的关系` : "与当前团队的关系"}</th></tr></thead>
              <tbody>
                {showInitialMemberLoading ? <tr><td colSpan={6} className="organization-member-state"><span role="status" className="organization-member-state-content"><Spin size="small" />正在加载成员…</span></td></tr> : null}
                {!showInitialMemberLoading && visibleUsers.length === 0 ? <tr><td colSpan={6} className="organization-member-state"><div role="status" className="organization-member-state-content"><Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="当前条件没有匹配用户" /></div></td></tr> : null}
                {!showInitialMemberLoading ? visibleUsers.map((user, index) => <MemberRow key={user.email} index={(pagination.page - 1) * pagination.page_size + index + 1} user={user} teamID={openTeamID} checked={renderedSelected.has(user.email)} onChange={(checked) => toggleUser(user, checked)} />) : null}
              </tbody>
            </table>
          </NativeTableViewport>
          {pagination.total > 0 ? <div className="table-pagination organization-pagination"><span className="pagination-summary">共 {formatNumber(pagination.total)} 位匹配用户；批量操作仅作用于已勾选用户</span><div className="pagination-controls"><button className="pagination-nav" type="button" disabled={pagination.page <= 1} onClick={() => { setRenderedSelected(new Map(selectedRef.current)); setPage((current) => Math.max(1, current - 1)); }}>上一页</button><span>{pagination.page} / {pagination.total_pages}</span><button className="pagination-nav" type="button" disabled={pagination.page >= pagination.total_pages} onClick={() => { setRenderedSelected(new Map(selectedRef.current)); setPage((current) => current + 1); }}>下一页</button></div></div> : null}
          {renderedSelected.size > 0 ? <div className="organization-bulk-bar"><div><strong>已选择 {formatNumber(renderedSelected.size)} 位用户</strong><small>已有团队成员不会被静默移动。</small></div><button className="button ghost" type="button" onClick={() => { const emptySelection = new Map<string, string | null>(); selectedRef.current = emptySelection; setRenderedSelected(emptySelection); }}>取消选择</button><button className="button secondary" type="button" onClick={() => beginAssignment("remove")}>移出当前团队</button><button className="button secondary" type="button" onClick={() => beginAssignment("move")}>从其他团队移动</button><button className="button primary" type="button" onClick={() => beginAssignment("join")}>加入当前团队</button></div> : null}
          <p className="form-error organization-error" role="alert">{error}</p>
        </div>
      </Modal>
      <LegacyConfirmModal title={confirm ? `${actionLabel}“${confirm.team.name}”` : "更新团队成员"} open={confirm !== null} okText={`确认${actionLabel}`} danger={confirm?.mode !== "join"} pending={assigning} onCancel={() => !assigning && setConfirm(null)} onConfirm={() => void submitAssignment()}>
        {confirm ? `${confirm.users.length} 位用户将${confirm.mode === "remove" ? "变为未分组" : confirm.mode === "move" ? "从原团队移动到该团队" : "加入该团队"}。保存后，所选统计范围内这些用户的 Token 会立即按当前团队重新汇总；历史事件本身不会改写。` : ""}
      </LegacyConfirmModal>
    </>
  );
}

function MemberRow({ index, user, teamID, checked, onChange }: { index: number; user: UserSummary; teamID: string; checked: boolean; onChange: (checked: boolean) => void }) {
  const conflict = Boolean(user.team_id && user.team_id !== teamID);
  const current = user.team_id === teamID;
  return <tr><td className="table-index-cell">{index}</td><td><input type="checkbox" aria-label={`选择 ${user.email}`} checked={checked} onChange={(event) => onChange(event.target.checked)} /></td><td><span className="table-primary">{user.email}</span></td><td>{user.team ? <span className={teamTagClassName(user.team.tag_style)}>{user.team.name}</span> : <span className={teamTagClassName(null, true)}>未分组</span>}</td><td className="number-cell token-total"><LegacyTokenValue value={user.usage?.weighted_tokens ?? 0} /></td><td><span className={`status-chip ${conflict ? "warning" : current ? "success" : "neutral"}`}>{conflict ? "属于其他团队" : current ? "本团队成员" : "尚未加入"}</span></td></tr>;
}

function IndeterminateCheckbox({ ariaLabel, checked, indeterminate, onChange }: { ariaLabel: string; checked: boolean; indeterminate: boolean; onChange: (checked: boolean) => void }) {
  const ref = useRef<HTMLInputElement>(null);
  useEffect(() => { if (ref.current) ref.current.indeterminate = indeterminate; }, [indeterminate]);
  return <input ref={ref} type="checkbox" aria-label={ariaLabel} checked={checked} onChange={(event) => onChange(event.target.checked)} />;
}

function LegacyTokenValue({ value }: { value: number }) {
  const amount = Number(value) || 0;
  const [formatted, unit = "Token"] = formatTokenAmount(amount).split(" ");
  const compacted = Math.abs(amount) >= 1_000;
  return <Tooltip title={`${formatNumber(amount)} Token`}><span className="token-usage"><span className="token-usage-main" aria-hidden="true"><span className="token-usage-value">{formatted}</span><small className="token-usage-unit">{unit}</small></span>{compacted ? <small className="token-usage-exact" aria-hidden="true">{formatNumber(amount)} Token</small> : null}<span className="token-usage-sr-only">{formatNumber(amount)} Token</span></span></Tooltip>;
}

function LegacyConfirmModal({ title, open, okText, danger, pending, children, onCancel, onConfirm }: { title: string; open: boolean; okText: string; danger?: boolean; pending?: boolean; children: ReactNode; onCancel: () => void; onConfirm: () => void }) {
  return <Modal className="legacy-confirm-modal" title={null} open={open} width={430} centered closable={false} transitionName="" maskTransitionName="" onCancel={onCancel} destroyOnHidden mask={{ closable: false }} footer={[<Button key="cancel" disabled={pending} onClick={onCancel}>取消</Button>, <Button key="confirm" danger={danger} type={danger ? "default" : "primary"} loading={pending} onClick={onConfirm}>{okText}</Button>]}><div className="legacy-confirm-body"><div className="legacy-confirm-icon" aria-hidden="true">!</div><h3>{title}</h3><div className="legacy-confirm-message">{children}</div></div></Modal>;
}

function LegacyDialogTitle({ title, kicker }: { title: string; kicker: string }) {
  return <div className="legacy-dialog-title"><strong>{title}</strong><span>{kicker}</span></div>;
}

function LegacyFormError({ error }: { error: unknown }) {
  return <p className="form-error" role="alert">{error ? errorMessage(error) : ""}</p>;
}

function errorMessage(error: unknown) {
  if (error instanceof ApiError || error instanceof Error) return error.message;
  return "请稍后重试";
}

function sameCriteria(left: MemberCriteria, right: MemberCriteria) {
  return left.query === right.query && left.scope === right.scope && left.usageState === right.usageState && left.window === right.window;
}

function formatNumber(value: number) {
  return new Intl.NumberFormat("en-US").format(Number(value) || 0);
}
