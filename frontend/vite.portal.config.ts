import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";
import { fileURLToPath } from "node:url";

import { portalCodeSplitting } from "./vite.shared.ts";

const frontendRoot = fileURLToPath(new URL(".", import.meta.url));
const sourceRoot = fileURLToPath(new URL("./src", import.meta.url));

export default defineConfig(({ command, mode }) => {
  const proxyTarget = loadEnv(mode, ".", "CPA_").CPA_DEV_PROXY_TARGET || "http://127.0.0.1:8318";
  const proxy = () => ({ target: proxyTarget, changeOrigin: true });

  return {
    root: "portal",
    // The built assets live below /portal/, while the public application owns
    // the real browser routes / and /native/. Vite's development-only base
    // guard would otherwise reject a direct /native/ navigation before React
    // Router can handle it.
    base: command === "serve" ? "/" : "/portal/",
    cacheDir: "../node_modules/.vite-portal",
    plugins: [react()],
    resolve: { alias: { "/@cpa-src": sourceRoot } },
    server: {
      host: "127.0.0.1",
      port: 5175,
      strictPort: true,
      fs: { allow: [frontendRoot] },
      proxy: {
        "/admin/api/native-accounts": proxy(),
        "/site-config.json": proxy(),
        "/branding": proxy(),
        "/portal/assets": proxy()
      }
    },
    build: {
      outDir: "../dist/portal",
      emptyOutDir: true,
      sourcemap: true,
      rolldownOptions: { output: { codeSplitting: portalCodeSplitting } }
    }
  };
});
