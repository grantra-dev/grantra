import { useState } from "react";
import { Link } from "react-router";
import { IdentityStatusBadge } from "@/components/AccessBadges";
import { FilterBox } from "@/components/FilterBox";
import { PageHeader } from "@/components/PageHeader";
import { Card } from "@/components/ui/card";
import { Select } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { identitiesPageSize, listIdentities } from "@/lib/api";
import { useDebounced, usePages } from "@/lib/hooks";
import { formatDate } from "@/lib/utils";
import { ShowMore } from "@/components/ShowMore";

export function People() {
  const [search, setSearch] = useState("");
  const [status, setStatus] = useState("");
  const q = useDebounced(search.trim());
  const list = usePages(
    ["identities", q, status],
    (after?: string) => listIdentities({ q, status, after }),
    identitiesPageSize,
  );
  const { rows: people, error, isPending } = list;

  return (
    <>
      <PageHeader
        title="People"
        description="Everyone Grantra knows about: employees from HR and the identities created by hand."
      />
      <Card>
        <div className="flex flex-wrap items-center gap-3 border-b border-slate-200 p-4">
          <FilterBox label="Search by name or email" value={search} onChange={setSearch} />
          <label className="flex items-center gap-2 text-sm">
            <span className="text-slate-500">Status</span>
            <Select value={status} onChange={(e) => setStatus(e.target.value)} className="w-40">
              <option value="">Any</option>
              <option value="active">Active</option>
              <option value="pending">Starting soon</option>
              <option value="suspended">Suspended</option>
              <option value="terminated">Left</option>
            </Select>
          </label>
        </div>
        {isPending ? (
          <p className="p-6 text-sm text-slate-500">Loading people…</p>
        ) : error ? (
          <p role="alert" className="p-6 text-sm text-red-700">
            Could not load people: {error.message}
          </p>
        ) : people.length === 0 ? (
          <p className="p-6 text-sm text-slate-500">
            {q || status
              ? "Nobody matches."
              : "There is nobody here yet. Connect an HR source to bring people in."}
          </p>
        ) : (
          <>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>Name</TableHead>
                  <TableHead>Status</TableHead>
                  <TableHead>Comes from</TableHead>
                  <TableHead>Started</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {people.map((p) => (
                  <TableRow key={p.id}>
                    <TableCell>
                      <Link
                        to={`/people/${p.id}`}
                        className="font-medium text-brand-700 hover:underline"
                      >
                        {p.display_name}
                      </Link>
                      {p.email && <div className="text-slate-500">{p.email}</div>}
                    </TableCell>
                    <TableCell>
                      <IdentityStatusBadge status={p.status} />
                    </TableCell>
                    <TableCell className="text-slate-600">
                      {p.source_id ? `HR, employee ${p.external_id}` : "Created by hand"}
                    </TableCell>
                    <TableCell className="whitespace-nowrap">
                      {p.start_date ? formatDate(p.start_date) : "—"}
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            <ShowMore query={list} />
          </>
        )}
      </Card>
    </>
  );
}
