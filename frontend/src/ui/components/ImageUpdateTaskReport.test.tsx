import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";

import { ImageUpdateTaskReport, parseImageUpdateOutput } from "./ImageUpdateTaskReport";

const successfulOutput = [
  "正在更新 arch：sha256:9ad5f -> sha256:d9db6",
  "arch 验证通过：运行探针",
  "正在更新 claudemaster：sha256:9ad5f -> sha256:d9db6",
  "claudemaster 验证通过：运行探针",
  "CPA 镜像更新完成：2 个"
].join("\n");

describe("ImageUpdateTaskReport", () => {
  it("turns image update logs into a complete per-account report", () => {
    const report = parseImageUpdateOutput(successfulOutput);
    expect(report).toMatchObject({
      updatedCount: 2,
      verifiedCount: 2,
      skippedCount: 0,
      targetImage: "sha256:d9db6",
      lineCount: 5
    });
    expect(report?.entries).toEqual([
      expect.objectContaining({ account: "arch", fromImage: "sha256:9ad5f", toImage: "sha256:d9db6", state: "verified" }),
      expect.objectContaining({ account: "claudemaster", fromImage: "sha256:9ad5f", toImage: "sha256:d9db6", state: "verified" })
    ]);

    render(<ImageUpdateTaskReport output={successfulOutput} status="succeeded" />);
    const summary = screen.getByLabelText("镜像更新摘要");
    expect(summary).toHaveTextContent("目标镜像sha256:d9db6");
    expect(summary).toHaveTextContent("涉及账号2");
    expect(summary).toHaveTextContent("更新完成2");
    expect(summary).toHaveTextContent("探针通过2");
    const accountResults = screen.getByRole("list", { name: "账号更新结果" });
    expect(within(accountResults).getAllByText("更新并验证通过")).toHaveLength(2);
    expect(screen.getByText("查看原始输出").closest("details")).not.toHaveAttribute("open");
  });

  it("keeps skipped and failed accounts explicit instead of implying success", () => {
    const output = [
      "跳过 beta：CPA 未运行；下次启动会使用目标镜像",
      "正在更新 arch：sha256:old -> sha256:new",
      "镜像更新失败，正在恢复已处理的 CPA：probe failed"
    ].join("\n");
    const report = parseImageUpdateOutput(output, "failed");
    expect(report).toMatchObject({ verifiedCount: 0, skippedCount: 1 });
    expect(report?.entries).toEqual([
      expect.objectContaining({ account: "beta", state: "skipped" }),
      expect.objectContaining({ account: "arch", state: "failed" })
    ]);

    render(<ImageUpdateTaskReport output={output} status="failed" />);
    const accountResults = screen.getByRole("list", { name: "账号更新结果" });
    expect(within(accountResults).getByText("已跳过")).toBeInTheDocument();
    expect(within(accountResults).getByText("更新失败", { selector: ".ant-tag" })).toBeInTheDocument();
    expect(screen.getByLabelText("任务说明")).toHaveTextContent("probe failed");
  });

  it("does not report an active account as completed before its probe finishes", () => {
    const output = "正在更新 an-account-name-that-is-long：sha256:old -> sha256:new";
    render(<ImageUpdateTaskReport output={output} status="running" />);

    const summary = screen.getByLabelText("镜像更新摘要");
    expect(summary).toHaveTextContent("更新完成—");
    expect(screen.getByText("更新中")).toBeInTheDocument();
    expect(screen.getByTitle("an-account-name-that-is-long")).toBeInTheDocument();
  });

  it("states when a completed skip-only output does not contain an image digest", () => {
    const output = "跳过 beta：CPA 未运行；下次启动会使用目标镜像\n没有运行中的 CPA；未改变已应用版本";
    render(<ImageUpdateTaskReport output={output} status="succeeded" />);

    expect(screen.getByLabelText("镜像更新摘要")).toHaveTextContent("目标镜像输出未包含镜像摘要");
    expect(screen.getByText("已跳过", { selector: ".ant-tag" })).toBeInTheDocument();
    expect(screen.getByLabelText("任务说明")).toHaveTextContent("没有运行中的 CPA");
  });

  it("does not reinterpret unrelated task output", () => {
    expect(parseImageUpdateOutput("service beta started")).toBeNull();
  });
});
