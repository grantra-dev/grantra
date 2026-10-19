import { Badge } from "@/components/ui/badge";
import type { Severity } from "@/lib/threats";
import { capitalize } from "@/lib/utils";

const severityVariants = { low: "default", medium: "amber", high: "red", critical: "red" } as const;

export function SeverityBadge({ severity }: { severity: Severity }) {
  return (
    <Badge
      variant={severityVariants[severity]}
      className={severity === "critical" ? "ring-1 ring-red-400" : undefined}
    >
      {capitalize(severity)}
    </Badge>
  );
}
