import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import { Manage } from "@/components/Manage";
import { Tabs } from "@/components/Tabs";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import type { Me } from "@/lib/api";
import { getThreats, scanThreats, severities } from "@/lib/threats";
import { Alerts } from "./threats/Alerts";
import { Detections } from "./threats/Detections";
import { SeverityBadge } from "./threats/shared";
import { SignIns } from "./threats/SignIns";

type Tab = "alerts" | "sign-ins" | "detections";

/** Security → Threats: what Grantra found, where it looks, and what it looks for. */
export function Threats({ me }: { me: Me }) {
  const [params, setParams] = useSearchParams();
  const asked = params.get("tab");
  const tab: Tab = asked === "sign-ins" || asked === "detections" ? asked : "alerts";
  const client = useQueryClient();
  const overview = useQuery({ queryKey: ["threats"], queryFn: getThreats });
  const scan = useMutation({
    mutationFn: scanThreats,
    onSuccess: () => void client.invalidateQueries({ queryKey: ["threats"] }),
  });
  const open = overview.data?.open ?? {};
  const openTotal = severities.reduce((n, s) => n + (open[s] ?? 0), 0);
  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="flex flex-wrap items-center gap-2 text-sm text-slate-600">
          {overview.isPending ? (
            "Loading…"
          ) : openTotal === 0 ? (
            "No open alert."
          ) : (
            <>
              Open:
              {severities.map(
                (s) =>
                  !!open[s] && (
                    <span key={s} className="flex items-center gap-1">
                      <SeverityBadge severity={s} /> {open[s]}
                    </span>
                  ),
              )}
            </>
          )}
        </p>
        <Manage>
          <Button
            variant="outline"
            size="sm"
            disabled={scan.isPending}
            onClick={() => scan.mutate()}
          >
            {scan.isPending ? "Looking…" : "Look now"}
          </Button>
        </Manage>
      </div>
      {scan.data && (
        <Alert variant={scan.data.error ? "warning" : "success"}>
          Read {scan.data.result.read} new sign-ins, looked at {scan.data.result.evaluated}, opened{" "}
          {scan.data.result.raised} alerts and made {scan.data.result.escalated} more severe.
          {scan.data.error && ` Part of it failed: ${scan.data.error}`}
        </Alert>
      )}
      <ErrorAlert error={scan.error} />
      <Tabs
        tabs={[
          ["alerts", "Alerts"],
          ["sign-ins", "Sign-ins"],
          ["detections", "Detections"],
        ]}
        current={tab}
        onSelect={(t) => setParams(t === "alerts" ? {} : { tab: t })}
      />
      {tab === "alerts" ? (
        <Alerts me={me} />
      ) : tab === "sign-ins" ? (
        <SignIns overview={overview.data} />
      ) : (
        <Detections />
      )}
    </div>
  );
}
