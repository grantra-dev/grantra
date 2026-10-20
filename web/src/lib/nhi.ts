// Non-human identities: service accounts, AI agents and workloads, each with
// an owner who answers for it.
import { api, send, type IdentityKind, type IdentityStatus } from "./api";

export type NonHumanKind = Exclude<IdentityKind, "human">;

export const identityKindLabels: Record<IdentityKind, string> = {
  human: "Person",
  service: "Service account",
  agent: "AI agent",
  workload: "Workload",
};

export const nonHumanKinds: NonHumanKind[] = ["service", "agent", "workload"];

export type Flag = "no_owner" | "unvaulted" | "unused" | "expiring";

/** What each flag means, as a short label and the reason behind it. */
export const flagInfo: Record<Flag, { label: string; why: string }> = {
  no_owner: { label: "No owner", why: "Nobody active answers for it." },
  unvaulted: {
    label: "Secret outside the vault",
    why: "An account with high or critical risk access keeps its password outside the vault.",
  },
  unused: { label: "Unused", why: "No sign-in in 90 days." },
  expiring: { label: "Ends soon", why: "Its end date is within 30 days." },
};

export const flags = Object.keys(flagInfo) as Flag[];

export interface NonHuman {
  id: string;
  kind: NonHumanKind;
  name: string;
  purpose?: string;
  spiffe_id?: string;
  status: IdentityStatus;
  owner_id?: string;
  owner_name?: string;
  owner_status?: string;
  end_date?: string;
  created_at: string;
  accounts: number;
  vaulted: number;
  risky_held: number;
  last_used_at?: string;
  flags: Flag[];
}

export interface NonHumanFilter {
  kind?: NonHumanKind;
  flag?: Flag;
  q?: string;
}

export type Counts = Record<"total" | Flag, number>;

export const nonHumanPageSize = 50;

export function listNonHuman(f: NonHumanFilter, after?: string) {
  const q = new URLSearchParams({ limit: String(nonHumanPageSize) });
  if (f.kind) q.set("kind", f.kind);
  if (f.flag) q.set("flag", f.flag);
  if (f.q) q.set("q", f.q);
  if (after) q.set("after", after);
  return api<{ identities: NonHuman[] }>(`/api/v1/non-human-identities?${q}`).then(
    (r) => r.identities,
  );
}

export const getNonHumanSummary = () => api<Counts>("/api/v1/non-human-identities/summary");

export const getNonHuman = (id: string) =>
  api<NonHuman>(`/api/v1/non-human-identities/${encodeURIComponent(id)}`);

/** Fields to set; on a change, the ones left out stay as they are and "" clears one. */
export interface NonHumanInput {
  kind?: NonHumanKind;
  name?: string;
  purpose?: string;
  owner_id?: string;
  spiffe_id?: string;
  end_date?: string;
  account_ids?: string[];
}

export const createNonHuman = (input: NonHumanInput) =>
  send<NonHuman>("POST", "/api/v1/non-human-identities", input);

export const updateNonHuman = (id: string, input: NonHumanInput) =>
  send<NonHuman>("PATCH", `/api/v1/non-human-identities/${encodeURIComponent(id)}`, input);

export const retireNonHuman = (id: string, reason: string) =>
  send<NonHuman>("POST", `/api/v1/non-human-identities/${encodeURIComponent(id)}/retire`, {
    reason,
  });

/** An active account nobody owns, which may be a program's. */
export interface Candidate {
  account_id: string;
  username: string;
  application_id: string;
  application: string;
  entitlements: number;
  risky: boolean;
  vaulted: boolean;
  last_login_at?: string;
  looks_like_service: boolean;
}

export const listCandidates = () =>
  api<{ accounts: Candidate[]; limit: number }>("/api/v1/non-human-identities/candidates");
