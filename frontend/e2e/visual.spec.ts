import { expect, test, type Page, type Route } from "@playwright/test";

const routes = [
  { slug: "overview", path: "/admin/overview", ready: "Token 使用", title: "运行总览" },
  { slug: "accounts", path: "/admin/accounts", ready: "更新通道", title: "账号管理" },
  { slug: "users", path: "/admin/users", ready: "添加用户", title: "用户管理" },
  { slug: "teams", path: "/admin/teams", ready: "创建团队", title: "团队管理" },
  { slug: "runtime", path: "/admin/runtime", ready: "容器服务", title: "运行维护" },
  { slug: "configuration", path: "/admin/configuration", ready: "保存配置", title: "系统设置" }
] as const;

const viewports = [
  { name: "desktop", width: 1440, height: 900 },
  { name: "narrow", width: 1024, height: 768 },
  { name: "mobile", width: 390, height: 844 }
] as const;

for (const viewport of viewports) {
  for (const theme of ["light", "dark"] as const) {
    test(`React 页面矩阵 ${viewport.name} ${theme}`, async ({ page }) => {
      test.setTimeout(120_000);
      await page.clock.setFixedTime(new Date("2026-08-28T05:42:00.000Z"));
      await page.setViewportSize(viewport);
      await setTheme(page, theme);
      await login(page, routes[0].path);

      for (const route of routes) {
        await openRoute(page, route);
        if (viewport.width <= 560) await expectActiveNavigationVisible(page);
        await expect(page).toHaveScreenshot(
          `react-${route.slug}-${viewport.name}-${theme}.png`,
          {
            fullPage: false,
            // macOS 15 and 27 rasterize mobile glyph edges differently. A
            // slightly wider color delta filters that antialiasing noise while
            // the bounded pixel ratio and the separate navigation, geometry
            // and overflow assertions continue to catch structural regressions.
            threshold: viewport.width <= 560 ? 0.3 : 0.2,
            maxDiffPixelRatio:
              viewport.width <= 560 ? 0.02 : viewport.width <= 1024 ? 0.015 : 0.005
          }
        );
      }
    });
  }
}

for (const viewport of viewports) {
  for (const theme of ["light", "dark"] as const) {
    test(`React 使用中心 ${viewport.name} ${theme} 视觉基准`, async ({ browser }) => {
      const context = await browser.newContext({
        baseURL: "http://127.0.0.1:5194",
        viewport,
        locale: "zh-CN",
        timezoneId: "Asia/Shanghai",
        colorScheme: theme
      });
      await context.addInitScript((value) => localStorage.setItem("cpa-ui-theme", value), theme);
      const page = await context.newPage();
      await installUsageVisualBackend(page);
      await page.goto("/usage/");
      await expect(page.getByText("账号明细", { exact: true })).toBeVisible();
      await expect(page).toHaveScreenshot(`react-usage-${viewport.name}-${theme}.png`, { fullPage: false });
      await context.close();
    });
  }
}

for (const state of ["loading", "empty", "error"] as const) {
  test(`React 使用中心 ${state} 视觉状态`, async ({ browser }) => {
    const context = await browser.newContext({
      baseURL: "http://127.0.0.1:5194",
      viewport: { width: 1440, height: 900 },
      locale: "zh-CN",
      timezoneId: "Asia/Shanghai"
    });
    const page = await context.newPage();
    await installUsageVisualBackend(page, state);
    await page.goto("/usage/");
    if (state === "loading") await expect(page.locator(".usage-skeleton-row").first()).toBeVisible();
    if (state === "empty") await expect(page.getByText("暂无可用账号", { exact: true })).toBeVisible();
    if (state === "error") await expect(page.getByText("账号与用量加载失败", { exact: true })).toBeVisible();
    await expect(page).toHaveScreenshot(`react-usage-state-${state}.png`, { fullPage: false });
    await context.close();
  });
}

test("个人使用中心每日趋势按范围和组合维度独立请求并可收起", async ({ page }) => {
  await page.setViewportSize({ width: 1086, height: 900 });
  await setTheme(page, "light");
  const trendRequests: string[] = [];
  page.on("request", (request) => {
    const url = new URL(request.url());
    if (url.pathname === "/usage/me/usage-trend") trendRequests.push(`${url.pathname}?${url.searchParams.toString()}`);
  });
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  const chart = page.getByRole("img", { name: /个人每日 Token 用量趋势/ });
  await expect(chart).toHaveCount(0);
  await expect.poll(() => trendRequests).toEqual([]);
  await expect(page.getByText("30天", { exact: true })).toBeVisible();
  const collapsedAccountTop = await page.getByText("账号明细", { exact: true }).evaluate((element) => element.getBoundingClientRect().top);

  await page.getByRole("button", { name: /^展开/ }).click();
  await expect(chart).toBeVisible();
  await expect.poll(() => trendRequests).toEqual([
    "/usage/me/usage-trend?window=30d&dimension=total"
  ]);
  const expandedAccountTop = await page.getByText("账号明细", { exact: true }).evaluate((element) => element.getBoundingClientRect().top);
  expect(collapsedAccountTop).toBeLessThan(expandedAccountTop - 120);
  await expect(page.getByLabel("趋势图例")).toHaveCount(0);
  await page.getByRole("button", { name: "7天", exact: true }).click();
  await page.getByRole("button", { name: "模型 + 推理强度", exact: true }).click();
  await expect.poll(() => trendRequests).toContain(
    "/usage/me/usage-trend?window=7d&dimension=model_reasoning"
  );
  await expect(page.getByText("主要组合", { exact: true })).toBeVisible();
  const primaryCombination = page.locator(".usage-trend-summary .primary-combination strong");
  await expect(primaryCombination).toHaveText("gpt-5.6-sol · xhigh");
  expect(await primaryCombination.evaluate((element) => ({
    clipped: element.scrollWidth > element.clientWidth,
    overflow: getComputedStyle(element).overflow,
    textOverflow: getComputedStyle(element).textOverflow
  }))).toEqual({ clipped: false, overflow: "visible", textOverflow: "clip" });

  await chart.focus();
  const tooltipOuter = page.locator(".usage-trend-echarts-tooltip");
  const tooltip = page.locator(".usage-trend-tooltip[data-active=true]");
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toHaveAttribute("data-layout", "single-column");
  expect(await tooltip.evaluate((element) => ({
    overflowX: getComputedStyle(element).overflowX,
    overflowY: getComputedStyle(element).overflowY,
    scrollable: element.scrollHeight > element.clientHeight || element.scrollWidth > element.clientWidth
  }))).toEqual({ overflowX: "hidden", overflowY: "hidden", scrollable: false });
  const combinationRows = tooltip.locator(".usage-trend-tooltip-combination");
  await expect(combinationRows.first()).toContainText("gpt-5.6-sol · xhigh");
  await expect(combinationRows.first()).toContainText("Token");
  await expect(combinationRows.locator("small")).toHaveCount(0);
  const markerStyles = await combinationRows.locator("i").evaluateAll((markers) => markers.map((marker) => ({
    color: getComputedStyle(marker).backgroundColor,
    width: marker.getBoundingClientRect().width,
    height: marker.getBoundingClientRect().height
  })));
  expect(markerStyles.length).toBeGreaterThanOrEqual(2);
  expect(new Set(markerStyles.map((marker) => marker.color)).size).toBeGreaterThan(1);
  expect(markerStyles.every((marker) => marker.color !== "rgb(21, 27, 40)" && marker.width >= 8 && marker.height >= 8)).toBe(true);
  expect(await combinationRows.evaluateAll((rows) => rows.every((row) => {
    const labelElement = row.querySelector<HTMLElement>("b");
    const valueElement = row.querySelector<HTMLElement>("em");
    if (!labelElement || !valueElement) return false;
    const rowRect = row.getBoundingClientRect();
    const label = labelElement.getBoundingClientRect();
    const value = valueElement.getBoundingClientRect();
    return Boolean(Math.abs(label.top - value.top) <= 1
      && labelElement.clientWidth >= labelElement.scrollWidth
      && valueElement.clientWidth >= valueElement.scrollWidth
      && value.right <= rowRect.right + .5);
  }))).toBe(true);
  expect(await tooltipOuter.evaluate((outer) => {
    const outerRect = outer.getBoundingClientRect();
    const inner = outer.querySelector<HTMLElement>(".usage-trend-tooltip");
    const values = [...outer.querySelectorAll<HTMLElement>(".usage-trend-tooltip-row em, .usage-trend-tooltip-total em")];
    if (!inner) return false;
    const innerRect = inner.getBoundingClientRect();
    return innerRect.right <= outerRect.right + .5
      && values.every((value) => value.getBoundingClientRect().right <= outerRect.right + .5);
  })).toBe(true);
  const tooltipRect = await tooltip.evaluate((element) => element.getBoundingClientRect());
  expect(tooltipRect.top).toBeGreaterThanOrEqual(0);
  expect(tooltipRect.bottom).toBeLessThanOrEqual(900);
  await expect(tooltip).toHaveScreenshot("react-usage-trend-tooltip-model-reasoning.png");
  await page.getByRole("button", { name: /^收起/ }).click();
  await expect(chart).toHaveCount(0);
});

test("个人使用中心趋势收起态视觉基准", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  await expect(page.getByRole("img", { name: /个人每日 Token 用量趋势/ })).toHaveCount(0);
  await expect(page.getByText("账号明细", { exact: true })).toBeVisible();
  await expect(page).toHaveScreenshot("react-usage-trend-collapsed-desktop-light.png", { fullPage: false });
});

test("个人使用中心模型与推理强度组合趋势视觉基准", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  await page.getByRole("button", { name: /^展开/ }).click();
  await page.getByRole("button", { name: "模型 + 推理强度", exact: true }).click();
  await expect(page.getByText("主要组合", { exact: true })).toBeVisible();
  await expect(page.getByRole("img", { name: /个人每日 Token 用量趋势/ })).toBeVisible();
  await expect(page).toHaveScreenshot("react-usage-trend-model-reasoning-desktop-light.png", { fullPage: false });
});

test("个人使用中心移动端可滚动到账号明细", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await setTheme(page, "light");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  const header = page.locator(".usage-center-head");
  const content = page.locator(".usage-center-content");
  const accountSection = page.locator(".usage-account-section");
  const accountTable = page.locator(".usage-table-wrap");
  const lastAccount = accountTable.locator(".usage-summary-row").last();
  const headerTop = await header.evaluate((element) => element.getBoundingClientRect().top);

  await expect(accountSection).toBeAttached();
  expect(await accountSection.evaluate((element) => element.getBoundingClientRect().height)).toBeGreaterThan(400);
  expect(await accountTable.evaluate((element) => element.getBoundingClientRect().height)).toBeGreaterThan(0);
  await lastAccount.scrollIntoViewIfNeeded();

  expect(await content.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  expect(await header.evaluate((element) => element.getBoundingClientRect().top)).toBe(headerTop);
  const accountRect = await lastAccount.evaluate((element) => element.getBoundingClientRect());
  expect(accountRect.top).toBeGreaterThanOrEqual(0);
  expect(accountRect.bottom).toBeLessThanOrEqual(844);
  await expect(accountTable).toBeVisible();
});

test("账号展开区复用旧版四层信息结构", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "dark");
  await login(page, "/admin/accounts", "更新通道");

  await page.getByRole("row", { name: "展开 cpa-main" }).click();
  await expect(page.getByRole("region", { name: "模型与推理强度 Token 明细" })).toBeVisible();
  await expect(page.getByText("上游邮箱", { exact: true })).toBeVisible();
  await expect(page.getByText("Token 总计", { exact: true })).toBeVisible();
  await expect(page.getByText("gpt-5.6-sol", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "重启容器" })).toBeVisible();
  await expect(page.getByRole("button", { name: "迁移全部用户" })).toBeVisible();

  await expect(page).toHaveScreenshot("react-accounts-expanded-desktop-dark.png", { fullPage: false });
});

const stateCases = [
  {
    slug: "overview",
    path: "/admin/overview",
    primary: "**/admin/api/overview/summary",
    loading: "正在加载总览",
    error: "总览数据加载失败",
    empty: "所选范围内没有账号 Token 数据",
    stateSurface: "page",
    emptyRoutes: ["**/admin/api/overview/summary", "**/admin/api/overview/usage?*"]
  },
  {
    slug: "accounts",
    path: "/admin/accounts",
    primary: "**/admin/api/accounts?*",
    loading: "正在加载账号数据",
    error: "视觉回归模拟错误",
    empty: "还没有 CPA 账号",
    stateSurface: "catalog"
  },
  {
    slug: "users",
    path: "/admin/users",
    primary: "**/admin/api/users?*",
    loading: "正在加载用户目录",
    error: "视觉回归模拟错误",
    empty: "还没有用户",
    stateSurface: "catalog"
  },
  {
    slug: "teams",
    path: "/admin/teams",
    primary: "**/admin/api/teams",
    loading: "正在加载团队目录",
    error: "视觉回归模拟错误",
    empty: "没有匹配的团队",
    stateSurface: "catalog"
  },
  {
    slug: "runtime",
    path: "/admin/runtime",
    primary: "**/admin/api/runtime/services",
    loading: "正在加载运行维护",
    error: "视觉回归模拟错误",
    empty: "当前没有可见容器服务",
    stateSurface: "catalog"
  },
  {
    slug: "configuration",
    path: "/admin/configuration",
    primary: "**/admin/api/settings/configuration",
    loading: "正在加载配置中心",
    error: "配置中心加载失败",
    empty: "当前没有可配置项",
    stateSurface: "page"
  }
] as const;

for (const stateCase of stateCases) {
  test(`${stateCase.slug} 加载状态`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await setTheme(page, "light");
    let releaseRequest = () => {};
    const requestGate = new Promise<void>((resolve) => {
      releaseRequest = resolve;
    });
    await page.route(stateCase.primary, async (route) => {
      await requestGate;
      await route.continue();
    });
    try {
      await login(page, stateCase.path);
      if (stateCase.stateSurface === "page") {
        await expect(page.getByLabel(stateCase.loading, { exact: true })).toBeVisible();
      } else {
        await expect(page.locator(".top-bar-refresh-state")).toHaveText("正在刷新");
      }
      await expect(page).toHaveScreenshot(`react-${stateCase.slug}-state-loading.png`, { fullPage: false });
    } finally {
      releaseRequest();
    }
  });

  test(`${stateCase.slug} 空数据状态`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await setTheme(page, "light");
    const matchers = "emptyRoutes" in stateCase ? stateCase.emptyRoutes : [stateCase.primary];
    for (const matcher of matchers) {
      await page.route(matcher, async (route) => fulfillEmpty(route, stateCase.slug));
    }
    await login(page, stateCase.path, stateCase.empty);
    await expect(page).toHaveScreenshot(`react-${stateCase.slug}-state-empty.png`, { fullPage: false });
  });

  test(`${stateCase.slug} 错误状态`, async ({ page }) => {
    await page.setViewportSize({ width: 1440, height: 900 });
    await setTheme(page, "light");
    await page.route(stateCase.primary, (route) => route.fulfill({
      status: 500,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "visual_test", message: "视觉回归模拟错误" } })
    }));
    await login(page, stateCase.path);
    if (stateCase.stateSurface === "page") {
      await expect(page.getByText(stateCase.error, { exact: false }).first()).toBeVisible();
    } else {
      await expect(page.locator(".top-bar-refresh-state")).toHaveText("刷新失败");
      await expect(page.getByText(stateCase.error, { exact: true }).first()).toBeVisible();
    }
    await expect(page).toHaveScreenshot(`react-${stateCase.slug}-state-error.png`, { fullPage: false });
  });
}

test("所有菜单滚动时保持页面标题固定", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await login(page, routes[0].path);

  for (const route of routes) {
    await openRoute(page, route);
    const topBar = page.locator(".top-bar");
    const before = await topBar.boundingBox();
    await page.locator(".main-surface").evaluate((element) => { element.scrollTop = 1_000; });
    await expect.poll(async () => (await topBar.boundingBox())?.y).toBe(before?.y);
    await page.locator(".main-surface").evaluate((element) => { element.scrollTop = 0; });
  }
});

test("共享表格视口保持动态高度、右侧滚动槽和正确边界阴影", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  let accountReads = 0;
  await page.route("**/admin/api/accounts?*", async (route) => {
    accountReads += 1;
    const response = await route.fetch();
    const payload = await response.json() as { accounts: Array<Record<string, unknown>> };
    const source = payload.accounts;
    if (accountReads > 1) payload.accounts = Array.from({ length: 24 }, (_, index) => {
      const account = structuredClone(source[index % source.length]);
      const id = `cpa-scroll-${String(index + 1).padStart(2, "0")}`;
      return {
        ...account,
        id,
        email: `${id}@example.com`,
        port: 19_000 + index,
        service: `cliproxy-${id}`,
        default: index === 0,
        account_state: {
          ...(account.account_state as Record<string, unknown>),
          account: id
        }
      };
    });
    await route.fulfill({ response, json: payload });
  });
  await login(page, "/admin/accounts", "更新通道");
  await page.addStyleTag({ content: ".admin-table-viewport::before,.admin-table-viewport::after,.native-table-viewport::before,.native-table-viewport::after{transition:none!important}" });

  const viewport = page.locator(".account-table-state .admin-table-viewport");
  const body = viewport.locator(".ant-table-body");
  await expect(viewport).toHaveAttribute("data-scroll-overflow", "false");
  const compactColumn = await page.evaluate(() => {
    const header = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-header th:nth-child(8)");
    const token = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-body .account-summary-row td:nth-child(8)");
    if (!header || !token) throw new Error("账号表格初始列锚点缺失");
    const headerRect = header.getBoundingClientRect();
    const tokenRect = token.getBoundingClientRect();
    return {
      headerLeft: headerRect.left,
      headerRight: headerRect.right,
      bodyLeftDelta: Math.abs(headerRect.left - tokenRect.left),
      bodyRightDelta: Math.abs(headerRect.right - tokenRect.right)
    };
  });
  expect(compactColumn.bodyLeftDelta).toBeLessThanOrEqual(1);
  expect(compactColumn.bodyRightDelta).toBeLessThanOrEqual(1);

  await page.getByRole("button", { name: "刷新", exact: true }).click();
  await expect.poll(() => accountReads).toBeGreaterThan(1);
  await expect.poll(() => body.evaluate((element) => element.scrollHeight - element.clientHeight)).toBeGreaterThan(100);
  await expect(viewport).toHaveAttribute("data-scroll-overflow", "true");
  await expect(viewport).toHaveClass(/can-scroll-down/);
  await expect(viewport).not.toHaveClass(/can-scroll-up/);
  expect(await body.evaluate((element) => getComputedStyle(element).scrollbarGutter)).toBe("stable");

  const topState = await viewport.evaluate((element) => ({
    before: getComputedStyle(element, "::before").opacity,
    after: getComputedStyle(element, "::after").opacity
  }));
  expect(topState).toEqual({ before: "0", after: "1" });

  await body.evaluate((element) => {
    element.scrollTop = (element.scrollHeight - element.clientHeight) / 2;
    element.dispatchEvent(new Event("scroll"));
  });
  await expect(viewport).toHaveClass(/can-scroll-up/);
  await expect(viewport).toHaveClass(/can-scroll-down/);
  expect(await viewport.evaluate((element) => ({
    before: getComputedStyle(element, "::before").opacity,
    after: getComputedStyle(element, "::after").opacity
  }))).toEqual({ before: "1", after: "1" });

  await body.evaluate((element) => {
    element.scrollTop = element.scrollHeight;
    element.dispatchEvent(new Event("scroll"));
  });
  await expect(viewport).toHaveClass(/can-scroll-up/);
  await expect(viewport).not.toHaveClass(/can-scroll-down/);
  expect(await viewport.evaluate((element) => ({
    before: getComputedStyle(element, "::before").opacity,
    after: getComputedStyle(element, "::after").opacity
  }))).toEqual({ before: "1", after: "0" });

  const layout = await page.evaluate(() => {
    const main = document.querySelector<HTMLElement>(".main-surface");
    const accountPage = document.querySelector<HTMLElement>(".account-page");
    const tableViewport = document.querySelector<HTMLElement>(".account-table-state .admin-table-viewport");
    const tableBody = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-body");
    const headerCell = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-header th:nth-child(8)");
    const tokenCell = document.querySelector<HTMLElement>(".account-legacy-table .ant-table-body .account-summary-row td:nth-child(8)");
    if (!main || !accountPage || !tableViewport || !tableBody || !headerCell || !tokenCell) throw new Error("账号表格几何锚点缺失");
    const pageRect = accountPage.getBoundingClientRect();
    const viewportRect = tableViewport.getBoundingClientRect();
    const headerRect = headerCell.getBoundingClientRect();
    const tokenRect = tokenCell.getBoundingClientRect();
    return {
      mainOverflowY: getComputedStyle(main).overflowY,
      mainHasVerticalOverflow: main.scrollHeight > main.clientHeight + 1,
      bottomGap: Math.round((pageRect.bottom - viewportRect.bottom) * 100) / 100,
      viewportHeight: viewportRect.height,
      tokenAlignment: getComputedStyle(tokenCell).textAlign,
      tokenVerticalAlignment: getComputedStyle(tokenCell).verticalAlign,
      rightScrollbarSlotWidth: tableBody.offsetWidth - tableBody.clientWidth,
      finalColumnLeftDelta: Math.abs(headerRect.left - tokenRect.left),
      finalColumnRightDelta: Math.abs(headerRect.right - tokenRect.right)
    };
  });
  expect(layout.mainOverflowY).toBe("hidden");
  expect(layout.mainHasVerticalOverflow).toBe(false);
  expect(layout.bottomGap).toBeGreaterThanOrEqual(29);
  expect(layout.bottomGap).toBeLessThanOrEqual(31);
  expect(layout.viewportHeight).toBeGreaterThan(150);
  expect(layout.tokenAlignment).toBe("right");
  expect(layout.tokenVerticalAlignment).toBe("middle");
  // Overlay scrollbars report zero occupied width; classic scrollbars report
  // their native right-side slot.  Header/body alignment below must hold in
  // either mode and is the user-visible invariant.
  expect(layout.rightScrollbarSlotWidth).toBeGreaterThanOrEqual(0);
  expect(layout.rightScrollbarSlotWidth).toBeLessThanOrEqual(16);
  expect(layout.finalColumnLeftDelta).toBeLessThanOrEqual(1);
  expect(layout.finalColumnRightDelta).toBeLessThanOrEqual(1);
  expect(Math.abs(layout.finalColumnLeftDelta - compactColumn.bodyLeftDelta)).toBeLessThanOrEqual(1);
  expect(Math.abs(layout.finalColumnRightDelta - compactColumn.bodyRightDelta)).toBeLessThanOrEqual(1);

  await page.goto("/admin/overview");
  const naturalTable = page.locator(".overview-legacy-table-wrap").first();
  await expect(naturalTable).toHaveAttribute("data-scroll-overflow", "false");
  await expect(naturalTable).not.toHaveClass(/can-scroll-up|can-scroll-down/);
  expect(await naturalTable.evaluate((element) => ({
    gutter: getComputedStyle(element).scrollbarGutter,
    before: getComputedStyle(element, "::before").opacity,
    after: getComputedStyle(element, "::after").opacity
  }))).toEqual({ gutter: "auto", before: "0", after: "0" });
});

test("新版本提示收敛到侧栏并在移动端保留入口", async ({ page }) => {
  let freshChecks = 0;
  await page.route("**/admin/api/release*", async (route) => {
    const url = new URL(route.request().url());
    if (url.searchParams.get("fresh") === "1") freshChecks += 1;
    await fulfillJSON(route, {
      current_version: "v1.0.0",
      latest_version: "v1.1.0",
      available: true,
      checked_at: 1_787_500_800,
      status: "ok"
    });
  });
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "dark");
  await login(page, "/admin/overview", "Token 使用");

  await expect(page.locator(".release-notice")).toHaveCount(0);
  const desktopEntry = page.locator(".side-nav-footer .release-version-update");
  await expect(desktopEntry).toBeVisible();
  await expect(desktopEntry).toContainText("发现新版本");
  await expect(desktopEntry).toContainText("v1.1.0");
  await desktopEntry.click();
  const details = page.getByRole("region", { name: "应用版本详情" });
  await expect(details).toBeVisible();
  await expect(details).toContainText("当前版本v1.0.0");
  await expect(details).toContainText("最新版本v1.1.0");
  await details.getByRole("button", { name: "重新检查" }).click();
  await expect.poll(() => freshChecks).toBe(1);
  await page.keyboard.press("Escape");
  await expect(details).toBeHidden();

  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator(".side-nav-footer")).toBeHidden();
  await expect(page.locator(".mobile-release-indicator")).toBeVisible();
  await expect(page.locator(".main-surface")).not.toHaveCSS("overflow-x", "scroll");
});

test("30 天图表 Tooltip 为单列 Top 10 且无滚动条", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await setTheme(page, "light");
  await page.route("**/admin/api/overview/usage?*", async (route) => {
    const response = await route.fetch();
    const payload = await response.json();
    payload.users = Array.from({ length: 12 }, (_, index) => {
      const value = (index + 1) * 100_000;
      return {
        name: `user-${String(index + 1).padStart(2, "0")}@example.com`,
        values: payload.buckets.map(() => value),
        current: value,
        average: value,
        maximum: value,
        total: value * payload.buckets.length
      };
    });
    await route.fulfill({ response, json: payload });
  });
  await login(page, "/admin/overview", "Token 使用");

  const responsePromise = page.waitForResponse((response) => response.url().includes("window=2592000"));
  const started = Date.now();
  await page.getByRole("button", { name: "30 天", exact: true }).click();
  await responsePromise;
  // This is the browser-visible round trip through Vite plus the deterministic mock proxy.
  // The SQLite query itself keeps the stricter 500 ms gate in tests.test_usage_store.
  expect(Date.now() - started).toBeLessThan(1_000);

  const chart = page.locator(".overview-legacy-chart").nth(2);
  await expect(chart).toBeVisible();
  await chart.scrollIntoViewIfNeeded();
  await expect(chart.locator("svg")).toBeVisible();
  const box = await chart.boundingBox();
  if (!box) throw new Error("用户趋势图没有可交互区域");
  await page.mouse.move(box.x + box.width * 0.55, box.y + box.height * 0.45);

  const tooltip = page.locator(".overview-chart-tooltip[data-active=true]");
  await expect(tooltip).toBeVisible();
  await expect(tooltip).toHaveAttribute("data-layout", "single-column");
  const rows = tooltip.locator(":scope > span");
  await expect(rows).toHaveCount(10);
  await expect(rows.first().locator("b")).toHaveText("user-12@example.com");
  await expect(rows.last().locator("b")).toHaveText("user-03@example.com");
  expect(await tooltip.evaluate((element) => ({
    vertical: element.scrollHeight <= element.clientHeight,
    horizontal: element.scrollWidth <= element.clientWidth
  }))).toEqual({ vertical: true, horizontal: true });
  await expect(page).toHaveScreenshot("react-overview-tooltip-top10.png", {
    fullPage: false,
    // Tooltip glyph antialiasing differs across supported macOS releases.
    // DOM shape, row count and overflow are asserted immediately above.
    maxDiffPixelRatio: 0.02
  });
});

test("使用中心仅按需读取 API Key，关闭立即清除，刷新后只展示新 Key", async ({ page }) => {
  const oldKey = "old-e2e-api-key-1234";
  const newKey = "new-e2e-api-key-9876";
  let revealRequests = 0;
  let rotateRequests = 0;
  await page.route(/\/usage\/(?:session|me)(?:\/|\?|$)/, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    if (request.method() === "GET" && path === "/usage/session") {
      await fulfillJSON(route, {
        authenticated: true,
        user: "alice@example.com",
        expires_at: 1_787_544_000,
        password_change_required: false
      });
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/profile") {
      await fulfillJSON(route, {
        user: "alice@example.com",
        current_group: "alpha",
        generated_at: 1_787_500_800
      });
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/route") {
      await fulfillJSON(route, { current_group: "alpha", generated_at: 1_787_500_800 });
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/quota") {
      await fulfillJSON(route, usageQuotaFixture());
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/accounts") {
      await fulfillJSON(route, usageAccountsFixture());
      return;
    }
    if (request.method() === "GET" && path === "/usage/me/key") {
      revealRequests += 1;
      await fulfillJSON(route, { api_key: oldKey, generated_at: 1_787_500_800 }, { "Cache-Control": "no-store" });
      return;
    }
    if (request.method() === "POST" && path === "/usage/me/key/rotate") {
      rotateRequests += 1;
      expect(request.postDataJSON()).toEqual({ confirm: true });
      await fulfillJSON(route, {
        message: "API Key 已刷新",
        api_key: newKey,
        snapshot_generation: "generation-2"
      }, { "Cache-Control": "no-store" });
      return;
    }
    await route.fulfill({
      status: 404,
      contentType: "application/json",
      body: JSON.stringify({ error: { code: "e2e_not_found", message: path } })
    });
  });

  await page.goto("http://127.0.0.1:5194/usage/");
  await expect(page.getByRole("button", { name: "管理 API Key" })).toBeVisible();
  expect(revealRequests).toBe(0);
  await page.getByRole("button", { name: "管理 API Key" }).click();
  const keyDialog = page.getByRole("dialog", { name: "管理 API Key" });
  await expect(keyDialog).toBeVisible();
  await expect(keyDialog.getByRole("button", { name: "查看 API Key" })).toBeVisible();
  expect(revealRequests).toBe(0);
  await expect(page.locator("body")).not.toContainText(oldKey);

  await keyDialog.getByRole("button", { name: "查看 API Key" }).click();
  const keyInput = page.getByLabel("API Key", { exact: true });
  await expect(keyInput).toHaveValue(oldKey);
  expect(revealRequests).toBe(1);
  await keyDialog.locator(".ant-modal-footer").getByRole("button", { name: "关闭" }).click();
  await expect(keyInput).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(oldKey);

  await page.getByRole("button", { name: "管理 API Key" }).click();
  await page.getByRole("dialog", { name: "管理 API Key" }).getByRole("button", { name: "刷新 API Key", exact: true }).click();
  await page.getByRole("button", { name: "确认刷新并使旧 Key 失效" }).click();
  await expect(keyInput).toHaveValue(newKey);
  expect(rotateRequests).toBe(1);
  await expect(page.locator("body")).not.toContainText(oldKey);
  expect(await page.evaluate(([oldValue, newValue]) => {
    const storage = [window.localStorage, window.sessionStorage];
    return storage.some((entry) => Object.values(entry).some((value) => value === oldValue || value === newValue));
  }, [oldKey, newKey])).toBe(false);

  await keyDialog.locator(".ant-modal-footer").getByRole("button", { name: "关闭" }).click();
  await expect(keyInput).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(newKey);
});

test("使用中心客户端配置弹框直接展示必要内容", async ({ page, context }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await context.grantPermissions(["clipboard-read", "clipboard-write"], { origin: "http://127.0.0.1:5194" });
  await setTheme(page, "dark");
  await installUsageVisualBackend(page);
  await page.goto("http://127.0.0.1:5194/usage/");

  await page.getByRole("button", { name: "配置 Codex" }).click();
  const codexDialog = page.getByRole("dialog", { name: "配置 Codex" });
  await expect(codexDialog).toBeVisible();
  await expect(codexDialog.getByText("选择要完成的 Codex 任务")).toHaveCount(0);
  await expect(codexDialog.getByText("Codex 配置内容")).toBeVisible();
  await expect(codexDialog.getByText("迁移旧会话")).toBeVisible();
  await expect(codexDialog.getByRole("button", { name: "复制配置" })).toBeVisible();
  await expect(codexDialog.getByRole("button", { name: "复制迁移指令" })).toBeVisible();
  await codexDialog.locator(".portal-config-actions").getByRole("button", { name: "关闭" }).click();

  await page.getByRole("button", { name: "导入 CC Switch" }).click();
  const switchDialog = page.getByRole("dialog", { name: "完成 CC Switch 配置" });
  await expect(switchDialog).toBeVisible();
  await expect(switchDialog.getByText("Codex 配置内容")).toBeVisible();
  await expect(switchDialog.getByText("迁移旧会话")).toBeVisible();
  await expect(switchDialog.getByRole("button", { name: "复制迁移指令" })).toBeVisible();
  await expect(switchDialog.getByText("操作文件")).toHaveCount(0);
  await expect(switchDialog.getByRole("button", { name: "仅复制图片配置" })).toHaveCount(0);
  await expect(switchDialog.locator(".portal-config-actions").getByRole("button")).toHaveCount(2);
  await expect(switchDialog.locator(".portal-config-actions").getByRole("button", { name: "关闭" })).toBeVisible();
  const copyAndImport = switchDialog.locator(".portal-config-actions").getByRole("button", { name: "复制并导入" });
  await expect(copyAndImport).toBeVisible();
  await expect(switchDialog).toHaveScreenshot("react-usage-ccswitch-config-dialog-dark.png");
  const expectedConfig = await switchDialog.locator(".portal-config-preview").first().innerText();
  await page.evaluate(() => navigator.clipboard.writeText("CPA_CLIPBOARD_SENTINEL"));
  await copyAndImport.click();
  await expect(switchDialog.getByRole("status")).toHaveText("完整配置已复制，正在打开 CC Switch…");
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(expectedConfig);
});

async function setTheme(page: Page, theme: "light" | "dark") {
  await page.addInitScript((value) => window.localStorage.setItem("cpa-ui-theme", value), theme);
}

async function login(page: Page, path: string, ready?: string) {
  await page.goto(path);
  const password = page.locator('input[type="password"]');
  await expect(password).toBeVisible();
  await password.fill("visual-preview");
  await page.getByRole("button", { name: "验证并进入" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  if (ready) await expect(page.getByText(ready, { exact: false }).first()).toBeVisible();
}

async function openRoute(page: Page, route: (typeof routes)[number]) {
  await page.goto(route.path);
  await expect(page.getByRole("heading", { name: route.title, level: 1 })).toBeVisible();
  await expect(page.getByText(route.ready, { exact: false }).first()).toBeVisible();
  await page.waitForTimeout(200);
}

async function expectActiveNavigationVisible(page: Page) {
  const geometry = await page.locator('.admin-nav [aria-current="page"]').evaluate((element) => {
    const item = element.getBoundingClientRect();
    const navigation = element.parentElement?.getBoundingClientRect();
    return {
      itemWidth: item.width,
      visible: Boolean(
        navigation
        && item.left >= Math.max(0, navigation.left)
        && item.right <= Math.min(window.innerWidth, navigation.right)
      )
    };
  });
  expect(geometry.itemWidth).toBeGreaterThan(50);
  expect(geometry.visible).toBe(true);
}

async function fulfillEmpty(route: Route, slug: string) {
  const response = await route.fetch();
  const payload = await response.json();
  if (slug === "overview") {
    if (route.request().url().includes("/summary")) {
      payload.summary = Object.fromEntries(Object.keys(payload.summary).map((key) => [key, 0]));
    } else {
      payload.accounts = [];
      payload.users = [];
      payload.selected_accounts = [];
      payload.selected_users = [];
    }
  } else if (slug === "accounts") {
    payload.accounts = [];
    payload.warnings = [];
  } else if (slug === "users") {
    payload.users = [];
    payload.pagination = { ...payload.pagination, page: 1, total: 0 };
  } else if (slug === "teams") {
    payload.teams = [];
  } else if (slug === "runtime") {
    payload.services = [];
  } else if (slug === "configuration") {
    payload.groups = [];
    payload.field_count = 0;
  }
  await route.fulfill({ response, json: payload });
}

async function fulfillJSON(route: Route, payload: unknown, headers: Record<string, string> = {}) {
  await route.fulfill({
    status: 200,
    contentType: "application/json",
    headers,
    body: JSON.stringify(payload)
  });
}

function usageAccountsFixture() {
  const usage = {
    request_count: 2,
    success_count: 2,
    failed_count: 0,
    input_tokens: 100,
    output_tokens: 20,
    reasoning_tokens: 10,
    cached_tokens: 0,
    total_tokens: 120,
    weighted_tokens: 180,
    last_used_at: 1_787_500_700
  };
  return {
    generated_at: 1_787_500_800,
    window: {
      window: "today",
      window_seconds: null,
      window_start_at: 1_787_472_000,
      window_end_at: 1_787_500_800,
      window_timezone: "Asia/Shanghai"
    },
    current_group: "alpha",
    accounts: [{
      id: "alpha",
      display_name: "CPA 1",
      current: true,
      enabled: true,
      selectable: true,
      status: {
        code: "available",
        label: "可用",
        tone: "success",
        reason: "账号当前可用",
        selectable: true,
        used_percent: 20,
        remaining_percent: 80,
        reset_at: 1_787_846_400
      },
      active_users_1h: 2,
      active_user_emails_1h: ["alice@example.com", "bob@example.com"],
      reset_credit_count: 1,
      resettable: true,
      reset_window_labels: ["常规周限额"],
      usage
    }],
    totals: usage,
    warnings: []
  };
}

function usageQuotaFixture() {
  return {
    generated_at: 1_787_500_800,
    weekly_quota: {
      period: "natural_week",
      timezone: "Asia/Shanghai",
      week_start_at: 1_787_241_600,
      week_end_at: 1_787_846_400,
      limit_tokens: 20_000_000,
      base_limit_tokens: 20_000_000,
      bonus_tokens: 0,
      used_tokens: 3_000_000,
      weighted_used_tokens: 3_000_000,
      raw_used_tokens: 2_400_000,
      unweighted_used_tokens: 2_400_000,
      weighted_raw_used_tokens: 3_000_000,
      usage_reset_tokens: 0,
      remaining_tokens: 17_000_000,
      used_percent: 15,
      limit_reached: false,
      source: "default",
      policy_mode: "inherit",
      policy_tokens: null,
      policy_updated_at: null,
      policy_updated_by: null,
      policy_reset_at: null,
      default_limit_tokens: 20_000_000,
      unlimited: false,
      soft_limit: false,
      quota_unit: "weighted_tokens",
      adjustment_count: 0,
      personal_policy_reset_enabled: true
    }
  };
}

function usageTrendFixture(window: string, dimension: string) {
  const windowDays = window === "7d" ? 7 : window === "90d" ? 90 : 30;
  const end = Date.UTC(2026, 7, 29) / 1000;
  const start = end - (windowDays - 1) * 86_400;
  const combinationDefinitions = [
    ["gpt-5.6-sol", "xhigh", 0.52],
    ["gpt-5.6-terra", "medium", 0.26],
    ["gpt-5.6-luna", "high", 0.16],
    ["gpt-5.6-luna", "low", 0.06]
  ] as const;
  const days = Array.from({ length: windowDays }, (_, index) => {
    const totalTokens = 380_000 + index * 22_000 + (index % 5) * 35_000;
    const weightedTokens = Math.round(totalTokens * 1.28);
    return {
      date: new Date((start + index * 86_400) * 1000).toISOString().slice(0, 10),
      start_at: start + index * 86_400,
      end_at: start + (index + 1) * 86_400,
      collection_state: index === 0 && windowDays === 90 ? "partial" : "complete",
      request_count: 80 + index * 2,
      total_tokens: totalTokens,
      weighted_tokens: weightedTokens,
      combinations: dimension === "model_reasoning" ? combinationDefinitions.map(([model, reasoningEffort, share]) => ({
        model,
        reasoning_effort: reasoningEffort,
        request_count: Math.max(1, Math.round((80 + index * 2) * share)),
        total_tokens: Math.round(totalTokens * share),
        weighted_tokens: Math.round(weightedTokens * share)
      })) : []
    };
  });
  return {
    generated_at: end,
    window,
    window_days: windowDays,
    window_start_at: days[0]?.start_at ?? start,
    window_end_at: days.at(-1)?.end_at ?? end,
    window_timezone: "Asia/Shanghai",
    dimension,
    definition: "视觉回归自然日聚合",
    collection_started_at: days[0]?.start_at ?? start,
    effective_start_at: days[0]?.start_at ?? start,
    days
  };
}

async function installUsageVisualBackend(page: Page, state: "normal" | "loading" | "empty" | "error" = "normal") {
  await page.route("**/site-config.json", (route) => fulfillJSON(route, {
    version: 1,
    product_name: "Codex CPA Cluster",
    short_name: "Codex CPA",
    environment_label: "本地模拟预览",
    public_base_url: "http://127.0.0.1:8317",
    provider_name: "Codex CPA",
    api_key_env: "CPA_API_KEY",
    default_model: "gpt-5.6-sol",
    logo: { custom: false, url: "/portal/assets/codex-cpa-cluster-logo.svg", content_type: "image/svg+xml", sha256: "", updated_at: null }
  }));
  await page.route(/\/usage\/(?:session|me)(?:\/|\?|$)/, async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    if (path === "/usage/session") {
      await fulfillJSON(route, { authenticated: true, user: "alice@example.com", expires_at: 1_787_544_000, password_change_required: false });
      return;
    }
    if (path === "/usage/me/profile") {
      await fulfillJSON(route, { user: "alice@example.com", current_group: "alpha", generated_at: 1_787_500_800 });
      return;
    }
    if (path === "/usage/me/route") {
      await fulfillJSON(route, { current_group: "alpha", generated_at: 1_787_500_800 });
      return;
    }
    if (path === "/usage/me/quota") {
      await fulfillJSON(route, usageQuotaFixture());
      return;
    }
    if (path === "/usage/me/accounts") {
      if (state === "loading") await new Promise((resolve) => setTimeout(resolve, 30_000));
      if (state === "error") {
        await route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ error: { code: "visual_error", message: "视觉回归模拟错误" } }) });
        return;
      }
      const payload = usageAccountsFixture();
      if (state === "empty") payload.accounts = [];
      await fulfillJSON(route, payload);
      return;
    }
    if (path === "/usage/me/key") {
      await fulfillJSON(route, { api_key: "visual-api-key", generated_at: 1_787_500_800 }, { "Cache-Control": "no-store" });
      return;
    }
    if (path === "/usage/me/usage-trend") {
      await fulfillJSON(route, usageTrendFixture(
        url.searchParams.get("window") ?? "30d",
        url.searchParams.get("dimension") ?? "total"
      ));
      return;
    }
    await route.fulfill({ status: 404, contentType: "application/json", body: JSON.stringify({ error: { code: "visual_not_found", message: path } }) });
  });
}
