import { useQuery } from "@tanstack/react-query";
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
