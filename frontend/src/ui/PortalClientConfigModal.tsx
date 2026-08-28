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

export type PortalClientConfigMode = "codex" | "claude" | "ccswitch" | "history";
type CodexTask = "choose" | "config" | "history";

type ClientConfig = {
  title: string;
  file: string;
  steps: string[];
  notice?: string;
  value: string;
  sections?: ClientConfigSection[];
  copyLabel?: string;
  externalLink?: string;
  externalLabel?: string;
};

type ClientConfigSection = {
  title: string;
  file: string;
  description: string;
  value: string;
  hint?: string;
  copyLabel: string;
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
  const [codexTask, setCodexTask] = useState<CodexTask>("choose");
  const request = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!open) return;
    setCopied("");
    setError("");
    if (mode === "codex") setCodexTask("choose");
  }, [mode, open]);

  useEffect(() => {
    if (!open) return;
    const requiresKey = mode !== "history" && (mode !== "codex" || codexTask === "config");
    if (!requiresKey) {
      request.current?.abort();
      request.current = null;
      setAPIKey("");
      setLoading(false);
      return;
    }
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
  }, [codexTask, mode, onSessionExpired, open]);

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
      setCodexTask("choose");
    });
    onClose();
  };

  const copy = async (value: string, label = "已复制") => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(label);
    } catch {
      setCopied("复制失败，请手动选择配置内容");
    }
  };

  const showingHistory = mode === "history" || (mode === "codex" && codexTask === "history");
  const title = showingHistory ? "迁移 Codex OAuth 历史会话" : mode === "codex" ? "配置 Codex" : config.title;
  return (
    <Modal
      className="portal-client-config-modal"
      title={title}
      open={open}
      width={mode === "claude" || mode === "ccswitch" ? 960 : 760}
      footer={null}
      onCancel={close}
      destroyOnHidden
    >
      {mode === "codex" && codexTask === "choose" ? (
        <Space orientation="vertical" size={16} className="portal-config-stack">
          <Alert type="info" showIcon title="选择要完成的 Codex 任务" description="只有生成含 Key 的配置时才会读取 API Key；迁移旧会话只提供可复制指令。" />
          <div className="portal-codex-task-grid">
            <button type="button" onClick={() => setCodexTask("config")}>
              <strong>生成 Codex 配置</strong>
              <span>按需读取当前 API Key，生成 Responses Provider 配置。</span>
            </button>
            <button type="button" onClick={() => setCodexTask("history")}>
              <strong>迁移旧会话</strong>
              <span>复制迁移指令交给 Codex Agent，不读取任何 API Key。</span>
            </button>
          </div>
          <div className="portal-config-actions"><span /><Button onClick={close}>关闭</Button></div>
        </Space>
      ) : showingHistory ? (
        <Space orientation="vertical" size={16} className="portal-config-stack">
          <Alert
            type="info"
            showIcon
            title="把提示交给 Codex Agent"
            description="提示不包含 API Key、OAuth Token 或 auth.json 内容。"
          />
          <Typography.Text strong>可复制的 Agent 提示</Typography.Text>
          <pre className="portal-config-preview"><code>{historyPrompt}</code></pre>
          <ConfigActions
            copied={copied}
            onClose={mode === "codex" ? () => setCodexTask("choose") : close}
            onCopy={() => void copy(historyPrompt, "迁移指令已复制")}
            copyLabel="复制迁移指令"
            closeLabel={mode === "codex" ? "返回" : "关闭"}
          />
        </Space>
      ) : loading ? (
        <Skeleton active paragraph={{ rows: 8 }} />
      ) : error ? (
        <Space orientation="vertical" size={14} className="portal-config-stack">
          <Alert type="error" showIcon title="客户端配置读取失败" description={error} />
          <Button onClick={close}>关闭</Button>
        </Space>
      ) : apiKey ? (
        <Space orientation="vertical" size={16} className="portal-config-stack">
          {config.notice ? <Alert type="warning" showIcon title="配置包含完整 API Key" description={config.notice} /> : null}
          <div className="portal-config-guide">
            <div><Typography.Text type="secondary">操作文件</Typography.Text><code>{config.file}</code></div>
            <div><Typography.Text type="secondary">操作步骤</Typography.Text><ol>{config.steps.map((step) => <li key={step}>{step}</li>)}</ol></div>
          </div>
          {config.sections?.length ? (
            <div className="portal-config-workflow">
              {config.sections.map((section, index) => (
                <article className="portal-config-step" key={section.title}>
                  <span className="portal-config-step-number">{String(index + 1).padStart(2, "0")}</span>
                  <div>
                    <header>
                      <span><strong>{section.title}</strong><code>{section.file}</code></span>
                      <Button size="small" onClick={() => void copy(section.value, `${section.title}已复制`)}>{section.copyLabel}</Button>
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
            onClose={mode === "codex" ? () => {
              request.current?.abort();
              request.current = null;
              setAPIKey("");
              setCodexTask("choose");
            } : close}
            onCopy={() => void copy(config.value, "配置已复制")}
            copyLabel={config.copyLabel ?? "复制配置"}
            externalLink={config.externalLink}
            externalLabel={config.externalLabel}
            closeLabel={mode === "codex" ? "返回" : "关闭"}
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
  externalLink,
  externalLabel,
  closeLabel = "关闭"
}: {
  copied: string;
  onClose: () => void;
  onCopy: () => void;
  copyLabel: string;
  externalLink?: string;
  externalLabel?: string;
  closeLabel?: string;
}) {
  return (
    <div className="portal-config-actions">
      <Typography.Text type={copied.startsWith("复制失败") ? "danger" : "secondary"} role="status">{copied}</Typography.Text>
      <Space wrap>
        <Button onClick={onClose}>{closeLabel}</Button>
        <Button type={externalLink ? "default" : "primary"} onClick={onCopy}>{copyLabel}</Button>
        {externalLink ? (
          <Button
            type="primary"
            href={externalLink}
            onClick={() => void navigator.clipboard.writeText(document.querySelector(".portal-config-preview code")?.textContent ?? "")}
          >
            {externalLabel ?? "继续"}
          </Button>
        ) : null}
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
      title: "Codex 配置",
      file: "~/.codex/config.toml",
      steps: ["打开或创建上述文件", "合并下方配置并保存", "重新启动 Codex"],
      notice: "以下配置已包含你的完整 API Key。仅在可信设备保存，不要粘贴到聊天、Issue 或 Git 仓库。",
      value: codex
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
    title: "完成 CC Switch 图片配置",
    file: "CC Switch → Codex → CPA Provider → 编辑 → config.toml",
    steps: [
      "点击“复制完整配置并继续导入”，在 CC Switch 中确认导入；导入链接只携带非敏感占位值，不携带真实 API Key",
      "编辑刚导入的 CPA Provider，将已复制的内容完整替换到 config.toml",
      "保存并切换到该 Provider；无需开启 CC Switch 本地路由",
      "完全退出并重新启动 Codex，然后新建任务"
    ],
    value: codex,
    notice: "一键导入只带入地址、模型和非敏感占位值，不会把真实 API Key 放入 URL。继续导入时会将下方完整配置复制到剪贴板；请在 CC Switch 中粘贴覆盖 config.toml。配置含完整 API Key，仅在自己的设备使用。",
    copyLabel: "仅复制图片配置",
    externalLink: `ccswitch://v1/import?${params.toString()}`,
    externalLabel: "复制完整配置并继续导入"
  };
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
