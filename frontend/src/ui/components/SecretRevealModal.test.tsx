import { act, cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { SecretRevealModal, type SecretReveal } from "./SecretRevealModal";

const created: SecretReveal = {
  kind: "created",
  message: "旧版冗长提示",
  passwordUser: "new@example.com",
  password: "test-initial-password",
  keys: [{
    account: "alpha",
    account_email: "alpha@example.com",
    user: "new@example.com",
    label: "new@example.com:alpha",
    status: "active",
    created_at: 1,
    updated_at: 1,
    preview: "test-…key",
    key: "test-one-time-api-key"
  }]
};
const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
let writeText = vi.fn<(text: string) => Promise<void>>();

function credentialInput(label: string) {
  return within(screen.getByRole("group", { name: label })).getByLabelText<HTMLInputElement>(label);
}

function eyeButton(label: string, pressed = false) {
  return within(screen.getByRole("group", { name: label })).getByRole("button", { pressed });
}

beforeEach(() => {
  writeText = vi.fn<(text: string) => Promise<void>>().mockResolvedValue(undefined);
  Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  if (clipboardDescriptor) Object.defineProperty(navigator, "clipboard", clipboardDescriptor);
  else Reflect.deleteProperty(navigator, "clipboard");
});

describe("SecretRevealModal", () => {
  it("shows the bilingual heading, one user summary and a compact save notice with two masked credentials", () => {
    render(<SecretRevealModal value={created} onClose={vi.fn()} />);

    const dialog = screen.getByRole("dialog", { name: "用户凭据" });
    const body = dialog.querySelector<HTMLElement>(".ant-modal-body")!;
    expect(within(body).getByRole("status")).toHaveTextContent("用户已创建");
    const email = within(body).getByText("new@example.com");
    expect(email).toHaveClass("secret-user-email");
    expect(email).toHaveAttribute("title", "new@example.com");
    expect(email.parentElement).toBe(within(body).getByRole("status").parentElement);
    expect(screen.getAllByText("new@example.com")).toHaveLength(1);
    expect(screen.getAllByRole("group").map((group) => group.getAttribute("aria-label")))
      .toEqual(["初始密码", "API Key"]);
    expect(screen.getByText("ONE-TIME SECRET").closest(".ant-modal-header")).not.toBeNull();
    expect(within(body).getByRole("note")).toHaveClass("secret-save-hint");
    expect(within(body).getByRole("note")).toHaveTextContent("请保存 API Key，首次登录需修改密码。");
    expect(screen.queryByText(created.message)).not.toBeInTheDocument();
    expect(screen.queryByText("alpha")).not.toBeInTheDocument();
    expect(document.body.textContent).not.toContain(created.password);
    expect(document.body.textContent).not.toContain(created.keys[0].key);

    expect(credentialInput("初始密码")).toHaveAttribute("type", "password");
    expect(credentialInput("API Key")).toHaveAttribute("type", "password");
    fireEvent.click(eyeButton("初始密码"));
    expect(credentialInput("初始密码")).toHaveAttribute("type", "text");
    expect(credentialInput("初始密码")).toHaveValue(created.password);
    expect(credentialInput("API Key")).toHaveAttribute("type", "password");
    fireEvent.click(eyeButton("初始密码", true));
    expect(credentialInput("初始密码")).toHaveAttribute("type", "password");
  });

  it("keeps the title and infers the result when existing state has no kind", () => {
    const view = render(<SecretRevealModal value={{ ...created, kind: undefined }} onClose={vi.fn()} />);
    expect(screen.getByRole("dialog", { name: "用户凭据" })).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("用户已创建");
    view.rerender(<SecretRevealModal value={{ ...created, kind: undefined, keys: [] }} onClose={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent("密码已重置");
    view.rerender(<SecretRevealModal value={{ ...created, kind: undefined, password: undefined }} onClose={vi.fn()} />);
    expect(screen.getByRole("status")).toHaveTextContent("API Key 已更新");
    expect(screen.getByRole("dialog", { name: "用户凭据" })).toBeInTheDocument();
  });

  it.each(["Control", "Meta"])("selects only the focused single-line field with %s+A", (modifier) => {
    const onPageKeyDown = vi.fn();
    render(<div onKeyDown={onPageKeyDown}><SecretRevealModal value={created} onClose={vi.fn()} /></div>);
    const password = credentialInput("初始密码");
    const key = credentialInput("API Key");
    expect(key.tagName).toBe("INPUT");
    expect(key).toHaveAttribute("readonly");
    password.setSelectionRange(1, 3);
    key.focus();
    key.setSelectionRange(2, 4);
    const allowedDefault = fireEvent.keyDown(key, { key: "a", ctrlKey: modifier === "Control", metaKey: modifier === "Meta" });
    expect(allowedDefault).toBe(false);
    expect(key.selectionStart).toBe(0);
    expect(key.selectionEnd).toBe(created.keys[0].key.length);
    expect(password.selectionStart).toBe(1);
    expect(password.selectionEnd).toBe(3);
    expect(onPageKeyDown).not.toHaveBeenCalled();
    expect(key).toHaveAttribute("type", "password");
  });

  it.each(["Control", "Meta"])("selects the hovered field with %s+A without moving focus on hover", (modifier) => {
    render(<SecretRevealModal value={created} onClose={vi.fn()} />);
    const password = credentialInput("初始密码");
    const key = credentialInput("API Key");
    fireEvent.click(eyeButton("API Key"));
    password.focus();
    password.setSelectionRange(1, 3);
    fireEvent.mouseEnter(key.closest(".secret-field-control")!);
    expect(password).toHaveFocus();
    expect(fireEvent.keyDown(password, { key: "a", ctrlKey: modifier === "Control", metaKey: modifier === "Meta" })).toBe(false);
    expect(key).toHaveFocus();
    expect(key.selectionStart).toBe(0);
    expect(key.selectionEnd).toBe(created.keys[0].key.length);
    expect(password.selectionStart).toBe(1);
    expect(password.selectionEnd).toBe(3);
  });

  it("does not capture other shortcuts, pointer exit or keys after closing", () => {
    const view = render(<SecretRevealModal value={created} onClose={vi.fn()} />);
    const key = credentialInput("API Key");
    const control = key.closest(".secret-field-control")!;
    const saved = screen.getByRole("button", { name: "我已保存" });
    saved.focus();
    fireEvent.mouseEnter(control);
    expect(fireEvent.keyDown(saved, { key: "a" })).toBe(true);
    expect(fireEvent.keyDown(saved, { key: "a", ctrlKey: true, altKey: true })).toBe(true);
    expect(fireEvent.keyDown(saved, { key: "a", ctrlKey: true, isComposing: true })).toBe(true);
    expect(fireEvent.keyDown(saved, { key: "c", metaKey: true })).toBe(true);
    expect(saved).toHaveFocus();
    fireEvent.mouseLeave(control);
    expect(fireEvent.keyDown(saved, { key: "a", ctrlKey: true })).toBe(true);
    expect(saved).toHaveFocus();
    fireEvent.mouseEnter(control);
    view.rerender(<SecretRevealModal value={null} onClose={vi.fn()} />);
    expect(fireEvent.keyDown(document.body, { key: "a", ctrlKey: true })).toBe(true);
  });

  it("copies the full masked credential repeatedly and reports feedback on that button only", async () => {
    render(<SecretRevealModal value={created} onClose={vi.fn()} />);
    const group = screen.getByRole("group", { name: "API Key" });
    const copy = within(group).getByRole("button", { name: "复制API Key" });

    fireEvent.click(copy);
    await waitFor(() => {
      expect(copy).toHaveTextContent("已复制");
      expect(copy).not.toHaveClass("ant-btn-loading");
    });
    fireEvent.click(copy);
    await waitFor(() => expect(writeText).toHaveBeenCalledTimes(2));
    expect(writeText).toHaveBeenNthCalledWith(1, created.keys[0].key);
    expect(writeText).toHaveBeenNthCalledWith(2, created.keys[0].key);
    expect(screen.getByRole("button", { name: "复制初始密码" })).toHaveTextContent(/复\s*制/);
    expect(credentialInput("API Key")).toHaveAttribute("type", "password");
  });

  it("restarts the feedback timeout after another copy", async () => {
    vi.useFakeTimers();
    render(<SecretRevealModal value={created} onClose={vi.fn()} />);
    const copy = screen.getByRole("button", { name: "复制初始密码" });

    await act(async () => { fireEvent.click(copy); });
    expect(copy).toHaveTextContent("已复制");
    act(() => vi.advanceTimersByTime(1_500));
    await act(async () => { fireEvent.click(copy); });
    act(() => vi.advanceTimersByTime(600));
    expect(copy).toHaveTextContent("已复制");
    act(() => vi.advanceTimersByTime(1_400));
    expect(copy).toHaveTextContent(/复\s*制/);
    expect(copy).not.toHaveTextContent("已复制");
    expect(writeText).toHaveBeenCalledTimes(2);
  });

  it("copies all credentials as labeled plain text without closing or revealing them", async () => {
    const onClose = vi.fn();
    render(<SecretRevealModal value={created} onClose={onClose} />);
    fireEvent.click(screen.getByRole("button", { name: "复制全部" }));

    await waitFor(() => expect(screen.getByRole("button", { name: "复制全部，已复制" })).toBeInTheDocument());
    expect(writeText).toHaveBeenCalledWith([
      "用户：new@example.com",
      `初始密码：${created.password}`,
      `API Key：${created.keys[0].key}`
    ].join("\n"));
    expect(onClose).not.toHaveBeenCalled();
    expect(credentialInput("API Key")).toHaveAttribute("type", "password");
    fireEvent.click(screen.getByRole("button", { name: "我已保存" }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("reports a rejected copy as a failure and allows retry", async () => {
    writeText.mockRejectedValueOnce(new Error("Permission denied"));
    render(<SecretRevealModal value={created} onClose={vi.fn()} />);
    const copy = screen.getByRole("button", { name: "复制API Key" });
    fireEvent.click(copy);
    await waitFor(() => {
      expect(copy).toHaveTextContent("复制失败");
      expect(copy).not.toHaveClass("ant-btn-loading");
    });
    expect(copy).not.toHaveTextContent("已复制");
    fireEvent.click(copy);
    await waitFor(() => expect(copy).toHaveTextContent("已复制"));
    expect(writeText).toHaveBeenCalledTimes(2);
  });

  it("does not report success without clipboard support and leaves manual reveal available", async () => {
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    render(<SecretRevealModal value={created} onClose={vi.fn()} />);
    const copy = screen.getByRole("button", { name: "复制API Key" });
    fireEvent.click(copy);
    await waitFor(() => expect(copy).toHaveTextContent("复制失败"));
    fireEvent.click(eyeButton("API Key"));
    expect(credentialInput("API Key")).toHaveAttribute("type", "text");
    expect(credentialInput("API Key")).toHaveValue(created.keys[0].key);
  });

  it("clears disclosure and pending copy feedback when closed and reopened", async () => {
    let finishCopy: (() => void) | undefined;
    writeText.mockImplementationOnce(() => new Promise<void>((resolve) => { finishCopy = resolve; }));
    const view = render(<SecretRevealModal value={created} onClose={vi.fn()} />);
    fireEvent.click(eyeButton("API Key"));
    fireEvent.click(screen.getByRole("button", { name: "复制API Key" }));
    view.rerender(<SecretRevealModal value={null} onClose={vi.fn()} />);
    expect(screen.queryByRole("dialog")).not.toBeInTheDocument();

    const rotated: SecretReveal = { ...created, kind: "rotated", password: undefined };
    view.rerender(<SecretRevealModal value={rotated} onClose={vi.fn()} />);
    await act(async () => { finishCopy?.(); });
    expect(screen.getByText("API Key 已更新")).toBeInTheDocument();
    expect(credentialInput("API Key")).toHaveAttribute("type", "password");
    expect(screen.getByRole("button", { name: "复制API Key" })).not.toHaveTextContent("已复制");
    expect(screen.queryByRole("group", { name: "初始密码" })).not.toBeInTheDocument();
  });

  it("uses the password reset result and does not show an unrelated API Key field", () => {
    render(<SecretRevealModal value={{ ...created, kind: "password-reset", keys: [] }} onClose={vi.fn()} />);
    expect(screen.getByRole("dialog", { name: "用户凭据" })).toBeInTheDocument();
    expect(screen.getByText("密码已重置")).toBeInTheDocument();
    expect(screen.getByText("下次登录需修改密码。")).toBeInTheDocument();
    expect(screen.getAllByRole("group")).toHaveLength(1);
    expect(screen.queryByRole("group", { name: "API Key" })).not.toBeInTheDocument();
  });

  it("keeps every returned API Key when more than one is supplied", async () => {
    const second = { ...created.keys[0], account: "beta", key: "test-second-api-key" };
    render(<SecretRevealModal value={{ ...created, keys: [...created.keys, second] }} onClose={vi.fn()} />);
    expect(screen.getByRole("group", { name: "API Key 1" })).toBeInTheDocument();
    expect(screen.getByRole("group", { name: "API Key 2" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "复制全部" }));
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(expect.stringContaining(`API Key 2：${second.key}`)));
    expect(writeText).toHaveBeenCalledWith(expect.stringContaining(`API Key 1：${created.keys[0].key}`));
  });
});
