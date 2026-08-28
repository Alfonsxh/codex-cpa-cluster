import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";
import { fileURLToPath } from "node:url";

import { usageCodeSplitting } from "./vite.shared.ts";

const frontendRoot = fileURLToPath(new URL(".", import.meta.url));
const sourceRoot = fileURLToPath(new URL("./src", import.meta.url));

export default defineConfig(({ mode }) => {
  const proxyTarget = loadEnv(mode, ".", "CPA_").CPA_DEV_PROXY_TARGET || "http://127.0.0.1:8318";
  const proxy = () => ({ target: proxyTarget, changeOrigin: true });

  return {
    root: "usage",
    base: "/usage/",
    cacheDir: "../node_modules/.vite-usage",
    plugins: [react()],
    resolve: { alias: { "/@cpa-src": sourceRoot } },
    server: {
      host: "127.0.0.1",
      port: 5174,
      strictPort: true,
      fs: { allow: [frontendRoot] },
      proxy: {
        "^/usage/(session|me)(?:/|$)": proxy(),
        "/site-config.json": proxy(),
        "/branding": proxy(),
        "/portal/assets": proxy()
      }
    },
    build: {
      outDir: "../dist/usage",
      emptyOutDir: true,
      sourcemap: true,
      rolldownOptions: { output: { codeSplitting: usageCodeSplitting } }
    }
  };
});
