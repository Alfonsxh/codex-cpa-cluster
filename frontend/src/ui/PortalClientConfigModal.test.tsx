import { describe, expect, it, vi } from "vitest";

import { defaultPublicSiteConfiguration } from "../api/public-site";
import { buildClientConfig, copyAndImportConfig } from "./PortalClientConfigModal";

const input = {
  apiKey: "secret-api-key",
  user: "alice@example.com",
  currentGroup: "cpa-main",
  browserOrigin: "http://127.0.0.1:5194",
  siteConfig: {
    ...defaultPublicSiteConfiguration,
    product_name: "Codex CPA Cluster",
    public_base_url: "https://cpa.example.com/",
    provider_name: "CPA Provider",
    api_key_env: "CPA_API_KEY",
    default_model: "gpt-5.6-sol"
  }
};

describe("buildClientConfig", () => {
  it("builds the Codex Responses provider with the configured public URL and current Key", () => {
    const result = buildClientConfig({ ...input, mode: "codex" });
    expect(result.value).toContain('base_url = "https://cpa.example.com/v1"');
    expect(result.value).toContain('experimental_bearer_token = "secret-api-key"');
    expect(result.value).toContain('name = "CPA Provider · alice"');
    expect(result.sections?.map((section) => section.title)).toEqual(["Codex 配置内容", "迁移旧会话"]);
    expect(result.sections?.[1].value).toContain("OAuth");
  });

  it("keeps Claude Code isolated to its launcher and secret env file", () => {
    const result = buildClientConfig({ ...input, mode: "claude" });
    expect(result.sections).toHaveLength(5);
    expect(result.sections?.[1].value).toBe("CPA_API_KEY='secret-api-key'\n");
    expect(result.value).toContain("export ANTHROPIC_BASE_URL='https://cpa.example.com'");
    expect(result.value).toContain('command claude --dangerously-skip-permissions --verbose --effort xhigh "$@"');
  });

  it("builds a CC Switch deep link and falls back from unsafe public URLs", () => {
    const result = buildClientConfig({
      ...input,
      mode: "ccswitch",
      siteConfig: { ...input.siteConfig, public_base_url: "https://user:password@example.com" }
    });
    expect(result.externalLink).toContain("ccswitch://v1/import?");
    expect(result.externalLink).toContain("endpoint=http%3A%2F%2F127.0.0.1%3A5194%2Fv1");
    expect(result.externalLink).not.toContain("secret-api-key");
    expect(result.externalLink).toContain("apiKey=PASTE_API_KEY_AFTER_IMPORT");
    expect(result.value).toContain('experimental_bearer_token = "secret-api-key"');
    expect(result.title).toBe("完成 CC Switch 配置");
    expect(result.copyLabel).toBe("复制并导入");
    expect(result.notice).toBeUndefined();
  });
});

describe("copyAndImportConfig", () => {
  it("copies the complete config before opening the CC Switch link", async () => {
    const events: string[] = [];
    const result = await copyAndImportConfig({
      value: "full-secret-config",
      externalLink: "ccswitch://v1/import?resource=provider",
      writeText: vi.fn(async (value) => { events.push(`copy:${value}`); }),
      openLink: vi.fn((link) => { events.push(`open:${link}`); })
    });

    expect(events).toEqual([
      "copy:full-secret-config",
      "open:ccswitch://v1/import?resource=provider"
    ]);
    expect(result.status).toBe("opened");
  });

  it("does not open CC Switch when clipboard permission is denied", async () => {
    const openLink = vi.fn();
    const result = await copyAndImportConfig({
      value: "full-secret-config",
      externalLink: "ccswitch://v1/import?resource=provider",
      writeText: vi.fn(async () => { throw new DOMException("denied", "NotAllowedError"); }),
      openLink
    });

    expect(openLink).not.toHaveBeenCalled();
    expect(result).toEqual({
      status: "copy_failed",
      message: "复制失败：剪贴板权限被拒绝，未打开 CC Switch"
    });
  });

  it("reports an unavailable CC Switch after the clipboard copy succeeds", async () => {
    const result = await copyAndImportConfig({
      value: "full-secret-config",
      externalLink: "ccswitch://v1/import?resource=provider",
      writeText: vi.fn(async () => undefined),
      openLink: vi.fn(() => { throw new Error("no handler"); })
    });

    expect(result).toEqual({
      status: "open_failed",
      message: "配置已复制，但无法打开 CC Switch，请确认已安装"
    });
  });
});
