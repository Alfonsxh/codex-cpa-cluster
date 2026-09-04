import { Tag } from "antd";

type ImageUpdateEntryState = "updating" | "verified" | "skipped" | "restored" | "failed";

export type ImageUpdateEntry = {
  account: string;
  fromImage: string;
  toImage: string;
  detail: string;
  state: ImageUpdateEntryState;
};

export type ImageUpdateReport = {
  entries: ImageUpdateEntry[];
  updatedCount: number | null;
  verifiedCount: number;
  skippedCount: number;
  targetImage: string;
  notices: string[];
  lineCount: number;
};

type TaskStatus = "queued" | "running" | "cancelling" | "succeeded" | "failed" | "cancelled";

export function ImageUpdateTaskReport({ output, status }: { output: string; status: TaskStatus }) {
  const report = parseImageUpdateOutput(output, status);
  if (!report) return null;
  const active = ["queued", "running", "cancelling"].includes(status);
  const discoveredUpdates = report.entries.filter((entry) => entry.fromImage && entry.toImage).length;
  const updatedCount = report.updatedCount ?? (status === "succeeded" ? discoveredUpdates : null);
  const targetImage = report.targetImage || (active ? "正在识别…" : "输出未包含镜像摘要");
  return (
    <section className="image-update-task-report" aria-label="CPA 镜像更新详情">
      <div className="image-update-summary" aria-label="镜像更新摘要">
        <div className="target">
          <span>目标镜像</span>
          <code title={targetImage}>{targetImage}</code>
        </div>
        <div>
          <span>涉及账号</span>
          <strong>{report.entries.length}</strong>
        </div>
        <div>
          <span>更新完成</span>
          <strong>{updatedCount ?? "—"}</strong>
        </div>
        <div>
          <span>探针通过</span>
          <strong>{report.verifiedCount}</strong>
        </div>
        <div>
          <span>已跳过</span>
          <strong>{report.skippedCount}</strong>
        </div>
      </div>

      <div className="image-update-account-list" role="list" aria-label="账号更新结果">
        {report.entries.map((entry) => {
          const presentation = imageUpdateEntryPresentation(entry, status);
          return (
            <article className={`image-update-account ${presentation.tone}`} role="listitem" key={entry.account}>
              <header>
                <strong title={entry.account}>{entry.account}</strong>
                <Tag color={presentation.color}>{presentation.label}</Tag>
              </header>
              {entry.fromImage || entry.toImage ? (
                <div className="image-update-transition">
                  <div><span>原镜像</span><code title={entry.fromImage}>{entry.fromImage || "—"}</code></div>
                  <i aria-hidden="true">→</i>
                  <div><span>目标镜像</span><code title={entry.toImage}>{entry.toImage || "—"}</code></div>
                </div>
              ) : null}
              <p>{entry.detail || presentation.detail}</p>
            </article>
          );
        })}
      </div>

      {report.notices.length ? (
        <div className="image-update-notices" aria-label="任务说明">
          {report.notices.map((notice, index) => <p key={`${index}-${notice}`}>{notice}</p>)}
        </div>
      ) : null}

      <details className="image-update-raw-output">
        <summary>查看原始输出 <span>{report.lineCount} 行</span></summary>
        <pre className="oauth-task-output">{output}</pre>
      </details>
    </section>
  );
}

export function parseImageUpdateOutput(output: string, status: TaskStatus = "succeeded"): ImageUpdateReport | null {
  const lines = output.split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  const entries = new Map<string, ImageUpdateEntry>();
  const notices: string[] = [];
  let updatedCount: number | null = null;
  let recognized = 0;

  const ensureEntry = (account: string) => {
    const existing = entries.get(account);
    if (existing) return existing;
    const created: ImageUpdateEntry = {
      account,
      fromImage: "",
      toImage: "",
      detail: "",
      state: "updating"
    };
    entries.set(account, created);
    return created;
  };

  for (const line of lines) {
    const updating = line.match(/^正在更新\s+(.+?)[:：]\s*(.+?)\s+->\s+(.+)$/);
    if (updating) {
      const entry = ensureEntry(updating[1].trim());
      entry.fromImage = updating[2].trim();
      entry.toImage = updating[3].trim();
      entry.state = "updating";
      entry.detail = "镜像已替换，等待运行探针确认";
      recognized++;
      continue;
    }
    const verified = line.match(/^(.+?)\s+验证通过[:：]\s*(.+)$/);
    if (verified) {
      const entry = ensureEntry(verified[1].trim());
      entry.state = "verified";
      entry.detail = `${verified[2].trim()}通过`;
      recognized++;
      continue;
    }
    const skipped = line.match(/^跳过\s+(.+?)[:：]\s*(.+)$/);
    if (skipped) {
      const entry = ensureEntry(skipped[1].trim());
      entry.state = "skipped";
      entry.detail = skipped[2].trim();
      recognized++;
      continue;
    }
    const restored = line.match(/^已恢复\s+(.+)$/);
    if (restored) {
      const entry = ensureEntry(restored[1].trim());
      entry.state = "restored";
      entry.detail = "更新失败，已恢复原镜像并通过探针";
      recognized++;
      continue;
    }
    const completed = line.match(/^CPA 镜像更新完成[:：]\s*(\d+)\s*个$/);
    if (completed) {
      updatedCount = Number(completed[1]);
      notices.push(line);
      recognized++;
      continue;
    }
    if (/^(运行中的 CPA 已验证|没有运行中的 CPA|镜像更新失败)/.test(line)) {
      notices.push(line);
      recognized++;
    }
  }

  if (!recognized) return null;
  const normalizedEntries = [...entries.values()].map((entry) => (
    status === "failed" && entry.state === "updating"
      ? { ...entry, state: "failed" as const, detail: "镜像更新未完成，请查看任务说明和原始输出" }
      : entry
  ));
  return {
    entries: normalizedEntries,
    updatedCount,
    verifiedCount: normalizedEntries.filter((entry) => entry.state === "verified").length,
    skippedCount: normalizedEntries.filter((entry) => entry.state === "skipped").length,
    targetImage: normalizedEntries.find((entry) => entry.toImage)?.toImage ?? "",
    notices,
    lineCount: lines.length
  };
}

function imageUpdateEntryPresentation(entry: ImageUpdateEntry, taskStatus: TaskStatus) {
  switch (entry.state) {
    case "verified":
      return { label: entry.fromImage ? "更新并验证通过" : "验证通过", color: "success", tone: "success", detail: "运行探针通过" };
    case "skipped":
      return { label: "已跳过", color: "default", tone: "neutral", detail: "此账号未执行镜像替换" };
    case "restored":
      return { label: "已恢复", color: "warning", tone: "warning", detail: "已恢复原镜像" };
    case "failed":
      return { label: "更新失败", color: "error", tone: "error", detail: "镜像更新未完成" };
    default:
      return taskStatus === "cancelled"
        ? { label: "已取消", color: "default", tone: "neutral", detail: "任务已取消" }
        : { label: "更新中", color: "processing", tone: "processing", detail: "等待运行探针确认" };
  }
}
