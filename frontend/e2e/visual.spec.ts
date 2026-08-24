import { expect, test, type Page, type Route } from "@playwright/test";

const routes = [
  { slug: "overview", path: "/admin/overview", ready: "Token 使用", title: "运行总览" },
  { slug: "accounts", path: "/admin/accounts", ready: "账号负载与额度", title: "账号管理" },
  { slug: "users", path: "/admin/users", ready: "用户目录", title: "用户管理" },
  { slug: "teams", path: "/admin/teams", ready: "团队目录", title: "团队管理" },
  { slug: "runtime", path: "/admin/runtime", ready: "Compose 服务", title: "运行维护" },
  { slug: "configuration", path: "/admin/configuration", ready: "保存配置", title: "配置中心" },
  { slug: "general-settings", path: "/admin/settings", ready: "保存通用设置", title: "配置中心" },
  { slug: "notifications", path: "/admin/notifications", ready: "保存通知规则", title: "配置中心" }
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
      await page.setViewportSize(viewport);
      await setTheme(page, theme);
      await login(page, routes[0].path);

      for (const route of routes) {
        await openRoute(page, route);
        if (viewport.width <= 560) await expectActiveNavigationVisible(page);
        await expect(page).toHaveScreenshot(
          `react-${route.slug}-${viewport.name}-${theme}.png`,
          { fullPage: false }
        );
      }
    });
  }
}

test("旧版页面提供独立桌面基准", async ({ browser }) => {
  const context = await browser.newContext({
    baseURL: "http://127.0.0.1:8896",
    viewport: { width: 1440, height: 900 },
    locale: "zh-CN",
    timezoneId: "Asia/Shanghai",
    colorScheme: "light"
  });
  const page = await context.newPage();
  await page.goto("/admin/");
  await page.locator("#management-key").fill("visual-preview");
  await page.locator("#auth-form button[type=submit]").click();
  await expect(page.locator("#app-shell")).toBeVisible();

  for (const view of ["overview", "accounts", "users", "organization", "operations", "settings"] as const) {
    await page.locator(`.nav-item[data-view="${view}"]`).click();
    await expect(page.locator(`[data-view-panel="${view}"]`)).toHaveClass(/active/);
    await page.waitForTimeout(200);
    await expect(page).toHaveScreenshot(`legacy-${view}-desktop-light.png`, { fullPage: false });
  }
  await context.close();
});

const stateCases = [
  {
    slug: "overview",
    path: "/admin/overview",
    primary: "**/admin/api/overview/summary",
    loading: "正在加载总览",
    error: "总览数据加载失败",
    empty: "所选范围内没有账号 Token 数据",
    emptyRoutes: ["**/admin/api/overview/summary", "**/admin/api/overview/usage?*"]
  },
  {
    slug: "accounts",
    path: "/admin/accounts",
    primary: "**/admin/api/accounts",
    loading: "正在加载账号数据",
    error: "账号数据加载失败",
    empty: "还没有业务账号"
  },
  {
    slug: "users",
    path: "/admin/users",
    primary: "**/admin/api/users?*",
    loading: "正在加载用户目录",
    error: "用户目录加载失败",
    empty: "当前条件下没有用户"
  },
  {
    slug: "teams",
    path: "/admin/teams",
    primary: "**/admin/api/teams",
    loading: "正在加载团队目录",
    error: "团队目录加载失败",
    empty: "还没有团队"
  },
  {
    slug: "runtime",
    path: "/admin/runtime",
    primary: "**/admin/api/runtime/services",
    loading: "正在加载运行维护",
    error: "Docker 运行状态加载失败",
    empty: "当前 Compose 项目没有可见容器"
  },
  {
    slug: "configuration",
    path: "/admin/configuration",
    primary: "**/admin/api/settings/configuration",
    loading: "正在加载配置中心",
    error: "配置中心加载失败",
    empty: "没有匹配的配置项"
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
      await expect(page.getByLabel(stateCase.loading, { exact: true })).toBeVisible();
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
    await login(page, stateCase.path, stateCase.error);
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
  await expect(page).toHaveScreenshot("react-overview-tooltip-top10.png", { fullPage: false });
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
  await expect(page.getByRole("button", { name: "查看 API Key" })).toBeVisible();
  expect(revealRequests).toBe(0);
  await expect(page.locator("body")).not.toContainText(oldKey);

  await page.getByRole("button", { name: "查看 API Key" }).click();
  const keyInput = page.getByLabel("API Key", { exact: true });
  await expect(keyInput).toHaveValue(oldKey);
  expect(revealRequests).toBe(1);
  await page.getByRole("button", { name: "关闭并清除" }).click();
  await expect(keyInput).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(oldKey);

  await page.getByRole("button", { name: "刷新 Key" }).click();
  await page.getByRole("button", { name: "确认刷新并使旧 Key 失效" }).click();
  await expect(keyInput).toHaveValue(newKey);
  expect(rotateRequests).toBe(1);
  await expect(page.locator("body")).not.toContainText(oldKey);
  expect(await page.evaluate(([oldValue, newValue]) => {
    const storage = [window.localStorage, window.sessionStorage];
    return storage.some((entry) => Object.values(entry).some((value) => value === oldValue || value === newValue));
  }, [oldKey, newKey])).toBe(false);

  await page.getByRole("button", { name: "关闭并清除" }).click();
  await expect(keyInput).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(newKey);
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
        remaining_percent: 80
      },
      active_users_1h: 2,
      usage
    }],
    totals: usage,
    warnings: []
  };
}
