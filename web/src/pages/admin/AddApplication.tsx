import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowLeft } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { FormActions } from "@/components/FormActions";
import { PickPerson, type Picked } from "@/components/PersonSearch";
import { Steps } from "@/components/Steps";
import { ErrorAlert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select } from "@/components/ui/field";
import {
  aggregate,
  createApplication,
  listCatalogApplications,
  updateApplication,
  type CatalogApplication,
  type ConnectorRun,
  type RuleChanges,
} from "@/lib/admin";
import { connectorTypes, type ConnectorType } from "@/lib/connectors";
import { RuleForm, RuleOutcome } from "./AccessEditors";
import {
  AccountFile,
  Accounts,
  Entitlements,
  FileConnector,
  Provisioning,
  ReadEverySelect,
  ReadOutcome,
} from "./Application";
import { ConnectorSettings } from "./ConnectorSettings";

const steps = [
  "Application",
  "Connection",
  "First read",
  "Accounts",
  "Entitlements",
  "Birthright access",
  "Go live",
] as const;

/** What can read an application: its connectors, account files, or nothing. */
const kinds = connectorTypes.filter((t) => !t.hrSource);
const byHand = "manual";

/**
 * Governance → Applications → Add application: everything an application
 * needs, in order, from its owner to provisioning. The application exists
 * from the first step, so the address (?app=&step=) resumes where someone
 * left off, and each step is the same piece its page uses later.
 */
export function AddApplication() {
  const [params, setParams] = useSearchParams();
  const apps = useQuery({ queryKey: ["catalog-applications"], queryFn: listCatalogApplications });
  const app = apps.data?.find((a) => a.id === params.get("app"));
  const step = app ? Math.min(Math.max(Number(params.get("step")) || 1, 1), steps.length - 1) : 0;
  const go = (n: number, id = app?.id) =>
    setParams((p) => {
      const next = new URLSearchParams(p);
      if (id) next.set("app", id);
      next.set("step", String(n));
      return next;
    });
  const connected = !!app && app.connector !== byHand;

  return (
    <>
      <Link
        to={app ? `/governance/applications/${app.id}` : "/governance/applications"}
        className="mb-4 inline-flex items-center gap-1 text-sm text-slate-500 hover:text-slate-900"
      >
        <ArrowLeft className="size-4" aria-hidden /> {app ? app.name : "Applications"}
      </Link>
      <Card>
        <CardHeader>
          <CardTitle>{app ? `Set up ${app.name}` : "Add an application"}</CardTitle>
          <Steps steps={steps} current={step} />
        </CardHeader>
        <CardContent className="text-sm">
          {apps.isPending ? (
            <p className="text-slate-500">Loading…</p>
          ) : step === 0 ? (
            <Basics apps={apps.data ?? []} onCreated={(id) => go(1, id)} />
          ) : !app ? null : step === 1 ? (
            <Connection
              app={app}
              preset={params.get("connector")}
              onDone={(kind) => go(kind === byHand ? 4 : 2)}
            />
          ) : step === 2 ? (
            <FirstRead app={app} onBack={() => go(1)} onNext={() => go(3)} />
          ) : step === 3 ? (
            <Step
              intro="Grantra matched each account to its person by employee id or email. Link the ones it could not, or mark shared and service logins as nobody's."
              onBack={() => go(2)}
              onNext={() => go(4)}
            >
              <Accounts appId={app.id} orphansOnly />
            </Step>
          ) : step === 4 ? (
            <Step
              intro={
                connected
                  ? "These came from the application, all low risk. Raise the risk of the ones that need it: it decides approval flows and who needs a second factor."
                  : "Add what people can be given in it, such as roles or groups, with their risk."
              }
              onBack={() => go(connected ? 3 : 1)}
              onNext={() => go(5)}
            >
              <Entitlements app={app} />
            </Step>
          ) : step === 5 ? (
            <Birthright app={app} onBack={() => go(4)} onNext={() => go(6)} />
          ) : (
            <GoLive app={app} onBack={() => go(5)} />
          )}
        </CardContent>
      </Card>
    </>
  );
}

/** One step's explanation, its content, and Back and Next. */
function Step({
  intro,
  children,
  onBack,
  onNext,
  next = "Next",
}: {
  intro?: ReactNode;
  children?: ReactNode;
  onBack?: () => void;
  onNext?: () => void;
  next?: string;
}) {
  return (
    <div className="flex flex-col gap-4">
      {intro && <p className="max-w-3xl text-slate-600">{intro}</p>}
      {children}
      <div className="flex gap-2 border-t border-slate-100 pt-4">
        {onNext && <Button onClick={onNext}>{next}</Button>}
        {onBack && (
          <Button variant="ghost" onClick={onBack}>
            Back
          </Button>
        )}
      </div>
    </div>
  );
}

/** Name, owner and description of a new application, or one already added by hand. */
function Basics({
  apps,
  onCreated,
}: {
  apps: CatalogApplication[];
  onCreated: (id: string) => void;
}) {
  const client = useQueryClient();
  // Applications without a connector can be set up here; the others
  // change on their own page.
  const free = apps.filter((a) => a.connector === byHand && !a.retired_at);
  const [existing, setExisting] = useState("");
  const [name, setName] = useState("");
  const [owner, setOwner] = useState<Picked>();
  const [description, setDescription] = useState("");
  const create = useMutation({
    mutationFn: async () => {
      if (existing) return existing;
      const app = await createApplication({ name, owner_id: owner?.id });
      if (description.trim()) {
        await updateApplication(app.id, {
          name: app.name,
          owner_id: owner?.id ?? "",
          description,
          read_every_hours: app.read_every_hours,
        });
      }
      return app.id;
    },
    onSuccess: async (id) => {
      await client.invalidateQueries({ queryKey: ["catalog-applications"] });
      onCreated(id);
    },
  });
  return (
    <form
      className="grid max-w-3xl gap-4 md:grid-cols-2"
      onSubmit={(e) => {
        e.preventDefault();
        create.mutate();
      }}
    >
      {free.length > 0 && (
        <div className="md:col-span-2">
          <Label htmlFor="add-existing">Application</Label>
          <Select id="add-existing" value={existing} onChange={(e) => setExisting(e.target.value)}>
            <option value="">A new application</option>
            {free.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name} (managed by hand)
              </option>
            ))}
          </Select>
        </div>
      )}
      {!existing && (
        <>
          <div>
            <Label htmlFor="add-name">Name</Label>
            <Input
              id="add-name"
              required
              maxLength={200}
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Salesforce"
            />
          </div>
          <div>
            <Label htmlFor="add-owner">Owner</Label>
            <PickPerson
              id="add-owner"
              value={owner}
              onChange={setOwner}
              placeholder="Who answers for it"
            />
          </div>
          <div className="md:col-span-2">
            <Label htmlFor="add-description">Description</Label>
            <Input
              id="add-description"
              maxLength={2000}
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              placeholder="What people use it for"
            />
          </div>
          <p className="text-slate-500 md:col-span-2">
            The owner approves requests for its access under the default approval flow.
          </p>
        </>
      )}
      <FormActions action={create} submit="Next" className="md:col-span-2" />
    </form>
  );
}

/** How Grantra reaches the application: a connector, account files, or nothing. */
function Connection({
  app,
  preset,
  onDone,
}: {
  app: CatalogApplication;
  preset: string | null;
  onDone: (kind: string) => void;
}) {
  const client = useQueryClient();
  const current = app.connector !== byHand ? app.connector : null;
  const [kind, setKind] = useState<string | null>(
    current ?? kinds.find((t) => t.kind === preset || t.name === preset)?.kind ?? null,
  );
  const type: ConnectorType | undefined = kinds.find((t) => t.kind === kind);
  const saved = () => {
    void client.invalidateQueries({ queryKey: ["catalog-applications"] });
    onDone(kind!);
  };

  if (!kind) {
    return (
      <Step intro={`How does Grantra reach ${app.name}?`}>
        <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
          {[
            ...kinds,
            {
              kind: byHand,
              name: "By hand",
              mark: "—",
              blurb:
                "No connector: Grantra keeps the catalog and someone makes the changes in the application.",
            },
          ].map((t) => (
            <button
              key={t.kind}
              type="button"
              onClick={() => (t.kind === byHand ? onDone(byHand) : setKind(t.kind))}
              className="flex items-start gap-3 rounded-lg border border-slate-200 p-3 text-left hover:border-brand-400 hover:bg-brand-50/40"
            >
              <span
                className="flex size-9 shrink-0 items-center justify-center rounded-md bg-brand-50 text-sm font-semibold text-brand-700"
                aria-hidden
              >
                {t.mark}
              </span>
              <span>
                <span className="block font-medium">{t.name}</span>
                <span className="block text-slate-500">{t.blurb}</span>
              </span>
            </button>
          ))}
        </div>
      </Step>
    );
  }
  if (kind === "csv") {
    return <FileConnector app={app} onDone={saved} onCancel={() => setKind(null)} />;
  }
  if (!type) return null;
  return (
    <div className="flex flex-col gap-3">
      <p className="font-medium">{type.name}</p>
      <ConnectorSettings
        type={type}
        label={app.name}
        current={app.connector === type.kind ? app.config : null}
        target={() => Promise.resolve(app.id)}
        onSaved={saved}
      >
        <Button type="button" variant="ghost" onClick={() => setKind(null)}>
          Another way
        </Button>
      </ConnectorSettings>
    </div>
  );
}

/** A first read proves the connection works; account files are checked, then imported. */
function FirstRead({
  app,
  onBack,
  onNext,
}: {
  app: CatalogApplication;
  onBack: () => void;
  onNext: () => void;
}) {
  const client = useQueryClient();
  const [run, setRun] = useState<ConnectorRun | null>(null);
  const read = useMutation({
    mutationFn: () => aggregate(app.id),
    onSuccess: (r) => {
      setRun(r);
      for (const k of ["connector-runs", "accounts", "entitlements"])
        void client.invalidateQueries({ queryKey: [k, app.id] });
    },
  });
  if (app.connector === "csv") {
    return (
      <Step onBack={onBack} onNext={onNext}>
        <AccountFile app={app} onEdit={onBack} />
      </Step>
    );
  }
  return (
    <Step
      intro={`Read ${app.name} once to check the connection works. This only reads: it changes nothing in ${app.name}.`}
      onBack={onBack}
      onNext={onNext}
      next={run?.status === "succeeded" ? "Next" : "Skip for now"}
    >
      <div>
        <Button onClick={() => read.mutate()} disabled={read.isPending}>
          {read.isPending ? "Reading…" : "Test: read accounts now"}
        </Button>
      </div>
      <ErrorAlert error={read.error} />
      {run && <ReadOutcome run={run} />}
    </Step>
  );
}

/** Optionally, a birthright rule giving some of the application's access to whoever matches. */
function Birthright({
  app,
  onBack,
  onNext,
}: {
  app: CatalogApplication;
  onBack: () => void;
  onNext: () => void;
}) {
  const [done, setDone] = useState<RuleChanges | null>(null);
  if (done) {
    return (
      <Step onBack={onBack} onNext={onNext}>
        <RuleOutcome changes={done} />
      </Step>
    );
  }
  return (
    <div className="flex flex-col gap-4">
      <p className="max-w-3xl text-slate-600">
        Should some people get access to {app.name} just by who they are, such as everyone in a
        department? A rule gives it on joining and takes it away when they stop matching. Others ask
        for it. Say who it is for and what it gives.
      </p>
      <RuleForm
        initial={{ name: `${app.name} by default` }}
        onDone={setDone}
        onCancel={onNext}
        submit="Add rule"
      />
      <p className="text-slate-500">
        Cancel skips this: rules can be added later under Roles and rules.
      </p>
      <div>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
      </div>
    </div>
  );
}

/** How often Grantra reads it, and whether it writes back: then the application's page. */
function GoLive({ app, onBack }: { app: CatalogApplication; onBack: () => void }) {
  const client = useQueryClient();
  const navigate = useNavigate();
  const connected = app.connector !== byHand;
  const [hours, setHours] = useState(app.read_every_hours);
  const finish = useMutation({
    mutationFn: () =>
      hours === app.read_every_hours
        ? Promise.resolve(app)
        : updateApplication(app.id, {
            name: app.name,
            description: app.description ?? "",
            owner_id: app.owner_id ?? "",
            read_every_hours: hours,
          }),
    onSuccess: async () => {
      await client.invalidateQueries({ queryKey: ["catalog-applications"] });
      void navigate(`/governance/applications/${app.id}`);
    },
  });
  return (
    <div className="flex flex-col gap-4">
      {connected && app.connector !== "csv" && (
        <div className="max-w-xs">
          <Label htmlFor="add-read">Read its accounts</Label>
          <ReadEverySelect id="add-read" value={hours} onChange={setHours} />
        </div>
      )}
      <div className="max-w-xl">
        <Provisioning app={app} connected={connected} />
      </div>
      <ErrorAlert error={finish.error} />
      <div className="flex gap-2 border-t border-slate-100 pt-4">
        <Button onClick={() => finish.mutate()} disabled={finish.isPending}>
          Finish
        </Button>
        <Button variant="ghost" onClick={onBack}>
          Back
        </Button>
      </div>
    </div>
  );
}
