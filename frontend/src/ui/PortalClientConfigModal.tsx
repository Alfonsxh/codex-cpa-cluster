import { Alert, Button, Modal, Skeleton, Space, Typography } from "antd";
import { useEffect, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";

import { ApiError } from "../api/client";
import { readPortalKey } from "../api/portal";
import {
  defaultPublicSiteConfiguration,
  readPublicSiteConfiguration,
  type PublicSiteConfiguration
} from "../api/public-site";

export type PortalClientConfigMode = "codex" | "claude" | "ccswitch";

type ClientConfig = {
  title: string;
  file?: string;
  steps?: string[];
  notice?: string;
  value: string;
  sections?: ClientConfigSection[];
  copyLabel?: string;
  externalLink?: string;
};

type ClientConfigSection = {
  title: string;
  file: string;
  description: string;
  value: string;
  hint?: string;
  copyLabel?: string;
};

const historyPrompt = "由于登录方式已从 OAuth 变为 API Key，请将 Codex 之前的会话迁移到当前 API Key 的会话历史中。";

export function PortalClientConfigModal({
  open,
  mode,
  user,
  currentGroup,
  onClose,
  onSessionExpired
}: {
  open: boolean;
  mode: PortalClientConfigMode;
  user: string;
  currentGroup: string;
  onClose: () => void;
  onSessionExpired: () => void;
}) {
  const [apiKey, setAPIKey] = useState("");
  const [siteConfig, setSiteConfig] = useState<PublicSiteConfiguration>(defaultPublicSiteConfiguration);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [copied, setCopied] = useState("");
  const request = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!open) return;
    setCopied("");
    setError("");
  }, [mode, open]);

  useEffect(() => {
    if (!open) return;
    request.current?.abort();
    const controller = new AbortController();
    request.current = controller;
    setLoading(true);
    setAPIKey("");
    void Promise.all([
      readPortalKey(controller.signal),
      readPublicSiteConfiguration(controller.signal).catch(() => defaultPublicSiteConfiguration)
    ]).then(([key, configuration]) => {
      if (request.current !== controller) return;
      setAPIKey(key.api_key);
      setSiteConfig(configuration);
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || request.current !== controller) return;
      if (reason instanceof ApiError && reason.status === 401) {
        onSessionExpired();
        return;
      }
      setError(reason instanceof Error ? reason.message : "客户端配置读取失败");
    }).finally(() => {
      if (request.current !== controller) return;
      request.current = null;
      setLoading(false);
    });
    return () => controller.abort();
  }, [mode, onSessionExpired, open]);

  const config = useMemo(() => buildClientConfig({
    mode,
    apiKey,
    user,
    currentGroup,
    siteConfig,
    browserOrigin: typeof window === "undefined" ? "" : window.location.origin
  }), [apiKey, currentGroup, mode, siteConfig, user]);

  const close = () => {
    request.current?.abort();
    request.current = null;
    // The generated snippets contain the full Key. Clear them synchronously
    // before Ant Design runs its closing animation and keeps the Modal mounted.
    flushSync(() => {
      setAPIKey("");
      setError("");
      setCopied("");
      setLoading(false);
    });
    onClose();
  };

  const copy = async (value: string, label = "已复制") => {
    try {
      await writeClipboardText(value);
      setCopied(label);
    } catch {
      setCopied("复制失败，请手动选择配置内容");
    }
  };

  const copyAndImport = async () => {
    if (!config.externalLink) return;
    const result = await copyAndImportConfig({
      value: config.value,
      externalLink: config.externalLink,
      writeText: writeClipboardText,
      openLink: (link) => window.location.assign(link)
    });
    flushSync(() => setCopied(result.message));
  };

  return (
    <Modal
      className="portal-client-config-modal"
      title={config.title}
      open={open}
      width={mode === "claude" || mode === "ccswitch" ? 960 : 760}
      footer={null}
      onCancel={close}
      destroyOnHidden
    >
      {loading ? (
        <Skeleton active paragraph={{ rows: 8 }} />
      ) : error ? (
        <Space orientation="vertical" size={14} className="portal-config-stack">
          <Alert type="error" showIcon title="客户端配置读取失败" description={error} />
          <Button onClick={close}>关闭</Button>
        </Space>
      ) : apiKey ? (
        <Space orientation="vertical" size={16} className="portal-config-stack">
          {mode === "claude" && config.notice ? <Alert type="warning" showIcon title="配置包含完整 API Key" description={config.notice} /> : null}
          {mode === "claude" ? (
            <div className="portal-config-guide">
              <div><Typography.Text type="secondary">操作文件</Typography.Text><code>{config.file}</code></div>
              <div><Typography.Text type="secondary">操作步骤</Typography.Text><ol>{config.steps?.map((step) => <li key={step}>{step}</li>)}</ol></div>
            </div>
          ) : null}
          {config.sections?.length ? (
            <div className="portal-config-workflow">
              {config.sections.map((section, index) => (
                <article className="portal-config-step" key={section.title}>
                  <span className="portal-config-step-number">{String(index + 1).padStart(2, "0")}</span>
                  <div>
                    <header>
                      <span><strong>{section.title}</strong><code>{section.file}</code></span>
                      {section.copyLabel ? <Button size="small" onClick={() => void copy(section.value, `${section.title}已复制`)}>{section.copyLabel}</Button> : null}
                    </header>
                    <p>{section.description}</p>
                    <pre className="portal-config-preview"><code>{section.value}</code></pre>
                    {section.hint ? <small>{section.hint}</small> : null}
                  </div>
                </article>
              ))}
            </div>
          ) : <pre className="portal-config-preview"><code>{config.value}</code></pre>}
          <ConfigActions
            copied={copied}
            onClose={close}
            onCopy={mode === "codex" ? undefined : mode === "ccswitch" ? () => void copyAndImport() : () => void copy(config.value, "配置已复制")}
            copyLabel={mode === "ccswitch" ? "复制并导入" : config.copyLabel ?? "复制配置"}
          />
        </Space>
      ) : null}
    </Modal>
  );
}

function ConfigActions({
  copied,
  onClose,
  onCopy,
  copyLabel,
  closeLabel = "关闭"
}: {
  copied: string;
  onClose: () => void;
  onCopy?: () => void;
  copyLabel: string;
  closeLabel?: string;
}) {
  return (
    <div className="portal-config-actions">
      <Typography.Text type={copied.startsWith("复制失败") ? "danger" : "secondary"} role="status">{copied}</Typography.Text>
      <Space wrap>
        <Button onClick={onClose}>{closeLabel}</Button>
        {onCopy ? <Button type="primary" onClick={onCopy}>{copyLabel}</Button> : null}
      </Space>
    </div>
  );
}

export function buildClientConfig({
  mode,
  apiKey,
  user,
  currentGroup,
  siteConfig,
  browserOrigin
}: {
  mode: PortalClientConfigMode;
  apiKey: string;
  user: string;
  currentGroup: string;
  siteConfig: PublicSiteConfiguration;
  browserOrigin: string;
}): ClientConfig {
  const origin = publicBaseURL(siteConfig.public_base_url, browserOrigin);
  const baseURL = `${origin}/v1`;
  const model = siteConfig.default_model || "gpt-5.6-sol";
  const provider = `${siteConfig.provider_name || "Codex CPA"} · ${user.split("@", 1)[0] || "user"}`;
  const environment = siteConfig.api_key_env || "CPA_API_KEY";
  const codex = buildCodexConfig(provider, baseURL, model, apiKey);
  if (mode === "codex") {
    return {
      title: "配置 Codex",
      value: codex,
      sections: [
        {
          title: "Codex 配置内容",
          file: "~/.codex/config.toml",
          description: "将下方内容合并到 Codex 配置文件，保存后重新启动 Codex。",
          value: codex,
          hint: "配置包含当前 API Key，仅在自己的可信设备保存。",
          copyLabel: "复制配置"
        },
        {
          title: "迁移旧会话",
          file: "Codex Agent",
          description: "将下方指令交给 Codex Agent，把 OAuth 登录时期的会话迁移到当前 API Key 会话历史。",
          value: historyPrompt,
          copyLabel: "复制迁移指令"
        }
      ]
    };
  }
  const launcher = buildClaudeLauncher(environment, origin, model);
  if (mode === "claude") {
    return {
      title: "Claude Code 终端配置",
      file: "~/.config/claude-cpa/",
      steps: ["准备目录", "保存 Key", "创建启动脚本", "加载并验证"],
      value: launcher,
      notice: "以下内容已包含你的完整 API Key。仅在可信设备保存，不要粘贴到聊天、Issue 或 Git 仓库。",
      copyLabel: "复制启动脚本",
      sections: [
        {
          title: "准备配置目录", file: "终端", description: "先确认 Claude Code 已安装，再创建仅当前用户可访问的配置目录。",
          value: 'claude --version\nmkdir -p "$HOME/.config/claude-cpa"\nchmod 700 "$HOME/.config/claude-cpa"', copyLabel: "复制命令"
        },
        {
          title: "保存当前 API Key", file: "~/.config/claude-cpa/env", description: "新建此文件并粘贴下方内容。该文件包含完整 Key，不要提交到 Git。",
          value: `${environment}=${shellQuote(apiKey)}\n`, hint: '保存后执行：chmod 600 "$HOME/.config/claude-cpa/env"', copyLabel: "复制文件内容"
        },
        {
          title: "创建 claude_cpa 启动脚本", file: "~/.config/claude-cpa/claude-cpa.zsh", description: `此函数仅影响 claude_cpa：通过当前网关使用 ${model}，推理强度固定为 xhigh。`,
          value: launcher, hint: '保存后执行：chmod 600 "$HOME/.config/claude-cpa/claude-cpa.zsh"', copyLabel: "复制启动脚本"
        },
        {
          title: "加载终端命令", file: "~/.zshrc", description: "将这一行追加到文件末尾，让每个新终端都能使用 claude_cpa。",
          value: 'source "$HOME/.config/claude-cpa/claude-cpa.zsh"\n', copyLabel: "复制加载配置"
        },
        {
          title: "加载并验证", file: "终端", description: "重新加载配置，检查函数存在，再发送一个最小请求验证网关。",
          value: 'source "$HOME/.zshrc"\ntype claude_cpa\nclaude_cpa -p \'Reply only: OK\' --output-format text\nclaude_cpa', copyLabel: "复制验证命令"
        }
      ]
    };
  }
  const params = new URLSearchParams({
    resource: "provider",
    app: "codex",
    name: provider,
    endpoint: baseURL,
    apiKey: "PASTE_API_KEY_AFTER_IMPORT",
    homepage: `${origin}/usage/`,
    model,
    notes: `${siteConfig.product_name} · ${currentGroup} · 导入链接不携带 API Key；请按使用中心提示粘贴完整 config.toml`
  });
  return {
    title: "完成 CC Switch 配置",
    value: codex,
    copyLabel: "复制并导入",
    sections: [
      {
        title: "Codex 配置内容",
        file: "~/.codex/config.toml",
        description: "“复制并导入”会先复制下方完整配置，再打开 CC Switch。",
        value: codex
      },
      {
        title: "迁移旧会话",
        file: "Codex Agent",
        description: "将下方指令交给 Codex Agent，把 OAuth 登录时期的会话迁移到当前 API Key 会话历史。",
        value: historyPrompt,
        copyLabel: "复制迁移指令"
      }
    ],
    externalLink: `ccswitch://v1/import?${params.toString()}`,
  };
}

export async function copyAndImportConfig({
  value,
  externalLink,
  writeText,
  openLink
}: {
  value: string;
  externalLink: string;
  writeText: (value: string) => Promise<void>;
  openLink: (link: string) => void;
}) {
  try {
    await writeText(value);
  } catch (error) {
    const permissionDenied = error instanceof DOMException && error.name === "NotAllowedError";
    return {
      status: "copy_failed" as const,
      message: permissionDenied
        ? "复制失败：剪贴板权限被拒绝，未打开 CC Switch"
        : "复制失败：未打开 CC Switch，请允许剪贴板访问后重试"
    };
  }
  try {
    openLink(externalLink);
    return { status: "opened" as const, message: "完整配置已复制，正在打开 CC Switch…" };
  } catch {
    return { status: "open_failed" as const, message: "配置已复制，但无法打开 CC Switch，请确认已安装" };
  }
}

export async function writeClipboardText(
  value: string,
  writers: {
    asyncWriter?: ((text: string) => Promise<void>) | null;
    legacyWriter?: (text: string) => boolean;
  } = {}
) {
  const asyncWriter = writers.asyncWriter === undefined
    ? (typeof navigator !== "undefined" && navigator.clipboard?.writeText
        ? navigator.clipboard.writeText.bind(navigator.clipboard)
        : null)
    : writers.asyncWriter;
  if (asyncWriter) {
    // A rejected Clipboard API call represents an explicit browser decision.
    // Do not bypass it with execCommand; the fallback is only for HTTP origins
    // where navigator.clipboard is unavailable altogether.
    await asyncWriter(value);
    return;
  }
  const legacyWriter = writers.legacyWriter ?? legacyClipboardWrite;
  if (legacyWriter(value)) return;
  throw new Error("clipboard unavailable");
}

function legacyClipboardWrite(value: string) {
  if (typeof document === "undefined" || !document.body || typeof document.execCommand !== "function") {
    return false;
  }
  const textarea = document.createElement("textarea");
  textarea.value = value;
  textarea.readOnly = true;
  textarea.setAttribute("aria-hidden", "true");
  textarea.style.position = "fixed";
  textarea.style.inset = "0 auto auto -9999px";
  textarea.style.opacity = "0";
  document.body.appendChild(textarea);
  textarea.select();
  textarea.setSelectionRange(0, value.length);
  try {
    return document.execCommand("copy");
  } finally {
    textarea.remove();
  }
}

function buildCodexConfig(provider: string, baseURL: string, model: string, apiKey: string) {
  return [
    'model_provider = "custom"',
    `model = ${tomlString(model)}`,
    'model_reasoning_effort = "xhigh"',
    'plan_mode_reasoning_effort = "max"',
    "",
    "[model_providers.custom]",
    `name = ${tomlString(provider)}`,
    `base_url = ${tomlString(baseURL)}`,
    'wire_api = "responses"',
    "requires_openai_auth = false",
    'http_headers = { "X-OpenAI-Actor-Authorization" = "local-proxy" }',
    `experimental_bearer_token = ${tomlString(apiKey)}`,
    ""
  ].join("\n");
}

function buildClaudeLauncher(environment: string, origin: string, model: string) {
  return [
    "claude_cpa() (",
    '  local env_file="${HOME}/.config/claude-cpa/env"',
    '  if [[ ! -r "$env_file" ]]; then print -u2 "claude_cpa: missing $env_file"; return 1; fi',
    '  source "$env_file"',
    `  if [[ -z "\${${environment}:-}" ]]; then print -u2 "claude_cpa: ${environment} is empty"; return 1; fi`,
    "  unset http_proxy https_proxy HTTP_PROXY HTTPS_PROXY all_proxy ALL_PROXY",
    "  unset ANTHROPIC_API_KEY CLAUDE_CODE_OAUTH_TOKEN ANTHROPIC_FOUNDRY_API_KEY",
    `  export ANTHROPIC_AUTH_TOKEN="$${environment}"`,
    `  unset ${environment}`,
    `  export ANTHROPIC_BASE_URL=${shellQuote(origin)}`,
    `  export ANTHROPIC_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_SMALL_FAST_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_OPUS_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_SONNET_MODEL=${shellQuote(model)}`,
    `  export ANTHROPIC_DEFAULT_HAIKU_MODEL=${shellQuote(model)}`,
    `  export CLAUDE_CODE_SUBAGENT_MODEL=${shellQuote(model)}`,
    '  export CLAUDE_CODE_EFFORT_LEVEL="xhigh"',
    "  export CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
    "  export CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1",
    "  export CLAUDE_CODE_DISABLE_1M_CONTEXT=1",
    "  export ENABLE_CLAUDEAI_MCP_SERVERS=0",
    "  export API_TIMEOUT_MS=600000",
    "  export CLAUDE_BASH_MAINTAIN_PROJECT_WORKING_DIR=1",
    '  command claude --dangerously-skip-permissions --verbose --effort xhigh "$@"',
    ")",
    ""
  ].join("\n");
}

function publicBaseURL(configured: string, fallback: string) {
  try {
    const parsed = new URL(configured.trim().replace(/\/+$/, "") || fallback);
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) throw new Error("invalid URL");
    return parsed.href.replace(/\/+$/, "");
  } catch {
    return fallback.replace(/\/+$/, "");
  }
}

function tomlString(value: string) {
  return JSON.stringify(value);
}

function shellQuote(value: string) {
  return `'${String(value).replaceAll("'", `'"'"'`)}'`;
}
