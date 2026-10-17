import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Loaded } from "@/components/Loaded";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
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
  createSource,
  listSourceRuns,
  listSources,
  hrFields,
  syncSource,
  updateSource,
  type IdentitySource,
  type Issue,
  type SyncResult,
} from "@/lib/admin";
import { cleanColumns } from "@/lib/csv";
import { formatTime } from "@/lib/utils";
import { ColumnMapping } from "./ColumnMapping";
import { Counts, RunStatus } from "./shared";

export function HrSources() {
  const { data, error, isPending } = useQuery({ queryKey: ["sources"], queryFn: listSources });
  const [adding, setAdding] = useState(false);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex items-center justify-between gap-3">
        <p className="text-sm text-slate-500">
          HR is the authority on who works here. Each sync turns the differences into joiners,
          movers and leavers. Check a file first: nothing changes until you apply it.
        </p>
        {!adding && (
          <Button variant="outline" className="shrink-0" onClick={() => setAdding(true)}>
            New HR source
          </Button>
        )}
      </div>
      {adding && <SourceForm onDone={() => setAdding(false)} />}
      <Loaded
        loading={isPending}
        error={error}
        empty={data?.length === 0 && "No HR source yet. Add one to bring people in from an export."}
      >
        <div className="divide-y divide-slate-200">
          {data?.map((s) => (
            <Source key={s.id} source={s} />
          ))}
        </div>
      </Loaded>
    </div>
  );
}

/** Creates a source, or edits one when given. */
function SourceForm({ source, onDone }: { source?: IdentitySource; onDone: () => void }) {
  const client = useQueryClient();
  const [name, setName] = useState(source?.name ?? "");
  const [missing, setMissing] = useState<"leave" | "ignore">(source?.config.missing ?? "leave");
  const [maxLeavers, setMaxLeavers] = useState(source?.config.max_leaver_percent ?? 10);
  const [columns, setColumns] = useState<Record<string, string>>(source?.config.columns ?? {});
  const save = useMutation({
    mutationFn: () => {
      const s = {
        name,
        config: { columns: cleanColumns(columns), missing, max_leaver_percent: maxLeavers },
      };
      return source ? updateSource(source.id, s) : createSource(s);
    },
    onSuccess: () => {
      void client.invalidateQueries({ queryKey: ["sources"] });
      onDone();
    },
  });
  const title = source ? `Edit ${source.name}` : "New HR source";
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          aria-label={title}
          className="grid gap-4 md:grid-cols-3"
          onSubmit={(e) => {
            e.preventDefault();
            save.mutate();
          }}
        >
          <div>
            <Label htmlFor="source-name">Name</Label>
            <Input
              id="source-name"
              required
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Workday export"
            />
          </div>
          <div>
            <Label htmlFor="source-missing">People missing from a file</Label>
            <Select
              id="source-missing"
              value={missing}
              onChange={(e) => setMissing(e.target.value as "leave" | "ignore")}
            >
              <option value="leave">Become leavers (full exports)</option>
              <option value="ignore">Stay as they are (files with only changes)</option>
            </Select>
          </div>
          <div>
            <Label htmlFor="source-max">Stop if more than this % would leave at once</Label>
            <Input
              id="source-max"
              type="number"
              min={1}
              max={100}
              value={maxLeavers}
              onChange={(e) => setMaxLeavers(Number(e.target.value))}
            />
          </div>
          <div className="md:col-span-3">
            <ColumnMapping fields={hrFields} value={columns} onChange={setColumns} />
          </div>
          <p className="text-sm text-slate-500 md:col-span-3">
            Files are CSV with a header row and one person per line; see the identity lifecycle docs
            for every column. Any other column is kept as an attribute of the person.
          </p>
          {save.error && (
            <Alert variant="error" className="md:col-span-3">
              {save.error.message}
            </Alert>
          )}
          <div className="flex gap-2 md:col-span-3">
            <Button type="submit" disabled={save.isPending}>
              {source ? "Save" : "Add source"}
            </Button>
            <Button type="button" variant="ghost" onClick={onDone}>
              Cancel
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function Source({ source }: { source: IdentitySource }) {
  const client = useQueryClient();
  const [file, setFile] = useState<File | null>(null);
  const [result, setResult] = useState<SyncResult | null>(null);
  const [showRuns, setShowRuns] = useState(false);
  const [editing, setEditing] = useState(false);
  const sync = useMutation({
    mutationFn: (o: { dryRun: boolean; force?: boolean }) => syncSource(source.id, file!, o),
    onSuccess: (r) => {
      setResult(r);
      void client.invalidateQueries({ queryKey: ["sources"] });
      void client.invalidateQueries({ queryKey: ["source-runs", source.id] });
      if (r.status === "applied") void client.invalidateQueries({ queryKey: ["identities"] });
    },
  });
  const last = source.last_run;
  const checked = result?.dry_run && result.status === "planned";
  const mapped = Object.entries(source.config.columns ?? {});

  if (editing) {
    return (
      <div className="p-5">
        <SourceForm source={source} onDone={() => setEditing(false)} />
      </div>
    );
  }
  return (
    <div className="p-5">
      <div className="flex flex-wrap items-start justify-between gap-3">
        <div>
          <h3 className="font-semibold">{source.name}</h3>
          <p className="text-sm text-slate-500">
            CSV ·{" "}
            {source.config.missing === "ignore"
              ? "people missing from a file stay as they are"
              : "people missing from a file become leavers"}{" "}
            · stops above {source.config.max_leaver_percent ?? 10}% leavers
          </p>
          {mapped.length > 0 && (
            <p className="text-sm text-slate-500">
              Columns: {mapped.map(([field, column]) => `${field} from “${column}”`).join(", ")}
            </p>
          )}
        </div>
        <div className="text-right text-sm">
          {last ? (
            <>
              <RunStatus status={last.status} />
              <div className="mt-1 text-slate-500">Last sync {formatTime(last.created_at)}</div>
            </>
          ) : (
            <span className="text-slate-500">Never synced</span>
          )}
        </div>
      </div>

      <div className="mt-4 flex flex-wrap items-center gap-2">
        <label className="text-sm">
          <span className="sr-only">HR export for {source.name}</span>
          <input
            type="file"
            accept=".csv,text/csv"
            aria-label={`HR export for ${source.name}`}
            onChange={(e) => {
              setFile(e.target.files?.[0] ?? null);
              setResult(null);
            }}
            className="text-sm file:mr-3 file:rounded-md file:border file:border-slate-300 file:bg-white file:px-3 file:py-1.5 file:text-sm"
          />
        </label>
        <Button
          variant="outline"
          disabled={!file || sync.isPending}
          onClick={() => sync.mutate({ dryRun: true })}
        >
          Check file
        </Button>
        <Button
          disabled={!file || !checked || sync.isPending}
          onClick={() => {
            if (window.confirm(`Apply this file to ${source.name}?`))
              sync.mutate({ dryRun: false });
          }}
        >
          Apply
        </Button>
        <Button variant="ghost" onClick={() => setShowRuns((v) => !v)}>
          {showRuns ? "Hide history" : "History"}
        </Button>
        <Button variant="ghost" onClick={() => setEditing(true)}>
          Edit
        </Button>
      </div>
      {sync.error && (
        <Alert variant="error" className="mt-3">
          {sync.error.message}
        </Alert>
      )}
      {result && (
        <SyncOutcome
          result={result}
          onForce={() => {
            if (
              window.confirm(
                "Apply anyway? Everyone listed as a leaver loses their access straight away.",
              )
            ) {
              sync.mutate({ dryRun: false, force: true });
            }
          }}
        />
      )}
      {showRuns && <Runs sourceId={source.id} />}
    </div>
  );
}

const changeLabels = {
  joiner: "Joins",
  mover: "Moves",
  leaver: "Leaves",
  rehire: "Comes back",
  update: "Details change",
};

function SyncOutcome({ result, onForce }: { result: SyncResult; onForce: () => void }) {
  const shown = result.changes.filter((c) => c.kind !== "update").slice(0, 50);
  return (
    <div className="mt-4 rounded-md border border-slate-200">
      <div className="flex flex-wrap items-center gap-3 border-b border-slate-200 p-3 text-sm">
        <RunStatus status={result.status} />
        <Counts counts={result.counts} />
        <span className="text-slate-500">· {result.unchanged} unchanged</span>
      </div>
      {result.status === "planned" && (
        <p className="p-3 text-sm text-slate-600">
          Nothing has changed yet. Apply the file to make these changes.
        </p>
      )}
      {result.status === "applied" && (
        <p className="p-3 text-sm text-emerald-800">
          Applied. Joiners and movers get their birthright access now.
        </p>
      )}
      <IssueList title="Errors (nothing was applied)" issues={result.errors} tone="error" />
      {result.status === "refused" && (
        <div className="px-3 pb-3">
          <Button variant="outline" onClick={onForce}>
            Apply anyway
          </Button>
        </div>
      )}
      <IssueList title="Warnings" issues={result.warnings} tone="info" />
      {shown.length > 0 && (
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>Person</TableHead>
              <TableHead>Change</TableHead>
              <TableHead>What changes</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {shown.map((c) => (
              <TableRow key={`${c.kind}-${c.external_id}`} className="align-top">
                <TableCell>
                  <div className="font-medium">{c.display_name}</div>
                  <div className="text-slate-500">Employee {c.external_id}</div>
                </TableCell>
                <TableCell>
                  <Badge variant={c.kind === "leaver" ? "red" : "brand"}>
                    {changeLabels[c.kind]}
                  </Badge>
                  {c.linked && <div className="mt-1 text-slate-500">Matched by email</div>}
                </TableCell>
                <TableCell className="text-sm">
                  {c.changes
                    ? Object.entries(c.changes)
                        .map(([f, v]) => `${f}: ${v.from || "—"} → ${v.to || "—"}`)
                        .join("; ")
                    : (c.reason ?? "—")}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

function IssueList({
  title,
  issues,
  tone,
}: {
  title: string;
  issues: Issue[];
  tone: "error" | "info";
}) {
  if (!issues?.length) return null;
  return (
    <Alert variant={tone} className="m-3">
      <div className="font-medium">{title}</div>
      <ul className="mt-1 list-disc pl-5">
        {issues.slice(0, 20).map((i, n) => (
          <li key={n}>
            {i.line ? `Line ${i.line}: ` : ""}
            {i.message}
          </li>
        ))}
        {issues.length > 20 && <li>…and {issues.length - 20} more</li>}
      </ul>
    </Alert>
  );
}

function Runs({ sourceId }: { sourceId: string }) {
  const { data, error, isPending } = useQuery({
    queryKey: ["source-runs", sourceId],
    queryFn: () => listSourceRuns(sourceId),
  });
  return (
    <div className="mt-4">
      <Loaded loading={isPending} error={error} empty={data?.length === 0 && "No syncs yet."}>
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>When</TableHead>
              <TableHead>Result</TableHead>
              <TableHead>Changes</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {data?.map((r) => (
              <TableRow key={r.id}>
                <TableCell className="whitespace-nowrap">{formatTime(r.created_at)}</TableCell>
                <TableCell>
                  <RunStatus status={r.status} />
                </TableCell>
                <TableCell>
                  <Counts counts={r.counts} />
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </Loaded>
    </div>
  );
}
