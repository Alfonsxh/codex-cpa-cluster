import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { login, sessionQueryKey } from "../api/session";
import { useTheme } from "./ThemeProvider";

const loginSchema = z.object({
  managementKey: z.string().trim().min(1, "请输入管理密钥")
});

type LoginValues = z.infer<typeof loginSchema>;

export function LoginPage({ notice = "" }: { notice?: string }) {
  const { theme } = useTheme();
  const queryClient = useQueryClient();
  const form = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { managementKey: "" }
  });
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: () => login(form.getValues("managementKey")),
    onSuccess: (session) => {
      form.reset();
      mutation.reset();
      queryClient.setQueryData(sessionQueryKey, session);
    }
  });

  return (
    <main className="login-layout auth-screen">
      <form
        className="login-card auth-card"
        onSubmit={form.handleSubmit(() => mutation.mutate())}
        noValidate
      >
        <div className="login-card-brand">
          <a href="/" aria-label="返回 Codex CPA 首页">
            <img
              className="auth-brand-logo"
              src={`/portal/assets/codex-cpa-cluster-logo${theme === "dark" ? "-dark" : ""}.svg`}
              alt="Codex CPA Cluster"
            />
          </a>
        </div>
        <div className="login-card-heading">
          <h1>进入管理中心</h1>
          <span className="eyebrow">CONTROL PLANE</span>
          <p>业务 CPA、用户管理、OAuth 授权和容器维护集中在一个界面。管理操作需要 CPA 管理密钥。</p>
        </div>
          {notice ? <div className="success-banner" role="status">{notice}</div> : null}
          <label className="field">
            <span>管理密钥</span>
            <input
              type="password"
              autoComplete="current-password"
              aria-invalid={Boolean(form.formState.errors.managementKey)}
              {...form.register("managementKey")}
            />
            {form.formState.errors.managementKey ? (
              <small className="field-error">{form.formState.errors.managementKey.message}</small>
            ) : null}
          </label>
          {mutation.isError ? (
            <div className="inline-alert" role="alert">
              {mutation.error instanceof ApiError && mutation.error.status === 401
                ? "管理密钥无效，请重新确认。"
                : mutation.error.message}
            </div>
          ) : null}
          <button className="button button-primary button-block" type="submit" disabled={mutation.isPending}>
            {mutation.isPending ? "正在验证…" : "验证并进入"}
          </button>
        <a className="quiet-link" href="/usage/">进入使用中心 →</a>
      </form>
    </main>
  );
}
