import { Button, Progress } from "antd";
import { Link } from "react-router-dom";

import type { OnboardingStatus } from "../api/onboarding";

export function OnboardingCard({ status }: { status: OnboardingStatus }) {
  if (status.required_complete) return null;

  const completed = status.required.complete;
  const total = status.required.total;
  const percent = total > 0 ? Math.round(completed / total * 100) : 100;
  const title = "完成基础配置";
  const detail = `${completed}/${total} 项基础配置已完成。`;

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
        <Button type="primary">继续设置</Button>
      </Link>
    </section>
  );
}
