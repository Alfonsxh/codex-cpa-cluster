import { zodResolver } from "@hookform/resolvers/zod";
import {
  BellOutlined,
  DeleteOutlined,
  ReloadOutlined,
  SaveOutlined,
  SendOutlined
} from "@ant-design/icons";
import {
  Alert,
  Button,
  Card,
  Col,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Modal,
  Row,
  Space,
  Switch,
  Tag,
  Typography
} from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import {
  clearNotificationWebhook,
  notificationSettingsQueryKey,
  readNotificationSettings,
  saveNotificationSettings,
  saveNotificationWebhook,
  sendNotification,
  testNotification,
  type NotificationValues
} from "../api/notifications";
import { PageState } from "./components/PageState";
import { PageToolbar } from "./components/PageToolbar";
import { ConfigurationSectionNav } from "./ConfigurationSectionNav";

const { Paragraph, Text } = Typography;

const notificationSchema = z.object({
  enabled: z.boolean(),
  timezone: z.string().trim().min(1, "请输入 IANA 时区"),
  daily_times: z.string().trim().regex(
    /^([01]\d|2[0-3]):[0-5]\d(?:,\s*([01]\d|2[0-3]):[0-5]\d)*$/,
    "请输入 HH:MM，多个时间使用逗号分隔"
  ),
  schedule_grace_minutes: z.number().int().min(0).max(120),
  quota_alert_enabled: z.boolean(),
  weekly_threshold_percent: z.number().min(1).max(100)
});

const webhookSchema = z.object({
  webhook_url: z.string().trim().url("请输入完整的 Webhook 地址").startsWith(
    "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=",
    "仅支持企业微信官方消息推送地址"
  )
});

type WebhookValues = z.infer<typeof webhookSchema>;

export function NotificationSettingsPage({ csrfToken }: { csrfToken: string }) {
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState("");
  const [clearOpen, setClearOpen] = useState(false);
  const settings = useQuery({
    queryKey: notificationSettingsQueryKey,
    queryFn: ({ signal }) => readNotificationSettings(signal),
    staleTime: 0,
    gcTime: 0,
    refetchInterval: 10_000,
    refetchOnWindowFocus: true
  });
  const form = useForm<NotificationValues>({
    resolver: zodResolver(notificationSchema),
    values: settings.data?.values,
    resetOptions: { keepDirtyValues: true },
    defaultValues: {
      enabled: false,
      timezone: "UTC",
      daily_times: "09:00,14:00,18:00",
      schedule_grace_minutes: 15,
      quota_alert_enabled: true,
      weekly_threshold_percent: 90
    }
  });
  const webhookForm = useForm<WebhookValues>({
    resolver: zodResolver(webhookSchema),
    defaultValues: { webhook_url: "" }
  });

  const refresh = async (message: string) => {
    setNotice(message);
    await queryClient.invalidateQueries({ queryKey: notificationSettingsQueryKey, exact: true });
  };
  const settingsMutation = useMutation({
    mutationFn: (values: NotificationValues) => saveNotificationSettings(values, csrfToken),
    onSuccess: async (result) => {
      form.reset(result.values);
      await refresh(result.message);
    }
  });
  const webhookMutation = useMutation({
    gcTime: 0,
    mutationFn: () => saveNotificationWebhook(webhookForm.getValues("webhook_url"), csrfToken),
    onSuccess: async (result) => {
      webhookForm.reset({ webhook_url: "" });
      await refresh(result.message);
    }
  });
  const clearMutation = useMutation({
    mutationFn: () => clearNotificationWebhook(csrfToken),
    onSuccess: async (result) => {
      setClearOpen(false);
      webhookForm.reset({ webhook_url: "" });
      form.setValue("enabled", false);
      await refresh(result.message);
    }
  });
  const sendMutation = useMutation({
    mutationFn: () => sendNotification(csrfToken),
    onSuccess: async (result) => refresh(result.message)
  });
  const testMutation = useMutation({
    mutationFn: () => testNotification(csrfToken),
    onSuccess: async (result) => refresh(result.message)
  });

  if (settings.isPending) {
    return <NotificationPageSkeleton />;
  }
  if (settings.isError) {
    return (
      <section className="page-content">
        <PageState
          kind="error"
          title="通知配置加载失败"
          detail={settings.error instanceof Error ? settings.error.message : "请稍后重试"}
          onAction={() => void settings.refetch()}
        />
      </section>
    );
  }

  const status = settings.data.notifications;
  const mutationError = settingsMutation.error ?? webhookMutation.error ?? clearMutation.error ?? sendMutation.error ?? testMutation.error;

  return (
    <section className="page-content notification-page">
      <ConfigurationSectionNav />
      <PageToolbar
        className="account-page-intro"
        description="本页只请求通知配置和运行状态；打开期间每 10 秒刷新一次。额度明细仅在手动发送或后台任务到期时读取。"
        actions={(
          <Button icon={<ReloadOutlined aria-hidden="true" />} loading={settings.isFetching} onClick={() => void settings.refetch()}>
            刷新当前页
          </Button>
        )}
      />

      {notice ? <Alert className="page-alert" type="success" showIcon closable message={notice} onClose={() => setNotice("")} /> : null}
      {mutationError ? (
        <Alert
          className="page-alert"
          type="error"
          showIcon
          message="通知操作失败"
          description={mutationError instanceof ApiError ? mutationError.message : "请稍后重试"}
        />
      ) : null}

      <Row gutter={[16, 16]} className="notification-grid">
        <Col xs={24} xl={10}>
          <Card title="企业微信 Webhook" extra={<WebhookTag configured={status.webhook_configured} />}>
            <Form layout="vertical" requiredMark={false} onFinish={() => webhookForm.handleSubmit(() => webhookMutation.mutate())()}>
              <Form.Item
                label="Webhook 地址"
                htmlFor="notification-webhook"
                validateStatus={webhookForm.formState.errors.webhook_url ? "error" : undefined}
                help={webhookForm.formState.errors.webhook_url?.message ?? "密钥加密存放在控制面 SQLite，不写入浏览器存储。"}
              >
                <Controller
                  control={webhookForm.control}
                  name="webhook_url"
                  render={({ field }) => (
                    <Input.Password
                      {...field}
                      id="notification-webhook"
                      autoComplete="off"
                      visibilityToggle={{ tabIndex: -1 }}
                      placeholder="https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."
                    />
                  )}
                />
              </Form.Item>
              <Space wrap>
                <Button
                  type="primary"
                  htmlType="submit"
                  icon={<SaveOutlined aria-hidden="true" />}
                  loading={webhookMutation.isPending}
                >
                  保存 Webhook
                </Button>
                <Button
                  danger
                  icon={<DeleteOutlined aria-hidden="true" />}
                  disabled={!status.webhook_configured}
                  onClick={() => setClearOpen(true)}
                >
                  清除
                </Button>
                <Button
                  icon={<SendOutlined aria-hidden="true" />}
                  loading={sendMutation.isPending}
                  disabled={!status.webhook_configured}
                  onClick={() => sendMutation.mutate()}
                >
                  发送账号信息
                </Button>
                <Button
                  icon={<BellOutlined aria-hidden="true" />}
                  loading={testMutation.isPending}
                  disabled={!status.webhook_configured}
                  onClick={() => testMutation.mutate()}
                >
                  发送测试消息
                </Button>
              </Space>
            </Form>
          </Card>

          <Card className="notification-status-card" title="运行状态">
            <Descriptions column={1} size="small">
              <Descriptions.Item label="Worker 心跳">{formatTimestamp(status.heartbeat_at)}</Descriptions.Item>
              <Descriptions.Item label="最近成功">{formatTimestamp(status.last_success_at)}</Descriptions.Item>
              <Descriptions.Item label="下次发送">{formatTimestamp(status.next_schedule_at)}</Descriptions.Item>
              <Descriptions.Item label="最近错误">
                {status.last_error ? <Text type="danger">{status.last_error}</Text> : "—"}
              </Descriptions.Item>
            </Descriptions>
          </Card>
        </Col>

        <Col xs={24} xl={14}>
          <Card title="发送与预警规则" extra={<BellOutlined aria-hidden="true" />}>
            <Form layout="vertical" requiredMark={false}>
              <Controller
                control={form.control}
                name="enabled"
                render={({ field }) => (
                  <Form.Item label="定时通知" extra="启用前必须先保存有效 Webhook。">
                    <Switch
                      aria-label="启用通知调度"
                      checked={field.value}
                      onChange={field.onChange}
                      checkedChildren="启用"
                      unCheckedChildren="关闭"
                    />
                  </Form.Item>
                )}
              />
              <Row gutter={16}>
                <Col xs={24} md={12}>
                  <Form.Item
                    label="IANA 时区"
                    htmlFor="notification-timezone"
                    validateStatus={form.formState.errors.timezone ? "error" : undefined}
                    help={form.formState.errors.timezone?.message}
                  >
                    <Controller
                      control={form.control}
                      name="timezone"
                      render={({ field }) => <Input {...field} id="notification-timezone" placeholder="Asia/Shanghai" />}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} md={12}>
                  <Form.Item
                    label="每日发送时间"
                    htmlFor="notification-times"
                    validateStatus={form.formState.errors.daily_times ? "error" : undefined}
                    help={form.formState.errors.daily_times?.message}
                  >
                    <Controller
                      control={form.control}
                      name="daily_times"
                      render={({ field }) => <Input {...field} id="notification-times" placeholder="09:00,14:00,18:00" />}
                    />
                  </Form.Item>
                </Col>
                <Col xs={24} md={12}>
                  <Controller
                    control={form.control}
                    name="schedule_grace_minutes"
                    render={({ field }) => (
                      <Form.Item label="定时补发窗口" validateStatus={form.formState.errors.schedule_grace_minutes ? "error" : undefined}>
                        <InputNumber {...field} min={0} max={120} precision={0} suffix="分钟" onChange={(value) => field.onChange(value ?? 0)} />
                      </Form.Item>
                    )}
                  />
                </Col>
                <Col xs={24} md={12}>
                  <Controller
                    control={form.control}
                    name="weekly_threshold_percent"
                    render={({ field }) => (
                      <Form.Item label="周额度预警阈值" validateStatus={form.formState.errors.weekly_threshold_percent ? "error" : undefined}>
                        <InputNumber {...field} min={1} max={100} step={0.5} suffix="%" onChange={(value) => field.onChange(value ?? 90)} />
                      </Form.Item>
                    )}
                  />
                </Col>
              </Row>
              <Controller
                control={form.control}
                name="quota_alert_enabled"
                render={({ field }) => (
                  <Form.Item label="额度状态提醒" extra="预警、耗尽、恢复和额度刷新按窗口去重发送。">
                    <Switch
                      aria-label="启用额度状态提醒"
                      checked={field.value}
                      onChange={field.onChange}
                      checkedChildren="启用"
                      unCheckedChildren="关闭"
                    />
                  </Form.Item>
                )}
              />
              <Button
                type="primary"
                icon={<SaveOutlined aria-hidden="true" />}
                loading={settingsMutation.isPending}
                onClick={() => void form.handleSubmit((values) => settingsMutation.mutate(values))()}
              >
                保存通知规则
              </Button>
            </Form>
          </Card>
        </Col>
      </Row>

      <Modal
        title="清除企业微信 Webhook？"
        open={clearOpen}
        okText="确认清除"
        cancelText="取消"
        okButtonProps={{ danger: true }}
        confirmLoading={clearMutation.isPending}
        onCancel={() => !clearMutation.isPending && setClearOpen(false)}
        onOk={() => clearMutation.mutate()}
      >
        <Paragraph>清除后会同时关闭通知调度。历史发送状态保留，Webhook 密钥不可恢复。</Paragraph>
      </Modal>
    </section>
  );
}

function WebhookTag({ configured }: { configured: boolean }) {
  return <Tag color={configured ? "success" : "default"}>{configured ? "已配置" : "未配置"}</Tag>;
}

function formatTimestamp(timestamp: number | null) {
  if (!timestamp) return "—";
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit"
  }).format(new Date(timestamp * 1000));
}

function NotificationPageSkeleton() {
  return (
    <section className="page-content" aria-label="正在加载通知配置">
      <div className="skeleton skeleton-title" />
      <div className="skeleton skeleton-line" />
      <div className="skeleton skeleton-table" />
    </section>
  );
}
