import { DownloadOutlined } from "@ant-design/icons";
import { Alert, Button, DatePicker, Modal } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import utc from "dayjs/plugin/utc";
import timezone from "dayjs/plugin/timezone";
import { useEffect, useRef, useState } from "react";

import { exportWeeklyUsage } from "../../api/overview";
import { useSiteTimezone } from "../site-time";

dayjs.extend(utc);
dayjs.extend(timezone);

function monday(date: Dayjs) {
  return date.startOf("day").subtract((date.day() + 6) % 7, "day");
}

export function WeeklyUsageExport({ onDownloaded }: { onDownloaded: () => void }) {
  const zone = useSiteTimezone();
  const [open, setOpen] = useState(false);
  const [week, setWeek] = useState<Dayjs | null>(null);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => { request.current?.abort(); request.current = null; }, []);

  // Calendar dates are intentionally zone-free in the picker. The server alone
  // translates the selected Monday to timezone-aware interval boundaries.
  const today = dayjs(dayjs().tz(zone).format("YYYY-MM-DD"));
  const currentWeek = monday(today);
  const end = week?.add(6, "day");
  const partial = week?.isSame(currentWeek, "day") ?? false;
  const valid = Boolean(week && !week.isAfter(currentWeek, "day"));

  function close() {
    request.current?.abort();
    request.current = null;
    setPending(false);
    setOpen(false);
  }

  async function download() {
    if (!week || !valid || request.current) return;
    const controller = new AbortController();
    request.current = controller;
    setPending(true);
    setError("");
    let timedOut = false;
    const timer = window.setTimeout(() => { timedOut = true; controller.abort(); }, 30_000);
    try {
      const { blob, filename } = await exportWeeklyUsage(week.format("YYYY-MM-DD"), controller.signal);
      if (controller.signal.aborted) return;
      const url = URL.createObjectURL(blob);
      const link = document.createElement("a");
      link.href = url;
      link.download = filename;
      document.body.appendChild(link);
      try { link.click(); } finally {
        link.remove();
        window.setTimeout(() => URL.revokeObjectURL(url), 1_000);
      }
      setOpen(false);
      onDownloaded();
    } catch (cause) {
      if (request.current !== controller) return;
      if (timedOut) setError("周报下载超时，请稍后重试");
      else if (!controller.signal.aborted) setError(cause instanceof Error ? cause.message : "周报导出失败，请稍后重试");
    } finally {
      window.clearTimeout(timer);
      if (request.current === controller) {
        request.current = null;
        setPending(false);
      }
    }
  }

  return <>
    <Button icon={<DownloadOutlined />} onClick={() => {
      setWeek(currentWeek.subtract(7, "day"));
      setError("");
      setOpen(true);
    }}>导出周报</Button>
    <Modal title="导出 Token 周报" open={open} centered width={560}
      okText={pending ? "正在生成" : "下载 XLSX"} cancelText={pending ? "取消生成" : "取消"}
      confirmLoading={pending} okButtonProps={{ disabled: !valid }}
      onOk={() => void download()} onCancel={close} destroyOnHidden>
      <div className="weekly-usage-export">
        <div className="weekly-usage-export-picker">
          <label htmlFor="weekly-report-week">统计周</label>
          <DatePicker id="weekly-report-week" value={week} allowClear={false} inputReadOnly disabled={pending}
            format="YYYY-MM-DD" placeholder="选择周内任意日期" minDate={dayjs("1970-01-01")}
            disabledDate={(date) => date.isAfter(today, "day")}
            onChange={(value) => { setWeek(value ? monday(value) : null); setError(""); }} />
          <Button disabled={pending} onClick={() => setWeek(currentWeek.subtract(7, "day"))}>上周</Button>
          <Button disabled={pending} onClick={() => setWeek(currentWeek)}>本周</Button>
        </div>
        <div className="weekly-usage-export-period" aria-live="polite">
          <strong>{week?.format("YYYY-MM-DD")} — {end?.format("YYYY-MM-DD")}</strong>
          <span>{zone} · 周一至周日{partial ? " · 本周未结束" : ""}</span>
        </div>
        <p>包含周报总览、团队统计、账号统计、个人统计、每日趋势。导出全部数据，不受页面筛选和 Top10 限制。</p>
        <p>以加权 Token 为主，保留原始 Token；团队按当前归属统计。{partial ? "本周统计截至生成时间，与上周同一时段对比。" : "与前一个完整自然周对比。"}</p>
        {error ? <Alert type="error" title={error} showIcon /> : null}
      </div>
    </Modal>
  </>;
}
