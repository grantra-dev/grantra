import { useQuery } from "@tanstack/react-query";
import {
  AlertTriangle,
  ArrowRight,
  CircleCheck,
  Clock,
  FileQuestion,
  Inbox,
  ShieldAlert,
  UserX,
  type LucideIcon,
} from "lucide-react";
import type { ReactNode } from "react";
import { Link } from "react-router";
import { PageHeader } from "@/components/PageHeader";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import type { Me } from "@/lib/api";
import { getHome, pendingReviews, type FailedRun, type Inbox as InboxData } from "@/lib/console";
import { useInbox } from "@/lib/hooks";
import { cn, formatDate } from "@/lib/utils";
import { visibleGroups } from "@/modules";

/** One thing worth knowing today, linking to where to act on it. */
function Notice({
  icon: Icon,
  tone = "brand",
  title,
  to,
  children,
}: {
  icon: LucideIcon;
  tone?: "brand" | "amber" | "red";
  title: string;
  to: string;
  children?: ReactNode;
}) {
  const colors = { brand: "text-brand-600", amber: "text-amber-600", red: "text-red-600" };
  return (
    <Card className="flex flex-col p-4">
      <div className="flex items-start gap-3">
        <Icon className={cn("mt-0.5 size-5 shrink-0", colors[tone])} aria-hidden />
        <div className="min-w-0 flex-1">
          <Link to={to} className="font-medium hover:underline">
            {title}
          </Link>
          {children && <div className="mt-1 text-sm text-slate-600">{children}</div>}
        </div>
      </div>
    </Card>
  );
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

function waitingText(inbox: InboxData) {
  const parts = [
    inbox.approvals.length > 0 && plural(inbox.approvals.length, "approval", "approvals"),
    pendingReviews(inbox) > 0 &&
      plural(pendingReviews(inbox), "access to review", "accesses to review"),
    inbox.break_glass.length > 0 &&
      plural(inbox.break_glass.length, "break-glass use", "break-glass uses"),
  ].filter(Boolean);
  return parts.join(", ");
}

const runLink = (r: FailedRun) =>
  r.kind === "hr" ? "/settings/hr-sources" : `/governance/applications/${r.id}?tab=runs`;

const runKind = { aggregation: "reading accounts", provisioning: "provisioning", hr: "HR import" };

/** What needs this person today, by what they do in Grantra. */
function Today() {
  const inbox = useInbox();
  const home = useQuery({ queryKey: ["home"], queryFn: getHome });
  if (home.isPending) return <p className="text-sm text-slate-500">Loading…</p>;
  if (home.error) {
    return (
      <p role="alert" className="text-sm text-red-700">
        Could not load your home page: {home.error.message}
      </p>
    );
  }
  const h = home.data;
  const g = h.governance;
  const notices: ReactNode[] = [];
  if (inbox.data && inbox.data.total > 0) {
    notices.push(
      <Notice
        key="inbox"
        icon={Inbox}
        title={`${plural(inbox.data.total, "thing waits", "things wait")} on you`}
        to="/inbox"
      >
        {waitingText(inbox.data)}
      </Notice>,
    );
  }
  if (h.expiring_access.length > 0) {
    notices.push(
      <Notice
        key="expiring"
        icon={Clock}
        tone="amber"
        title="Your access ending this week"
        to="/my-access"
      >
        <ul className="space-y-0.5">
          {h.expiring_access.slice(0, 4).map((a) => (
            <li key={a.name + a.expires_at}>
              {a.name}
              {a.application && <span className="text-slate-500"> · {a.application}</span>} ·{" "}
              {formatDate(a.expires_at)} ·{" "}
              <Link to={`/request?q=${encodeURIComponent(a.name)}`} className="text-brand-600">
                ask for more time
              </Link>
            </li>
          ))}
        </ul>
      </Notice>,
    );
  }
  if (h.pending_requests > 0) {
    notices.push(
      <Notice
        key="requests"
        icon={Clock}
        title={`${plural(h.pending_requests, "request of yours is", "requests of yours are")} waiting`}
        to="/requests"
      >
        See who decides next and since when.
      </Notice>,
    );
  }
  if (g) {
    if (g.failed_runs.length > 0) {
      notices.push(
        <Notice
          key="runs"
          icon={AlertTriangle}
          tone="red"
          title={`${plural(g.failed_runs.length, "connection is", "connections are")} failing`}
          to={runLink(g.failed_runs[0]!)}
        >
          <ul className="space-y-0.5">
            {g.failed_runs.slice(0, 4).map((r) => (
              <li key={r.kind + r.id}>
                <Link to={runLink(r)} className="font-medium text-slate-700 hover:underline">
                  {r.name}
                </Link>{" "}
                <span className="text-slate-500">
                  ({runKind[r.kind]}){r.error && `: ${r.error}`}
                </span>
              </li>
            ))}
          </ul>
        </Notice>,
      );
    }
    const orphans = g.orphans.reduce((n, o) => n + o.count, 0);
    if (orphans > 0) {
      notices.push(
        <Notice
          key="orphans"
          icon={UserX}
          tone="amber"
          title={`${plural(orphans, "account belongs", "accounts belong")} to nobody`}
          to={`/governance/applications/${g.orphans[0]!.application_id}?tab=accounts`}
        >
          {g.orphans.slice(0, 5).map((o, i) => (
            <span key={o.application_id}>
              {i > 0 && ", "}
              <Link
                to={`/governance/applications/${o.application_id}?tab=accounts`}
                className="text-brand-600"
              >
                {o.application}
              </Link>{" "}
              {o.count}
            </span>
          ))}
          {g.orphans.length > 5 && ", and more"}. Find out whose each one is, or remove it from the
          application.
        </Notice>,
      );
    }
    if (g.open_violations) {
      notices.push(
        <Notice
          key="sod"
          icon={ShieldAlert}
          tone="red"
          title={`${plural(g.open_violations, "separation of duties violation is", "separation of duties violations are")} open`}
          to="/governance/separation-of-duties"
        />,
      );
    }
    if (g.undescribed > 0) {
      notices.push(
        <Notice
          key="undescribed"
          icon={FileQuestion}
          title={`${plural(g.undescribed, "access has", "accesses have")} no description`}
          to="/governance/applications"
        >
          People ask for what they understand. Say in plain words what each one lets them do.
        </Notice>,
      );
    }
  }
  if (notices.length === 0) {
    notices.push(
      <Notice key="clear" icon={CircleCheck} title="All clear" to="/my-access">
        Nothing needs you today. Your access is in My access.
      </Notice>,
    );
  }
  return (
    <section aria-label="Today" className="mb-8">
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">{notices}</div>
    </section>
  );
}

export function Home({ me }: { me: Me }) {
  const firstName = me.display_name.split(" ")[0];
  return (
    <>
      <PageHeader title={`Hi, ${firstName}`} description="Here is what needs you today." />
      <Today />
      <div className="space-y-8">
        {visibleGroups(me).map((g) => (
          <section key={g.id} aria-labelledby={`home-${g.id}`}>
            <h2
              id={`home-${g.id}`}
              className="mb-3 text-xs font-semibold tracking-wide text-slate-500 uppercase"
            >
              {g.title}
            </h2>
            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              {g.items.map((s) => (
                <Link key={s.path} to={s.path} className="group">
                  <Card className="h-full transition-colors group-hover:border-brand-600">
                    <CardHeader>
                      <s.icon className="mb-2 size-5 text-brand-600" aria-hidden />
                      <CardTitle className="flex items-center justify-between">
                        {s.title}
                        <ArrowRight
                          className="size-4 text-slate-400 group-hover:text-brand-600"
                          aria-hidden
                        />
                      </CardTitle>
                      <CardDescription>{s.description}</CardDescription>
                    </CardHeader>
                  </Card>
                </Link>
              ))}
            </div>
          </section>
        ))}
      </div>
    </>
  );
}
