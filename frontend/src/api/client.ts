export type ApiErrorBody = {
  error?: {
    code?: string;
    message?: string;
    type?: string;
  };
};

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryAfterSeconds: number;

  constructor(status: number, code: string, message: string, retryAfterSeconds = 0) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.retryAfterSeconds = retryAfterSeconds;
  }
}

export type UnauthorizedEvent = {
  path: string;
  scope: "admin" | "portal" | "unknown";
  code: string;
  message: string;
};

const unauthorizedListeners = new Set<(event: UnauthorizedEvent) => void>();

export function subscribeUnauthorized(listener: (event: UnauthorizedEvent) => void) {
  unauthorizedListeners.add(listener);
  return () => {
    unauthorizedListeners.delete(listener);
  };
}

export async function apiRequest<T>(path: string, init: RequestInit = {}): Promise<T> {
  const response = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: {
      Accept: "application/json",
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...init.headers
    }
  });
  if (!response.ok) {
    let payload: ApiErrorBody = {};
    try {
      payload = (await response.json()) as ApiErrorBody;
    } catch {
      // A proxy or interrupted response may not contain the API error envelope.
    }
    const error = new ApiError(
      response.status,
      payload.error?.code ?? "request_failed",
      payload.error?.message ?? `请求失败（HTTP ${response.status}）`,
      parseRetryAfterSeconds(response.headers.get("Retry-After"))
    );
    if (response.status === 401) {
      const event: UnauthorizedEvent = {
        path,
        code: error.code,
        message: error.message,
        scope: path.startsWith("/admin/api/")
          ? "admin"
          : path.startsWith("/usage/")
            ? "portal"
            : "unknown"
      };
      unauthorizedListeners.forEach((listener) => listener(event));
    }
    throw error;
  }
  return (await response.json()) as T;
}

function parseRetryAfterSeconds(value: string | null): number {
  if (!value) return 0;
  const seconds = Number.parseInt(value, 10);
  return Number.isFinite(seconds) ? Math.max(0, Math.min(seconds, 3_600)) : 0;
}
