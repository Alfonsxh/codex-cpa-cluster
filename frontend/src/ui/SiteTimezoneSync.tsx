import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { publicSiteQueryKey, readPublicSiteConfiguration } from "../api/public-site";
import { defaultSiteTimezone, setSiteTimezone, useSiteTimezone } from "./site-time";

export function SiteTimezoneSync() {
  const timezone = useSiteTimezone();
  const previousTimezone = useRef(timezone);
  const queryClient = useQueryClient();
  const configuration = useQuery({
    queryKey: publicSiteQueryKey,
    queryFn: ({ signal }) => readPublicSiteConfiguration(signal),
    refetchInterval: 60_000,
    refetchOnWindowFocus: true
  });
  useEffect(() => {
    if (configuration.data) setSiteTimezone(configuration.data.timezone || defaultSiteTimezone);
  }, [configuration.data]);
  useEffect(() => {
    if (previousTimezone.current === timezone) return;
    previousTimezone.current = timezone;
    // Requery date buckets as well as repainting their timestamps. The previous
    // timezone's "today" and weekly totals are no longer the selected window.
    void queryClient.invalidateQueries({ predicate: (query) => query.queryKey[0] !== publicSiteQueryKey[0] });
  }, [queryClient, timezone]);
  return null;
}
