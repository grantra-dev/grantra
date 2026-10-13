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

function StepLine({ s }: { s: ApprovalStep }) {
  const who = s.kind === "admin" ? kindLabels.admin : `${kindLabels[s.kind]}: ${s.approver_name}`;
  return (
    <li className="flex items-start gap-2 text-sm">
      <span className="mt-0.5">{stepIcons[s.status]}</span>
      <span>
        <span className="font-medium">{who}</span>{" "}
        <span className="text-slate-500">
          {stepLabels[s.status]}
          {s.decided_at && ` on ${formatDate(s.decided_at)}`}
          {s.status === "pending" && s.due_at && `, by ${formatDate(s.due_at)} or it expires`}
        </span>
        {s.comment && <span className="text-slate-600"> · “{s.comment}”</span>}
      </span>
    </li>
  );
}

/** The approval chain of a request, in order. */
export function RequestSteps({ steps }: { steps: ApprovalStep[] }) {
  return (
    <ol className="mt-3 flex flex-col gap-1.5 border-t border-slate-100 pt-3">
      {steps.map((s) => (
        <StepLine key={s.position} s={s} />
      ))}
    </ol>
  );
}
