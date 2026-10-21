import { useQuery } from "@tanstack/react-query";
import { ArrowUpRight, Check, Circle, CircleDot, Minus, Send, ShieldCheck, X } from "lucide-react";
import type { ReactNode } from "react";
import { ApiError, getRequest, type AccessRequest, type ApprovalStep } from "@/lib/api";
import { policyText, stepTitle, timeoutLabels, whoText, type Plan } from "@/lib/approvals";
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

/** One line of a flow: an icon, what happened, and the details under it. */
function Line({
  icon,
  title,
  children,
}: {
  icon: ReactNode;
  title: ReactNode;
  children?: ReactNode;
}) {
  return (
    <li className="flex items-start gap-2 text-sm">
      <span className="mt-0.5">{icon}</span>
      <div className="min-w-0">
        <div>{title}</div>
        {children && <div className="text-slate-500">{children}</div>}
      </div>
    </li>
  );
}

function StepLine({ s, since }: { s: ApprovalStep; since?: string }) {
  // A step that has waited more than a day says so: a request stuck with
  // someone should be visible, not a mystery.
  const waited = since ? daysSince(since) : 0;
  const approved = new Set(s.approvals.map((a) => a.identity_id));
  const names = s.approvers.map((a) => (approved.has(a.id) ? `${a.name} ✓` : a.name));
  const status = s.kind === "risk" && s.status === "approved" ? "accepted" : stepLabels[s.status];
  return (
    <Line
      icon={stepIcons[s.status]}
      title={
        <>
          <span className="font-medium">{stepTitle(s)}</span>{" "}
          <span className="text-slate-500">
            {status}
            {s.exception_days && ` for ${s.exception_days} days`}
            {s.decided_at && ` on ${formatDate(s.decided_at)}`}
          </span>
        </>
      }
    >
      {s.kind !== "risk" && <div>{whoText(names, s.required)}</div>}
      {s.status === "pending" && s.required > 1 && (
        <div>
          {s.approvals.length} of {s.required} approvals
        </div>
      )}
      {s.escalated_at && (
        <div className="flex items-center gap-1">
          <ArrowUpRight className="size-3.5" aria-hidden />
          Escalated on {formatDate(s.escalated_at)}: {s.escalated_from?.join(", ") || "nobody"} did
          not decide in time
        </div>
      )}
      {s.status === "pending" && (
        <div>
          {waited >= 1 && `Waiting ${waited} ${waited === 1 ? "day" : "days"}. `}
          {s.due_at && `By ${formatDate(s.due_at)}, or ${timeoutLabels[s.on_timeout]}.`}
        </div>
      )}
      {/* Several approvals each say why; a single decision has the step's comment. */}
      {s.approvals.length > 1
        ? s.approvals
            .filter((a) => a.comment)
            .map((a) => (
              <div key={a.identity_id} className="text-slate-600">
                {a.name}: “{a.comment}”
              </div>
            ))
        : s.comment && <div className="text-slate-600">“{s.comment}”</div>}
    </Line>
  );
}

const outcomes = {
  approved: {
    icon: <ShieldCheck className="size-4 text-emerald-600" aria-hidden />,
    text: "Granted",
  },
  rejected: { icon: <X className="size-4 text-red-600" aria-hidden />, text: "Rejected" },
  expired: { icon: <Minus className="size-4 text-slate-400" aria-hidden />, text: "Expired" },
  cancelled: { icon: <Minus className="size-4 text-slate-400" aria-hidden />, text: "Cancelled" },
};

/**
 * The approval flow of a request, from asking to the outcome: who decided
 * each step and when, who it waits on now and until when, and what comes
 * next. full also shows who asked and the outcome, for a request seen on
 * its own.
 */
export function ApprovalFlow({ r, full = false }: { r: AccessRequest; full?: boolean }) {
  const outcome = r.status !== "pending" ? outcomes[r.status] : undefined;
  return (
    <div className="mt-3 border-t border-slate-100 pt-3">
      <ol className="flex flex-col gap-2" aria-label="Approval flow">
        {full && (
          <Line
            icon={<Send className="size-4 text-brand-600" aria-hidden />}
            title={
              <>
                <span className="font-medium">Asked by {r.requester_name}</span>{" "}
                <span className="text-slate-500">on {formatDate(r.created_at)}</span>
              </>
            }
          >
            <span className="text-slate-600">“{r.justification}”</span>
          </Line>
        )}
        {r.steps.map((s, i) => (
          <StepLine
            key={s.position}
            s={s}
            since={
              s.status === "pending"
                ? (s.escalated_at ??
                  r.steps
                    .slice(0, i)
                    .map((p) => p.decided_at)
                    .filter(Boolean)
                    .at(-1) ??
                  r.created_at)
                : undefined
            }
          />
        ))}
        {full && outcome && (
          <Line
            icon={outcome.icon}
            title={
              <>
                <span className="font-medium">{outcome.text}</span>
                {r.closed_at && (
                  <span className="text-slate-500"> on {formatDate(r.closed_at)}</span>
                )}
              </>
            }
          >
            {r.reason}
          </Line>
        )}
      </ol>
      <p className="mt-2 text-xs text-slate-500">{policyText(r)}</p>
    </div>
  );
}

/** The approval flow of the request someone's access was granted by, loaded on demand. */
export function RequestFlow({ id }: { id: string }) {
  const { data, error } = useQuery({
    queryKey: ["access-request", id],
    queryFn: () => getRequest(id),
  });
  if (error)
    return (
      <p className="text-sm text-slate-500">
        {error instanceof ApiError && error.status === 403
          ? "Only the people involved, admins and auditors see how it was approved."
          : `Could not load how it was approved: ${error.message}`}
      </p>
    );
  if (!data) return <p className="text-sm text-slate-500">Loading…</p>;
  return (
    <div>
      <p className="text-sm font-medium">
        Request for {data.target_name}
        {data.application && ` (${data.application})`}
        {data.requester_id !== data.beneficiary_id && `, for ${data.beneficiary_name}`}
      </p>
      <ApprovalFlow r={data} full />
    </div>
  );
}

/**
 * Who a request would ask, before it is sent: plan's steps, then an admin
 * when it breaks separation of duties (conflicts).
 */
export function PlanSteps({ plan, conflicts = [] }: { plan: Plan; conflicts?: string[] }) {
  return (
    <div>
      <ol className="flex flex-col gap-2" aria-label="Who approves">
        {plan.steps.length === 0 && conflicts.length === 0 && (
          <Line
            icon={<ShieldCheck className="size-4 text-emerald-600" aria-hidden />}
            title={<span className="font-medium">Nobody: it is granted at once</span>}
          >
            The policy approves {plan.risk} risk access with no step.
          </Line>
        )}
        {plan.steps.map((s, i) => (
          <Line
            key={i}
            icon={<span className="text-xs font-semibold text-slate-400">{i + 1}</span>}
            title={<span className="font-medium">{stepTitle(s)}</span>}
          >
            <div>
              {whoText(
                s.approvers.map((a) => a.name),
                s.required,
              )}
              {s.requester_approves && ", one approval given by asking"}
            </div>
            <div>
              {s.timeout_hours ? `${s.timeout_hours} hours to decide` : "The usual time to decide"},
              then {timeoutLabels[s.on_timeout]}.
            </div>
            {s.note && <div className="text-amber-700">{s.note}</div>}
          </Line>
        ))}
        {conflicts.length > 0 && (
          <Line
            icon={
              <span className="text-xs font-semibold text-slate-400">{plan.steps.length + 1}</span>
            }
            title={<span className="font-medium">{stepTitle({ kind: "risk" })}</span>}
          >
            Breaks {conflicts.join(", ")}: an admin accepts the risk, with a reason and an end date.
          </Line>
        )}
      </ol>
      <p className="mt-2 text-xs text-slate-500">{policyText(plan)}</p>
    </div>
  );
}
