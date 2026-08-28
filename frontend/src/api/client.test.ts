import { beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError, subscribeUnauthorized } from "./client";
import { login } from "./session";
import { createTeam } from "./teams";
import { loginPortal } from "./portal";

describe("fine-grained Admin API client", () => {
  beforeEach(() => {
    localStorage.clear();
  });

  it("exchanges the management key without persisting it", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      authenticated: true,
      csrf_token: "csrf-test"
    }), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await expect(login("test-management-key")).resolves.toMatchObject({ authenticated: true });
    expect(fetchMock).toHaveBeenCalledOnce();
    const [, request] = fetchMock.mock.calls[0];
    expect(request.headers["X-Management-Key"]).toBe("test-management-key");
    expect(localStorage.length).toBe(0);
  });

  it("adds CSRF only to the requested team mutation", async () => {
    const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
      message: "团队已创建",
      team: { id: "team_1", name: "Platform" }
    }), { status: 201, headers: { "Content-Type": "application/json" } }));
    vi.stubGlobal("fetch", fetchMock);

    await createTeam({ name: "Platform", description: "Core" }, "csrf-test");
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/teams", expect.objectContaining({
      method: "POST",
      credentials: "same-origin",
      headers: expect.objectContaining({ "X-CSRF-Token": "csrf-test" })
    }));
  });

  it("preserves the API error code and status", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: "team_name_conflict", message: "团队名称已存在" }
    }), { status: 409, headers: { "Content-Type": "application/json" } })));

    await expect(createTeam({ name: "Platform", description: "" }, "csrf-test"))
      .rejects.toMatchObject({ status: 409, code: "team_name_conflict" } satisfies Partial<ApiError>);
  });

  it("parses a bounded Retry-After value for portal login rate limits", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: "rate_limited", message: "登录尝试过于频繁" }
    }), {
      status: 429,
      headers: { "Content-Type": "application/json", "Retry-After": "7" }
    })));

    await expect(loginPortal("alice@example.com", "password"))
      .rejects.toMatchObject({ status: 429, code: "rate_limited", retryAfterSeconds: 7 } satisfies Partial<ApiError>);
  });

  it("notifies the Admin shell when a fine-grained request loses its session", async () => {
    const listener = vi.fn();
    const unsubscribe = subscribeUnauthorized(listener);
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({
      error: { code: "unauthorized", message: "管理会话已失效" }
    }), { status: 401, headers: { "Content-Type": "application/json" } })));

    await expect(createTeam({ name: "Platform", description: "" }, "expired-csrf"))
      .rejects.toMatchObject({ status: 401, code: "unauthorized" });
    expect(listener).toHaveBeenCalledWith({ path: "/admin/api/teams", scope: "admin" });
    unsubscribe();
  });
});
