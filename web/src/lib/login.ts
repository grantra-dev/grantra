// Client for the Grantra Login endpoints: the apps that sign in through it
// and its sign-in policy.

import { api } from "./api";

export type MFAPolicy = "optional" | "required" | "sensitive";

export interface LoginPolicy {
  mfa: MFAPolicy;
  passkeys: boolean;
}

export interface SSOApp {
  id: string;
  client_id: string;
  name: string;
  description?: string;
  enabled: boolean;
  public: boolean;
  redirect_uris: string[];
}

export interface LoginStatus {
  connected: boolean;
  application?: { id: string; name: string };
  realm?: { realm: string; display_name: string; login_theme: string; url: string };
  policy?: LoginPolicy;
  apps?: SSOApp[];
  error?: string;
}

export interface NewSSOApp {
  client_id: string;
  name: string;
  public: boolean;
  redirect_uris: string[];
}

const send = <T>(method: string, path: string, body?: unknown) =>
  api<T>(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

export const getLogin = () => api<LoginStatus>("/api/v1/login");

export const createLoginApp = (app: NewSSOApp) =>
  send<{ app: SSOApp; secret: string }>("POST", "/api/v1/login/apps", app);

export const rotateLoginAppSecret = (id: string) =>
  send<{ secret: string }>("POST", `/api/v1/login/apps/${encodeURIComponent(id)}/secret`);

export const setLoginPolicy = (policy: LoginPolicy) =>
  send<{ policy: LoginPolicy; sync?: { added: number; removed: number }; sync_error?: string }>(
    "PUT",
    "/api/v1/login/policy",
    policy,
  );
