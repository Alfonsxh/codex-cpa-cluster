import { DatePicker, Modal } from "antd";
import dayjs, { type Dayjs } from "dayjs";
import timezonePlugin from "dayjs/plugin/timezone";
import utc from "dayjs/plugin/utc";
import { useEffect, useMemo, useState } from "react";

dayjs.extend(utc);
dayjs.extend(timezonePlugin);

export type CustomUsageRange = {
  startAt: number;
  endAt: number;
};

type PickerRange = [Dayjs | null, Dayjs | null] | null;

export function CustomUsageRangeModal({
  open,
  title,
  range,
  timezone,
  onCancel,
  onApply
}: {
  open: boolean;
  title: string;
  range: CustomUsageRange | null;
  timezone?: string;
  onCancel: () => void;
  onApply: (range: CustomUsageRange) => void;
}) {
  const zone = normalizeTimezone(timezone);
  const [draft, setDraft] = useState<PickerRange>(() => createPickerRange(range, zone));
  const [error, setError] = useState("");
  const nowInZone = dayjs().tz(zone);

  useEffect(() => {
    if (!open) return;
    setDraft(createPickerRange(range, zone));
    setError("");
  }, [open, range?.endAt, range?.startAt, zone]);

  const timestamps = useMemo(() => pickerTimestamps(draft, zone), [draft, zone]);
  const preview = timestamps
    ? formatFullCustomUsageRange(timestamps, zone)
    : "请选择开始和结束时间";

  const submit = () => {
    if (!timestamps) {
      setError("请选择有效的开始和结束时间");
      return;
    }
    if (timestamps.startAt >= timestamps.endAt) {
      setError("开始时间必须早于结束时间");
      return;
    }
    if (timestamps.endAt > Math.floor(Date.now() / 1000)) {
      setError("结束时间不能晚于当前时间");
      return;
    }
    setError("");
    onApply(timestamps);
  };

  return (
    <Modal
      className="custom-usage-range-modal"
      title={<div className="custom-usage-range-title"><strong>{title}</strong></div>}
      open={open}
      width={720}
      centered
      transitionName=""
      maskTransitionName=""
      okText="应用范围"
      cancelText="取消"
      onCancel={() => {
        setError("");
        onCancel();
      }}
      onOk={submit}
      destroyOnHidden
    >
      <div className="custom-usage-range-body">
        <DatePicker.RangePicker
          className="custom-usage-range-picker"
          aria-label="时间范围"
          value={draft}
          format="YYYY/MM/DD HH:mm"
          showTime={{ format: "HH:mm" }}
          allowClear={false}
          inputReadOnly
          disabledDate={(current) => current.tz(zone, true).startOf("day").isAfter(nowInZone.endOf("day"))}
          onCalendarChange={(value) => {
            setDraft(value ? [value[0], value[1]] : null);
            setError("");
          }}
          onChange={(value) => {
            setDraft(value ? [value[0], value[1]] : null);
            setError("");
          }}
          presets={[
            { label: "最近 1 小时", value: [nowInZone.subtract(1, "hour"), nowInZone] },
            { label: "最近 24 小时", value: [nowInZone.subtract(24, "hour"), nowInZone] },
            { label: "最近 7 天", value: [nowInZone.subtract(7, "day"), nowInZone] }
          ]}
        />
        <div className="custom-range-selection" aria-live="polite">
          <span>已选范围</span>
          <strong>{preview}</strong>
        </div>
        <p className="custom-range-error" role="alert">{error}</p>
      </div>
    </Modal>
  );
}

function createPickerRange(range: CustomUsageRange | null, zone: string): PickerRange {
  const now = Math.floor(Date.now() / 60_000) * 60;
  const startAt = range?.startAt ?? now - 24 * 60 * 60;
  const endAt = range?.endAt ?? now;
  return [dayjs.unix(startAt).tz(zone), dayjs.unix(endAt).tz(zone)];
}

function pickerTimestamps(range: PickerRange, zone: string): CustomUsageRange | null {
  if (!range?.[0]?.isValid() || !range?.[1]?.isValid()) return null;
  return {
    startAt: range[0].tz(zone, true).unix(),
    endAt: range[1].tz(zone, true).unix()
  };
}

function normalizeTimezone(value?: string) {
  const candidate = value?.trim() || Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  try {
    new Intl.DateTimeFormat("en-US", { timeZone: candidate }).format();
    return candidate;
  } catch {
    return "UTC";
  }
}

export function formatCustomUsageRange(range: CustomUsageRange | null, timezone?: string) {
  if (!range?.startAt || !range?.endAt) return "选择时间范围";
  const zone = normalizeTimezone(timezone);
  const start = dayjs.unix(range.startAt).tz(zone);
  const end = dayjs.unix(range.endAt).tz(zone);
  const sameYear = start.year() === end.year();
  const sameDay = sameYear && start.month() === end.month() && start.date() === end.date();
  if (sameDay) return `${start.format("MM/DD HH:mm")}–${end.format("HH:mm")}`;
  return `${start.format(sameYear ? "MM/DD HH:mm" : "YYYY/MM/DD HH:mm")} → ${end.format(sameYear ? "MM/DD HH:mm" : "YYYY/MM/DD HH:mm")}`;
}

export function formatFullCustomUsageRange(range: CustomUsageRange, timezone?: string) {
  const zone = normalizeTimezone(timezone);
  return `${dayjs.unix(range.startAt).tz(zone).format("YYYY/MM/DD HH:mm")} → ${dayjs.unix(range.endAt).tz(zone).format("YYYY/MM/DD HH:mm")}`;
}
