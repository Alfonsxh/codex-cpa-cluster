import type { QueryClient, QueryKey } from "@tanstack/react-query";

// Admin and Usage account lists use the same freshness and polling policy.
export const accountListRefreshOptions = {
  staleTime: 15_000,
  gcTime: 5 * 60_000,
  retry: false,
  refetchOnMount: true,
  refetchOnWindowFocus: "always",
  refetchIntervalInBackground: false,
  refetchInterval: (query: { state: { data?: { quota_refreshing?: boolean } } }) =>
    query.state.data?.quota_refreshing ? 3_000 : 15_000
} as const;

// Cancel older list reads before the explicit refresh and invalidate all time
// windows, while refetching only the visible one. Never reuse a pre-click result.
export async function refreshAccountList<T>(
  client: QueryClient,
  root: QueryKey,
  key: QueryKey,
  read: (signal: AbortSignal) => Promise<T>
): Promise<T> {
  await client.cancelQueries({ queryKey: root });
  await client.invalidateQueries({ queryKey: root, refetchType: "none" });
  return client.fetchQuery({ queryKey: key, queryFn: ({ signal }) => read(signal), staleTime: 0 });
}
