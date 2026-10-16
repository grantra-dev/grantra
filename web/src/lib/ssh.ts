// Client for SSH access: targets, the certificates Grantra signs for them,
// and the sessions the gateway records.

import { api, send } from "./api";
import type { Risk } from "./admin";

export interface SSHInfo {
  /** Whether Grantra can sign certificates (it needs the vault key). */
  enabled: boolean;
  /** host or host:port of the SSH gateway, when the operator set it. */
  gateway_address: string;
  /** The line servers put in their TrustedUserCAKeys file. */
  ca_public_key: string;
}

export interface SSHTarget {
  id: string;
  name: string;
  address: string;
  login: string;
  notes?: string;
  entitlement_id: string;
  entitlement_name: string;
  risk: Risk;
  max_certificate_hours: number;
  /** Reached through the gateway, which records every session. */
  via_gateway: boolean;
  host_key?: string;
  created_at: string;
  /** Whether you hold access to it right now. */
  held: boolean;
}

export interface SSHTargetSettings {
  address?: string;
  notes?: string;
  max_certificate_hours?: number;
  via_gateway?: boolean;
  forget_host_key?: boolean;
}

export interface NewSSHTarget extends SSHTargetSettings {
  name: string;
  login: string;
  risk?: string;
}

export interface SSHCertificate {
  id: string;
  serial: number;
  target_id: string;
  target_name: string;
  login: string;
  principal: string;
  key_fingerprint: string;
  via_gateway: boolean;
  valid_after: string;
  valid_before: string;
  certificate: string;
}

export type SessionEnd = "closed" | "access_ended" | "terminated" | "lost" | "failed";

export interface SSHSession {
  id: string;
  target_id?: string;
  target_name: string;
  login: string;
  identity_id: string;
  identity_name: string;
  client_address: string;
  gateway: string;
  kind: "shell" | "exec";
  command?: string;
  width: number;
  height: number;
  started_at: string;
  ended_at?: string;
  end_reason?: SessionEnd;
  error?: string;
  exit_status?: number;
  terminate_requested: boolean;
  recording_bytes: number;
  recording_truncated: boolean;
}

export interface SSHCommand {
  id: number;
  session_id: string;
  at: string;
  offset_seconds: number;
  command: string;
  target_name?: string;
  login?: string;
  identity_id?: string;
  identity_name?: string;
}

export const getSSH = () => api<SSHInfo>("/api/v1/ssh");

export const listSSHTargets = () =>
  api<{ targets: SSHTarget[] }>("/api/v1/ssh/targets").then((r) => r.targets);

export const createSSHTarget = (t: NewSSHTarget) =>
  send<SSHTarget>("POST", "/api/v1/ssh/targets", t);

export const updateSSHTarget = (id: string, s: SSHTargetSettings) =>
  send<SSHTarget>("PUT", `/api/v1/ssh/targets/${id}`, s);

export const deleteSSHTarget = (id: string) => send<void>("DELETE", `/api/v1/ssh/targets/${id}`);

export const issueSSHCertificate = (id: string, publicKey: string) =>
  send<SSHCertificate>("POST", `/api/v1/ssh/targets/${id}/certificates`, {
    public_key: publicKey,
  });

export const listSSHSessions = (f: { active?: boolean; target?: string } = {}) => {
  const q = new URLSearchParams({ limit: "200" });
  if (f.active) q.set("active", "true");
  if (f.target) q.set("target", f.target);
  return api<{ sessions: SSHSession[] }>(`/api/v1/ssh/sessions?${q}`).then((r) => r.sessions);
};

export const getSSHSession = (id: string) =>
  api<SSHSession & { commands: SSHCommand[] }>(`/api/v1/ssh/sessions/${id}`);

export const recordingURL = (id: string) => `/api/v1/ssh/sessions/${id}/recording`;

export const terminateSSHSession = (id: string) =>
  send<void>("POST", `/api/v1/ssh/sessions/${id}/terminate`);

export const searchSSHCommands = (q: string) =>
  api<{ commands: SSHCommand[] }>(
    `/api/v1/ssh/commands?${new URLSearchParams({ q, limit: "200" })}`,
  ).then((r) => r.commands);

/** The ssh command that reaches a target with a Grantra certificate. */
export function connectCommand(
  t: Pick<SSHTarget, "name" | "login" | "address" | "via_gateway">,
  gateway: string,
) {
  if (!t.via_gateway) {
    const [host, port] = splitHostPort(t.address);
    return `ssh ${port === "22" ? "" : `-p ${port} `}${t.login}@${host}`;
  }
  const [host, port] = splitHostPort(gateway || "GATEWAY:2222");
  return `ssh ${port === "22" ? "" : `-p ${port} `}${t.name}@${host}`;
}

function splitHostPort(addr: string): [string, string] {
  const m = /^\[(.+)\]:(\d+)$/.exec(addr) ?? /^([^:]+):(\d+)$/.exec(addr);
  return m ? [m[1] ?? addr, m[2] ?? "22"] : [addr, "22"];
}

export const endReasonText: Record<SessionEnd, string> = {
  closed: "Closed",
  access_ended: "Access ended",
  terminated: "Ended by an admin",
  lost: "Lost",
  failed: "Failed",
};
