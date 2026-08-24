import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";

export default defineConfig(({ mode }) => {
  const proxyTarget = loadEnv(mode, ".", "CPA_").CPA_DEV_PROXY_TARGET || "http://127.0.0.1:8318";
  const proxy = () => ({ target: proxyTarget, changeOrigin: true });

  return {
    root: "portal",
    base: "/portal/",
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5175,
      strictPort: true,
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
      sourcemap: true
    }
  };
});
