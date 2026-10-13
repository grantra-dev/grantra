import { Badge } from "@/components/ui/badge";
import type { Requestable } from "@/lib/api";

const variants = { medium: "amber", high: "red", critical: "red" } as const;

/** Shows the risk of an entitlement; low risk is the norm and shows nothing. */
export function RiskBadge({ risk }: { risk: Requestable["risk"] }) {
  if (!risk || risk === "low") return null;
  return <Badge variant={variants[risk]}>{risk[0]!.toUpperCase() + risk.slice(1)} risk</Badge>;
}
