import { Select, type SelectProps } from "antd";

export type WideSelectProps<ValueType = unknown> = Omit<SelectProps<ValueType>, "classNames" | "popupMatchSelectWidth"> & {
  popupWidth?: number | false;
};

export function WideSelect<ValueType = unknown>({ popupWidth = 360, className = "", suffixIcon, ...props }: WideSelectProps<ValueType>) {
  return (
    <Select<ValueType>
      {...props}
      className={["admin-wide-select", className].filter(Boolean).join(" ")}
      popupMatchSelectWidth={popupWidth}
      classNames={{ popup: { root: "admin-wide-select-popup" } }}
      suffixIcon={suffixIcon ?? <span className="admin-wide-select-caret" aria-hidden="true">⌄</span>}
    />
  );
}
