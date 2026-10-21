import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Users, X } from "lucide-react";
import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router";
import { PlanSteps } from "@/components/ApprovalFlow";
import { FilterBox } from "@/components/FilterBox";
import { PageHeader } from "@/components/PageHeader";
import { RiskBadge } from "@/components/RiskBadge";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Label, Select, Textarea } from "@/components/ui/field";
import { listEligibilities } from "@/lib/jit";
import { hasWords } from "@/lib/utils";
import {
  createRequest,
  getIdentityAccess,
  listCatalog,
  listMyRequests,
  previewRequest,
  targetOf,
  type Me,
  type Requestable,
} from "@/lib/api";

export function RequestAccess({ me }: { me: Me }) {
  // The search box and the home page link here with what to look for.
  const [params] = useSearchParams();
  const [filter, setFilter] = useState(params.get("q") ?? "");
  const [selected, setSelected] = useState<Requestable | null>(null);
  const catalog = useQuery({ queryKey: ["requestable", "team"], queryFn: listCatalog });
  const teamSize = catalog.data?.team_size ?? 0;
  const access = useQuery({
    queryKey: ["identity-access", me.identity_id],
    queryFn: () => getIdentityAccess(me.identity_id),
  });
  const mine = useQuery({ queryKey: ["my-requests"], queryFn: listMyRequests });
  const eligible = useQuery({
    queryKey: ["eligibilities", me.identity_id],
    queryFn: () => listEligibilities(me.identity_id),
  });

  const held = new Set(access.data?.map((a) => a.target_id));
  const canActivate = new Set(eligible.data?.map((e) => e.target_id));
  const pending = new Set(
    mine.data
      ?.filter((r) => r.status === "pending" && r.beneficiary_id === me.identity_id)
      .map((r) => r.target_id),
  );

  const needle = filter.trim();
  const items = (catalog.data?.items ?? []).filter((i) =>
    hasWords(needle, i.name, i.application, i.description),
  );

  return (
    <>
      <PageHeader
        title="Request access"
        description="Find what you need and say why. You see who approves it before you send it."
      />
      <div className="grid items-start gap-6 lg:grid-cols-[minmax(0,1fr)_24rem]">
        <Card>
          <div className="border-b border-slate-200 p-4">
            <FilterBox
              label="Search the catalog"
              placeholder="Search by name, application or description"
              value={filter}
              onChange={setFilter}
              className="max-w-sm"
            />
          </div>
          {catalog.isPending ? (
            <p className="p-6 text-sm text-slate-500">Loading the catalog…</p>
          ) : catalog.error ? (
            <p role="alert" className="p-6 text-sm text-red-700">
              Could not load the catalog: {catalog.error.message}
            </p>
          ) : items.length === 0 ? (
            <p className="p-6 text-sm text-slate-500">
              {needle
                ? `Nothing matches “${filter}”.`
                : "Nothing can be requested yet. Your admins add applications to the catalog."}
            </p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {items.map((i) => (
                <li key={i.id} className="flex items-start justify-between gap-4 p-4">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{i.name}</span>
                      <RiskBadge risk={i.risk} />
                      {i.jit_only && <Badge variant="amber">Just in time</Badge>}
                    </div>
                    <div className="text-sm text-slate-500">
                      {i.type === "role" ? "Role" : i.application}
                    </div>
                    {i.description ? (
                      <p className="mt-1 text-sm text-slate-600">{i.description}</p>
                    ) : (
                      <p className="mt-1 text-sm text-slate-400 italic">
                        No description yet. If the name tells you nothing, ask the owner before
                        requesting it.
                      </p>
                    )}
                    {!!i.team_holders && (
                      <p className="mt-1 flex items-center gap-1 text-xs text-slate-500">
                        <Users className="size-3.5" aria-hidden />
                        {teamText(i.team_holders, teamSize)}
                      </p>
                    )}
                  </div>
                  {held.has(i.id) ? (
                    <Badge variant="green">You have this</Badge>
                  ) : canActivate.has(i.id) ? (
                    <Badge variant="green">Activate it from My access</Badge>
                  ) : pending.has(i.id) ? (
                    <Badge variant="amber">Requested</Badge>
                  ) : (
                    <Button
                      size="sm"
                      variant={selected?.id === i.id ? "default" : "outline"}
                      onClick={() => setSelected(i)}
                      aria-label={`Request ${i.name}`}
                    >
                      Request
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Card>
        {selected ? (
          <RequestForm key={selected.id} item={selected} onClose={() => setSelected(null)} />
        ) : (
          <Card className="hidden lg:block">
            <CardContent className="pt-5 text-sm text-slate-500">
              Pick something from the catalog to request it.
            </CardContent>
          </Card>
        )}
      </div>
    </>
  );
}

/** How many teammates (people who share your manager) hold something; the API only counts teams of three or more. */
function teamText(holders: number, team: number) {
  if (holders === team) return `All ${team} of your teammates have this`;
  return `${holders} of your ${team} teammates ${holders === 1 ? "has" : "have"} this`;
}

// Durations as "h:<hours>", "d:<days>" or "" for no end date.
const hourOptions = [
  { value: "h:1", label: "1 hour" },
  { value: "h:4", label: "4 hours" },
  { value: "h:8", label: "8 hours" },
];
const dayOptions = [
  { value: "d:1", label: "1 day" },
  { value: "d:7", label: "1 week" },
  { value: "d:30", label: "30 days" },
  { value: "d:90", label: "90 days" },
  { value: "d:180", label: "6 months" },
  { value: "d:365", label: "1 year" },
];
const forever = { value: "", label: "Until it is removed" };

function durationOptions(item: Requestable, kind: "access" | "eligibility") {
  if (kind === "eligibility") return [forever, ...dayOptions];
  // Just-in-time access is never held for good.
  return item.jit_only ? [...hourOptions, ...dayOptions] : [forever, ...hourOptions, ...dayOptions];
}

function RequestForm({ item, onClose }: { item: Requestable; onClose: () => void }) {
  const [justification, setJustification] = useState("");
  // For just-in-time access, being eligible is the usual ask.
  const [kind, setKind] = useState<"access" | "eligibility">(
    item.jit_only ? "eligibility" : "access",
  );
  const options = durationOptions(item, kind);
  const [duration, setDuration] = useState(options[0]!.value);
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const target = targetOf(item);

  const [unit, n] = duration.split(":");
  const asked = {
    ...target,
    kind,
    duration_days: unit === "d" ? Number(n) : undefined,
    duration_hours: unit === "h" ? Number(n) : undefined,
  };
  // Who approves can depend on the kind of request and how long it is for.
  const check = useQuery({
    queryKey: ["request-preview", item.id, kind, duration],
    queryFn: () => previewRequest(asked),
    placeholderData: (previous) => previous,
  });
  const conflicts = check.data?.sod_conflicts ?? [];

  const submit = useMutation({
    mutationFn: () => createRequest({ ...asked, justification: justification.trim() }),
    onSuccess: async (req) => {
      await queryClient.invalidateQueries({ queryKey: ["my-requests"] });
      navigate("/requests", { state: { created: req.target_name } });
    },
  });

  const blocked = check.isPending;
  return (
    <Card className="lg:sticky lg:top-5">
      <CardHeader className="flex-row items-start justify-between">
        <div>
          <CardTitle>Request {item.name}</CardTitle>
          <CardDescription>{item.type === "role" ? "Role" : item.application}</CardDescription>
        </div>
        <Button variant="ghost" size="sm" onClick={onClose} aria-label="Close">
          <X />
        </Button>
      </CardHeader>
      <CardContent>
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            submit.mutate();
          }}
        >
          {check.error && (
            <Alert variant="error">
              Could not check who approves it and separation of duties: {check.error.message}
            </Alert>
          )}
          {conflicts.length > 0 && (
            <Alert variant="warning">
              Together with what you already have, this access breaks the separation of duties{" "}
              {conflicts.length === 1 ? "policy" : "policies"}{" "}
              <strong>{conflicts.join(", ")}</strong>. You can still send the request: after your
              approvers, a Grantra admin has to accept the risk, with a reason and an end date, or
              the request is rejected. Explain why you need both.
            </Alert>
          )}
          <div>
            <Label htmlFor="justification">Why do you need it?</Label>
            <Textarea
              id="justification"
              required
              maxLength={2000}
              value={justification}
              onChange={(e) => setJustification(e.target.value)}
              placeholder="For example: I am joining the Q3 billing project"
            />
            <p className="mt-1 text-xs text-slate-500">
              Your approvers and the auditors read this.
            </p>
          </div>
          {item.jit_only && (
            <fieldset>
              <legend className="mb-1 text-sm font-medium">What do you need?</legend>
              <p className="mb-2 text-xs text-slate-500">Nobody holds this access all the time.</p>
              {(
                [
                  ["eligibility", "To be eligible: activate it for a few hours when needed"],
                  ["access", "The access itself, once, for a set time"],
                ] as const
              ).map(([k, label]) => (
                <label key={k} className="flex items-start gap-2 py-1 text-sm">
                  <input
                    type="radio"
                    name="kind"
                    className="mt-1"
                    checked={kind === k}
                    onChange={() => {
                      setKind(k);
                      setDuration(durationOptions(item, k)[0]!.value);
                    }}
                  />
                  {label}
                </label>
              ))}
            </fieldset>
          )}
          <div>
            <Label htmlFor="duration">
              {kind === "eligibility" ? "Eligible for how long?" : "For how long?"}
            </Label>
            <Select id="duration" value={duration} onChange={(e) => setDuration(e.target.value)}>
              {options.map((d) => (
                <option key={d.value} value={d.value}>
                  {d.label}
                </option>
              ))}
            </Select>
          </div>
          {check.data && (
            <section aria-label="Who approves it">
              <h3 className="mb-2 text-sm font-medium">Who approves it</h3>
              <PlanSteps plan={check.data.plan} conflicts={conflicts} />
            </section>
          )}
          {submit.error && <Alert variant="error">{submit.error.message}</Alert>}
          <Button type="submit" disabled={blocked || !justification.trim() || submit.isPending}>
            {submit.isPending ? "Sending…" : "Send request"}
          </Button>
        </form>
      </CardContent>
    </Card>
  );
}
