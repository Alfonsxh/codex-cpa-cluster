import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

import type { ConfigurationCatalog } from "../api/configuration";
import type { OnboardingStatus } from "../api/onboarding";
import { OnboardingPage } from "./OnboardingPage";

describe("OnboardingPage", () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });
  it("saves required email domains without persisting any setup value in browser storage", async () => {
    let status = freshStatus();
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/onboarding") return jsonResponse(status);
      if (path === "/admin/api/settings/configuration" && !init?.method) return jsonResponse(configurationCatalog());
      if (path === "/admin/api/settings/configuration" && init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { values: Record<string, unknown> };
        expect(body).toEqual({ confirm: "save", values: { "identity.allowed_email_domains": "example.com, example.org" } });
        status = completeStep(status, "email_domains");
        return jsonResponse({ message: "已保存 1 项配置", changed: ["identity.allowed_email_domains"], applied: ["live"], pending_deployment: false });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderOnboarding("/setup");

    await user.type(await screen.findByLabelText("允许的邮箱域名"), "example.com, example.org");
    await user.click(screen.getByRole("button", { name: "保存并检查" }));

    expect(await screen.findByText("已保存 1 项配置，完成状态已重新检查")).toBeInTheDocument();
    await waitFor(() => expect(screen.getByText("此步骤已完成")).toBeInTheDocument());
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
    const mutation = fetchMock.mock.calls.find(([, init]) => init?.method === "POST");
    expect(mutation?.[1]).toMatchObject({ headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" }) });
  });

  it("lets operators skip and restore recommendations while required progress remains separate", async () => {
    let status = { ...freshStatus(), required: { complete: 2, total: 5 } };
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/onboarding" && !init?.method) return jsonResponse(status);
      if (path === "/admin/api/settings/configuration" && !init?.method) return jsonResponse(configurationCatalog());
      if (path === "/admin/api/onboarding/preferences" && init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { skipped_recommended: string[] };
        status = withSkipped(status, body.skipped_recommended);
        return jsonResponse(status);
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderOnboarding("/setup?step=public_base_url");

    expect((await screen.findByRole("progressbar")).getAttribute("aria-valuenow")).toBe("40");
    expect(screen.queryByRole("button", { name: "全部推荐项稍后再说" })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "稍后继续" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "暂时跳过此项" }));
    expect(await screen.findByText("已跳过")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "40");

    await user.click(screen.getByRole("button", { name: "恢复此推荐项" }));
    expect(await screen.findByText("待设置")).toBeInTheDocument();
    const requests = fetchMock.mock.calls.filter(([path]) => String(path) === "/admin/api/onboarding/preferences");
    expect(JSON.parse(String(requests[0][1]?.body))).toMatchObject({
      confirm: "save",
      skipped_recommended: ["public_base_url"]
    });
    expect(JSON.parse(String(requests[1][1]?.body))).toMatchObject({ skipped_recommended: [] });
  });

  it("configures every remaining recommendation inline without leaving the setup workspace", async () => {
    let status = freshStatus();
    const configurationBodies: Array<Record<string, unknown>> = [];
    let webhookBody: Record<string, unknown> | undefined;
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/onboarding" && !init?.method) return jsonResponse(status);
      if (path === "/admin/api/settings/configuration" && !init?.method) return jsonResponse(configurationCatalog());
      if (path === "/admin/api/settings/configuration" && init?.method === "POST") {
        const body = JSON.parse(String(init.body)) as { values: Record<string, unknown> };
        configurationBodies.push(body.values);
        const stepID = configurationStepForValues(body.values);
        status = completeRecommendedStep(status, stepID);
        return jsonResponse({ message: "已保存配置", changed: Object.keys(body.values), applied: ["live"], pending_deployment: false });
      }
      if (path === "/admin/api/settings/notification-webhook" && init?.method === "POST") {
        webhookBody = JSON.parse(String(init.body)) as Record<string, unknown>;
        status = completeRecommendedStep(status, "notifications");
        return jsonResponse({ message: "企业微信 Webhook 已保存", notifications: { webhook_configured: true } });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    const webhookURL = ["https://qyapi.weixin.qq.com", "/cgi-bin/webhook/send", "?key=", "unit-test"].join("");
    renderOnboarding("/setup?step=quota_timezone");

    const timezone = await screen.findByLabelText("用户额度时区");
    await user.clear(timezone);
    await user.type(timezone, "Asia/Shanghai");
    await user.click(screen.getByRole("button", { name: "保存时区" }));
    await waitFor(() => expect(screen.getByText("此步骤已完成")).toBeInTheDocument());

    await user.click(within(screen.getByRole("navigation", { name: "推荐设置" })).getByRole("button", { name: /默认额度/ }));
    await user.type(await screen.findByLabelText("新用户默认周额度"), "20000000");
    await user.click(screen.getByRole("button", { name: "保存默认额度" }));
    await waitFor(() => expect(screen.getByText("此步骤已完成")).toBeInTheDocument());

    await user.click(within(screen.getByRole("navigation", { name: "推荐设置" })).getByRole("button", { name: /^通知/ }));
    await user.type(await screen.findByLabelText("企业微信群 Webhook"), webhookURL);
    await user.click(screen.getByRole("button", { name: "保存 Webhook" }));
    await waitFor(() => expect(screen.getByText("此步骤已完成")).toBeInTheDocument());

    await user.click(within(screen.getByRole("navigation", { name: "推荐设置" })).getByRole("button", { name: /^品牌/ }));
    const productName = await screen.findByLabelText("产品名称");
    await user.clear(productName);
    await user.type(productName, "QData CPA");
    const environmentLabel = screen.getByLabelText("环境说明");
    await user.clear(environmentLabel);
    await user.type(environmentLabel, "研发团队专用");
    await user.click(screen.getByRole("button", { name: "保存品牌信息" }));
    await waitFor(() => expect(screen.getByText("此步骤已完成")).toBeInTheDocument());

    await user.click(within(screen.getByRole("navigation", { name: "推荐设置" })).getByRole("button", { name: /上游代理/ }));
    await user.type(await screen.findByLabelText("默认上游代理 URL"), "socks5://user:password@proxy.example.com:1080");
    expect(screen.queryByRole("button", { name: "前往设置" })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "保存并启用代理" }));
    await waitFor(() => expect(screen.getByText("此步骤已完成")).toBeInTheDocument());

    expect(configurationBodies).toEqual([
      { "user_quota.timezone": "Asia/Shanghai" },
      { "user_quota.default_weekly_tokens": 20_000_000 },
      {
        "branding.product_name": "QData CPA",
        "branding.short_name": "Codex CPA",
        "branding.environment_label": "研发团队专用"
      },
      {
        "cpa.proxy_enabled": true,
        "cpa.proxy_url": "socks5://user:password@proxy.example.com:1080"
      }
    ]);
    expect(webhookBody).toEqual({
      confirm: "save",
      webhook_url: webhookURL
    });
    expect(screen.getByTestId("location")).toHaveTextContent("/setup?step=proxy");
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("keeps the Admin recoverable when status loading fails", async () => {
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request) => {
      if (String(input) === "/admin/api/settings/configuration") return jsonResponse(configurationCatalog());
      return new Response(JSON.stringify({
        error: { code: "status_unavailable", message: "状态检查暂不可用" }
      }), { status: 503, headers: { "Content-Type": "application/json" } });
    }));
    const user = userEvent.setup();
    renderOnboarding("/setup");

    expect(await screen.findByText("首次设置状态暂时不可用")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "进入运行总览" }));
    expect(screen.getByTestId("location")).toHaveTextContent("/overview");
  });
});

function renderOnboarding(entry: string) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter initialEntries={[entry]}>
        <OnboardingPage csrfToken="csrf-test" />
        <LocationProbe />
      </MemoryRouter>
    </QueryClientProvider>
  );
}

function LocationProbe() {
  const location = useLocation();
  return <output data-testid="location">{location.pathname}{location.search}</output>;
}

function freshStatus(): OnboardingStatus {
  const required = [
    ["email_domains", "组织与访问", "/configuration?group=品牌与身份&key=identity.allowed_email_domains"],
    ["initial_password", "用户初始密码", "/configuration?section=access"],
    ["first_account", "创建第一个 CPA", "/accounts?create=1"],
    ["account_authorization", "OAuth 与运行状态", "/accounts?auth=pending"],
    ["first_user", "创建第一个用户", "/users?create=1"]
  ] as const;
  const recommended = [
    ["public_base_url", "公开访问地址"],
    ["quota_timezone", "用户额度时区"],
    ["weekly_quota", "默认周额度"],
    ["notifications", "企业微信通知"],
    ["branding", "品牌信息"],
    ["proxy", "默认上游代理"]
  ] as const;
  return {
    version: 1,
    generated_at: 1_800_000_000,
    required_complete: false,
    required: { complete: 0, total: 5 },
    recommended: { complete: 0, skipped: 0, total: 6 },
    skipped_recommended: [],
    steps: [
      ...required.map(([id, title, action_path], index) => ({
        id,
        kind: "required" as const,
        status: index >= 3 ? "blocked" as const : "incomplete" as const,
        title,
        description: `${title}说明`,
        action_path,
        blockers: index >= 3 ? ["先完成前置步骤"] : []
      })),
      ...recommended.map(([id, title]) => ({
        id,
        kind: "recommended" as const,
        status: "incomplete" as const,
        title,
        description: `${title}说明`,
        action_path: "/configuration",
        blockers: []
      }))
    ]
  };
}

function completeStep(status: OnboardingStatus, stepID: string): OnboardingStatus {
  return {
    ...status,
    generated_at: status.generated_at + 1,
    required: { ...status.required, complete: status.required.complete + 1 },
    steps: status.steps.map((step) => step.id === stepID ? { ...step, status: "complete" } : step)
  };
}

function withSkipped(status: OnboardingStatus, skipped: string[]): OnboardingStatus {
  const skippedSet = new Set(skipped);
  return {
    ...status,
    generated_at: status.generated_at + 1,
    skipped_recommended: skipped,
    recommended: { ...status.recommended, skipped: skipped.length },
    steps: status.steps.map((step) => step.kind === "recommended"
      ? { ...step, status: skippedSet.has(step.id) ? "skipped" : "incomplete" }
      : step)
  };
}

function completeRecommendedStep(status: OnboardingStatus, stepID: string): OnboardingStatus {
  return {
    ...status,
    generated_at: status.generated_at + 1,
    recommended: { ...status.recommended, complete: status.recommended.complete + 1 },
    steps: status.steps.map((step) => step.id === stepID ? { ...step, status: "complete" } : step)
  };
}

function configurationStepForValues(values: Record<string, unknown>) {
  const keys = Object.keys(values);
  if (keys.includes("user_quota.timezone")) return "quota_timezone";
  if (keys.includes("user_quota.default_weekly_tokens")) return "weekly_quota";
  if (keys.includes("branding.product_name")) return "branding";
  if (keys.includes("cpa.proxy_enabled")) return "proxy";
  if (keys.includes("branding.public_base_url")) return "public_base_url";
  if (keys.includes("identity.allowed_email_domains")) return "email_domains";
  throw new Error(`unmapped configuration values: ${keys.join(",")}`);
}

function configurationCatalog(): ConfigurationCatalog {
  return {
    version: 1,
    generated_at: 1_800_000_000,
    field_count: 8,
    groups: [
      {
        name: "品牌与身份",
        description: "品牌配置",
        fields: [
          configurationField("branding.product_name", "产品名称", "text", "Codex CPA Cluster", "Codex CPA Cluster"),
          configurationField("branding.short_name", "产品简称", "text", "Codex CPA", "Codex CPA"),
          configurationField("branding.environment_label", "环境说明", "optional_text", "Self-hosted service", "Self-hosted service"),
          configurationField("branding.public_base_url", "公开访问地址", "base_url", "", "")
        ]
      },
      {
        name: "用户额度",
        description: "额度配置",
        fields: [
          configurationField("user_quota.timezone", "用户额度时区", "timezone", "UTC", "UTC"),
          configurationField("user_quota.default_weekly_tokens", "默认周额度", "nullable_integer", null, null)
        ]
      },
      {
        name: "CPA 请求",
        description: "CPA 配置",
        fields: [
          configurationField("cpa.proxy_enabled", "启用默认上游代理", "boolean", false, false),
          { ...configurationField("cpa.proxy_url", "默认上游代理 URL", "proxy_url_secret", "", ""), configured: false }
        ]
      }
    ]
  };
}

function configurationField(
  key: string,
  label: string,
  type: "text" | "optional_text" | "base_url" | "timezone" | "nullable_integer" | "boolean" | "proxy_url_secret",
  value: string | number | boolean | null,
  defaultValue: string | number | boolean | null
) {
  return {
    key,
    label,
    description: `${label}说明`,
    type,
    value,
    default: defaultValue,
    apply_mode: "live" as const,
    editable: true as const
  };
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
}
