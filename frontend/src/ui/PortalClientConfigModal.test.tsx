import { describe, expect, it } from "vitest";

import { defaultPublicSiteConfiguration } from "../api/public-site";
import { buildClientConfig } from "./PortalClientConfigModal";

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
    expect(result.notice).toContain("不会把真实 API Key 放入 URL");
  });
});
