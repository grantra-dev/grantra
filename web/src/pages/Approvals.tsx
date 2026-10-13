import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { PageHeader } from "@/components/PageHeader";
import { RequestSteps } from "@/components/RequestSteps";
import { Alert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label, Textarea } from "@/components/ui/field";
import {
  ApiError,
  decideRequest,
  listApprovals,
  PlatformRole,
  type AccessRequest,
  type Me,
} from "@/lib/api";
import { kindLabels } from "@/lib/requests";
import { formatDate } from "@/lib/utils";

interface Outcome {
  id: string;
  text: string;
}

export function Approvals({ me }: { me: Me }) {
  const [outcomes, setOutcomes] = useState<Outcome[]>([]);
  const { data, error, isPending } = useQuery({
    queryKey: ["approvals"],
    queryFn: listApprovals,
  });
  const decided = (o: Outcome) => setOutcomes((prev) => [o, ...prev]);

  return (
    <>
      <PageHeader
        title="Approvals"
        description="Access requests waiting for your decision, oldest deadline first."
      />
      {outcomes.map((o) => (
        <Alert key={o.id} variant="success" className="mb-3">
          {o.text}
        </Alert>
      ))}
      {isPending ? (
        <p className="text-sm text-slate-500">Loading your approvals…</p>
      ) : error ? (
        <p role="alert" className="text-sm text-red-700">
          Could not load your approvals: {error.message}
        </p>
      ) : data.length === 0 ? (
        <Card className="p-6 text-sm text-slate-500">Nothing is waiting for you. 🎉</Card>
      ) : (
        <ul className="flex flex-col gap-3">
          {byDeadline(data).map((r) => (
            <ApprovalCard key={r.id} r={r} me={me} onDecided={decided} />
          ))}
        </ul>
      )}
    </>
  );
}

const deadline = (r: AccessRequest) =>
  r.steps.find((s) => s.status === "pending")?.due_at ?? r.created_at;

const byDeadline = (list: AccessRequest[]) =>
  [...list].sort((a, b) => deadline(a).localeCompare(deadline(b)));

function ApprovalCard({
  r,
  me,
  onDecided,
}: {
  r: AccessRequest;
  me: Me;
  onDecided: (o: Outcome) => void;
}) {
  const [comment, setComment] = useState("");
  const queryClient = useQueryClient();
  const step = r.steps.find((s) => s.status === "pending");
  const decide = useMutation({
    mutationFn: (decision: "approve" | "reject") => decideRequest(r.id, decision, comment.trim()),
    onSuccess: async (out, decision) => {
      onDecided({ id: r.id, text: outcomeText(out, decision) });
      await queryClient.invalidateQueries({ queryKey: ["approvals"] });
    },
  });
  const sodConflict =
    decide.error instanceof ApiError &&
    decide.error.status === 409 &&
    decide.error.message.includes("separation of duties");
  const as =
    step?.kind === "admin" || step?.approver_id !== me.identity_id
      ? "a Grantra admin"
      : step.kind === "manager"
        ? `${r.beneficiary_name}'s manager`
        : "the owner";

  return (
    <li>
      <Card className="p-4">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="font-medium">
              {r.beneficiary_name} wants {r.target_name}
            </div>
            <div className="text-sm text-slate-500">
              {r.type === "role" ? "Role" : r.application}
              {r.requester_id !== r.beneficiary_id && <> · asked by {r.requester_name}</>}
              {" · "}
              {r.duration_days ? `for ${r.duration_days} days` : "no end date"}
              {" · "}
              asked {formatDate(r.created_at)}
            </div>
          </div>
          {step?.due_at && (
            <span className="text-sm whitespace-nowrap text-amber-700">
              Decide by {formatDate(step.due_at)}
            </span>
          )}
        </div>
        <p className="mt-2 text-sm text-slate-700">“{r.justification}”</p>
        <RequestSteps steps={r.steps} />
        <form
          className="mt-4 flex flex-col gap-3 border-t border-slate-100 pt-4"
          onSubmit={(e) => e.preventDefault()}
        >
          <div>
            <Label htmlFor={`comment-${r.id}`}>Comment</Label>
            <Textarea
              id={`comment-${r.id}`}
              className="min-h-16"
              maxLength={2000}
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder="Optional when approving, required when rejecting"
            />
          </div>
          {sodConflict ? (
            <Alert variant="error">
              <p>
                You cannot approve this: {r.beneficiary_name} would hold both sides of a separation
                of duties policy in prevent mode ({policyNames(decide.error!.message)}). Grantra
                refused to grant it and the request is still pending.
              </p>
              <p className="mt-2">
                {me.platform_roles.includes(PlatformRole.Admin)
                  ? "Reject it with a comment explaining why, or review the policy if this combination is acceptable."
                  : "Reject it with a comment explaining why, or talk to a Grantra admin: they own the separation of duties policies."}
              </p>
            </Alert>
          ) : (
            decide.error && <Alert variant="error">{decide.error.message}</Alert>
          )}
          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              onClick={() => decide.mutate("approve")}
              disabled={decide.isPending || sodConflict}
            >
              Approve
            </Button>
            <Button
              type="button"
              variant="outline"
              onClick={() => decide.mutate("reject")}
              disabled={decide.isPending || !comment.trim()}
            >
              Reject
            </Button>
            <span className="text-xs text-slate-500">You decide as {as}.</span>
          </div>
        </form>
      </Card>
    </li>
  );
}

function outcomeText(r: AccessRequest, decision: "approve" | "reject") {
  const what = `${r.beneficiary_name}'s request for ${r.target_name}`;
  if (decision === "reject") return `You rejected ${what}.`;
  if (r.status === "approved") return `You approved ${what}. The access is granted.`;
  const next = r.steps.find((s) => s.status === "pending");
  const who = next?.kind === "admin" ? kindLabels.admin : next?.approver_name;
  return `You approved ${what}. It now waits for ${who ?? "the next approver"}.`;
}

// The core answers "this access would break separation of duties: A, B".
function policyNames(message: string) {
  return message.split(": ").slice(1).join(": ") || "see the policies";
}
