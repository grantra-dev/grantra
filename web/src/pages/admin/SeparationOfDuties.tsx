import { useMutation, useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link } from "react-router";
import { Loaded } from "@/components/Loaded";
import { RiskBadge } from "@/components/RiskBadge";
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
import {
  createSoDPolicy,
  deleteSoDPolicy,
  exceptSoDViolation,
  listCatalogApplications,
  listEntitlements,
  listRoles,
  listSoDPolicies,
  listSoDViolations,
  scanSoD,
  type NewSoDPolicy,
  type SoDPolicy,
  type SoDRef,
  type SoDViolation,
} from "@/lib/admin";
import { formatDate } from "@/lib/utils";

export function SeparationOfDuties() {
  return (
    <div className="flex flex-col gap-6">
      <Policies />
      <Violations />
    </div>
  );
}

const modeLabels = {
  detect: "Detect: flag people who have both",
  prevent: "Prevent: also refuse requests that would cause it",
};

function sideNames(refs: SoDRef[]) {
  return refs.map((r) => r.name ?? r.role_id ?? r.entitlement_id).join(", ");
}

function Policies() {
  const client = useQueryClient();
  const [adding, setAdding] = useState(false);
  const { data, error, isPending } = useQuery({
    queryKey: ["sod-policies"],
    queryFn: listSoDPolicies,
  });
  const remove = useMutation({
    mutationFn: (p: SoDPolicy) => deleteSoDPolicy(p.id),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["sod-policies"] }),
  });
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold">Policies</h2>
          <p className="text-sm text-slate-500">
            Pairs of access nobody should hold together, like creating and approving payments.
          </p>
        </div>
        {!adding && (
          <Button variant="outline" onClick={() => setAdding(true)}>
            New policy
          </Button>
        )}
      </div>
      {adding && <NewPolicy onDone={() => setAdding(false)} />}
      {remove.error && <Alert variant="error">{remove.error.message}</Alert>}
      <Loaded loading={isPending} error={error} empty={data?.length === 0 && "No policies yet."}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Policy</TableHead>
              <TableHead>One side</TableHead>
              <TableHead>Other side</TableHead>
              <TableHead>Mode</TableHead>
              <TableHead className="relative">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((p) => (
              <TableRow key={p.id} className="align-top">
                <TableCell>
                  <div className="flex items-center gap-2 font-medium">
                    {p.name} <RiskBadge risk={p.severity} />
                  </div>
                  {!p.enabled && <Badge className="mt-1">Off</Badge>}
                  {p.description && <div className="text-slate-500">{p.description}</div>}
                </TableCell>
                <TableCell>{sideNames(p.side_a)}</TableCell>
                <TableCell>{sideNames(p.side_b)}</TableCell>
                <TableCell>
                  <Badge variant={p.mode === "prevent" ? "red" : "brand"}>
                    {p.mode === "prevent" ? "Prevent" : "Detect"}
                  </Badge>
                </TableCell>
                <TableCell className="text-right">
                  <Button
                    size="sm"
                    variant="ghost"
                    disabled={remove.isPending}
                    onClick={() => {
                      if (
                        window.confirm(`Delete the policy ${p.name}? Its violations go with it.`)
                      ) {
                        remove.mutate(p);
                      }
                    }}
                  >
                    Delete
                  </Button>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </section>
  );
}

/** Every role and entitlement, to pick the two sides of a policy from. */
function useAccessOptions() {
  const apps = useQuery({ queryKey: ["catalog-applications"], queryFn: listCatalogApplications });
  const roles = useQuery({ queryKey: ["roles"], queryFn: listRoles });
  const ents = useQueries({
    queries: (apps.data ?? [])
      .filter((a) => a.connector !== "grantra")
      .map((a) => ({
        queryKey: ["entitlements", a.id],
        queryFn: () => listEntitlements(a.id),
      })),
  });
  return {
    roles: roles.data ?? [],
    entitlements: ents.flatMap((q) => q.data ?? []),
  };
}

function SidePicker({
  id,
  label,
  value,
  onChange,
}: {
  id: string;
  label: string;
  value: string[];
  onChange: (v: string[]) => void;
}) {
  const { roles, entitlements } = useAccessOptions();
  const byApp = new Map<string, typeof entitlements>();
  for (const e of entitlements)
    byApp.set(e.application_name, [...(byApp.get(e.application_name) ?? []), e]);
  return (
    <div>
      <Label htmlFor={id}>{label}</Label>
      <select
        id={id}
        multiple
        required
        value={value}
        onChange={(e) => onChange(Array.from(e.target.selectedOptions, (o) => o.value))}
        className="h-40 w-full rounded-md border border-slate-300 bg-white p-1 text-sm focus:outline-2 focus:outline-brand-600"
      >
        {roles.length > 0 && (
          <optgroup label="Business roles">
            {roles.map((r) => (
              <option key={r.id} value={`role:${r.id}`}>
                {r.name}
              </option>
            ))}
          </optgroup>
        )}
        {[...byApp].map(([app, list]) => (
          <optgroup key={app} label={app}>
            {list.map((e) => (
              <option key={e.id} value={`entitlement:${e.id}`}>
                {e.name}
              </option>
            ))}
          </optgroup>
        ))}
      </select>
      <p className="mt-1 text-xs text-slate-500">Hold Ctrl or ⌘ to pick several.</p>
    </div>
  );
}

const toRefs = (picked: string[]): SoDRef[] =>
  picked.map((p) => {
    const [kind, id] = p.split(":");
    return kind === "role" ? { role_id: id } : { entitlement_id: id };
  });

function NewPolicy({ onDone }: { onDone: () => void }) {
  const client = useQueryClient();
  const [name, setName] = useState("");
  const [mode, setMode] = useState<NewSoDPolicy["mode"]>("detect");
  const [severity, setSeverity] = useState<NewSoDPolicy["severity"]>("high");
  const [a, setA] = useState<string[]>([]);
  const [b, setB] = useState<string[]>([]);
  const create = useMutation({
    mutationFn: () =>
      createSoDPolicy({ name, mode, severity, side_a: toRefs(a), side_b: toRefs(b) }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["sod-policies"] });
      onDone();
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>New policy</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-4 md:grid-cols-3"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <div>
            <Label htmlFor="sod-name">Name</Label>
            <Input
              id="sod-name"
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Payments"
            />
          </div>
          <div>
            <Label htmlFor="sod-mode">Mode</Label>
            <Select
              id="sod-mode"
              value={mode}
              onChange={(e) => setMode(e.target.value as NewSoDPolicy["mode"])}
            >
              <option value="detect">{modeLabels.detect}</option>
              <option value="prevent">{modeLabels.prevent}</option>
            </Select>
          </div>
          <div>
            <Label htmlFor="sod-severity">Severity</Label>
            <Select
              id="sod-severity"
              value={severity}
              onChange={(e) => setSeverity(e.target.value as NewSoDPolicy["severity"])}
            >
              <option value="low">Low</option>
              <option value="medium">Medium</option>
              <option value="high">High</option>
              <option value="critical">Critical</option>
            </Select>
          </div>
          <div className="md:col-span-3 grid gap-4 md:grid-cols-2">
            <SidePicker id="sod-a" label="Nobody holding any of these…" value={a} onChange={setA} />
            <SidePicker id="sod-b" label="…may also hold any of these" value={b} onChange={setB} />
          </div>
          {create.error && (
            <Alert variant="error" className="md:col-span-3">
              {create.error.message}
            </Alert>
          )}
          <div className="flex gap-2 md:col-span-3">
            <Button type="submit" disabled={create.isPending}>
              Add policy
            </Button>
            <Button type="button" variant="ghost" onClick={onDone}>
              Cancel
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function Violations() {
  const client = useQueryClient();
  const [status, setStatus] = useState<SoDViolation["status"]>("open");
  const { data, error, isPending } = useQuery({
    queryKey: ["sod-violations", status],
    queryFn: () => listSoDViolations(status),
  });
  const scan = useMutation({
    mutationFn: scanSoD,
    onSuccess: () => void client.invalidateQueries({ queryKey: ["sod-violations"] }),
  });
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="font-semibold">Violations</h2>
          <p className="text-sm text-slate-500">
            People who hold both sides of a policy. Grantra checks every 30 minutes.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Select
            aria-label="Show violations"
            value={status}
            onChange={(e) => setStatus(e.target.value as SoDViolation["status"])}
            className="w-44"
          >
            <option value="open">Open</option>
            <option value="excepted">Accepted risks</option>
            <option value="resolved">Resolved</option>
          </Select>
          <Button variant="outline" disabled={scan.isPending} onClick={() => scan.mutate()}>
            {scan.isPending ? "Checking…" : "Check now"}
          </Button>
        </div>
      </div>
      {scan.error && <Alert variant="error">{scan.error.message}</Alert>}
      {scan.data && (
        <Alert variant="success">
          Checked: {scan.data.detected} new, {scan.data.resolved} resolved,{" "}
          {scan.data.exceptions_expired} accepted risks expired, {scan.data.open} open.
        </Alert>
      )}
      <Loaded
        loading={isPending}
        error={error}
        empty={data?.length === 0 && (status === "open" ? "No open violations." : "Nothing here.")}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Person</TableHead>
              <TableHead>Policy</TableHead>
              <TableHead>Since</TableHead>
              <TableHead>{status === "excepted" ? "Accepted risk" : ""}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((v) => (
              <TableRow key={v.id} className="align-top">
                <TableCell>
                  <Link to={`/people/${v.identity_id}`} className="text-brand-700 hover:underline">
                    {v.identity_name}
                  </Link>
                </TableCell>
                <TableCell>
                  <div className="flex items-center gap-2">
                    {v.policy_name} <RiskBadge risk={v.severity} />
                  </div>
                </TableCell>
                <TableCell className="whitespace-nowrap">{formatDate(v.detected_at)}</TableCell>
                <TableCell>
                  {v.status === "open" && <AcceptRisk violation={v} />}
                  {v.status === "excepted" && (
                    <div className="text-sm">
                      <div>{v.exception_reason}</div>
                      {v.exception_until && (
                        <div className="text-slate-500">Until {formatDate(v.exception_until)}</div>
                      )}
                      {v.request_id && (
                        <div className="text-slate-500">Accepted on an access request</div>
                      )}
                    </div>
                  )}
                  {v.status === "resolved" && v.resolved_at && (
                    <span className="text-slate-500">Resolved {formatDate(v.resolved_at)}</span>
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

function AcceptRisk({ violation }: { violation: SoDViolation }) {
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const [reason, setReason] = useState("");
  const [days, setDays] = useState(30);
  const accept = useMutation({
    mutationFn: () => exceptSoDViolation(violation.id, reason, days),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["sod-violations"] }),
  });
  if (!open) {
    return (
      <Button size="sm" variant="outline" onClick={() => setOpen(true)}>
        Accept the risk
      </Button>
    );
  }
  return (
    <form
      className="flex flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        accept.mutate();
      }}
    >
      <Input
        aria-label={`Why ${violation.identity_name} may keep both`}
        required
        value={reason}
        onChange={(e) => setReason(e.target.value)}
        placeholder="Why, and what checks make up for it"
      />
      <div className="flex items-center gap-2">
        <Select
          aria-label="For how long"
          value={days}
          onChange={(e) => setDays(Number(e.target.value))}
          className="w-32"
        >
          {[7, 30, 90, 180, 365].map((d) => (
            <option key={d} value={d}>
              {d} days
            </option>
          ))}
        </Select>
        <Button size="sm" type="submit" disabled={accept.isPending}>
          Accept
        </Button>
        <Button size="sm" type="button" variant="ghost" onClick={() => setOpen(false)}>
          Cancel
        </Button>
      </div>
      {accept.error && <Alert variant="error">{accept.error.message}</Alert>}
    </form>
  );
}
