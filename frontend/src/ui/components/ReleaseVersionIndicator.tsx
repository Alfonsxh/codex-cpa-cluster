import { useQuery } from "@tanstack/react-query";

import { readReleaseStatus } from "../../api/overview";

const releasesURL = "https://github.com/Alfonsxh/codex-cpa-pool/releases";
const stableVersionPattern = /^v(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/;

export function ReleaseVersionIndicator({ className = "" }: { className?: string }) {
  const release = useQuery({
    queryKey: ["admin-release-status"],
    queryFn: ({ signal }) => readReleaseStatus(false, signal),
    retry: false,
    refetchInterval: 15 * 60 * 1_000,
    refetchOnWindowFocus: false
  });
  const status = release.data;
  const currentVersion = status?.current_version.trim() || "";
  const latestVersion = status?.latest_version?.trim() || "";
  const updateAvailable = !release.isError && status?.status === "ok"
    && stableVersionPattern.test(latestVersion) && status.available;
  const versionLabel = currentVersion || (release.isPending ? "读取版本…" : "版本未知");
  const indicatorClass = ["release-version-indicator", className].filter(Boolean).join(" ");

  if (updateAvailable) {
    return (
      <a className={indicatorClass} data-update="true"
        href={`${releasesURL}/tag/${encodeURIComponent(latestVersion)}`}
        target="_blank" rel="noopener noreferrer"
        aria-label={`当前版本 ${versionLabel}，有版本更新，查看发布说明`}>
        <span className="release-version-heartbeat" aria-hidden="true" />
        <span className="release-version-update">有版本更新</span>
      </a>
    );
  }
  return (
    <span className={indicatorClass} data-update="false" aria-label={`当前版本 ${versionLabel}`}>
      <span className="release-version-number">{versionLabel}</span>
    </span>
  );
}
