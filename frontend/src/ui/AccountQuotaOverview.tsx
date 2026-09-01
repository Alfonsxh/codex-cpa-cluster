import { Link } from "react-router-dom";

import type { OverviewAccountQuotaSummary } from "../api/overview";

export function AccountQuotaOverview({ quota }: { quota: OverviewAccountQuotaSummary }) {
  const used = clampPercent(quota.average_used_percent ?? 0);
  const remaining = clampPercent(quota.average_remaining_percent ?? 0);
  return (
    <section className="overview-account-quota overview-legacy-panel" aria-labelledby="overview-account-quota-title">
      <header className="overview-account-quota-header">
        <div>
          <h3 id="overview-account-quota-title">账号周额度</h3>
          <p className="section-kicker">ACCOUNT WEEKLY QUOTA</p>
        </div>
        <Link className="button ghost overview-account-quota-link" to="/accounts">查看账号详情</Link>
      </header>

      {quota.enabled_accounts === 0 ? (
        <QuotaState title="暂无启用账号" detail="创建并启用业务 CPA 后，这里会汇总常规周限额。" />
      ) : !quota.available || quota.known_accounts === 0 ? (
        <QuotaState
          title="额度数据暂不可用"
          detail={`${quota.enabled_accounts} 个启用账号当前都没有可用的常规周限额数据。`}
        />
      ) : (
        <>
          <div className="overview-account-quota-body">
            <div className="overview-account-quota-primary">
              <span>账号平均已用</span>
              <strong>{formatPercent(used)}</strong>
              <div
                className="overview-account-quota-progress"
                role="progressbar"
                aria-label="账号平均周额度已用"
                aria-valuemin={0}
                aria-valuemax={100}
                aria-valuenow={used}
              >
                <i style={{ width: `${used}%` }} />
              </div>
              <div className="overview-account-quota-progress-labels">
                <span>已用 {formatPercent(used)}</span>
                <span>剩余 {formatPercent(remaining)}</span>
              </div>
            </div>

            <dl className="overview-account-quota-metrics" aria-label="账号周额度汇总指标">
              <QuotaMetric label="等价剩余" value={`${formatEquivalent(quota.equivalent_remaining_accounts)} 个账号`} />
              <QuotaMetric label="数据覆盖" value={`${quota.known_accounts} / ${quota.enabled_accounts}`} />
              <QuotaMetric label="已耗尽" value={quota.exhausted_accounts} tone={quota.exhausted_accounts > 0 ? "danger" : undefined} />
              <QuotaMetric label="高风险" value={quota.high_risk_accounts} tone={quota.high_risk_accounts > 0 ? "warning" : undefined} />
              <QuotaMetric label="额度未知" value={quota.unknown_accounts} tone={quota.unknown_accounts > 0 ? "neutral" : undefined} />
            </dl>
          </div>
          <footer className="overview-account-quota-footer">
            按额度已知的启用账号等权计算，未知账号不参与平均；高风险表示已用达到 90% 且尚未耗尽。
          </footer>
        </>
      )}
    </section>
  );
}

function QuotaMetric({
  label,
  value,
  tone
}: {
  label: string;
  value: string | number;
  tone?: "danger" | "warning" | "neutral";
}) {
  return (
    <div className={tone ? `tone-${tone}` : undefined}>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function QuotaState({ title, detail }: { title: string; detail: string }) {
  return (
    <div className="overview-account-quota-state" role="status">
      <strong>{title}</strong>
      <span>{detail}</span>
    </div>
  );
}

function clampPercent(value: number) {
  return Math.min(100, Math.max(0, Number.isFinite(value) ? value : 0));
}

function formatPercent(value: number) {
  return `${value.toLocaleString("zh-CN", { minimumFractionDigits: 1, maximumFractionDigits: 1 })}%`;
}

function formatEquivalent(value: number) {
  const safe = Number.isFinite(value) ? Math.max(0, value) : 0;
  return safe.toLocaleString("zh-CN", { minimumFractionDigits: 1, maximumFractionDigits: 1 });
}
