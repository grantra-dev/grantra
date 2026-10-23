import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { CsvHelp } from "@/components/CsvHelp";
import { FormActions } from "@/components/FormActions";
import { Loaded } from "@/components/Loaded";
import { RiskBadge, RiskOptions } from "@/components/RiskBadge";
import { Tabs } from "@/components/Tabs";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { buttonVariants } from "@/components/ui/button-variants";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Manage, ReadOnlyFields } from "@/components/Manage";
import { PersonSearch, PickPerson } from "@/components/PersonSearch";
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
  accountFields,
  accountsPageSize,
  aggregate,
  connectorLabels,
  createEntitlement,
  deleteApplication,
  deleteEntitlement,
  importAccounts,
  linkAccount,
  listAccounts,
  listCatalogApplications,
  listConnectorRuns,
  listEntitlements,
  provision,
  retireApplication,
  setConnector,
  setProvisioning,
  updateApplication,
  updateEntitlement,
  type AppAccount,
  type CatalogApplication,
  type ConnectorRun,
  type Entitlement,
  type FileConnectorConfig,
  type ProvisioningRun,
  type ProvisioningSettings,
} from "@/lib/admin";
import { connectorTypes, secretShown } from "@/lib/connectors";
import { cleanColumns } from "@/lib/csv";
import { setEntitlementJIT, type JITSettings } from "@/lib/jit";
import { formatDate, formatTime } from "@/lib/utils";
import { NotFound } from "../NotFound";
import { Authorization } from "./Authorization";
import { ColumnMapping } from "./ColumnMapping";
import { ConnectorSettings } from "./ConnectorSettings";
import { Counts, RunStatus } from "./shared";
import { usePages } from "@/lib/hooks";
import { ShowMore } from "@/components/ShowMore";

type Tab = "entitlements" | "accounts" | "runs" | "authorization";

export function Application() {
  const { id = "" } = useParams();
  const [params, setParams] = useSearchParams();
  const { data, error, isPending } = useQuery({
    queryKey: ["catalog-applications"],
    queryFn: listCatalogApplications,
  });
  const app = data?.find((a) => a.id === id);
  const asked = params.get("tab");
  const tab: Tab =
    asked === "accounts" || asked === "runs" || asked === "authorization" ? asked : "entitlements";

  if (isPending) return <p className="text-sm text-slate-500">Loading…</p>;
  if (error) {
    return (
      <p role="alert" className="text-sm text-red-700">
        Could not load the application: {error.message}
      </p>
    );
  }
  if (!app || app.connector === "grantra") return <NotFound />;
  const connected = app.connector !== "manual";

  return (
    <>
      <Link
        to="/governance/applications"
        className="mb-4 inline-flex items-center gap-1 text-sm text-slate-500 hover:text-slate-900"
      >
        <ArrowLeft className="size-4" aria-hidden /> Applications
      </Link>
      <h1 className="mb-6 flex flex-wrap items-center gap-3 text-2xl font-semibold tracking-tight">
        {app.name}
        {app.retired_at && <Badge>Retired</Badge>}
      </h1>
      <About key={app.id} app={app} />
      <div className="mb-6 grid gap-4 lg:grid-cols-2">
        <Connector app={app} />
        <Provisioning app={app} connected={connected} />
      </div>
      <Tabs
        tabs={
          [
            ["entitlements", "Entitlements"],
            ["accounts", "Accounts"],
            ["runs", "Runs"],
            ["authorization", "Authorization"],
          ] as const
        }
        current={tab}
        onSelect={(key) => setParams(key === "entitlements" ? {} : { tab: key })}
      />
      {tab === "entitlements" && <Entitlements app={app} />}
      {tab === "accounts" && <Accounts appId={app.id} />}
      {tab === "runs" && <Runs appId={app.id} />}
      {tab === "authorization" && <Authorization key={app.id} appId={app.id} />}
    </>
  );
}

/** How often the scheduled job may read an application. */
const readChoices = [
  [0, "Only when someone asks"],
  [1, "Every hour"],
  [4, "Every 4 hours"],
  [12, "Every 12 hours"],
  [24, "Once a day"],
  [168, "Once a week"],
] as const;

const readLabel = (hours: number) =>
  readChoices.find(([h]) => h === hours)?.[1] ?? `Every ${hours} hours`;

/** How often Grantra reads an application, as a Select; any value the API set is kept. */
export function ReadEverySelect({
  id,
  value,
  onChange,
}: {
  id: string;
  value: number;
  onChange: (hours: number) => void;
}) {
  const choices = readChoices.some(([h]) => h === value)
    ? readChoices
    : [...readChoices, [value, readLabel(value)] as const];
  return (
    <Select id={id} value={value} onChange={(e) => onChange(Number(e.target.value))}>
      {choices.map(([h, label]) => (
        <option key={h} value={h}>
          {label}
        </option>
      ))}
    </Select>
  );
}

/** Who answers for the application, what it is for and how often it is read; and its retirement. */
function About({ app }: { app: CatalogApplication }) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const [editing, setEditing] = useState(false);
  const blank = {
    name: app.name,
    description: app.description ?? "",
    owner: app.owner_id ? { id: app.owner_id, name: app.owner_name ?? "" } : undefined,
    read_every_hours: app.read_every_hours,
  };
  const [form, setForm] = useState<typeof blank>(blank);
  const refresh = () => client.invalidateQueries({ queryKey: ["catalog-applications"] });
  const save = useMutation({
    mutationFn: () =>
      updateApplication(app.id, {
        name: form.name,
        description: form.description,
        owner_id: form.owner?.id ?? "",
        read_every_hours: form.read_every_hours,
      }),
    onSuccess: () => {
      setEditing(false);
      return refresh();
    },
  });
  const retire = useMutation({
    mutationFn: (retired: boolean) => retireApplication(app.id, retired),
    onSuccess: refresh,
  });
  const remove = useMutation({
    mutationFn: () => deleteApplication(app.id),
    onSuccess: async () => {
      await refresh();
      void navigate("/governance/applications");
    },
  });
  return (
    <Card className="mb-6">
      <CardHeader className="flex-row items-start justify-between gap-2">
        <CardTitle>About</CardTitle>
        {!editing && (
          <Manage>
            <Button
              size="sm"
              variant="outline"
              onClick={() => {
                setForm(blank);
                setEditing(true);
              }}
            >
              Edit
            </Button>
          </Manage>
        )}
      </CardHeader>
      <CardContent className="text-sm">
        {editing ? (
          <form
            className="grid gap-4 md:grid-cols-2"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            <div>
              <Label htmlFor="app-edit-name">Name</Label>
              <Input
                id="app-edit-name"
                required
                maxLength={200}
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </div>
            <div>
              <Label htmlFor="app-edit-owner">Owner</Label>
              <PickPerson
                id="app-edit-owner"
                value={form.owner}
                onChange={(owner) => setForm({ ...form, owner })}
                placeholder="Who answers for it"
              />
            </div>
            <div className="md:col-span-2">
              <Label htmlFor="app-edit-description">Description</Label>
              <Input
                id="app-edit-description"
                maxLength={2000}
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
                placeholder="What people use it for"
              />
            </div>
            <div>
              <Label htmlFor="app-edit-read">Read its accounts</Label>
              <ReadEverySelect
                id="app-edit-read"
                value={form.read_every_hours}
                onChange={(read_every_hours) => setForm({ ...form, read_every_hours })}
              />
            </div>
            <FormActions
              action={save}
              submit="Save"
              onCancel={() => setEditing(false)}
              className="md:col-span-2"
            />
          </form>
        ) : (
          <dl className="grid gap-3 sm:grid-cols-3">
            <div>
              <dt className="text-slate-500">Owner</dt>
              <dd>
                {app.owner_name ?? (
                  <span className="text-amber-700">
                    Nobody yet: requests that need the owner have nobody to ask
                  </span>
                )}
              </dd>
            </div>
            <div>
              <dt className="text-slate-500">Read its accounts</dt>
              <dd>
                {app.connector === "manual" ? "No connector" : readLabel(app.read_every_hours)}
              </dd>
            </div>
            <div>
              <dt className="text-slate-500">Description</dt>
              <dd>{app.description || "—"}</dd>
            </div>
          </dl>
        )}
        {app.retired_at && (
          <Alert className="mt-4">
            Retired on {formatDate(app.retired_at)}: nobody can ask for its access, and Grantra no
            longer reads or writes to it. What people still hold stays visible until a review or an
            admin removes it.
          </Alert>
        )}
        <Manage>
          <div className="mt-4 flex flex-wrap gap-2 border-t border-slate-100 pt-4">
            {app.retired_at ? (
              <Button
                size="sm"
                variant="outline"
                disabled={retire.isPending}
                onClick={() => retire.mutate(false)}
              >
                Bring back into service
              </Button>
            ) : (
              <Button
                size="sm"
                variant="outline"
                disabled={retire.isPending}
                onClick={() => {
                  if (
                    window.confirm(
                      `Retire ${app.name}? Nobody can ask for its access, and provisioning turns off. Its history stays.`,
                    )
                  )
                    retire.mutate(true);
                }}
              >
                Retire
              </Button>
            )}
            <Button
              size="sm"
              variant="ghost"
              className="text-red-700"
              disabled={remove.isPending}
              onClick={() => {
                if (
                  window.confirm(`Delete ${app.name}? Only an application without history can go.`)
                )
                  remove.mutate();
              }}
            >
              Delete
            </Button>
            <ErrorAlert error={retire.error ?? remove.error} className="w-full" />
          </div>
        </Manage>
      </CardContent>
    </Card>
  );
}

function Connector({ app }: { app: CatalogApplication }) {
  const client = useQueryClient();
  const [run, setRun] = useState<ConnectorRun | null>(null);
  const [settingUp, setSettingUp] = useState(false);
  const [editing, setEditing] = useState(false);
  const type = connectorTypes.find((t) => t.kind === app.connector && t.fields.length > 0);
  const read = useMutation({
    mutationFn: () => aggregate(app.id),
    onSuccess: (r) => {
      setRun(r);
      void client.invalidateQueries({ queryKey: ["connector-runs", app.id] });
      void client.invalidateQueries({ queryKey: ["accounts", app.id] });
      void client.invalidateQueries({ queryKey: ["entitlements", app.id] });
    },
  });
  if (settingUp) return <FileConnector app={app} onDone={() => setSettingUp(false)} />;
  if (editing && type) {
    return (
      <Card>
        <CardHeader>
          <CardTitle>Change the {type.name} connector</CardTitle>
        </CardHeader>
        <CardContent>
          <ConnectorSettings
            type={type}
            label={app.name}
            current={app.config}
            target={() => Promise.resolve(app.id)}
            onSaved={() => {
              setEditing(false);
              void client.invalidateQueries({ queryKey: ["catalog-applications"] });
            }}
          >
            <Button type="button" variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </ConnectorSettings>
        </CardContent>
      </Card>
    );
  }
  const secrets = new Set(type?.fields.filter((f) => f.type === "secret").map((f) => f.key));
  const settings = Object.entries(app.config ?? {});
  return (
    <Card>
      <CardHeader>
        <CardTitle>Connector</CardTitle>
        <p className="text-sm text-slate-500">How Grantra reads accounts and groups.</p>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        <div>
          <span className="text-slate-500">Kind: </span>
          {connectorLabels[app.connector] ?? app.connector}
        </div>
        {settings.length > 0 && (
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1">
            {settings.map(([k, v]) => (
              <div key={k} className="contents">
                <dt className="text-slate-500">{k}</dt>
                <dd className="font-mono text-xs break-all">
                  {typeof v === "object"
                    ? JSON.stringify(v)
                    : secrets.has(k)
                      ? secretShown(String(v))
                      : String(v)}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {app.connector === "manual" && (
          <>
            <p className="text-slate-500">
              Nothing to read: access to this application is managed by hand. Connect it to read its
              accounts, or, with no API, load them from a CSV export.
            </p>
            <Manage>
              <div className="flex flex-wrap gap-2">
                <Link
                  to={`/governance/applications/new?app=${app.id}&step=1`}
                  className={buttonVariants({ variant: "outline" })}
                >
                  Connect it
                </Link>
                <Button variant="ghost" onClick={() => setSettingUp(true)}>
                  Load accounts from a file
                </Button>
              </div>
            </Manage>
          </>
        )}
        <Manage>
          {app.connector === "csv" && <AccountFile app={app} onEdit={() => setSettingUp(true)} />}
          {app.connector !== "manual" && app.connector !== "csv" && (
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" disabled={read.isPending} onClick={() => read.mutate()}>
                {read.isPending ? "Reading…" : "Read accounts now"}
              </Button>
              {type && (
                <Button variant="ghost" onClick={() => setEditing(true)}>
                  Change settings
                </Button>
              )}
            </div>
          )}
        </Manage>
        <ErrorAlert error={read.error} />
        {run && <ReadOutcome run={run} />}
      </CardContent>
    </Card>
  );
}

export function ReadOutcome({ run, fromFile }: { run: ConnectorRun; fromFile?: boolean }) {
  if (run.status === "failed") {
    return (
      <Alert variant="error">
        {fromFile ? "The file was not imported: " : "Could not read the application: "}
        {run.error}
      </Alert>
    );
  }
  return (
    <Alert variant={run.status === "planned" ? "info" : "success"}>
      {run.status === "planned" ? "Nothing has changed yet. Importing would bring: " : "Read: "}
      <Counts counts={run.counts} />
    </Alert>
  );
}

/** Uploads an account file: checked first, then imported. */
export function AccountFile({ app, onEdit }: { app: CatalogApplication; onEdit: () => void }) {
  const client = useQueryClient();
  const [file, setFile] = useState<File | null>(null);
  const [run, setRun] = useState<ConnectorRun | null>(null);
  const upload = useMutation({
    mutationFn: (dryRun: boolean) => importAccounts(app.id, file!, { dryRun }),
    onSuccess: (r) => {
      setRun(r);
      void client.invalidateQueries({ queryKey: ["connector-runs", app.id] });
      if (r.status === "succeeded") {
        void client.invalidateQueries({ queryKey: ["accounts", app.id] });
        void client.invalidateQueries({ queryKey: ["entitlements", app.id] });
      }
    },
  });
  const checked = run?.status === "planned";
  return (
    <>
      <p className="text-slate-500">
        Upload the application&apos;s user list as CSV, one row per account, or one per account and
        role. Accounts missing from a file are marked as gone.
      </p>
      <div className="flex flex-wrap items-center gap-2">
        <input
          type="file"
          accept=".csv,text/csv"
          aria-label={`Account file for ${app.name}`}
          onChange={(e) => {
            setFile(e.target.files?.[0] ?? null);
            setRun(null);
          }}
          className="text-sm file:mr-3 file:rounded-md file:border file:border-slate-300 file:bg-white file:px-3 file:py-1.5 file:text-sm"
        />
        <CsvHelp
          kind="accounts"
          separator={(app.config as FileConnectorConfig | null)?.separator}
        />
        <Button
          variant="outline"
          disabled={!file || upload.isPending}
          onClick={() => upload.mutate(true)}
        >
          Check file
        </Button>
        <Button
          disabled={!file || !checked || upload.isPending}
          onClick={() => {
            if (window.confirm(`Import this file into ${app.name}?`)) upload.mutate(false);
          }}
        >
          Import
        </Button>
        <Button variant="ghost" onClick={onEdit}>
          Edit columns
        </Button>
      </div>
      <ErrorAlert error={upload.error} />
      {run && <ReadOutcome run={run} fromFile />}
    </>
  );
}

/** Sets an application up to read its accounts from files. */
export function FileConnector({
  app,
  onDone,
  onCancel = onDone,
}: {
  app: CatalogApplication;
  onDone: () => void;
  onCancel?: () => void;
}) {
  const client = useQueryClient();
  const current = (app.connector === "csv" ? app.config : null) as FileConnectorConfig | null;
  const [columns, setColumns] = useState<Record<string, string>>(current?.columns ?? {});
  const [separator, setSeparator] = useState(current?.separator ?? ";");
  const save = useMutation({
    mutationFn: () => setConnector(app.id, "csv", { columns: cleanColumns(columns), separator }),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["catalog-applications"] });
      onDone();
    },
  });
  return (
    <Card>
      <CardHeader>
        <CardTitle>Accounts from a file</CardTitle>
        <div className="flex items-center gap-1 text-sm text-slate-500">
          <p>
            How Grantra reads {app.name}&apos;s account files. Every other column is kept as an
            attribute of the account.
          </p>
          <CsvHelp kind="accounts" separator={current?.separator} align="end" />
        </div>
      </CardHeader>
      <CardContent>
        <form
          aria-label="Accounts from a file"
          className="flex flex-col gap-4 text-sm"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <ColumnMapping fields={accountFields} value={columns} onChange={setColumns} />
          <div className="max-w-xs">
            <Label htmlFor="file-separator">Between several entitlements in one cell</Label>
            <Input
              id="file-separator"
              required
              maxLength={3}
              value={separator}
              onChange={(e) => setSeparator(e.target.value)}
            />
          </div>
          <FormActions action={save} submit="Save" onCancel={onCancel} />
        </form>
      </CardContent>
    </Card>
  );
}

export function Provisioning({ app, connected }: { app: CatalogApplication; connected: boolean }) {
  const client = useQueryClient();
  const current: ProvisioningSettings = {
    mode: app.provisioning?.mode ?? "off",
    remove_unassigned: app.provisioning?.remove_unassigned ?? false,
    max_change_percent: app.provisioning?.max_change_percent ?? 10,
  };
  const [form, setForm] = useState(current);
  const [plan, setPlan] = useState<ProvisioningRun | null>(null);
  const save = useMutation({
    mutationFn: () => setProvisioning(app.id, form),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["catalog-applications"] }),
  });
  const dryRun = useMutation({
    mutationFn: () => provision(app.id, { dryRun: true }),
    onSuccess: (r) => {
      setPlan(r);
      void client.invalidateQueries({ queryKey: ["connector-runs", app.id] });
    },
  });
  const changed = JSON.stringify(form) !== JSON.stringify(current);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Provisioning</CardTitle>
        <p className="text-sm text-slate-500">
          Whether Grantra writes the access it decides back to the application.
        </p>
      </CardHeader>
      <CardContent className="flex flex-col gap-3 text-sm">
        {!connected ? (
          <p className="text-slate-500">Needs a connector first.</p>
        ) : app.connector === "csv" ? (
          <p className="text-slate-500">
            Accounts come from files, so Grantra cannot write to this application. Changes are made
            by hand and show up with the next file.
          </p>
        ) : (
          <>
            <form
              className="grid gap-3 sm:grid-cols-2"
              onSubmit={(e) => {
                e.preventDefault();
                if (
                  form.mode === "off" ||
                  window.confirm(
                    `Let Grantra write to ${app.name}? Run a dry run first to see what it would do.`,
                  )
                ) {
                  save.mutate();
                }
              }}
            >
              <ReadOnlyFields>
                <div>
                  <Label htmlFor="prov-mode">Mode</Label>
                  <Select
                    id="prov-mode"
                    value={form.mode}
                    onChange={(e) => setForm({ ...form, mode: e.target.value as "off" | "on" })}
                  >
                    <option value="off">Off, only read</option>
                    <option value="on">On, write changes</option>
                  </Select>
                </div>
                <div>
                  <Label htmlFor="prov-max">Stop if more than this % would be removed</Label>
                  <Input
                    id="prov-max"
                    type="number"
                    min={1}
                    max={100}
                    value={form.max_change_percent}
                    onChange={(e) =>
                      setForm({ ...form, max_change_percent: Number(e.target.value) })
                    }
                  />
                </div>
                <label className="flex items-center gap-2 sm:col-span-2">
                  <input
                    type="checkbox"
                    checked={form.remove_unassigned}
                    onChange={(e) => setForm({ ...form, remove_unassigned: e.target.checked })}
                  />
                  Also remove group memberships Grantra did not grant
                </label>
              </ReadOnlyFields>
              <Manage>
                <div className="flex flex-wrap gap-2 sm:col-span-2">
                  <Button type="submit" disabled={!changed || save.isPending}>
                    Save
                  </Button>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={dryRun.isPending}
                    onClick={() => dryRun.mutate()}
                  >
                    {dryRun.isPending ? "Planning…" : "Dry run"}
                  </Button>
                </div>
              </Manage>
            </form>
            <ErrorAlert error={save.error} />
            <ErrorAlert error={dryRun.error} />
            {plan && <Plan run={plan} />}
          </>
        )}
      </CardContent>
    </Card>
  );
}

const opLabels = {
  create_account: "Create account",
  enable_account: "Enable account",
  disable_account: "Disable account",
  add_member: "Add to group",
  remove_member: "Remove from group",
};

function Plan({ run }: { run: ProvisioningRun }) {
  return (
    <div className="rounded-md border border-slate-200">
      <div className="flex flex-wrap items-center gap-2 border-b border-slate-200 p-3">
        <RunStatus status={run.status} />
        <span>
          {run.actions.length === 1 ? "1 change" : `${run.actions.length} changes`} planned. Nothing
          was written.
        </span>
      </div>
      {run.error && <p className="p-3 text-red-700">{run.error}</p>}
      {run.actions.length > 0 && (
        <ul className="max-h-64 divide-y divide-slate-100 overflow-y-auto">
          {run.actions.map((a, n) => (
            <li key={n} className="flex justify-between gap-3 px-3 py-2">
              <span>{opLabels[a.op]}</span>
              <span className="text-right text-slate-600">
                {a.username || a.native_account || "new account"}
                {a.native_entitlement && ` · ${a.native_entitlement}`}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function Entitlements({ app }: { app: CatalogApplication }) {
  const appId = app.id;
  const client = useQueryClient();
  const { data, error, isPending } = useQuery({
    queryKey: ["entitlements", appId],
    queryFn: () => listEntitlements(appId),
  });
  const [form, setForm] = useState<{ name: string; native_id: string; risk: Entitlement["risk"] }>({
    name: "",
    native_id: "",
    risk: "low",
  });
  const add = useMutation({
    mutationFn: () =>
      createEntitlement(appId, {
        name: form.name,
        native_id: form.native_id,
        risk: form.risk,
      }),
    onSuccess: () => {
      setForm({ name: "", native_id: "", risk: "low" });
      void client.invalidateQueries({ queryKey: ["entitlements", appId] });
      void client.invalidateQueries({ queryKey: ["catalog-applications"] });
    },
  });
  return (
    <div className="flex flex-col gap-4">
      <Manage>
        <Card>
          <CardContent className="pt-5">
            <form
              className="flex flex-wrap items-end gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                add.mutate();
              }}
            >
              <div>
                <Label htmlFor="ent-name">Name</Label>
                <Input
                  id="ent-name"
                  required
                  value={form.name}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder="Sales reps"
                />
              </div>
              <div>
                <Label htmlFor="ent-native">Id in the application</Label>
                <Input
                  id="ent-native"
                  required
                  value={form.native_id}
                  onChange={(e) => setForm({ ...form, native_id: e.target.value })}
                  placeholder="cn=sales,ou=groups"
                />
              </div>
              <div>
                <Label htmlFor="ent-risk">Risk</Label>
                <Select
                  id="ent-risk"
                  value={form.risk}
                  onChange={(e) =>
                    setForm({ ...form, risk: e.target.value as Entitlement["risk"] })
                  }
                >
                  <RiskOptions />
                </Select>
              </div>
              <Button type="submit" disabled={add.isPending}>
                Add entitlement
              </Button>
              <ErrorAlert error={add.error} className="w-full" />
            </form>
          </CardContent>
        </Card>
      </Manage>
      <Loaded
        loading={isPending}
        error={error}
        empty={
          data?.length === 0 && "No entitlements yet. Reading the application brings its groups in."
        }
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Entitlement</TableHead>
              <TableHead>Id in the application</TableHead>
              <TableHead>Risk</TableHead>
              <TableHead>Held</TableHead>
              <TableHead>Added</TableHead>
              <TableHead>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((e) => (
              <EntitlementRow key={e.id} ent={e} byHand={app.connector === "manual"} />
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </div>
  );
}

/**
 * One entitlement, with its name, description and risk to change. Grantra
 * names only the ones added by hand: a connected application names its own,
 * and lists them, so only the hand-made ones can be deleted.
 */
function EntitlementRow({ ent, byHand }: { ent: Entitlement; byHand: boolean }) {
  const client = useQueryClient();
  const blank = { name: ent.name, description: ent.description ?? "", risk: ent.risk };
  const [form, setForm] = useState<typeof blank | null>(null);
  const refresh = () =>
    client.invalidateQueries({ queryKey: ["entitlements", ent.application_id] });
  const save = useMutation({
    mutationFn: () => updateEntitlement(ent.id, form!),
    onSuccess: () => {
      setForm(null);
      return refresh();
    },
  });
  const remove = useMutation({
    mutationFn: () => deleteEntitlement(ent.id),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["catalog-applications"] });
      return refresh();
    },
  });

  if (form) {
    return (
      <TableRow>
        <TableCell colSpan={6}>
          <form
            className="grid gap-3 md:grid-cols-3"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            <div>
              <Label htmlFor={`ent-${ent.id}-name`}>Name</Label>
              <Input
                id={`ent-${ent.id}-name`}
                required
                maxLength={200}
                readOnly={!byHand}
                title={byHand ? undefined : "The application names it"}
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </div>
            <div>
              <Label htmlFor={`ent-${ent.id}-description`}>Description</Label>
              <Input
                id={`ent-${ent.id}-description`}
                value={form.description}
                onChange={(e) => setForm({ ...form, description: e.target.value })}
                placeholder="What it lets people do"
              />
            </div>
            <div>
              <Label htmlFor={`ent-${ent.id}-risk`}>Risk</Label>
              <Select
                id={`ent-${ent.id}-risk`}
                value={form.risk}
                onChange={(e) => setForm({ ...form, risk: e.target.value as Entitlement["risk"] })}
              >
                <RiskOptions />
              </Select>
            </div>
            <p className="text-slate-500 md:col-span-3">
              Risk decides which approval flow a request takes, the separation of duties risk step
              and who needs a second factor to sign in.
            </p>
            <FormActions
              action={save}
              submit="Save"
              onCancel={() => setForm(null)}
              className="md:col-span-3"
            />
          </form>
        </TableCell>
      </TableRow>
    );
  }
  return (
    <TableRow>
      <TableCell>
        <div className="font-medium">{ent.name}</div>
        {ent.description && <div className="text-slate-500">{ent.description}</div>}
      </TableCell>
      <TableCell className="font-mono text-xs">{ent.native_id}</TableCell>
      <TableCell>
        {ent.risk === "low" ? (
          <span className="text-slate-500">Low</span>
        ) : (
          <RiskBadge risk={ent.risk} />
        )}
      </TableCell>
      <TableCell>
        <JustInTime ent={ent} />
      </TableCell>
      <TableCell className="whitespace-nowrap">{formatDate(ent.created_at)}</TableCell>
      <TableCell>
        <Manage>
          <div className="flex gap-1">
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setForm(blank)}
              aria-label={`Edit ${ent.name}`}
            >
              Edit
            </Button>
            {byHand && (
              <Button
                size="sm"
                variant="ghost"
                className="text-red-700"
                disabled={remove.isPending}
                aria-label={`Delete ${ent.name}`}
                onClick={() => {
                  if (
                    window.confirm(`Delete ${ent.name}? Only one nobody holds or asked for can go.`)
                  )
                    remove.mutate();
                }}
              >
                Delete
              </Button>
            )}
          </div>
          <ErrorAlert error={remove.error} className="mt-1" />
        </Manage>
      </TableCell>
    </TableRow>
  );
}

/** How an entitlement is held: for good, or just in time. */
function JustInTime({ ent }: { ent: Entitlement }) {
  const client = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [form, setForm] = useState<JITSettings>({
    jit_only: ent.jit_only,
    max_activation_hours: ent.max_activation_hours,
    activation_approval: ent.activation_approval,
  });
  const save = useMutation({
    mutationFn: () => setEntitlementJIT(ent.id, form),
    onSuccess: () => {
      setEditing(false);
      return client.invalidateQueries({ queryKey: ["entitlements", ent.application_id] });
    },
  });

  if (!editing) {
    return (
      <div className="flex items-center gap-2">
        {ent.jit_only ? (
          <Badge variant="amber">
            Just in time, up to {ent.max_activation_hours}h{ent.activation_approval && ", approved"}
          </Badge>
        ) : (
          <span className="text-slate-500">Standing allowed</span>
        )}
        <Manage>
          <Button
            size="sm"
            variant="ghost"
            onClick={() => setEditing(true)}
            aria-label={`Change how ${ent.name} is held`}
          >
            Change
          </Button>
        </Manage>
      </div>
    );
  }
  return (
    <form
      className="flex min-w-56 flex-col gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        save.mutate();
      }}
    >
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={form.jit_only}
          onChange={(e) => setForm({ ...form, jit_only: e.target.checked })}
        />
        Just in time only
      </label>
      <label className="flex items-center gap-2">
        Up to
        <Input
          type="number"
          min={1}
          max={24}
          className="w-16"
          aria-label="Most hours per activation"
          value={form.max_activation_hours}
          onChange={(e) => setForm({ ...form, max_activation_hours: Number(e.target.value) })}
        />
        hours per activation
      </label>
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={form.activation_approval}
          onChange={(e) => setForm({ ...form, activation_approval: e.target.checked })}
        />
        Each activation needs approval
      </label>
      <div className="flex gap-2">
        <Button type="submit" size="sm" disabled={save.isPending}>
          Save
        </Button>
        <Button type="button" size="sm" variant="outline" onClick={() => setEditing(false)}>
          Cancel
        </Button>
      </div>
      <ErrorAlert error={save.error} />
    </form>
  );
}

export function Accounts({ appId, orphansOnly = false }: { appId: string; orphansOnly?: boolean }) {
  const [orphans, setOrphans] = useState(orphansOnly);
  const list = usePages(
    ["accounts", appId, orphans],
    (after?: string) => listAccounts(appId, { orphans, after }),
    accountsPageSize,
  );
  const { rows, error, isPending } = list;
  return (
    <div className="flex flex-col gap-3">
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={orphans} onChange={(e) => setOrphans(e.target.checked)} />
        Only accounts that belong to nobody (orphans)
      </label>
      <Loaded
        loading={isPending}
        error={error}
        empty={
          rows.length === 0 &&
          (orphans
            ? "No orphan accounts."
            : "No accounts yet. Read the application to bring them in.")
        }
      >
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Account</TableHead>
              <TableHead>Belongs to</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Groups</TableHead>
              <TableHead>Last seen</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((a) => (
              <TableRow key={a.id}>
                <TableCell>{a.username || a.native_id}</TableCell>
                <TableCell>
                  {a.identity_id ? (
                    <>
                      <Link
                        to={`/people/${a.identity_id}`}
                        className="text-brand-700 hover:underline"
                      >
                        {a.identity_name}
                      </Link>
                      {a.correlated_by && (
                        <div className="text-slate-500">
                          {a.correlated_by === "manual"
                            ? "Linked by hand"
                            : `Matched by ${a.correlated_by}`}
                        </div>
                      )}
                    </>
                  ) : (
                    <Badge variant="amber">Nobody</Badge>
                  )}
                  <Manage>
                    <LinkAccount account={a} />
                  </Manage>
                </TableCell>
                <TableCell>{a.status}</TableCell>
                <TableCell>{a.entitlements.length ? a.entitlements.join(", ") : "—"}</TableCell>
                <TableCell className="whitespace-nowrap">
                  {a.last_seen_at ? formatDate(a.last_seen_at) : "—"}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
        <ShowMore query={list} />
      </Loaded>
    </div>
  );
}

/**
 * Ties an account to its person by hand, for the ones the rules could not
 * match, or marks it as nobody's: a shared or service login. Either way,
 * later reads keep the choice.
 */
function LinkAccount({ account }: { account: AppAccount }) {
  const client = useQueryClient();
  const [open, setOpen] = useState(false);
  const link = useMutation({
    mutationFn: (identity: string | null) => linkAccount(account.id, identity),
    onSuccess: () => {
      setOpen(false);
      return client.invalidateQueries({ queryKey: ["accounts", account.application_id] });
    },
  });
  const label = account.username || account.native_id;
  if (!open) {
    return (
      <button
        type="button"
        className="block text-brand-700 hover:underline"
        onClick={() => setOpen(true)}
        aria-label={`Change who ${label} belongs to`}
      >
        {account.identity_id ? "Change" : "Link to a person"}
      </button>
    );
  }
  return (
    <div className="mt-1 flex min-w-56 flex-col gap-1">
      <PersonSearch
        id={`link-${account.id}`}
        action="Link to"
        skip={account.identity_id ? [account.identity_id] : []}
        onPick={(p) => link.mutate(p.id)}
      />
      <div className="flex gap-3">
        {account.identity_id && (
          <button
            type="button"
            className="text-brand-700 hover:underline"
            onClick={() => link.mutate(null)}
          >
            Nobody&apos;s (shared or service)
          </button>
        )}
        <button
          type="button"
          className="text-slate-500 hover:underline"
          onClick={() => setOpen(false)}
        >
          Cancel
        </button>
      </div>
      <ErrorAlert error={link.error} />
    </div>
  );
}

function Runs({ appId }: { appId: string }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["connector-runs", appId],
    queryFn: () => listConnectorRuns(appId),
  });
  return (
    <Loaded loading={isPending} error={error} empty={data?.length === 0 && "No runs yet."}>
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>When</TableHead>
            <TableHead>What</TableHead>
            <TableHead>Result</TableHead>
            <TableHead>Counts</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {data?.map((r) => (
            <TableRow key={r.id} className="align-top">
              <TableCell className="whitespace-nowrap">{formatTime(r.started_at)}</TableCell>
              <TableCell>{r.kind === "aggregation" ? "Read accounts" : "Provisioning"}</TableCell>
              <TableCell>
                <RunStatus status={r.status} />
                {r.error && <div className="mt-1 text-red-700">{r.error}</div>}
              </TableCell>
              <TableCell>
                <Counts counts={r.counts} />
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </Loaded>
  );
}
