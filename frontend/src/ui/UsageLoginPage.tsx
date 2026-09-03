import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { useEffect, useState } from "react";
import { z } from "zod";

import { ApiError } from "../api/client";
import { loginPortal, portalSessionQueryKey } from "../api/portal";
import {
  emailDomainHint,
  emailPlaceholder,
  publicSiteQueryKey,
  readPublicSiteConfiguration
} from "../api/public-site";
import { ThemeToggle, useTheme } from "./ThemeProvider";

const loginSchema = z.object({
  email: z.string().trim().email("请输入有效的企业邮箱"),
  password: z.string().min(1, "请输入密码").max(128, "密码格式无效")
});

type LoginValues = z.infer<typeof loginSchema>;

export function UsageLoginPage({ overlay = false }: { overlay?: boolean }) {
  const { theme } = useTheme();
  const queryClient = useQueryClient();
  const [retrySeconds, setRetrySeconds] = useState(0);
  const siteConfiguration = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    retry: false,
    staleTime: 60_000,
    refetchOnWindowFocus: false
  });
  const emailDomains = siteConfiguration.data?.allowed_email_domains;
  const domainHint = emailDomainHint(emailDomains);
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
  useEffect(() => {
    if (!(login.error instanceof ApiError) || login.error.status !== 429) return;
    setRetrySeconds(Math.max(1, login.error.retryAfterSeconds || 1));
  }, [login.error]);
  useEffect(() => {
    if (retrySeconds <= 0) return;
    const timer = window.setTimeout(() => setRetrySeconds((current) => Math.max(0, current - 1)), 1_000);
    return () => window.clearTimeout(timer);
  }, [retrySeconds]);

  const formCard = (
      <form
        className={`login-card auth-card usage-login-card ${overlay ? "usage-login-card-overlay" : ""}`}
        noValidate
        onSubmit={form.handleSubmit(() => {
          if (retrySeconds <= 0 && !login.isPending) login.mutate();
        })}
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
              aria-label="用户邮箱"
              autoComplete="username"
              placeholder={emailPlaceholder(emailDomains)}
              aria-invalid={Boolean(form.formState.errors.email)}
              aria-describedby={!form.formState.errors.email && domainHint ? "usage-login-email-domain-hint" : undefined}
              {...form.register("email")}
            />
            {form.formState.errors.email ? <small className="field-error">{form.formState.errors.email.message}</small> : null}
            {!form.formState.errors.email && domainHint ? <small id="usage-login-email-domain-hint" className="field-hint">{domainHint}</small> : null}
          </label>
          <label className="field">
            <span>密码</span>
            <input
              type="password"
              aria-label="密码"
              autoComplete="current-password"
              aria-invalid={Boolean(form.formState.errors.password)}
              {...form.register("password")}
            />
            {form.formState.errors.password ? <small className="field-error">{form.formState.errors.password.message}</small> : null}
          </label>
          {login.isError ? (
            <div className="inline-alert" role="alert">
              {retrySeconds > 0
                ? `登录尝试过于频繁，请 ${retrySeconds} 秒后重试`
                : login.error.message}
            </div>
          ) : null}
          <button className="button button-primary button-block" type="submit" disabled={login.isPending || retrySeconds > 0}>
            {retrySeconds > 0 ? `${retrySeconds} 秒后重试` : login.isPending ? "正在验证…" : "登录"}
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
