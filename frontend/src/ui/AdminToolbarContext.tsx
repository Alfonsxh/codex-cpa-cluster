import { createContext, useContext } from "react";

export type AdminToolbarContextValue = {
  setRefreshing: (refreshing: boolean) => void;
  setRefreshLabel: (label: string) => void;
  setRefreshAction: (action: (() => Promise<void>) | null) => void;
  setPageDetail: (detail: { title: string; eyebrow: string } | null) => void;
};

export const AdminToolbarContext = createContext<AdminToolbarContextValue>({
  setRefreshing: () => undefined,
  setRefreshLabel: () => undefined,
  setRefreshAction: () => undefined,
  setPageDetail: () => undefined
});

export function useAdminToolbar() {
  return useContext(AdminToolbarContext);
}
