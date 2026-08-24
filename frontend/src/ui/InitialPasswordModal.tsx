import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, Button, Form, Input, Modal, Space, Typography } from "antd";
import { useMutation } from "@tanstack/react-query";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { saveInitialPassword } from "../api/general-settings";

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

  return (
    <Modal
      title="设置未来用户初始密码"
      open={open}
      footer={null}
      onCancel={close}
      destroyOnHidden
    >
      <Space orientation="vertical" size={16} className="portal-form-stack">
        <Typography.Paragraph type="secondary">
          只用于后续新建或重置用户。已有用户密码不会变化，密码不会在保存后返回或写入浏览器存储。
        </Typography.Paragraph>
        {mutation.isError ? (
          <Alert
            type="error"
            showIcon
            message="初始密码保存失败"
            description={mutation.error instanceof ApiError ? mutation.error.message : "请稍后重试"}
          />
        ) : null}
        <form noValidate onSubmit={form.handleSubmit(() => mutation.mutate())}>
          <Form.Item
            label="初始密码"
            htmlFor="settings-initial-password"
            validateStatus={form.formState.errors.initialPassword ? "error" : undefined}
            help={form.formState.errors.initialPassword?.message}
          >
            <Controller
              control={form.control}
              name="initialPassword"
              render={({ field }) => (
                <Input.Password id="settings-initial-password" autoComplete="new-password" {...field} />
              )}
            />
          </Form.Item>
          <Form.Item
            label="确认初始密码"
            htmlFor="settings-initial-password-confirmation"
            validateStatus={form.formState.errors.confirmation ? "error" : undefined}
            help={form.formState.errors.confirmation?.message}
          >
            <Controller
              control={form.control}
              name="confirmation"
              render={({ field }) => (
                <Input.Password id="settings-initial-password-confirmation" autoComplete="new-password" {...field} />
              )}
            />
          </Form.Item>
          <Space>
            <Button onClick={close} disabled={mutation.isPending}>取消</Button>
            <Button type="primary" htmlType="submit" loading={mutation.isPending}>安全保存</Button>
          </Space>
        </form>
      </Space>
    </Modal>
  );
}
