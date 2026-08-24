import { Card, Statistic, type StatisticProps } from "antd";

export type MetricCardProps = Omit<StatisticProps, "title" | "value" | "valueStyle"> & {
  title: React.ReactNode;
  value: StatisticProps["value"];
  tone?: "default" | "warning" | "danger";
  className?: string;
};

export function MetricCard({ title, value, tone = "default", className = "", ...props }: MetricCardProps) {
  return (
    <Card className={["metric-card", `metric-card-${tone}`, className].filter(Boolean).join(" ")}>
      <Statistic {...props} title={title} value={value} />
    </Card>
  );
}
