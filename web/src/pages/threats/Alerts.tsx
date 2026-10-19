import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Loaded } from "@/components/Loaded";
import { Manage } from "@/components/Manage";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { Me } from "@/lib/api";
import {
  closeThreatAlert,
  getThreatAlert,
  listThreatAlerts,
  respondToThreat,
  responseLabels,
  severities,
  type ResponseAction,
  type ThreatAlert,
  type ThreatResponse,
  capitalize,
  evidenceLabel,
  evidenceValue,
} from "@/lib/threats";
import { cn, formatTime } from "@/lib/utils";
import { SeverityBadge } from "./shared";

const pageSize = 50;

/** The alerts, newest seen first; ?alert=<id> opens one. */
export function Alerts({ me }: { me: Me }) {
  const [params, setParams] = useSearchParams();
  const selected = params.get("alert");
  const [status, setStatus] = useState("open");
  const [severity, setSeverity] = useState("");
  const alerts = useInfiniteQuery({
    queryKey: ["threat-alerts", status, severity],
    queryFn: ({ pageParam }) =>
      listThreatAlerts({ status: status || undefined, severity, before: pageParam || undefined }),
    initialPageParam: "",
    getNextPageParam: (last) => (last.length < pageSize ? undefined : last[last.length - 1]!.id),
  });
  const rows = alerts.data?.pages.flat() ?? [];
  const open = (id: string | null) => setParams(id ? { alert: id } : {});
  return (
    <div className="flex flex-col gap-4">
      {selected && <AlertDetail id={selected} me={me} onClose={() => open(null)} />}
      <div className="flex flex-wrap gap-3">
        <div>
          <Label htmlFor="alert-status">Status</Label>
          <Select id="alert-status" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="open">Open</option>
            <option value="resolved">Resolved</option>
            <option value="dismissed">Dismissed</option>
            <option value="">Any</option>
          </Select>
        </div>
        <div>
          <Label htmlFor="alert-severity">Severity</Label>
          <Select
            id="alert-severity"
            value={severity}
            onChange={(e) => setSeverity(e.target.value)}
          >
            <option value="">Any</option>
            {severities.map((s) => (
              <option key={s} value={s}>
                {capitalize(s)}
              </option>
            ))}
          </Select>
        </div>
      </div>
      <Loaded
        loading={alerts.isPending}
        error={alerts.error}
        empty={
          rows.length === 0 &&
          (status === "open"
            ? "No open alert. Grantra looks at new sign-ins and access every minute."
            : "No alert here.")
        }
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Severity</TableHead>
              <TableHead>What</TableHead>
              <TableHead>Who</TableHead>
              <TableHead>Last seen</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((a) => (
              <TableRow
                key={a.id}
                className={cn("cursor-pointer align-top", a.id === selected && "bg-brand-50")}
                onClick={() => open(a.id)}
              >
                <TableCell>
                  <SeverityBadge severity={a.severity} />
                </TableCell>
                <TableCell className="text-sm">
                  <button
                    type="button"
                    className="text-left font-medium hover:underline"
                    onClick={(e) => {
                      e.stopPropagation();
                      open(a.id);
                    }}
                  >
                    {a.title}
                  </button>
                  <div className="text-slate-500">{a.summary}</div>
                </TableCell>
                <TableCell className="text-sm">{a.identity_name ?? a.subject}</TableCell>
                <TableCell className="text-sm whitespace-nowrap text-slate-500">
                  {formatTime(a.last_seen_at)}
                  {a.occurrences > 1 && <div>{a.occurrences} times</div>}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        {alerts.hasNextPage && (
          <div className="p-3 text-center">
            <Button
              variant="ghost"
              size="sm"
              disabled={alerts.isFetchingNextPage}
              onClick={() => void alerts.fetchNextPage()}
            >
              Show more
            </Button>
          </div>
        )}
      </Loaded>
    </div>
  );
}

const statusBadges = {
  open: <Badge variant="amber">Open</Badge>,
  resolved: <Badge variant="green">Resolved</Badge>,
  dismissed: <Badge>Dismissed</Badge>,
};

function AlertDetail({ id, me, onClose }: { id: string; me: Me; onClose: () => void }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["threat-alert", id],
    queryFn: () => getThreatAlert(id),
  });
  return (
    <Card aria-label="Alert">
      {isPending ? (
        <p className="p-6 text-sm text-slate-500">Loading…</p>
      ) : error ? (
        <p role="alert" className="p-6 text-sm text-red-700">
          Could not load this alert: {error.message}
        </p>
      ) : (
        <>
          <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-3">
            <div>
              <CardTitle className="flex flex-wrap items-center gap-2">
                {data.title} <SeverityBadge severity={data.severity} /> {statusBadges[data.status]}
              </CardTitle>
              <p className="mt-1 text-sm text-slate-600">{data.summary}</p>
            </div>
            <Button variant="ghost" size="sm" onClick={onClose}>
              Close
            </Button>
          </CardHeader>
          <CardContent className="flex flex-col gap-5">
            <Facts alert={data} />
            {data.responses && data.responses.length > 0 && (
              <Responses responses={data.responses} />
            )}
            {data.status === "open" &&
              (data.identity_id === me.identity_id ? (
                <Alert variant="info">
                  This alert is about you, so another admin looks into it and acts on it.
                </Alert>
              ) : (
                <Manage>
                  {data.identity_id && <Respond alert={data} />}
                  <CloseAlert alert={data} />
                </Manage>
              ))}
          </CardContent>
        </>
      )}
    </Card>
  );
}

function Facts({ alert: a }: { alert: ThreatAlert }) {
  const evidence = Object.entries(a.evidence ?? {}).filter(([k]) => !k.endsWith("_id"));
  return (
    <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-[auto_1fr]">
      <dt className="text-slate-500">About</dt>
      <dd>
        {a.identity_id ? (
          <Link to={`/people/${a.identity_id}`} className="text-brand-600 hover:underline">
            {a.identity_name ?? a.subject}
          </Link>
        ) : (
          a.subject
        )}
        {a.identity_status && a.identity_status !== "active" && (
          <Badge variant="amber" className="ml-2">
            {capitalize(a.identity_status)}
          </Badge>
        )}
      </dd>
      <dt className="text-slate-500">Seen</dt>
      <dd>
        {formatTime(a.first_seen_at)}
        {a.occurrences > 1 &&
          ` and ${a.occurrences - 1} more times, last ${formatTime(a.last_seen_at)}`}
      </dd>
      {evidence.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="text-slate-500">{evidenceLabel(k)}</dt>
          <dd className="break-words">{evidenceValue(v)}</dd>
        </div>
      ))}
      {a.closed_at && (
        <>
          <dt className="text-slate-500">{capitalize(a.status)}</dt>
          <dd>
            {formatTime(a.closed_at)}
            {a.closed_by && ` by ${a.closed_by}`}
            {a.close_note && `: “${a.close_note}”`}
          </dd>
        </>
      )}
    </dl>
  );
}

function Responses({ responses }: { responses: ThreatResponse[] }) {
  return (
    <section>
      <h3 className="mb-2 text-sm font-semibold">What was done</h3>
      <ul className="flex flex-col gap-1 text-sm">
        {responses.map((r) => (
          <li key={r.id}>
            <span className="font-medium">{responseLabels[r.action]}</span>{" "}
            <span className="text-slate-500">
              by {r.actor === "system:itdr" ? "Grantra, on its own" : r.actor} ·{" "}
              {formatTime(r.created_at)}
            </span>
            {r.reason && <span className="text-slate-500"> · “{r.reason}”</span>}
            {r.error && <div className="text-red-700">Part of it failed: {r.error}</div>}
          </li>
        ))}
      </ul>
    </section>
  );
}

const respondHelp: Record<ResponseAction, string> = {
  revoke_sessions:
    "Signs them out of Grantra Login and the console, ends their server and gateway sessions and checks in what they took from the vault.",
  require_mfa:
    "Asks for a second factor at every sign-in for a while, and signs them out so it applies now. Needs a Grantra Login sign-in policy that asks for one.",
  suspend:
    "Ends their sessions and turns their Grantra Login account off until an admin reinstates them. Their access in Grantra is kept, so reinstating gives it back.",
  reinstate: "Turns their Grantra Login account back on so they can sign in again.",
};

function Respond({ alert: a }: { alert: ThreatAlert }) {
  const client = useQueryClient();
  const suspended = a.identity_status === "suspended";
  const actions: ResponseAction[] = suspended
    ? ["revoke_sessions", "reinstate"]
    : ["revoke_sessions", "require_mfa", "suspend"];
  const [action, setAction] = useState<ResponseAction>("revoke_sessions");
  const [reason, setReason] = useState("");
  const [days, setDays] = useState(7);
  const respond = useMutation({
    mutationFn: (act: ResponseAction) =>
      respondToThreat(a.identity_id!, {
        action: act,
        alert_id: a.id,
        reason,
        days: act === "require_mfa" ? days : undefined,
      }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["threat-alert", a.id] });
      void client.invalidateQueries({ queryKey: ["threat-alerts"] });
    },
  });
  const current = actions.includes(action) ? action : actions[0]!;
  return (
    <form
      aria-label="Respond"
      className="flex flex-col gap-3 rounded-md border border-slate-200 p-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (
          current === "suspend" &&
          !window.confirm(
            `Suspend ${a.identity_name ?? "this person"}? They cannot sign in until reinstated.`,
          )
        )
          return;
        respond.mutate(current);
      }}
    >
      <h3 className="text-sm font-semibold">Respond</h3>
      <div className="flex flex-wrap items-end gap-3">
        <div>
          <Label htmlFor="respond-action">Action</Label>
          <Select
            id="respond-action"
            value={current}
            onChange={(e) => setAction(e.target.value as ResponseAction)}
          >
            {actions.map((r) => (
              <option key={r} value={r}>
                {responseLabels[r]}
              </option>
            ))}
          </Select>
        </div>
        {current === "require_mfa" && (
          <div>
            <Label htmlFor="respond-days">For days</Label>
            <Input
              id="respond-days"
              type="number"
              min={1}
              max={90}
              className="w-24"
              value={days}
              onChange={(e) => setDays(Number(e.target.value))}
            />
          </div>
        )}
        <div className="min-w-48 flex-1">
          <Label htmlFor="respond-reason">Reason</Label>
          <Input
            id="respond-reason"
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            placeholder="Sign-in from a country they never work from"
          />
        </div>
        <Button
          type="submit"
          variant={current === "suspend" ? "destructive" : "default"}
          disabled={respond.isPending}
        >
          {responseLabels[current]}
        </Button>
      </div>
      <p className="text-xs text-slate-500">{respondHelp[current]}</p>
      {respond.error && <Alert variant="error">{respond.error.message}</Alert>}
      {respond.data && (
        <Alert variant={respond.data.error ? "warning" : "success"}>
          Done: {responseLabels[respond.data.action]}.
          {respond.data.error && ` Part of it failed: ${respond.data.error}`}
        </Alert>
      )}
    </form>
  );
}

function CloseAlert({ alert: a }: { alert: ThreatAlert }) {
  const client = useQueryClient();
  const [note, setNote] = useState("");
  const close = useMutation({
    mutationFn: (status: "resolved" | "dismissed") => closeThreatAlert(a.id, status, note),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["threat-alert", a.id] });
      void client.invalidateQueries({ queryKey: ["threat-alerts"] });
      void client.invalidateQueries({ queryKey: ["threats"] });
      void client.invalidateQueries({ queryKey: ["inbox"] });
    },
  });
  return (
    <form
      aria-label="Close the alert"
      className="mt-3 flex flex-col gap-3 rounded-md border border-slate-200 p-4"
      onSubmit={(e) => e.preventDefault()}
    >
      <h3 className="text-sm font-semibold">Close the alert</h3>
      <div>
        <Label htmlFor="close-note">Note</Label>
        <Input
          id="close-note"
          value={note}
          onChange={(e) => setNote(e.target.value)}
          placeholder="Ana is at the Lisbon office this week"
        />
        <p className="mt-1 text-xs text-slate-500">
          Dismissing needs a note saying why it was not a threat. If the same thing happens again, a
          new alert opens.
        </p>
      </div>
      <div className="flex flex-wrap gap-2">
        <Button type="button" disabled={close.isPending} onClick={() => close.mutate("resolved")}>
          Resolve
        </Button>
        <Button
          type="button"
          variant="outline"
          disabled={close.isPending || !note.trim()}
          onClick={() => close.mutate("dismissed")}
        >
          Dismiss
        </Button>
      </div>
      {close.error && <Alert variant="error">{close.error.message}</Alert>}
    </form>
  );
}
