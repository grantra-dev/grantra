import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, ExternalLink } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { Manage } from "@/components/Manage";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select } from "@/components/ui/field";
import {
  aggregate,
  connectorLabels,
  createApplication,
  listCatalogApplications,
  setConnector,
  type CatalogApplication,
  type ConnectorRun,
} from "@/lib/admin";
import {
  connectorConfig,
  connectorTypes,
  missingFields,
  type ConnectorField,
  type ConnectorType,
} from "@/lib/connectors";
import { cn } from "@/lib/utils";
import { Counts } from "./shared";

const docsBase = "https://github.com/grantra-dev/grantra/blob/main/docs/connectors.md";

/** Settings → Connectors: what Grantra can connect to, and a guided way to do it. */
export function Connectors() {
  const apps = useQuery({ queryKey: ["catalog-applications"], queryFn: listCatalogApplications });
  const [picked, setPicked] = useState<ConnectorType | null>(null);
  const connected = (apps.data ?? []).filter(
    (a) => a.connector !== "manual" && a.connector !== "grantra",
  );

  return (
    <div className="flex flex-col gap-6">
      {picked && (
        <Wizard
          key={picked.name}
          type={picked}
          apps={apps.data ?? []}
          onClose={() => setPicked(null)}
        />
      )}
      <section aria-labelledby="connected">
        <h2 id="connected" className="mb-2 text-sm font-semibold text-slate-700">
          Connected
        </h2>
        <Card>
          {apps.isPending ? (
            <p className="p-4 text-sm text-slate-500">Loading…</p>
          ) : apps.error ? (
            <p role="alert" className="p-4 text-sm text-red-700">
              Could not load the applications: {apps.error.message}
            </p>
          ) : connected.length === 0 ? (
            <p className="p-4 text-sm text-slate-500">
              No application is connected yet. Pick one below.
            </p>
          ) : (
            <ul className="divide-y divide-slate-100">
              {connected.map((a) => (
                <li key={a.id} className="flex items-center justify-between gap-3 p-4 text-sm">
                  <Link to={`/governance/applications/${a.id}`} className="font-medium">
                    {a.name}
                  </Link>
                  <span className="text-slate-500">
                    {connectorLabels[a.connector] ?? a.connector}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </Card>
      </section>
      <section aria-labelledby="gallery">
        <h2 id="gallery" className="mb-2 text-sm font-semibold text-slate-700">
          Connect an application
        </h2>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {connectorTypes.map((t) => (
            <Card key={t.name} className={cn("flex flex-col p-4", t.planned && "bg-slate-50")}>
              <div className="flex items-start gap-3">
                <span
                  className="flex size-10 shrink-0 items-center justify-center rounded-md bg-brand-50 text-sm font-semibold text-brand-700"
                  aria-hidden
                >
                  {t.mark}
                </span>
                <div className="min-w-0">
                  <div className="flex flex-wrap items-center gap-2 font-medium">
                    {t.name}
                    {t.planned && <Badge>Planned</Badge>}
                  </div>
                  <p className="text-sm text-slate-500">{t.blurb}</p>
                </div>
              </div>
              <div className="mt-auto pt-3">
                {t.planned ? (
                  <p className="text-xs text-slate-500">{t.planned}</p>
                ) : (
                  <Manage>
                    <Button
                      size="sm"
                      variant="outline"
                      onClick={() => setPicked(t)}
                      aria-label={`Connect ${t.name}`}
                    >
                      Connect
                    </Button>
                  </Manage>
                )}
              </div>
            </Card>
          ))}
        </div>
      </section>
    </div>
  );
}

const steps = ["Application", "Connection", "Test"] as const;

/** Three steps: which application, how to reach it, and a first read to prove it works. */
function Wizard({
  type,
  apps,
  onClose,
}: {
  type: ConnectorType;
  apps: CatalogApplication[];
  onClose: () => void;
}) {
  const client = useQueryClient();
  const [step, setStep] = useState(0);
  // Only applications without a connector can take one here; the others
  // change theirs on their own page.
  const free = apps.filter((a) => a.connector === "manual");
  const [appId, setAppId] = useState("");
  const [name, setName] = useState("");
  const [values, setValues] = useState<Record<string, string | boolean>>({
    directory: "active_directory",
  });
  const [app, setApp] = useState<CatalogApplication | null>(null);
  const [run, setRun] = useState<ConnectorRun | null>(null);

  const save = useMutation({
    mutationFn: async () => {
      const target =
        app && app.id === appId
          ? app
          : (free.find((a) => a.id === appId) ?? (await createApplication({ name: name.trim() })));
      // An application made here keeps existing if the settings are wrong,
      // so trying again fixes it rather than making another.
      setApp(target);
      setAppId(target.id);
      await setConnector(target.id, type.kind!, connectorConfig(type, values));
      return target;
    },
    onSuccess: () => setStep(2),
    onSettled: () => void client.invalidateQueries({ queryKey: ["catalog-applications"] }),
  });
  const test = useMutation({
    mutationFn: () => aggregate(app!.id),
    onSuccess: (r) => {
      setRun(r);
      void client.invalidateQueries({ queryKey: ["connector-runs", app!.id] });
    },
  });

  const missing = missingFields(type, values);
  const named = appId !== "" || name.trim() !== "";
  return (
    <Card>
      <CardHeader className="flex-row items-start justify-between">
        <div>
          <CardTitle>Connect {type.name}</CardTitle>
          <ol className="mt-2 flex flex-wrap gap-4 text-sm" aria-label="Steps">
            {steps.map((s, i) => (
              <li
                key={s}
                aria-current={i === step ? "step" : undefined}
                className={cn(
                  "flex items-center gap-1.5",
                  i === step ? "font-medium text-brand-700" : "text-slate-500",
                )}
              >
                <span
                  className={cn(
                    "flex size-5 items-center justify-center rounded-full text-xs",
                    i < step ? "bg-brand-600 text-white" : "border border-current",
                  )}
                >
                  {i < step ? <Check className="size-3" aria-hidden /> : i + 1}
                </span>
                {s}
              </li>
            ))}
          </ol>
        </div>
        <Button variant="ghost" size="sm" onClick={onClose}>
          Close
        </Button>
      </CardHeader>
      <CardContent>
        {step === 0 && (
          <form
            className="flex max-w-lg flex-col gap-4 text-sm"
            onSubmit={(e) => {
              e.preventDefault();
              setStep(1);
            }}
          >
            {free.length > 0 && (
              <div>
                <Label htmlFor="wizard-app">Application</Label>
                <Select id="wizard-app" value={appId} onChange={(e) => setAppId(e.target.value)}>
                  <option value="">A new application</option>
                  {free.map((a) => (
                    <option key={a.id} value={a.id}>
                      {a.name}
                    </option>
                  ))}
                </Select>
              </div>
            )}
            {appId === "" && (
              <div>
                <Label htmlFor="wizard-name">Name of the new application</Label>
                <Input
                  id="wizard-name"
                  required
                  maxLength={200}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  placeholder={type.kind === "ldap" ? "Corporate directory" : "CRM"}
                />
              </div>
            )}
            <div>
              <Button type="submit" disabled={!named}>
                Next
              </Button>
            </div>
          </form>
        )}
        {step === 1 && (
          <form
            className="flex max-w-lg flex-col gap-4 text-sm"
            onSubmit={(e) => {
              e.preventDefault();
              save.mutate();
            }}
          >
            {type.fields.length === 0 ? (
              <p className="text-slate-600">
                Nothing to fill in: you upload the files from the application&apos;s page, where you
                can also say which column is which.
              </p>
            ) : (
              type.fields.map((f) => (
                <Field
                  key={f.key}
                  f={f}
                  value={values[f.key]}
                  onChange={(v) => setValues({ ...values, [f.key]: v })}
                />
              ))
            )}
            <a
              href={docsBase + type.docs}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-brand-600"
            >
              Every setting, explained <ExternalLink className="size-3.5" aria-hidden />
            </a>
            {save.error && <Alert variant="error">{save.error.message}</Alert>}
            <div className="flex gap-2">
              <Button type="submit" disabled={missing.length > 0 || save.isPending}>
                {save.isPending ? "Saving…" : "Save and continue"}
              </Button>
              <Button type="button" variant="ghost" onClick={() => setStep(0)}>
                Back
              </Button>
            </div>
          </form>
        )}
        {step === 2 && app && (
          <div className="flex flex-col gap-3 text-sm">
            {type.kind === "csv" ? (
              <p>
                {app.name} now reads its accounts from files.{" "}
                <Link
                  to={`/governance/applications/${app.id}`}
                  className="text-brand-600 underline"
                >
                  Upload the first file
                </Link>{" "}
                from its page: Grantra checks it before importing anything.
              </p>
            ) : (
              <>
                <p>
                  {app.name} is connected. Read its accounts once to check the connection works:
                  this only reads, it changes nothing in {app.name}.
                </p>
                <div>
                  <Button onClick={() => test.mutate()} disabled={test.isPending}>
                    {test.isPending ? "Reading…" : "Test: read accounts now"}
                  </Button>
                </div>
                {test.error && <Alert variant="error">{test.error.message}</Alert>}
                {run &&
                  (run.status === "failed" ? (
                    <Alert variant="error">
                      Could not read {app.name}: {run.error}. Go back to fix the settings, or check
                      that the secret is in the server&apos;s environment.
                    </Alert>
                  ) : (
                    <Alert variant="success">
                      It works: <Counts counts={run.counts} />. Grantra reads it again every hour.
                    </Alert>
                  ))}
                <div className="flex gap-2">
                  <Button variant="ghost" onClick={() => setStep(1)}>
                    Back to the settings
                  </Button>
                </div>
              </>
            )}
            <Link to={`/governance/applications/${app.id}`} className="text-brand-600">
              Open {app.name}
            </Link>
          </div>
        )}
      </CardContent>
    </Card>
  );
}

function Field({
  f,
  value,
  onChange,
}: {
  f: ConnectorField;
  value: string | boolean | undefined;
  onChange: (v: string | boolean) => void;
}) {
  const id = `connector-${f.key}`;
  if (f.type === "checkbox") {
    return (
      <label className="flex items-center gap-2">
        <input
          id={id}
          type="checkbox"
          checked={value === true}
          onChange={(e) => onChange(e.target.checked)}
        />
        {f.label}
      </label>
    );
  }
  return (
    <div>
      <Label htmlFor={id}>{f.label}</Label>
      {f.type === "select" ? (
        <Select id={id} value={String(value ?? "")} onChange={(e) => onChange(e.target.value)}>
          {f.options?.map(([v, label]) => (
            <option key={v} value={v}>
              {label}
            </option>
          ))}
        </Select>
      ) : (
        <Input
          id={id}
          type={f.type === "url" ? "url" : "text"}
          required={f.required}
          value={String(value ?? "")}
          onChange={(e) => onChange(e.target.value)}
          placeholder={f.placeholder}
          spellCheck={false}
          autoComplete="off"
        />
      )}
      {f.help && <p className="mt-1 text-xs text-slate-500">{f.help}</p>}
    </div>
  );
}
