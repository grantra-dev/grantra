// Client for an application's authorization: its Cedar policies, the API
// keys its backend asks with, and the policy playground.

import { api, send } from "./api";

export interface APIKey {
  id: string;
  name: string;
  prefix: string;
  created_by: string;
  created_at: string;
  last_used_at?: string;
  revoked_at?: string;
}

export interface AppAuthz {
  policies: string;
  updated_by?: string;
  updated_at?: string;
  keys: APIKey[];
}

export interface Question {
  principal: { id?: string; email?: string; external_id?: string; username?: string };
  action: string;
  resource: { type: string; id: string; attributes?: unknown };
  context?: unknown;
}

export interface Person {
  id: string;
  email: string;
  display_name: string;
  status: string;
  manager_email?: string;
  attributes: Record<string, string>;
  roles: string[];
  entitlements: string[];
}

export interface Answer {
  allowed: boolean;
  policies: string[];
  errors?: string[];
  reason?: string;
}

const base = (appId: string) => `/api/v1/applications/${encodeURIComponent(appId)}/authz`;

export const getAppAuthz = (appId: string) => api<AppAuthz>(base(appId));

export const setAppPolicies = (appId: string, policies: string) =>
  send<{ policies: string }>("PUT", `${base(appId)}/policies`, { policies });

export const createAPIKey = (appId: string, name: string) =>
  send<{ key: APIKey; secret: string }>("POST", `${base(appId)}/keys`, { name });

export const revokeAPIKey = (appId: string, keyId: string) =>
  send<void>("DELETE", `${base(appId)}/keys/${encodeURIComponent(keyId)}`);

/** Asks a question with the stored policies, or with a draft. */
export const testAuthz = (appId: string, q: Question, policies?: string) =>
  send<Answer & { person: Person | null }>("POST", `${base(appId)}/test`, { ...q, policies });
