import type { NavItem } from "@/modules";
import type { SearchHit } from "./console";
import { hasWords } from "./utils";

/**
 * Where a hit leads, among the pages this person sees: an admin opens an
 * entitlement's application, anyone else finds it in Request access.
 */
export function hitLink(hit: SearchHit, sees: (path: string) => boolean): string {
  const asked = (q: string) => `/request?q=${encodeURIComponent(q)}`;
  switch (hit.kind) {
    case "person":
      return `/people/${hit.id}`;
    case "application":
      return `/governance/applications/${hit.id}`;
    case "entitlement":
      return sees("/governance/applications") && hit.application_id
        ? `/governance/applications/${hit.application_id}`
        : asked(hit.title);
    case "role":
      return sees("/governance/roles") ? "/governance/roles" : asked(hit.title);
    case "vault_account":
      return `/vault?account=${hit.id}`;
    case "server":
      return `/servers?target=${hit.id}`;
    case "remote_target":
      return `/remote-access?target=${hit.id}`;
  }
}

/** The pages whose name or description has every word of the query. */
export function matchPages(items: NavItem[], query: string): NavItem[] {
  if (!query.trim()) return [];
  return items.filter((i) => hasWords(query, i.title, i.description));
}
