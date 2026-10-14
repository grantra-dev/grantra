import { Badge } from "@/components/ui/badge";

const runVariants: Record<string, "green" | "amber" | "red" | "brand" | "default"> = {
  applied: "green",
  succeeded: "green",
  planned: "brand",
  running: "brand",
  partial: "amber",
  refused: "amber",
  invalid: "red",
  failed: "red",
  interrupted: "red",
};

const runLabels: Record<string, string> = {
  applied: "Applied",
  succeeded: "Succeeded",
  planned: "Dry run",
  running: "Running",
  partial: "Partly failed",
  refused: "Stopped by safety limit",
  invalid: "File has errors",
  failed: "Failed",
  interrupted: "Interrupted",
};

export function RunStatus({ status }: { status: string }) {
  return <Badge variant={runVariants[status] ?? "default"}>{runLabels[status] ?? status}</Badge>;
}

// Counts that runs report, in words; any other key is shown as is.
const countLabels: Record<string, [string, string]> = {
  joiner: ["joiner", "joiners"],
  mover: ["mover", "movers"],
  leaver: ["leaver", "leavers"],
  rehire: ["rehire", "rehires"],
  update: ["detail update", "detail updates"],
  accounts: ["account", "accounts"],
  accounts_new: ["new account", "new accounts"],
  accounts_deleted: ["account gone", "accounts gone"],
  correlated: ["matched to a person", "matched to people"],
  orphans: ["orphan", "orphans"],
  entitlements: ["entitlement", "entitlements"],
  entitlements_new: ["new entitlement", "new entitlements"],
  entitlements_missing: ["entitlement gone", "entitlements gone"],
  memberships_added: ["membership added", "memberships added"],
  memberships_removed: ["membership removed", "memberships removed"],
  unknown_memberships: ["unknown membership", "unknown memberships"],
  unassigned_kept: ["membership kept", "memberships kept"],
  skipped_no_email: ["skipped without email", "skipped without email"],
  skipped_no_account: ["skipped without account", "skipped without account"],
  failed: ["failed", "failed"],
};

/** "2 joiners, 1 leaver". */
export function Counts({ counts }: { counts: Record<string, number | undefined> }) {
  const parts = Object.entries(counts).filter((e): e is [string, number] => !!e[1]);
  if (parts.length === 0) return <span className="text-slate-500">No changes</span>;
  const word = (k: string, n: number) => {
    const label = countLabels[k];
    return label ? label[n === 1 ? 0 : 1] : k.replace(/_/g, " ");
  };
  return <span>{parts.map(([k, n]) => `${n} ${word(k, n)}`).join(", ")}</span>;
}
