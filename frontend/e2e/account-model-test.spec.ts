import { expect, test } from "@playwright/test";

for (const width of [1920, 1024, 390]) {
  for (const theme of ["dark", "light"] as const) {
    test(`账号时间筛选与模型通信 ${width} ${theme}`, async ({ page }, testInfo) => {
      await page.setViewportSize({ width, height: width === 390 ? 844 : 1080 });
      await page.addInitScript(value => localStorage.setItem("cpa-ui-theme", value), theme);
      let posts = 0;
      let account = "";
      await page.route("**/admin/api/accounts/models?*", async route => {
        account = new URL(route.request().url()).searchParams.get("account")!;
        await route.fulfill({ json: { account, models: ["gpt-5.5", "gpt-6-astra"] } });
      });
      await page.route("**/admin/api/accounts/model-test", async route => {
        expect(route.request().postDataJSON()).toEqual({ account, model: "gpt-6-astra" });
        expect(route.request().headers()["x-csrf-token"]).toBeTruthy();
        posts++;
        await route.fulfill({ json: { account, model: "gpt-6-astra", success: posts > 1, elapsed_ms: 2300,
          checked_at: 1788840000, upstream_status: posts > 1 ? 200 : 401,
          code: posts > 1 ? "model_test_passed" : "model_auth_failed",
          message: posts > 1 ? "模型已完成生成并返回文本" : "账号授权失败，请检查 OAuth 状态" } });
      });
      await page.goto("/admin/accounts");
      await page.locator('input[type="password"]').fill("visual-preview");
      await page.getByRole("button", { name: "验证并进入" }).click();
      const range = page.getByRole("group", { name: "账号用量时间范围" });
      await expect(range.getByRole("button", { name: "今日", exact: true })).toHaveAttribute("aria-pressed", "true");
      expect(await range.evaluate(el => el.closest(".account-management-toolbar")?.firstElementChild === el.closest(".user-time-filter"))).toBe(true);
      await expect(page.getByLabel("账号用量时间边界")).toBeVisible();
      expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width);
      const firstRow = page.getByRole("row", { name: /^展开 / }).first();
      await firstRow.scrollIntoViewIfNeeded();
      await expect(firstRow).toBeInViewport();
      await page.screenshot({ path: testInfo.outputPath(`accounts-time-${width}-${theme}.png`) });
      await firstRow.click();
      await page.getByRole("button", { name: "模型通信测试", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: "模型通信测试" });
      await expect(dialog.getByRole("button", { name: "开始测试" })).toBeEnabled();
      expect(posts).toBe(0);
      await dialog.getByRole("button", { name: "开始测试" }).click();
      await expect(dialog.getByText("模型通信失败", { exact: true })).toBeVisible();
      await expect(dialog.getByText("401", { exact: true })).toBeVisible();
      await dialog.getByRole("button", { name: "重新测试" }).click();
      await expect(dialog.getByText("模型通信正常", { exact: true })).toBeVisible();
      await expect(dialog.getByText("2.30 秒", { exact: true })).toBeVisible();
      expect(await dialog.evaluate(el => { const r = el.getBoundingClientRect(); return r.left >= 0 && r.right <= innerWidth; })).toBe(true);
      await page.screenshot({ path: testInfo.outputPath(`model-test-${width}-${theme}.png`) });
      await dialog.locator(".ant-modal-footer").getByRole("button", { name: "关闭", exact: true }).click();
      await expect(dialog).toBeHidden();
      expect(posts).toBe(2);
    });
  }
}
