// The help pages of the console. Their text lives in docs/help, so the same
// files read on GitHub; this list says who sees each one. The API does not
// guard them: they describe the product, and hiding pages only keeps the
// list short.

import administration from "../../../docs/help/administration.md?raw";
import approvalsAndReviews from "../../../docs/help/approvals-and-reviews.md?raw";
import csvFiles from "../../../docs/help/csv-files.md?raw";
import gettingStarted from "../../../docs/help/getting-started.md?raw";
import peopleAndAudit from "../../../docs/help/people-and-audit.md?raw";
import privilegedAccess from "../../../docs/help/privileged-access.md?raw";
import insights from "../../../docs/help/insights.md?raw";
import threats from "../../../docs/help/threats.md?raw";
import { PlatformRole, type Me } from "@/lib/api";
import { approves, hasRole } from "@/lib/roles";

export interface HelpPage {
  slug: string;
  title: string;
  summary: string;
  /** The Markdown, without its title. */
  body: string;
  visible: (me: Me) => boolean;
}

/** Splits the title (the first "# " line) from the rest of a page. */
function page(slug: string, source: string, summary: string, visible: HelpPage["visible"]) {
  const m = /^# (.+)\n/.exec(source.replace(/\r\n?/g, "\n"));
  if (!m) throw new Error(`help page ${slug} has no title`);
  return { slug, title: (m[1] ?? "").trim(), summary, body: source.slice(m[0].length), visible };
}

const everyone = () => true;

export const helpPages: HelpPage[] = [
  page(
    "getting-started",
    gettingStarted,
    "See your access, ask for more and follow your requests.",
    everyone,
  ),
  page(
    "privileged-access",
    privilegedAccess,
    "Just-in-time access, the vault, servers and remote access.",
    everyone,
  ),
  page(
    "approvals-and-reviews",
    approvalsAndReviews,
    "Decide on requests and review the access of your people or applications.",
    (me) => approves(me) || hasRole(me, PlatformRole.Auditor),
  ),
  page(
    "people-and-audit",
    peopleAndAudit,
    "Look someone up, give Grantra roles and read the audit log.",
    (me) => hasRole(me, PlatformRole.Admin, PlatformRole.Helpdesk, PlatformRole.Auditor),
  ),
  page(
    "administration",
    administration,
    "HR sources, applications, policies and API tokens.",
    (me) => hasRole(me, PlatformRole.Admin),
  ),
  page(
    "threats",
    threats,
    "Suspicious sign-ins and access, attack paths, and how to respond.",
    (me) => hasRole(me, PlatformRole.Admin, PlatformRole.Auditor),
  ),
  page("insights", insights, "Unused access and role suggestions.", (me) =>
    hasRole(me, PlatformRole.Admin, PlatformRole.Auditor),
  ),
  page(
    "csv-files",
    csvFiles,
    "The HR export and account file formats, templates and every error.",
    (me) => hasRole(me, PlatformRole.Admin),
  ),
];

export const visibleHelpPages = (me: Me) => helpPages.filter((p) => p.visible(me));
