import { zodResolver } from "@hookform/resolvers/zod";
import { KeyOutlined, ReloadOutlined, SaveOutlined, UndoOutlined, UploadOutlined } from "@ant-design/icons";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Avatar, Button, Card, Col, Form, Input, Modal, Popconfirm, Row, Skeleton, Space, Tag, Typography } from "antd";
import { Controller, useForm } from "react-hook-form";
import { useEffect, useState } from "react";
import { z } from "zod";

import {
  generalSettingsQueryKey,
  readGeneralSettings,
  resetBrandingLogo,
  rotateManagementKey,
  saveBrandingLogo,
  saveGeneralSettings,
  type GeneralSettings,
  type GeneralSettingsValues
} from "../api/general-settings";
import { PageState } from "./components/PageState";
import { PageToolbar } from "./components/PageToolbar";
import { ConfigurationSectionNav } from "./ConfigurationSectionNav";
import { InitialPasswordModal } from "./InitialPasswordModal";

const { Paragraph, Text } = Typography;

const settingsSchema = z.object({
  product_name: z.string().trim().min(2, "至少输入 2 个字符").max(64, "最多输入 64 个字符"),
  short_name: z.string().trim().min(2, "至少输入 2 个字符").max(32, "最多输入 32 个字符"),
  environment_label: z.string().trim().max(64, "最多输入 64 个字符"),
  public_base_url: z.string().trim().refine(validPublicBaseURL, "请输入不带路径、账号、查询参数或片段的 HTTP(S) 根地址"),
  allowed_email_domains: z.string(),
  key_prefix: z.string().regex(/^[a-z][a-z0-9_]{1,30}_$/, "请输入 3-32 位小写前缀，并以下划线结尾"),
  provider_name: z.string().trim().min(2, "至少输入 2 个字符").max(48, "最多输入 48 个字符"),
  api_key_env: z.string().regex(/^[A-Z][A-Z0-9_]{1,63}$/, "请输入有效的大写环境变量名"),
  default_model: z.string().trim().min(1, "请输入默认模型").max(128, "最多输入 128 个字符")
});

type SettingsFormValues = z.infer<typeof settingsSchema>;

const managementKeySchema = z.object({
  new_key: z.string().min(12, "至少输入 12 个字符").max(128, "最多输入 128 个字符").regex(/^\S+$/, "不能包含空白字符"),
  confirmation: z.string().min(1, "请再次输入新管理密钥")
}).refine((values) => values.new_key === values.confirmation, {
  path: ["confirmation"],
  message: "两次输入的管理密钥不一致"
});

type ManagementKeyFormValues = z.infer<typeof managementKeySchema>;
const maxLogoBytes = 2 * 1024 * 1024;
const supportedLogoTypes = new Set(["image/png", "image/jpeg", "image/gif", "image/webp", "image/svg+xml"]);

export function GeneralSettingsPage({
  csrfToken,
  onManagementKeyRotated = () => undefined
}: {
  csrfToken: string;
  onManagementKeyRotated?: (message: string) => void;
}) {
  const queryClient = useQueryClient();
  const [notice, setNotice] = useState("");
  const [initialPasswordOpen, setInitialPasswordOpen] = useState(false);
  const [logoOpen, setLogoOpen] = useState(false);
  const [logoFile, setLogoFile] = useState<File | null>(null);
  const [logoValidationError, setLogoValidationError] = useState("");
  const [managementKeyOpen, setManagementKeyOpen] = useState(false);
  const settings = useQuery({
    queryKey: generalSettingsQueryKey,
    queryFn: ({ signal }) => readGeneralSettings(signal)
  });
  const form = useForm<SettingsFormValues>({
    resolver: zodResolver(settingsSchema),
    defaultValues: emptyFormValues()
  });
  const managementKeyForm = useForm<ManagementKeyFormValues>({
    resolver: zodResolver(managementKeySchema),
    defaultValues: { new_key: "", confirmation: "" }
  });
  useEffect(() => {
    if (settings.data) form.reset(toFormValues(settings.data.values));
  }, [form, settings.data]);
  const mutation = useMutation({
    mutationFn: (values: SettingsFormValues) => saveGeneralSettings(toAPIValues(values), csrfToken),
    onSuccess: (result) => {
      queryClient.setQueryData(generalSettingsQueryKey, result.settings);
      form.reset(toFormValues(result.settings.values));
      setNotice(result.message);
    }
  });
  const logoMutation = useMutation({
    mutationFn: (file: File) => saveBrandingLogo(file, csrfToken),
    onSuccess: (result) => {
      queryClient.setQueryData<GeneralSettings>(generalSettingsQueryKey, (current) => current ? ({
        ...current,
        branding: {
          custom_logo: result.logo.custom,
          logo_sha256: result.logo.sha256
        }
      }) : current);
      setLogoFile(null);
      setLogoValidationError("");
      setLogoOpen(false);
      setNotice(result.message);
    }
  });
  const logoResetMutation = useMutation({
    mutationFn: () => resetBrandingLogo(csrfToken),
    onSuccess: (result) => {
      queryClient.setQueryData<GeneralSettings>(generalSettingsQueryKey, (current) => current ? ({
        ...current,
        branding: { custom_logo: false }
      }) : current);
      setNotice(result.message);
    }
  });
  const managementKeyMutation = useMutation({
    gcTime: 0,
    mutationFn: () => rotateManagementKey(
      managementKeyForm.getValues("new_key"),
      managementKeyForm.getValues("confirmation"),
      csrfToken
    ),
    onSuccess: (result) => {
      managementKeyForm.reset();
      managementKeyMutation.reset();
      setManagementKeyOpen(false);
      onManagementKeyRotated(result.message);
    }
  });

  if (settings.isPending) {
    return <section className="page-content"><Skeleton active paragraph={{ rows: 10 }} /></section>;
  }
  if (settings.isError) {
    return (
      <section className="page-content">
        <PageState
          kind="error"
          title="通用设置加载失败"
          detail={settings.error instanceof Error ? settings.error.message : "请稍后重试"}
          onAction={() => void settings.refetch()}
        />
      </section>
    );
  }

  return (
    <section className="page-content settings-page">
      <ConfigurationSectionNav />
      <PageToolbar
        description="这里只管理可实时生效的品牌、登录域名和客户端导出字段。代理、配额、部署镜像等需要专用事务或重建的配置不会混入本接口。"
        actions={(
          <Button icon={<ReloadOutlined aria-hidden="true" />} loading={settings.isFetching} onClick={() => void settings.refetch()}>
            刷新当前页
          </Button>
        )}
      />
      {notice ? <Alert className="page-alert" type="success" showIcon closable title={notice} onClose={() => setNotice("")} /> : null}
      {mutation.isError ? (
        <Alert className="page-alert" type="error" showIcon title="设置未保存" description={mutation.error instanceof Error ? mutation.error.message : "请求失败"} />
      ) : null}

      <Row gutter={[16, 16]} className="settings-status-grid">
        <Col xs={24} md={8}>
          <Card title="管理密钥">
            <Space orientation="vertical" size={12}>
              <Tag color={settings.data.security.management_key_configured ? "success" : "error"}>{settings.data.security.management_key_configured ? "已配置" : "未配置"}</Tag>
              <Button
                size="small"
                icon={<KeyOutlined aria-hidden="true" />}
                disabled={!settings.data.security.management_key_configured}
                onClick={() => setManagementKeyOpen(true)}
              >
                轮换密钥
              </Button>
            </Space>
          </Card>
        </Col>
        <Col xs={24} md={8}>
          <Card title="用户初始密码">
            <Space orientation="vertical" size={12}>
              <Tag color={settings.data.security.initial_password_configured ? "success" : "warning"}>{settings.data.security.initial_password_configured ? "已配置" : "未配置"}</Tag>
              <Button size="small" onClick={() => setInitialPasswordOpen(true)}>{settings.data.security.initial_password_configured ? "更新密码" : "立即设置"}</Button>
            </Space>
          </Card>
        </Col>
        <Col xs={24} md={8}>
          <Card title="品牌 Logo">
            <Space size={12} align="center">
              <Avatar
                className="settings-logo-preview"
                shape="square"
                size={44}
                src={logoPreviewURL(settings.data)}
              >
                C
              </Avatar>
              <Space orientation="vertical" size={8}>
                <Tag color={settings.data.branding.custom_logo ? "blue" : "default"}>{settings.data.branding.custom_logo ? "自定义" : "默认"}</Tag>
                <Space size={6} wrap>
                  <Button size="small" icon={<UploadOutlined aria-hidden="true" />} onClick={() => setLogoOpen(true)}>
                    {settings.data.branding.custom_logo ? "替换" : "上传"}
                  </Button>
                  {settings.data.branding.custom_logo ? (
                    <Popconfirm
                      title="恢复默认 Logo？"
                      description="自定义 Logo 会从控制面中删除。"
                      okText="确认恢复"
                      cancelText="取消"
                      onConfirm={() => logoResetMutation.mutate()}
                    >
                      <Button size="small" icon={<UndoOutlined aria-hidden="true" />} loading={logoResetMutation.isPending}>恢复默认</Button>
                    </Popconfirm>
                  ) : null}
                </Space>
              </Space>
            </Space>
          </Card>
        </Col>
      </Row>

      <form onSubmit={form.handleSubmit((values) => mutation.mutate(values))}>
        <Card
          title="品牌与身份"
          className="settings-form-card"
          extra={<Text type="secondary">保存方式：细粒度 SQLite 更新 · 实时生效</Text>}
        >
          <Row gutter={[18, 0]}>
            <Col xs={24} lg={12}><FormField control={form.control} name="product_name" label="产品名称" /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="short_name" label="产品简称" /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="environment_label" label="环境说明" /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="public_base_url" label="公开访问地址" placeholder="https://cpa.example.com" /></Col>
            <Col xs={24}>
              <Controller
                control={form.control}
                name="allowed_email_domains"
                render={({ field, fieldState }) => (
                  <Form.Item label="允许的邮箱域名" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message ?? "使用逗号、空格或换行分隔；留空会禁止新的用户登录。"}>
                    <Input.TextArea {...field} aria-label="允许的邮箱域名" autoSize={{ minRows: 2, maxRows: 4 }} placeholder="example.com, example.org" />
                  </Form.Item>
                )}
              />
            </Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="key_prefix" label="新 Key 前缀" /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="provider_name" label="客户端 Provider 名称" /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="api_key_env" label="客户端 Key 环境变量" /></Col>
            <Col xs={24} lg={12}><FormField control={form.control} name="default_model" label="客户端默认模型" /></Col>
          </Row>
          <Space>
            <Button type="primary" htmlType="submit" icon={<SaveOutlined aria-hidden="true" />} loading={mutation.isPending}>保存通用设置</Button>
            <Button type="default" icon={<UndoOutlined aria-hidden="true" />} disabled={mutation.isPending || !form.formState.isDirty} onClick={() => form.reset(toFormValues(settings.data.values))}>撤销修改</Button>
          </Space>
        </Card>
      </form>
      <InitialPasswordModal
        open={initialPasswordOpen}
        csrfToken={csrfToken}
        onClose={() => setInitialPasswordOpen(false)}
        onSuccess={(message) => {
          queryClient.setQueryData(generalSettingsQueryKey, {
            ...settings.data,
            security: { ...settings.data.security, initial_password_configured: true }
          });
          setInitialPasswordOpen(false);
          setNotice(message);
        }}
      />
      <Modal
        title={settings.data.branding.custom_logo ? "替换品牌 Logo" : "上传品牌 Logo"}
        open={logoOpen}
        okText="保存 Logo"
        cancelText="取消"
        confirmLoading={logoMutation.isPending}
        okButtonProps={{ disabled: !logoFile || Boolean(logoValidationError) }}
        onCancel={() => {
          if (logoMutation.isPending) return;
          setLogoOpen(false);
          setLogoFile(null);
          setLogoValidationError("");
          logoMutation.reset();
        }}
        onOk={() => logoFile && logoMutation.mutate(logoFile)}
        destroyOnHidden
      >
        <Paragraph type="secondary">支持 PNG、JPEG、GIF、WebP 或安全 SVG，文件不超过 2 MiB。保存后入口页会立即使用新版本。</Paragraph>
        <label className="logo-file-picker">
          <UploadOutlined aria-hidden="true" />
          <span>{logoFile ? logoFile.name : "选择 Logo 文件"}</span>
          <input
            aria-label="Logo 文件"
            type="file"
            accept="image/png,image/jpeg,image/gif,image/webp,image/svg+xml"
            onChange={(event) => {
              const selected = event.target.files?.[0] ?? null;
              const validationError = selected ? validateLogoFile(selected) : "请选择 Logo 文件";
              setLogoFile(validationError ? null : selected);
              setLogoValidationError(validationError);
              logoMutation.reset();
            }}
          />
        </label>
        {logoValidationError ? <Alert className="page-alert" type="error" showIcon message={logoValidationError} /> : null}
        {logoMutation.isError ? <Alert className="page-alert" type="error" showIcon message="Logo 未保存" description={logoMutation.error instanceof Error ? logoMutation.error.message : "请稍后重试"} /> : null}
      </Modal>
      <Modal
        title="轮换管理密钥"
        open={managementKeyOpen}
        okText="确认轮换并重新登录"
        cancelText="取消"
        confirmLoading={managementKeyMutation.isPending}
        onCancel={() => {
          if (managementKeyMutation.isPending) return;
          setManagementKeyOpen(false);
          managementKeyForm.reset();
          managementKeyMutation.reset();
        }}
        onOk={() => void managementKeyForm.handleSubmit(() => managementKeyMutation.mutate())()}
        destroyOnHidden
      >
        <Alert
          className="page-alert"
          type="warning"
          showIcon
          message="提交后所有管理会话立即失效"
          description="API Key、用户会话和数据面流量不会改变；你需要使用新管理密钥重新进入。"
        />
        {managementKeyMutation.isError ? <Alert className="page-alert" type="error" showIcon message="管理密钥未更新" description={managementKeyMutation.error instanceof Error ? managementKeyMutation.error.message : "请稍后重试"} /> : null}
        <Form layout="vertical" requiredMark={false}>
          <Controller
            control={managementKeyForm.control}
            name="new_key"
            render={({ field, fieldState }) => (
              <Form.Item label="新管理密钥" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
                <Input.Password {...field} aria-label="新管理密钥" autoComplete="new-password" />
              </Form.Item>
            )}
          />
          <Controller
            control={managementKeyForm.control}
            name="confirmation"
            render={({ field, fieldState }) => (
              <Form.Item label="确认新管理密钥" validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
                <Input.Password {...field} aria-label="确认新管理密钥" autoComplete="new-password" />
              </Form.Item>
            )}
          />
        </Form>
      </Modal>
    </section>
  );
}

function FormField({ control, name, label, placeholder }: {
  control: ReturnType<typeof useForm<SettingsFormValues>>["control"];
  name: Exclude<keyof SettingsFormValues, "allowed_email_domains">;
  label: string;
  placeholder?: string;
}) {
  return (
    <Controller
      control={control}
      name={name}
      render={({ field, fieldState }) => (
        <Form.Item label={label} validateStatus={fieldState.error ? "error" : undefined} help={fieldState.error?.message}>
          <Input {...field} aria-label={label} placeholder={placeholder} />
        </Form.Item>
      )}
    />
  );
}

function emptyFormValues(): SettingsFormValues {
  return {
    product_name: "", short_name: "", environment_label: "", public_base_url: "",
    allowed_email_domains: "", key_prefix: "cpa_", provider_name: "", api_key_env: "CPA_API_KEY", default_model: ""
  };
}

function toFormValues(values: GeneralSettingsValues): SettingsFormValues {
  return { ...values, allowed_email_domains: values.allowed_email_domains.join(", ") };
}

function toAPIValues(values: SettingsFormValues): GeneralSettingsValues {
  return {
    ...values,
    allowed_email_domains: values.allowed_email_domains
      .split(/[,，\s]+/)
      .map((domain) => domain.trim())
      .filter(Boolean)
  };
}

function validPublicBaseURL(value: string) {
  if (!value) return true;
  try {
    const parsed = new URL(value);
    return (parsed.protocol === "http:" || parsed.protocol === "https:") &&
      !parsed.username && !parsed.password &&
      (parsed.pathname === "" || parsed.pathname === "/") &&
      !parsed.search && !parsed.hash;
  } catch {
    return false;
  }
}

function validateLogoFile(file: File) {
  if (!supportedLogoTypes.has(file.type)) return "仅支持 PNG、JPEG、GIF、WebP 或 SVG 文件";
  if (file.size < 1) return "Logo 文件不能为空";
  if (file.size > maxLogoBytes) return "Logo 文件不能超过 2 MiB";
  if (Array.from(file.name).length > 128) return "Logo 文件名不能超过 128 个字符";
  return "";
}

function logoPreviewURL(settings: GeneralSettings) {
  if (!settings.branding.custom_logo) return "/portal/assets/codex-cpa-cluster-mark.svg";
  const digest = settings.branding.logo_sha256 ?? "";
  return digest ? `/branding/logo?v=${encodeURIComponent(digest.slice(0, 16))}` : "/branding/logo";
}
