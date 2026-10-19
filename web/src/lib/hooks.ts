import { keepPreviousData, useInfiniteQuery, useQuery, type QueryKey } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { getInbox } from "./console";

/** value, once it has stopped changing for ms milliseconds. */
export function useDebounced<T>(value: T, ms = 300): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return settled;
}

/**
 * Everything waiting on the person, for the counters in the menu, the header
 * and the inbox page. Nothing polls: it is read again when they move to
 * another page and it is older than half a minute, and after each decision.
 */
export function useInbox() {
  return useQuery({ queryKey: ["inbox"], queryFn: getInbox, staleTime: 30_000 });
}

/**
 * A list the API gives page by page: each page asks for what comes after
 * the last row of the one before, and a short page is the last one. While a
 * new filter loads, the rows of the previous one stay on screen.
 */
export function usePages<T extends { id: string | number }>(
  queryKey: QueryKey,
  fetchPage: (after: NoInfer<T["id"]> | undefined) => Promise<T[]>,
  pageSize: number,
) {
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam }) => fetchPage(pageParam as T["id"] | undefined),
    initialPageParam: undefined as T["id"] | undefined,
    getNextPageParam: (last) => (last.length === pageSize ? last.at(-1)?.id : undefined),
    placeholderData: keepPreviousData,
  });
  return { ...query, rows: query.data?.pages.flat() ?? [] };
}
