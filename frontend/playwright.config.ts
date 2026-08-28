import { defineConfig } from "@playwright/test";
import { existsSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const frontendRoot = path.dirname(fileURLToPath(import.meta.url));
const repositoryRoot = path.resolve(frontendRoot, "..");
const macOSChrome = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

export default defineConfig({
  testDir: "./e2e",
  timeout: 30_000,
  expect: {
    timeout: 10_000,
    toHaveScreenshot: {
      animations: "disabled",
      caret: "hide",
      maxDiffPixelRatio: 0.005
    }
  },
  fullyParallel: false,
  workers: 1,
  reporter: [["list"], ["html", { open: "never" }]],
  use: {
    baseURL: "http://127.0.0.1:5193",
    browserName: "chromium",
    launchOptions: existsSync(macOSChrome) ? { executablePath: macOSChrome } : {},
    locale: "zh-CN",
    timezoneId: "Asia/Shanghai",
    colorScheme: "light",
    screenshot: "only-on-failure",
    trace: "retain-on-failure"
  },
  webServer: [
    {
      command: "go run ./cmd/test-preview --address 127.0.0.1:8896 --root .",
      cwd: repositoryRoot,
      url: "http://127.0.0.1:8896/healthz",
      reuseExistingServer: false,
      timeout: 30_000
    },
    {
      command: "CPA_DEV_PROXY_TARGET=http://127.0.0.1:8896 npm run dev -- --port 5193",
      cwd: frontendRoot,
      url: "http://127.0.0.1:5193/admin/",
      reuseExistingServer: false,
      timeout: 30_000
    },
    {
      command: "CPA_DEV_PROXY_TARGET=http://127.0.0.1:8896 npm run dev:usage -- --port 5194",
      cwd: frontendRoot,
      url: "http://127.0.0.1:5194/usage/",
      reuseExistingServer: false,
      timeout: 30_000
    },
    {
      command: "CPA_DEV_PROXY_TARGET=http://127.0.0.1:8896 npm run dev:portal -- --port 5192",
      cwd: frontendRoot,
      url: "http://127.0.0.1:5192/",
      reuseExistingServer: false,
      timeout: 30_000
    }
  ]
});
