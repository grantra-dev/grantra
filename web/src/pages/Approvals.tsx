import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { PageHeader } from "@/components/PageHeader";
import { ApprovalFlow } from "@/components/ApprovalFlow";
import { Alert, ErrorAlert } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Label, Select, Textarea } from "@/components/ui/field";
import { decideRequest, durationText, listApprovals, type AccessRequest, type Me } from "@/lib/api";
import { focusRing, useFocusParam } from "@/lib/focus";
import { stepTitle, whoText } from "@/lib/approvals";
import { exceptionChoices, SettingKey, useSetting } from "@/lib/settings";
import { cn, formatDate } from "@/lib/utils";

const wants = { access: "", eligibility: "to be eligible for ", activation: "to activate " };

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
  // An approval email links to its request.
  const asked = useFocusParam("request", !!data);
  const gone = asked && data && !data.some((r) => r.id === asked);

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
      {gone && !outcomes.some((o) => o.id === asked) && (
        <Alert variant="info" className="mb-3">
          The request you followed a link to no longer waits for you: someone decided it, it was
          withdrawn or it expired. Its requester sees how it ended in My requests.
        </Alert>
      )}
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
            <ApprovalCard key={r.id} r={r} me={me} onDecided={decided} focused={r.id === asked} />
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
  focused,
}: {
  r: AccessRequest;
  me: Me;
  onDecided: (o: Outcome) => void;
  focused: boolean;
}) {
  const [comment, setComment] = useState("");
  const queryClient = useQueryClient();
  const step = r.steps.find((s) => s.status === "pending");
  const risk = step?.kind === "risk";
  const [picked, setDays] = useState(r.duration_days ?? (r.duration_hours ? 7 : 90));
  // Never longer than the tenant lets a risk be accepted for.
  const most = useSetting(SettingKey.SoDExceptionMaxDays, risk);
  const days = Math.min(picked, most ?? picked);
  const decide = useMutation({
    mutationFn: (decision: "approve" | "reject") =>
      decideRequest(
        r.id,
        decision,
        comment.trim(),
        risk && decision === "approve" ? days : undefined,
      ),
    onSuccess: async (out, decision) => {
      onDecided({ id: r.id, text: outcomeText(out, decision, risk) });
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["approvals"] }),
        queryClient.invalidateQueries({ queryKey: ["inbox"] }),
      ]);
    },
  });
  const as =
    !step || risk || !step.approvers.some((a) => a.id === me.identity_id)
      ? "a Grantra admin"
      : step.kind === "manager"
        ? `${r.beneficiary_name}'s manager`
        : step.kind === "owner"
          ? "the owner"
          : stepTitle(step);
  const conflicts = r.sod_conflicts?.join(", ") || "a policy in prevent mode";

  return (
    <li data-focus={r.id}>
      <Card className={cn("p-4", focused && focusRing)}>
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="min-w-0">
            <div className="font-medium">
              {r.beneficiary_name} wants {wants[r.kind]}
              {r.target_name}
            </div>
            <div className="text-sm text-slate-500">
              {r.type === "role" ? "Role" : r.application}
              {r.requester_id !== r.beneficiary_id && <> · asked by {r.requester_name}</>}
              {" · "}
              {durationText(r) || "no end date"}
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
        <ApprovalFlow r={r} />
        {risk && (
          <Alert variant="warning" className="mt-3">
            Together with what {r.beneficiary_name} already has, this access breaks separation of
            duties: <strong>{conflicts}</strong>. Approving it accepts that risk for{" "}
            {r.beneficiary_name}: the violation is recorded as an exception with your reason, and it
            reopens when the exception ends.
          </Alert>
        )}
        <form
          className="mt-4 flex flex-col gap-3 border-t border-slate-100 pt-4"
          onSubmit={(e) => e.preventDefault()}
        >
          <div>
            <Label htmlFor={`comment-${r.id}`}>
              {risk ? "Why is the risk acceptable?" : "Comment"}
            </Label>
            <Textarea
              id={`comment-${r.id}`}
              className="min-h-16"
              maxLength={2000}
              value={comment}
              onChange={(e) => setComment(e.target.value)}
              placeholder={
                risk
                  ? "Required: why it is needed and how the risk is kept in check"
                  : "Optional when approving, required when rejecting"
              }
            />
          </div>
          {risk && (
            <div>
              <Label htmlFor={`days-${r.id}`}>Accept the risk for</Label>
              <Select
                id={`days-${r.id}`}
                value={days}
                onChange={(e) => setDays(Number(e.target.value))}
              >
                {exceptionChoices(most, r.duration_days).map((d) => (
                  <option key={d} value={d}>
                    {d === r.duration_days ? `${d} days, as long as the access` : `${d} days`}
                  </option>
                ))}
              </Select>
            </div>
          )}
          <ErrorAlert error={decide.error} />
          <div className="flex flex-wrap items-center gap-2">
            <Button
              type="button"
              onClick={() => decide.mutate("approve")}
              disabled={decide.isPending || (risk && !comment.trim())}
            >
              {risk ? "Accept the risk and approve" : "Approve"}
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

function outcomeText(r: AccessRequest, decision: "approve" | "reject", risk: boolean) {
  const what = `${r.beneficiary_name}'s request for ${r.target_name}`;
  if (decision === "reject") return `You rejected ${what}.`;
  if (r.status === "approved")
    return risk
      ? `You accepted the separation of duties risk and approved ${what}. The access is granted and the exception is recorded.`
      : `You approved ${what}. The access is granted.`;
  const next = r.steps.find((s) => s.status === "pending");
  if (next?.kind === "risk")
    return `You approved ${what}. It breaks separation of duties, so it now waits for a Grantra admin to accept the risk.`;
  if (!next) return `You approved ${what}.`;
  const done = new Set(next.approvals.map((a) => a.identity_id));
  const left = next.approvers.filter((a) => !done.has(a.id)).map((a) => a.name);
  return `You approved ${what}. It now waits for ${whoText(left, next.required - next.approvals.length)}.`;
}
