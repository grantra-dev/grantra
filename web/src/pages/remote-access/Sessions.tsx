import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  isCloud,
  kindText,
  listGatewaySessions,
  searchGatewayEvents,
  terminateGatewaySession,
  type GatewaySession,
} from "@/lib/gateway";
import { endReasonText } from "@/lib/ssh";
import { formatTime } from "@/lib/utils";

/** Bytes, in a unit people read. */
function sizeText(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
}

export function Sessions({ admin }: { admin: boolean }) {
  const client = useQueryClient();
  const [active, setActive] = useState(false);
  const { data, error, isPending } = useQuery({
    queryKey: ["gateway-sessions", active],
    queryFn: () => listGatewaySessions({ active }),
  });
  const end = useMutation({
    mutationFn: terminateGatewaySession,
    onSuccess: () => client.invalidateQueries({ queryKey: ["gateway-sessions"] }),
  });
  return (
    <section className="flex flex-col gap-3">
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={active} onChange={(e) => setActive(e.target.checked)} />
        Only running sessions
      </label>
      {end.error && <Alert variant="error">{end.error.message}</Alert>}
      <Loaded loading={isPending} error={error} empty={data?.length === 0 && "No sessions yet."}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Who</TableHead>
              <TableHead>Target</TableHead>
              <TableHead>Started</TableHead>
              <TableHead>Logged</TableHead>
              <TableHead>Status</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((s) => (
              <TableRow key={s.id}>
                <TableCell>{s.identity_name}</TableCell>
                <TableCell>
                  {s.target_name}
                  <div className="text-xs text-slate-500">
                    {kindText[s.kind]}
                    {s.account && ` as ${s.account}`}
                    {s.database && ` on ${s.database}`}
                  </div>
                </TableCell>
                <TableCell>
                  {formatTime(s.started_at)}
                  {!s.ended_at && s.valid_before && (
                    <div className="text-xs text-slate-500">
                      ends by {formatTime(s.valid_before)}
                    </div>
                  )}
                </TableCell>
                <TableCell>
                  {s.kind === "rdp"
                    ? sizeText(s.recording_bytes)
                    : isCloud(s.kind)
                      ? "—"
                      : s.events}
                </TableCell>
                <TableCell>
                  <GatewaySessionStatus s={s} />
                </TableCell>
                <TableCell className="flex justify-end gap-2">
                  {!isCloud(s.kind) && (
                    <Link
                      to={`/remote-access/sessions/${s.id}`}
                      className="text-sm text-brand-700 underline"
                    >
                      {s.kind === "rdp" ? "Recording" : "Log"}
                    </Link>
                  )}
                  {admin && !s.ended_at && !s.terminate_requested && s.kind !== "aws" && (
                    <Button
                      size="sm"
                      variant="destructive"
                      onClick={() => end.mutate(s.id)}
                      disabled={end.isPending}
                    >
                      End
                    </Button>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </section>
  );
}

export function GatewaySessionStatus({ s }: { s: GatewaySession }) {
  if (!s.ended_at) {
    return <Badge variant="amber">{s.terminate_requested ? "Ending…" : "Running"}</Badge>;
  }
  const reason = s.end_reason ?? "closed";
  return (
    <Badge variant={reason === "closed" ? "default" : "red"} title={s.error}>
      {reason === "recording_full"
        ? s.kind === "rdp"
          ? "Recording limit reached"
          : "Log limit reached"
        : endReasonText[reason]}
    </Badge>
  );
}

export function Queries() {
  const [q, setQ] = useState("");
  const [asked, setAsked] = useState("");
  const { data, error, isFetching } = useQuery({
    queryKey: ["gateway-events", asked],
    queryFn: () => searchGatewayEvents(asked),
    enabled: asked !== "",
  });
  return (
    <section className="flex flex-col gap-3">
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          setAsked(q.trim());
        }}
      >
        <Input
          aria-label="Query"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="DELETE FROM"
        />
        <Button type="submit" disabled={!q.trim()}>
          Search
        </Button>
      </form>
      <p className="text-xs text-slate-500">
        SQL statements as the database got them, and cluster requests (such as{" "}
        <code>DELETE /api/v1/namespaces/shop/pods/web</code>). What goes on inside kubectl exec is
        not logged.
      </p>
      {asked && (
        <Loaded
          loading={isFetching}
          error={error}
          empty={data?.length === 0 && `Nothing matches “${asked}”.`}
        >
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>Who</TableHead>
                <TableHead>Target</TableHead>
                <TableHead>Query or request</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {data?.map((ev) => (
                <TableRow key={ev.id}>
                  <TableCell>{formatTime(ev.at)}</TableCell>
                  <TableCell>{ev.identity_name}</TableCell>
                  <TableCell>{ev.target_name}</TableCell>
                  <TableCell>
                    <Link
                      to={`/remote-access/sessions/${ev.session_id}`}
                      className="break-all font-mono text-xs text-brand-700 underline"
                    >
                      {ev.text}
                    </Link>
                    {ev.status !== undefined && (
                      <span className="ml-2 text-xs text-slate-500">{ev.status}</span>
                    )}
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Loaded>
      )}
    </section>
  );
}
