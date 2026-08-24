import { zodResolver } from "@hookform/resolvers/zod";
import { DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import {
  Alert,
  Button,
  Card,
  Empty,
  Form,
  Input,
  Modal,
  Result,
  Space,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import {
  createTeam,
  deleteTeam,
  listTeams,
  teamsQueryKey,
  updateTeam,
  type Team
} from "../api/teams";

const { Paragraph, Text } = Typography;
const { TextArea } = Input;

const teamSchema = z.object({
  name: z.string().trim().min(1, "请输入团队名称").max(64, "团队名称不能超过 64 个字符"),
  description: z.string().trim().max(200, "团队说明不能超过 200 个字符")
});

type TeamValues = z.infer<typeof teamSchema>;

export function TeamsPage({ csrfToken }: { csrfToken: string }) {
  const queryClient = useQueryClient();
  const [editor, setEditor] = useState<{ mode: "create" | "edit"; team?: Team } | null>(null);
  const [deleting, setDeleting] = useState<Team | null>(null);
  const [notice, setNotice] = useState("");
  const teams = useQuery({ queryKey: teamsQueryKey, queryFn: listTeams });
  const deleteMutation = useMutation({
    mutationFn: (team: Team) => deleteTeam(team.id, csrfToken),
    onSuccess: async (result) => {
      setDeleting(null);
      setNotice(result.message);
      await queryClient.invalidateQueries({ queryKey: teamsQueryKey, exact: true });
    }
  });

  if (teams.isPending) {
    return <TeamsPageSkeleton />;
  }
  if (teams.isError) {
    return (
      <section className="page-content">
        <Result
          status="warning"
          title="团队目录加载失败"
          subTitle={teams.error instanceof Error ? teams.error.message : "请稍后重试"}
          extra={<Button type="primary" onClick={() => void teams.refetch()}>重新加载</Button>}
        />
      </section>
    );
  }

  const columns: TableColumnsType<Team> = [
    {
      title: "团队",
      dataIndex: "name",
      align: "center",
      width: 230,
      render: (_, team) => (
        <Space orientation="vertical" size={1}>
          <Text strong>{team.name}</Text>
          <Text type="secondary" code>{team.id}</Text>
        </Space>
      )
    },
    {
      title: "说明",
      dataIndex: "description",
      render: (description: string) => description || <Text type="secondary">暂无说明</Text>
    },
    {
      title: "当前成员",
      dataIndex: "user_count",
      align: "center",
      width: 110,
      render: (count: number) => <Tag>{count}</Tag>
    },
    {
      title: "最近更新",
      dataIndex: "updated_at",
      align: "center",
      width: 170,
      render: formatTimestamp
    },
    {
      title: "操作",
      fixed: "right",
      align: "center",
      width: 170,
      render: (_, team) => (
        <Space>
          <Button
            type="link"
            icon={<EditOutlined aria-hidden="true" />}
            onClick={() => setEditor({ mode: "edit", team })}
          >
            编辑
          </Button>
          <Button
            type="link"
            danger
            icon={<DeleteOutlined aria-hidden="true" />}
            onClick={() => {
              deleteMutation.reset();
              setDeleting(team);
            }}
          >
            删除
          </Button>
        </Space>
      )
    }
  ];

  return (
    <section className="page-content team-page">
      <div className="page-intro account-page-intro">
        <Paragraph>
          团队用于组织用户并按当前成员汇总用量。标签数据仅保留兼容，新版 API 和界面不提供标签管理。
        </Paragraph>
        <Space wrap>
          <Button icon={<ReloadOutlined aria-hidden="true" />} loading={teams.isFetching} onClick={() => void teams.refetch()}>
            刷新当前页
          </Button>
          <Button type="primary" icon={<PlusOutlined aria-hidden="true" />} onClick={() => setEditor({ mode: "create" })}>
            新建团队
          </Button>
        </Space>
      </div>

      {notice ? <Alert className="page-alert" type="success" showIcon closable message={notice} onClose={() => setNotice("")} /> : null}

      <Card className="account-table-card" title="团队目录" extra={<Text type="secondary">仅请求团队目录</Text>}>
        {teams.data.teams.length === 0 ? (
          <Empty
            description="还没有团队"
            image={Empty.PRESENTED_IMAGE_SIMPLE}
          >
            <Button type="primary" onClick={() => setEditor({ mode: "create" })}>新建第一个团队</Button>
          </Empty>
        ) : (
          <Table<Team>
            rowKey="id"
            columns={columns}
            dataSource={teams.data.teams}
            pagination={false}
            scroll={{ x: 900 }}
          />
        )}
      </Card>

      {editor ? (
        <TeamEditor
          key={editor.team?.id ?? "new-team"}
          csrfToken={csrfToken}
          team={editor.team}
          onClose={() => setEditor(null)}
          onSaved={async (message) => {
            setEditor(null);
            setNotice(message);
            await queryClient.invalidateQueries({ queryKey: teamsQueryKey, exact: true });
          }}
        />
      ) : null}

      <Modal
        title={deleting ? `删除“${deleting.name}”？` : "删除团队"}
        open={deleting !== null}
        okText="确认删除"
        cancelText="取消"
        okButtonProps={{ danger: true }}
        confirmLoading={deleteMutation.isPending}
        onCancel={() => !deleteMutation.isPending && setDeleting(null)}
        onOk={() => deleting && deleteMutation.mutate(deleting)}
        destroyOnHidden
      >
        <Paragraph>仅空团队可以删除。这项操作不会删除用户、API Key 或用量数据。</Paragraph>
        {deleteMutation.isError ? (
          <Alert type="error" showIcon message="团队删除失败" description={deleteMutation.error.message} />
        ) : null}
      </Modal>
    </section>
  );
}

function TeamEditor({
  team,
  csrfToken,
  onClose,
  onSaved
}: {
  team?: Team;
  csrfToken: string;
  onClose: () => void;
  onSaved: (message: string) => Promise<void>;
}) {
  const form = useForm<TeamValues>({
    resolver: zodResolver(teamSchema),
    defaultValues: { name: team?.name ?? "", description: team?.description ?? "" }
  });
  const mutation = useMutation({
    mutationFn: (values: TeamValues) =>
      team
        ? updateTeam({ ...values, id: team.id }, csrfToken)
        : createTeam(values, csrfToken),
    onSuccess: (result) => onSaved(result.message)
  });

  return (
    <Modal
      title={team ? "编辑团队" : "新建团队"}
      open
      okText="保存团队"
      cancelText="取消"
      confirmLoading={mutation.isPending}
      onCancel={() => !mutation.isPending && onClose()}
      onOk={() => void form.handleSubmit((values) => mutation.mutate(values))()}
      afterOpenChange={(open) => open && form.setFocus("name")}
      destroyOnHidden
    >
      <Form layout="vertical" requiredMark={false}>
        <Form.Item
          label="团队名称"
          htmlFor="team-name"
          validateStatus={form.formState.errors.name ? "error" : undefined}
          help={form.formState.errors.name?.message}
        >
          <Controller
            control={form.control}
            name="name"
            render={({ field }) => <Input {...field} autoFocus id="team-name" maxLength={64} placeholder="例如：平台研发" />}
          />
        </Form.Item>
        <Form.Item
          label="团队说明（可选）"
          htmlFor="team-description"
          validateStatus={form.formState.errors.description ? "error" : undefined}
          help={form.formState.errors.description?.message}
        >
          <Controller
            control={form.control}
            name="description"
            render={({ field }) => (
              <TextArea
                {...field}
                id="team-description"
                rows={4}
                maxLength={200}
                showCount
                placeholder="说明团队职责或成员范围"
              />
            )}
          />
        </Form.Item>
        {mutation.isError ? (
          <Alert
            type="error"
            showIcon
            message="团队保存失败"
            description={mutation.error instanceof ApiError ? mutation.error.message : "请稍后重试"}
          />
        ) : null}
      </Form>
    </Modal>
  );
}

function TeamsPageSkeleton() {
  return (
    <section className="page-content" aria-label="正在加载团队目录">
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
