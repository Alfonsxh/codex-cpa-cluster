import { QueryClient, QueryObserver, focusManager } from "@tanstack/react-query";
import { afterEach, expect, it, vi } from "vitest";
import { accountListRefreshOptions, refreshAccountList } from "./account-refresh";

const clients: QueryClient[] = [];
afterEach(() => {
  for (const client of clients.splice(0)) { client.unmount(); client.clear(); }
  focusManager.setFocused(undefined);
  vi.useRealTimers();
});

it("keeps both visible surfaces current, polls pending refreshes faster and refreshes on focus", async () => {
  vi.useFakeTimers();
  focusManager.setFocused(true);
  let status = "quota_exhausted";
  let pending = false;
  const surfaces = ["admin", "usage"].map((surface) => {
    const client = new QueryClient();
    clients.push(client);
    client.mount();
    const read = vi.fn(async () => ({ status, quota_refreshing: pending }));
    const observer = new QueryObserver(client, {
      ...accountListRefreshOptions, queryKey: [surface, "accounts"], queryFn: read
    });
    const unsubscribe = observer.subscribe(() => undefined);
    return { client, read, observer, unsubscribe };
  });
  await vi.advanceTimersByTimeAsync(0);
  expect(surfaces.map(({ observer }) => observer.getCurrentResult().data?.status)).toEqual(["quota_exhausted", "quota_exhausted"]);
  status = "available";
  await vi.advanceTimersByTimeAsync(15_000);
  expect(surfaces.map(({ observer }) => observer.getCurrentResult().data?.status)).toEqual(["available", "available"]);
  pending = true;
  await Promise.all(surfaces.map(({ client }) => client.invalidateQueries()));
  await vi.advanceTimersByTimeAsync(0);
  const before = surfaces.map(({ read }) => read.mock.calls.length);
  pending = false;
  await vi.advanceTimersByTimeAsync(3_000);
  surfaces.forEach(({ observer, read }, index) => {
    expect(read).toHaveBeenCalledTimes(before[index] + 1);
    expect(observer.getCurrentResult().data?.quota_refreshing).toBe(false);
  });
  focusManager.setFocused(false);
  status = "credential_unavailable";
  await vi.advanceTimersByTimeAsync(15_000);
  expect(surfaces.map(({ observer }) => observer.getCurrentResult().data?.status)).toEqual(["available", "available"]);
  focusManager.setFocused(true);
  await vi.advanceTimersByTimeAsync(0);
  expect(surfaces.map(({ observer }) => observer.getCurrentResult().data?.status)).toEqual(["credential_unavailable", "credential_unavailable"]);
  // Returning quickly also refreshes snapshots that are still within staleTime.
  focusManager.setFocused(false);
  status = "available";
  focusManager.setFocused(true);
  await vi.advanceTimersByTimeAsync(0);
  expect(surfaces.map(({ observer }) => observer.getCurrentResult().data?.status)).toEqual(["available", "available"]);
  surfaces.forEach(({ unsubscribe }) => unsubscribe());
});

it("does not let a pre-refresh read overwrite the new result and invalidates inactive windows", async () => {
  const client = new QueryClient();
  clients.push(client);
  const key = ["accounts", "today"];
  const inactive = ["accounts", "604800"];
  client.setQueryData(key, { status: "old" });
  client.setQueryData(inactive, { status: "old" });
  let resolveOld!: (data: { status: string }) => void;
  const previous = client.fetchQuery({ queryKey: key, queryFn: () => new Promise<{ status: string }>((resolve) => { resolveOld = resolve; }) });
  const previousError = previous.catch((error: unknown) => error);
  const fresh = vi.fn(async () => ({ status: "available" }));
  await refreshAccountList(client, ["accounts"], key, fresh);
  resolveOld({ status: "quota_exhausted" });
  await previousError;
  expect(client.getQueryData(key)).toEqual({ status: "available" });
  expect(client.getQueryState(inactive)?.isInvalidated).toBe(true);
  expect(fresh).toHaveBeenCalledOnce();
});
