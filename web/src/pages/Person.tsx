import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import { useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { Expiry, IdentityStatusBadge, Origin } from "@/components/AccessBadges";
import { AuditTable } from "@/components/AuditTable";
import { Loaded } from "@/components/Loaded";
import { RiskBadge } from "@/components/RiskBadge";
import { Tabs } from "@/components/Tabs";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import {
  ApiError,
  getIdentity,
  getIdentityAccess,
  getIdentityAccounts,
  grantPlatformRole,
  listAuditEvents,
  listLifecycleEvents,
  listPlatformRoles,
  PlatformRole,
  revokePlatformRole,
  type Identity,
  type Me,
  type PlatformRoleInfo,
} from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { NotFound } from "./NotFound";

type Tab = "access" | "accounts" | "lifecycle" | "history";

export function Person({ me }: { me: Me }) {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const isAdmin = me.platform_roles.includes(PlatformRole.Admin);
  // Helpdesk looks people up; admins and auditors also see roles and history.
  const readsAll = isAdmin || me.platform_roles.includes(PlatformRole.Auditor);
  const {
    data: person,
    error,
    isPending,
  } = useQuery({
    queryKey: ["identity", id],
    queryFn: () => getIdentity(id),
  });

  const tabs: [Tab, string][] = [
    ["access", "Access"],
    ["accounts", "Accounts"],
  ];
  if (readsAll) tabs.push(["lifecycle", "Joiner, mover, leaver"], ["history", "History"]);
  const asked = params.get("tab") as Tab | null;
  const tab = tabs.some(([k]) => k === asked) ? asked! : "access";

  if (isPending) return <p className="text-sm text-slate-500">Loading…</p>;
  if (error instanceof ApiError && error.status === 404) return <NotFound />;
  if (error) {
    return (
      <p role="alert" className="text-sm text-red-700">
        Could not load this person: {error.message}
      </p>
    );
  }

  return (
    <>
      <Link
        to="/people"
        className="mb-4 inline-flex items-center gap-1 text-sm text-slate-500 hover:text-slate-900"
      >
        <ArrowLeft className="size-4" aria-hidden /> People
      </Link>
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <h1 className="text-2xl font-semibold tracking-tight">{person.display_name}</h1>
        <IdentityStatusBadge status={person.status} />
        {person.email && <span className="text-slate-500">{person.email}</span>}
      </div>
      <div className="mb-6 grid gap-4 lg:grid-cols-2">
        <Details person={person} />
        {readsAll && (
          <PlatformRoles person={person} canChange={isAdmin} isMe={person.id === me.identity_id} />
        )}
      </div>
      <Tabs
        tabs={tabs}
        current={tab}
        onSelect={(key) => setParams(key === "access" ? {} : { tab: key })}
      />
      {tab === "access" && <AccessTab id={person.id} />}
      {tab === "accounts" && <AccountsTab id={person.id} />}
      {tab === "lifecycle" && <LifecycleTab id={person.id} />}
      {tab === "history" && <HistoryTab id={person.id} />}
    </>
  );
}

function Details({ person }: { person: Identity }) {
  const { data: manager } = useQuery({
    queryKey: ["identity", person.manager_id],
    queryFn: () => getIdentity(person.manager_id!),
    enabled: !!person.manager_id,
  });
  const attributes = Object.entries(person.attributes ?? {});
  const rows: [string, React.ReactNode][] = [
    ["Comes from", person.source_id ? "An HR source" : "Created by hand"],
    ["Employee id", person.external_id || "—"],
    [
      "Manager",
      person.manager_id ? (
        <Link to={`/people/${person.manager_id}`} className="text-brand-700 hover:underline">
          {manager?.display_name ?? "…"}
        </Link>
      ) : (
        "—"
      ),
    ],
    ["Start date", person.start_date ? formatDate(person.start_date) : "—"],
    ["End date", person.end_date ? formatDate(person.end_date) : "—"],
    ["In Grantra since", formatDate(person.created_at)],
    ...attributes.map(([k, v]): [string, React.ReactNode] => [fieldLabel(k), String(v)]),
  ];
  return (
    <Card>
      <CardHeader>
        <CardTitle>Details</CardTitle>
      </CardHeader>
      <CardContent>
        <dl className="grid grid-cols-[max-content_1fr] gap-x-6 gap-y-2 text-sm">
          {rows.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="text-slate-500">{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      </CardContent>
    </Card>
  );
}

function PlatformRoles({
  person,
  canChange,
  isMe,
}: {
  person: Identity;
  canChange: boolean;
  isMe: boolean;
}) {
  const { data: roles, error } = useQuery({
    queryKey: ["platform-roles"],
    queryFn: listPlatformRoles,
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>Grantra roles</CardTitle>
        <p className="text-sm text-slate-500">What this person can do in Grantra itself.</p>
      </CardHeader>
      <CardContent className="flex flex-col gap-3">
        {error ? (
          <p role="alert" className="text-sm text-red-700">
            Could not load the roles: {error.message}
          </p>
        ) : !roles ? (
          <p className="text-sm text-slate-500">Loading…</p>
        ) : (
          roles.map((r) => (
            <RoleRow key={r.name} role={r} person={person} canChange={canChange} isMe={isMe} />
          ))
        )}
      </CardContent>
    </Card>
  );
}

function RoleRow({
  role,
  person,
  canChange,
  isMe,
}: {
  role: PlatformRoleInfo;
  person: Identity;
  canChange: boolean;
  isMe: boolean;
}) {
  const client = useQueryClient();
  const [until, setUntil] = useState("");
  const member = role.members.find((m) => m.identity_id === person.id);
  const change = useMutation({
    mutationFn: () =>
      member
        ? revokePlatformRole(role.name, person.id)
        : grantPlatformRole(
            role.name,
            person.id,
            until ? new Date(`${until}T23:59:59`).toISOString() : undefined,
          ),
    onSuccess: () => {
      setUntil("");
      void client.invalidateQueries({ queryKey: ["platform-roles"] });
    },
  });
  const canGive = canChange && !member && !isMe && person.status === "active";

  return (
    <div className="rounded-md border border-slate-200 p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="font-medium">{role.display_name}</span>
          <RiskBadge risk={role.risk} />
        </div>
        {member ? (
          <Badge variant="green">
            Has it{member.expires_at ? ` until ${formatDate(member.expires_at)}` : ""}
          </Badge>
        ) : (
          <span className="text-sm text-slate-500">Does not have it</span>
        )}
      </div>
      <p className="mt-1 text-sm text-slate-500">{role.description}</p>
      {canChange && member && (
        <div className="mt-2">
          <Button
            size="sm"
            variant="outline"
            disabled={change.isPending}
            onClick={() => {
              if (window.confirm(`Remove ${role.display_name} from ${person.display_name}?`)) {
                change.mutate();
              }
            }}
          >
            Remove
          </Button>
        </div>
      )}
      {canGive && (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <label className="flex items-center gap-2 text-sm text-slate-500">
            Until
            <input
              type="date"
              value={until}
              onChange={(e) => setUntil(e.target.value)}
              className="h-8 rounded-md border border-slate-300 px-2 text-sm"
            />
          </label>
          <Button
            size="sm"
            disabled={change.isPending}
            onClick={() => {
              if (window.confirm(`Give ${role.display_name} to ${person.display_name}?`)) {
                change.mutate();
              }
            }}
          >
            Give {until ? "until then" : "with no end date"}
          </Button>
        </div>
      )}
      {canChange && !member && isMe && (
        <p className="mt-2 text-sm text-slate-500">Nobody can give a Grantra role to themselves.</p>
      )}
      {change.error && (
        <Alert variant="error" className="mt-2">
          {change.error.message}
        </Alert>
      )}
    </div>
  );
}

function AccessTab({ id }: { id: string }) {
  const [now] = useState(() => Date.now());
  const { data, error, isPending } = useQuery({
    queryKey: ["identity-access", id],
    queryFn: () => getIdentityAccess(id),
  });
  return (
    <Loaded
      loading={isPending}
      error={error}
      empty={data?.length === 0 ? "No access through Grantra." : undefined}
    >
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Access</TableHead>
            <TableHead>Why they have it</TableHead>
            <TableHead>Since</TableHead>
            <TableHead>Until</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {data?.map((a) => (
            <TableRow key={a.id}>
              <TableCell>
                <div className="font-medium">{a.name}</div>
                <div className="text-slate-500">
                  {a.type === "role" ? "Role" : a.application || "Entitlement"}
                </div>
              </TableCell>
              <TableCell>
                <Origin a={a} />
              </TableCell>
              <TableCell className="whitespace-nowrap">{formatDate(a.granted_at)}</TableCell>
              <TableCell className="whitespace-nowrap">
                <Expiry at={a.expires_at} now={now} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Loaded>
  );
}

const accountStatus = { active: "green", disabled: "amber", deleted: "red" } as const;

function AccountsTab({ id }: { id: string }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["identity-accounts", id],
    queryFn: () => getIdentityAccounts(id),
  });
  return (
    <Loaded
      loading={isPending}
      error={error}
      empty={
        data?.length === 0
          ? "No accounts found in connected applications. Accounts appear after an application's accounts are read."
          : undefined
      }
    >
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>Application</TableHead>
            <TableHead>Account</TableHead>
            <TableHead>Status</TableHead>
            <TableHead>Entitlements</TableHead>
            <TableHead>Last seen</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {data?.map((a) => (
            <TableRow key={a.id}>
              <TableCell className="font-medium">{a.application_name}</TableCell>
              <TableCell>
                <div>{a.username || a.native_id}</div>
                {a.correlated_by && (
                  <div className="text-slate-500">Matched by {a.correlated_by}</div>
                )}
              </TableCell>
              <TableCell>
                <Badge variant={accountStatus[a.status]}>{a.status}</Badge>
              </TableCell>
              <TableCell>{a.entitlements.length ? a.entitlements.join(", ") : "—"}</TableCell>
              <TableCell className="whitespace-nowrap">
                {a.last_seen_at ? formatDate(a.last_seen_at) : "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Loaded>
  );
}

const fieldLabels: Record<string, string> = {
  manager_id: "Manager (employee id)",
  display_name: "Name",
  email: "Email",
  status: "Status",
  start_date: "Start date",
  end_date: "End date",
};

/** "cost_center" reads as "Cost center". */
function fieldLabel(field: string) {
  if (fieldLabels[field]) return fieldLabels[field];
  const words = field.replace(/_/g, " ");
  return words.charAt(0).toUpperCase() + words.slice(1);
}

const kindLabels = {
  joiner: "Joined",
  mover: "Moved",
  leaver: "Left",
  rehire: "Came back",
};

function LifecycleTab({ id }: { id: string }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["lifecycle-events", id],
    queryFn: () => listLifecycleEvents(id),
  });
  return (
    <Loaded
      loading={isPending}
      error={error}
      empty={data?.length === 0 ? "No joiner, mover or leaver events from HR." : undefined}
    >
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>When</TableHead>
            <TableHead>What</TableHead>
            <TableHead>Changes</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {data?.map((e) => (
            <TableRow key={e.id} className="align-top">
              <TableCell className="whitespace-nowrap">{formatDate(e.effective_at)}</TableCell>
              <TableCell>
                <Badge variant={e.kind === "leaver" ? "red" : "brand"}>{kindLabels[e.kind]}</Badge>
                {e.details.reason && <div className="mt-1 text-slate-500">{e.details.reason}</div>}
              </TableCell>
              <TableCell>
                {e.details.changes ? (
                  <ul className="text-sm">
                    {Object.entries(e.details.changes).map(([field, c]) => (
                      <li key={field}>
                        <span className="text-slate-500">{fieldLabel(field)}:</span> {c.from || "—"}{" "}
                        → {c.to || "—"}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <span className="text-slate-400">—</span>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Loaded>
  );
}

function HistoryTab({ id }: { id: string }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["audit-events", { identity: id }],
    queryFn: () => listAuditEvents({ identity: id }),
  });
  return (
    <Loaded
      loading={isPending}
      error={error}
      empty={data?.length === 0 ? "Nothing in the audit log by or about this person." : undefined}
    >
      <AuditTable events={data ?? []} />
    </Loaded>
  );
}
