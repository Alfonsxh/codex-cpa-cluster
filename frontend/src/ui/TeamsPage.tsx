import { Button, Modal, Tooltip } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  useCallback,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode
} from "react";

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

type TeamStatus = "all" | "active" | "empty";
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

export function TeamsPage({ csrfToken }: { csrfToken: string }) {
  const queryClient = useQueryClient();
  const { setRefreshing, setRefreshAction, setRefreshLabel } = useAdminToolbar();
  const { toasts, showToast } = useLegacyToasts();
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState<TeamStatus>("all");
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
  const allUsageRange = useMemo(() => ({ window: "all" as const }), []);
  const allUsage = useQuery({
    queryKey: teamUsageQueryKey(allUsageRange),
    queryFn: ({ signal }) => readTeamUsage(allUsageRange, signal),
    enabled: teams.isSuccess,
    retry: false,
    refetchOnWindowFocus: false
  });

  const refreshWorkspace = useCallback(async (fresh = true) => {
    setRefreshing(true);
    try {
      const usageRange = { window: "all" as const, fresh };
      const teamResult = await queryClient.fetchQuery({
        queryKey: teamsQueryKey,
        queryFn: ({ signal }) => listTeams(signal),
        staleTime: 0
      });
      queryClient.setQueryData(teamsQueryKey, teamResult);
      const usageResult = await queryClient.fetchQuery({
        queryKey: teamUsageQueryKey(usageRange),
        queryFn: ({ signal }) => readTeamUsage(usageRange, signal),
        staleTime: 0
      });
      queryClient.setQueryData(teamUsageQueryKey(allUsageRange), usageResult);
      setRefreshLabel("团队数据已刷新");
      showToast("数据已刷新");
    } catch (error) {
      setRefreshLabel("刷新失败");
      throw error;
    } finally {
      setRefreshing(false);
    }
  }, [allUsageRange, queryClient, setRefreshLabel, setRefreshing, showToast]);

  useEffect(() => {
    setRefreshAction(() => refreshWorkspace(true));
    return () => setRefreshAction(null);
  }, [refreshWorkspace, setRefreshAction]);
  useEffect(() => {
    setRefreshing(teams.isFetching || allUsage.isFetching);
  }, [allUsage.isFetching, setRefreshing, teams.isFetching]);
  useEffect(() => {
    if (!teams.isSuccess || allUsage.isFetching) return;
    reportedCatalogError.current = null;
    setRefreshLabel("团队数据已刷新");
  }, [allUsage.isFetching, allUsage.status, setRefreshLabel, teams.isSuccess]);
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
    () => new Map((allUsage.data?.teams ?? []).map((team) => [team.id, team])),
    [allUsage.data?.teams]
  );
  const visibleTeams = useMemo(() => {
    const normalized = search.trim().toLowerCase();
    return (teams.data?.teams ?? []).filter((team) => {
      const matchesSearch = !normalized || `${team.name} ${team.description || ""}`.toLowerCase().includes(normalized);
      const matchesStatus = status === "all" || (status === "active" ? team.user_count > 0 : team.user_count === 0);
      return matchesSearch && matchesStatus;
    });
  }, [search, status, teams.data?.teams]);

  const refreshAfterCatalogMutation = async () => {
    const teamResult = await queryClient.fetchQuery({
      queryKey: teamsQueryKey,
      queryFn: ({ signal }) => listTeams(signal),
      staleTime: 0
    });
    queryClient.setQueryData(teamsQueryKey, teamResult);
    const usageResult = await queryClient.fetchQuery({
      queryKey: teamUsageQueryKey(allUsageRange),
      queryFn: ({ signal }) => readTeamUsage(allUsageRange, signal),
      staleTime: 0
    });
    queryClient.setQueryData(teamUsageQueryKey(allUsageRange), usageResult);
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
          <div className="organization-table-filters">
            <label className="search-field">
              <span aria-hidden="true">⌕</span>
              <input
                type="search"
                aria-label="搜索团队名称或说明"
                placeholder="搜索团队名称或说明"
                value={search}
                onChange={(event) => setSearch(event.target.value)}
              />
            </label>
            <label className="organization-status-filter">
              <span className="visually-hidden">团队状态</span>
              <select aria-label="团队状态" value={status} onChange={(event) => setStatus(event.target.value as TeamStatus)}>
                <option value="all">全部团队</option>
                <option value="active">有成员</option>
                <option value="empty">空团队</option>
              </select>
            </label>
          </div>
          <div>
            <span>{formatNumber(visibleTeams.length)} 个团队</span>
            <button className="button primary" type="button" onClick={() => setEditor({ mode: "create" })}>创建团队</button>
          </div>
        </div>
        <NativeTableViewport className="table-wrap organization-catalog-table-wrap" aria-label="团队目录表格">
          <table className="organization-catalog-table">
            <thead>
              <tr>
                <th className="table-index-column">序号</th>
                <th>团队</th>
                <th>当前成员</th>
                <th>活跃成员</th>
                <th>全部历史 Token</th>
                <th>更新时间</th>
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

function TeamRow({ index, team, usage, onMembers, onEdit, onDelete }: {
  index: number;
  team: Team;
  usage?: TeamUsageRow;
  onMembers: () => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  return (
    <tr>
      <td className="table-index-cell">{index}</td>
      <td><span className="organization-catalog-name"><strong title={team.name}>{team.name}</strong><small title={team.description || "无说明"}>{team.description || "无说明"}</small></span></td>
      <td className="number-cell">{formatNumber(team.user_count)}</td>
      <td className="number-cell">{formatNumber(usage?.usage.active_users ?? 0)}</td>
      <td className="number-cell token-total"><LegacyTokenValue value={usage?.usage.weighted_tokens ?? 0} /></td>
      <td>{formatTimestamp(team.updated_at)}</td>
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
  const [draftCriteria, setDraftCriteria] = useState<MemberCriteria>(defaultMemberCriteria);
  const [criteria, setCriteria] = useState<MemberCriteria>(defaultMemberCriteria);
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
      onToast(`已更新 ${count} 位用户的团队归属`);
      await onCatalogRefresh();
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
            <label className="search-field"><span aria-hidden="true">⌕</span><input type="search" aria-label="搜索用户邮箱" placeholder="搜索用户邮箱" value={draftCriteria.query} onChange={(event) => setDraftCriteria((current) => ({ ...current, query: event.target.value }))} /></label>
            <label className="window-field filter-field"><span>成员范围</span><LegacyEnhancedSelect id="organization-user-scope-react" label="成员范围" value={draftCriteria.scope} options={[{ value: "current", label: "当前团队成员" }, { value: "unassigned", label: "未分组用户" }, { value: "all", label: "全部用户" }]} onChange={(scope) => setDraftCriteria((current) => ({ ...current, scope }))} /></label>
            <label className="window-field filter-field"><span>Token 状态</span><LegacyEnhancedSelect id="organization-usage-state-react" label="Token 状态" value={draftCriteria.usageState} options={[{ value: "all", label: "不限用量" }, { value: "used", label: "已产生 Token" }, { value: "unused", label: "未产生 Token" }]} onChange={(usageState) => setDraftCriteria((current) => ({ ...current, usageState }))} /></label>
            <label className="window-field filter-field"><span>统计范围</span><LegacyEnhancedSelect id="organization-usage-window-react" label="统计范围" value={draftCriteria.window} options={[{ value: "today", label: "今日" }, { value: "604800", label: "近 7 天" }, { value: "2592000", label: "近 30 天" }, { value: "all", label: "全部历史" }]} onChange={(window) => setDraftCriteria((current) => ({ ...current, window }))} /></label>
          </div>
          <NativeTableViewport className="organization-member-table-wrap" aria-label="团队成员表格">
            <table className="organization-member-table">
              <thead><tr><th className="table-index-column">序号</th><th className="user-select-column"><IndeterminateCheckbox ariaLabel="选择本页用户" checked={everyVisible} indeterminate={!everyVisible && anyVisible} onChange={toggleVisible} /></th><th>用户</th><th>团队归属</th><th>Token 用量</th><th>{team ? `与“${team.name}”的关系` : "与当前团队的关系"}</th></tr></thead>
              <tbody>
                {showInitialMemberLoading ? <tr><td colSpan={6} className="team-usage-state">正在加载成员…</td></tr> : null}
                {!showInitialMemberLoading && visibleUsers.length === 0 ? <tr><td colSpan={6} className="team-usage-state">当前条件没有匹配用户</td></tr> : null}
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
  return <tr><td className="table-index-cell">{index}</td><td><input type="checkbox" aria-label={`选择 ${user.email}`} checked={checked} onChange={(event) => onChange(event.target.checked)} /></td><td><span className="table-primary">{user.email}</span></td><td>{user.team ? <span className="team-chip">{user.team.name}</span> : <span className="team-chip unassigned">未分组</span>}</td><td className="number-cell token-total"><LegacyTokenValue value={user.usage?.weighted_tokens ?? 0} /></td><td><span className={`status-chip ${conflict ? "warning" : current ? "success" : "neutral"}`}>{conflict ? "属于其他团队" : current ? "本团队成员" : "尚未加入"}</span></td></tr>;
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

function formatTimestamp(timestamp: number) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", { month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(timestamp * 1000));
}

function formatNumber(value: number) {
  return new Intl.NumberFormat("en-US").format(Number(value) || 0);
}
