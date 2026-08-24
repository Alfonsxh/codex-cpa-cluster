import { Button, Result, Skeleton } from "antd";

export type PageStateProps = {
  kind: "loading" | "error";
  title?: string;
  detail?: string;
  actionLabel?: string;
  onAction?: () => void;
  rows?: number;
};

export function PageState({
  kind,
  title = "页面加载失败",
  detail = "请稍后重试",
  actionLabel = "重新加载",
  onAction,
  rows = 8
}: PageStateProps) {
  if (kind === "loading") {
    return (
      <div className="admin-page-state" aria-label={title || "正在加载页面"}>
        <Skeleton active paragraph={{ rows }} />
      </div>
    );
  }
  return (
    <Result
      className="admin-page-state"
      status="warning"
      title={title}
      subTitle={detail}
      extra={onAction ? <Button type="primary" onClick={onAction}>{actionLabel}</Button> : null}
    />
  );
}
