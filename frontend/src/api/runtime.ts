import { apiRequest } from "./client";
import type {
  CpaImageStatus,
  OperationImpact,
  RuntimeJob,
  RuntimeJobCancelResponse,
  RuntimeJobCatalog,
  RuntimeJobRequest,
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
  RuntimeJobSubmissionResponse,
  RuntimeLogs,
  RuntimeService,
  RuntimeServiceCatalog
} from "./generated";

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

export function cancelRuntimeJob(jobID: string, csrfToken: string): Promise<RuntimeJobCancelResponse> {
  return apiRequest<RuntimeJobCancelResponse>(`/admin/api/runtime/jobs/${encodeURIComponent(jobID)}/cancel`, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
    body: JSON.stringify({})
  });
}

export function isActiveRuntimeJob(job: RuntimeJob) {
  return ["queued", "running", "cancelling"].includes(job.status);
}
