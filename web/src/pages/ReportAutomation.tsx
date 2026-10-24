// Governance → Reports: packs Grantra makes on a schedule, and auditors from
// outside the company with access until a date.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Download } from "lucide-react";
import { useState, type FormEvent } from "react";
import { FormActions } from "@/components/FormActions";
import { Loaded } from "@/components/Loaded";
import { Manage } from "@/components/Manage";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { linkButton } from "@/components/ui/button-variants";
import { CardContent } from "@/components/ui/card";
import { Input, Label, Select } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { listCatalogApplications } from "@/lib/admin";
import { listPlatformRoles, PlatformRole } from "@/lib/api";
import {
  deleteSchedule,
  deliveryFile,
  endAuditor,
  extendAuditor,
  inviteAuditor,
  listDeliveries,
  listExternalAuditors,
  listFrameworks,
  listSchedules,
  saveSchedule,
  type AuditorInput,
  type Delivery,
  type ExternalAuditor,
  type ReportDefinition,
  type Schedule,
  type ScheduleInput,
} from "@/lib/reports";
import { useCanManage } from "@/lib/manage";
import { cn, formatTime } from "@/lib/utils";

/** How much each pack covers, by months. */
const spans: Record<number, string> = {
  1: "Every month",
  3: "Every quarter",
  6: "Every half year",
  12: "Every year",
};
const spanText = (months: number) => spans[months] ?? `Every ${months} months`;

/** Today in UTC plus days, as YYYY-MM-DD. */
const dayFromNow = (days: number) =>
  new Date(Date.now() + days * 86_400_000).toISOString().slice(0, 10);

function invalidate(client: ReturnType<typeof useQueryClient>, ...keys: string[]) {
  for (const k of keys) void client.invalidateQueries({ queryKey: [k] });
}

/** Scheduled packs: what each schedule makes, for whom, and the packs it made. */
export function Schedules({ reports }: { reports: ReportDefinition[] }) {
  const client = useQueryClient();
  const [editing, setEditing] = useState<Schedule | "new" | null>(null);
  const schedules = useQuery({ queryKey: ["report-schedules"], queryFn: listSchedules });
  const frameworks = useQuery({ queryKey: ["report-frameworks"], queryFn: listFrameworks });
  const remove = useMutation({
    mutationFn: (s: Schedule) => deleteSchedule(s.id),
    onSuccess: () => invalidate(client, "report-schedules"),
  });
  const what = (s: Schedule) =>
    s.framework
      ? (frameworks.data?.find((f) => f.id === s.framework)?.name ?? s.framework)
      : `${reports.find((r) => r.id === s.report)?.title ?? s.report}${s.application ? `, ${s.application}` : ""}`;
  const form = (s?: Schedule) => (
    <ScheduleForm
      key={s?.id ?? "new"}
      schedule={s}
      reports={reports}
      onDone={() => setEditing(null)}
    />
  );
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-slate-500">
        Grantra makes each pack once its period ends, counted from the first month of the fiscal
        year (Settings → General), and emails the people it goes to a link to download it. Packs go
        only to admins and auditors, and stay here as long as the settings keep them.
      </p>
      <ErrorAlert error={remove.error} />
      {editing === "new" && form()}
      <Loaded
        loading={schedules.isPending}
        error={schedules.error}
        empty={schedules.data?.length === 0 && editing !== "new" && "No scheduled packs yet."}
      >
        <div className="divide-y divide-slate-200">
          {schedules.data?.map((s) =>
            editing !== "new" && editing?.id === s.id ? (
              form(s)
            ) : (
              <section key={s.id} className={cn("p-5", !s.enabled && "opacity-70")}>
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-64 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <h2 className="font-semibold">{s.name}</h2>
                      {!s.enabled && <Badge>Off</Badge>}
                    </div>
                    <p className="text-sm text-slate-500">
                      {what(s)} · {spanText(s.months)} · next: {s.next_from} to {s.next_to}
                    </p>
                    <p className="mt-1 text-sm">
                      To{" "}
                      {s.recipients.map((r, i) => (
                        <span
                          key={r.id}
                          className={cn(!r.allowed && "text-slate-400 line-through")}
                        >
                          {i > 0 && ", "}
                          {r.name || "someone removed"}
                        </span>
                      ))}
                      {s.recipients.some((r) => !r.allowed) && (
                        <span className="text-xs text-slate-500">
                          {" "}
                          (crossed out: no longer an admin or auditor, so they get nothing)
                        </span>
                      )}
                    </p>
                    {s.last && <DeliveryLine d={s.last} />}
                  </div>
                  <Manage>
                    <div className="flex flex-wrap gap-2">
                      <Button variant="ghost" size="sm" onClick={() => setEditing(s)}>
                        Edit
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={remove.isPending}
                        onClick={() => remove.mutate(s)}
                      >
                        Delete
                      </Button>
                    </div>
                  </Manage>
                </div>
              </section>
            ),
          )}
        </div>
      </Loaded>
      <Manage>
        {editing !== "new" && (
          <div>
            <Button variant="outline" onClick={() => setEditing("new")}>
              Schedule a pack
            </Button>
          </div>
        )}
      </Manage>
      <Deliveries />
    </div>
  );
}

/** The last pack of a schedule: its download, or why it could not be made. */
function DeliveryLine({ d }: { d: Delivery }) {
  return d.status === "ready" ? (
    <p className="mt-2 flex flex-wrap items-center gap-2 text-sm text-slate-600">
      Last pack: {d.from} to {d.to}, made {formatTime(d.created_at)}
      <a className={linkButton} href={deliveryFile(d.id)} download>
        <Download aria-hidden /> Download
      </a>
    </p>
  ) : (
    <Alert variant="error" className="mt-2">
      The pack for {d.from} to {d.to} could not be made: {d.error}
    </Alert>
  );
}

/** Every pack schedules made, newest first. */
function Deliveries() {
  const { data, error, isPending } = useQuery({
    queryKey: ["report-deliveries"],
    queryFn: listDeliveries,
  });
  return (
    <section className="flex flex-col gap-2">
      <h2 className="font-semibold">Packs made</h2>
      <Loaded loading={isPending} error={error} empty={data?.length === 0 && "None yet."}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Pack</TableHead>
              <TableHead>Period</TableHead>
              <TableHead>Made</TableHead>
              <TableHead>Sent to</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((d) => (
              <TableRow key={d.id}>
                <TableCell className="font-medium">{d.title}</TableCell>
                <TableCell className="whitespace-nowrap">
                  {d.from} to {d.to}
                </TableCell>
                <TableCell className="whitespace-nowrap">{formatTime(d.created_at)}</TableCell>
                <TableCell>{d.recipients}</TableCell>
                <TableCell className="text-right">
                  {d.status === "ready" ? (
                    <a className={linkButton} href={deliveryFile(d.id)} download>
                      <Download aria-hidden /> {(d.bytes / 1024).toFixed(0)} KB
                    </a>
                  ) : (
                    <Badge variant="red" title={d.error}>
                      Failed
                    </Badge>
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

/** Adds a schedule, or edits one. */
function ScheduleForm({
  schedule,
  reports,
  onDone,
}: {
  schedule?: Schedule;
  reports: ReportDefinition[];
  onDone: () => void;
}) {
  const client = useQueryClient();
  const [s, setS] = useState<ScheduleInput>({
    name: schedule?.name ?? "",
    report: schedule?.report ?? "",
    framework: schedule?.framework ?? "",
    application_id: schedule?.application_id ?? "",
    months: schedule?.months ?? 3,
    recipients: schedule?.recipients.filter((r) => r.allowed).map((r) => r.id) ?? [],
    enabled: schedule?.enabled ?? true,
  });
  const frameworks = useQuery({ queryKey: ["report-frameworks"], queryFn: listFrameworks });
  const roles = useQuery({ queryKey: ["platform-roles"], queryFn: listPlatformRoles });
  const def = reports.find((r) => r.id === s.report);
  const apps = useQuery({
    queryKey: ["catalog-applications"],
    queryFn: listCatalogApplications,
    enabled: !!def?.application,
  });
  // Admins and auditors, once each.
  const people = (roles.data ?? [])
    .filter((r) => r.name === PlatformRole.Admin || r.name === PlatformRole.Auditor)
    .flatMap((r) => r.members)
    .filter(
      (m, i, all) =>
        m.status === "active" && all.findIndex((x) => x.identity_id === m.identity_id) === i,
    );
  const save = useMutation({
    mutationFn: () => saveSchedule(schedule?.id, s),
    onSuccess: () => {
      invalidate(client, "report-schedules");
      onDone();
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  const target = s.framework ? `framework:${s.framework}` : s.report ? `report:${s.report}` : "";
  return (
    <form
      onSubmit={submit}
      className="flex flex-col gap-4 border-y border-slate-200 bg-slate-50 p-5"
    >
      <h2 className="font-semibold">{schedule ? `Edit ${schedule.name}` : "Schedule a pack"}</h2>
      <div className="grid gap-3 md:grid-cols-2">
        <div>
          <Label htmlFor="schedule-name">Name</Label>
          <Input
            id="schedule-name"
            required
            maxLength={200}
            placeholder="SOC 2 for the external auditor"
            value={s.name}
            onChange={(e) => setS({ ...s, name: e.target.value })}
          />
        </div>
        <div>
          <Label htmlFor="schedule-what">What</Label>
          <Select
            id="schedule-what"
            required
            value={target}
            onChange={(e) => {
              const [kind, id = ""] = e.target.value.split(":");
              setS({
                ...s,
                framework: kind === "framework" ? id : "",
                report: kind === "report" ? id : "",
                application_id: "",
              });
            }}
          >
            <option value="">Pick a framework or a report</option>
            <optgroup label="Frameworks (every report its controls need)">
              {frameworks.data
                ?.filter((f) => f.enabled)
                .map((f) => (
                  <option key={f.id} value={`framework:${f.id}`}>
                    {f.name}
                  </option>
                ))}
            </optgroup>
            <optgroup label="Reports">
              {reports.map((r) => (
                <option key={r.id} value={`report:${r.id}`}>
                  {r.title}
                </option>
              ))}
            </optgroup>
          </Select>
        </div>
        {def?.application && (
          <div>
            <Label htmlFor="schedule-app">Application</Label>
            <Select
              id="schedule-app"
              value={s.application_id}
              onChange={(e) => setS({ ...s, application_id: e.target.value })}
            >
              <option value="">All applications</option>
              {apps.data?.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </Select>
          </div>
        )}
        <div>
          <Label htmlFor="schedule-months">How often</Label>
          <Select
            id="schedule-months"
            value={s.months}
            onChange={(e) => setS({ ...s, months: Number(e.target.value) })}
          >
            {Array.from({ length: 12 }, (_, i) => i + 1).map((m) => (
              <option key={m} value={m}>
                {spanText(m)}
              </option>
            ))}
          </Select>
        </div>
      </div>
      <fieldset>
        <legend className="mb-1 text-sm font-medium">Send it to</legend>
        <div className="flex flex-wrap gap-x-4 gap-y-1">
          {people.map((m) => (
            <label key={m.identity_id} className="flex items-center gap-1.5 text-sm">
              <input
                type="checkbox"
                checked={s.recipients.includes(m.identity_id)}
                onChange={(e) =>
                  setS({
                    ...s,
                    recipients: e.target.checked
                      ? [...s.recipients, m.identity_id]
                      : s.recipients.filter((x) => x !== m.identity_id),
                  })
                }
              />
              {m.display_name}
            </label>
          ))}
        </div>
        <p className="mt-1 text-xs text-slate-500">
          Admins and auditors. Someone from outside the company can be invited under External
          auditors.
        </p>
      </fieldset>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={s.enabled}
          onChange={(e) => setS({ ...s, enabled: e.target.checked })}
        />
        On
      </label>
      <FormActions
        action={save}
        submit={schedule ? "Save" : "Schedule it"}
        disabled={!target || s.recipients.length === 0}
        onCancel={onDone}
      />
    </form>
  );
}

/** Auditors from outside the company, with access until their last day. */
export function ExternalAuditors() {
  const client = useQueryClient();
  const [inviting, setInviting] = useState(false);
  const [invited, setInvited] = useState<{ name: string; loginError?: string } | null>(null);
  const { data, error, isPending } = useQuery({
    queryKey: ["external-auditors"],
    queryFn: listExternalAuditors,
  });
  const end = useMutation({
    mutationFn: (a: ExternalAuditor) => endAuditor(a.id),
    onSuccess: () => invalidate(client, "external-auditors"),
  });
  return (
    <div className="flex flex-col gap-4">
      <p className="text-sm text-slate-500">
        An auditor from outside the company gets the auditor role until their last day: they read
        every report and the audit log and change nothing, and every download they make is recorded.
        The day after, they become a leaver and their access ends on its own. Requests they make go
        to whoever invited them, and rules that give access to everyone never apply to them.
      </p>
      {invited && (
        <Alert variant={invited.loginError ? "warning" : "success"}>
          {invited.name} is invited.{" "}
          {invited.loginError
            ? `No Grantra Login account was made: ${invited.loginError}.`
            : "Grantra Login emailed them a link to set their password."}
        </Alert>
      )}
      <ErrorAlert error={end.error} />
      {inviting && (
        <InviteForm
          onDone={(a) => {
            setInviting(false);
            if (a) setInvited(a);
          }}
        />
      )}
      <Loaded
        loading={isPending}
        error={error}
        empty={data?.length === 0 && !inviting && "Nobody from outside has been invited."}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Auditor</TableHead>
              <TableHead>Last day</TableHead>
              <TableHead>Invited by</TableHead>
              <TableHead>Grantra Login</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((a) => (
              <TableRow key={a.id}>
                <TableCell>
                  <div className="font-medium">{a.name}</div>
                  <div className="text-xs text-slate-500">
                    {a.email}
                    {a.company && ` · ${a.company}`}
                  </div>
                </TableCell>
                <TableCell className="whitespace-nowrap">
                  {a.status === "terminated" ? (
                    <Badge>Ended {a.until}</Badge>
                  ) : (
                    <LastDay auditor={a} />
                  )}
                </TableCell>
                <TableCell>{a.sponsor || "—"}</TableCell>
                <TableCell>{a.login ? <Badge>{a.login}</Badge> : "—"}</TableCell>
                <TableCell className="text-right">
                  {a.status !== "terminated" && (
                    <Manage>
                      <Button
                        variant="ghost"
                        size="sm"
                        disabled={end.isPending}
                        onClick={() => end.mutate(a)}
                      >
                        End now
                      </Button>
                    </Manage>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
      <Manage>
        {!inviting && (
          <div>
            <Button variant="outline" onClick={() => setInviting(true)}>
              Invite an auditor
            </Button>
          </div>
        )}
      </Manage>
    </div>
  );
}

/** An auditor's last day, which admins can move. */
function LastDay({ auditor }: { auditor: ExternalAuditor }) {
  const client = useQueryClient();
  const [until, setUntil] = useState(auditor.until);
  const move = useMutation({
    mutationFn: () => extendAuditor(auditor.id, until),
    onSuccess: () => invalidate(client, "external-auditors"),
  });
  if (!useCanManage()) return <span className="text-sm">{auditor.until}</span>;
  return (
    <>
      <form
        className="flex items-center gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          move.mutate();
        }}
      >
        <Input
          type="date"
          aria-label={`Last day of ${auditor.name}`}
          className="h-8 w-40"
          min={dayFromNow(0)}
          value={until}
          onChange={(e) => setUntil(e.target.value)}
        />
        <Button size="sm" variant="outline" disabled={until === auditor.until || move.isPending}>
          Move
        </Button>
      </form>
      <ErrorAlert error={move.error} className="mt-1" />
    </>
  );
}

function InviteForm({
  onDone,
}: {
  onDone: (invited?: { name: string; loginError?: string }) => void;
}) {
  const client = useQueryClient();
  const [a, setA] = useState<AuditorInput>({
    name: "",
    email: "",
    company: "",
    until: dayFromNow(30),
    login: true,
  });
  const invite = useMutation({
    mutationFn: () => inviteAuditor(a),
    onSuccess: (out) => {
      invalidate(client, "external-auditors", "platform-roles");
      onDone({ name: out.name, loginError: a.login ? out.login_error : undefined });
    },
  });
  return (
    <CardContent className="rounded-lg border border-slate-200 bg-slate-50 pt-5">
      <form
        className="grid gap-3 md:grid-cols-2"
        onSubmit={(e) => {
          e.preventDefault();
          invite.mutate();
        }}
      >
        <h2 className="font-semibold md:col-span-2">Invite an auditor</h2>
        <div>
          <Label htmlFor="auditor-name">Name</Label>
          <Input
            id="auditor-name"
            required
            maxLength={200}
            value={a.name}
            onChange={(e) => setA({ ...a, name: e.target.value })}
          />
        </div>
        <div>
          <Label htmlFor="auditor-email">Email</Label>
          <Input
            id="auditor-email"
            type="email"
            required
            value={a.email}
            onChange={(e) => setA({ ...a, email: e.target.value })}
          />
        </div>
        <div>
          <Label htmlFor="auditor-company">Company</Label>
          <Input
            id="auditor-company"
            maxLength={200}
            placeholder="The audit firm"
            value={a.company}
            onChange={(e) => setA({ ...a, company: e.target.value })}
          />
        </div>
        <div>
          <Label htmlFor="auditor-until">Last day</Label>
          <Input
            id="auditor-until"
            type="date"
            required
            min={dayFromNow(0)}
            value={a.until}
            onChange={(e) => setA({ ...a, until: e.target.value })}
          />
          <p className="mt-1 text-xs text-slate-500">
            At most 90 days ahead by default (Settings → General → Compliance reports).
          </p>
        </div>
        <label className="flex items-center gap-2 text-sm md:col-span-2">
          <input
            type="checkbox"
            checked={a.login}
            onChange={(e) => setA({ ...a, login: e.target.checked })}
          />
          Give them a Grantra Login account: they get an email to set a password, and it is disabled
          when their access ends
        </label>
        <FormActions
          className="md:col-span-2"
          action={invite}
          submit="Invite"
          onCancel={() => onDone()}
        />
      </form>
    </CardContent>
  );
}
