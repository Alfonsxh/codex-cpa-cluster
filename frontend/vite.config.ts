import react from "@vitejs/plugin-react";
import { loadEnv } from "vite";
import { defineConfig } from "vitest/config";

export default defineConfig(({ mode }) => {
  const proxyTarget = loadEnv(mode, ".", "CPA_").CPA_DEV_PROXY_TARGET || "http://127.0.0.1:8318";
  const proxy = () => ({ target: proxyTarget, changeOrigin: true });

  return {
    base: "/admin/",
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5173,
      strictPort: true,
      proxy: {
        "/admin/api": proxy(),
        "/branding": proxy(),
        "/portal/assets": proxy(),
        "/site-config.json": proxy()
      }
    },
    build: {
      outDir: "dist/admin",
      emptyOutDir: true,
      sourcemap: true
    },
    test: {
      environment: "jsdom",
      // Ant Design mounts portals and runs layout effects for most admin pages.
      // Running every page file in parallel makes the shared local validation
      // gate CPU-bound and causes otherwise healthy interaction tests to exceed
      // Vitest's per-test timeout. Keep the suite deterministic: each file still
      // exercises its own async behavior, while files run one at a time.
      fileParallelism: false,
      setupFiles: "./src/test/setup.ts",
      restoreMocks: true
    }
  };
});
