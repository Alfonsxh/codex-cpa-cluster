import {
  ReloadOutlined,
  SaveOutlined,
  SearchOutlined,
  UndoOutlined,
  WarningOutlined
} from "@ant-design/icons";
import { useMutation, useQuery } from "@tanstack/react-query";
import {
  Alert,
  Button,
  Card,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Result,
  Select,
  Skeleton,
  Space,
  Switch,
  Tag,
  Typography
} from "antd";
import { useEffect, useMemo, useState } from "react";

import { ApiError } from "../api/client";
import {
  configurationQueryKey,
  readConfiguration,
  saveConfiguration,
  type ConfigurationCatalog,
  type ConfigurationField,
  type ConfigurationValue
} from "../api/configuration";
import { ConfigurationSectionNav } from "./ConfigurationSectionNav";

const { Paragraph, Text, Title } = Typography;

type DraftValue = string | number | boolean | null;
type Draft = Record<string, DraftValue>;

export function ConfigurationPage({ csrfToken }: { csrfToken: string }) {
  const [selectedGroup, setSelectedGroup] = useState("");
  const [search, setSearch] = useState("");
  const [draft, setDraft] = useState<Draft>({});
  const [notice, setNotice] = useState("");
  const [confirmOpen, setConfirmOpen] = useState(false);
  const catalog = useQuery({
    queryKey: configurationQueryKey,
    queryFn: ({ signal }) => readConfiguration(signal),
    staleTime: 0,
    gcTime: 0,
    refetchOnWindowFocus: false
  });

  useEffect(() => {
    if (!catalog.data) return;
    setDraft(configurationDraft(catalog.data));
    setSelectedGroup((current) => catalog.data.groups.some((group) => group.name === current)
      ? current
      : (catalog.data.groups[0]?.name ?? ""));
  }, [catalog.data]);

  const fields = useMemo(() => flattenConfiguration(catalog.data), [catalog.data]);
  const dirtyFields = useMemo(
    () => fields.filter((field) => !sameConfigurationValue(normalizeDraftValue(field, draft[field.key]), field.value)),
    [draft, fields]
  );
  const errors = useMemo(() => Object.fromEntries(
    fields.map((field) => [field.key, validateDraftValue(field, draft[field.key])]).filter(([, error]) => Boolean(error))
  ) as Record<string, string>, [draft, fields]);
  const selected = catalog.data?.groups.find((group) => group.name === selectedGroup) ?? catalog.data?.groups[0];
  const normalizedSearch = search.trim().toLocaleLowerCase("zh-CN");
  const visibleFields = normalizedSearch
    ? fields.filter((field) => [field.group, field.label, field.description, field.key]
        .join(" ").toLocaleLowerCase("zh-CN").includes(normalizedSearch))
    : (selected?.fields.map((field) => ({ ...field, group: selected.name })) ?? []);
  const dirtyModes = [...new Set(dirtyFields.map((field) => field.apply_mode))];
  const riskyEffects = dirtyModes.filter((mode) => mode !== "live" && mode !== "future" && mode !== "quota");

  const mutation = useMutation({
    mutationFn: () => saveConfiguration(
      Object.fromEntries(dirtyFields.map((field) => [field.key, normalizeDraftValue(field, draft[field.key])])),
      csrfToken
    ),
    onSuccess: async (result) => {
      setConfirmOpen(false);
      setNotice(result.message);
      await catalog.refetch();
    }
  });

  if (catalog.isPending) {
    return (
      <section className="page-content configuration-page" aria-label="正在加载配置中心">
        <Skeleton active paragraph={{ rows: 12 }} />
      </section>
    );
  }
  if (catalog.isError || !catalog.data) {
    return (
      <section className="page-content">
        <Result
          status="warning"
          title="配置中心加载失败"
          subTitle={catalog.error instanceof Error ? catalog.error.message : "请稍后重试"}
          extra={<Button type="primary" onClick={() => void catalog.refetch()}>重新加载</Button>}
        />
      </section>
    );
  }

  const updateDraft = (field: ConfigurationField, value: DraftValue) => {
    setDraft((current) => ({ ...current, [field.key]: value }));
    if (mutation.isError) mutation.reset();
  };
  const requestSave = () => {
    if (!dirtyFields.length || Object.keys(errors).length) return;
    if (riskyEffects.length) setConfirmOpen(true);
    else mutation.mutate();
  };

  return (
    <section className="page-content configuration-page">
      <ConfigurationSectionNav />
      <div className="page-intro configuration-page-intro">
        <Paragraph>
          进入本页时才读取完整配置。每次保存只提交已修改字段；默认代理为只写秘密，页面只显示是否已配置。
        </Paragraph>
        <Button icon={<ReloadOutlined aria-hidden="true" />} loading={catalog.isFetching} onClick={() => void catalog.refetch()}>
          刷新当前页
        </Button>
      </div>

      {notice ? <Alert className="page-alert" type="success" showIcon closable title={notice} onClose={() => setNotice("")} /> : null}
      {mutation.isError ? (
        <Alert
          className="page-alert"
          type="error"
          showIcon
          title="配置未保存"
          description={mutation.error instanceof ApiError ? mutation.error.message : "请稍后重试"}
        />
      ) : null}

      <Card className="configuration-action-card">
        <div className="configuration-action-summary">
          <div>
            <Text type="secondary">当前修改</Text>
            <strong>{dirtyFields.length ? `${dirtyFields.length} 项未保存` : "没有未保存修改"}</strong>
          </div>
          <Space wrap size={[6, 6]}>
            {dirtyModes.length
              ? dirtyModes.map((mode) => <Tag key={mode} color={applyModeColor(mode)}>{applyModeLabel(mode)}</Tag>)
              : <Tag>修改后将在这里汇总生效影响</Tag>}
          </Space>
          <Space wrap>
            <Button
              icon={<UndoOutlined aria-hidden="true" />}
              disabled={!dirtyFields.length || mutation.isPending}
              onClick={() => setDraft(configurationDraft(catalog.data))}
            >
              撤销全部
            </Button>
            <Button
              type="primary"
              icon={<SaveOutlined aria-hidden="true" />}
              loading={mutation.isPending}
              disabled={!dirtyFields.length || Object.keys(errors).length > 0}
              onClick={requestSave}
            >
              保存配置
            </Button>
          </Space>
        </div>
        {Object.keys(errors).length ? (
          <Alert className="configuration-validation-alert" type="warning" showIcon title={`请先修正 ${Object.keys(errors).length} 个字段`} />
        ) : null}
      </Card>

      <div className="configuration-workspace">
        <aside className="configuration-sidebar" aria-label="配置分类">
          <Card>
            <Input
              allowClear
              aria-label="搜索配置"
              prefix={<SearchOutlined aria-hidden="true" />}
              placeholder="搜索名称、说明或 Key"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
            />
            <nav className="configuration-group-nav">
              {catalog.data.groups.map((group) => {
                const dirtyCount = group.fields.filter((field) => dirtyFields.some((item) => item.key === field.key)).length;
                return (
                  <button
                    key={group.name}
                    type="button"
                    className={!normalizedSearch && selected?.name === group.name ? "active" : ""}
                    onClick={() => { setSearch(""); setSelectedGroup(group.name); }}
                  >
                    <span>{group.name}</span>
                    <small className={dirtyCount ? "dirty" : ""}>{dirtyCount ? `${dirtyCount} 项修改` : `${group.fields.length} 项`}</small>
                  </button>
                );
              })}
            </nav>
            <Text className="configuration-catalog-meta" type="secondary">
              共 {catalog.data.field_count} 项 · 版本 {catalog.data.version}
            </Text>
          </Card>
        </aside>

        <Card className="configuration-fields-card">
          <div className="configuration-group-heading">
            <div>
              <Text className="eyebrow">{normalizedSearch ? "CONFIGURATION SEARCH" : "CONFIGURATION GROUP"}</Text>
              <Title level={3}>{normalizedSearch ? `“${search.trim()}”的搜索结果` : selected?.name}</Title>
              <Paragraph>{normalizedSearch ? `找到 ${visibleFields.length} 项配置` : selected?.description}</Paragraph>
            </div>
            <Tag>{visibleFields.length} 项</Tag>
          </div>

          {visibleFields.length ? (
            <Form layout="vertical" requiredMark={false} className="configuration-fields">
              {visibleFields.map((field) => (
                <ConfigurationEditor
                  key={field.key}
                  field={field}
                  group={field.group}
                  value={draft[field.key]}
                  error={errors[field.key]}
                  dirty={dirtyFields.some((item) => item.key === field.key)}
                  searching={Boolean(normalizedSearch)}
                  onChange={(value) => updateDraft(field, value)}
                />
              ))}
            </Form>
          ) : <Empty description="没有匹配的配置项" />}
        </Card>
      </div>

      <Modal
        open={confirmOpen}
        title="保存并应用配置？"
        okText="保存并应用"
        cancelText="继续检查"
        confirmLoading={mutation.isPending}
        onCancel={() => setConfirmOpen(false)}
        onOk={() => mutation.mutate()}
      >
        <Space orientation="vertical" size={14} className="configuration-confirmation">
          <Alert
            type="warning"
            showIcon
            icon={<WarningOutlined aria-hidden="true" />}
            title={`${dirtyFields.length} 项配置将被保存`}
            description="如果运行时应用失败，控制面会尝试恢复原配置。"
          />
          <Space wrap>{dirtyModes.map((mode) => <Tag key={mode} color={applyModeColor(mode)}>{applyModeLabel(mode)}</Tag>)}</Space>
        </Space>
      </Modal>
    </section>
  );
}

type EditorField = ConfigurationField & { group: string };

function ConfigurationEditor({ field, group, value, error, dirty, searching, onChange }: {
  field: EditorField;
  group: string;
  value: DraftValue;
  error?: string;
  dirty: boolean;
  searching: boolean;
  onChange: (value: DraftValue) => void;
}) {
  return (
    <article className={`configuration-field-row${dirty ? " dirty" : ""}`} data-configuration-key={field.key}>
      <div className="configuration-field-copy">
        <div className="configuration-field-title">
          <Text strong>{field.label}</Text>
          {searching ? <Tag>{group}</Tag> : null}
        </div>
        <Paragraph>{field.description}</Paragraph>
      </div>
      <Form.Item
        className="configuration-field-control"
        validateStatus={error ? "error" : undefined}
        help={error || configurationHint(field)}
      >
        <ConfigurationControl field={field} value={value} onChange={onChange} />
      </Form.Item>
      <div className="configuration-field-meta">
        <Tag color={applyModeColor(field.apply_mode)}>{applyModeLabel(field.apply_mode)}</Tag>
        <code>{field.key}</code>
        <small>默认：{configurationValueLabel(field.default, field.unit)}</small>
      </div>
    </article>
  );
}

function ConfigurationControl({ field, value, onChange }: {
  field: ConfigurationField;
  value: DraftValue;
  onChange: (value: DraftValue) => void;
}) {
  if (field.type === "boolean") {
    return <Switch aria-label={field.label} checked={Boolean(value)} checkedChildren="已启用" unCheckedChildren="已关闭" onChange={onChange} />;
  }
  if (field.type === "choice") {
    return (
      <Select
        aria-label={field.label}
        value={String(value ?? "")}
        options={(field.choices ?? []).map((choice) => ({ value: choice.value, label: choice.label }))}
        onChange={onChange}
      />
    );
  }
  if (["integer", "number", "nullable_integer"].includes(field.type)) {
    return (
      <InputNumber
        aria-label={field.label}
        value={typeof value === "number" ? value : null}
        min={field.min}
        max={field.max}
        step={field.type === "number" ? 0.1 : 1}
        stringMode={false}
        addonAfter={field.unit}
        placeholder={field.type === "nullable_integer" ? "不限额" : undefined}
        onChange={(next) => onChange(typeof next === "number" ? next : null)}
      />
    );
  }
  if (field.type === "domain_list") {
    return (
      <Input.TextArea
        aria-label={field.label}
        autoSize={{ minRows: 2, maxRows: 4 }}
        value={String(value ?? "")}
        placeholder="example.com, example.org"
        onChange={(event) => onChange(event.target.value)}
      />
    );
  }
  if (field.type === "color") {
    return (
      <Space.Compact className="configuration-color-control">
        <Input aria-label={`${field.label}颜色`} type="color" value={String(value ?? "#000000")} onChange={(event) => onChange(event.target.value)} />
        <Input aria-label={field.label} value={String(value ?? "")} onChange={(event) => onChange(event.target.value)} />
      </Space.Compact>
    );
  }
  const secret = field.type === "proxy_url_secret";
  const InputComponent = secret ? Input.Password : Input;
  return (
    <InputComponent
      aria-label={field.label}
      value={String(value ?? "")}
      maxLength={field.max_length}
      autoComplete={secret ? "new-password" : "off"}
      placeholder={secret
        ? (field.configured ? "已配置；留空保持不变" : "socks5://user:pass@host:1080")
        : undefined}
      addonAfter={field.unit}
      onChange={(event) => onChange(event.target.value)}
    />
  );
}

function flattenConfiguration(catalog?: ConfigurationCatalog): EditorField[] {
  return catalog?.groups.flatMap((group) => group.fields.map((field) => ({ ...field, group: group.name }))) ?? [];
}

function configurationDraft(catalog: ConfigurationCatalog): Draft {
  return Object.fromEntries(flattenConfiguration(catalog).map((field) => [field.key, draftValue(field)]));
}

function draftValue(field: ConfigurationField): DraftValue {
  if (field.type === "domain_list") return Array.isArray(field.value) ? field.value.join(", ") : "";
  if (field.type === "proxy_url_secret") return "";
  if (Array.isArray(field.value)) return field.value.join(", ");
  return field.value;
}

function normalizeDraftValue(field: ConfigurationField, value: DraftValue): ConfigurationValue {
  if (field.type === "proxy_url_secret" && String(value ?? "").trim() === "") return field.value;
  if (field.type === "domain_list") {
    return String(value ?? "").split(/[,，\s]+/).map((item) => item.trim()).filter(Boolean);
  }
  if (typeof value === "string") return value.trim();
  return value;
}

function sameConfigurationValue(left: ConfigurationValue, right: ConfigurationValue): boolean {
  return JSON.stringify(left) === JSON.stringify(right);
}

function validateDraftValue(field: ConfigurationField, raw: DraftValue): string {
  if (field.type === "nullable_integer" && raw === null) return "";
  if (["integer", "number", "nullable_integer"].includes(field.type)) {
    if (typeof raw !== "number" || !Number.isFinite(raw)) return "请输入有效数字";
    if ((field.type === "integer" || field.type === "nullable_integer") && !Number.isInteger(raw)) return "请输入整数";
    if (field.min !== undefined && raw < field.min) return `不能小于 ${field.min}`;
    if (field.max !== undefined && raw > field.max) return `不能大于 ${field.max}`;
    return "";
  }
  if (field.type === "boolean" || field.type === "choice" || field.type === "domain_list") return "";
  const value = String(raw ?? "").trim();
  if (field.type === "proxy_url_secret" && value === "") return "";
  if (field.type === "optional_text" || field.type === "optional_image" || field.type === "base_url") {
    if (value === "") return "";
  } else if (!value) return "不能为空";
  if (field.min_length !== undefined && [...value].length < field.min_length) return `至少输入 ${field.min_length} 个字符`;
  if (field.max_length !== undefined && [...value].length > field.max_length) return `最多输入 ${field.max_length} 个字符`;
  if (field.type === "key_prefix" && !/^[a-z][a-z0-9_]{1,30}_$/.test(value)) return "请输入 3-32 位小写前缀，并以下划线结尾";
  if (field.type === "env_name" && !/^[A-Z][A-Z0-9_]{1,63}$/.test(value)) return "请输入有效的大写环境变量名";
  if (field.type === "color" && !/^#[0-9a-fA-F]{6}$/.test(value)) return "请输入 #RRGGBB 颜色";
  if (field.type === "duration" && !/^[1-9][0-9]*[smhd]$/.test(value)) return "请输入 30s、5m、1h 或 7d 格式";
  if (field.type === "time_list" && !/^([01]?\d|2[0-3]):[0-5]\d(?:\s*[,，]\s*([01]?\d|2[0-3]):[0-5]\d)*$/.test(value)) return "请输入 HH:MM，多个时间使用逗号分隔";
  if ((field.type === "base_url" || field.type === "proxy_url_secret") && !validConfigurationURL(value, field.type === "proxy_url_secret")) return "请输入有效的 HTTP(S) 或 SOCKS5 根地址";
  if (field.type === "ip" && !validIPv4(value)) return "请输入有效 IPv4 地址";
  if ((field.type === "image" || field.type === "optional_image") && !/^[A-Za-z0-9._:/@-]+$/.test(value)) return "镜像名称格式无效";
  if (field.digest_required && !/^[A-Za-z0-9._:/-]+@sha256:[0-9a-f]{64}$/.test(value)) return "必须使用 name:tag@sha256:digest 固定镜像";
  return "";
}

function validConfigurationURL(value: string, proxy: boolean): boolean {
  try {
    const parsed = new URL(value);
    if (!["http:", "https:", ...(proxy ? ["socks5:"] : [])].includes(parsed.protocol)) return false;
    if (!parsed.hostname || parsed.pathname !== "/" || parsed.search || parsed.hash) return false;
    return proxy || (!parsed.username && !parsed.password);
  } catch {
    return false;
  }
}

function validIPv4(value: string): boolean {
  const parts = value.split(".");
  return parts.length === 4 && parts.every((part) => /^\d{1,3}$/.test(part) && Number(part) >= 0 && Number(part) <= 255);
}

function configurationHint(field: ConfigurationField): string {
  if (field.type === "proxy_url_secret") return field.configured ? "当前秘密已配置；留空不会覆盖。" : "秘密只会提交到控制面，不会回显。";
  if (field.type === "nullable_integer") return "留空表示不限额。";
  if (field.min !== undefined || field.max !== undefined) return `允许范围：${field.min ?? "不限"} 至 ${field.max ?? "不限"}${field.unit ? ` ${field.unit}` : ""}`;
  return " ";
}

function configurationValueLabel(value: ConfigurationValue, unit?: string): string {
  if (value === null || value === "") return "空";
  if (Array.isArray(value)) return value.length ? value.join(", ") : "空";
  if (typeof value === "boolean") return value ? "启用" : "关闭";
  return `${value}${unit ? ` ${unit}` : ""}`;
}

function applyModeLabel(mode: ConfigurationField["apply_mode"]): string {
  return ({
    live: "立即生效",
    accounts: "重建业务 CPA",
    collector: "重启采集器",
    future: "仅新账号",
    deployment: "下次部署",
    quota: "下次采集生效"
  })[mode];
}

function applyModeColor(mode: ConfigurationField["apply_mode"]): string {
  return ({ live: "green", accounts: "volcano", collector: "orange", future: "blue", deployment: "purple", quota: "gold" })[mode];
}
