import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { NotificationStatus } from "../../api/notifications";
import { NotificationRuntimeStatus } from "./NotificationRuntimeStatus";

const status: NotificationStatus = {
  webhook_configured: true, webhook_url: "", worker_status: "running",
  heartbeat_at: 1_800_000_000, last_success_at: 1_800_000_000,
  last_error: "", next_schedule_at: 1_800_000_600
};

describe("NotificationRuntimeStatus", () => {
  it("separates an enabled switch and manual success from a lost heartbeat", () => {
    render(<NotificationRuntimeStatus status={{ ...status, worker_status: "heartbeat_lost" }} enabled />);
    expect(screen.getByText("已启用")).toBeInTheDocument();
    expect(screen.getByText("心跳中断")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("自动通知可能已暂停");
    expect(within(screen.getByText("下次发送").parentElement!).getByText("—")).toBeInTheDocument();
  });

  it("shows missing heartbeats and handles older backends without claiming they are live", () => {
    const { rerender } = render(<NotificationRuntimeStatus status={{ ...status, worker_status: "not_started" }} enabled />);
    expect(screen.getByText("尚无心跳")).toBeInTheDocument();
    rerender(<NotificationRuntimeStatus status={{ ...status, worker_status: undefined }} enabled />);
    expect(screen.getByText("状态未知")).toBeInTheDocument();
    expect(within(screen.getByText("下次发送").parentElement!).getByText("—")).toBeInTheDocument();
  });

  it("shows live scheduling, disabled standby and independent delivery errors", () => {
    const { rerender } = render(<NotificationRuntimeStatus status={status} enabled />);
    expect(screen.getByText("心跳正常")).toBeInTheDocument();
    expect(screen.getByText("下次发送").parentElement).not.toHaveTextContent("—");
    rerender(<NotificationRuntimeStatus status={{ ...status, last_error: "temporary failure" }} enabled={false} />);
    expect(screen.getByText("待命中")).toBeInTheDocument();
    expect(screen.getByText("已关闭")).toBeInTheDocument();
    expect(screen.getByText("最近发送错误：temporary failure")).toBeInTheDocument();
    expect(within(screen.getByText("下次发送").parentElement!).getByText("—")).toBeInTheDocument();
  });

  it("hides scheduling while a Webhook is missing or status refresh fails", () => {
    const { rerender } = render(<NotificationRuntimeStatus status={{ ...status, webhook_configured: false }} enabled />);
    expect(screen.getByRole("status")).toHaveTextContent("尚未配置 Webhook");
    rerender(<NotificationRuntimeStatus status={status} enabled unavailable />);
    expect(screen.getByText("状态未知")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("暂时无法刷新");
    expect(within(screen.getByText("下次发送").parentElement!).getByText("—")).toBeInTheDocument();
  });
});
