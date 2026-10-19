# Grantra help

The help pages people read inside the console, under **Help**. Each person sees the pages for what they do in Grantra; the list is in [`web/src/help/pages.ts`](../../web/src/help/pages.ts).

| Page | For |
|---|---|
| [Getting started](getting-started.md) | Everyone |
| [Privileged access](privileged-access.md) | Everyone |
| [Approvals and reviews](approvals-and-reviews.md) | Managers, application owners, admins, auditors |
| [People and audit](people-and-audit.md) | Admins, helpdesk, auditors |
| [Administration](administration.md) | Admins |
| [Threats](threats.md) | Admins, auditors |
| [Insights](insights.md) | Admins, auditors |
| [CSV files](csv-files.md) | Admins |

The console renders a small part of Markdown: headings, paragraphs, lists, tables, code, bold, italics and links. A link to another page here opens it in the console, a link to a file under `web/public` downloads it, and any other relative link opens on GitHub.
