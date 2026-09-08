import { expect, test } from "@playwright/test";

for (const width of [1920, 1024, 390]) {
  for (const theme of ["dark", "light"] as const) {
    test(`通知调度状态 ${width} ${theme}`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1080 });
      await page.addInitScript(value => localStorage.setItem("cpa-ui-theme", value), theme);
      let worker = "heartbeat_lost";
      let statusReads = 0;
      await page.route("**/admin/api/settings/notifications", async route => {
        expect(route.request().method()).toBe("GET");
        statusReads++;
        await route.fulfill({ json: {
          notifications: {
            webhook_configured: true, webhook_url: "", worker_status: worker,
            webhook_display_url: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=••••••demo",
            heartbeat_at: 1_788_843_600, last_success_at: 1_788_847_200,
            next_schedule_at: 1_788_861_600, last_error: ""
          },
          values: { enabled: true, timezone: "Asia/Shanghai", daily_times: "09:00,14:00,18:00",
            schedule_grace_minutes: 15, quota_alert_enabled: true, weekly_threshold_percent: 90 }
        } });
      });
      await page.route("**/admin/api/settings/configuration", async route => {
        const response = await route.fetch();
        const catalog = await response.json();
        for (const group of catalog.groups) {
          for (const field of group.fields) {
            if (field.key === "notification.enabled") field.value = true;
          }
        }
        await route.fulfill({ json: catalog });
      });
      await page.clock.install();
      await page.goto("/admin/configuration?section=notifications");
      await page.locator('input[type="password"]').fill("visual-preview");
      await page.getByRole("button", { name: "验证并进入" }).click();
      const status = page.getByRole("region", { name: "通知运行状态" });
      await expect(status.getByText("心跳中断")).toBeVisible();
      await expect(status.getByText("已启用", { exact: true })).toBeVisible();
      await expect(status.getByText("下次发送").locator("..")).toContainText("—");
      await status.scrollIntoViewIfNeeded();
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
      expect(await status.evaluate(el => { const r = el.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth; })).toBe(true);
      await page.screenshot({ path: testInfo.outputPath(`notification-lost-${width}-${theme}.png`) });
      worker = "running";
      const previousReads = statusReads;
      await page.clock.fastForward(31_000);
      await expect(status.getByText("心跳正常")).toBeVisible();
      expect(statusReads).toBeGreaterThan(previousReads);
      await expect(status.getByText("下次发送").locator("..")).not.toContainText("—");
      await page.screenshot({ path: testInfo.outputPath(`notification-live-${width}-${theme}.png`) });
    });
  }
}
