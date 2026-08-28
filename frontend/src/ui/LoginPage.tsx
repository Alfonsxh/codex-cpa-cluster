import { zodResolver } from "@hookform/resolvers/zod";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { login, sessionQueryKey } from "../api/session";
import { useTheme } from "./ThemeProvider";

const loginSchema = z.object({
  managementKey: z.string().trim().min(1, "请输入管理密钥")
});

type LoginValues = z.infer<typeof loginSchema>;

export function LoginPage({ notice = "", onAuthenticated }: { notice?: string; onAuthenticated?: () => void }) {
  const { theme } = useTheme();
  const queryClient = useQueryClient();
  const [passwordVisible, setPasswordVisible] = useState(false);
  const [noticeDismissed, setNoticeDismissed] = useState(false);
  const form = useForm<LoginValues>({
    resolver: zodResolver(loginSchema),
    defaultValues: { managementKey: "" }
  });
  useEffect(() => setNoticeDismissed(false), [notice]);
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: () => login(form.getValues("managementKey")),
    onSuccess: (session) => {
      form.reset({ managementKey: "" });
      setPasswordVisible(false);
      mutation.reset();
      queryClient.setQueryData(sessionQueryKey, session);
      onAuthenticated?.();
    },
    onError: (error) => {
      if (error instanceof ApiError && error.status === 401) {
        form.reset({ managementKey: "" });
        setPasswordVisible(false);
      }
    }
  });
  const validationError = form.formState.errors.managementKey?.message;
  const requestError = mutation.isError
    ? mutation.error instanceof ApiError && mutation.error.status === 401
      ? "管理密钥无效"
      : mutation.error.message
    : "";
  const errorMessage = requestError || validationError || (!noticeDismissed ? notice : "");

  return (
    <main className="login-layout auth-screen admin-login-layout">
      <section className="login-card auth-card">
        <img
          className="auth-brand-logo"
          src={`/portal/assets/codex-cpa-cluster-logo${theme === "dark" ? "-dark" : ""}.svg`}
          alt="Codex CPA Cluster"
        />
        <h1>进入管理中心</h1>
        <p className="eyebrow">CONTROL PLANE</p>
        <p className="auth-copy">业务 CPA、用户管理、OAuth 授权和容器维护集中在一个界面。管理操作需要 CPA 管理密钥。</p>
        <form
          className="auth-form"
          onSubmit={form.handleSubmit(() => {
            setNoticeDismissed(true);
            if (!mutation.isPending) mutation.mutate();
          })}
        >
          <label htmlFor="management-key">管理密钥</label>
          <div className="password-row">
            <span className="password-input">
              <input
                id="management-key"
                type={passwordVisible ? "text" : "password"}
                autoComplete="current-password"
                autoFocus
                required
                aria-invalid={Boolean(validationError)}
                {...form.register("managementKey")}
              />
              <button
                className="password-visibility-toggle"
                type="button"
                aria-controls="management-key"
                aria-label={passwordVisible ? "隐藏密码" : "显示密码"}
                aria-pressed={passwordVisible}
                title={passwordVisible ? "隐藏密码" : "显示密码"}
                onClick={() => setPasswordVisible((visible) => !visible)}
              >
                <svg className="password-eye-show" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <path d="M2.5 12s3.5-6 9.5-6 9.5 6 9.5 6-3.5 6-9.5 6-9.5-6-9.5-6Z" />
                  <circle cx="12" cy="12" r="2.75" />
                </svg>
                <svg className="password-eye-hide" viewBox="0 0 24 24" fill="none" aria-hidden="true">
                  <path d="M3 3l18 18M10.6 6.2A10.7 10.7 0 0 1 12 6c6 0 9.5 6 9.5 6a17.6 17.6 0 0 1-2.5 3.2M6.2 6.2C3.8 8 2.5 12 2.5 12s3.5 6 9.5 6a9.9 9.9 0 0 0 3.2-.5M9.9 9.9a3 3 0 0 0 4.2 4.2" />
                </svg>
              </button>
            </span>
            <button className="button button-primary primary" type="submit" disabled={mutation.isPending}>
              验证并进入
            </button>
          </div>
          <p className="form-error" role="alert">{errorMessage}</p>
        </form>
        <a className="quiet-link" href="/usage/">进入使用中心 →</a>
      </section>
    </main>
  );
}
