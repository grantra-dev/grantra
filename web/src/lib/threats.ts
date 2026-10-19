// Identity threat detection: alerts, sign-ins, the detections behind them,
// and what admins do about them.
import { api, send } from "./api";
import { formatTime } from "./utils";

export type Severity = "low" | "medium" | "high" | "critical";
export type AlertStatus = "open" | "resolved" | "dismissed";
export type ResponseAction = "revoke_sessions" | "require_mfa" | "suspend" | "reinstate";

export const severities: Severity[] = ["critical", "high", "medium", "low"];

export const responseLabels: Record<ResponseAction, string> = {
  revoke_sessions: "End sessions",
  require_mfa: "Require a second factor",
  suspend: "Suspend",
  reinstate: "Reinstate",
};

export interface ThreatResponse {
  id: string;
  identity_id: string;
  alert_id?: string;
  action: ResponseAction;
  actor: string;
  reason?: string;
  details: Record<string, unknown>;
  error?: string;
  created_at: string;
}

export interface ThreatAlert {
  id: string;
  detection: string;
  title: string;
  severity: Severity;
  status: AlertStatus;
  /** Empty for an alert about an address, such as a password spray. */
  identity_id?: string;
  identity_name?: string;
  identity_status?: string;
  subject: string;
  summary: string;
  evidence: Record<string, unknown>;
  occurrences: number;
  first_seen_at: string;
  last_seen_at: string;
  created_at: string;
  closed_by?: string;
  closed_at?: string;
  close_note?: string;
  /** Only on a single alert. */
  responses?: ThreatResponse[];
}

/** Open alerts per severity. */
export type Counts = Partial<Record<Severity, number>>;

export interface SignInSource {
  source: string;
  last_at?: string;
  last_day: number;
  identified: number;
}

export interface ThreatsOverview {
  open: Counts;
  sources: SignInSource[];
  grantra_login: boolean;
  login_event_types: string[];
  token_scope: string;
}

export interface SignInEvent {
  id: number;
  source: string;
  occurred_at: string;
  identity_id?: string;
  identity_name?: string;
  username?: string;
  outcome: "success" | "failure" | "mfa_failure";
  ip?: string;
  country?: string;
  application?: string;
  user_agent?: string;
}

export interface DetectionParam {
  name: string;
  description: string;
  default: number;
  min: number;
  max: number;
}

export interface Detection {
  id: string;
  title: string;
  description: string;
  severity: Severity;
  params: DetectionParam[] | null;
  /** The responses Grantra may take on its own when this detection fires. */
  auto_responses: ResponseAction[] | null;
  enabled: boolean;
  auto_response: ResponseAction | "none";
  values: Record<string, number> | null;
  updated_by?: string;
  updated_at?: string;
}

export interface ScanResult {
  read: number;
  evaluated: number;
  raised: number;
  escalated: number;
  responses: number;
  response_errors: number;
  deleted: number;
}

const query = (params: Record<string, string | undefined>) => {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v) q.set(k, v);
  const s = q.toString();
  return s ? `?${s}` : "";
};

export const getThreats = () => api<ThreatsOverview>("/api/v1/threats");

export const listThreatAlerts = (f: {
  status?: string;
  severity?: string;
  identity?: string;
  before?: string;
}) => api<{ alerts: ThreatAlert[] }>(`/api/v1/threats/alerts${query(f)}`).then((r) => r.alerts);

export const getThreatAlert = (id: string) =>
  api<ThreatAlert>(`/api/v1/threats/alerts/${encodeURIComponent(id)}`);

export const closeThreatAlert = (id: string, status: "resolved" | "dismissed", note: string) =>
  send<ThreatAlert>("POST", `/api/v1/threats/alerts/${encodeURIComponent(id)}/close`, {
    status,
    note,
  });

export const listDetections = () =>
  api<{ detections: Detection[] }>("/api/v1/threats/detections").then((r) => r.detections);

export const saveDetection = (
  id: string,
  d: Pick<Detection, "enabled" | "severity" | "auto_response"> & { values: Record<string, number> },
) => send<Detection>("PUT", `/api/v1/threats/detections/${encodeURIComponent(id)}`, d);

export const scanThreats = () =>
  send<{ result: ScanResult; error?: string }>("POST", "/api/v1/threats/scan");

export const listSignIns = (f: {
  identity?: string;
  outcome?: string;
  ip?: string;
  before?: string;
}) => api<{ events: SignInEvent[] }>(`/api/v1/sign-in-events${query(f)}`).then((r) => r.events);

export const respondToThreat = (
  identityId: string,
  r: { action: ResponseAction; alert_id?: string; reason?: string; days?: number },
) =>
  send<ThreatResponse>(
    "POST",
    `/api/v1/identities/${encodeURIComponent(identityId)}/threat-responses`,
    r,
  );

export const listThreatResponses = (identityId: string) =>
  api<{ responses: ThreatResponse[] }>(
    `/api/v1/identities/${encodeURIComponent(identityId)}/threat-responses`,
  ).then((r) => r.responses);

export const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** Turns an evidence key such as "previous_country" into "Previous country". */
export const evidenceLabel = (key: string) => capitalize(key.replaceAll("_", " "));

/** Shows one evidence value: dates as dates, lists joined. */
export function evidenceValue(value: unknown): string {
  if (value === null || value === undefined || value === "") return "–";
  if (Array.isArray(value)) return value.map(evidenceValue).join(", ");
  if (typeof value === "string" && /^\d{4}-\d{2}-\d{2}T/.test(value)) {
    return formatTime(value);
  }
  if (typeof value === "object") return JSON.stringify(value);
  return String(value);
}
