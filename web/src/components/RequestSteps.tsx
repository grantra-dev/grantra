import { Check, Circle, CircleDot, Minus, X } from "lucide-react";
import type { ApprovalStep } from "@/lib/api";
import { kindLabels } from "@/lib/requests";
import { formatDate } from "@/lib/utils";

const stepIcons = {
  waiting: <Circle className="size-4 text-slate-300" aria-hidden />,
  pending: <CircleDot className="size-4 text-amber-600" aria-hidden />,
  approved: <Check className="size-4 text-emerald-600" aria-hidden />,
  rejected: <X className="size-4 text-red-600" aria-hidden />,
  skipped: <Minus className="size-4 text-slate-300" aria-hidden />,
};

const stepLabels = {
  waiting: "next",
  pending: "deciding now",
  approved: "approved",
  rejected: "rejected",
  skipped: "skipped",
};

const day = 24 * 60 * 60 * 1000;

/** Whole days from iso until now. */
const daysSince = (iso: string) => Math.floor((Date.now() - new Date(iso).getTime()) / day);

function StepLine({ s, since }: { s: ApprovalStep; since?: string }) {
  // A step that has waited more than a day says so: a request stuck with
  // someone should be visible, not a mystery.
  const waited = since ? daysSince(since) : 0;
  const who =
    s.kind === "admin" || s.kind === "risk"
      ? kindLabels[s.kind]
      : `${kindLabels[s.kind]}: ${s.approver_name}`;
  return (
    <li className="flex items-start gap-2 text-sm">
      <span className="mt-0.5">{stepIcons[s.status]}</span>
      <span>
        <span className="font-medium">{who}</span>{" "}
        <span className="text-slate-500">
          {s.kind === "risk" && s.status === "approved" ? "accepted" : stepLabels[s.status]}
          {s.exception_days && ` for ${s.exception_days} days`}
          {s.decided_at && ` on ${formatDate(s.decided_at)}`}
          {s.status === "pending" &&
            waited >= 1 &&
            `, waiting ${waited} ${waited === 1 ? "day" : "days"}`}
          {s.status === "pending" && s.due_at && `, by ${formatDate(s.due_at)} or it expires`}
        </span>
        {s.comment && <span className="text-slate-600"> · “{s.comment}”</span>}
      </span>
    </li>
  );
}

/**
 * The approval chain of a request, in order. createdAt, when the request
 * was made, tells how long the step deciding now has waited.
 */
export function RequestSteps({ steps, createdAt }: { steps: ApprovalStep[]; createdAt?: string }) {
  return (
    <ol className="mt-3 flex flex-col gap-1.5 border-t border-slate-100 pt-3">
      {steps.map((s, i) => (
        <StepLine
          key={s.position}
          s={s}
          since={
            s.status === "pending" && createdAt
              ? (steps
                  .slice(0, i)
                  .map((p) => p.decided_at)
                  .filter(Boolean)
                  .at(-1) ?? createdAt)
              : undefined
          }
        />
      ))}
    </ol>
  );
}
