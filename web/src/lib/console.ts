// What the console gathers from every module for the person signed in:
// their inbox, their home page, the search box and the modules the tenant
// turned off.
import { api, send } from "./api";

/** A request waiting on your decision. */
export interface WaitingRequest {
  id: string;
  beneficiary_name: string;
  target_name: string;
  application?: string;
  kind: "access" | "eligibility" | "activation";
  created_at: string;
  due_at?: string;
}

/** A campaign where you still have access to review. */
export interface CampaignToReview {
  campaign_id: string;
  campaign_name: string;
  due_at: string;
  pending: number;
}

/** A break-glass use waiting for an admin's review. */
export interface BreakGlassTask {
  id: string;
  account: string;
  person: string;
  reason: string;
  checked_out_at: string;
}

/** A serious threat alert for an admin to look at; never one about themselves. */
export interface ThreatTask {
  id: string;
  title: string;
  summary: string;
  severity: "high" | "critical";
  last_seen_at: string;
}

/** Everything waiting on you, from GET /api/v1/me/inbox. */
export interface Inbox {
  approvals: WaitingRequest[];
  reviews: CampaignToReview[];
  break_glass: BreakGlassTask[];
  threats: ThreatTask[];
  total: number;
}

export const getInbox = () => api<Inbox>("/api/v1/me/inbox");

/** How many reviews wait on you, across campaigns. */
export const pendingReviews = (inbox: Inbox) => inbox.reviews.reduce((n, r) => n + r.pending, 0);

export interface FailedRun {
  kind: "aggregation" | "provisioning" | "hr";
  id: string;
  name: string;
  error?: string;
  at: string;
}

export interface Home {
  expiring_access: { name: string; application?: string; expires_at: string }[];
  pending_requests: number;
  /** For admins and auditors. */
  governance?: {
    failed_runs: FailedRun[];
    /** Per application, most first. */
    orphans: { application_id: string; application: string; count: number }[];
    undescribed: number;
    open_violations?: number;
  };
  /** For whoever watches for threats, while threat detection is on. */
  security?: { open_alerts: Partial<Record<"low" | "medium" | "high" | "critical", number>> };
}

export const getHome = () => api<Home>("/api/v1/me/home");

export type SearchKind =
  "person" | "role" | "entitlement" | "application" | "vault_account" | "server" | "remote_target";

export interface SearchHit {
  kind: SearchKind;
  id: string;
  title: string;
  detail?: string;
  application_id?: string;
}

export const search = (q: string) =>
  api<{ results: SearchHit[] }>(`/api/v1/search?q=${encodeURIComponent(q)}`).then((r) => r.results);

/** A part of Grantra a tenant can turn off. */
export interface ConsoleModule {
  id: "pam" | "login" | "itdr";
  title: string;
  description: string;
  enabled: boolean;
}

export const listModules = () =>
  api<{ modules: ConsoleModule[] }>("/api/v1/modules").then((r) => r.modules);

export const setModule = (id: string, enabled: boolean) =>
  send<ConsoleModule>("PUT", `/api/v1/modules/${encodeURIComponent(id)}`, { enabled });
