import { Modal } from "antd";
import { useEffect, useMemo, useState } from "react";

export type CustomUsageRange = {
  startAt: number;
  endAt: number;
};

type Boundary = "start" | "end";
type BoundaryDraft = {
  date: Date;
  month: Date;
  time: string;
};
type RangeDraft = Record<Boundary, BoundaryDraft>;

export function CustomUsageRangeModal({
  open,
  title,
  range,
  onCancel,
  onApply
}: {
  open: boolean;
  title: string;
  range: CustomUsageRange | null;
  onCancel: () => void;
  onApply: (range: CustomUsageRange) => void;
}) {
  const [draft, setDraft] = useState<RangeDraft>(() => createRangeDraft(range));
  const [error, setError] = useState("");

  useEffect(() => {
    if (!open) return;
    setDraft(createRangeDraft(range));
    setError("");
  }, [open, range?.endAt, range?.startAt]);

  const timestamps = useMemo(() => ({
    startAt: boundaryTimestamp(draft.start),
    endAt: boundaryTimestamp(draft.end)
  }), [draft]);
  const preview = Number.isFinite(timestamps.startAt) && Number.isFinite(timestamps.endAt)
    ? formatFullCustomUsageRange(timestamps)
    : "请选择完整的日期和时间";

  const updateBoundary = (boundary: Boundary, update: (current: BoundaryDraft) => BoundaryDraft) => {
    setDraft((current) => ({ ...current, [boundary]: update(current[boundary]) }));
  };
  const submit = () => {
    const { startAt, endAt } = timestamps;
    if (!Number.isFinite(startAt) || !Number.isFinite(endAt)) {
      setError("请选择有效的开始和结束时间");
      return;
    }
    if (startAt >= endAt) {
      setError("开始时间必须早于结束时间");
      return;
    }
    if (endAt > Math.floor(Date.now() / 1000)) {
      setError("结束时间不能晚于当前时间");
      return;
    }
    setError("");
    onApply({ startAt, endAt });
  };
  const cancel = () => {
    setError("");
    onCancel();
  };

  return (
    <Modal
      className="custom-usage-range-modal"
      title={(
        <div className="custom-usage-range-title">
          <strong>{title}</strong>
          <span>CUSTOM USAGE RANGE</span>
          <small>按本地时间选择历史用量的起止边界</small>
        </div>
      )}
      open={open}
      width={940}
      centered
      transitionName=""
      maskTransitionName=""
      okText="应用范围"
      cancelText="取消"
      onCancel={cancel}
      onOk={submit}
      destroyOnHidden
    >
      <div className="custom-usage-range-body">
        <div className="custom-range-boundaries">
          {(["start", "end"] as const).map((boundary) => (
            <BoundaryCard
              key={boundary}
              boundary={boundary}
              draft={draft[boundary]}
              onDate={(date) => updateBoundary(boundary, (current) => ({ ...current, date }))}
              onMonth={(month) => updateBoundary(boundary, (current) => ({ ...current, month }))}
              onTime={(time) => updateBoundary(boundary, (current) => ({ ...current, time }))}
            />
          ))}
        </div>
        <div className="custom-range-selection">
          <span>已选范围</span>
          <strong>{preview}</strong>
        </div>
        <p className="custom-range-help">时间按当前浏览器所在时区解释；查询包含开始时刻，不包含结束时刻。</p>
        <p className="custom-range-error" role="alert">{error}</p>
      </div>
    </Modal>
  );
}

function BoundaryCard({
  boundary,
  draft,
  onDate,
  onMonth,
  onTime
}: {
  boundary: Boundary;
  draft: BoundaryDraft;
  onDate: (date: Date) => void;
  onMonth: (month: Date) => void;
  onTime: (time: string) => void;
}) {
  const today = startOfDay(new Date());
  const cells = calendarCells(draft.month);
  const selectedKey = dateKey(draft.date);
  const currentMonthKey = monthKey(today);
  const displayedMonthKey = monthKey(draft.month);
  const label = boundary === "start" ? "开始时间" : "结束时间";
  const inclusion = boundary === "start" ? "包含" : "不包含";
  return (
    <section className="custom-boundary-card" aria-labelledby={`custom-usage-${boundary}-label`}>
      <div className="custom-boundary-heading">
        <span id={`custom-usage-${boundary}-label`}>{label}</span>
        <small>{inclusion}</small>
      </div>
      <div className="custom-boundary-controls">
        <button className="custom-date-value" type="button" aria-label={`${label}日期`}>
          <span>日期</span>
          <strong>{formatDraftDate(draft.date)}</strong>
        </button>
        <label className="custom-time-field">
          <span>时间</span>
          <input
            aria-label={`${label}时间`}
            type="text"
            inputMode="numeric"
            maxLength={5}
            pattern="(?:[01][0-9]|2[0-3]):[0-5][0-9]"
            placeholder="00:00"
            autoComplete="off"
            value={draft.time}
            onChange={(event) => onTime(event.target.value)}
          />
        </label>
      </div>
      <div className="custom-calendar">
        <header>
          <button
            type="button"
            aria-label={`${boundary === "start" ? "开始" : "结束"}日期上个月`}
            onClick={() => onMonth(new Date(draft.month.getFullYear(), draft.month.getMonth() - 1, 1))}
          >‹</button>
          <strong>{draft.month.getFullYear()} 年 {draft.month.getMonth() + 1} 月</strong>
          <button
            type="button"
            aria-label={`${boundary === "start" ? "开始" : "结束"}日期下个月`}
            disabled={displayedMonthKey >= currentMonthKey}
            onClick={() => onMonth(new Date(draft.month.getFullYear(), draft.month.getMonth() + 1, 1))}
          >›</button>
        </header>
        <div className="custom-calendar-weekdays" aria-hidden="true">
          {Array.from("日一二三四五六", (weekday) => <span key={weekday}>{weekday}</span>)}
        </div>
        <div className="custom-calendar-days">
          {cells.map((date) => {
            const key = dateKey(date);
            const future = date > today;
            const outside = date.getMonth() !== draft.month.getMonth();
            const classes = [
              outside ? "outside" : "",
              key === selectedKey ? "selected" : "",
              key === dateKey(today) ? "today" : ""
            ].filter(Boolean).join(" ");
            return (
              <button
                key={key}
                type="button"
                className={classes}
                disabled={future}
                aria-label={`${date.getFullYear()}年${date.getMonth() + 1}月${date.getDate()}日`}
                onClick={() => onDate(startOfDay(date))}
              >{date.getDate()}</button>
            );
          })}
        </div>
      </div>
    </section>
  );
}

function createRangeDraft(range: CustomUsageRange | null): RangeDraft {
  const now = Math.floor(Date.now() / 60_000) * 60;
  const start = new Date((range?.startAt ?? now - 24 * 60 * 60) * 1000);
  const end = new Date((range?.endAt ?? now) * 1000);
  return {
    start: createBoundaryDraft(start),
    end: createBoundaryDraft(end)
  };
}

function createBoundaryDraft(date: Date): BoundaryDraft {
  return {
    date: startOfDay(date),
    month: new Date(date.getFullYear(), date.getMonth(), 1),
    time: `${twoDigits(date.getHours())}:${twoDigits(date.getMinutes())}`
  };
}

function boundaryTimestamp(draft: BoundaryDraft) {
  const match = /^(\d{2}):(\d{2})$/.exec(draft.time.trim());
  if (!match) return Number.NaN;
  const hour = Number(match[1]);
  const minute = Number(match[2]);
  if (hour > 23 || minute > 59) return Number.NaN;
  return Math.floor(new Date(
    draft.date.getFullYear(), draft.date.getMonth(), draft.date.getDate(), hour, minute
  ).getTime() / 1000);
}

function calendarCells(month: Date) {
  const year = month.getFullYear();
  const monthIndex = month.getMonth();
  const firstWeekday = new Date(year, monthIndex, 1).getDay();
  return Array.from({ length: 42 }, (_, index) => (
    new Date(year, monthIndex, index - firstWeekday + 1)
  ));
}

function startOfDay(date: Date) {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate());
}

function dateKey(date: Date) {
  return `${date.getFullYear()}-${twoDigits(date.getMonth() + 1)}-${twoDigits(date.getDate())}`;
}

function monthKey(date: Date) {
  return date.getFullYear() * 12 + date.getMonth();
}

function formatDraftDate(date: Date) {
  return `${date.getFullYear()}/${twoDigits(date.getMonth() + 1)}/${twoDigits(date.getDate())}`;
}

function twoDigits(value: number) {
  return String(value).padStart(2, "0");
}

export function formatCustomUsageRange(range: CustomUsageRange | null) {
  if (!range?.startAt || !range?.endAt) return "选择时间范围";
  const start = new Date(range.startAt * 1000);
  const end = new Date(range.endAt * 1000);
  const sameYear = start.getFullYear() === end.getFullYear();
  const sameDay = sameYear && start.getMonth() === end.getMonth() && start.getDate() === end.getDate();
  const day = (date: Date, includeYear = false) => [
    ...(includeYear ? [date.getFullYear()] : []),
    twoDigits(date.getMonth() + 1),
    twoDigits(date.getDate())
  ].join("/");
  const time = (date: Date) => `${twoDigits(date.getHours())}:${twoDigits(date.getMinutes())}`;
  if (sameDay) return `${day(start)} ${time(start)}–${time(end)}`;
  const part = (date: Date, includeYear = false) => `${day(date, includeYear)} ${time(date)}`;
  return `${part(start, !sameYear)} → ${part(end, !sameYear)}`;
}

export function formatFullCustomUsageRange(range: CustomUsageRange) {
  const format = (timestamp: number) => {
    const date = new Date(timestamp * 1000);
    return `${date.getFullYear()}/${twoDigits(date.getMonth() + 1)}/${twoDigits(date.getDate())} ${twoDigits(date.getHours())}:${twoDigits(date.getMinutes())}`;
  };
  return `${format(range.startAt)} → ${format(range.endAt)}`;
}
