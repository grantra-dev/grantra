import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useSearchParams } from "react-router";
import { Loaded } from "@/components/Loaded";
import { PageHeader } from "@/components/PageHeader";
import { RiskBadge } from "@/components/RiskBadge";
import { Tabs } from "@/components/Tabs";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { PlatformRole, type Me } from "@/lib/api";
import {
  connectCommand,
  createGatewayTarget,
  deleteGatewayTarget,
  gatewayAddress,
  getGateway,
  issueGatewayToken,
  kindText,
  listGatewaySessions,
  listGatewayTargets,
  searchGatewayEvents,
  terminateGatewaySession,
  updateGatewayTarget,
  type GatewayInfo,
  type GatewayKind,
  type GatewaySession,
  type GatewayTarget,
  type GatewayToken,
  type NewGatewayTarget,
  type TLSMode,
} from "@/lib/gateway";
import { endReasonText } from "@/lib/ssh";
import { formatTime } from "@/lib/utils";
import { listVaultAccounts } from "@/lib/vault";

type Tab = "targets" | "sessions" | "queries";

export function Databases({ me }: { me: Me }) {
  const admin = me.platform_roles.includes(PlatformRole.Admin);
  const reader = admin || me.platform_roles.includes(PlatformRole.Auditor);
  const [params, setParams] = useSearchParams();
  const asked = params.get("tab");
  const tab: Tab = reader && (asked === "sessions" || asked === "queries") ? asked : "targets";
  return (
    <>
      <PageHeader
        title="Databases & clusters"
        description="Connect to databases and Kubernetes clusters through the Grantra gateway with a token that lasts only as long as your access. Every query and cluster request is logged."
      />
      {reader && (
        <Tabs
          tabs={[
            ["targets", "Databases & clusters"],
            ["sessions", "Sessions"],
            ["queries", "Queries"],
          ]}
          current={tab}
          onSelect={(t) => setParams(t === "targets" ? {} : { tab: t })}
        />
      )}
      {tab === "targets" ? (
        <Targets admin={admin} reader={reader} />
      ) : tab === "sessions" ? (
        <Sessions admin={admin} />
      ) : (
        <Queries />
      )}
    </>
  );
}

function Targets({ admin, reader }: { admin: boolean; reader: boolean }) {
  const info = useQuery({ queryKey: ["gateway"], queryFn: getGateway });
  const { data, error, isPending } = useQuery({
    queryKey: ["gateway-targets"],
    queryFn: listGatewayTargets,
  });
  const [adding, setAdding] = useState(false);
  const [open, setOpen] = useState<string | null>(null);
  return (
    <section className="flex flex-col gap-3">
      {info.data && !info.data.enabled && (
        <Alert variant="warning">
          The gateway is off: an operator has to set GRANTRA_VAULT_KEY and restart Grantra.
        </Alert>
      )}
      {admin && (
        <div className="flex justify-end">
          {!adding && (
            <Button variant="outline" onClick={() => setAdding(true)}>
              Add database or cluster
            </Button>
          )}
        </div>
      )}
      {adding && <NewTarget onDone={() => setAdding(false)} />}
      <Loaded
        loading={isPending}
        error={error}
        empty={
          data?.length === 0 &&
          (reader
            ? "No databases or clusters yet."
            : "You hold no database or cluster access right now. Request it under Request access, or activate it from My access.")
        }
      >
        <ul className="divide-y divide-slate-200">
          {data?.map((t) => (
            <TargetRow
              key={t.id}
              t={t}
              admin={admin}
              info={info.data}
              open={open === t.id}
              onToggle={() => setOpen(open === t.id ? null : t.id)}
            />
          ))}
        </ul>
      </Loaded>
    </section>
  );
}

function TargetRow({
  t,
  admin,
  info,
  open,
  onToggle,
}: {
  t: GatewayTarget;
  admin: boolean;
  info: GatewayInfo | undefined;
  open: boolean;
  onToggle: () => void;
}) {
  const [token, setToken] = useState<GatewayToken | null>(null);
  const issue = useMutation({ mutationFn: () => issueGatewayToken(t.id), onSuccess: setToken });
  return (
    <li className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{t.name}</span>
            <Badge>{kindText[t.kind]}</Badge>
            <RiskBadge risk={t.risk} />
          </div>
          <div className="text-sm text-slate-500">
            {t.address}
            {t.database && ` · ${t.database}`}
            {t.vault_account_name && ` · logs in as ${t.vault_account_name}`} · tokens last up to{" "}
            {t.max_token_hours} {t.max_token_hours === 1 ? "hour" : "hours"}
          </div>
          {t.notes && <p className="mt-1 text-sm text-slate-700">{t.notes}</p>}
        </div>
        <div className="flex gap-2">
          {t.held && !token && (
            <Button size="sm" onClick={() => issue.mutate()} disabled={issue.isPending}>
              Get token
            </Button>
          )}
          {admin && (
            <Button size="sm" variant="outline" onClick={onToggle}>
              {open ? "Close" : "Settings"}
            </Button>
          )}
        </div>
      </div>
      {issue.error && (
        <Alert variant="error" className="mt-3">
          {issue.error.message}
        </Alert>
      )}
      {token && <TokenPanel t={t} token={token} info={info} onClose={() => setToken(null)} />}
      {open && <TargetSettings t={t} onDone={onToggle} />}
    </li>
  );
}

/** Offers text as a file to save. */
function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

function TokenPanel({
  t,
  token,
  info,
  onClose,
}: {
  t: GatewayTarget;
  token: GatewayToken;
  info: GatewayInfo | undefined;
  onClose: () => void;
}) {
  const gateway = gatewayAddress(info, t.kind);
  return (
    <div className="mt-3 flex flex-col gap-2 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm">
      <p>
        Valid until <strong>{formatTime(token.valid_before)}</strong>, or until your access ends.
        Grantra shows it only now.
      </p>
      {t.kind === "kubernetes" ? (
        <>
          <p>Save the kubeconfig, which holds the token, and use it with kubectl:</p>
          <Textarea
            aria-label="Kubeconfig"
            readOnly
            rows={6}
            value={token.kubeconfig ?? ""}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
          <div>
            <Button
              size="sm"
              variant="outline"
              onClick={() => download(`grantra-${t.name}.yaml`, token.kubeconfig ?? "")}
            >
              Download kubeconfig
            </Button>
          </div>
        </>
      ) : (
        <>
          <p>
            Save the gateway&apos;s certificate as <code>grantra-gateway-ca.pem</code>, then connect
            and give the token as the password:
          </p>
          <Label htmlFor={`token-${t.id}`}>Token</Label>
          <Input
            id={`token-${t.id}`}
            readOnly
            value={token.token}
            className="font-mono text-xs"
            onFocus={(e) => e.currentTarget.select()}
          />
          <div>
            <Button
              size="sm"
              variant="outline"
              onClick={() => download("grantra-gateway-ca.pem", token.ca_certificate)}
            >
              Download certificate
            </Button>
          </div>
        </>
      )}
      <pre className="overflow-x-auto rounded bg-slate-900 p-2 text-xs text-slate-100">
        {connectCommand(t, gateway)}
      </pre>
      {gateway.startsWith("GATEWAY:") && (
        <p className="text-slate-500">Ask your Grantra admin for the gateway address.</p>
      )}
      <div>
        <Button size="sm" variant="outline" onClick={onClose}>
          Done
        </Button>
      </div>
    </div>
  );
}

/** Password accounts in the vault, which the gateway logs in with. */
function VaultAccountSelect({
  id,
  value,
  onChange,
  optional,
}: {
  id: string;
  value: string;
  onChange: (v: string) => void;
  optional: boolean;
}) {
  const accounts = useQuery({ queryKey: ["vault-accounts"], queryFn: listVaultAccounts });
  return (
    <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">
        {optional ? "The gateway's own service account" : "Pick a vault account"}
      </option>
      {accounts.data?.accounts
        .filter((a) => a.kind === "password")
        .map((a) => (
          <option key={a.id} value={a.id}>
            {a.name} ({a.username})
          </option>
        ))}
    </Select>
  );
}

function TLSSelect({
  id,
  value,
  kind,
  onChange,
}: {
  id: string;
  value: TLSMode;
  kind: GatewayKind;
  onChange: (v: TLSMode) => void;
}) {
  return (
    <Select id={id} value={value} onChange={(e) => onChange(e.target.value as TLSMode)}>
      <option value="verify">TLS, verified</option>
      <option value="require">TLS, not verified</option>
      {kind !== "kubernetes" && <option value="disable">No TLS</option>}
    </Select>
  );
}

const groupsText = (g: string[]) => g.join(", ");
const parseGroups = (s: string) =>
  s
    .split(",")
    .map((g) => g.trim())
    .filter(Boolean);

function TargetSettings({ t, onDone }: { t: GatewayTarget; onDone: () => void }) {
  const client = useQueryClient();
  const [address, setAddress] = useState(t.address);
  const [database, setDatabase] = useState(t.database ?? "");
  const [tls, setTLS] = useState(t.tls_mode);
  const [ca, setCA] = useState(t.ca_certificate ?? "");
  const [account, setAccount] = useState(t.vault_account_id ?? "");
  const [groups, setGroups] = useState(groupsText(t.kube_groups));
  const [hours, setHours] = useState(t.max_token_hours);
  const [notes, setNotes] = useState(t.notes ?? "");
  const refresh = () => client.invalidateQueries({ queryKey: ["gateway-targets"] });
  const save = useMutation({
    mutationFn: () =>
      updateGatewayTarget(t.id, {
        address,
        database,
        tls_mode: tls,
        ca_certificate: ca,
        vault_account_id: account,
        max_token_hours: hours,
        notes,
        ...(t.kind === "kubernetes" ? { kube_groups: parseGroups(groups) } : {}),
      }),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: () => deleteGatewayTarget(t.id),
    onSuccess: () => {
      onDone();
      return refresh();
    },
  });
  const cluster = t.kind === "kubernetes";
  return (
    <form
      className="mt-3 grid gap-3 rounded-md border border-slate-200 p-3 sm:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div>
        <Label htmlFor={`addr-${t.id}`}>Address</Label>
        <Input id={`addr-${t.id}`} value={address} onChange={(e) => setAddress(e.target.value)} />
      </div>
      {cluster ? (
        <div>
          <Label htmlFor={`groups-${t.id}`}>Kubernetes groups (comma separated)</Label>
          <Input id={`groups-${t.id}`} value={groups} onChange={(e) => setGroups(e.target.value)} />
        </div>
      ) : (
        <div>
          <Label htmlFor={`db-${t.id}`}>Default database</Label>
          <Input id={`db-${t.id}`} value={database} onChange={(e) => setDatabase(e.target.value)} />
        </div>
      )}
      <div>
        <Label htmlFor={`acct-${t.id}`}>Logs in with</Label>
        <VaultAccountSelect
          id={`acct-${t.id}`}
          value={account}
          onChange={setAccount}
          optional={cluster}
        />
      </div>
      <div>
        <Label htmlFor={`tls-${t.id}`}>TLS to the {cluster ? "cluster" : "database"}</Label>
        <TLSSelect id={`tls-${t.id}`} value={tls} kind={t.kind} onChange={setTLS} />
      </div>
      <div>
        <Label htmlFor={`hours-${t.id}`}>Tokens last up to (hours)</Label>
        <Input
          id={`hours-${t.id}`}
          type="number"
          min={1}
          max={24}
          value={hours}
          onChange={(e) => setHours(Number(e.target.value))}
        />
      </div>
      <div>
        <Label htmlFor={`notes-${t.id}`}>Notes</Label>
        <Input id={`notes-${t.id}`} value={notes} onChange={(e) => setNotes(e.target.value)} />
      </div>
      {tls === "verify" && (
        <div className="sm:col-span-2">
          <Label htmlFor={`ca-${t.id}`}>CA certificate (PEM, empty for the system&apos;s)</Label>
          <Textarea
            id={`ca-${t.id}`}
            rows={3}
            value={ca}
            onChange={(e) => setCA(e.target.value)}
            className="font-mono text-xs"
          />
        </div>
      )}
      <div className="flex flex-wrap gap-2 sm:col-span-2">
        <Button type="submit" size="sm" disabled={save.isPending}>
          Save
        </Button>
        <Button
          type="button"
          size="sm"
          variant="destructive"
          onClick={() => {
            if (window.confirm(`Delete ${t.name}? Everyone loses access to it; its logs stay.`))
              remove.mutate();
          }}
          disabled={remove.isPending}
        >
          Delete
        </Button>
      </div>
      {(save.error ?? remove.error) && (
        <Alert variant="error" className="sm:col-span-2">
          {(save.error ?? remove.error)!.message}
        </Alert>
      )}
    </form>
  );
}

const placeholders: Record<GatewayKind, string> = {
  kubernetes: "https://10.0.0.1:6443",
  postgres: "10.0.0.7:5432",
  mysql: "10.0.0.8:3306",
};

function NewTarget({ onDone }: { onDone: () => void }) {
  const client = useQueryClient();
  const [t, setT] = useState<NewGatewayTarget>({
    name: "",
    kind: "postgres",
    address: "",
    database: "",
    tls_mode: "verify",
    ca_certificate: "",
    vault_account_id: "",
    risk: "high",
  });
  const [groups, setGroups] = useState("");
  const set = (patch: Partial<NewGatewayTarget>) => setT({ ...t, ...patch });
  const cluster = t.kind === "kubernetes";
  const create = useMutation({
    mutationFn: () =>
      createGatewayTarget({
        ...t,
        database: cluster ? undefined : t.database,
        kube_groups: cluster && groups.trim() ? parseGroups(groups) : undefined,
      }),
    onSuccess: () => {
      onDone();
      return client.invalidateQueries({ queryKey: ["gateway-targets"] });
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a database or cluster</CardTitle>
        <p className="text-sm text-slate-500">
          Grantra adds the access to connect to it to the catalog, just in time only: people are
          made eligible and activate it, or request it for a time. The gateway logs in with an
          account in the vault, so nobody sees its password.
        </p>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-3 sm:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            create.mutate();
          }}
        >
          <div>
            <Label htmlFor="gw-kind">Kind</Label>
            <Select
              id="gw-kind"
              value={t.kind}
              onChange={(e) => {
                const kind = e.target.value as GatewayKind;
                set({ kind, tls_mode: kind === "kubernetes" ? "verify" : t.tls_mode });
              }}
            >
              <option value="postgres">PostgreSQL</option>
              <option value="mysql">MySQL or MariaDB</option>
              <option value="kubernetes">Kubernetes cluster</option>
            </Select>
          </div>
          <div>
            <Label htmlFor="gw-name">Name</Label>
            <Input
              id="gw-name"
              value={t.name}
              onChange={(e) => set({ name: e.target.value })}
              placeholder={cluster ? "prod" : "orders-db"}
              required
            />
          </div>
          <div>
            <Label htmlFor="gw-address">{cluster ? "API server URL" : "Address"}</Label>
            <Input
              id="gw-address"
              value={t.address}
              onChange={(e) => set({ address: e.target.value })}
              placeholder={placeholders[t.kind]}
              required
            />
          </div>
          {cluster ? (
            <div>
              <Label htmlFor="gw-groups">Kubernetes groups (comma separated)</Label>
              <Input
                id="gw-groups"
                value={groups}
                onChange={(e) => setGroups(e.target.value)}
                placeholder={`grantra:${t.name || "prod"}`}
              />
            </div>
          ) : (
            <div>
              <Label htmlFor="gw-db">Default database</Label>
              <Input
                id="gw-db"
                value={t.database}
                onChange={(e) => set({ database: e.target.value })}
                placeholder="orders"
              />
            </div>
          )}
          <div>
            <Label htmlFor="gw-account">Logs in with</Label>
            <VaultAccountSelect
              id="gw-account"
              value={t.vault_account_id ?? ""}
              onChange={(v) => set({ vault_account_id: v })}
              optional={cluster}
            />
          </div>
          <div>
            <Label htmlFor="gw-tls">TLS to the {cluster ? "cluster" : "database"}</Label>
            <TLSSelect
              id="gw-tls"
              value={t.tls_mode ?? "verify"}
              kind={t.kind}
              onChange={(v) => set({ tls_mode: v })}
            />
          </div>
          <div>
            <Label htmlFor="gw-risk">Risk</Label>
            <Select id="gw-risk" value={t.risk} onChange={(e) => set({ risk: e.target.value })}>
              <option value="high">High</option>
              <option value="critical">Critical</option>
            </Select>
          </div>
          {t.tls_mode === "verify" && (
            <div className="sm:col-span-2">
              <Label htmlFor="gw-ca">CA certificate (PEM, empty for the system&apos;s)</Label>
              <Textarea
                id="gw-ca"
                rows={3}
                value={t.ca_certificate}
                onChange={(e) => set({ ca_certificate: e.target.value })}
                className="font-mono text-xs"
              />
            </div>
          )}
          <div className="flex gap-2 sm:col-span-2">
            <Button type="submit" disabled={create.isPending}>
              Add
            </Button>
            <Button type="button" variant="outline" onClick={onDone}>
              Cancel
            </Button>
          </div>
          {create.error && (
            <Alert variant="error" className="sm:col-span-2">
              {create.error.message}
            </Alert>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

function Sessions({ admin }: { admin: boolean }) {
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
                <TableCell>{formatTime(s.started_at)}</TableCell>
                <TableCell>{s.events}</TableCell>
                <TableCell>
                  <GatewaySessionStatus s={s} />
                </TableCell>
                <TableCell className="flex justify-end gap-2">
                  <Link
                    to={`/databases/sessions/${s.id}`}
                    className="text-sm text-brand-700 underline"
                  >
                    Log
                  </Link>
                  {admin && !s.ended_at && !s.terminate_requested && (
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
      {reason === "recording_full" ? "Log limit reached" : endReasonText[reason]}
    </Badge>
  );
}

function Queries() {
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
                      to={`/databases/sessions/${ev.session_id}`}
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
