import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, useLocation } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";

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

    expect(await screen.findByText("设置已保存，完成状态已重新检查")).toBeInTheDocument();
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
      if (path === "/admin/api/onboarding/preferences" && init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { deferred: boolean; skipped_recommended: string[] };
        status = withSkipped(status, body.skipped_recommended, body.deferred);
        return jsonResponse(status);
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderOnboarding("/setup?step=public_base_url");

    expect((await screen.findByRole("progressbar")).getAttribute("aria-valuenow")).toBe("40");
    await user.click(screen.getByRole("button", { name: "暂时跳过此项" }));
    expect(await screen.findByText("已跳过")).toBeInTheDocument();
    expect(screen.getByRole("progressbar")).toHaveAttribute("aria-valuenow", "40");

    await user.click(screen.getByRole("button", { name: "恢复此推荐项" }));
    expect(await screen.findByText("待设置")).toBeInTheDocument();
    const requests = fetchMock.mock.calls.filter(([path]) => String(path) === "/admin/api/onboarding/preferences");
    expect(JSON.parse(String(requests[0][1]?.body))).toMatchObject({
      confirm: "save",
      deferred: false,
      skipped_recommended: ["public_base_url"]
    });
    expect(JSON.parse(String(requests[1][1]?.body))).toMatchObject({ skipped_recommended: [] });
  });

  it("saves a deferred marker and returns to Overview when the operator continues later", async () => {
    let status = freshStatus();
    vi.stubGlobal("fetch", vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/onboarding" && !init?.method) return jsonResponse(status);
      if (path === "/admin/api/onboarding/preferences" && init?.method === "PUT") {
        const body = JSON.parse(String(init.body)) as { deferred: boolean; skipped_recommended: string[] };
        status = withSkipped(status, body.skipped_recommended, body.deferred);
        return jsonResponse(status);
      }
      throw new Error(`unexpected request: ${path}`);
    }));
    const user = userEvent.setup();
    renderOnboarding("/setup");

    const continueLater = await screen.findByText("稍后继续");
    await user.click(continueLater.closest("button") ?? continueLater);
    await waitFor(() => expect(screen.getByTestId("location")).toHaveTextContent("/overview"));
    expect(localStorage.length).toBe(0);
  });

  it("keeps the Admin recoverable when status loading fails", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: "status_unavailable", message: "状态检查暂不可用" }
    }), { status: 503, headers: { "Content-Type": "application/json" } })));
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
    deferred: false,
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

function withSkipped(status: OnboardingStatus, skipped: string[], deferred: boolean): OnboardingStatus {
  const skippedSet = new Set(skipped);
  return {
    ...status,
    generated_at: status.generated_at + 1,
    deferred,
    skipped_recommended: skipped,
    recommended: { ...status.recommended, skipped: skipped.length },
    steps: status.steps.map((step) => step.kind === "recommended"
      ? { ...step, status: skippedSet.has(step.id) ? "skipped" : "incomplete" }
      : step)
  };
}

function jsonResponse(payload: unknown) {
  return new Response(JSON.stringify(payload), { status: 200, headers: { "Content-Type": "application/json" } });
}
