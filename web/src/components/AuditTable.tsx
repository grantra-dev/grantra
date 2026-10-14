import { Link } from "react-router";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { AuditEvent } from "@/lib/api";
import { actionLabel, describeActor } from "@/lib/audit";
import { formatTime } from "@/lib/utils";

export function AuditTable({ events }: { events: AuditEvent[] }) {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>When</TableHead>
          <TableHead>Who</TableHead>
          <TableHead>What</TableHead>
          <TableHead>On</TableHead>
          <TableHead>Details</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        {events.map((e) => {
          const actor = describeActor(e);
          return (
            <TableRow key={e.id} className="align-top">
              <TableCell className="whitespace-nowrap text-slate-600">
                {formatTime(e.occurred_at)}
              </TableCell>
              <TableCell>
                {actor.identityId ? (
                  <PersonLink id={actor.identityId}>{actor.label}</PersonLink>
                ) : (
                  actor.label
                )}
              </TableCell>
              <TableCell>
                <div className="font-medium">{actionLabel(e.action)}</div>
                <div className="font-mono text-xs text-slate-400">{e.action}</div>
              </TableCell>
              <TableCell>
                <Target e={e} />
              </TableCell>
              <TableCell>
                <Details details={e.details} />
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );
}

function PersonLink({ id, children }: { id: string; children: React.ReactNode }) {
  return (
    <Link to={`/people/${id}`} className="text-brand-700 hover:underline">
      {children}
    </Link>
  );
}

function Target({ e }: { e: AuditEvent }) {
  if (!e.target_type) return <span className="text-slate-400">—</span>;
  if (e.target_type === "identity" && e.target_id) {
    return <PersonLink id={e.target_id}>{e.target_name || "A deleted identity"}</PersonLink>;
  }
  return (
    <div>
      <div>{actionLabel(e.target_type)}</div>
      {e.target_id && (
        <div className="font-mono text-xs break-all text-slate-400">{e.target_id}</div>
      )}
    </div>
  );
}

function Details({ details }: { details: Record<string, unknown> }) {
  if (!details || Object.keys(details).length === 0) {
    return <span className="text-slate-400">—</span>;
  }
  return (
    <details>
      <summary className="cursor-pointer text-sm text-brand-700">Show</summary>
      <pre className="mt-2 max-w-md overflow-x-auto rounded bg-slate-50 p-2 text-xs">
        {JSON.stringify(details, null, 2)}
      </pre>
    </details>
  );
}
