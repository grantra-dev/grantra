import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link, useLocation } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { RequestSteps } from "@/components/RequestSteps";
import { Alert } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
  cancelRequest,
  durationText,
  kindText,
  listMyRequests,
  type AccessRequest,
  type Me,
} from "@/lib/api";
import { formatDate } from "@/lib/utils";

const statusBadges = {
  pending: { variant: "amber", label: "Pending" },
  approved: { variant: "green", label: "Approved" },
  rejected: { variant: "red", label: "Rejected" },
  cancelled: { variant: "default", label: "Cancelled" },
  expired: { variant: "default", label: "Expired" },
} as const;

export function MyRequests({ me }: { me: Me }) {
  const location = useLocation();
  const created = (location.state as { created?: string } | null)?.created;
  const { data, error, isPending } = useQuery({
    queryKey: ["my-requests"],
    queryFn: listMyRequests,
  });

  return (
    <>
      <PageHeader title="My requests" description="What you asked for and where it stands." />
      {created && (
        <Alert variant="success" className="mb-4">
          Request for <strong>{created}</strong> sent. It now waits for its first approval.
        </Alert>
      )}
      {isPending ? (
        <p className="text-sm text-slate-500">Loading your requests…</p>
      ) : error ? (
        <p role="alert" className="text-sm text-red-700">
          Could not load your requests: {error.message}
        </p>
      ) : data.length === 0 ? (
        <Card className="p-6 text-sm text-slate-500">
          You have not asked for anything yet.{" "}
          <Link to="/request" className="text-brand-600 underline">
            Request access
          </Link>
        </Card>
      ) : (
        <ul className="flex flex-col gap-3">
          {data.map((r) => (
            <RequestCard key={r.id} r={r} me={me} />
          ))}
        </ul>
      )}
    </>
  );
}

function RequestCard({ r, me }: { r: AccessRequest; me: Me }) {
  const queryClient = useQueryClient();
  const cancel = useMutation({
    mutationFn: () => cancelRequest(r.id),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: ["my-requests"] }),
  });
  const badge = statusBadges[r.status];

  return (
    <li>
      <Card className="p-4">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0">
            <div className="flex flex-wrap items-center gap-2">
              <span className="font-medium">{r.target_name}</span>
              <Badge variant={badge.variant}>{badge.label}</Badge>
              {kindText(r) && <Badge variant="brand">{kindText(r)}</Badge>}
            </div>
            <div className="text-sm text-slate-500">
              {r.type === "role" ? "Role" : r.application}
              {r.beneficiary_id !== me.identity_id && <> · for {r.beneficiary_name}</>}
              {r.requester_id !== me.identity_id && <> · asked by {r.requester_name}</>}
              {" · "}
              {formatDate(r.created_at)}
              {durationText(r) && <> · {durationText(r)}</>}
            </div>
          </div>
          {r.status === "pending" && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => cancel.mutate()}
              disabled={cancel.isPending}
            >
              Withdraw
            </Button>
          )}
        </div>
        <p className="mt-2 text-sm text-slate-700">“{r.justification}”</p>
        {r.reason && <p className="mt-1 text-sm text-slate-500">{closingReason(r.reason)}</p>}
        <RequestSteps steps={r.steps} />
        {cancel.error && (
          <Alert variant="error" className="mt-3">
            {cancel.error.message}
          </Alert>
        )}
      </Card>
    </li>
  );
}

// The core records who withdrew a request as an actor id; the steps already
// say who was involved.
function closingReason(reason: string) {
  if (reason.startsWith("withdrawn")) return "Withdrawn.";
  return reason[0]!.toUpperCase() + reason.slice(1) + ".";
}
