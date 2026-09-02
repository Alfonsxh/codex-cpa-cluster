import { zodResolver } from "@hookform/resolvers/zod";
import { Button, Modal } from "antd";
import { useMutation } from "@tanstack/react-query";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { saveInitialPassword } from "../api/general-settings";
import { LegacyPasswordInput } from "./components/LegacyPasswordInput";

const initialPasswordSchema = z.object({
  initialPassword: z.string().min(8, "初始密码至少需要 8 位").max(128, "初始密码不能超过 128 位"),
  confirmation: z.string().min(1, "请再次输入初始密码")
}).refine((values) => values.initialPassword === values.confirmation, {
  path: ["confirmation"],
  message: "两次输入的初始密码不一致"
}).refine((values) => values.initialPassword !== "123456", {
  path: ["initialPassword"],
  message: "不能使用已停用的历史默认密码"
});

type InitialPasswordValues = z.infer<typeof initialPasswordSchema>;

export function InitialPasswordModal({
  open,
  csrfToken,
  onClose,
  onSuccess
}: {
  open: boolean;
  csrfToken: string;
  onClose: () => void;
  onSuccess: (message: string) => void;
}) {
  const form = useForm<InitialPasswordValues>({
    resolver: zodResolver(initialPasswordSchema),
    defaultValues: { initialPassword: "", confirmation: "" }
  });
  const mutation = useMutation({
    gcTime: 0,
    mutationFn: () => saveInitialPassword(
      form.getValues("initialPassword"),
      form.getValues("confirmation"),
      csrfToken
    ),
    onSuccess: (result) => {
      form.reset();
      mutation.reset();
      onSuccess(result.message);
    }
  });
  const close = () => {
    if (mutation.isPending) return;
    form.reset();
    mutation.reset();
    onClose();
  };
  const error = form.formState.errors.initialPassword?.message
    ?? form.formState.errors.confirmation?.message
    ?? (mutation.isError
      ? mutation.error instanceof ApiError ? mutation.error.message : "请稍后重试"
      : "");

  return (
    <Modal
      className="legacy-account-editor-modal legacy-settings-form-modal"
      title={<div className="legacy-dialog-title"><strong>设置用户初始密码</strong><span>ENCRYPTED USER SECRET</span></div>}
      open={open}
      width={560}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      afterOpenChange={(visible) => { if (visible) form.setFocus("initialPassword"); }}
      onCancel={close}
      destroyOnHidden
      footer={[
        <Button key="cancel" className="legacy-modal-ghost" tabIndex={-1} disabled={mutation.isPending} onClick={close}>取消</Button>,
        <Button
          key="submit"
          type="primary"
          htmlType="submit"
          form="settings-initial-password-form"
          disabled={mutation.isPending}
        >{mutation.isPending ? "正在保存…" : "安全保存"}</Button>
      ]}
    >
      <div className="warning-banner">仅影响后续新建或重置的用户。密码以 AES-GCM 加密保存在控制面数据库中，页面不会读取或回显现值。</div>
      <form id="settings-initial-password-form" noValidate onSubmit={form.handleSubmit(() => mutation.mutate())}>
        <div className="field">
          <label htmlFor="settings-initial-password">新初始密码</label>
          <Controller control={form.control} name="initialPassword" render={({ field }) => (
            <LegacyPasswordInput id="settings-initial-password" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={8} maxLength={128} onValueChange={field.onChange} />
          )} />
        </div>
        <div className="field account-email-field">
          <label htmlFor="settings-initial-password-confirmation">再次输入</label>
          <Controller control={form.control} name="confirmation" render={({ field }) => (
            <LegacyPasswordInput id="settings-initial-password-confirmation" value={field.value} name={field.name} inputRef={field.ref} onBlur={field.onBlur} minLength={8} maxLength={128} onValueChange={field.onChange} />
          )} />
        </div>
        <p className="form-error" role="alert">{error}</p>
      </form>
    </Modal>
  );
}
