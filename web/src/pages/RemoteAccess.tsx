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
import { buttonVariants } from "@/components/ui/button-variants";
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
  awsExports,
  connectCommand,
  createGatewayTarget,
  deleteGatewayTarget,
  gatewayAddress,
  getGateway,
  isCloud,
  issueGatewayToken,
  kindText,
  listGatewaySessions,
  listGatewayTargets,
  openConsole,
  searchGatewayEvents,
  terminateGatewaySession,
  updateGatewayTarget,
  usesToken,
  type Clipboard,
  type ConsoleSession,
  type GatewayInfo,
  type GatewayKind,
  type GatewaySession,
  type GatewayTarget,
  type GatewayTargetSettings,
  type GatewayToken,
  type NewGatewayTarget,
  type TLSMode,
  type TokenKind,
} from "@/lib/gateway";
import { endReasonText } from "@/lib/ssh";
import { formatTime } from "@/lib/utils";
import { listVaultAccounts } from "@/lib/vault";

type Tab = "targets" | "sessions" | "queries";

export function RemoteAccess({ me }: { me: Me }) {
  const admin = me.platform_roles.includes(PlatformRole.Admin);
  const reader = admin || me.platform_roles.includes(PlatformRole.Auditor);
  const [params, setParams] = useSearchParams();
  const asked = params.get("tab");
  const tab: Tab = reader && (asked === "sessions" || asked === "queries") ? asked : "targets";
  return (
    <>
      <PageHeader
        title="Remote access"
        description="Databases, Kubernetes clusters, desktops and cloud consoles, with access that lasts only as long as yours. Nobody sees the passwords; queries and requests are logged, desktops recorded."
      />
      {reader && (
        <Tabs
          tabs={[
            ["targets", "Targets"],
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
              Add a target
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
            ? "No databases, clusters, desktops or cloud consoles yet."
            : "You hold no remote access right now. Request it under Request access, or activate it from My access.")
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

/** What a target is, in a line. */
function describe(t: GatewayTarget) {
  const hours = `${t.max_token_hours} ${t.max_token_hours === 1 ? "hour" : "hours"}`;
  const as = t.vault_account_name ? ` · logs in as ${t.vault_account_name}` : "";
  switch (t.kind) {
    case "rdp":
      return `${t.address}${as} · sessions last up to ${hours}`;
    case "aws":
      return `Role ${t.address}${as} · sessions last up to ${hours}`;
    case "azure":
    case "gcp":
      return `${t.cloud_role} on ${t.address}${as} · sessions last up to ${hours}`;
    default:
      return `${t.address}${t.database ? ` · ${t.database}` : ""}${as} · tokens last up to ${hours}`;
  }
}

/**
 * Opens url in a new tab. The tab opens on the click, before the URL is
 * known, so browsers do not block it as a pop-up.
 */
function newTab() {
  const w = window.open("", "_blank");
  return {
    go: (url: string) => {
      if (!w) return window.location.assign(url);
      w.opener = null;
      w.location.href = url;
    },
    close: () => w?.close(),
  };
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
  const [opened, setOpened] = useState<ConsoleSession | null>(null);
  const issue = useMutation({ mutationFn: () => issueGatewayToken(t.id), onSuccess: setToken });
  const sign = useMutation({
    mutationFn: async (tab: ReturnType<typeof newTab>) => {
      try {
        const s = await openConsole(t.id);
        tab.go(s.url);
        return s;
      } catch (e) {
        tab.close();
        throw e;
      }
    },
    onSuccess: setOpened,
  });
  const desktopsOff = t.kind === "rdp" && info !== undefined && !info.desktops_enabled;
  const err = issue.error ?? sign.error;
  return (
    <li className="p-4">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-medium">{t.name}</span>
            <Badge>{kindText[t.kind]}</Badge>
            <RiskBadge risk={t.risk} />
          </div>
          <div className="break-all text-sm text-slate-500">{describe(t)}</div>
          {t.notes && <p className="mt-1 text-sm text-slate-700">{t.notes}</p>}
          {t.held && desktopsOff && (
            <p className="mt-1 text-sm text-amber-700">
              Desktops are off: an operator has to set up guacd (GRANTRA_GUACD_ADDR).
            </p>
          )}
        </div>
        <div className="flex gap-2">
          {t.held && usesToken(t.kind) && !token && (
            <Button size="sm" onClick={() => issue.mutate()} disabled={issue.isPending}>
              Get token
            </Button>
          )}
          {t.held && t.kind === "rdp" && !desktopsOff && (
            <Link
              to={`/remote-access/desktops/${t.id}`}
              target="_blank"
              className={buttonVariants({ size: "sm" })}
            >
              Connect
            </Link>
          )}
          {t.held && isCloud(t.kind) && (
            <Button size="sm" onClick={() => sign.mutate(newTab())} disabled={sign.isPending}>
              Open console
            </Button>
          )}
          {admin && (
            <Button size="sm" variant="outline" onClick={onToggle}>
              {open ? "Close" : "Settings"}
            </Button>
          )}
        </div>
      </div>
      {err && (
        <Alert variant="error" className="mt-3">
          {err.message}
        </Alert>
      )}
      {token && usesToken(t.kind) && (
        <TokenPanel
          t={t}
          kind={t.kind as TokenKind}
          token={token}
          info={info}
          onClose={() => setToken(null)}
        />
      )}
      {opened && <ConsolePanel t={t} s={opened} onClose={() => setOpened(null)} />}
      {open && <TargetSettings t={t} onDone={onToggle} />}
    </li>
  );
}

function ConsolePanel({
  t,
  s,
  onClose,
}: {
  t: GatewayTarget;
  s: ConsoleSession;
  onClose: () => void;
}) {
  return (
    <div className="mt-3 flex flex-col gap-2 rounded-md border border-slate-200 bg-slate-50 p-3 text-sm">
      <p>
        The {kindText[t.kind]} console opened in a new tab. Your session lasts until{" "}
        <strong>{formatTime(s.valid_before)}</strong>, or until your access ends
        {t.kind === "aws" && " (on AWS, an ended session stays signed in until then)"}.{" "}
        <a href={s.url} target="_blank" rel="noreferrer" className="text-brand-700 underline">
          Open it again
        </a>
      </p>
      {s.credentials && (
        <>
          <p>For the AWS CLI, in a terminal (Grantra shows them only now):</p>
          <pre className="overflow-x-auto rounded bg-slate-900 p-2 text-xs text-slate-100">
            {awsExports(s.credentials)}
          </pre>
        </>
      )}
      <div>
        <Button size="sm" variant="outline" onClick={onClose}>
          Done
        </Button>
      </div>
    </div>
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
  kind,
  token,
  info,
  onClose,
}: {
  t: GatewayTarget;
  kind: TokenKind;
  token: GatewayToken;
  info: GatewayInfo | undefined;
  onClose: () => void;
}) {
  const gateway = gatewayAddress(info, kind);
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

/** What the gateway logs in with when a target has no vault account. */
const ownCredentials: Partial<Record<GatewayKind, string>> = {
  kubernetes: "The gateway's own service account",
  azure: "Grantra's own Azure identity",
  gcp: "Grantra's own Google Cloud service account",
};

const accountLabels: Partial<Record<GatewayKind, string>> = {
  aws: "Assumes the role as (an IAM user's access key)",
  azure: "Assigns roles as (an app's client ID and secret)",
  gcp: "Grants roles as (a service account's JSON key)",
};

/** Password accounts in the vault, which the gateway logs in with. */
function VaultAccountSelect({
  id,
  value,
  kind,
  onChange,
}: {
  id: string;
  value: string;
  kind: GatewayKind;
  onChange: (v: string) => void;
}) {
  const accounts = useQuery({ queryKey: ["vault-accounts"], queryFn: listVaultAccounts });
  return (
    <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
      <option value="">{ownCredentials[kind] ?? "Pick a vault account"}</option>
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
      {kind !== "kubernetes" && kind !== "rdp" && <option value="disable">No TLS</option>}
    </Select>
  );
}

const groupsText = (g: string[]) => g.join(", ");
const parseGroups = (s: string) =>
  s
    .split(",")
    .map((g) => g.trim())
    .filter(Boolean);

const addressLabels: Record<GatewayKind, string> = {
  kubernetes: "API server URL",
  postgres: "Address",
  mysql: "Address",
  rdp: "Address",
  aws: "IAM role ARN",
  azure: "Scope (subscription or resource group)",
  gcp: "Project ID",
};

const placeholders: Record<GatewayKind, string> = {
  kubernetes: "https://10.0.0.1:6443",
  postgres: "10.0.0.7:5432",
  mysql: "10.0.0.8:3306",
  rdp: "10.0.0.9:3389",
  aws: "arn:aws:iam::123456789012:role/ReadOnly",
  azure: "/subscriptions/00000000-0000-0000-0000-000000000000/resourceGroups/shop",
  gcp: "shop-prod-123",
};

const rolePlaceholders: Partial<Record<GatewayKind, string>> = {
  azure: "Reader",
  gcp: "roles/compute.viewer",
};

/** The longest a target's tokens or sessions can last, in hours. */
const maxHours = (k: GatewayKind) => (k === "aws" ? 12 : 24);

/**
 * The fields that depend on the kind of target: what a cluster, database,
 * desktop or cloud needs besides an address.
 */
function KindFields({
  idPrefix,
  kind,
  name,
  s,
  set,
  groups,
  setGroups,
}: {
  idPrefix: string;
  kind: GatewayKind;
  name: string;
  s: GatewayTargetSettings;
  set: (patch: GatewayTargetSettings) => void;
  groups: string;
  setGroups: (g: string) => void;
}) {
  switch (kind) {
    case "kubernetes":
      return (
        <div>
          <Label htmlFor={`${idPrefix}-groups`}>Kubernetes groups (comma separated)</Label>
          <Input
            id={`${idPrefix}-groups`}
            value={groups}
            onChange={(e) => setGroups(e.target.value)}
            placeholder={`grantra:${name || "prod"}`}
          />
        </div>
      );
    case "postgres":
    case "mysql":
      return (
        <div>
          <Label htmlFor={`${idPrefix}-db`}>Default database</Label>
          <Input
            id={`${idPrefix}-db`}
            value={s.database ?? ""}
            onChange={(e) => set({ database: e.target.value })}
            placeholder="orders"
          />
        </div>
      );
    case "rdp":
      return (
        <div>
          <Label htmlFor={`${idPrefix}-clip`}>Clipboard</Label>
          <Select
            id={`${idPrefix}-clip`}
            value={s.clipboard ?? "paste"}
            onChange={(e) => set({ clipboard: e.target.value as Clipboard })}
          >
            <option value="paste">Paste into the desktop only</option>
            <option value="both">Copy and paste both ways</option>
            <option value="none">Off</option>
          </Select>
        </div>
      );
    case "aws":
      return null;
    case "azure":
    case "gcp":
      return (
        <>
          <div>
            <Label htmlFor={`${idPrefix}-role`}>Role people get</Label>
            <Input
              id={`${idPrefix}-role`}
              value={s.cloud_role ?? ""}
              onChange={(e) => set({ cloud_role: e.target.value })}
              placeholder={rolePlaceholders[kind]}
              required
            />
          </div>
          {kind === "azure" && (
            <div>
              <Label htmlFor={`${idPrefix}-dir`}>Microsoft Entra tenant (ID or domain)</Label>
              <Input
                id={`${idPrefix}-dir`}
                value={s.cloud_directory ?? ""}
                onChange={(e) => set({ cloud_directory: e.target.value })}
                placeholder="corp.onmicrosoft.com"
              />
            </div>
          )}
        </>
      );
  }
}

/** Only the settings a kind of target takes, so the API accepts them. */
function settingsFor(kind: GatewayKind, s: GatewayTargetSettings, groups: string) {
  const out: GatewayTargetSettings = {
    address: s.address,
    tls_mode: s.tls_mode,
    vault_account_id: s.vault_account_id,
    max_token_hours: s.max_token_hours,
    notes: s.notes,
  };
  if (kind === "kubernetes") {
    out.ca_certificate = s.ca_certificate;
    if (groups.trim()) out.kube_groups = parseGroups(groups);
  } else if (kind === "postgres" || kind === "mysql") {
    out.ca_certificate = s.ca_certificate;
    out.database = s.database;
  } else if (kind === "rdp") {
    out.clipboard = s.clipboard;
  } else if (kind !== "aws") {
    out.cloud_role = s.cloud_role;
    if (kind === "azure") out.cloud_directory = s.cloud_directory;
  }
  return out;
}

/** Whether a kind of target takes its own CA certificate. */
const takesCA = (k: GatewayKind) => usesToken(k);

function TargetSettings({ t, onDone }: { t: GatewayTarget; onDone: () => void }) {
  const client = useQueryClient();
  const [s, setS] = useState<GatewayTargetSettings>({
    address: t.address,
    database: t.database ?? "",
    tls_mode: t.tls_mode,
    ca_certificate: t.ca_certificate ?? "",
    vault_account_id: t.vault_account_id ?? "",
    clipboard: t.clipboard,
    cloud_role: t.cloud_role ?? "",
    cloud_directory: t.cloud_directory ?? "",
    max_token_hours: t.max_token_hours,
    notes: t.notes ?? "",
  });
  const [groups, setGroups] = useState(groupsText(t.kube_groups));
  const set = (patch: GatewayTargetSettings) => setS({ ...s, ...patch });
  const refresh = () => client.invalidateQueries({ queryKey: ["gateway-targets"] });
  const save = useMutation({
    mutationFn: () =>
      // An empty group list goes as is: it takes the groups away.
      updateGatewayTarget(t.id, {
        ...settingsFor(t.kind, s, groups),
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
  const id = `t-${t.id}`;
  return (
    <form
      className="mt-3 grid gap-3 rounded-md border border-slate-200 p-3 sm:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <div>
        <Label htmlFor={`${id}-address`}>{addressLabels[t.kind]}</Label>
        <Input
          id={`${id}-address`}
          value={s.address}
          onChange={(e) => set({ address: e.target.value })}
        />
      </div>
      <KindFields
        idPrefix={id}
        kind={t.kind}
        name={t.name}
        s={s}
        set={set}
        groups={groups}
        setGroups={setGroups}
      />
      <div>
        <Label htmlFor={`${id}-account`}>{accountLabels[t.kind] ?? "Logs in with"}</Label>
        <VaultAccountSelect
          id={`${id}-account`}
          value={s.vault_account_id ?? ""}
          kind={t.kind}
          onChange={(v) => set({ vault_account_id: v })}
        />
      </div>
      {!isCloud(t.kind) && (
        <div>
          <Label htmlFor={`${id}-tls`}>TLS to the {kindNoun[t.kind]}</Label>
          <TLSSelect
            id={`${id}-tls`}
            value={s.tls_mode ?? "verify"}
            kind={t.kind}
            onChange={(v) => set({ tls_mode: v })}
          />
        </div>
      )}
      <div>
        <Label htmlFor={`${id}-hours`}>
          {usesToken(t.kind) ? "Tokens" : "Sessions"} last up to (hours)
        </Label>
        <Input
          id={`${id}-hours`}
          type="number"
          min={1}
          max={maxHours(t.kind)}
          value={s.max_token_hours}
          onChange={(e) => set({ max_token_hours: Number(e.target.value) })}
        />
      </div>
      <div>
        <Label htmlFor={`${id}-notes`}>Notes</Label>
        <Input
          id={`${id}-notes`}
          value={s.notes}
          onChange={(e) => set({ notes: e.target.value })}
        />
      </div>
      {takesCA(t.kind) && s.tls_mode === "verify" && (
        <div className="sm:col-span-2">
          <Label htmlFor={`${id}-ca`}>CA certificate (PEM, empty for the system&apos;s)</Label>
          <Textarea
            id={`${id}-ca`}
            rows={3}
            value={s.ca_certificate}
            onChange={(e) => set({ ca_certificate: e.target.value })}
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

const kindNoun: Record<GatewayKind, string> = {
  kubernetes: "cluster",
  postgres: "database",
  mysql: "database",
  rdp: "desktop",
  aws: "cloud",
  azure: "cloud",
  gcp: "cloud",
};

const defaultHours = (k: GatewayKind) => (k === "aws" ? 1 : 8);

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
    clipboard: "paste",
    cloud_role: "",
    cloud_directory: "",
    max_token_hours: 8,
    risk: "high",
  });
  const [groups, setGroups] = useState("");
  const set = (patch: Partial<NewGatewayTarget>) => setT({ ...t, ...patch });
  const create = useMutation({
    mutationFn: () =>
      createGatewayTarget({
        name: t.name,
        kind: t.kind,
        risk: t.risk,
        ...settingsFor(t.kind, t, groups),
      }),
    onSuccess: () => {
      onDone();
      return client.invalidateQueries({ queryKey: ["gateway-targets"] });
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a target</CardTitle>
        <p className="text-sm text-slate-500">
          Grantra adds the access to it to the catalog, just in time only: people are made eligible
          and activate it, or request it for a time. The gateway logs in with an account in the
          vault, so nobody sees its password; on a cloud, Grantra gives people the role for the
          session and takes it back after.
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
                const tls =
                  isCloud(kind) ||
                  ((kind === "kubernetes" || kind === "rdp") && t.tls_mode === "disable")
                    ? "verify"
                    : t.tls_mode;
                set({ kind, tls_mode: tls, max_token_hours: defaultHours(kind) });
              }}
            >
              <option value="postgres">PostgreSQL</option>
              <option value="mysql">MySQL or MariaDB</option>
              <option value="kubernetes">Kubernetes cluster</option>
              <option value="rdp">Windows desktop (RDP)</option>
              <option value="aws">AWS console</option>
              <option value="azure">Azure portal</option>
              <option value="gcp">Google Cloud console</option>
            </Select>
          </div>
          <div>
            <Label htmlFor="gw-name">Name</Label>
            <Input
              id="gw-name"
              value={t.name}
              onChange={(e) => set({ name: e.target.value })}
              placeholder={t.kind === "kubernetes" || isCloud(t.kind) ? "prod" : "orders-db"}
              required
            />
          </div>
          <div>
            <Label htmlFor="gw-address">{addressLabels[t.kind]}</Label>
            <Input
              id="gw-address"
              value={t.address}
              onChange={(e) => set({ address: e.target.value })}
              placeholder={placeholders[t.kind]}
              required
            />
          </div>
          <KindFields
            idPrefix="gw"
            kind={t.kind}
            name={t.name}
            s={t}
            set={set}
            groups={groups}
            setGroups={setGroups}
          />
          <div>
            <Label htmlFor="gw-account">{accountLabels[t.kind] ?? "Logs in with"}</Label>
            <VaultAccountSelect
              id="gw-account"
              value={t.vault_account_id ?? ""}
              kind={t.kind}
              onChange={(v) => set({ vault_account_id: v })}
            />
          </div>
          {!isCloud(t.kind) && (
            <div>
              <Label htmlFor="gw-tls">TLS to the {kindNoun[t.kind]}</Label>
              <TLSSelect
                id="gw-tls"
                value={t.tls_mode ?? "verify"}
                kind={t.kind}
                onChange={(v) => set({ tls_mode: v })}
              />
            </div>
          )}
          <div>
            <Label htmlFor="gw-hours">
              {usesToken(t.kind) ? "Tokens" : "Sessions"} last up to (hours)
            </Label>
            <Input
              id="gw-hours"
              type="number"
              min={1}
              max={maxHours(t.kind)}
              value={t.max_token_hours}
              onChange={(e) => set({ max_token_hours: Number(e.target.value) })}
            />
          </div>
          <div>
            <Label htmlFor="gw-risk">Risk</Label>
            <Select id="gw-risk" value={t.risk} onChange={(e) => set({ risk: e.target.value })}>
              <option value="high">High</option>
              <option value="critical">Critical</option>
            </Select>
          </div>
          {takesCA(t.kind) && t.tls_mode === "verify" && (
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
          {t.kind === "aws" && (
            <p className="text-sm text-slate-500 sm:col-span-2">
              The role&apos;s trust policy must let the IAM user assume it, and its maximum session
              duration must be at least as long as sessions here.
            </p>
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

/** Bytes, in a unit people read. */
function sizeText(n: number) {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(0)} KB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(1)} GB`;
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
