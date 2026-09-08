import type { NotificationStatus } from "../../api/notifications";
import { formatSiteTimestamp } from "../site-time";
import "./notification-runtime-status.css";

export function NotificationRuntimeStatus({ status, enabled, unavailable = false }: {
  status: NotificationStatus;
  enabled: boolean;
  unavailable?: boolean;
}) {
  const worker = unavailable ? undefined : status.worker_status;
  const running = worker === "running";
  const ready = running && enabled && status.webhook_configured;
  const label = running ? (enabled ? "心跳正常" : "待命中")
    : worker === "heartbeat_lost" ? "心跳中断"
      : worker === "not_started" ? "尚无心跳" : "状态未知";
  const warning = unavailable ? "暂时无法刷新调度状态，请稍后重试。"
    : worker === "heartbeat_lost" ? "后台调度心跳已中断，自动通知可能已暂停，请检查通知服务。"
      : worker === "not_started" ? "尚未收到后台调度心跳，请检查通知服务是否启动。"
        : !worker ? "当前后端未提供调度状态，请升级后端后查看。"
          : enabled && !status.webhook_configured ? "尚未配置 Webhook，自动通知暂停。" : "";
  return <section className="notification-runtime-status" aria-label="通知运行状态">
    <div className="notification-runtime-grid">
      <div><span>通知开关</span><strong>{enabled ? "已启用" : "已关闭"}</strong></div>
      <div><span>后台调度</span><strong className={`status-chip ${running ? "success" : "warning"}`}>{label}</strong></div>
      <div><span>最近心跳</span><strong>{formatSiteTimestamp(status.heartbeat_at)}</strong></div>
      <div title="包含手动发送和自动发送"><span>最近发送成功</span><strong>{formatSiteTimestamp(status.last_success_at)}</strong></div>
      <div><span>下次发送</span><strong>{ready ? formatSiteTimestamp(status.next_schedule_at) : "—"}</strong></div>
    </div>
    {warning ? <p className="notification-runtime-warning" role="status">{warning}</p> : null}
    {status.last_error ? <p className="notification-runtime-error">最近发送错误：{status.last_error}</p> : null}
  </section>;
}
