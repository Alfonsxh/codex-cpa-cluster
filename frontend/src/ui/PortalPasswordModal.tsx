import { zodResolver } from "@hookform/resolvers/zod";
import { Alert, Button, Form, Input, Modal, Space, Typography } from "antd";
import { useMutation } from "@tanstack/react-query";
import { Controller, useForm } from "react-hook-form";
import { z } from "zod";

import { ApiError } from "../api/client";
import { changePortalPassword } from "../api/portal";

const passwordSchema = z.object({
  currentPassword: z.string().min(1, "请输入当前密码").max(128, "密码格式无效"),
  newPassword: z.string().min(8, "新密码至少需要 8 位").max(128, "密码格式无效"),
  confirmation: z.string().min(1, "请再次输入新密码")
}).refine((values) => values.newPassword === values.confirmation, {
  path: ["confirmation"],
  message: "两次输入的新密码不一致"
}).refine((values) => values.newPassword !== values.currentPassword, {
  path: ["newPassword"],
  message: "新密码不能与当前密码相同"
});

type PasswordValues = z.infer<typeof passwordSchema>;

export function PortalPasswordModal({
  open,
  mandatory = false,
  onClose,
  onSuccess
}: {
  open: boolean;
  mandatory?: boolean;
  onClose: () => void;
  onSuccess: () => void;
}) {
  const form = useForm<PasswordValues>({
    resolver: zodResolver(passwordSchema),
    defaultValues: { currentPassword: "", newPassword: "", confirmation: "" }
  });
  const change = useMutation({
    gcTime: 0,
    mutationFn: () => changePortalPassword(
      form.getValues("currentPassword"),
      form.getValues("newPassword")
    ),
    onSuccess: () => {
      form.reset();
      change.reset();
      onSuccess();
    }
  });
  const close = () => {
    if (mandatory || change.isPending) return;
    form.reset();
    change.reset();
    onClose();
  };

  return (
    <Modal
      className="portal-password-modal"
      title={mandatory ? "首次登录必须修改密码" : "修改个人密码"}
      open={open}
      width={620}
      closable={!mandatory}
      mask={{ closable: !mandatory }}
      keyboard={!mandatory}
      footer={null}
      onCancel={close}
      destroyOnHidden
    >
      <Space orientation="vertical" size={16} className="portal-form-stack">
        <Typography.Paragraph type="secondary">
          修改后会撤销该用户的其他浏览器会话，当前会话继续有效。
        </Typography.Paragraph>
        {change.isError ? (
          <Alert
            type="error"
            showIcon
            title="密码修改失败"
            description={change.error instanceof ApiError ? change.error.message : "请稍后重试"}
          />
        ) : null}
        <form
          className="portal-password-form"
          noValidate
          onSubmit={form.handleSubmit(() => change.mutate())}
        >
          <Form.Item
            label="当前密码"
            htmlFor="portal-current-password"
            validateStatus={form.formState.errors.currentPassword ? "error" : undefined}
            help={form.formState.errors.currentPassword?.message}
          >
            <Controller
              control={form.control}
              name="currentPassword"
              render={({ field }) => (
                <Input.Password id="portal-current-password" autoComplete="current-password" {...field} />
              )}
            />
          </Form.Item>
          <Form.Item
            label="新密码"
            htmlFor="portal-new-password"
            validateStatus={form.formState.errors.newPassword ? "error" : undefined}
            help={form.formState.errors.newPassword?.message}
          >
            <Controller
              control={form.control}
              name="newPassword"
              render={({ field }) => (
                <Input.Password id="portal-new-password" autoComplete="new-password" {...field} />
              )}
            />
          </Form.Item>
          <Form.Item
            label="确认新密码"
            htmlFor="portal-password-confirmation"
            validateStatus={form.formState.errors.confirmation ? "error" : undefined}
            help={form.formState.errors.confirmation?.message}
          >
            <Controller
              control={form.control}
              name="confirmation"
              render={({ field }) => (
                <Input.Password id="portal-password-confirmation" autoComplete="new-password" {...field} />
              )}
            />
          </Form.Item>
          <Space className="portal-form-actions">
            {!mandatory ? <Button onClick={close}>取消</Button> : null}
            <Button type="primary" htmlType="submit" loading={change.isPending}>保存新密码</Button>
          </Space>
        </form>
      </Space>
    </Modal>
  );
}
