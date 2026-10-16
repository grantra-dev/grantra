// Client for just-in-time access: eligibilities, their activations, and
// standing access to privileged entitlements.

import { api, send, type AccessRequest } from "./api";

/** What someone may activate for a few hours when they need it. */
export interface Eligibility {
  id: string;
  identity_id: string;
  type: "role" | "entitlement";
  target_id: string;
  name: string;
  application?: string;
  max_activation_hours: number;
  activation_approval: boolean;
  granted_by: string;
  granted_at: string;
  expires_at?: string;
  request_id?: string;
  /** The activation running now. */
  active?: Activation;
}

export interface Activation {
  assignment_id: string;
  eligibility_id: string;
  justification: string;
  granted_at: string;
  expires_at: string;
}

/** Activating gives the access at once, or opens a request for approval. */
export interface ActivationResult {
  activation?: Activation;
  request?: AccessRequest;
}

/** How an entitlement is held just in time. */
export interface JITSettings {
  jit_only: boolean;
  max_activation_hours: number;
  activation_approval: boolean;
}

/** Standing access to a high or critical risk entitlement. */
export interface Standing {
  assignment_id: string;
  identity_id: string;
  identity_name: string;
  type: "role" | "entitlement";
  target_id: string;
  name: string;
  application?: string;
  risk: "low" | "medium" | "high" | "critical";
  jit_only: boolean;
  origin: string;
  rule_name?: string;
  granted_by: string;
  granted_at: string;
  convertible: boolean;
}

const id = encodeURIComponent;

export const listEligibilities = (identityId: string) =>
  api<{ eligibilities: Eligibility[] }>(`/api/v1/identities/${id(identityId)}/eligibilities`).then(
    (r) => r.eligibilities,
  );

export const makeEligible = (
  identityId: string,
  target: { role_id?: string; entitlement_id?: string },
  durationDays?: number,
) =>
  send<Eligibility>("POST", `/api/v1/identities/${id(identityId)}/eligibilities`, {
    ...target,
    duration_days: durationDays,
  });

export const removeEligibility = (eligibilityId: string) =>
  send<void>("DELETE", `/api/v1/eligibilities/${id(eligibilityId)}`);

export const activate = (eligibilityId: string, hours: number, justification: string) =>
  send<ActivationResult>("POST", `/api/v1/eligibilities/${id(eligibilityId)}/activations`, {
    hours,
    justification,
  });

export const deactivate = (eligibilityId: string) =>
  send<void>("POST", `/api/v1/eligibilities/${id(eligibilityId)}/deactivate`);

export const setEntitlementJIT = (entitlementId: string, s: JITSettings) =>
  send<unknown>("PUT", `/api/v1/entitlements/${id(entitlementId)}/jit`, s);

export const listStandingAccess = () =>
  api<{ standing: Standing[] }>("/api/v1/standing-access?limit=200").then((r) => r.standing);

export const convertToEligibility = (assignmentId: string) =>
  send<Eligibility>("POST", `/api/v1/assignments/${id(assignmentId)}/eligibility`);
