import { useQuery } from "@tanstack/react-query";
import { Search } from "lucide-react";
import { useState } from "react";
import { PageHeader } from "@/components/PageHeader";
import { Expiry, Origin } from "@/components/AccessBadges";
import { Eligibilities } from "@/components/Eligibilities";
import { Card } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { getIdentityAccess, type Me } from "@/lib/api";
import { formatDate } from "@/lib/utils";

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
      <Eligibilities identityId={me.identity_id} self canManage={false} />
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
