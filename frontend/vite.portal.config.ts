import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";
import { fileURLToPath } from "node:url";

import { portalCodeSplitting } from "./vite.shared.ts";

const frontendRoot = fileURLToPath(new URL(".", import.meta.url));
const sourceRoot = fileURLToPath(new URL("./src", import.meta.url));

export default defineConfig(({ mode }) => {
  const proxyTarget = loadEnv(mode, ".", "CPA_").CPA_DEV_PROXY_TARGET || "http://127.0.0.1:8318";
  const proxy = () => ({ target: proxyTarget, changeOrigin: true });

  return {
    root: "portal",
    base: "/portal/",
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
