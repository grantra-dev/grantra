import {
  ChevronRight,
  ClipboardCheck,
  ListChecks,
  Radar,
  Siren,
  SlidersHorizontal,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { pendingReviews } from "@/lib/console";
import { useInbox } from "@/lib/hooks";
import { describeSetting } from "@/lib/settings";
import { formatDate, formatTime } from "@/lib/utils";

const wants = { access: "", eligibility: "to be eligible for ", activation: "to activate " };

/** Due before now: late, so it goes first and says so. */
const late = (due?: string) => !!due && new Date(due).getTime() < Date.now();

function Due({ at }: { at?: string }) {
  if (!at) return null;
  return late(at) ? (
    <Badge variant="red">Overdue since {formatDate(at)}</Badge>
  ) : (
    <span className="text-sm whitespace-nowrap text-amber-700">by {formatDate(at)}</span>
  );
}

function Section({
  icon: Icon,
  title,
  count,
  children,
}: {
  icon: LucideIcon;
  title: string;
  count: number;
  children: ReactNode;
}) {
  return (
    <section aria-label={title}>
      <h2 className="mb-2 flex items-center gap-2 text-sm font-semibold text-slate-700">
        <Icon className="size-4 text-brand-600" aria-hidden />
        {title}
        <span className="font-normal text-slate-500">{count}</span>
      </h2>
      <Card>
        <ul className="divide-y divide-slate-100">{children}</ul>
      </Card>
    </section>
  );
}

function Task({ to, children, aside }: { to: string; children: ReactNode; aside?: ReactNode }) {
  return (
    <li>
      <Link
        to={to}
        className="flex items-center justify-between gap-3 p-4 text-sm hover:bg-slate-50"
      >
        <span className="min-w-0">{children}</span>
        <span className="flex shrink-0 items-center gap-2">
          {aside}
          <ChevronRight className="size-4 text-slate-400" aria-hidden />
        </span>
      </Link>
    </li>
  );
}

/** Everything waiting on you, each line opening its exact task. */
export function InboxPage() {
  const { data, error, isPending } = useInbox();
  return (
    <>
      <PageHeader
        title="Inbox"
        description="Everything waiting on you, from every part of Grantra. Each line opens the exact task."
      />
      {isPending ? (
        <p className="text-sm text-slate-500">Loading your inbox…</p>
      ) : error ? (
        <p role="alert" className="text-sm text-red-700">
          Could not load your inbox: {error.message}
        </p>
      ) : data.total === 0 ? (
        <Card className="p-6 text-sm text-slate-500">
          Nothing is waiting for you. 🎉 Your own requests are in{" "}
          <Link to="/requests" className="text-brand-600 underline">
            My requests
          </Link>
          .
        </Card>
      ) : (
        <div className="flex flex-col gap-6">
          {data.threats.length > 0 && (
            <Section icon={Radar} title="Threats" count={data.threats.length}>
              {data.threats.map((t) => (
                <Task
                  key={t.id}
                  to={`/security/threats?alert=${t.id}`}
                  aside={
                    <Badge variant="red">{t.severity === "critical" ? "Critical" : "High"}</Badge>
                  }
                >
                  <span className="block font-medium">{t.title}</span>
                  <span className="block text-slate-500">
                    {t.summary} · {formatTime(t.last_seen_at)}
                  </span>
                </Task>
              ))}
            </Section>
          )}
          {data.approvals.length > 0 && (
            <Section icon={ClipboardCheck} title="Approvals" count={data.approvals.length}>
              {data.approvals.map((r) => (
                <Task key={r.id} to={`/approvals?request=${r.id}`} aside={<Due at={r.due_at} />}>
                  <span className="block font-medium">
                    {r.beneficiary_name} wants {wants[r.kind]}
                    {r.target_name}
                  </span>
                  <span className="block text-slate-500">
                    {r.application ?? "Role"} · asked {formatDate(r.created_at)}
                  </span>
                </Task>
              ))}
            </Section>
          )}
          {data.reviews.length > 0 && (
            <Section icon={ListChecks} title="Access reviews" count={pendingReviews(data)}>
              {data.reviews.map((c) => (
                <Task
                  key={c.campaign_id}
                  to={`/reviews?campaign=${c.campaign_id}`}
                  aside={<Due at={c.due_at} />}
                >
                  <span className="block font-medium">{c.campaign_name}</span>
                  <span className="block text-slate-500">
                    {c.pending} {c.pending === 1 ? "access" : "accesses"} to keep or revoke
                  </span>
                </Task>
              ))}
            </Section>
          )}
          {!!data.setting_changes?.length && (
            <Section
              icon={SlidersHorizontal}
              title="Setting changes"
              count={data.setting_changes.length}
            >
              {data.setting_changes.map((c) => (
                <Task key={c.id} to="/settings/general">
                  <span className="block font-medium">
                    {c.proposed_by_name ?? "An admin"} wants {c.title.toLowerCase()} at{" "}
                    {describeSetting(c, c.value)}
                  </span>
                  <span className="block text-slate-500">
                    Now {describeSetting(c, c.old_value)} · asked {formatDate(c.proposed_at)}
                  </span>
                </Task>
              ))}
            </Section>
          )}
          {data.break_glass.length > 0 && (
            <Section icon={Siren} title="Break-glass reviews" count={data.break_glass.length}>
              {data.break_glass.map((u) => (
                <Task key={u.id} to={`/vault?tab=break-glass&use=${u.id}`}>
                  <span className="block font-medium">
                    {u.person} opened {u.account}
                  </span>
                  <span className="block text-slate-500">
                    “{u.reason}” · {formatTime(u.checked_out_at)}
                  </span>
                </Task>
              ))}
            </Section>
          )}
        </div>
      )}
    </>
  );
}
