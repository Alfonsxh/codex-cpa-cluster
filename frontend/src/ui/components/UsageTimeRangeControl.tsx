export const recentUsageWindows = [
  { value: "3600", label: "1 小时" },
  { value: "21600", label: "6 小时" },
  { value: "today", label: "今日" },
  { value: "86400", label: "24 小时" },
  { value: "604800", label: "7 天" },
  { value: "2592000", label: "30 天" }
] as const;

export function UsageTimeRangeControl<T extends string>({ value, options, onChange, label, className = "", onCustomSelect }: {
  value: T;
  options: ReadonlyArray<{ value: T; label: string; title?: string }>;
  onChange: (value: T) => void;
  label: string;
  className?: string;
  onCustomSelect?: () => void;
}) {
  return (
    <fieldset className={`overview-legacy-window-control usage-time-control ${className}`.trim()}>
      <legend>时间范围</legend>
      <div className="overview-legacy-window-segments usage-time-segments" role="group" aria-label={label}>
        {options.map((option) => (
          <button key={option.value} type="button" title={option.title} aria-pressed={value === option.value} onClick={() => onChange(option.value)}>
            {option.label}
          </button>
        ))}
        {onCustomSelect ? <button type="button" aria-pressed={value === "custom"} title="选择时间范围" onClick={onCustomSelect}>时间选择</button> : null}
      </div>
    </fieldset>
  );
}
