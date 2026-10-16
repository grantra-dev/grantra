// Client for the credential vault: privileged accounts, their check-outs,
// rotation and discovery.

import { api, send } from "./api";

export interface VaultCheckout {
  id: string;
  vault_account_id: string;
  identity_id: string;
  identity_name: string;
  version: number;
  /** Empty for other people's check-outs unless you read the whole vault. */
  reason: string;
  checked_out_at: string;
  expires_at: string;
  ended_at?: string;
  ended_by?: "checked_in" | "expired" | "ended";
  account_name: string;
  /** Opened in an emergency: an admin other than the person reviews it. */
  break_glass: boolean;
  review_status?: "pending" | "justified" | "flagged";
  reviewed_by?: string;
  reviewed_at?: string;
  review_comment?: string;
}

export interface Responder {
  id: string;
  name: string;
}

export interface VaultAccount {
  id: string;
  name: string;
  username: string;
  address?: string;
  notes?: string;
  kind: "password" | "ssh_key";
  account_id?: string;
  application_id?: string;
  application_name?: string;
  entitlement_id: string;
  entitlement_name: string;
  risk: "high" | "critical";
  checkout_minutes: number;
  exclusive: boolean;
  rotate_every_days?: number;
  rotate_after_checkin: boolean;
  can_rotate: boolean;
  rotate_at?: string;
  rotation_failures: number;
  last_rotation_error?: string;
  /** The current secret's version; 0 when none is stored yet. */
  version: number;
  secret_at?: string;
  checkouts: VaultCheckout[];
  /** Responders may open it in an emergency without holding access. */
  break_glass: boolean;
  /** Who may break the glass; the platform admins when empty. */
  break_glass_responders?: Responder[];
  /** Whether you hold access to it, and so can check it out. */
  held: boolean;
  /** Whether you may break the glass on it. */
  responder: boolean;
  created_at: string;
}

export interface VaultSettings {
  address: string;
  notes: string;
  checkout_minutes: number;
  exclusive: boolean;
  rotate_every_days: number;
  rotate_after_checkin: boolean;
  break_glass: boolean;
  break_glass_responders: string[];
}

export interface NewVaultAccount extends VaultSettings {
  name: string;
  username: string;
  kind: "password" | "ssh_key";
  risk: "high" | "critical";
  account_id: string;
  secret: string;
}

export interface VaultVersion {
  version: number;
  status: "pending" | "current" | "retired" | "failed";
  source: "set" | "rotation";
  key_id: string;
  created_by: string;
  created_at: string;
}

export interface VaultAccountDetail extends VaultAccount {
  versions: VaultVersion[];
  recent_checkouts: VaultCheckout[];
}

export interface VaultLease extends VaultCheckout {
  secret: string;
}

export interface Rotation {
  vault_account_id: string;
  version: number;
  rotated: boolean;
  error?: string;
}

export interface VaultFinding {
  account_id: string;
  application_id: string;
  application_name: string;
  username: string;
  native_id: string;
  identity_id?: string;
  identity_name?: string;
  reasons: ("privileged" | "orphan" | "service")[];
  entitlements: string[];
  can_rotate: boolean;
  vault_account_id?: string;
}

const account = (id: string) => `/api/v1/vault/accounts/${encodeURIComponent(id)}`;

export const listVaultAccounts = () =>
  api<{ enabled: boolean; accounts: VaultAccount[] }>("/api/v1/vault/accounts");

export const getVaultAccount = (id: string) => api<VaultAccountDetail>(account(id));

export const createVaultAccount = (a: NewVaultAccount) =>
  send<VaultAccount>("POST", "/api/v1/vault/accounts", a);

export const updateVaultAccount = (id: string, s: VaultSettings) =>
  send<VaultAccount>("PUT", account(id), s);

export const deleteVaultAccount = (id: string) => send<void>("DELETE", account(id));

export const setVaultSecret = (id: string, secret: string) =>
  send<void>("PUT", `${account(id)}/secret`, { secret });

export const rotateVaultAccount = (id: string) =>
  send<Rotation>("POST", `${account(id)}/rotations`);

export const checkoutVaultAccount = (id: string, reason: string) =>
  send<VaultLease>("POST", `${account(id)}/checkouts`, { reason });

export const checkinVault = (checkoutId: string) =>
  send<void>("POST", `/api/v1/vault/checkouts/${encodeURIComponent(checkoutId)}/checkin`);

export const discoverVaultAccounts = () =>
  api<{ findings: VaultFinding[] }>("/api/v1/vault/discovery?limit=200").then((r) => r.findings);

export const breakGlass = (id: string, reason: string) =>
  send<VaultLease>("POST", `${account(id)}/break-glass`, { reason });

export const listBreakGlassUses = (all: boolean) =>
  api<{ uses: VaultCheckout[] }>(
    `/api/v1/vault/break-glass?status=${all ? "all" : "pending"}`,
  ).then((r) => r.uses);

export const reviewBreakGlass = (checkoutId: string, justified: boolean, comment: string) =>
  send<VaultCheckout>(
    "POST",
    `/api/v1/vault/break-glass/${encodeURIComponent(checkoutId)}/review`,
    {
      justified,
      comment,
    },
  );
