import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Download, FileCheck2, Printer } from "lucide-react";
import { useState, type FormEvent } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { FormActions } from "@/components/FormActions";
import { Loaded } from "@/components/Loaded";
import { Manage } from "@/components/Manage";
import { PageHeader } from "@/components/PageHeader";
import { Tabs } from "@/components/Tabs";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { linkButton } from "@/components/ui/button-variants";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input, Label, Select, Textarea } from "@/components/ui/field";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { listCatalogApplications } from "@/lib/admin";
import type { Me } from "@/lib/api";
import { ManageContext } from "@/lib/manage";
import {
  deleteFramework,
  frameworkPack,
  listFrameworks,
  listReports,
  reportFile,
  runReport,
  saveFramework,
  verifyReport,
  type Control,
  type Framework,
  type FrameworkInput,
  type ReportDefinition,
  type ReportParams,
} from "@/lib/reports";
import { isAdmin } from "@/lib/roles";
import { cn, formatTime } from "@/lib/utils";
import { ExternalAuditors, Schedules } from "./ReportAutomation";

type Tab = "reports" | "frameworks" | "scheduled" | "auditors" | "verify";

/** The period (and application) in the address, so a report's link can be shared. */
function useReportParams() {
  const [params, setParams] = useSearchParams();
  const defaults = useQuery({ queryKey: ["reports"], queryFn: listReports });
  const p: ReportParams = {
    from: params.get("from") ?? defaults.data?.default_from,
    to: params.get("to") ?? defaults.data?.default_to,
    application: params.get("application") ?? undefined,
  };
  const set = (next: Partial<ReportParams> & { tab?: Tab }) => {
    const merged = new URLSearchParams(params);
    for (const [k, v] of Object.entries(next)) {
      if (v) merged.set(k, v);
      else merged.delete(k);
    }
    setParams(merged);
  };
  return { p, set, defaults, search: params.toString() };
}

/** Governance → Reports: compliance reports, frameworks and evidence packs. */
export function Reports({ me }: { me: Me }) {
  const { set, defaults } = useReportParams();
  const [params] = useSearchParams();
  const tab = (params.get("tab") as Tab | null) ?? "reports";
  return (
    <div>
      <PageHeader
        title="Reports"
        description="Evidence for auditors: who reviewed and approved what, how fast leavers lost access, who holds what. Every download is recorded with its fingerprint, so it can be checked later."
      />
      <Tabs
        tabs={[
          ["reports", "Reports"],
          ["frameworks", "Frameworks"],
          ["scheduled", "Scheduled"],
          ["auditors", "External auditors"],
          ["verify", "Verify a file"],
        ]}
        current={tab}
        onSelect={(t) => set({ tab: t === "reports" ? undefined : t })}
      />
      {tab === "reports" ? (
        <Loaded loading={defaults.isPending} error={defaults.error}>
          <ReportList reports={defaults.data?.reports ?? []} />
        </Loaded>
      ) : tab === "verify" ? (
        <Verify />
      ) : (
        // Anyone here reads and downloads; only admins change these.
        <ManageContext.Provider value={isAdmin(me)}>
          {tab === "frameworks" ? (
            <Frameworks reports={defaults.data?.reports ?? []} />
          ) : tab === "scheduled" ? (
            <Schedules reports={defaults.data?.reports ?? []} />
          ) : (
            <ExternalAuditors />
          )}
        </ManageContext.Provider>
      )}
    </div>
  );
}

/** The dates a report covers, and the application when it can be narrowed. */
function PeriodPicker({ application }: { application?: boolean }) {
  const { p, set } = useReportParams();
  const apps = useQuery({
    queryKey: ["catalog-applications"],
    queryFn: listCatalogApplications,
    enabled: !!application,
  });
  return (
    <div className="flex flex-wrap items-end gap-3">
      <div>
        <Label htmlFor="report-from">From</Label>
        <Input
          id="report-from"
          type="date"
          value={p.from ?? ""}
          onChange={(e) => set({ from: e.target.value })}
        />
      </div>
      <div>
        <Label htmlFor="report-to">To</Label>
        <Input
          id="report-to"
          type="date"
          value={p.to ?? ""}
          onChange={(e) => set({ to: e.target.value })}
        />
      </div>
      {application && (
        <div>
          <Label htmlFor="report-app">Application</Label>
          <Select
            id="report-app"
            value={p.application ?? ""}
            onChange={(e) => set({ application: e.target.value })}
          >
            <option value="">All applications</option>
            {apps.data?.map((a) => (
              <option key={a.id} value={a.id}>
                {a.name}
              </option>
            ))}
          </Select>
        </div>
      )}
      <p className="pb-2 text-xs text-slate-500">
        Days are UTC, both included. The usual period is set under Settings → General.
      </p>
    </div>
  );
}

function ReportList({ reports }: { reports: ReportDefinition[] }) {
  const { search } = useReportParams();
  const frameworks = useQuery({ queryKey: ["report-frameworks"], queryFn: listFrameworks });
  /** Which enabled frameworks' controls each report gives evidence for. */
  const controls = (id: string) =>
    (frameworks.data ?? [])
      .filter((f) => f.enabled)
      .map(
        (f) => [f.name, f.controls.filter((c) => c.reports.includes(id)).map((c) => c.id)] as const,
      )
      .filter(([, ids]) => ids.length > 0)
      .map(([name, ids]) => `${name}: ${ids.join(", ")}`);
  return (
    <CardContent className="grid gap-4 pt-5 md:grid-cols-2">
      {reports.map((r) => (
        <Link
          key={r.id}
          to={`/governance/reports/${r.id}${search ? `?${search}` : ""}`}
          className="rounded-lg border border-slate-200 p-4 hover:border-brand-600 hover:bg-slate-50"
        >
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="font-semibold">{r.title}</h2>
            <Badge>{r.period ? "For a period" : "As of now"}</Badge>
          </div>
          <p className="mt-1 text-sm text-slate-500">{r.description}</p>
          {controls(r.id).length > 0 && (
            <p className="mt-2 text-xs text-slate-500">Evidence for {controls(r.id).join(" · ")}</p>
          )}
        </Link>
      ))}
    </CardContent>
  );
}

/** One report: its counts, a preview of each table, and the downloads. */
export function ReportView() {
  const { id = "" } = useParams();
  const { p, defaults, search } = useReportParams();
  const def = defaults.data?.reports.find((r) => r.id === id);
  const ready = !!p.from && !!p.to;
  const result = useQuery({
    queryKey: ["report", id, p],
    queryFn: () => runReport(id, p),
    enabled: ready && !!def,
  });
  const res = result.data;
  return (
    <div className="flex flex-col gap-4">
      <Link
        to={`/governance/reports${search ? `?${search}` : ""}`}
        className="text-sm text-brand-700"
      >
        ← All reports
      </Link>
      <PageHeader title={def?.title ?? "Report"} description={def?.description} />
      {defaults.data && !def && <Alert variant="error">There is no report “{id}”.</Alert>}
      {def && (
        <Card>
          <CardContent className="flex flex-col gap-4 pt-5">
            {!def.period && (
              <p className="text-sm text-slate-500">
                This report shows how things are now; the dates only name the period the evidence is
                for.
              </p>
            )}
            <PeriodPicker application={def.application} />
            <div className="flex flex-wrap gap-2">
              <a className={linkButton} href={reportFile(id, p, "zip")} download>
                <Download aria-hidden /> Evidence pack (.zip)
              </a>
              <a
                className={linkButton}
                href={reportFile(id, p, "html")}
                target="_blank"
                rel="noopener"
              >
                <Printer aria-hidden /> Printable summary, or PDF
              </a>
            </div>
          </CardContent>
        </Card>
      )}
      <ErrorAlert error={result.error} />
      {result.isPending && def && <p className="text-sm text-slate-500">Running the report…</p>}
      {res && (
        <>
          <p className="text-xs text-slate-500">
            {res.params.from.slice(0, 10)} to {res.params.to.slice(0, 10)}
            {res.application && ` · ${res.application}`} · generated {formatTime(res.generated_at)}
          </p>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            {res.facts.map((f) => (
              <Card key={f.label} className="p-4">
                <div className="text-2xl font-semibold">{f.value}</div>
                <div className="text-sm text-slate-500">{f.label}</div>
              </Card>
            ))}
          </div>
          {res.tables.map((t) => (
            <Card key={t.name}>
              <CardHeader className="flex-row items-start justify-between gap-3">
                <div className="min-w-0 flex-1">
                  <CardTitle>
                    {t.title} ({t.row_count})
                  </CardTitle>
                  {t.description && <CardDescription>{t.description}</CardDescription>}
                </div>
                <a className={linkButton} href={reportFile(id, p, "csv", t.name)} download>
                  <Download aria-hidden /> CSV
                </a>
              </CardHeader>
              {t.rows.length === 0 ? (
                <p className="px-5 pb-5 text-sm text-slate-500">Nothing to show.</p>
              ) : (
                <>
                  <Table className="text-xs">
                    <TableHeader>
                      <TableRow>
                        {t.columns.map((c) => (
                          <TableHead key={c.key} className="whitespace-nowrap">
                            {c.title}
                          </TableHead>
                        ))}
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {t.rows.map((row, i) => (
                        <TableRow key={i}>
                          {row.map((v, j) => (
                            <TableCell key={j} className="py-2">
                              {v}
                            </TableCell>
                          ))}
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                  {t.row_count > t.rows.length && (
                    <p className="px-5 py-3 text-sm text-slate-500">
                      Showing the first {t.rows.length} of {t.row_count}. The CSV has them all.
                    </p>
                  )}
                </>
              )}
            </Card>
          ))}
        </>
      )}
    </div>
  );
}

function Frameworks({ reports }: { reports: ReportDefinition[] }) {
  const client = useQueryClient();
  const { p } = useReportParams();
  const [editing, setEditing] = useState<Framework | "new" | null>(null);
  const { data, error, isPending } = useQuery({
    queryKey: ["report-frameworks"],
    queryFn: listFrameworks,
  });
  const remove = useMutation({
    mutationFn: (f: Framework) => deleteFramework(f.id),
    onSuccess: () => void client.invalidateQueries({ queryKey: ["report-frameworks"] }),
  });
  const title = (id: string) => reports.find((r) => r.id === id)?.title ?? id;
  return (
    <div className="flex flex-col gap-4">
      <Card>
        <CardContent className="flex flex-col gap-2 pt-5">
          <p className="text-sm text-slate-500">
            Each framework maps its controls to the reports that give their evidence. Its pack has
            every one of them for the period, with the controls.
          </p>
          <PeriodPicker />
        </CardContent>
      </Card>
      <ErrorAlert error={remove.error} />
      {editing === "new" && <FrameworkForm reports={reports} onDone={() => setEditing(null)} />}
      <Loaded loading={isPending} error={error} empty={data?.length === 0 && "No frameworks."}>
        <div className="divide-y divide-slate-200">
          {data?.map((f) =>
            editing !== "new" && editing?.id === f.id ? (
              <FrameworkForm
                key={f.id}
                framework={f}
                reports={reports}
                onDone={() => setEditing(null)}
              />
            ) : (
              <section key={f.id} className={cn("p-5", !f.enabled && "opacity-70")}>
                <div className="flex flex-wrap items-start justify-between gap-3">
                  <div className="min-w-64 flex-1">
                    <div className="flex flex-wrap items-center gap-2">
                      <h2 className="font-semibold">{f.name}</h2>
                      {!f.enabled && <Badge>Off</Badge>}
                      {f.customized && <Badge variant="brand">Changed</Badge>}
                      {!f.builtin && <Badge variant="brand">Added</Badge>}
                    </div>
                    {f.description && <p className="text-sm text-slate-500">{f.description}</p>}
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <a className={linkButton} href={frameworkPack(f.id, p)} download>
                      <Download aria-hidden /> Evidence pack
                    </a>
                    <Manage>
                      <Button variant="ghost" size="sm" onClick={() => setEditing(f)}>
                        Edit
                      </Button>
                      {(f.customized || !f.builtin) && (
                        <Button
                          variant="ghost"
                          size="sm"
                          disabled={remove.isPending}
                          onClick={() => remove.mutate(f)}
                        >
                          {f.builtin ? "Back to defaults" : "Delete"}
                        </Button>
                      )}
                    </Manage>
                  </div>
                </div>
                {f.controls.length > 0 && (
                  <Table className="mt-3">
                    <TableHeader>
                      <TableRow>
                        <TableHead>Control</TableHead>
                        <TableHead>What it asks</TableHead>
                        <TableHead>Evidence</TableHead>
                      </TableRow>
                    </TableHeader>
                    <TableBody>
                      {f.controls.map((c) => (
                        <TableRow key={c.id}>
                          <TableCell className="whitespace-nowrap font-medium">{c.id}</TableCell>
                          <TableCell>{c.title}</TableCell>
                          <TableCell>
                            {c.reports.map((r, i) => (
                              <span key={r}>
                                {i > 0 && ", "}
                                <Link
                                  className="text-brand-700"
                                  to={`/governance/reports/${r}?from=${p.from ?? ""}&to=${p.to ?? ""}`}
                                >
                                  {title(r)}
                                </Link>
                              </span>
                            ))}
                          </TableCell>
                        </TableRow>
                      ))}
                    </TableBody>
                  </Table>
                )}
              </section>
            ),
          )}
        </div>
      </Loaded>
      <Manage>
        {editing !== "new" && (
          <div>
            <Button variant="outline" onClick={() => setEditing("new")}>
              Add a framework
            </Button>
          </div>
        )}
      </Manage>
    </div>
  );
}

const emptyControl = (): Control => ({ id: "", title: "", reports: [] });

/** Adds a framework, or edits one: a built-in one keeps its name. */
function FrameworkForm({
  framework,
  reports,
  onDone,
}: {
  framework?: Framework;
  reports: ReportDefinition[];
  onDone: () => void;
}) {
  const client = useQueryClient();
  const [f, setF] = useState<FrameworkInput>(
    framework ?? { name: "", description: "", enabled: true, controls: [emptyControl()] },
  );
  const save = useMutation({
    mutationFn: () => saveFramework(framework?.id, f),
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["report-frameworks"] });
      onDone();
    },
  });
  const setControl = (i: number, c: Partial<Control>) =>
    setF({ ...f, controls: f.controls.map((x, n) => (n === i ? { ...x, ...c } : x)) });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    save.mutate();
  };
  return (
    <form
      onSubmit={submit}
      className="flex flex-col gap-4 border-y border-slate-200 bg-slate-50 p-5"
    >
      <h2 className="font-semibold">{framework ? `Edit ${framework.name}` : "Add a framework"}</h2>
      {!framework?.builtin && (
        <div className="grid gap-3 md:grid-cols-2">
          <div>
            <Label htmlFor="fw-name">Name</Label>
            <Input
              id="fw-name"
              required
              maxLength={100}
              value={f.name}
              placeholder="DORA, ENS, internal policy…"
              onChange={(e) => setF({ ...f, name: e.target.value })}
            />
          </div>
          <div className="md:col-span-2">
            <Label htmlFor="fw-description">Description</Label>
            <Textarea
              id="fw-description"
              maxLength={1000}
              className="min-h-16"
              value={f.description}
              onChange={(e) => setF({ ...f, description: e.target.value })}
            />
          </div>
        </div>
      )}
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={f.enabled}
          onChange={(e) => setF({ ...f, enabled: e.target.checked })}
        />
        On: show it, and which of its controls each report covers
      </label>
      <fieldset className="flex flex-col gap-3">
        <legend className="mb-1 text-sm font-medium">Controls</legend>
        {f.controls.map((c, i) => (
          <div
            key={i}
            className="flex flex-col gap-2 rounded-md border border-slate-200 bg-white p-3"
          >
            <div className="grid gap-2 md:grid-cols-[10rem_1fr_auto]">
              <Input
                aria-label={`Control ${i + 1} id`}
                required
                maxLength={40}
                placeholder="CC6.2"
                value={c.id}
                onChange={(e) => setControl(i, { id: e.target.value })}
              />
              <Input
                aria-label={`Control ${i + 1} title`}
                required
                maxLength={300}
                placeholder="What the control asks"
                value={c.title}
                onChange={(e) => setControl(i, { title: e.target.value })}
              />
              <Button
                type="button"
                variant="ghost"
                size="sm"
                onClick={() => setF({ ...f, controls: f.controls.filter((_, n) => n !== i) })}
              >
                Remove
              </Button>
            </div>
            <div className="flex flex-wrap gap-x-4 gap-y-1">
              {reports.map((r) => (
                <label key={r.id} className="flex items-center gap-1.5 text-sm">
                  <input
                    type="checkbox"
                    checked={c.reports.includes(r.id)}
                    onChange={(e) =>
                      setControl(i, {
                        reports: e.target.checked
                          ? [...c.reports, r.id]
                          : c.reports.filter((x) => x !== r.id),
                      })
                    }
                  />
                  {r.title}
                </label>
              ))}
            </div>
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => setF({ ...f, controls: [...f.controls, emptyControl()] })}
          >
            Add a control
          </Button>
        </div>
      </fieldset>
      <FormActions
        action={save}
        submit={framework ? "Save" : "Add framework"}
        disabled={f.controls.some((c) => c.reports.length === 0)}
        onCancel={onDone}
      />
    </form>
  );
}

/** Checks a downloaded pack or file against its manifest and the audit log. */
function Verify() {
  const check = useMutation({ mutationFn: verifyReport });
  const v = check.data;
  return (
    <Card>
      <CardHeader>
        <CardTitle>Verify a file</CardTitle>
        <CardDescription>
          Pick an evidence pack or a report someone downloaded. Grantra checks every file in a pack
          against its manifest, and that its own audit log recorded handing it out. The file stays
          in your browser and on this server; it is not stored.
        </CardDescription>
      </CardHeader>
      <CardContent className="flex flex-col gap-4">
        <Input
          type="file"
          aria-label="File to verify"
          className="py-1.5"
          accept=".zip,.csv,.html"
          onChange={(e) => {
            const file = e.target.files?.[0];
            if (file) check.mutate(file);
          }}
        />
        {check.isPending && <p className="text-sm text-slate-500">Checking…</p>}
        <ErrorAlert error={check.error} />
        {v && (
          <Alert variant={v.valid ? "success" : "error"}>
            <p className="flex items-center gap-2 font-medium">
              <FileCheck2 className="size-4" aria-hidden />
              {v.valid
                ? `Unchanged since Grantra handed it out${v.generated_at ? ` on ${formatTime(v.generated_at)}` : ""}${v.generated_by ? ` to ${v.generated_by}` : ""}.`
                : "This file does not match what Grantra handed out."}
            </p>
            {v.manifest && (
              <p className="mt-1">
                {v.manifest.title}, {v.manifest.from} to {v.manifest.to}
                {v.manifest.application && `, ${v.manifest.application}`} ·{" "}
                {v.manifest.files.length} files
              </p>
            )}
            {v.problems.length > 0 && (
              <ul className="mt-1 list-disc pl-5">
                {v.problems.map((p) => (
                  <li key={p}>{p}</li>
                ))}
              </ul>
            )}
            <p className="mt-1 text-xs break-all opacity-80">SHA-256 {v.sha256}</p>
          </Alert>
        )}
      </CardContent>
    </Card>
  );
}
