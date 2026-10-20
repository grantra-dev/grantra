// Client for the Grantra core API. The browser holds only the HttpOnly
// session cookie; no tokens are ever handled here.
import type { Suggestion } from "./insights";

export class ApiError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

export async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: { Accept: "application/json", ...init?.headers },
  });
  if (!res.ok) {
    let message = res.statusText;
    try {
      const body = (await res.json()) as { error?: string };
      if (body.error) message = body.error;
    } catch {
      // Not JSON, e.g. the plain "not signed in" answer.
    }
    throw new ApiError(res.status, message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

/** Calls an endpoint with a JSON body, when there is one. */
export const send = <T>(method: string, path: string, body?: unknown) =>
  api<T>(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

export const PlatformRole = {
  Admin: "grantra-admin",
  Helpdesk: "grantra-helpdesk",
  Auditor: "grantra-auditor",
} as const;

/** The signed-in person, from GET /api/v1/me. */
export interface Me {
  identity_id: string;
  display_name: string;
  email?: string;
  platform_roles: string[];
  manages_people: boolean;
  owns_applications: boolean;
  /** Parts of Grantra the tenant turned off; their pages are hidden. */
  disabled_modules?: string[];
}

/** A role or entitlement someone has, and why. */
export interface Assignment {
  id: string;
  type: "role" | "entitlement";
  target_id: string;
  name: string;
  application?: string;
  origin: "birthright" | "manual" | "request" | "activation";
  rule_id?: string;
  rule_name?: string;
  granted_by: string;
  granted_at: string;
  expires_at?: string;
  /** Why an activation was made. */
  justification?: string;
}

export const getMe = () => api<Me>("/api/v1/me");

export const getIdentityAccess = (identityId: string) =>
  api<{ assignments: Assignment[] }>(
    `/api/v1/identities/${encodeURIComponent(identityId)}/access`,
  ).then((r) => r.assignments);

/** Something people can ask for, from GET /api/v1/requestable-access. */
export interface Requestable {
  type: "role" | "entitlement";
  id: string;
  name: string;
  description?: string;
  application?: string;
  risk?: "low" | "medium" | "high" | "critical";
  /** Nobody holds it for good: ask for it for a time, or to be eligible. */
  jit_only: boolean;
  /** How many people on your team (who share your manager) hold it now. */
  team_holders?: number;
}

/** What a request asks for: the access, to be eligible, or to activate. */
export type RequestKind = "access" | "eligibility" | "activation";

export type RequestStatus = "pending" | "approved" | "rejected" | "cancelled" | "expired";

export interface ApprovalStep {
  position: number;
  kind: "manager" | "owner" | "admin" | "risk";
  approver_id?: string;
  approver_name?: string;
  status: "waiting" | "pending" | "approved" | "rejected" | "skipped";
  due_at?: string;
  decided_by?: string;
  decided_at?: string;
  comment?: string;
  /** How long an accepted separation of duties risk lasts (risk steps). */
  exception_days?: number;
}

export interface AccessRequest {
  id: string;
  requester_id: string;
  requester_name: string;
  beneficiary_id: string;
  beneficiary_name: string;
  type: "role" | "entitlement";
  target_id: string;
  target_name: string;
  application?: string;
  justification: string;
  kind: RequestKind;
  duration_days?: number;
  duration_hours?: number;
  status: RequestStatus;
  reason?: string;
  created_at: string;
  closed_at?: string;
  steps: ApprovalStep[];
  /** The policies the access breaks, while an admin has to accept the risk. */
  sod_conflicts?: string[];
}

export interface NewRequest {
  role_id?: string;
  entitlement_id?: string;
  kind?: "access" | "eligibility";
  justification: string;
  duration_days?: number;
  duration_hours?: number;
}

/** "for 3 hours", "for 30 days" or "" for a request's duration. */
export const durationText = (r: Pick<AccessRequest, "duration_days" | "duration_hours">) =>
  r.duration_hours
    ? `for ${r.duration_hours} ${r.duration_hours === 1 ? "hour" : "hours"}`
    : r.duration_days
      ? `for ${r.duration_days} ${r.duration_days === 1 ? "day" : "days"}`
      : "";

/** "Eligibility" or "Activation" for requests that are not for the access itself. */
export const kindText = (r: Pick<AccessRequest, "kind">) =>
  r.kind === "eligibility" ? "Eligibility" : r.kind === "activation" ? "Activation" : "";

const post = <T>(path: string, body?: unknown) => send<T>("POST", path, body);

/** The fields that name what is asked for. */
export const targetOf = (r: Pick<Requestable, "type" | "id">) =>
  r.type === "role" ? { role_id: r.id } : { entitlement_id: r.id };

export const listRequestable = () =>
  api<{ items: Requestable[] }>("/api/v1/requestable-access").then((r) => r.items);

/** The catalog, with how many people are on your team to compare team_holders with. */
export const listCatalog = () =>
  api<{ items: Requestable[]; team_size: number }>("/api/v1/requestable-access");

/**
 * Every page of a list the API pages by id with ?after=: lists of things
 * waiting on someone can outgrow one page, and a task left off the page
 * is a task nobody sees.
 */
export async function fetchAllPages<T extends { id: string }>(
  path: string,
  pick: (body: never) => T[],
  limit = 200,
): Promise<T[]> {
  const out: T[] = [];
  for (let after = ""; ;) {
    const sep = path.includes("?") ? "&" : "?";
    const page = pick(
      await api<never>(
        `${path}${sep}limit=${limit}${after && `&after=${encodeURIComponent(after)}`}`,
      ),
    );
    out.push(...page);
    if (page.length < limit) return out;
    after = page[page.length - 1]!.id;
  }
}

export const previewRequest = (target: Pick<NewRequest, "role_id" | "entitlement_id">) =>
  post<{ sod_conflicts: string[] }>("/api/v1/access-requests/preview", target).then(
    (r) => r.sod_conflicts,
  );

export const createRequest = (req: NewRequest) =>
  post<AccessRequest>("/api/v1/access-requests", req);

export const listMyRequests = () =>
  api<{ requests: AccessRequest[] }>("/api/v1/access-requests?scope=mine&limit=200").then(
    (r) => r.requests,
  );

export const cancelRequest = (id: string) =>
  post<AccessRequest>(`/api/v1/access-requests/${encodeURIComponent(id)}/cancel`);

export const listApprovals = () =>
  fetchAllPages(
    "/api/v1/access-requests?scope=approvals",
    (r: { requests: AccessRequest[] }) => r.requests,
  );

export const decideRequest = (
  id: string,
  decision: "approve" | "reject",
  comment: string,
  exceptionDays?: number,
) =>
  post<AccessRequest>(`/api/v1/access-requests/${encodeURIComponent(id)}/decision`, {
    decision,
    comment,
    exception_days: exceptionDays,
  });

export type ReviewDecision = "pending" | "keep" | "revoke";

/** One access to review in a campaign. */
export interface ReviewItem {
  id: string;
  campaign_id: string;
  campaign_name: string;
  identity_id: string;
  identity_name: string;
  type: "role" | "entitlement";
  target_id: string;
  target_name: string;
  application?: string;
  risk?: Requestable["risk"];
  /** "eligibility": the person may activate it just in time, rather than hold it now. */
  origin: Assignment["origin"] | "eligibility";
  granted_at: string;
  reviewer_id?: string;
  reviewer_name?: string;
  decision: ReviewDecision;
  decided_by?: string;
  decided_at?: string;
  comment?: string;
  /** What Grantra makes of the access, on pending items for their reviewer. */
  suggestion?: Suggestion;
}

export interface Campaign {
  id: string;
  name: string;
  reviewer: "manager" | "owner";
  application_ids: string[];
  on_missing: "keep" | "revoke";
  due_at: string;
  status: "active" | "completed";
  created_by: string;
  created_at: string;
  closed_at?: string;
  /** Items by decision. */
  progress: Partial<Record<ReviewDecision, number>>;
}

export interface NewCampaign {
  name: string;
  reviewer: Campaign["reviewer"];
  application_ids: string[];
  on_missing: Campaign["on_missing"];
  due_in_days: number;
}

export interface Application {
  id: string;
  name: string;
  connector: string;
}

export const listMyReviewItems = () =>
  fetchAllPages(
    "/api/v1/review-items?scope=mine&decision=pending",
    (r: { items: ReviewItem[] }) => r.items,
  );

export const decideReviewItem = (id: string, decision: "keep" | "revoke", comment: string) =>
  post<ReviewItem>(`/api/v1/review-items/${encodeURIComponent(id)}/decision`, {
    decision,
    comment,
  });

export const listCampaigns = () =>
  api<{ campaigns: Campaign[] }>("/api/v1/review-campaigns").then((r) => r.campaigns);

export const launchCampaign = (c: NewCampaign) => post<Campaign>("/api/v1/review-campaigns", c);

export const closeCampaign = (id: string) =>
  post<Campaign>(`/api/v1/review-campaigns/${encodeURIComponent(id)}/close`);

/** Applications in the catalog, without Grantra's own platform roles app. */
export const listApplications = () =>
  api<{ applications: Application[] }>("/api/v1/applications").then((r) =>
    r.applications.filter((a) => a.connector !== "grantra"),
  );

export type IdentityStatus = "pending" | "active" | "suspended" | "terminated";

export type IdentityKind = "human" | "service" | "agent" | "workload";

/** A person or other identity, from GET /api/v1/identities. */
export interface Identity {
  id: string;
  display_name: string;
  email?: string;
  kind: IdentityKind;
  status: IdentityStatus;
  manager_id?: string;
  /** The HR source that owns the identity, and its employee id there. */
  source_id?: string;
  external_id?: string;
  start_date?: string;
  end_date?: string;
  /** For service accounts, agents and workloads, whose manager is their owner. */
  purpose?: string;
  spiffe_id?: string;
  attributes: Record<string, unknown>;
  created_at: string;
}

export const identitiesPageSize = 50;

export function listIdentities(f: {
  q?: string;
  status?: string;
  kind?: "human" | "non_human";
  after?: string;
}) {
  const params = new URLSearchParams({ limit: String(identitiesPageSize) });
  if (f.q) params.set("q", f.q);
  if (f.status) params.set("status", f.status);
  if (f.kind) params.set("kind", f.kind);
  if (f.after) params.set("after", f.after);
  return api<{ identities: Identity[] }>(`/api/v1/identities?${params}`).then((r) => r.identities);
}

export const getIdentity = (id: string) =>
  api<Identity>(`/api/v1/identities/${encodeURIComponent(id)}`);

/** An account someone has in an application. */
export interface Account {
  id: string;
  application_id: string;
  application_name: string;
  native_id: string;
  username?: string;
  status: "active" | "disabled" | "deleted";
  identity_id?: string;
  correlated_by?: string;
  entitlements: string[];
  last_seen_at?: string;
}

export const getIdentityAccounts = (id: string) =>
  api<{ accounts: Account[] }>(`/api/v1/identities/${encodeURIComponent(id)}/accounts`).then(
    (r) => r.accounts,
  );

/** A joiner, mover, leaver or rehire, from an HR source sync. */
export interface LifecycleEvent {
  id: number;
  identity_id: string;
  display_name: string;
  kind: "joiner" | "mover" | "leaver" | "rehire";
  run_id?: string;
  details: {
    external_id?: string;
    status?: string;
    reason?: string;
    changes?: Record<string, { from: string; to: string }>;
  };
  effective_at: string;
  created_at: string;
}

export const listLifecycleEvents = (identityId: string) =>
  api<{ events: LifecycleEvent[] }>(
    `/api/v1/lifecycle-events?limit=200&identity=${encodeURIComponent(identityId)}`,
  ).then((r) => r.events);

export interface AuditEvent {
  id: number;
  occurred_at: string;
  /** Who did it, like "identity:<id>" or "system:scheduler". */
  actor: string;
  actor_name?: string;
  action: string;
  target_type?: string;
  target_id?: string;
  target_name?: string;
  details: Record<string, unknown>;
}

export const auditPageSize = 100;

export function listAuditEvents(f: { before?: number; identity?: string }) {
  const params = new URLSearchParams({ limit: String(auditPageSize) });
  if (f.before) params.set("before", String(f.before));
  if (f.identity) params.set("identity", f.identity);
  return api<{ events: AuditEvent[] }>(`/api/v1/audit-events?${params}`).then((r) => r.events);
}

export interface RoleMember {
  role: string;
  identity_id: string;
  display_name: string;
  email?: string;
  status: IdentityStatus;
  granted_at: string;
  expires_at?: string;
}

/** A platform role (admin, helpdesk, auditor) and who holds it. */
export interface PlatformRoleInfo {
  name: string;
  display_name: string;
  description: string;
  risk: NonNullable<Requestable["risk"]>;
  members: RoleMember[];
}

export const listPlatformRoles = () =>
  api<{ roles: PlatformRoleInfo[] }>("/api/v1/platform-roles").then((r) => r.roles);

const memberPath = (role: string, identityId: string) =>
  `/api/v1/platform-roles/${encodeURIComponent(role)}/members/${encodeURIComponent(identityId)}`;

export const grantPlatformRole = (role: string, identityId: string, expiresAt?: string) =>
  send<void>("PUT", memberPath(role, identityId), expiresAt ? { expires_at: expiresAt } : {});

export const revokePlatformRole = (role: string, identityId: string) =>
  api<void>(memberPath(role, identityId), { method: "DELETE" });

/** One kind of email and whether the signed-in person gets it. */
export interface NotificationSetting {
  kind: string;
  title: string;
  description: string;
  enabled: boolean;
  /** Required emails cannot be turned off. */
  required?: boolean;
}

/** From GET /api/v1/me/notifications. */
export interface NotificationSettings {
  /** Whether this Grantra sends email at all. */
  email_enabled: boolean;
  /** Whether the signed-in person has an email address. */
  has_email: boolean;
  kinds: NotificationSetting[];
}

export const getNotificationSettings = () => api<NotificationSettings>("/api/v1/me/notifications");

export const setNotification = (kind: string, enabled: boolean) =>
  send<NotificationSettings>("PUT", `/api/v1/me/notifications/${encodeURIComponent(kind)}`, {
    enabled,
  });
