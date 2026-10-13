import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useState } from "react";
import { PageHeader } from "@/components/PageHeader";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { getIdentityAccess, type Assignment, type Me } from "@/lib/api";
import { formatDate } from "@/lib/utils";

const soonDays = 14;

export function MyAccess({ me }: { me: Me }) {
  const [filter, setFilter] = useState("");
  const [now] = useState(() => Date.now());
  const { data, error, isPending } = useQuery({
    queryKey: ["identity-access", me.identity_id],
    queryFn: () => getIdentityAccess(me.identity_id),
  });

  const needle = filter.trim().toLowerCase();
  const rows = (data ?? []).filter(
    (a) =>
      !needle ||
      a.name.toLowerCase().includes(needle) ||
      a.application?.toLowerCase().includes(needle),
  );

  return (
    <>
      <PageHeader
        title="My access"
        description="Everything you can access through Grantra, why you have it and when it ends."
      />
      <Card>
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 p-4">
          <label className="relative block w-full max-w-xs">
            <span className="sr-only">Filter by name or application</span>
            <Search
              className="pointer-events-none absolute top-2.5 left-3 size-4 text-slate-400"
              aria-hidden
            />
            <input
              type="search"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Filter by name or application"
              className="h-9 w-full rounded-md border border-slate-300 pr-3 pl-9 text-sm focus:outline-2 focus:outline-brand-600"
            />
          </label>
          {data && (
            <span className="text-sm text-slate-500">
              {data.length === 1 ? "1 access" : `${data.length} accesses`}
            </span>
          )}
        </div>
        {isPending ? (
          <p className="p-6 text-sm text-slate-500">Loading your access…</p>
        ) : error ? (
          <p role="alert" className="p-6 text-sm text-red-700">
            Could not load your access: {error.message}
          </p>
        ) : data.length === 0 ? (
          <p className="p-6 text-sm text-slate-500">
            You have no access through Grantra yet. Birthright access appears here once an HR source
            assigns it, and anything you request appears once it is approved.
          </p>
        ) : rows.length === 0 ? (
          <p className="p-6 text-sm text-slate-500">Nothing matches “{filter}”.</p>
        ) : (
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>Access</TableHead>
                <TableHead>Why you have it</TableHead>
                <TableHead>Since</TableHead>
                <TableHead>Until</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rows.map((a) => (
                <TableRow key={a.id}>
                  <TableCell>
                    <div className="font-medium">{a.name}</div>
                    <div className="text-slate-500">
                      {a.type === "role" ? "Role" : a.application || "Entitlement"}
                    </div>
                  </TableCell>
                  <TableCell>
                    <Origin a={a} />
                  </TableCell>
                  <TableCell className="whitespace-nowrap">{formatDate(a.granted_at)}</TableCell>
                  <TableCell className="whitespace-nowrap">
                    <Expiry at={a.expires_at} now={now} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        )}
      </Card>
    </>
  );
}

function Origin({ a }: { a: Assignment }) {
  switch (a.origin) {
    case "birthright":
      return (
        <div className="flex flex-col items-start gap-1">
          <Badge variant="brand">Birthright</Badge>
          {a.rule_name && <span className="text-slate-500">Rule: {a.rule_name}</span>}
        </div>
      );
    case "request":
      return <Badge variant="green">Requested and approved</Badge>;
    case "manual":
      return <Badge>Granted by an administrator</Badge>;
  }
}

function Expiry({ at, now }: { at?: string; now: number }) {
  if (!at) return <span className="text-slate-500">No end date</span>;
  const soon = new Date(at).getTime() - now < soonDays * 24 * 60 * 60 * 1000;
  return soon ? <Badge variant="amber">{formatDate(at)}</Badge> : <>{formatDate(at)}</>;
}
