import { Button, Input, Modal, Tooltip, type InputRef } from "antd";
import { useEffect, useId, useRef, useState } from "react";

import type { UserOneTimeKey } from "../../api/users";

export type SecretReveal = {
  kind?: "created" | "rotated" | "password-reset";
  message: string;
  keys: UserOneTimeKey[];
  password?: string;
  passwordUser?: string;
};

export function SecretRevealModal({ value, onClose }: {
  value: SecretReveal | null;
  onClose: () => void;
}) {
  if (!value) return null;

  const user = value.passwordUser || value.keys[0]?.user || "";
  const secrets = [
    ...(value.password ? [{ id: "password", label: "初始密码", value: value.password }] : []),
    ...value.keys.map((key, index) => ({
      id: `key-${index}`,
      label: value.keys.length > 1 ? `API Key ${index + 1}` : "API Key",
      value: key.key
    }))
  ];
  const kind = value.kind ?? (value.password ? (value.keys.length ? "created" : "password-reset") : "rotated");
  const resultText = {
    created: "用户已创建",
    rotated: "API Key 已更新",
    "password-reset": "密码已重置"
  }[kind] ?? "凭据已生成";
  const hint = value.password
    ? (value.keys.length ? "请保存 API Key，首次登录需修改密码。" : "下次登录需修改密码。")
    : "请保存本次生成的 API Key。";
  const allSecrets = [
    ...(user ? [`用户：${user}`] : []),
    ...secrets.map((secret) => `${secret.label}：${secret.value}`)
  ].join("\n");

  return (
    <Modal
      className="legacy-secret-modal"
      title={(
        <div className="secret-dialog-title">
          <strong>用户凭据</strong>
          <span aria-hidden="true">ONE-TIME SECRET</span>
        </div>
      )}
      open
      width={600}
      centered
      closeIcon={<span className="legacy-dialog-close" aria-hidden="true">×</span>}
      transitionName=""
      maskTransitionName=""
      onCancel={onClose}
      destroyOnHidden
      mask={{ closable: false }}
      footer={[
        <SecretCopyButton key="copy-all" text={allSecrets} label="复制全部" />,
        <Button key="saved" type="primary" onClick={onClose}>我已保存</Button>
      ]}
    >
      <div className="secret-user-summary">
        <span className="secret-user-result" role="status">{resultText}</span>
        {user ? <strong className="secret-user-email" title={user}>{user}</strong> : null}
      </div>
      <div className="secret-fields">
        {secrets.map((secret) => <SecretField key={secret.id} label={secret.label} value={secret.value} />)}
      </div>
      <p className="secret-save-hint" role="note">{hint}</p>
    </Modal>
  );
}

function SecretField({ label, value }: { label: string; value: string }) {
  const id = useId();
  const input = useRef<InputRef>(null);
  const hovered = useRef(false);
  const [shownValue, setShownValue] = useState<string | null>(null);
  const visible = shownValue === value;

  useEffect(() => {
    // Scope pointer-directed select-all to this mounted credential field. Hover alone
    // must not move focus, and keyboard-only selection keeps the native input path.
    const selectHovered = (event: KeyboardEvent) => {
      if (!hovered.current || event.defaultPrevented || event.isComposing || event.altKey
        || !(event.ctrlKey || event.metaKey) || event.key.toLowerCase() !== "a") return;
      event.preventDefault();
      event.stopPropagation();
      input.current?.focus({ preventScroll: true });
      input.current?.select();
    };
    document.addEventListener("keydown", selectHovered, true);
    return () => document.removeEventListener("keydown", selectHovered, true);
  }, []);

  return (
    <div className="secret-field" role="group" aria-label={label}>
      <label className="secret-field-label" htmlFor={id}>{label}</label>
      <div
        className="secret-field-control"
        onMouseEnter={() => { hovered.current = true; }}
        onMouseLeave={() => { hovered.current = false; }}
      >
        <Input.Password
          ref={input}
          id={id}
          className="secret-field-input"
          value={value}
          readOnly
          autoComplete="off"
          spellCheck={false}
          visibilityToggle={{ visible, onVisibleChange: (next) => setShownValue(next ? value : null) }}
          onKeyDown={(event) => {
            if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "a") {
              event.preventDefault();
              event.stopPropagation();
              event.currentTarget.select();
            }
          }}
        />
      </div>
      <SecretCopyButton text={value} label="复制" accessibleLabel={`复制${label}`} />
    </div>
  );
}

function SecretCopyButton({ text, label, accessibleLabel = label }: {
  text: string;
  label: string;
  accessibleLabel?: string;
}) {
  const [feedback, setFeedback] = useState<{ text: string; status: "copying" | "copied" | "failed" } | null>(null);
  const request = useRef(0);
  const timer = useRef<number | undefined>(undefined);
  const status = feedback?.text === text ? feedback.status : undefined;

  useEffect(() => () => {
    request.current += 1;
    window.clearTimeout(timer.current);
  }, []);

  const copy = async () => {
    const currentRequest = ++request.current;
    window.clearTimeout(timer.current);
    setFeedback({ text, status: "copying" });
    let nextStatus: "copied" | "failed" = "copied";
    try {
      if (!navigator.clipboard?.writeText) throw new Error("Clipboard unavailable");
      await navigator.clipboard.writeText(text);
    } catch {
      nextStatus = "failed";
    }
    if (currentRequest !== request.current) return;
    setFeedback({ text, status: nextStatus });
    timer.current = window.setTimeout(() => {
      if (currentRequest === request.current) setFeedback(null);
    }, 2_000);
  };
  const feedbackLabel = status === "copied" ? "已复制" : status === "failed" ? "复制失败" : "";

  return (
    <Tooltip title={status === "failed" ? "请显示凭据后手动复制" : "复制完整内容"}>
      <Button
        className="secret-copy-action"
        size="small"
        aria-label={feedbackLabel ? `${accessibleLabel}，${feedbackLabel}` : accessibleLabel}
        loading={status === "copying"}
        danger={status === "failed"}
        onClick={() => void copy()}
      ><span aria-live="polite">{feedbackLabel || label}</span></Button>
    </Tooltip>
  );
}
