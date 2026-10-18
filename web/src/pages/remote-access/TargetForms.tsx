import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import {
  createGatewayTarget,
  deleteGatewayTarget,
  isCloud,
  updateGatewayTarget,
  usesToken,
  type Clipboard,
  type RDPSecurity,
  type GatewayKind,
  type GatewayTarget,
  type GatewayTargetSettings,
  type NewGatewayTarget,
  type TLSMode,
} from "@/lib/gateway";
import { listVaultAccounts } from "@/lib/vault";

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
        <>
          <div>
            <Label htmlFor={`${idPrefix}-sec`}>Security</Label>
            <Select
              id={`${idPrefix}-sec`}
              value={s.rdp_security ?? "nla"}
              onChange={(e) => set({ rdp_security: e.target.value as RDPSecurity })}
            >
              <option value="nla">Network Level Authentication (Windows)</option>
              <option value="tls">TLS only (desktops without NLA, such as xrdp)</option>
            </Select>
          </div>
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
        </>
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
    out.rdp_security = s.rdp_security;
  } else if (kind !== "aws") {
    out.cloud_role = s.cloud_role;
    if (kind === "azure") out.cloud_directory = s.cloud_directory;
  }
  return out;
}

/** Whether a kind of target takes its own CA certificate. */
const takesCA = (k: GatewayKind) => usesToken(k);

export function TargetSettings({ t, onDone }: { t: GatewayTarget; onDone: () => void }) {
  const client = useQueryClient();
  const [s, setS] = useState<GatewayTargetSettings>({
    address: t.address,
    database: t.database ?? "",
    tls_mode: t.tls_mode,
    ca_certificate: t.ca_certificate ?? "",
    vault_account_id: t.vault_account_id ?? "",
    clipboard: t.clipboard,
    rdp_security: t.rdp_security,
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

export function NewTarget({ onDone }: { onDone: () => void }) {
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
    rdp_security: "nla",
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
