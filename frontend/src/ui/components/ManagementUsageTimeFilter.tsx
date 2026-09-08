import { UsageTimeRangeControl } from "./UsageTimeRangeControl";

export function ManagementUsageTimeFilter<T extends string>({
  value, options, onChange, onCustomSelect, label, start, end, updating
}: {
  value: T;
  options: ReadonlyArray<{ value: T; label: string }>;
  onChange: (value: T) => void;
  onCustomSelect: () => void;
  label: string;
  start: string;
  end: string;
  updating: boolean;
}) {
  return <div className="overview-token-window-row user-time-filter">
    <UsageTimeRangeControl value={value} options={options} onChange={onChange} onCustomSelect={onCustomSelect} label={`${label}时间范围`} />
    <div className="overview-token-window-boundaries" aria-label={`${label}时间边界`} aria-live="polite" aria-busy={updating}>
      <div className="overview-token-window-value"><small>起始时间</small><strong>{start}</strong></div>
      <div className="overview-token-window-value"><small>结束时间</small><strong>{end}</strong></div>
    </div>
  </div>;
}
