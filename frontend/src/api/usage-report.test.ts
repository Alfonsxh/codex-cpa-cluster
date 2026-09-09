import { afterEach, describe, expect, it, vi } from "vitest";
import { exportWeeklyUsage } from "./overview";
import { subscribeUnauthorized } from "./client";

afterEach(() => vi.unstubAllGlobals());

describe("weekly usage binary export", () => {
  it("downloads an authenticated XLSX with the server filename and no overview filters", async () => {
    const filename = "CCPA_Token周报_2026-08-31_2026-09-06.xlsx";
    const fetchMock = vi.fn().mockResolvedValue(new Response("workbook-bytes", { headers: {
      "Content-Type": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
      "Content-Disposition": `attachment; filename*=utf-8''${encodeURIComponent(filename)}`
    } }));
    vi.stubGlobal("fetch", fetchMock);
    const result = await exportWeeklyUsage("2026-08-31");
    expect(result.filename).toBe(filename);
    expect(result.blob.size).toBe(14);
    expect(fetchMock).toHaveBeenCalledWith("/admin/api/overview/usage-report.xlsx?week_start=2026-08-31", expect.objectContaining({ credentials: "same-origin" }));
  });

  it("uses normal session expiry handling for failed downloads", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { code: "unauthorized", message: "会话失效" } }), { status: 401 })));
    const listener = vi.fn();
    const unsubscribe = subscribeUnauthorized(listener);
    try {
      await expect(exportWeeklyUsage("2026-08-31")).rejects.toMatchObject({ status: 401, message: "会话失效" });
      expect(listener).toHaveBeenCalledWith(expect.objectContaining({ scope: "admin" }));
    } finally { unsubscribe(); }
  });

  it("does not save proxy HTML as a workbook and preserves busy errors", async () => {
    vi.stubGlobal("fetch", vi.fn().mockResolvedValueOnce(new Response("<html>login</html>", { headers: { "Content-Type": "text/html" } }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: { code: "report_busy", message: "正在生成其他周报" } }), { status: 429, headers: { "Retry-After": "5" } })));
    await expect(exportWeeklyUsage("2026-08-31")).rejects.toThrow("未收到有效的周报文件");
    await expect(exportWeeklyUsage("2026-08-31")).rejects.toMatchObject({ code: "report_busy", retryAfterSeconds: 5 });
  });
});
