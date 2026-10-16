// Client for the Grantra Login endpoints: the apps that sign in through it,
// its sign-in policy, federated identity providers and imports.

import { api, send } from "./api";

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
  identity_providers?: IdentityProvider[];
  error?: string;
}

export interface NewSSOApp {
  client_id: string;
  name: string;
  public: boolean;
  redirect_uris: string[];
}

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

export type ProviderKind = "entra" | "okta" | "oidc" | "saml";

export interface IdentityProvider {
  alias: string;
  display_name: string;
  kind: ProviderKind;
  enabled: boolean;
  link_by_email: boolean;
  domains: string[];
  redirect_uri: string;
  saml_metadata?: string;
}

export interface NewIdentityProvider {
  alias: string;
  display_name: string;
  kind: ProviderKind;
  tenant?: string;
  domain?: string;
  issuer?: string;
  client_id?: string;
  client_secret?: string;
  metadata_url?: string;
  email_attribute?: string;
  link_by_email: boolean;
}

export const createIdentityProvider = (p: NewIdentityProvider) =>
  send<IdentityProvider>("POST", "/api/v1/login/identity-providers", p);

export const setProviderDomains = (alias: string, domains: string[]) =>
  send<{ domains: string[] }>(
    "PUT",
    `/api/v1/login/identity-providers/${encodeURIComponent(alias)}/domains`,
    { domains },
  );

export const deleteIdentityProvider = (alias: string) =>
  send<void>("DELETE", `/api/v1/login/identity-providers/${encodeURIComponent(alias)}`);

export interface ImportSet {
  add: string[];
  skip: string[];
}

export interface ImportPlan {
  realm: string;
  users: ImportSet;
  groups: ImportSet;
  roles: ImportSet;
  apps: ImportSet;
  notes: string[];
  applied: boolean;
}

/** Sends a Keycloak realm export; a dry run only says what would change. */
export const importToLogin = (file: Blob, dryRun: boolean) =>
  api<ImportPlan>(`/api/v1/login/import?dry_run=${dryRun}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: file,
  });
