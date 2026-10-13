// Client for the Grantra core API. The browser holds only the HttpOnly
// session cookie; no tokens are ever handled here.

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
  return (await res.json()) as T;
}

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
}

/** A role or entitlement someone has, and why. */
export interface Assignment {
  id: string;
  type: "role" | "entitlement";
  target_id: string;
  name: string;
  application?: string;
  origin: "birthright" | "manual" | "request";
  rule_id?: string;
  rule_name?: string;
  granted_by: string;
  granted_at: string;
  expires_at?: string;
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
}

export type RequestStatus = "pending" | "approved" | "rejected" | "cancelled" | "expired";

export interface ApprovalStep {
  position: number;
  kind: "manager" | "owner" | "admin";
  approver_id?: string;
  approver_name?: string;
  status: "waiting" | "pending" | "approved" | "rejected" | "skipped";
  due_at?: string;
  decided_by?: string;
  decided_at?: string;
  comment?: string;
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
  duration_days?: number;
  status: RequestStatus;
  reason?: string;
  created_at: string;
  closed_at?: string;
  steps: ApprovalStep[];
}

export interface NewRequest {
  role_id?: string;
  entitlement_id?: string;
  justification: string;
  duration_days?: number;
}

const post = <T>(path: string, body?: unknown) =>
  api<T>(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

/** The fields that name what is asked for. */
export const targetOf = (r: Pick<Requestable, "type" | "id">) =>
  r.type === "role" ? { role_id: r.id } : { entitlement_id: r.id };

export const listRequestable = () =>
  api<{ items: Requestable[] }>("/api/v1/requestable-access").then((r) => r.items);

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
  api<{ requests: AccessRequest[] }>("/api/v1/access-requests?scope=approvals&limit=200").then(
    (r) => r.requests,
  );

export const decideRequest = (id: string, decision: "approve" | "reject", comment: string) =>
  post<AccessRequest>(`/api/v1/access-requests/${encodeURIComponent(id)}/decision`, {
    decision,
    comment,
  });
