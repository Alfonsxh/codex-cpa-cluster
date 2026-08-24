import { createContext, useContext } from "react";

export type AdminToolbarContextValue = {
  refreshRevision: number;
  setRefreshing: (refreshing: boolean) => void;
};

export const AdminToolbarContext = createContext<AdminToolbarContextValue>({
  refreshRevision: 0,
  setRefreshing: () => undefined
});

export function useAdminToolbar() {
  return useContext(AdminToolbarContext);
}
