import { Button, Progress } from "antd";
import { Link } from "react-router-dom";

import type { OnboardingStatus } from "../api/onboarding";

export function OnboardingCard({ status }: { status: OnboardingStatus }) {
  const completed = status.required.complete + status.recommended.complete + status.recommended.skipped;
  const total = status.required.total + status.recommended.total;
  const percent = total > 0 ? Math.round(completed / total * 100) : 100;
  const title = "继续完善系统配置";
  const detail = `${completed}/${total} 项配置已处理，可随时返回继续。`;

  return (
    <section className="onboarding-resume-card" aria-label={title}>
      <div className="onboarding-resume-mark" aria-hidden="true">→</div>
      <div className="onboarding-resume-copy">
        <span className="section-kicker">GETTING STARTED</span>
        <h2>{title}</h2>
        <p>{detail}</p>
      </div>
      <div className="onboarding-resume-progress">
        <Progress percent={percent} showInfo={false} size="small" />
        <small>配置进度 {percent}%</small>
      </div>
      <Link to="/setup">
        <Button type="primary">继续配置</Button>
      </Link>
    </section>
  );
}
