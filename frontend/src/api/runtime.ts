import { apiRequest } from "./client";
import type {
  CpaImageStatus,
  LegacyRuntimeJob,
  LegacyRuntimeJobCancelResponse,
  LegacyRuntimeJobCatalog,
  LegacyRuntimeJobResponse,
  LegacyRuntimeJobSubmissionResponse,
  OperationImpact,
  RuntimeJob,
  RuntimeJobCancelResponse,
  RuntimeJobCatalog,
  RuntimeJobRequest,
  RuntimeJobResponse,
  RuntimeJobSubmissionResponse,
  RuntimeLogs,
  RuntimeService,
  RuntimeServiceCatalog
} from "./generated";

export type {
  CpaAccountImage,
  CpaImageLocal,
  CpaImageStatus,
  OperationImpact,
  RuntimeJob,
  RuntimeJobCancelResponse,
  RuntimeJobCatalog,
  RuntimeJobRequest,
  RuntimeJobResponse,
  RuntimeJobSubmissionResponse,
  RuntimeLogs,
  RuntimeService,
  RuntimeServiceCatalog
} from "./generated";

export type LegacyRuntimeJobView = Omit<LegacyRuntimeJob, "output"> & {
  output?: string;
  error?: string;
};
export type LegacyRuntimeAction = RuntimeJobRequest["action"] | "health" | "verify-routing" | "render";
type LegacyRuntimeJobViewCatalog = { jobs: LegacyRuntimeJobView[] };
type LegacyRuntimeJobViewResponse = { job: LegacyRuntimeJobView };
type LegacyRuntimeJobViewSubmissionResponse = Omit<LegacyRuntimeJobSubmissionResponse, "job"> & { job: LegacyRuntimeJobView };
type LegacyRuntimeJobViewCancelResponse = Omit<LegacyRuntimeJobCancelResponse, "job"> & { job: LegacyRuntimeJobView };

export const runtimeServicesQueryKey = ["runtime-services"] as const;
export const runtimeJobsQueryKey = ["runtime-jobs"] as const;
export const cpaImageStatusQueryKey = ["cpa-image-status"] as const;

export function runtimeLogsQueryKey(target: string) {
  return ["runtime-logs", target] as const;
}

export function operationImpactQueryKey(action: string, target: string) {
  return ["runtime-operation-impact", action, target] as const;
}

export function listRuntimeServices(signal?: AbortSignal): Promise<RuntimeServiceCatalog> {
  return apiRequest<RuntimeServiceCatalog>("/admin/api/runtime/services", { signal });
}

export function listRuntimeJobs(signal?: AbortSignal): Promise<RuntimeJobCatalog> {
  return apiRequest<RuntimeJobCatalog>("/admin/api/runtime/jobs?limit=30", { signal });
}

export async function listLegacyRuntimeJobs(signal?: AbortSignal): Promise<LegacyRuntimeJobViewCatalog> {
  const result = await apiRequest<LegacyRuntimeJobCatalog>("/admin/api/jobs", { signal });
  return { jobs: result.jobs.map(normalizeLegacyRuntimeJob) };
}

export function readRuntimeJob(jobID: string, signal?: AbortSignal): Promise<RuntimeJobResponse> {
  return apiRequest<RuntimeJobResponse>(`/admin/api/runtime/jobs/${encodeURIComponent(jobID)}`, {
    signal,
    cache: "no-store"
  });
}

export async function readLegacyRuntimeJob(jobID: string, signal?: AbortSignal): Promise<LegacyRuntimeJobViewResponse> {
  const result = await apiRequest<LegacyRuntimeJobResponse>(`/admin/api/jobs/${encodeURIComponent(jobID)}`, {
    signal,
    cache: "no-store"
  });
  return { job: normalizeLegacyRuntimeJob(result.job) };
}

export function readCPAImageStatus(signal?: AbortSignal): Promise<CpaImageStatus> {
  return apiRequest<CpaImageStatus>("/admin/api/images/cliproxy", { signal, cache: "no-store" });
}

export function readOperationImpact(target: string, signal?: AbortSignal): Promise<OperationImpact> {
  const query = new URLSearchParams({ action: "stop", target });
  return apiRequest<OperationImpact>(`/admin/api/operations/impact?${query.toString()}`, {
    signal,
    cache: "no-store"
  });
}

export function readRuntimeLogs(target: string, signal?: AbortSignal): Promise<RuntimeLogs> {
  const query = new URLSearchParams({ target });
  return apiRequest<RuntimeLogs>(`/admin/api/runtime/logs?${query.toString()}`, { signal });
}

export function readLegacyRuntimeLogs(target: string, signal?: AbortSignal): Promise<RuntimeLogs> {
  const query = new URLSearchParams({ target });
  return apiRequest<RuntimeLogs>(`/admin/api/logs?${query.toString()}`, { signal });
}

export function submitRuntimeJob(
  action: RuntimeJobRequest["action"],
  target: string,
  csrfToken: string
): Promise<RuntimeJobSubmissionResponse> {
  return apiRequest<RuntimeJobSubmissionResponse>("/admin/api/runtime/jobs", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ action, target, confirm: `${action}:${target}` })
  });
}

export async function submitLegacyRuntimeJob(
  action: LegacyRuntimeAction,
  target: string,
  csrfToken: string
): Promise<LegacyRuntimeJobViewSubmissionResponse> {
  const result = await apiRequest<LegacyRuntimeJobSubmissionResponse>("/admin/api/operations", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ action, target })
  });
  return { ...result, job: normalizeLegacyRuntimeJob(result.job) };
}

export function cancelRuntimeJob(jobID: string, csrfToken: string): Promise<RuntimeJobCancelResponse> {
  return apiRequest<RuntimeJobCancelResponse>(`/admin/api/runtime/jobs/${encodeURIComponent(jobID)}/cancel`, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({})
  });
}

export async function cancelLegacyRuntimeJob(jobID: string, csrfToken: string): Promise<LegacyRuntimeJobViewCancelResponse> {
  const result = await apiRequest<LegacyRuntimeJobCancelResponse>("/admin/api/jobs/cancel", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({ id: jobID })
  });
  return { ...result, job: normalizeLegacyRuntimeJob(result.job) };
}

export function isActiveRuntimeJob(job: Pick<RuntimeJob, "status"> | Pick<LegacyRuntimeJobView, "status">) {
  return ["queued", "running", "cancelling"].includes(job.status);
}

function normalizeLegacyRuntimeJob(job: LegacyRuntimeJob): LegacyRuntimeJobView {
  return {
    ...job,
    output: job.output?.join("\n")
  };
}
