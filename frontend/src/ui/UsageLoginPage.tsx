import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useForm } from "react-hook-form";
import { useEffect, useMemo, useState } from "react";
import { z } from "zod";

import { ApiError } from "../api/client";
import { loginPortal, portalSessionQueryKey } from "../api/portal";
import { applicationHref } from "../application-links";
import {
  normalizedEmailDomains,
  publicSiteQueryKey,
  readPublicSiteConfiguration
} from "../api/public-site";
import { ThemeToggle, useTheme } from "./ThemeProvider";
import { LegacyEnhancedSelect } from "./components/LegacyEnhancedSelect";

const loginSchema = z.object({
  email: z.string().trim().min(1, "请输入邮箱用户名"),
  password: z.string().min(1, "请输入密码").max(128, "密码格式无效")
});

type LoginValues = z.infer<typeof loginSchema>;

export function UsageLoginPage({ overlay = false }: { overlay?: boolean }) {
  const { theme } = useTheme();
  const queryClient = useQueryClient();
  const [retrySeconds, setRetrySeconds] = useState(0);
  const [emailDomain, setEmailDomain] = useState("");
  const siteConfiguration = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    retry: false,
    staleTime: 60_000,
    refetchOnWindowFocus: false
  });
  const emailDomains = useMemo(() => normalizedEmailDomains(siteConfiguration.data?.allowed_email_domains), [siteConfiguration.data?.allowed_email_domains]);
  const selectedDomain = emailDomains.includes(emailDomain) ? emailDomain : emailDomains[0] ?? "";
  const domainsLoading = siteConfiguration.isPending || siteConfiguration.isFetching;
  const domainsReady = !domainsLoading && !siteConfiguration.isError && Boolean(selectedDomain);
  const form = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { email: "", password: "" }
  });
  const login = useMutation({
    gcTime: 0,
    mutationFn: (credentials: LoginValues) => loginPortal(credentials.email, credentials.password),
    onSuccess: (session) => {
      form.reset();
      setEmailDomain("");
      login.reset();
      queryClient.setQueryData(portalSessionQueryKey, session);
    }
  });
  const emailInput = form.register("email");
  const parseFullEmail = (value: string) => {
    const parts = value.trim().split("@");
    if (parts.length !== 2 || !emailDomains.includes(parts[1].toLowerCase())) return null;
    return { localPart: parts[0], domain: parts[1].toLowerCase() };
  };
  const acceptFullEmail = (value: string) => {
    const parsed = parseFullEmail(value);
    if (!parsed) return false;
    form.setValue("email", parsed.localPart, { shouldDirty: true });
    form.clearErrors("email");
    setEmailDomain(parsed.domain);
    return true;
  };
  const submit = (values: LoginValues) => {
    if (retrySeconds > 0 || login.isPending || !domainsReady) return;
    const parsed = parseFullEmail(values.email);
    if (values.email.includes("@") && !parsed) {
      form.setError("email", { type: "validate", message: "邮箱后缀不匹配，请仅输入用户名并选择已配置的后缀。" }, { shouldFocus: true });
      return;
    }
    const address = `${parsed?.localPart ?? values.email}@${parsed?.domain ?? selectedDomain}`;
    if (!z.string().email().safeParse(address).success) {
      form.setError("email", { type: "validate", message: "请输入有效的企业邮箱" }, { shouldFocus: true });
      return;
    }
    login.mutate({ email: address, password: values.password });
  };
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
        onSubmit={form.handleSubmit(submit)}
      >
        {!overlay ? <div className="login-card-toolbar">
          <a href={applicationHref("portal")} aria-label="返回 Codex CPA 首页">
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
          <div className="field">
            <span id="usage-login-email-label">用户邮箱</span>
            <div className="usage-login-email-fields" role="group" aria-labelledby="usage-login-email-label">
              <input
                type="text"
                inputMode="email"
                aria-label="邮箱用户名"
                autoComplete="username"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                placeholder="输入用户名"
                disabled={login.isPending}
                aria-invalid={Boolean(form.formState.errors.email)}
                aria-describedby={form.formState.errors.email ? "usage-login-email-error" : undefined}
                {...emailInput}
                onChange={(event) => {
                  void emailInput.onChange(event);
                  form.clearErrors("email");
                }}
                onBlur={(event) => {
                  void emailInput.onBlur(event);
                  acceptFullEmail(event.target.value);
                }}
                onPaste={(event) => {
                  if (acceptFullEmail(event.clipboardData.getData("text"))) event.preventDefault();
                }}
              />
              <LegacyEnhancedSelect
                id="usage-login-email-domain"
                label="邮箱后缀"
                value={selectedDomain}
                options={emailDomains.length
                  ? emailDomains.map((domain) => ({ value: domain, label: `@${domain}` }))
                  : [{ value: "", label: domainsLoading ? "正在加载后缀…" : "暂无可用后缀" }]}
                disabled={login.isPending || !domainsReady}
                onChange={(domain) => {
                  const parts = form.getValues("email").trim().split("@");
                  if (parts.length === 2) form.setValue("email", parts[0], { shouldDirty: true });
                  setEmailDomain(domain);
                  form.clearErrors("email");
                }}
              />
            </div>
            {form.formState.errors.email ? <small className="field-error" id="usage-login-email-error" role="alert">{form.formState.errors.email.message}</small> : null}
            {domainsLoading ? <small className="field-hint" role="status">正在读取企业邮箱后缀…</small>
              : siteConfiguration.isError ? <small className="field-error" role="alert">邮箱后缀加载失败。<button className="usage-email-retry" type="button" onClick={() => { void siteConfiguration.refetch(); }}>重试</button></small>
                : !emailDomains.length ? <small className="field-error" role="alert">尚未配置企业邮箱后缀，请联系管理员设置。</small>
                  : null}
          </div>
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
          <button className="button button-primary button-block" type="submit" disabled={login.isPending || retrySeconds > 0 || !domainsReady}>
            {retrySeconds > 0 ? `${retrySeconds} 秒后重试` : login.isPending ? "正在验证…" : "登录"}
          </button>
        <a className="quiet-link" href={applicationHref("portal")}>返回服务入口 →</a>
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
