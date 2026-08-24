import { Empty, Table, type TableProps } from "antd";
import type { ReactNode } from "react";

export type AdminTableProps<RecordType extends object> = Omit<TableProps<RecordType>, "locale" | "scroll"> & {
  emptyText?: ReactNode;
  emptyAction?: ReactNode;
  minWidth?: number | string;
  maxBodyHeight?: number | string;
  locale?: TableProps<RecordType>["locale"];
  scroll?: TableProps<RecordType>["scroll"];
};

export function AdminTable<RecordType extends object>({
  className = "",
  emptyText = "暂无数据",
  emptyAction,
  locale,
  maxBodyHeight = "min(54vh, 560px)",
  minWidth = 900,
  pagination = false,
  scroll,
  size = "middle",
  ...props
}: AdminTableProps<RecordType>) {
  const configuredEmpty = typeof locale?.emptyText === "function" ? locale.emptyText() : locale?.emptyText;
  const emptyState = configuredEmpty ?? (
    <Empty description={emptyText} image={Empty.PRESENTED_IMAGE_SIMPLE}>
      {emptyAction}
    </Empty>
  );
  if (!props.loading && Array.isArray(props.dataSource) && props.dataSource.length === 0 && emptyAction) {
    return <div className="admin-table-empty">{emptyState}</div>;
  }

  return (
    <Table<RecordType>
      {...props}
      className={["admin-data-table", className].filter(Boolean).join(" ")}
      locale={{ ...locale, emptyText: emptyState }}
      pagination={pagination}
      scroll={{ x: minWidth, y: maxBodyHeight, ...scroll }}
      size={size}
    />
  );
}
