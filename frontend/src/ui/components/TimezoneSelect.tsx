import { Select } from "antd";
import { useMemo } from "react";

const commonZones: Record<string, string> = {
  "Asia/Shanghai": "北京时间",
  UTC: "协调世界时",
  "Asia/Hong_Kong": "香港",
  "Asia/Tokyo": "东京",
  "Asia/Singapore": "新加坡",
  "Europe/London": "伦敦",
  "America/New_York": "纽约",
  "America/Los_Angeles": "洛杉矶"
};

export function timezoneOptions(value: string) {
  const supported = (Intl as typeof Intl & { supportedValuesOf?: (key: "timeZone") => string[] })
    .supportedValuesOf?.("timeZone") ?? [];
  const zones = [...new Set([...Object.keys(commonZones), ...supported, value].filter(Boolean))];
  const now = new Date();
  return zones.map((zone) => {
    const offset = new Intl.DateTimeFormat("en", { timeZone: zone, timeZoneName: "longOffset" })
      .formatToParts(now).find((part) => part.type === "timeZoneName")?.value.replace("GMT", "UTC") ?? "";
    return { value: zone, label: `${commonZones[zone] ? `${commonZones[zone]} · ` : ""}${zone} (${offset})` };
  });
}

export function TimezoneSelect({ id, value, onChange, disabled = false }: {
  id: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
}) {
  const options = useMemo(() => timezoneOptions(value), [value]);
  return <Select id={id} aria-label="系统时区" value={value} options={options} onChange={onChange}
    disabled={disabled} showSearch={{ optionFilterProp: "label" }} placeholder="搜索城市或时区"
    style={{ width: "100%", minWidth: 0 }} popupMatchSelectWidth />;
}
