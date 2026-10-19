// Attack paths (who reaches the riskiest access, and how) and access
// insights (unused access, suggestions for reviewers, roles to create).
import { api, send } from "./api";
import type { Risk } from "./admin";

export type StepKind =
  "person" | "rule" | "role" | "entitlement" | "account" | "vault_account" | "gateway_target";

export type Edge =
  | "assigned"
  | "requested"
  | "activated"
  | "eligible"
  | "matches"
  | "gives"
  | "includes"
  | "owns"
  | "holds"
  | "logs_in_as";

/** How a step leads to the next one, as a sentence reads it. */
export const edgeLabels: Record<Edge, string> = {
  assigned: "was given",
  requested: "requested",
  activated: "turned on",
  eligible: "may turn on",
  matches: "matches the rule",
  gives: "which gives",
  includes: "which includes",
  owns: "owns the account",
  holds: "which holds",
  logs_in_as: "which logs in as",
};

export const stepLabels: Record<StepKind, string> = {
  person: "Person",
  rule: "Birthright rule",
  role: "Role",
  entitlement: "Entitlement",
  account: "Account",
  vault_account: "Vault account",
  gateway_target: "Gateway target",
};

export interface Step {
  kind: StepKind;
  id?: string;
  name: string;
  detail?: string;
  edge?: Edge;
}

export interface AttackPath {
  identity_id: string;
  identity_name: string;
  standing: boolean;
  outside: boolean;
  credential: boolean;
  until?: string;
  steps: Step[];
}

export interface AttackTarget {
  id: string;
  name: string;
  application: string;
  risk: Risk;
  jit_only: boolean;
  people: number;
  standing: number;
  credential: number;
  outside: number;
  orphans: number;
}

export interface Orphan {
  account_id: string;
  account: string;
  application: string;
  owner?: string;
  owner_status?: string;
}

export interface AttackDetail extends AttackTarget {
  paths: AttackPath[];
  /** More paths than the API lists. */
  truncated: boolean;
  orphan_accounts: Orphan[];
}

export interface Reach extends AttackTarget {
  paths: AttackPath[];
}

export const listAttackTargets = () =>
  api<{ targets: AttackTarget[] }>("/api/v1/attack-paths").then((r) => r.targets);

export const getAttackPaths = (id: string) =>
  api<AttackDetail>(`/api/v1/attack-paths/${encodeURIComponent(id)}`);

export const getReach = (identityId: string) =>
  api<{ targets: Reach[] }>(`/api/v1/identities/${encodeURIComponent(identityId)}/reach`).then(
    (r) => r.targets,
  );

// Insights

export interface Use {
  last_used_at?: string;
  tracked: boolean;
}

export type Signal = "unused" | "used" | "common" | "rare";

export interface Suggestion extends Use {
  action?: "keep" | "revoke";
  signals: Signal[];
  peer_group?: "department" | "team";
  department?: string;
  peers: number;
  peer_holders: number;
}

export interface UnusedAccess {
  id: string;
  eligible: boolean;
  identity_id: string;
  identity_name: string;
  type: "role" | "entitlement";
  target_id: string;
  name: string;
  application?: string;
  risk: Risk;
  origin: "manual" | "request" | "eligibility";
  granted_at: string;
  expires_at?: string;
  last_used_at?: string;
}

export const unusedPageSize = 50;

export const listUnusedAccess = (after?: string) => {
  const q = new URLSearchParams({ limit: String(unusedPageSize) });
  if (after) q.set("after", after);
  return api<{ access: UnusedAccess[]; days: number }>(`/api/v1/unused-access?${q}`).then(
    (r) => r.access,
  );
};

export interface SuggestedEntitlement {
  id: string;
  name: string;
  application: string;
  risk: Risk;
  holders: number;
}

export interface RoleSuggestion {
  name: string;
  conditions: Record<string, string[]>;
  people: number;
  have_all: number;
  entitlements: SuggestedEntitlement[];
}

export const listRoleSuggestions = () =>
  api<{ suggestions: RoleSuggestion[] }>("/api/v1/role-suggestions").then((r) => r.suggestions);

/** Creates the suggested role, and with rule a birthright rule giving it to the whole group. */
export async function createSuggestedRole(
  s: RoleSuggestion,
  name: string,
  entitlementIds: string[],
  rule: boolean,
) {
  const role = await send<{ id: string; name: string }>("POST", "/api/v1/roles", {
    name,
    description: `Suggested by Grantra from what ${s.name} hold.`,
    entitlement_ids: entitlementIds,
  });
  if (rule) {
    await send("POST", "/api/v1/access-rules", {
      name,
      description: `Gives the ${name} role to everyone in ${s.name}.`,
      conditions: s.conditions,
      role_ids: [role.id],
    });
  }
  return role;
}

/** Who a suggestion compared someone with, as a sentence ends it. */
export function peerGroupText(s: Suggestion): string {
  return s.peer_group === "department" && s.department
    ? `in ${s.department}`
    : "on their manager's team";
}
