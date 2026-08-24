import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { loginPortal, portalSessionQueryKey } from "../api/portal";
import { ThemeToggle, useTheme } from "./ThemeProvider";

const loginSchema = z.object({
  email: z.string().trim().email("请输入有效的企业邮箱"),
  password: z.string().min(1, "请输入密码").max(128, "密码格式无效")
});

type LoginValues = z.infer<typeof loginSchema>;

export function UsageLoginPage({ overlay = false }: { overlay?: boolean }) {
  const { theme } = useTheme();
  const queryClient = useQueryClient();
  const form = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { email: "", password: "" }
  });
  const login = useMutation({
    gcTime: 0,
    mutationFn: () => loginPortal(form.getValues("email"), form.getValues("password")),
    onSuccess: (session) => {
      form.reset();
      login.reset();
      queryClient.setQueryData(portalSessionQueryKey, session);
    }
  });

  const formCard = (
      <form
        className={`login-card auth-card usage-login-card ${overlay ? "usage-login-card-overlay" : ""}`}
        noValidate
        onSubmit={form.handleSubmit(() => login.mutate())}
      >
        {!overlay ? <div className="login-card-toolbar">
          <a href="/" aria-label="返回 Codex CPA 首页">
            <img
              className="auth-brand-logo"
              src={`/portal/assets/codex-cpa-cluster-logo${theme === "dark" ? "-dark" : ""}.svg`}
              alt="Codex CPA Cluster"
            />
          </a>
          <ThemeToggle />
        </div> : null}
        <div className="login-card-heading">
          <span className="eyebrow">USER</span>
          <h1>登录使用中心</h1>
          <p>使用企业邮箱与个人密码登录，查看 API Key、当前 CPA 和个人 Token 用量。</p>
        </div>
          <label className="field">
            <span>用户邮箱</span>
            <input
              type="email"
              autoComplete="username"
              placeholder="name@example.com"
              aria-invalid={Boolean(form.formState.errors.email)}
              {...form.register("email")}
            />
            {form.formState.errors.email ? <small className="field-error">{form.formState.errors.email.message}</small> : null}
          </label>
          <label className="field">
            <span>密码</span>
            <input
              type="password"
              autoComplete="current-password"
              aria-invalid={Boolean(form.formState.errors.password)}
              {...form.register("password")}
            />
            {form.formState.errors.password ? <small className="field-error">{form.formState.errors.password.message}</small> : null}
          </label>
          {login.isError ? (
            <div className="inline-alert" role="alert">
              {login.error instanceof ApiError && login.error.status === 401
                ? "邮箱或密码错误，请重新确认。"
                : login.error.message}
            </div>
          ) : null}
          <button className="button button-primary button-block" type="submit" disabled={login.isPending}>
            {login.isPending ? "正在验证…" : "登录"}
          </button>
        <a className="quiet-link" href="/">返回服务入口 →</a>
      </form>
  );

  if (overlay) {
    return (
      <div className="usage-login-backdrop">
        <section className="usage-login-dialog" role="dialog" aria-modal="true" aria-label="登录使用中心">
          {formCard}
        </section>
      </div>
    );
  }
  return (
    <main className="login-layout auth-screen usage-login-layout">
      {formCard}
    </main>
  );
}
