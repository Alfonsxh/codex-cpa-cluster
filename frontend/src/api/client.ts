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

  constructor(status: number, code: string, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
  }
}

export type UnauthorizedEvent = {
  path: string;
  scope: "admin" | "portal" | "unknown";
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
      payload.error?.message ?? `请求失败（HTTP ${response.status}）`
    );
    if (response.status === 401) {
      const event: UnauthorizedEvent = {
        path,
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
