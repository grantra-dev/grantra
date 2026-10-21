// Approval policies (Governance → Approval flows): who approves what people
// ask for, tried in order, with the built-in flow when none matches.
import { api, send, type NewRequest, type OnTimeout, type RequestKind } from "./api";
import type { Risk } from "./admin";

/** Who approves a step of a policy. */
export type Approver = "manager" | "owner" | "people" | "holders" | "admin";

export interface PolicyStep {
  approver: Approver;
  /** For manager: 1 is the manager, 2 their manager, up to 5. */
  level?: number;
  /** For people: who is asked. */
  people_ids?: string[];
  /** For holders: the business role whose holders are asked. */
  role_id?: string;
  /** How many must approve (1 when not set); all asks every one. */
  required?: number;
  all?: boolean;
  /** How long the step waits; the tenant's setting when not set. */
  timeout_hours?: number;
  on_timeout?: OnTimeout;
  label?: string;
  /** The names of people_ids, when reading. */
  people?: { id: string; name: string }[];
}

export interface Conditions {
  kinds?: RequestKind[];
  application_ids?: string[];
  role_ids?: string[];
  risks?: Risk[];
  departments?: string[];
  identity_kinds?: string[];
  longer_than_days?: number;
}

export interface ApprovalPolicy {
  id: string;
  name: string;
  description?: string;
  position: number;
  enabled: boolean;
  conditions: Conditions;
  steps: PolicyStep[];
  version: number;
  updated_by: string;
  updated_at: string;
}

export interface PolicyInput {
  name: string;
  description?: string;
  enabled: boolean;
  conditions: Conditions;
  steps: PolicyStep[];
}

/** A step of a plan: who a request would ask. */
export interface PlannedStep {
  kind: Approver;
  label?: string;
  /** None means any Grantra admin decides. */
  approvers: { id: string; name: string }[];
  required: number;
  timeout_hours?: number;
  on_timeout: OnTimeout;
  /** The requester is one of the approvers: asking counts as their approval. */
  requester_approves?: boolean;
  /** Why the step is not what the policy says. */
  note?: string;
}

/** Who would approve a request, and the policy that says so. */
export interface Plan {
  /** Both empty for the built-in flow; no id for a policy not saved yet. */
  policy_id?: string;
  policy_name?: string;
  policy_version?: number;
  risk: Risk;
  steps: PlannedStep[];
}

export const listApprovalPolicies = () =>
  api<{ policies: ApprovalPolicy[]; builtin: ApprovalPolicy }>("/api/v1/approval-policies");

export const saveApprovalPolicy = (p: PolicyInput, id?: string) =>
  id
    ? send<ApprovalPolicy>("PUT", `/api/v1/approval-policies/${encodeURIComponent(id)}`, p)
    : send<ApprovalPolicy>("POST", "/api/v1/approval-policies", p);

export const deleteApprovalPolicy = (id: string) =>
  api<void>(`/api/v1/approval-policies/${encodeURIComponent(id)}`, { method: "DELETE" });

export const reorderApprovalPolicies = (ids: string[]) =>
  send<{ policies: ApprovalPolicy[] }>("PUT", "/api/v1/approval-policies/order", { ids });

export interface Simulation extends Pick<NewRequest, "role_id" | "entitlement_id"> {
  kind?: RequestKind;
  beneficiary_id: string;
  requester_id?: string;
  duration_days?: number;
  /** A policy being edited, tried before the saved ones. */
  policy?: PolicyInput;
}

export const simulateApprovals = (s: Simulation) =>
  send<{ plan: Plan; sod_conflicts: string[] }>("POST", "/api/v1/approval-policies/simulate", s);

/** How each kind of approver is named. */
export const approverLabels: Record<Approver | "risk", string> = {
  manager: "Manager",
  owner: "Owner",
  people: "Named people",
  holders: "Holders of a role",
  admin: "Grantra admins",
  risk: "Separation of duties risk, Grantra admins",
};

/** "Manager", "Manager's manager", "Payroll lead"... */
export function stepTitle(s: {
  kind?: Approver | "risk";
  approver?: Approver;
  label?: string;
  level?: number;
}) {
  if (s.label) return s.label;
  const kind = s.kind ?? s.approver ?? "admin";
  if (kind === "manager" && s.level && s.level > 1)
    return ["Manager", ...Array<string>(s.level - 1).fill("'s manager")].join("");
  return approverLabels[kind];
}

/** "Ana", "any of Ana, Luis", "2 of Ana, Luis, Paz", "all of Ana, Luis". */
export function whoText(names: string[], required: number) {
  if (names.length === 0) return "any Grantra admin";
  const list = names.join(", ");
  if (names.length === 1) return list;
  if (required >= names.length) return `all of ${list}`;
  return required > 1 ? `${required} of ${list}` : `any of ${list}`;
}

export const timeoutLabels: Record<OnTimeout, string> = {
  expire: "the request expires",
  escalate: "it goes to the approvers' managers",
  reject: "the request is rejected",
};

/** Which approval policy a request or plan follows. */
export function policyText(r: { policy_name?: string; policy_version?: number }) {
  if (!r.policy_name) return "Built-in approval flow: the manager, then the owner";
  return `Approval policy “${r.policy_name}”${r.policy_version ? `, version ${r.policy_version}` : ""}`;
}
