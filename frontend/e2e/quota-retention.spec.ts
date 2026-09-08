import { expect, test } from "@playwright/test";

const retentionKey = "user_quota.preserve_personal_weekly_on_new_week";

for (const viewport of [
  { name: "desktop", width: 1440, height: 900 },
  { name: "narrow", width: 1024, height: 768 },
  { name: "mobile", width: 390, height: 844 }
]) {
  for (const theme of ["light", "dark"] as const) {
    test(`全局额度保留开关 ${viewport.name} ${theme}`, async ({ page }, testInfo) => {
      let preserve = false;
      const saved: boolean[] = [];
      await page.setViewportSize(viewport);
      await page.addInitScript((value) => localStorage.setItem("cpa-ui-theme", value), theme);
      await page.route("**/admin/api/settings/configuration", async (route) => {
        if (route.request().method() === "POST") {
          const body = route.request().postDataJSON();
          expect(Object.keys(body.values)).toEqual([retentionKey]);
          preserve = body.values[retentionKey];
          saved.push(preserve);
          await route.fulfill({ json: { message: "已保存 1 项配置", changed: [retentionKey], applied: ["quota"], pending_deployment: false } });
          return;
        }
        const response = await route.fetch();
        const catalog = await response.json();
        for (const group of catalog.groups) {
          for (const field of group.fields) if (field.key === retentionKey) field.value = preserve;
        }
        await route.fulfill({ response, json: catalog });
      });
      await page.goto("/admin/configuration?section=quota");
      await page.locator('input[type="password"]').fill("visual-preview");
      await page.getByRole("button", { name: "验证并进入" }).click();
      const retention = page.getByRole("checkbox", { name: /保留修改后的额度/ });
      await expect(retention).not.toBeChecked();
      await expect(page.getByText("新周恢复默认个人额度", { exact: true })).toHaveCount(0);
      const row = page.locator(`[data-configuration-field="${retentionKey}"]`);
      await expect(row).toContainText("对所有用户生效");
      await expect(row).toContainText("下周一 00:00");
      await expect(row.locator("xpath=preceding-sibling::*[1]")).toHaveAttribute("data-configuration-field", "user_quota.default_weekly_tokens");
      await row.scrollIntoViewIfNeeded();
      await page.screenshot({ path: testInfo.outputPath(`quota-retention-${viewport.name}-${theme}.png`), animations: "disabled" });
      for (const enabled of [true, false]) {
        await retention.setChecked(enabled);
        await page.getByRole("button", { name: "保存配置", exact: true }).click();
        await page.getByRole("button", { name: "保存并应用", exact: true }).click();
        await expect(page.locator(".configuration-actions [role=status]")).toHaveText("未修改");
        await page.reload();
        await expect(retention).toBeChecked({ checked: enabled });
      }
      expect(saved).toEqual([true, false]);
    });
  }
}
