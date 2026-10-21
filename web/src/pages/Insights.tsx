import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Loaded } from "@/components/Loaded";
import { Manage } from "@/components/Manage";
import { RiskBadge } from "@/components/RiskBadge";
import { ShowMore } from "@/components/ShowMore";
import { Tabs } from "@/components/Tabs";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import type { Me } from "@/lib/api";
import { usePages } from "@/lib/hooks";
import {
  createSuggestedRole,
  listRoleSuggestions,
  listUnusedAccess,
  unusedPageSize,
  type RoleSuggestion,
  type UnusedAccess,
} from "@/lib/insights";
import { convertToEligibility, removeEligibility } from "@/lib/jit";
import { unusedDays } from "@/lib/settings";
import { formatDate } from "@/lib/utils";

type Tab = "unused" | "roles";

/** Governance → Insights: access nobody uses, and roles hiding in what people hold. */
export function Insights({ me }: { me: Me }) {
  const [params, setParams] = useSearchParams();
  const tab: Tab = params.get("tab") === "roles" ? "roles" : "unused";
  return (
    <div>
      <Tabs
        tabs={[
          ["unused", "Unused access"],
          ["roles", "Role suggestions"],
        ]}
        current={tab}
        onSelect={(t) => setParams(t === "unused" ? {} : { tab: t })}
      />
      {tab === "unused" ? <Unused days={unusedDays(me)} /> : <Roles />}
    </div>
  );
}

function Unused({ days }: { days?: number }) {
  const access = usePages(
    ["unused-access"],
    (after?: string) => listUnusedAccess(after),
    unusedPageSize,
  );
  const { rows } = access;
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardContent className="pt-5 text-sm text-slate-600">
          Access given by hand or by request, or that someone may turn on, and that they have not
          used in {days ? `${days} days` : "a while"}. Grantra knows when the vault, servers and the
          gateway are used, when eligibilities are turned on, and when people sign in to an
          application whose accounts say so (Active Directory, an account file with a{" "}
          <code>last_login</code> column) or whose sign-ins reach Grantra. Where it cannot tell, it
          says nothing. Reviews show the same findings next to each access.
        </CardContent>
      </Card>
      <Loaded
        loading={access.isPending}
        error={access.error}
        empty={rows.length === 0 && "Everything Grantra can check was used lately."}
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Person</TableHead>
              <TableHead>Access</TableHead>
              <TableHead>Held since</TableHead>
              <TableHead>Last used</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((u) => (
              <UnusedRow key={u.id} u={u} />
            ))}
          </TableBody>
        </Table>
        <ShowMore query={access} />
      </Loaded>
    </div>
  );
}

function UnusedRow({ u }: { u: UnusedAccess }) {
  const client = useQueryClient();
  const fix = useMutation({
    mutationFn: async () => {
      if (u.eligible) await removeEligibility(u.id);
      else await convertToEligibility(u.id);
    },
    onSuccess: () => client.invalidateQueries({ queryKey: ["unused-access"] }),
  });
  return (
    <TableRow className="align-top">
      <TableCell>
        <Link
          to={`/people/${u.identity_id}`}
          className="font-medium text-brand-700 hover:underline"
        >
          {u.identity_name}
        </Link>
      </TableCell>
      <TableCell>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium">{u.name}</span>
          <RiskBadge risk={u.risk} />
        </div>
        <div className="text-slate-500">
          {u.type === "role" ? "Role" : u.application}
          {u.eligible && " · may turn it on"}
        </div>
      </TableCell>
      <TableCell className="whitespace-nowrap">{formatDate(u.granted_at)}</TableCell>
      <TableCell className="whitespace-nowrap">
        {u.last_used_at ? formatDate(u.last_used_at) : "Never"}
      </TableCell>
      <TableCell className="text-right">
        <Manage>
          <Button size="sm" variant="outline" disabled={fix.isPending} onClick={() => fix.mutate()}>
            {u.eligible ? "Remove eligibility" : "Make it just in time"}
          </Button>
          <ErrorAlert error={fix.error} className="mt-2 text-left" />
        </Manage>
      </TableCell>
    </TableRow>
  );
}

function Roles() {
  const { data, error, isPending } = useQuery({
    queryKey: ["role-suggestions"],
    queryFn: listRoleSuggestions,
  });
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardContent className="pt-5 text-sm text-slate-600">
          Grantra looks in every department, and every job title in one, for access at least 80% of
          the people there hold one by one: by hand, by request, or in the application without
          Grantra giving it. A business role with a birthright rule gives it to all of them at once,
          and to whoever joins, and a review then looks at one rule instead of every grant. Groups
          of fewer than 5 people, just-in-time only access and what a role or rule already gives are
          left out.
        </CardContent>
      </Card>
      <Loaded
        loading={isPending}
        error={error}
        empty={data?.length === 0 && "No role stands out in what people hold."}
      >
        <div className="flex flex-col divide-y divide-slate-100">
          {data?.map((s) => (
            <Suggested key={s.name} s={s} />
          ))}
        </div>
      </Loaded>
    </div>
  );
}

function Suggested({ s }: { s: RoleSuggestion }) {
  const client = useQueryClient();
  const [name, setName] = useState(s.name);
  const [chosen, setChosen] = useState(() => new Set(s.entitlements.map((e) => e.id)));
  const [rule, setRule] = useState(true);
  const create = useMutation({
    mutationFn: () => createSuggestedRole(s, name.trim(), [...chosen], rule),
    onSuccess: () =>
      Promise.all(
        ["role-suggestions", "roles", "access-rules"].map((k) =>
          client.invalidateQueries({ queryKey: [k] }),
        ),
      ),
  });
  // Who lacks some of it is only known for the whole suggestion.
  const missing = chosen.size === s.entitlements.length ? s.people - s.have_all : 0;
  return (
    <section aria-label={s.name} className="flex flex-col gap-3 p-4">
      <CardHeader className="p-0">
        <CardTitle>{s.name}</CardTitle>
        <p className="text-sm text-slate-500">
          {s.people} people · {s.have_all} hold all of it already
        </p>
      </CardHeader>
      <ul className="flex flex-col gap-1 text-sm">
        {s.entitlements.map((e) => (
          <li key={e.id}>
            <label className="flex flex-wrap items-center gap-2">
              <input
                type="checkbox"
                checked={chosen.has(e.id)}
                onChange={() =>
                  setChosen((prev) => {
                    const next = new Set(prev);
                    if (next.has(e.id)) next.delete(e.id);
                    else next.add(e.id);
                    return next;
                  })
                }
              />
              <span className="font-medium">{e.name}</span>
              <span className="text-slate-500">
                {e.application} · {e.holders} of {s.people}
              </span>
              <RiskBadge risk={e.risk} />
            </label>
          </li>
        ))}
      </ul>
      <Manage>
        {create.isSuccess ? (
          <Alert variant="success">
            Created the {name} role{rule && " and its birthright rule"}.{" "}
            <Link to="/governance/roles" className="underline">
              See roles and rules
            </Link>
          </Alert>
        ) : (
          <form
            className="flex flex-wrap items-end gap-3"
            onSubmit={(e) => {
              e.preventDefault();
              create.mutate();
            }}
          >
            <div>
              <Label htmlFor={`role-name-${s.name}`}>Role name</Label>
              <Input
                id={`role-name-${s.name}`}
                value={name}
                onChange={(e) => setName(e.target.value)}
              />
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input type="checkbox" checked={rule} onChange={(e) => setRule(e.target.checked)} />
              Give it to everyone in {s.name} from now on
              {missing > 0 && rule && ` (${missing} would get access they do not have yet)`}
            </label>
            <Button
              type="submit"
              size="sm"
              disabled={create.isPending || chosen.size === 0 || !name.trim()}
            >
              Create role
            </Button>
          </form>
        )}
        <ErrorAlert error={create.error} />
      </Manage>
    </section>
  );
}
