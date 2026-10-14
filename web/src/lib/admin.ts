// Client for the administration endpoints: HR sources, the catalog,
// connectors and provisioning, and separation of duties.

import { api, ApiError, type IdentityStatus, type Requestable } from "./api";

type Risk = NonNullable<Requestable["risk"]>;

const send = <T>(method: string, path: string, body?: unknown) =>
  api<T>(path, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });

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
}

export interface IdentitySource {
  id: string;
  name: string;
  kind: "csv";
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

export const createSource = (s: { name: string; config: SourceConfig }) =>
  send<IdentitySource>("POST", "/api/v1/identity-sources", { ...s, kind: "csv" });

export const listSourceRuns = (sourceId: string) =>
  api<{ runs: SourceRun[] }>(`/api/v1/identity-sources/${id(sourceId)}/runs?limit=20`).then(
    (r) => r.runs,
  );

/** Sends an HR export. 422 (errors in the file) and 409 (too many leavers) carry the run. */
export function syncSource(sourceId: string, file: Blob, o: { dryRun: boolean; force?: boolean }) {
  const params = new URLSearchParams({ dry_run: String(o.dryRun) });
  if (o.force) params.set("force", "true");
  return outcome<SyncResult>(
    `/api/v1/identity-sources/${id(sourceId)}/runs?${params}`,
    { method: "POST", headers: { "Content-Type": "text/csv" }, body: file },
    [409, 422],
  );
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
  scim: "SCIM",
  ldap: "LDAP / Active Directory",
  grantra: "Built in",
};

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
