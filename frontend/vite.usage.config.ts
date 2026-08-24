import react from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";

export default defineConfig(({ mode }) => {
  const proxyTarget = loadEnv(mode, ".", "CPA_").CPA_DEV_PROXY_TARGET || "http://127.0.0.1:8318";
  const proxy = () => ({ target: proxyTarget, changeOrigin: true });

  return {
    root: "usage",
    base: "/usage/",
    plugins: [react()],
    server: {
      host: "127.0.0.1",
      port: 5174,
      strictPort: true,
      proxy: {
        "^/usage/(session|me)(?:/|$)": proxy(),
        "/branding": proxy(),
        "/portal/assets": proxy()
      }
    },
    build: {
      outDir: "../dist/usage",
      emptyOutDir: true,
      sourcemap: true
    }
  };
});
