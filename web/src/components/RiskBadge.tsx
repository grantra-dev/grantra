import { Badge } from "@/components/ui/badge";
import type { Requestable } from "@/lib/api";
import { capitalize } from "@/lib/utils";

const variants = { medium: "amber", high: "red", critical: "red" } as const;

/** Shows the risk of an entitlement; low risk is the norm and shows nothing. */
export function RiskBadge({ risk }: { risk: Requestable["risk"] }) {
  if (!risk || risk === "low") return null;
  return <Badge variant={variants[risk]}>{capitalize(risk)} risk</Badge>;
}

/** The four risk levels, lowest first, as options of a Select. */
export function RiskOptions() {
  return (["low", "medium", "high", "critical"] as const).map((r) => (
    <option key={r} value={r}>
      {capitalize(r)}
    </option>
  ));
}
