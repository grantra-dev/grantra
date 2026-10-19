import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
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
import { endReasonText, type SessionEnd } from "@/lib/ssh";
import { formatTime } from "@/lib/utils";

/** What SSH and remote access sessions have in common. */
interface RecordedSession {
  id: string;
  identity_name: string;
  ended_at?: string;
  end_reason?: SessionEnd;
  error?: string;
  terminate_requested: boolean;
}

/** A column: its heading and what a row shows in it. */
export type Column<T> = [heading: string, cell: (row: T) => ReactNode];

/** Whether a session runs, or how it ended; full names a full recording. */
export function SessionBadge({ s, full }: { s: RecordedSession; full?: string }) {
  if (!s.ended_at) {
    return <Badge variant="amber">{s.terminate_requested ? "Ending…" : "Running"}</Badge>;
  }
  const reason = s.end_reason ?? "closed";
  return (
    <Badge variant={reason === "closed" ? "default" : "red"} title={s.error}>
      {reason === "recording_full" && full ? full : endReasonText[reason]}
    </Badge>
  );
}

/**
 * The sessions of a gateway, newest first, with who ran them, what columns
 * says, their status, a link to the recording and, for admins, a button to
 * end a running one.
 */
export function SessionList<S extends RecordedSession>(props: {
  name: string;
  list: (f: { active: boolean }) => Promise<S[]>;
  terminate: (id: string) => Promise<unknown>;
  admin: boolean;
  columns: Column<S>[];
  status: (s: S) => ReactNode;
  link: (s: S) => ReactNode;
  /** Whether an admin may end this running session; all of them otherwise. */
  endable?: (s: S) => boolean;
}) {
  const { name, list, terminate, admin, columns, status, link, endable } = props;
  const client = useQueryClient();
  const [active, setActive] = useState(false);
  const { data, error, isPending } = useQuery({
    queryKey: [name, active],
    queryFn: () => list({ active }),
  });
  const end = useMutation({
    mutationFn: terminate,
    onSuccess: () => client.invalidateQueries({ queryKey: [name] }),
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
              {columns.map(([heading]) => (
                <TableHead key={heading}>{heading}</TableHead>
              ))}
              <TableHead>Status</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((s) => (
              <TableRow key={s.id}>
                <TableCell>{s.identity_name}</TableCell>
                {columns.map(([heading, cell]) => (
                  <TableCell key={heading}>{cell(s)}</TableCell>
                ))}
                <TableCell>{status(s)}</TableCell>
                <TableCell className="flex justify-end gap-2">
                  {link(s)}
                  {admin && !s.ended_at && !s.terminate_requested && (endable?.(s) ?? true) && (
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

/** Something typed in a recorded session: a command, a query, a request. */
interface Recorded {
  id: number;
  at: string;
  identity_name?: string;
}

/**
 * Searches what people typed through a gateway: a box, a note on what is
 * recorded, then when, who and the columns for each match.
 */
export function RecordedSearch<R extends Recorded>(props: {
  name: string;
  search: (q: string) => Promise<R[]>;
  label: string;
  placeholder: string;
  note: ReactNode;
  empty: (asked: string) => string;
  columns: Column<R>[];
}) {
  const { name, search, label, placeholder, note, empty, columns } = props;
  const [q, setQ] = useState("");
  const [asked, setAsked] = useState("");
  const { data, error, isFetching } = useQuery({
    queryKey: [name, asked],
    queryFn: () => search(asked),
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
          aria-label={label}
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder={placeholder}
        />
        <Button type="submit" disabled={!q.trim()}>
          Search
        </Button>
      </form>
      <p className="text-xs text-slate-500">{note}</p>
      {asked && (
        <Loaded loading={isFetching} error={error} empty={data?.length === 0 && empty(asked)}>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>When</TableHead>
                <TableHead>Who</TableHead>
                {columns.map(([heading]) => (
                  <TableHead key={heading}>{heading}</TableHead>
                ))}
              </TableRow>
            </TableHeader>
            <TableBody>
              {data?.map((row) => (
                <TableRow key={row.id}>
                  <TableCell>{formatTime(row.at)}</TableCell>
                  <TableCell>{row.identity_name}</TableCell>
                  {columns.map(([heading, cell]) => (
                    <TableCell key={heading}>{cell(row)}</TableCell>
                  ))}
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Loaded>
      )}
    </section>
  );
}
