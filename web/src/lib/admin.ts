// Client for the administration endpoints: HR sources, the catalog,
// connectors and provisioning, and separation of duties.

import { useQueries, useQuery } from "@tanstack/react-query";
import { api, ApiError, send, type IdentityStatus, type Requestable } from "./api";
import { connectorTypes } from "./connectors";

export type Risk = NonNullable<Requestable["risk"]>;

const id = encodeURIComponent;

/**
 * Calls an endpoint whose refusals still carry a useful body, such as a sync
 * stopped by a safety limit: those statuses resolve instead of throwing.
 */
async function outcome<T>(path: string, init: RequestInit, alsoOk: number[]): Promise<T> {
  const res = await fetch(path, {
    ...init,
    credentials: "same-origin",
    headers: { Accept: "application/json", ...init.headers },
  });
  const body = (await res.json().catch(() => ({}))) as T & { error?: string };
  if (!res.ok && !alsoOk.includes(res.status)) {
    throw new ApiError(res.status, body.error ?? res.statusText);
  }
  return body;
}

// HR sources

export interface Issue {
  line?: number;
  external_id?: string;
  message: string;
}

export type LifecycleKind = "joiner" | "mover" | "leaver" | "rehire" | "update";

export interface SourceRun {
  id: string;
  source_id: string;
  actor: string;
  dry_run: boolean;
  status: "planned" | "applied" | "invalid" | "refused";
  counts: Partial<Record<LifecycleKind, number>>;
  errors: Issue[];
  warnings: Issue[];
  created_at: string;
}

export interface SourceConfig {
  columns?: Record<string, string>;
  missing?: "leave" | "ignore";
  max_leaver_percent?: number;
  /** How Grantra reaches the HR system, for every kind but csv. */
  connection?: Record<string, unknown>;
  /** How often Grantra reads the HR system on its own. */
  every_hours?: number;
}

/** csv is fed with files; the others are HR systems Grantra reads itself. */
export type SourceKind = "csv" | "workday" | "bamboohr";

export interface IdentitySource {
  id: string;
  name: string;
  kind: SourceKind;
  config: SourceConfig;
  created_at: string;
  last_run?: SourceRun;
}

export interface SourceChange {
  kind: LifecycleKind;
  identity_id?: string;
  external_id: string;
  display_name: string;
  status: IdentityStatus;
  linked?: boolean;
  reason?: string;
  changes?: Record<string, { from: string; to: string }>;
}

export interface SyncResult extends SourceRun {
  changes: SourceChange[];
  unchanged: number;
}

export const listSources = () =>
  api<{ sources: IdentitySource[] }>("/api/v1/identity-sources").then((r) => r.sources);

export const createSource = (s: { name: string; kind: SourceKind; config: SourceConfig }) =>
  send<IdentitySource>("POST", "/api/v1/identity-sources", s);

export const updateSource = (sourceId: string, s: { name: string; config: SourceConfig }) =>
  send<IdentitySource>("PUT", `/api/v1/identity-sources/${id(sourceId)}`, s);

/** The columns Grantra reads from an HR export, in the order people think of them. */
export const hrFields = [
  ["employee_id", "Employee id", "required"],
  ["display_name", "Full name", "or first and last name"],
  ["first_name", "First name", ""],
  ["last_name", "Last name", ""],
  ["email", "Work email", ""],
  ["manager_id", "Manager's employee id", ""],
  ["department", "Department", ""],
  ["title", "Job title", ""],
  ["location", "Location", ""],
  ["status", "Status", "active or terminated"],
  ["start_date", "Start date", "YYYY-MM-DD"],
  ["end_date", "End date", "YYYY-MM-DD"],
] as const satisfies readonly (readonly [string, string, string])[];

export const listSourceRuns = (sourceId: string) =>
  api<{ runs: SourceRun[] }>(`/api/v1/identity-sources/${id(sourceId)}/runs?limit=20`).then(
    (r) => r.runs,
  );

export interface SyncOptions {
  dryRun: boolean;
  force?: boolean;
}

/**
 * Syncs a source: with an HR export for a csv source, or by reading its HR
 * system now for the others. 422 (errors in the file, or the HR system could
 * not be read) and 409 (too many leavers) carry the run.
 */
export function syncSource(sourceId: string, file: Blob | null, o: SyncOptions) {
  const params = new URLSearchParams({ dry_run: String(o.dryRun) });
  if (o.force) params.set("force", "true");
  const path = `/api/v1/identity-sources/${id(sourceId)}/${file ? "runs" : "pulls"}?${params}`;
  const init: RequestInit = file
    ? { method: "POST", headers: { "Content-Type": "text/csv" }, body: file }
    : { method: "POST" };
  return outcome<SyncResult>(path, init, [409, 422]);
}

// Catalog

export interface ProvisioningSettings {
  mode: "off" | "on";
  remove_unassigned?: boolean;
  max_change_percent?: number;
}

export interface CatalogApplication {
  id: string;
  name: string;
  connector: string;
  config: Record<string, unknown> | null;
  provisioning: Partial<ProvisioningSettings> | null;
  owner_id?: string;
  entitlement_count: number;
  created_at: string;
}

export interface Entitlement {
  id: string;
  application_id: string;
  application_name: string;
  native_id: string;
  name: string;
  description?: string;
  risk: Risk;
  /** Nobody holds it for good: people activate it for a few hours. */
  jit_only: boolean;
  max_activation_hours: number;
  activation_approval: boolean;
  created_at: string;
}

export interface BusinessRole {
  id: string;
  name: string;
  description?: string;
  owner_id?: string;
  entitlements: Entitlement[];
  created_at: string;
}

export interface Ref {
  id: string;
  name: string;
  application?: string;
}

export interface AccessRule {
  id: string;
  name: string;
  description?: string;
  conditions: Record<string, string[]>;
  enabled: boolean;
  roles: Ref[];
  entitlements: Ref[];
  holders: number;
  created_at: string;
  updated_at: string;
}

/** Every application, including Grantra's own (its entitlements are the platform roles). */
export const listCatalogApplications = () =>
  api<{ applications: CatalogApplication[] }>("/api/v1/applications").then((r) => r.applications);

export const createApplication = (a: { name: string; owner_id?: string }) =>
  send<CatalogApplication>("POST", "/api/v1/applications", a);

export const listEntitlements = (appId: string) =>
  api<{ entitlements: Entitlement[] }>(`/api/v1/applications/${id(appId)}/entitlements`).then(
    (r) => r.entitlements,
  );

export const createEntitlement = (
  appId: string,
  e: { native_id: string; name: string; description?: string; risk: Risk },
) => send<Entitlement>("POST", `/api/v1/applications/${id(appId)}/entitlements`, e);

export const listRoles = () => api<{ roles: BusinessRole[] }>("/api/v1/roles").then((r) => r.roles);

export const listAccessRules = () =>
  api<{ rules: AccessRule[] }>("/api/v1/access-rules").then((r) => r.rules);

// Connectors and provisioning

/** How each connector kind reads in the console. */
export const connectorLabels: Record<string, string> = {
  manual: "None, managed by hand",
  grantra: "Built in",
  ...Object.fromEntries(connectorTypes.map((t) => [t.kind, t.name])),
};

/** How an application with the csv connector reads its files. */
export interface FileConnectorConfig {
  columns?: Record<string, string>;
  separator?: string;
}

/** The columns Grantra reads from an account file. */
export const accountFields = [
  ["account_id", "Account id", "or the user name"],
  ["username", "User name", ""],
  ["email", "Email", ""],
  ["display_name", "Name", ""],
  ["employee_id", "Employee id", "links the account to its person"],
  ["status", "Status", "active or disabled"],
  ["entitlements", "Entitlements", "roles or groups"],
  ["last_login", "Last sign-in", "YYYY-MM-DD, shows unused access"],
] as const satisfies readonly (readonly [string, string, string])[];

export const setConnector = (appId: string, kind: string, config: object) =>
  send<void>("PUT", `/api/v1/applications/${id(appId)}/connector`, { kind, config });

export interface ConnectorRun {
  id: string;
  application_id: string;
  kind: "aggregation" | "provisioning";
  actor: string;
  status: "succeeded" | "failed" | "planned" | "partial" | "refused" | "running" | "interrupted";
  counts: Record<string, number>;
  error?: string;
  started_at: string;
  finished_at?: string;
}

export interface ProvisioningAction {
  op: "create_account" | "enable_account" | "disable_account" | "add_member" | "remove_member";
  identity_id: string;
  account_id?: string;
  native_account?: string;
  username?: string;
  entitlement_id?: string;
  native_entitlement?: string;
  status: string;
  error?: string;
}

export interface ProvisioningRun extends ConnectorRun {
  actions: ProvisioningAction[];
}

export interface AppAccount {
  id: string;
  native_id: string;
  username?: string;
  status: "active" | "disabled" | "deleted";
  identity_id?: string;
  identity_name?: string;
  correlated_by?: string;
  entitlements: string[];
  last_seen_at?: string;
}

export const listConnectorRuns = (appId: string) =>
  api<{ runs: ConnectorRun[] }>(`/api/v1/applications/${id(appId)}/runs?limit=20`).then(
    (r) => r.runs,
  );

export const accountsPageSize = 100;

export function listAccounts(appId: string, f: { orphans: boolean; after?: string }) {
  const params = new URLSearchParams({ limit: String(accountsPageSize) });
  if (f.orphans) params.set("orphans", "true");
  if (f.after) params.set("after", f.after);
  return api<{ accounts: AppAccount[] }>(
    `/api/v1/applications/${id(appId)}/accounts?${params}`,
  ).then((r) => r.accounts);
}

/** Reads the application now. A failed read (502) still returns its run. */
export const aggregate = (appId: string) =>
  outcome<ConnectorRun>(
    `/api/v1/applications/${id(appId)}/aggregations`,
    { method: "POST" },
    [502],
  );

/** Sends an account file. A refused file (422) still returns its run. */
export const importAccounts = (appId: string, file: Blob, o: { dryRun: boolean }) =>
  outcome<ConnectorRun>(
    `/api/v1/applications/${id(appId)}/account-imports?dry_run=${o.dryRun}`,
    { method: "POST", headers: { "Content-Type": "text/csv" }, body: file },
    [422],
  );

/** Plans (dry run) or writes the changes an application needs. */
export function provision(appId: string, o: { dryRun: boolean; force?: boolean }) {
  const params = new URLSearchParams({ dry_run: String(o.dryRun) });
  if (o.force) params.set("force", "true");
  return outcome<ProvisioningRun>(
    `/api/v1/applications/${id(appId)}/provisioning-runs?${params}`,
    { method: "POST" },
    [409, 502],
  );
}

export const setProvisioning = (appId: string, s: ProvisioningSettings) =>
  send<ProvisioningSettings>("PUT", `/api/v1/applications/${id(appId)}/provisioning`, s);

// Separation of duties

export interface SoDRef {
  role_id?: string;
  entitlement_id?: string;
  name?: string;
}

export interface SoDPolicy {
  id: string;
  name: string;
  description?: string;
  severity: Risk;
  mode: "detect" | "prevent";
  enabled: boolean;
  side_a: SoDRef[];
  side_b: SoDRef[];
  created_at: string;
  updated_at: string;
}

export interface NewSoDPolicy {
  name: string;
  description?: string;
  severity: Risk;
  mode: SoDPolicy["mode"];
  side_a: SoDRef[];
  side_b: SoDRef[];
}

export interface SoDViolation {
  id: string;
  policy_id: string;
  policy_name: string;
  severity: Risk;
  identity_id: string;
  identity_name: string;
  status: "open" | "excepted" | "resolved";
  detected_at: string;
  resolved_at?: string;
  exception_by?: string;
  exception_reason?: string;
  exception_until?: string;
  /** The access request the risk was accepted on. */
  request_id?: string;
}

export const listSoDPolicies = () =>
  api<{ policies: SoDPolicy[] }>("/api/v1/sod-policies").then((r) => r.policies);

export const createSoDPolicy = (p: NewSoDPolicy) =>
  send<SoDPolicy>("POST", "/api/v1/sod-policies", p);

export const deleteSoDPolicy = (policyId: string) =>
  api<void>(`/api/v1/sod-policies/${id(policyId)}`, { method: "DELETE" });

export const listSoDViolations = (status: SoDViolation["status"]) =>
  api<{ violations: SoDViolation[] }>(`/api/v1/sod-violations?status=${status}&limit=200`).then(
    (r) => r.violations,
  );

export const scanSoD = () =>
  send<{ detected: number; resolved: number; exceptions_expired: number; open: number }>(
    "POST",
    "/api/v1/sod-violations/scan",
  );

export const exceptSoDViolation = (violationId: string, reason: string, days: number) =>
  send<SoDViolation>("POST", `/api/v1/sod-violations/${id(violationId)}/exception`, {
    reason,
    days,
  });

// Service tokens

export const tokenScopes = {
  "identity_source.sync": "Feed an HR source",
  "application.import_accounts": "Load an application's account file",
  "sign_in_event.send": "Send sign-ins from another login provider",
} as const;

export type TokenScope = keyof typeof tokenScopes;

/** Scopes that always cover the whole tenant: there is nothing to pin them to. */
export const unpinnedScopes: readonly TokenScope[] = ["sign_in_event.send"];

export interface ServiceToken {
  id: string;
  name: string;
  prefix: string;
  /** An action, alone or pinned to one source or application: "identity_source.sync:<id>". */
  scopes: string[];
  created_by: string;
  created_at: string;
  expires_at: string;
  last_used_at?: string;
  revoked_at?: string;
  creator_name?: string;
  /** The person who created it has left the company; the token still works. */
  creator_left?: boolean;
}

export const listServiceTokens = () =>
  api<{ tokens: ServiceToken[] }>("/api/v1/service-tokens").then((r) => r.tokens);

export const createServiceToken = (t: {
  name: string;
  scopes: string[];
  expires_in_days: number;
}) => send<{ token: ServiceToken; secret: string }>("POST", "/api/v1/service-tokens", t);

export const revokeServiceToken = (tokenId: string) =>
  send<void>("DELETE", `/api/v1/service-tokens/${id(tokenId)}`);

/** Every role and entitlement, to pick from: separation of duties sides, approval flows. */
export function useAccessOptions() {
  const apps = useQuery({ queryKey: ["catalog-applications"], queryFn: listCatalogApplications });
  const roles = useQuery({ queryKey: ["roles"], queryFn: listRoles });
  const ents = useQueries({
    queries: (apps.data ?? [])
      .filter((a) => a.connector !== "grantra")
      .map((a) => ({
        queryKey: ["entitlements", a.id],
        queryFn: () => listEntitlements(a.id),
      })),
  });
  return {
    roles: roles.data ?? [],
    entitlements: ents.flatMap((q) => q.data ?? []),
  };
}

/** The role or entitlement an AccessOptions value names. */
export function refOf(value: string): { role_id?: string; entitlement_id?: string } {
  const [kind, id] = value.split(":");
  return kind === "role" ? { role_id: id } : { entitlement_id: id };
}
