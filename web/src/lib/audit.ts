import type { AuditEvent } from "./api";

/** "platform_role.granted" reads as "Platform role granted". */
export function actionLabel(action: string) {
  const words = action.replace(/[._]/g, " ").trim();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

const systemActors: Record<string, string> = {
  aggregation: "account sync",
  birthright: "birthright rules",
  bootstrap: "first start",
  demo: "demo data",
  lifecycle: "HR sync",
  provisioning: "provisioning",
  requests: "request timers",
  reviews: "review timers",
  sod: "separation of duties checks",
};

/** Who did it, in words, and the identity behind it when there is one. */
export function describeActor(e: Pick<AuditEvent, "actor" | "actor_name">): {
  label: string;
  identityId?: string;
} {
  const [kind, ...rest] = e.actor.split(":");
  const id = rest.join(":");
  if (kind === "identity") return { label: e.actor_name || "A deleted identity", identityId: id };
  if (kind === "system") return { label: `Grantra (${systemActors[id] ?? id})` };
  if (kind === "oidc") return { label: "Someone signing in" };
  return { label: e.actor };
}

/** Text to match a filter against. */
export const searchText = (e: AuditEvent) =>
  [
    describeActor(e).label,
    e.action,
    actionLabel(e.action),
    e.target_type,
    e.target_name,
    e.target_id,
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
