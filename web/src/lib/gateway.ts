// Client for remote access through the gateway: databases, Kubernetes
// clusters, desktops and cloud consoles; the short-lived tokens people
// connect to databases and clusters with, and the sessions the gateway logs
// or records.

import { api, send } from "./api";
import type { Risk } from "./admin";
import type { SessionEnd } from "./ssh";

export type GatewayKind = "kubernetes" | "postgres" | "mysql" | "rdp" | "aws" | "azure" | "gcp";
export type Clipboard = "both" | "paste" | "none";

export const isCloud = (k: GatewayKind) => k === "aws" || k === "azure" || k === "gcp";
/** Targets reached with a token, from the person's own tools. */
export const usesToken = (k: GatewayKind) =>
  k === "kubernetes" || k === "postgres" || k === "mysql";
export type TLSMode = "verify" | "require" | "disable";

export interface GatewayInfo {
  /** Whether people can connect (it needs the vault key). */
  enabled: boolean;
  /** host:port of each listener, when the operator set them. */
  kubernetes_address: string;
  postgres_address: string;
  mysql_address: string;
  /** The PEM certificate clients verify the gateway with. */
  ca_certificate: string;
  /** Whether desktops can open (Grantra has guacd) and cloud consoles. */
  desktops_enabled: boolean;
  consoles_enabled: boolean;
}

export interface GatewayTarget {
  id: string;
  name: string;
  kind: GatewayKind;
  address: string;
  database?: string;
  tls_mode: TLSMode;
  ca_certificate?: string;
  /** What the gateway logs in with; only admins and auditors see it. */
  vault_account_id?: string;
  vault_account_name?: string;
  kube_groups: string[];
  /** Desktops: which way the clipboard works. */
  clipboard?: Clipboard;
  /** Azure and Google Cloud: the role people get; Azure: the directory. */
  cloud_role?: string;
  cloud_directory?: string;
  notes?: string;
  entitlement_id: string;
  entitlement_name: string;
  risk: Risk;
  max_token_hours: number;
  created_at: string;
  updated_at: string;
  /** Whether you hold access to it right now. */
  held: boolean;
}

export interface GatewayTargetSettings {
  address?: string;
  database?: string;
  tls_mode?: TLSMode;
  ca_certificate?: string;
  vault_account_id?: string;
  kube_groups?: string[];
  clipboard?: Clipboard;
  cloud_role?: string;
  cloud_directory?: string;
  max_token_hours?: number;
  notes?: string;
}

export interface NewGatewayTarget extends GatewayTargetSettings {
  name: string;
  kind: GatewayKind;
  risk?: string;
}

export interface GatewayToken {
  id: string;
  target_id: string;
  target_name: string;
  kind: GatewayKind;
  valid_before: string;
  token: string;
  kubeconfig?: string;
  ca_certificate: string;
}

export interface GatewaySession {
  id: string;
  target_id?: string;
  target_name: string;
  kind: GatewayKind;
  identity_id: string;
  identity_name: string;
  client_address: string;
  gateway: string;
  database?: string;
  account?: string;
  started_at: string;
  ended_at?: string;
  end_reason?: SessionEnd;
  error?: string;
  terminate_requested: boolean;
  events: number;
  /** When it ends at the latest. */
  valid_before?: string;
  /** A desktop session's screen recording, in bytes. */
  recording_bytes: number;
}

/** A cloud console sign-in: the link, and on AWS credentials for the CLI. */
export interface ConsoleSession {
  session_id: string;
  url: string;
  valid_before: string;
  credentials?: {
    access_key_id: string;
    secret_access_key: string;
    session_token: string;
    expiration: string;
  };
}

export interface GatewayEvent {
  id: number;
  session_id: string;
  at: string;
  kind: "query" | "request";
  text: string;
  /** The HTTP status a cluster answered a request with. */
  status?: number;
  target_name?: string;
  identity_id?: string;
  identity_name?: string;
}

const targets = "/api/v1/gateway/targets";

export const getGateway = () => api<GatewayInfo>("/api/v1/gateway");

export const listGatewayTargets = () =>
  api<{ targets: GatewayTarget[] }>(targets).then((r) => r.targets);

export const createGatewayTarget = (t: NewGatewayTarget) => send<GatewayTarget>("POST", targets, t);

export const updateGatewayTarget = (id: string, s: GatewayTargetSettings) =>
  send<GatewayTarget>("PUT", `${targets}/${id}`, s);

export const deleteGatewayTarget = (id: string) => send<void>("DELETE", `${targets}/${id}`);

export const issueGatewayToken = (id: string) =>
  send<GatewayToken>("POST", `${targets}/${id}/tokens`);

export const openConsole = (id: string) => send<ConsoleSession>("POST", `${targets}/${id}/console`);

/** The WebSocket a desktop session runs on (the Guacamole tunnel). */
export const desktopTunnelURL = (id: string) => `${targets}/${id}/rdp`;

/** A desktop session's screen recording, for the player. */
export const desktopRecordingURL = (id: string) => `/api/v1/gateway/sessions/${id}/recording`;

export const listGatewaySessions = (f: { active?: boolean; target?: string } = {}) => {
  const q = new URLSearchParams({ limit: "200" });
  if (f.active) q.set("active", "true");
  if (f.target) q.set("target", f.target);
  return api<{ sessions: GatewaySession[] }>(`/api/v1/gateway/sessions?${q}`).then(
    (r) => r.sessions,
  );
};

/** A session and its log, 500 events at a time after the event id after. */
export const getGatewaySession = (id: string, after = 0) =>
  api<GatewaySession & { log: GatewayEvent[] }>(
    `/api/v1/gateway/sessions/${id}${after ? `?after=${after}` : ""}`,
  );

export const terminateGatewaySession = (id: string) =>
  send<void>("POST", `/api/v1/gateway/sessions/${id}/terminate`);

export const searchGatewayEvents = (q: string) =>
  api<{ events: GatewayEvent[] }>(
    `/api/v1/gateway/events?${new URLSearchParams({ q, limit: "200" })}`,
  ).then((r) => r.events);

export const kindText: Record<GatewayKind, string> = {
  kubernetes: "Kubernetes",
  postgres: "PostgreSQL",
  mysql: "MySQL",
  rdp: "Desktop (RDP)",
  aws: "AWS",
  azure: "Azure",
  gcp: "Google Cloud",
};

export type TokenKind = "kubernetes" | "postgres" | "mysql";

export const defaultPorts: Record<TokenKind, string> = {
  kubernetes: "8443",
  postgres: "15432",
  mysql: "13306",
};

/** Where the gateway listens for a kind of target. */
export function gatewayAddress(info: GatewayInfo | undefined, kind: TokenKind) {
  const addr =
    kind === "kubernetes"
      ? info?.kubernetes_address
      : kind === "postgres"
        ? info?.postgres_address
        : info?.mysql_address;
  return addr || `GATEWAY:${defaultPorts[kind]}`;
}

function splitHostPort(addr: string): [string, string] {
  const m = /^\[(.+)\]:(\d+)$/.exec(addr) ?? /^([^:]+):(\d+)$/.exec(addr);
  return m ? [m[1] ?? addr, m[2] ?? ""] : [addr, ""];
}

/** The shell commands that use AWS credentials with the AWS CLI. */
export function awsExports(c: NonNullable<ConsoleSession["credentials"]>) {
  return [
    `export AWS_ACCESS_KEY_ID=${c.access_key_id}`,
    `export AWS_SECRET_ACCESS_KEY=${c.secret_access_key}`,
    `export AWS_SESSION_TOKEN=${c.session_token}`,
  ].join("\n");
}

/**
 * The command that connects to a database through the gateway, with the
 * gateway's CA saved as grantra-gateway-ca.pem. The token is the password.
 */
export function connectCommand(t: Pick<GatewayTarget, "name" | "kind">, gateway: string) {
  const [host, port] = splitHostPort(gateway);
  if (t.kind === "postgres") {
    return `psql "host=${host} port=${port} user=${t.name} sslmode=verify-full sslrootcert=grantra-gateway-ca.pem"`;
  }
  if (t.kind === "mysql") {
    return `mysql -h ${host} -P ${port} -u ${t.name} -p --ssl-mode=VERIFY_IDENTITY --ssl-ca=grantra-gateway-ca.pem`;
  }
  return `kubectl --kubeconfig grantra-${t.name}.yaml get pods`;
}
