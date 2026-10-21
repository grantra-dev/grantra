import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, Plus, Trash2, X } from "lucide-react";
import { useState } from "react";
import { PlanSteps } from "@/components/ApprovalFlow";
import { FormActions } from "@/components/FormActions";
import { Loaded } from "@/components/Loaded";
import { Manage } from "@/components/Manage";
import { PersonSearch, PickPerson, type Picked } from "@/components/PersonSearch";
import { ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import { listCatalogApplications, listRoles, refOf, type Risk } from "@/lib/admin";
import type { OnTimeout, RequestKind } from "@/lib/api";
import {
  approverLabels,
  deleteApprovalPolicy,
  listApprovalPolicies,
  reorderApprovalPolicies,
  saveApprovalPolicy,
  simulateApprovals,
  stepTitle,
  timeoutLabels,
  whoText,
  type ApprovalPolicy,
  type Approver,
  type Conditions,
  type PolicyInput,
  type PolicyStep,
} from "@/lib/approvals";
import { formatDate } from "@/lib/utils";
import { AccessOptions } from "./shared";

const kindNames: Record<RequestKind, string> = {
  access: "the access itself",
  eligibility: "eligibility",
  activation: "activations",
};
const risks: Risk[] = ["low", "medium", "high", "critical"];
const identityKinds = {
  human: "people",
  service: "service accounts",
  agent: "AI agents",
  workload: "workloads",
};

/** Names of the applications and roles the policies point at. */
function useNames() {
  const apps = useQuery({ queryKey: ["catalog-applications"], queryFn: listCatalogApplications });
  const roles = useQuery({ queryKey: ["roles"], queryFn: listRoles });
  const name = (list: { id: string; name: string }[] | undefined, id: string) =>
    list?.find((x) => x.id === id)?.name ?? "an item that no longer exists";
  return {
    apps: (apps.data ?? []).filter((a) => a.connector !== "grantra"),
    roles: roles.data ?? [],
    app: (id: string) => name(apps.data, id),
    role: (id: string) => name(roles.data, id),
  };
}

type Names = ReturnType<typeof useNames>;

/** What a policy is for, in a line: "Payroll · high or critical risk · in Finance". */
function conditionsText(c: Conditions, n: Names) {
  const parts = [
    c.kinds?.length && `Requests for ${c.kinds.map((k) => kindNames[k]).join(" or ")}`,
    c.application_ids?.length && c.application_ids.map(n.app).join(" or "),
    c.role_ids?.length && `Role ${c.role_ids.map(n.role).join(" or ")}`,
    c.risks?.length && `${c.risks.join(" or ")} risk`,
    c.departments?.length && `for ${c.departments.join(" or ")}`,
    c.identity_kinds?.length &&
      `for ${c.identity_kinds.map((k) => identityKinds[k as keyof typeof identityKinds] ?? k).join(" or ")}`,
    c.longer_than_days && `longer than ${c.longer_than_days} days, or with no end`,
  ].filter(Boolean);
  return parts.length ? parts.join(" · ") : "Every request";
}

/** Who a step of a policy asks, in a few words. */
function stepText(s: PolicyStep, n: Names) {
  const title = stepTitle(s);
  const how = s.all ? "all" : s.required && s.required > 1 ? `${s.required}` : "";
  switch (s.approver) {
    case "people": {
      const names = s.people?.map((p) => p.name) ?? [];
      return `${title}: ${whoText(names, s.all ? names.length : (s.required ?? 1))}`;
    }
    case "holders":
      return `${title}: ${how ? `${how} of ` : "any of "}the holders of ${n.role(s.role_id ?? "")}`;
    default:
      return title;
  }
}

/** Governance → Approval flows: who approves what people ask for. */
export function ApprovalFlows() {
  const [editing, setEditing] = useState<ApprovalPolicy | "new">();
  return (
    <div className="flex flex-col gap-6">
      <p className="text-sm text-slate-500">
        Grantra tries the policies from the top and the first one that fits a request decides who
        approves it; when none fits, the built-in flow does. Whatever a policy says, nobody approves
        their own access, a separation of duties risk always needs an admin, and only low risk
        access can go with no step. Requests already sent keep the flow they started with.
      </p>
      {editing ? (
        <PolicyEditor
          key={editing === "new" ? "new" : editing.id}
          policy={editing === "new" ? undefined : editing}
          onDone={() => setEditing(undefined)}
        />
      ) : (
        <PolicyList onEdit={setEditing} />
      )}
      {!editing && (
        <Card>
          <CardHeader>
            <CardTitle>Try it</CardTitle>
          </CardHeader>
          <CardContent>
            <Simulator />
          </CardContent>
        </Card>
      )}
    </div>
  );
}

function PolicyList({ onEdit }: { onEdit: (p: ApprovalPolicy | "new") => void }) {
  const client = useQueryClient();
  const names = useNames();
  const { data, error, isPending } = useQuery({
    queryKey: ["approval-policies"],
    queryFn: listApprovalPolicies,
  });
  const refresh = () => client.invalidateQueries({ queryKey: ["approval-policies"] });
  const reorder = useMutation({ mutationFn: reorderApprovalPolicies, onSuccess: refresh });
  const remove = useMutation({ mutationFn: deleteApprovalPolicy, onSuccess: refresh });
  const policies = data?.policies ?? [];
  const move = (i: number, by: number) => {
    const ids = policies.map((p) => p.id);
    [ids[i], ids[i + by]] = [ids[i + by]!, ids[i]!];
    reorder.mutate(ids);
  };
  return (
    <section className="flex flex-col gap-3" aria-label="Approval policies">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h2 className="font-semibold">Policies, in the order Grantra tries them</h2>
        <Manage>
          <Button variant="outline" onClick={() => onEdit("new")}>
            <Plus /> New policy
          </Button>
        </Manage>
      </div>
      <ErrorAlert error={reorder.error ?? remove.error} />
      <Loaded loading={isPending} error={error}>
        <ol className="divide-y divide-slate-100">
          {policies.map((p, i) => (
            <li key={p.id} className="flex flex-wrap items-start justify-between gap-3 p-4">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2 font-medium">
                  <span className="text-slate-400">{i + 1}.</span> {p.name}
                  {!p.enabled && <Badge>Off</Badge>}
                </div>
                {p.description && <p className="text-sm text-slate-500">{p.description}</p>}
                <p className="text-sm">
                  <span className="text-slate-500">When:</span>{" "}
                  {conditionsText(p.conditions, names)}
                </p>
                <p className="text-sm">
                  <span className="text-slate-500">Approved by:</span>{" "}
                  {p.steps.length
                    ? p.steps.map((s) => stepText(s, names)).join(" → ")
                    : "nobody, granted at once"}
                </p>
                <p className="text-xs text-slate-500">
                  Version {p.version}, changed {formatDate(p.updated_at)}
                </p>
              </div>
              <Manage>
                <div className="flex items-center gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={`Move ${p.name} up`}
                    disabled={i === 0 || reorder.isPending}
                    onClick={() => move(i, -1)}
                  >
                    <ArrowUp />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={`Move ${p.name} down`}
                    disabled={i === policies.length - 1 || reorder.isPending}
                    onClick={() => move(i, 1)}
                  >
                    <ArrowDown />
                  </Button>
                  <Button size="sm" variant="outline" onClick={() => onEdit(p)}>
                    Edit
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    aria-label={`Delete ${p.name}`}
                    disabled={remove.isPending}
                    onClick={() => {
                      if (
                        window.confirm(
                          `Delete the approval policy ${p.name}? Requests already sent keep their flow.`,
                        )
                      )
                        remove.mutate(p.id);
                    }}
                  >
                    <Trash2 />
                  </Button>
                </div>
              </Manage>
            </li>
          ))}
          {data && (
            <li className="p-4">
              <div className="font-medium">When no policy fits: the built-in flow</div>
              <p className="text-sm">
                <span className="text-slate-500">Approved by:</span>{" "}
                {data.builtin.steps.map((s) => stepText(s, names)).join(" → ")}
              </p>
              <p className="text-xs text-slate-500">
                To change it, add a policy with no conditions at the end.
              </p>
            </li>
          )}
        </ol>
      </Loaded>
    </section>
  );
}

/** A checkbox per value, for a condition that takes several. */
function Checks<T extends string>({
  legend,
  options,
  value = [],
  onChange,
}: {
  legend: string;
  options: Record<T, string>;
  value?: T[];
  onChange: (v: T[]) => void;
}) {
  return (
    <fieldset>
      <legend className="mb-1 text-sm font-medium">{legend}</legend>
      <div className="flex flex-wrap gap-x-4 gap-y-1">
        {(Object.entries(options) as [T, string][]).map(([k, label]) => (
          <label key={k} className="flex items-center gap-1.5 text-sm">
            <input
              type="checkbox"
              checked={value.includes(k)}
              onChange={(e) =>
                onChange(e.target.checked ? [...value, k] : value.filter((x) => x !== k))
              }
            />
            {label}
          </label>
        ))}
      </div>
    </fieldset>
  );
}

/** A multiple select over a list of names. */
function Many({
  id,
  label,
  options,
  value = [],
  onChange,
}: {
  id: string;
  label: string;
  options: { id: string; name: string }[];
  value?: string[];
  onChange: (v: string[]) => void;
}) {
  return (
    <div>
      <Label htmlFor={id}>{label}</Label>
      <select
        id={id}
        multiple
        value={value}
        onChange={(e) => onChange(Array.from(e.target.selectedOptions, (o) => o.value))}
        className="h-28 w-full rounded-md border border-slate-300 bg-white p-1 text-sm focus:outline-2 focus:outline-brand-600"
      >
        {options.map((o) => (
          <option key={o.id} value={o.id}>
            {o.name}
          </option>
        ))}
      </select>
      <p className="mt-1 text-xs text-slate-500">
        Any of them; none picked means all. Ctrl or ⌘ picks several.
      </p>
    </div>
  );
}

const riskNames = Object.fromEntries(risks.map((r) => [r, r])) as Record<Risk, string>;

function PolicyEditor({ policy, onDone }: { policy?: ApprovalPolicy; onDone: () => void }) {
  const client = useQueryClient();
  const names = useNames();
  const [p, setP] = useState<PolicyInput>(
    policy
      ? {
          name: policy.name,
          description: policy.description,
          enabled: policy.enabled,
          conditions: policy.conditions,
          steps: policy.steps,
        }
      : {
          name: "",
          enabled: true,
          conditions: {},
          steps: [{ approver: "manager" }, { approver: "owner" }],
        },
  );
  const [departments, setDepartments] = useState((p.conditions.departments ?? []).join(", "));
  const c = p.conditions;
  const setC = (patch: Partial<Conditions>) => setP({ ...p, conditions: { ...c, ...patch } });
  const setStep = (i: number, s?: PolicyStep) =>
    setP({
      ...p,
      steps: s ? p.steps.map((x, j) => (j === i ? s : x)) : p.steps.filter((_, j) => j !== i),
    });
  const input = (): PolicyInput => ({
    ...p,
    conditions: {
      ...c,
      departments: departments
        .split(",")
        .map((d) => d.trim())
        .filter(Boolean),
    },
  });
  const save = useMutation({
    mutationFn: () => saveApprovalPolicy(input(), policy?.id),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["approval-policies"] });
      onDone();
    },
  });
  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between">
        <CardTitle>{policy ? `Edit ${policy.name}` : "New approval policy"}</CardTitle>
        <Button variant="ghost" size="sm" onClick={onDone} aria-label="Close">
          <X />
        </Button>
      </CardHeader>
      <CardContent>
        <form
          aria-label="Approval policy"
          className="flex flex-col gap-6"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <div className="grid gap-4 md:grid-cols-2">
            <div>
              <Label htmlFor="policy-name">Name</Label>
              <Input
                id="policy-name"
                required
                maxLength={200}
                value={p.name}
                placeholder="Payroll"
                onChange={(e) => setP({ ...p, name: e.target.value })}
              />
            </div>
            <label className="flex items-center gap-2 self-end text-sm">
              <input
                type="checkbox"
                role="switch"
                checked={p.enabled}
                onChange={(e) => setP({ ...p, enabled: e.target.checked })}
              />
              On: Grantra uses it for new requests
            </label>
            <div className="md:col-span-2">
              <Label htmlFor="policy-description">Description</Label>
              <Textarea
                id="policy-description"
                maxLength={2000}
                value={p.description ?? ""}
                placeholder="Why these people approve it"
                onChange={(e) => setP({ ...p, description: e.target.value })}
              />
            </div>
          </div>

          <section aria-label="When it applies" className="grid gap-4 md:grid-cols-2">
            <h3 className="font-medium md:col-span-2">When it applies</h3>
            <Checks
              legend="Kind of request"
              options={kindNames}
              value={c.kinds}
              onChange={(kinds) => setC({ kinds })}
            />
            <Checks
              legend="Risk of the access"
              options={riskNames}
              value={c.risks}
              onChange={(r) => setC({ risks: r })}
            />
            <Many
              id="policy-apps"
              label="Applications"
              options={names.apps}
              value={c.application_ids}
              onChange={(v) => setC({ application_ids: v })}
            />
            <Many
              id="policy-roles"
              label="Business roles"
              options={names.roles}
              value={c.role_ids}
              onChange={(v) => setC({ role_ids: v })}
            />
            <div>
              <Label htmlFor="policy-departments">Departments of the person it is for</Label>
              <Input
                id="policy-departments"
                value={departments}
                placeholder="Finance, Payroll"
                onChange={(e) => setDepartments(e.target.value)}
              />
              <p className="mt-1 text-xs text-slate-500">Separated by commas, as HR names them.</p>
            </div>
            <Checks
              legend="Who it is for"
              options={identityKinds}
              value={c.identity_kinds as (keyof typeof identityKinds)[]}
              onChange={(v) => setC({ identity_kinds: v })}
            />
            <div>
              <Label htmlFor="policy-longer">Only when asked for longer than (days)</Label>
              <Input
                id="policy-longer"
                type="number"
                min={0}
                max={365}
                className="w-28"
                value={c.longer_than_days ?? ""}
                placeholder="any"
                onChange={(e) => setC({ longer_than_days: e.target.valueAsNumber || undefined })}
              />
            </div>
          </section>

          <section aria-label="Who approves" className="flex flex-col gap-3">
            <h3 className="font-medium">Who approves, in order</h3>
            {p.steps.length === 0 && (
              <p className="text-sm text-slate-500">
                No step: requests are granted at once. Only for a policy limited to low risk access.
              </p>
            )}
            {p.steps.map((s, i) => (
              <StepEditor
                key={i}
                n={i + 1}
                s={s}
                names={names}
                onChange={(next) => setStep(i, next)}
                onMove={(by) => {
                  const steps = [...p.steps];
                  [steps[i], steps[i + by]] = [steps[i + by]!, steps[i]!];
                  setP({ ...p, steps });
                }}
                first={i === 0}
                last={i === p.steps.length - 1}
              />
            ))}
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={p.steps.length >= 10}
                onClick={() => setP({ ...p, steps: [...p.steps, { approver: "owner" }] })}
              >
                <Plus /> Add a step
              </Button>
            </div>
          </section>

          <section aria-label="Try this policy" className="rounded-md border border-slate-200 p-4">
            <h3 className="mb-2 font-medium">Try it before saving</h3>
            {/* A draft needs a name to be checked; it is only shown here. */}
            <Simulator draft={{ ...input(), name: p.name.trim() || "This policy" }} />
          </section>

          <FormActions
            action={save}
            submit={save.isPending ? "Saving…" : "Save policy"}
            disabled={!p.name.trim()}
            onCancel={onDone}
          />
        </form>
      </CardContent>
    </Card>
  );
}

const levels = ["Manager", "Manager's manager", "3 levels up", "4 levels up", "5 levels up"];

function StepEditor({
  n,
  s,
  names,
  onChange,
  onMove,
  first,
  last,
}: {
  n: number;
  s: PolicyStep;
  names: Names;
  onChange: (s?: PolicyStep) => void;
  onMove: (by: number) => void;
  first: boolean;
  last: boolean;
}) {
  const id = `step-${n}`;
  const several = s.approver === "people" || s.approver === "holders";
  const people: Picked[] = s.people ?? [];
  const setPeople = (list: Picked[]) =>
    onChange({ ...s, people: list, people_ids: list.map((x) => x.id) });
  const how = s.all ? "all" : s.required && s.required > 1 ? "some" : "one";
  return (
    <fieldset
      className="grid gap-3 rounded-md border border-slate-200 p-3 md:grid-cols-3"
      aria-label={`Step ${n}`}
    >
      <legend className="px-1 text-sm font-medium">Step {n}</legend>
      <div>
        <Label htmlFor={`${id}-approver`}>Who</Label>
        <Select
          id={`${id}-approver`}
          value={s.approver}
          onChange={(e) =>
            onChange({
              approver: e.target.value as Approver,
              label: s.label,
              timeout_hours: s.timeout_hours,
              on_timeout: s.on_timeout,
            })
          }
        >
          {(Object.keys(approverLabels) as (Approver | "risk")[])
            .filter((k): k is Approver => k !== "risk")
            .map((k) => (
              <option key={k} value={k}>
                {approverLabels[k]}
              </option>
            ))}
        </Select>
      </div>
      {s.approver === "manager" && (
        <div>
          <Label htmlFor={`${id}-level`}>Which manager</Label>
          <Select
            id={`${id}-level`}
            value={s.level ?? 1}
            onChange={(e) => onChange({ ...s, level: Number(e.target.value) })}
          >
            {levels.map((l, i) => (
              <option key={l} value={i + 1}>
                {l}
              </option>
            ))}
          </Select>
        </div>
      )}
      {s.approver === "holders" && (
        <div>
          <Label htmlFor={`${id}-role`}>People who hold the role</Label>
          <Select
            id={`${id}-role`}
            required
            value={s.role_id ?? ""}
            onChange={(e) => onChange({ ...s, role_id: e.target.value })}
          >
            <option value="">Pick a role</option>
            {names.roles.map((r) => (
              <option key={r.id} value={r.id}>
                {r.name}
              </option>
            ))}
          </Select>
        </div>
      )}
      {s.approver === "people" && (
        <div className="md:col-span-2">
          <Label htmlFor={`${id}-people`}>People</Label>
          <ul className="mb-1 flex flex-wrap gap-1">
            {people.map((x) => (
              <li key={x.id}>
                <Badge>
                  {x.name}
                  <button
                    type="button"
                    className="ml-1"
                    aria-label={`Remove ${x.name}`}
                    onClick={() => setPeople(people.filter((y) => y.id !== x.id))}
                  >
                    <X className="size-3" />
                  </button>
                </Badge>
              </li>
            ))}
          </ul>
          <PersonSearch
            id={`${id}-people`}
            action="Add"
            skip={people.map((x) => x.id)}
            onPick={(x) => setPeople([...people, { id: x.id, name: x.display_name }])}
          />
        </div>
      )}
      {several && (
        <div>
          <Label htmlFor={`${id}-how`}>How many must approve</Label>
          <div className="flex gap-2">
            <Select
              id={`${id}-how`}
              value={how}
              onChange={(e) =>
                onChange({
                  ...s,
                  all: e.target.value === "all",
                  required: e.target.value === "some" ? Math.max(s.required ?? 2, 2) : undefined,
                })
              }
            >
              <option value="one">Any one</option>
              <option value="some">A number of them</option>
              <option value="all">All of them</option>
            </Select>
            {how === "some" && (
              <Input
                type="number"
                aria-label="How many"
                className="w-20"
                min={2}
                max={20}
                value={s.required ?? 2}
                onChange={(e) => onChange({ ...s, required: e.target.valueAsNumber })}
              />
            )}
          </div>
        </div>
      )}
      <div>
        <Label htmlFor={`${id}-timeout`}>Hours to decide</Label>
        <Input
          id={`${id}-timeout`}
          type="number"
          min={1}
          max={720}
          className="w-28"
          placeholder="usual"
          value={s.timeout_hours ?? ""}
          onChange={(e) => onChange({ ...s, timeout_hours: e.target.valueAsNumber || undefined })}
        />
      </div>
      <div>
        <Label htmlFor={`${id}-on-timeout`}>If nobody decides in time</Label>
        <Select
          id={`${id}-on-timeout`}
          value={s.on_timeout ?? "expire"}
          onChange={(e) => onChange({ ...s, on_timeout: e.target.value as OnTimeout })}
        >
          {(Object.entries(timeoutLabels) as [OnTimeout, string][])
            .filter(([k]) => k !== "escalate" || s.approver !== "admin")
            .map(([k, label]) => (
              <option key={k} value={k}>
                {label[0]!.toUpperCase() + label.slice(1)}
              </option>
            ))}
        </Select>
      </div>
      <div>
        <Label htmlFor={`${id}-label`}>Name shown to people (optional)</Label>
        <Input
          id={`${id}-label`}
          maxLength={100}
          value={s.label ?? ""}
          placeholder={stepTitle({ ...s, label: undefined })}
          onChange={(e) => onChange({ ...s, label: e.target.value })}
        />
      </div>
      <div className="flex items-end gap-1 md:col-span-3">
        <Button
          type="button"
          size="sm"
          variant="ghost"
          aria-label={`Move step ${n} up`}
          disabled={first}
          onClick={() => onMove(-1)}
        >
          <ArrowUp />
        </Button>
        <Button
          type="button"
          size="sm"
          variant="ghost"
          aria-label={`Move step ${n} down`}
          disabled={last}
          onClick={() => onMove(1)}
        >
          <ArrowDown />
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={() => onChange(undefined)}>
          Remove step
        </Button>
      </div>
    </fieldset>
  );
}

const durations = [
  ["", "No end"],
  ["7", "7 days"],
  ["30", "30 days"],
  ["90", "90 days"],
  ["365", "A year"],
] as const;

/** Who would approve a request, with the saved policies and draft, if given, first. */
function Simulator({ draft }: { draft?: PolicyInput }) {
  const [person, setPerson] = useState<Picked>();
  const [target, setTarget] = useState("");
  const [kind, setKind] = useState<RequestKind>("access");
  const [days, setDays] = useState("");
  const run = useMutation({
    mutationFn: () =>
      simulateApprovals({
        beneficiary_id: person!.id,
        ...refOf(target),
        kind,
        duration_days: days ? Number(days) : undefined,
        policy: draft,
      }),
  });
  return (
    <div className="flex flex-col gap-3">
      <div className="grid gap-3 md:grid-cols-4">
        <div>
          <Label htmlFor={`sim-person${draft ? "-draft" : ""}`}>If</Label>
          <PickPerson
            id={`sim-person${draft ? "-draft" : ""}`}
            value={person}
            onChange={setPerson}
            placeholder="Search a person"
          />
        </div>
        <div>
          <Label htmlFor={`sim-target${draft ? "-draft" : ""}`}>asks for</Label>
          <Select
            id={`sim-target${draft ? "-draft" : ""}`}
            value={target}
            onChange={(e) => setTarget(e.target.value)}
          >
            <option value="">Pick a role or entitlement</option>
            <AccessOptions />
          </Select>
        </div>
        <div>
          <Label htmlFor={`sim-kind${draft ? "-draft" : ""}`}>as</Label>
          <Select
            id={`sim-kind${draft ? "-draft" : ""}`}
            value={kind}
            onChange={(e) => setKind(e.target.value as RequestKind)}
          >
            {(Object.entries(kindNames) as [RequestKind, string][]).map(([k, label]) => (
              <option key={k} value={k}>
                {label}
              </option>
            ))}
          </Select>
        </div>
        <div>
          <Label htmlFor={`sim-days${draft ? "-draft" : ""}`}>for</Label>
          <Select
            id={`sim-days${draft ? "-draft" : ""}`}
            value={days}
            onChange={(e) => setDays(e.target.value)}
          >
            {durations.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </Select>
        </div>
      </div>
      <div>
        <Button
          type="button"
          size="sm"
          disabled={!person || !target || run.isPending}
          onClick={() => run.mutate()}
        >
          Who approves it?
        </Button>
      </div>
      <ErrorAlert error={run.error} />
      {run.data && (
        <div aria-live="polite">
          <PlanSteps plan={run.data.plan} conflicts={run.data.sod_conflicts} />
        </div>
      )}
    </div>
  );
}
