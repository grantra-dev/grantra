import { useInfiniteQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useState } from "react";
import { AuditTable } from "@/components/AuditTable";
import { PageHeader } from "@/components/PageHeader";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { auditPageSize, listAuditEvents } from "@/lib/api";
import { searchText } from "@/lib/audit";

export function AuditLog() {
  const [filter, setFilter] = useState("");
  const { data, error, isPending, hasNextPage, fetchNextPage, isFetchingNextPage } =
    useInfiniteQuery({
      queryKey: ["audit-events"],
      queryFn: ({ pageParam }) => listAuditEvents({ before: pageParam }),
      initialPageParam: 0,
      getNextPageParam: (last) => (last.length === auditPageSize ? last.at(-1)?.id : undefined),
    });
  const events = data?.pages.flat() ?? [];
  const needle = filter.trim().toLowerCase();
  const rows = needle ? events.filter((e) => searchText(e).includes(needle)) : events;

  return (
    <>
      <PageHeader
        title="Audit log"
        description="Every change and decision in Grantra, newest first. Nothing here can be edited or deleted."
      />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 p-4">
          <label className="relative block w-full max-w-xs">
            <span className="sr-only">Filter by person, action or target</span>
            <Search
              className="pointer-events-none absolute top-2.5 left-3 size-4 text-slate-400"
              aria-hidden
            />
            <input
              type="search"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter by person, action or target"
              className="h-9 w-full rounded-md border border-slate-300 pr-3 pl-9 text-sm focus:outline-2 focus:outline-brand-600"
            />
          </label>
          {data && (
            <span className="text-sm text-slate-500">
              {needle
                ? `${rows.length} of ${events.length} loaded events`
                : `${events.length} events loaded`}
            </span>
          )}
        </div>
        {isPending ? (
          <p className="p-6 text-sm text-slate-500">Loading the audit log…</p>
        ) : error ? (
          <p role="alert" className="p-6 text-sm text-red-700">
            Could not load the audit log: {error.message}
          </p>
        ) : events.length === 0 ? (
          <p className="p-6 text-sm text-slate-500">Nothing has happened yet.</p>
        ) : (
          <>
            {rows.length === 0 ? (
              <p className="p-6 text-sm text-slate-500">
                Nothing loaded matches “{filter}”.
                {hasNextPage && " Load older events to search further back."}
              </p>
            ) : (
              <AuditTable events={rows} />
            )}
            {hasNextPage && (
              <div className="border-t border-slate-200 p-3 text-center">
                <Button
                  variant="outline"
                  onClick={() => void fetchNextPage()}
                  disabled={isFetchingNextPage}
                >
                  {isFetchingNextPage ? "Loading…" : "Load older events"}
                </Button>
              </div>
            )}
          </>
        )}
      </Card>
    </>
  );
}
