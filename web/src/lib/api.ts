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
