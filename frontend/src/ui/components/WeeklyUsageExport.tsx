import { DownloadOutlined, FileExcelOutlined } from "@ant-design/icons";
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
  const [withUnits, setWithUnits] = useState(true);
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);
  const request = useRef<AbortController | null>(null);
  useEffect(() => () => { request.current?.abort(); request.current = null; }, []);

  // Calendar dates are intentionally zone-free in the picker. The server alone
  // translates the selected Monday to timezone-aware interval boundaries.
  const today = dayjs(dayjs().tz(zone).format("YYYY-MM-DD"));
  const currentWeek = monday(today);
  const previousWeek = currentWeek.subtract(7, "day");
  const end = week?.add(6, "day").endOf("day");
  const partial = week?.isSame(currentWeek, "day") ?? false;
  const valid = Boolean(week && !week.isAfter(currentWeek, "day"));

  function selectWeek(value: Dayjs | null) {
    setWeek(value ? monday(value) : null);
    setError("");
  }

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
      const { blob, filename } = await exportWeeklyUsage(week.format("YYYY-MM-DD"), controller.signal, withUnits);
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
      selectWeek(previousWeek);
      setOpen(true);
    }}>导出</Button>
    <Modal className="weekly-usage-export-modal" open={open} centered width={560}
      title={<div className="weekly-usage-export-title">
        <span className="weekly-usage-export-icon" aria-hidden="true"><FileExcelOutlined /></span>
        <strong>导出 Token 周报</strong>
      </div>}
      okText={pending ? "正在生成" : "下载 XLSX"} cancelText={pending ? "取消生成" : "取消"}
      confirmLoading={pending} okButtonProps={{ disabled: !valid, icon: <DownloadOutlined /> }}
      footer={(_, { OkBtn, CancelBtn }) => <div className="weekly-usage-export-footer">
        {pending ? <span role="status">正在整理统计数据…</span> : null}
        <div className="weekly-usage-export-actions"><CancelBtn /><OkBtn /></div>
      </div>}
      onOk={() => void download()} onCancel={close} destroyOnHidden>
      <div className="weekly-usage-export">
        <section className="weekly-usage-export-period" aria-labelledby="weekly-report-period-title">
          <div className="weekly-usage-export-heading">
            <h3 id="weekly-report-period-title">统计周期</h3>
            <div className="weekly-usage-export-shortcuts" role="group" aria-label="快捷选择统计周">
              <button type="button" disabled={pending} aria-pressed={week?.isSame(previousWeek, "day") ?? false}
                onClick={() => selectWeek(previousWeek)}>上周</button>
              <button type="button" disabled={pending} aria-pressed={partial}
                onClick={() => selectWeek(currentWeek)}>本周</button>
            </div>
          </div>
          <div className="weekly-usage-export-dates">
            <div className="weekly-usage-export-date-field">
              <label htmlFor="weekly-report-start">开始时间</label>
              <DatePicker className="weekly-usage-export-picker" id="weekly-report-start" value={week}
                allowClear={false} inputReadOnly disabled={pending} aria-describedby="weekly-report-period"
                format="YYYY-MM-DD HH:mm:ss" placeholder="选择开始时间" minDate={dayjs("1970-01-01")}
                disabledDate={(date) => date.isAfter(today, "day")}
                onChange={selectWeek} />
            </div>
            <div className="weekly-usage-export-date-field">
              <label htmlFor="weekly-report-end">结束时间</label>
              <DatePicker className="weekly-usage-export-picker" id="weekly-report-end" value={end ?? null}
                allowClear={false} inputReadOnly disabled={pending} aria-describedby="weekly-report-period"
                format="YYYY-MM-DD HH:mm:ss" placeholder="选择结束时间" minDate={dayjs("1970-01-01")}
                disabledDate={(date) => monday(date).isAfter(currentWeek, "day")}
                onChange={selectWeek} />
            </div>
          </div>
          <span className="sr-only" id="weekly-report-period" aria-live="polite">
            {week?.format("YYYY-MM-DD HH:mm:ss")} 至 {end?.format("YYYY-MM-DD HH:mm:ss")}
          </span>
          {partial ? <span className="weekly-usage-export-partial">本周未结束</span> : null}
        </section>
        <section className="weekly-usage-export-content" aria-labelledby="weekly-report-content">
          <div className="weekly-usage-export-heading">
            <h3 id="weekly-report-content">导出内容</h3><span>5 个工作表</span>
          </div>
          <ul className="weekly-usage-export-sheets">
            {["周报总览", "团队统计", "账号统计", "个人统计", "每日趋势"].map((sheet) => <li key={sheet}>{sheet}</li>)}
          </ul>
          <div className="weekly-usage-export-format">
            <div className="weekly-usage-export-heading">
              <h3 id="weekly-report-format">Token 数值格式</h3>
              <div className="weekly-usage-export-shortcuts" role="group" aria-labelledby="weekly-report-format">
                <button type="button" disabled={pending} aria-pressed={withUnits}
                  onClick={() => { setWithUnits(true); setError(""); }}>带单位</button>
                <button type="button" disabled={pending} aria-pressed={!withUnits}
                  onClick={() => { setWithUnits(false); setError(""); }}>不带单位</button>
              </div>
            </div>
            <p aria-live="polite">示例：<strong>{withUnits ? "1.25 M" : "1,250,000"}</strong></p>
          </div>
        </section>
        <Alert className="weekly-usage-export-method" type="info" showIcon title="统计口径"
          description={<>
            <p>按自然周统计，开始与结束日期自动联动。</p>
            <p>以加权 Token 为主，同时保留原始 Token；团队按当前归属汇总。</p>
            <p>{partial ? "本周统计截至生成时间，与上周同一时段对比。" : "所选周与前一个完整自然周对比。"}</p>
          </>} />
        {error ? <Alert type="error" title={error} showIcon /> : null}
      </div>
    </Modal>
  </>;
}
