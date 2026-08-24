import { Space } from "antd";
import type { ReactNode } from "react";

export type PageToolbarProps = {
  description: ReactNode;
  actions?: ReactNode;
  className?: string;
};

export function PageToolbar({ description, actions, className = "" }: PageToolbarProps) {
  return (
    <div className={["page-intro", "page-toolbar", className].filter(Boolean).join(" ")}>
      <div className="page-toolbar-description">
        {typeof description === "string" ? <p>{description}</p> : description}
      </div>
      {actions ? (
        <Space className="page-toolbar-actions" wrap size={[8, 8]}>
          {actions}
        </Space>
      ) : null}
    </div>
  );
}
