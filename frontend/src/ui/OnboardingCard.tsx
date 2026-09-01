import { Button, Progress } from "antd";
import { Link } from "react-router-dom";

import type { OnboardingStatus } from "../api/onboarding";

export function OnboardingCard({ status }: { status: OnboardingStatus }) {
  const requiredPercent = status.required.total > 0
    ? Math.round(status.required.complete / status.required.total * 100)
    : 100;
  const recommendationsHandled = status.recommended.complete + status.recommended.skipped;
  const title = status.required_complete ? "继续完善推荐设置" : "完成首次设置";
  const detail = status.required_complete
    ? `${recommendationsHandled}/${status.recommended.total} 项推荐设置已处理，可随时补充或恢复跳过项。`
    : `${status.required.complete}/${status.required.total} 项必需设置已完成，继续后即可交付第一个用户。`;

  return (
    <section className="onboarding-resume-card" aria-label={title}>
      <div className="onboarding-resume-mark" aria-hidden="true">{status.required_complete ? "✦" : "→"}</div>
      <div className="onboarding-resume-copy">
        <span className="section-kicker">GETTING STARTED</span>
        <h2>{title}</h2>
        <p>{detail}</p>
      </div>
      {!status.required_complete ? (
        <div className="onboarding-resume-progress">
          <Progress percent={requiredPercent} showInfo={false} size="small" />
          <small>必需设置 {requiredPercent}%</small>
        </div>
      ) : null}
      <Link to="/setup">
        <Button type="primary">{status.required_complete ? "查看推荐设置" : "继续初始化"}</Button>
      </Link>
    </section>
  );
}
