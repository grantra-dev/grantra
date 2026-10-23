// Compliance reports (Governance → Reports): reports on what Grantra
// records, frameworks whose controls point to them, and evidence packs.
import { api, send } from "./api";

export interface ReportColumn {
  key: string;
  title: string;
}

export interface ReportTable {
  name: string;
  title: string;
  description?: string;
  columns: ReportColumn[];
}

export interface ReportDefinition {
  id: string;
  title: string;
  description: string;
  /** Covers what happened in a period; otherwise shows how things are now. */
  period: boolean;
  /** Can be narrowed to one application. */
  application: boolean;
  tables: ReportTable[];
}

export interface ReportResult {
  report: ReportDefinition;
  params: { from: string; to: string; application_id?: string };
  application?: string;
  generated_at: string;
  facts: { label: string; value: number }[];
  /** A preview: rows holds the first ones, row_count says how many there are. */
  tables: (ReportTable & { rows: string[][]; row_count: number })[];
}

export interface Control {
  id: string;
  title: string;
  reports: string[];
}

export interface Framework {
  id: string;
  name: string;
  description: string;
  builtin: boolean;
  customized: boolean;
  enabled: boolean;
  controls: Control[];
  updated_by?: string;
  updated_at?: string;
}

export type FrameworkInput = Pick<Framework, "name" | "description" | "enabled" | "controls">;

export interface Verification {
  valid: boolean;
  pack: boolean;
  sha256: string;
  manifest?: {
    title: string;
    report?: string;
    framework?: string;
    from: string;
    to: string;
    application?: string;
    generated_at: string;
    generated_by: { actor: string; name?: string };
    files: { path: string; sha256: string; bytes: number; rows?: number }[];
  };
  generated_at?: string;
  generated_by?: string;
  problems: string[];
}

/** The period and application a report runs for; empty means the default. */
export interface ReportParams {
  from?: string;
  to?: string;
  application?: string;
}

export type DownloadFormat = "csv" | "html" | "zip";

const query = (p: ReportParams & { format?: DownloadFormat; table?: string }) => {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(p)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
};

export const listReports = () =>
  api<{ reports: ReportDefinition[]; default_from: string; default_to: string }>("/api/v1/reports");

export const runReport = (id: string, p: ReportParams) =>
  api<ReportResult>(`/api/v1/reports/${id}${query(p)}`);

/** Where a report downloads from; the download goes to the audit log. */
export const reportFile = (id: string, p: ReportParams, format: DownloadFormat, table?: string) =>
  `/api/v1/reports/${id}${query({ ...p, format, table })}`;

/** Where a framework's evidence pack downloads from. */
export const frameworkPack = (id: string, p: ReportParams) =>
  `/api/v1/report-frameworks/${encodeURIComponent(id)}/pack${query({ from: p.from, to: p.to })}`;

export const listFrameworks = () =>
  api<{ frameworks: Framework[] }>("/api/v1/report-frameworks").then((r) => r.frameworks);

/** Adds a framework, or changes the one id names. */
export const saveFramework = (id: string | undefined, f: FrameworkInput) =>
  id
    ? send<Framework>("PUT", `/api/v1/report-frameworks/${encodeURIComponent(id)}`, f)
    : send<Framework>("POST", "/api/v1/report-frameworks", f);

/** Removes a framework the tenant added, or brings a built-in one back to its defaults. */
export const deleteFramework = (id: string) =>
  send<void>("DELETE", `/api/v1/report-frameworks/${encodeURIComponent(id)}`);

export const verifyReport = (file: Blob) =>
  api<Verification>("/api/v1/report-verifications", { method: "POST", body: file });
