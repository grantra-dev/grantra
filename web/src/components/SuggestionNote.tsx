import { Badge } from "@/components/ui/badge";
import { peerGroupText, type Signal, type Suggestion } from "@/lib/insights";
import { formatDate } from "@/lib/utils";

/** Why Grantra suggests what it does, one line per thing it found. */
function reasons(s: Suggestion, days?: number): Record<Signal, string> {
  const group = peerGroupText(s);
  return {
    unused: s.last_used_at
      ? `Not used since ${formatDate(s.last_used_at)}`
      : days
        ? `Not used once in the last ${days} days`
        : "Not used lately",
    used: `Used ${s.last_used_at ? formatDate(s.last_used_at) : "lately"}`,
    common: `${s.peer_holders} of ${s.peers} people ${group} have it too`,
    rare:
      s.peer_holders === 0
        ? `Nobody else ${group} has it (${s.peers} people)`
        : `Only ${s.peer_holders} of ${s.peers} people ${group} have it`,
  };
}

/**
 * Grantra's suggestion for some access under review, and why. days is how
 * long access goes unused before it counts as unused, for the tenant.
 */
export function SuggestionNote({
  suggestion: s,
  days,
}: {
  suggestion?: Suggestion;
  days?: number;
}) {
  if (!s?.action) return null;
  const why = reasons(s, days);
  return (
    <div className="mt-1 flex flex-col gap-0.5 text-xs">
      <Badge variant={s.action === "keep" ? "green" : "amber"} className="self-start">
        Grantra suggests: {s.action}
      </Badge>
      {s.signals.map((sig) => (
        <span key={sig} className="text-slate-500">
          {why[sig]}
        </span>
      ))}
    </div>
  );
}
