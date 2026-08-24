import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { RuntimePage } from "./RuntimePage";

const serviceCatalog = {
  services: [
    {
      service: "cliproxy-alpha",
      container_id: "aaaaaaaaaaaa",
      name: "cpa-alpha",
      image: "registry.example.com/cpa:v2",
      state: "running",
      status: "Up 10 minutes"
    },
    {
      service: "edge",
      container_id: "eeeeeeeeeeee",
      name: "cpa-edge",
      image: "registry.example.com/edge:v2",
      state: "running",
      status: "Up 10 minutes"
    }
  ]
};

const imageStatus = {
  target_image: "registry.example.com/cpa:v2",
  update_channel: "registry.example.com/cpa:v2",
  candidate: { version: "v2.0.0-rc1", commit: "abcdef1" },
  applied: { version: "v1.9.0", commit: "1234567" },
  local_image: {
    available: true,
    id: "sha256:aaaaaaaa",
    short_id: "aaaaaaaaaaaa",
    created: "2026-08-21T00:00:00Z",
    repo_digests: [],
    version: "v2.0.0-rc1",
    commit: "abcdef1",
    built_at: "2026-08-21T00:00:00Z",
    resolved_ref: "registry.example.com/cpa@sha256:aaaaaaaa"
  },
  accounts: [{
    account: "alpha",
    service: "cliproxy-alpha",
    enabled: true,
    container_exists: true,
    running: true,
    state: "running",
    image_ref: "registry.example.com/cpa:v1",
    image_id: "sha256:bbbbbbbb",
    image_short_id: "bbbbbbbbbbbb",
    version: "v1.9.0",
    using_target: false,
    rollback_available: true
  }],
  running_count: 1,
  current_count: 0,
  outdated_count: 1,
  cached: false
};

describe("RuntimePage", () => {
  it("loads fine-grained service and job APIs and fetches logs only after opening the drawer", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const path = String(input);
      if (path === "/admin/api/runtime/services") return jsonResponse(serviceCatalog);
      if (path === "/admin/api/runtime/jobs?limit=30") return jsonResponse({ jobs: [] });
      if (path === "/admin/api/images/cliproxy") return jsonResponse(imageStatus);
      if (path === "/admin/api/runtime/logs?target=edge") {
        return jsonResponse({ target: "edge", output: "Bearer [REDACTED]", truncated: false });
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderRuntimePage();

    expect(await screen.findByText("cpa-alpha")).toBeInTheDocument();
    expect(await screen.findByText("v2.0.0-rc1")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledTimes(3);
    const edgeRow = screen.getByText("cpa-edge").closest("tr");
    expect(edgeRow).not.toBeNull();
    expect(edgeRow).not.toHaveTextContent("停止");
    expect(edgeRow).not.toHaveTextContent("重启");
    await user.click(screen.getAllByRole("button", { name: "日志" })[1]);
    expect(await screen.findByText("Bearer [REDACTED]")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(
      "/admin/api/runtime/logs?target=edge",
      expect.objectContaining({ credentials: "same-origin" })
    );
  });

  it("submits an exactly confirmed restart through the bounded runtime job API", async () => {
    let submitted = false;
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/runtime/services") return jsonResponse(serviceCatalog);
      if (path === "/admin/api/images/cliproxy") return jsonResponse(imageStatus);
      if (path === "/admin/api/runtime/jobs?limit=30") return jsonResponse({ jobs: submitted ? [
        { id: "job-1", name: "重启服务", action: "restart", target: "alpha", status: "queued", created_at: 100 }
      ] : [] });
      if (path === "/admin/api/runtime/jobs" && init?.method === "POST") {
        submitted = true;
        return jsonResponse({
          message: "任务已提交",
          reused: false,
          job: { id: "job-1", name: "重启服务", action: "restart", target: "alpha", status: "queued", created_at: 100 }
        }, 202);
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderRuntimePage();

    const accountRow = (await screen.findByText("cpa-alpha")).closest("tr");
    expect(accountRow).not.toBeNull();
    await user.click(withinRowButton(accountRow!, "重启"));
    expect(screen.getByRole("dialog", { name: "重启 cliproxy-alpha？" })).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认提交" }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([path, init]) => String(path) === "/admin/api/runtime/jobs" && init?.method === "POST")).toBe(true));
    const mutation = fetchMock.mock.calls.find(([path, init]) => String(path) === "/admin/api/runtime/jobs" && init?.method === "POST")!;
    expect(mutation[0]).toBe("/admin/api/runtime/jobs");
    expect(mutation[1]).toMatchObject({
      method: "POST",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    });
    expect(JSON.parse(String(mutation[1]?.body))).toEqual({
      action: "restart",
      target: "alpha",
      confirm: "restart:alpha"
    });
  });

  it("requires a fresh routed-user impact read before enabling a stop", async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = String(input);
      if (path === "/admin/api/runtime/services") return jsonResponse(serviceCatalog);
      if (path === "/admin/api/runtime/jobs?limit=30") return jsonResponse({ jobs: [] });
      if (path === "/admin/api/images/cliproxy") return jsonResponse(imageStatus);
      if (path === "/admin/api/operations/impact?action=stop&target=alpha") {
        return jsonResponse({ action: "stop", target: "alpha", target_type: "account", routed_users: 17 });
      }
      if (path === "/admin/api/runtime/jobs" && init?.method === "POST") {
        return jsonResponse({
          message: "任务已提交",
          reused: false,
          job: { id: "job-stop", name: "停止服务", action: "stop", target: "alpha", status: "queued", created_at: 100 }
        }, 202);
      }
      throw new Error(`unexpected request: ${path}`);
    });
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();
    renderRuntimePage();

    const accountRow = (await screen.findByText("cpa-alpha")).closest("tr");
    expect(accountRow).not.toBeNull();
    await user.click(withinRowButton(accountRow!, "停止"));
    expect(await screen.findByText("将影响 17 个已路由用户")).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "确认提交" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      "/admin/api/operations/impact?action=stop&target=alpha",
      expect.objectContaining({ credentials: "same-origin", cache: "no-store" })
    ));
    const request = fetchMock.mock.calls.find(([path, init]) => String(path) === "/admin/api/runtime/jobs" && init?.method === "POST");
    expect(JSON.parse(String(request?.[1]?.body))).toEqual({
      action: "stop",
      target: "alpha",
      confirm: "stop:alpha"
    });
  });
});

function renderRuntimePage() {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: 0 }, mutations: { retry: false } }
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <RuntimePage csrfToken="csrf-test" />
    </QueryClientProvider>
  );
}

function withinRowButton(row: HTMLElement, name: string) {
  const button = Array.from(row.querySelectorAll("button")).find((candidate) => candidate.textContent?.trim() === name);
  if (!button) throw new Error(`button ${name} was not found`);
  return button;
}

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}
