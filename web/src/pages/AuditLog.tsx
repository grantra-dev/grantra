import { useState } from "react";
import { AuditTable } from "@/components/AuditTable";
import { FilterBox } from "@/components/FilterBox";
import { PageHeader } from "@/components/PageHeader";
import { Card } from "@/components/ui/card";
import { auditPageSize, listAuditEvents } from "@/lib/api";
import { searchText } from "@/lib/audit";
import { hasWords } from "@/lib/utils";
import { usePages } from "@/lib/hooks";
import { ShowMore } from "@/components/ShowMore";

export function AuditLog() {
  const [filter, setFilter] = useState("");
  const list = usePages(
    ["audit-events"],
    (before?: number) => listAuditEvents({ before }),
    auditPageSize,
  );
  const { rows: events, error, isPending, hasNextPage } = list;
  const needle = filter.trim();
  const rows = needle ? events.filter((e) => hasWords(needle, searchText(e))) : events;

  return (
    <>
      <PageHeader
        title="Audit log"
        description="Every change and decision in Grantra, newest first. Nothing here can be edited or deleted."
      />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 p-4">
          <FilterBox
            label="Filter by person, action or target"
            value={filter}
            onChange={setFilter}
          />
          {list.data && (
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
            <ShowMore query={list} label="Load older events" />
          </>
        )}
      </Card>
    </>
  );
}
