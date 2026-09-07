import { expect, test } from "@playwright/test";

test("默认品牌资源随主题切换，自定义 Logo 保持原样", async ({ page }) => {
  await page.addInitScript(() => localStorage.setItem("cpa-ui-theme", "light"));
  await page.goto("/admin/overview");
  await expect(page.locator(".auth-brand-logo")).toHaveAttribute("src", /codex-cpa-pool-logo\.svg$/);
  await page.locator('input[type="password"]').fill("visual-preview");
  await page.getByRole("button", { name: "验证并进入" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  await page.goto("/admin/configuration?group=品牌与身份");

  for (const theme of ["light", "dark", "light"] as const) {
    const current = await page.locator("html").getAttribute("data-theme");
    if (current !== theme) await page.getByRole("button", { name: `切换为${theme === "dark" ? "深色" : "浅色"}主题` }).click();
    const suffix = theme === "dark" ? "-dark" : "";
    await expect(page.locator(".side-nav-brand img")).toHaveAttribute("src", `/portal/assets/codex-cpa-pool-mark${suffix}.svg`);
    await expect(page.getByAltText("当前 Logo")).toHaveAttribute("src", `/portal/assets/codex-cpa-pool-logo${suffix}.svg`);
    await expect(page.locator('link[rel="icon"]')).toHaveAttribute("href", `/portal/assets/codex-cpa-pool-favicon${suffix}.svg`);
    for (const image of [page.locator(".side-nav-brand img"), page.getByAltText("当前 Logo")]) {
      await expect.poll(() => image.evaluate((element: HTMLImageElement) => element.complete && element.naturalWidth > 0)).toBe(true);
    }
  }

  await page.route("**/admin/api/settings/general", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    await route.fulfill({ response, json: { ...payload, branding: { custom_logo: true, logo_sha256: "0123456789abcdef" } } });
  });
  await page.route("**/branding/logo?*", (route) => route.fulfill({ contentType: "image/svg+xml", body: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 40"><circle cx="20" cy="20" r="16" fill="white"/></svg>' }));
  await page.reload();
  await expect(page.getByAltText("当前 Logo")).toHaveAttribute("src", "/branding/logo?v=0123456789abcdef");
  await page.getByRole("button", { name: "切换为深色主题" }).click();
  await expect(page.getByAltText("当前 Logo")).toHaveAttribute("src", "/branding/logo?v=0123456789abcdef");
});
