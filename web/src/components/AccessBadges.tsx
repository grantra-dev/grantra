import { ChevronDown, ChevronRight } from "lucide-react";
import { Fragment, useState } from "react";
import { RequestFlow } from "@/components/ApprovalFlow";
import { Badge } from "@/components/ui/badge";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { Assignment, IdentityStatus } from "@/lib/api";
import { formatDate } from "@/lib/utils";

const soonDays = 14;

/** Why someone has an access. */
export function Origin({ a }: { a: Assignment }) {
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
    case "activation":
      return (
        <div className="flex flex-col items-start gap-1">
          <Badge variant="amber">Activated for now</Badge>
          {a.justification && <span className="text-slate-500">{a.justification}</span>}
        </div>
      );
  }
}

/** When an access ends, in amber when that is soon. */
export function Expiry({ at, now }: { at?: string; now: number }) {
  if (!at) return <span className="text-slate-500">No end date</span>;
  const soon = new Date(at).getTime() - now < soonDays * 24 * 60 * 60 * 1000;
  return soon ? <Badge variant="amber">{formatDate(at)}</Badge> : <>{formatDate(at)}</>;
}

const statusVariants = {
  pending: "brand",
  active: "green",
  suspended: "amber",
  terminated: "red",
} as const;

const statusLabels = {
  pending: "Starting soon",
  active: "Active",
  suspended: "Suspended",
  terminated: "Left",
};

export function IdentityStatusBadge({ status }: { status: IdentityStatus }) {
  return <Badge variant={statusVariants[status]}>{statusLabels[status]}</Badge>;
}

/**
 * Someone's access: what, why they have it ("you" or "they"), since and
 * until when. Access granted by a request opens the request's approval flow.
 */
export function AccessTable({ rows, who }: { rows: Assignment[]; who: "you" | "they" }) {
  const [now] = useState(() => Date.now());
  const [open, setOpen] = useState<string>();
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Access</TableHead>
          <TableHead>Why {who} have it</TableHead>
          <TableHead>Since</TableHead>
          <TableHead>Until</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {rows.map((a) => (
          <Fragment key={a.id}>
            <TableRow>
              <TableCell>
                <div className="font-medium">{a.name}</div>
                <div className="text-slate-500">
                  {a.type === "role" ? "Role" : a.application || "Entitlement"}
                </div>
              </TableCell>
              <TableCell>
                <Origin a={a} />
                {a.request_id && (
                  <button
                    type="button"
                    className="mt-1 flex items-center gap-1 text-xs text-brand-700 hover:underline"
                    aria-expanded={open === a.id}
                    onClick={() => setOpen(open === a.id ? undefined : a.id)}
                  >
                    {open === a.id ? (
                      <ChevronDown className="size-3.5" aria-hidden />
                    ) : (
                      <ChevronRight className="size-3.5" aria-hidden />
                    )}
                    How it was approved
                  </button>
                )}
              </TableCell>
              <TableCell className="whitespace-nowrap">{formatDate(a.granted_at)}</TableCell>
              <TableCell className="whitespace-nowrap">
                <Expiry at={a.expires_at} now={now} />
              </TableCell>
            </TableRow>
            {open === a.id && a.request_id && (
              <TableRow>
                <TableCell colSpan={4} className="bg-slate-50">
                  <RequestFlow id={a.request_id} />
                </TableCell>
              </TableRow>
            )}
          </Fragment>
        ))}
      </TableBody>
    </Table>
  );
}
