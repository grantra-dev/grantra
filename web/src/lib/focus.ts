import { useEffect } from "react";
import { useSearchParams } from "react-router";

/**
 * The id a link asks the page to bring forward with ?<name>=<id>: a request
 * from an approval email, a vault account from the search box. Once ready
 * (the list has loaded), the element marked data-focus="<id>" is scrolled
 * into view; the page highlights it.
 */
export function useFocusParam(name: string, ready: boolean): string | null {
  const [params] = useSearchParams();
  const raw = params.get(name);
  // Ids are uuids; anything else is not ours to look for.
  const id = raw && /^[\w-]{1,64}$/.test(raw) ? raw : null;
  useEffect(() => {
    if (!id || !ready) return;
    document.querySelector(`[data-focus="${id}"]`)?.scrollIntoView?.({ block: "center" });
  }, [id, ready]);
  return id;
}

/** The classes that make the element a link asked for stand out. */
export const focusRing = "ring-2 ring-brand-500 ring-offset-2";
