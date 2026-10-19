import { useQuery } from "@tanstack/react-query";
import {
  AppWindow,
  KeyRound,
  LockKeyhole,
  MonitorCloud,
  Network,
  Search,
  SquareTerminal,
  User,
  type LucideIcon,
} from "lucide-react";
import { useId, useState } from "react";
import { useNavigate } from "react-router";
import type { Me } from "@/lib/api";
import { search, type SearchKind } from "@/lib/console";
import { hitLink, matchPages } from "@/lib/search";
import { useDebounced } from "@/lib/hooks";
import { cn } from "@/lib/utils";
import { visibleItems } from "@/modules";

/** One line of the results: a page of the console, or something the API found. */
interface Result {
  key: string;
  icon: LucideIcon;
  title: string;
  detail?: string;
  to: string;
}

const kindLabels: Record<SearchKind, string> = {
  person: "People",
  role: "Access",
  entitlement: "Access",
  application: "Applications",
  vault_account: "Privileged access",
  server: "Privileged access",
  remote_target: "Privileged access",
};

const kindIcons: Record<SearchKind, LucideIcon> = {
  person: User,
  role: Network,
  entitlement: KeyRound,
  application: AppWindow,
  vault_account: LockKeyhole,
  server: SquareTerminal,
  remote_target: MonitorCloud,
};

/** The ⌘K box: pages first, then people, access and targets from the API. */
export function SearchDialog({ me, onClose }: { me: Me; onClose: () => void }) {
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const navigate = useNavigate();
  const listId = useId();
  const settled = useDebounced(query.trim(), 200);
  const items = visibleItems(me);
  const sees = (path: string) => items.some((i) => i.path === path);

  const hits = useQuery({
    queryKey: ["search", settled],
    queryFn: () => search(settled),
    enabled: settled.length >= 2,
    staleTime: 30_000,
  });

  const pages: Result[] = matchPages(items, query.trim())
    .slice(0, 5)
    .map((i) => ({
      key: `page:${i.path}`,
      icon: i.icon,
      title: i.title,
      detail: i.description,
      to: i.path,
    }));
  const found: Result[] =
    settled.length >= 2 && settled === query.trim()
      ? (hits.data ?? []).map((h) => ({
          key: `${h.kind}:${h.id}`,
          icon: kindIcons[h.kind],
          title: h.title,
          detail: [kindLabels[h.kind], h.detail].filter(Boolean).join(" · "),
          to: hitLink(h, sees),
        }))
      : [];
  const results = [...pages, ...found];

  // Results arrive while the person types; the choice stays within them.
  const current = Math.max(0, Math.min(active, results.length - 1));

  const go = (r: Result | undefined) => {
    if (!r) return;
    onClose();
    navigate(r.to);
  };

  const waiting = query.trim().length >= 2 && (settled !== query.trim() || hits.isFetching);
  return (
    <div
      className="fixed inset-0 z-50 flex items-start justify-center p-4 pt-[12vh]"
      role="dialog"
      aria-modal="true"
      aria-label="Search"
    >
      <div className="absolute inset-0 bg-slate-900/30" onClick={onClose} aria-hidden />
      <div className="relative w-full max-w-xl overflow-hidden rounded-lg bg-white shadow-2xl">
        <div className="flex items-center gap-2 border-b border-slate-200 px-4">
          <Search className="size-4 text-slate-400" aria-hidden />
          <input
            autoFocus
            role="combobox"
            aria-expanded={results.length > 0}
            aria-controls={listId}
            aria-activedescendant={results[current] ? `${listId}-${current}` : undefined}
            aria-label="Search people, access, applications and pages"
            value={query}
            maxLength={100}
            onChange={(e) => {
              setQuery(e.target.value);
              // A new query starts at the first result.
              setActive(0);
            }}
            onKeyDown={(e) => {
              if (e.key === "Escape") onClose();
              else if (e.key === "ArrowDown") {
                e.preventDefault();
                setActive(Math.min(current + 1, results.length - 1));
              } else if (e.key === "ArrowUp") {
                e.preventDefault();
                setActive(Math.max(current - 1, 0));
              } else if (e.key === "Enter") {
                e.preventDefault();
                go(results[current]);
              }
            }}
            placeholder="Search people, access, applications, pages…"
            className="h-12 flex-1 text-sm outline-none"
          />
          <kbd className="rounded border border-slate-200 px-1.5 text-xs text-slate-400">Esc</kbd>
        </div>
        <ul id={listId} role="listbox" className="max-h-[60vh] overflow-y-auto p-1">
          {results.map((r, i) => (
            <li
              key={r.key}
              id={`${listId}-${i}`}
              role="option"
              aria-selected={i === current}
              onMouseEnter={() => setActive(i)}
              onClick={() => go(r)}
              className={cn(
                "flex cursor-pointer items-center gap-3 rounded-md px-3 py-2 text-sm",
                i === current && "bg-brand-50",
              )}
            >
              <r.icon className="size-4 shrink-0 text-slate-500" aria-hidden />
              <span className="min-w-0">
                <span className="block truncate font-medium">{r.title}</span>
                {r.detail && (
                  <span className="block truncate text-xs text-slate-500">{r.detail}</span>
                )}
              </span>
            </li>
          ))}
        </ul>
        <p
          className="border-t border-slate-100 px-4 py-2 text-xs text-slate-500"
          aria-live="polite"
        >
          {query.trim().length === 0
            ? "Type a name, an email, an application, an access or a page."
            : hits.error
              ? `Could not search: ${hits.error.message}`
              : waiting
                ? "Searching…"
                : results.length === 0
                  ? `Nothing matches “${query.trim()}”.`
                  : "↑ ↓ to move, Enter to open."}
        </p>
      </div>
    </div>
  );
}
