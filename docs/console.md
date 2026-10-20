# Web console

One web app for everyone in the company. Each person sees the sections their
relationships and platform roles give them; the API checks every call with
the Cedar policies regardless of what the console shows.

The menu groups sections by what people come to do, and a group only shows
when the person has something in it: **For you** (my access, requests),
**To do** (approvals, reviews), **Privileged access** (vault, servers, remote
access), **Security** (threats), **Governance** (people, applications, roles, policies, audit log)
and **Settings** (HR sources, connectors, Grantra Login, API tokens,
modules). Groups fold, and the console remembers it in the browser. On small
screens the menu opens as a drawer. Each page belongs to a module
(`web/src/modules/`): a new feature adds its pages to its module's list and
the menu, routes and home page follow. Addresses from before the groups
(`/admin?tab=…`, `/admin/applications/…`) redirect to the new pages.

Across every page:

- **Search** (the box in the header, or <kbd>⌘K</kbd>, <kbd>Ctrl K</kbd> or
  <kbd>/</kbd>) finds pages by name, and people, roles, entitlements,
  applications, vault accounts, servers and remote targets through
  `GET /api/v1/search?q=`. It only returns what the person may see: people
  for admins, helpdesk and auditors, applications for admins and auditors,
  privileged targets for their readers or for whoever holds them, and
  requestable roles and entitlements for everyone. Each result opens the
  exact thing (`/vault?account=…`, `/servers?target=…`), or, for access you
  do not manage, Request access already searching for it.
- **Inbox** (the tray in the header and `/inbox`) lists everything waiting
  on you: requests to approve, campaigns with access to review, and, for
  admins, break-glass uses to review and high or critical threat alerts
  (never the ones about themselves). Menu items show the same counts. It
  comes from `GET /api/v1/me/inbox`, which uses the same conditions as the
  Approvals and Access reviews pages, so the numbers always match.
- **Emails link to the task**: an approval email opens
  `/approvals?request=…` with that request highlighted, a review email
  `/reviews?campaign=…` filtered to that campaign. If someone else already
  decided, the page says so.
- **Read only for auditors**: auditors see every Governance and Settings
  page, with a note saying only admins change them, and no buttons to change
  anything. The API refuses their changes anyway.

| Section | Who sees it | Status |
|---|---|---|
| Home | Everyone | Done: what needs you today, from `GET /api/v1/me/home`: what waits in your inbox, your access ending in the next 7 days (with a link to ask for more time) and your pending requests. Admins and auditors also see connections whose last run failed, accounts that belong to nobody, open separation of duties violations and access with no description. Shortcuts to each group below |
| My access | Everyone | Done: what you have, why (birthright rule, approved request, granted by an admin, activated just in time) and until when. What you are eligible for, activated for some hours with a reason and ended early. See [just-in-time.md](just-in-time.md) |
| Request access | Everyone | Done: search the catalog, ask with a reason and an optional duration (hours or days). Each item says how many of your teammates (people who share your manager, in teams of three or more) hold it, and items without a description say so instead of leaving a cryptic name alone. For just-in-time access, ask to be eligible or for the access itself for a set time. Separation of duties conflicts in `prevent` mode show before sending, with what happens next: an admin has to accept the risk |
| My requests | Everyone | Done: each request with its approval steps, who decides now, for how many days it has waited and until when, and a button to withdraw it while pending |
| Vault | Everyone | Done: the privileged accounts you hold access to, checked out with a reason and checked back in, and a **Break glass** button for responders. Admins add accounts, choose break-glass responders, type secrets, rotate passwords and add what Discovery finds; admins and auditors see every account with its history and every break-glass use, which admins review. See [vault.md](vault.md) |
| Servers | Everyone | Done: the SSH servers you hold access to, and a certificate for your public key with the command to connect. Admins add servers and see the key servers trust; admins and auditors list every gateway session, play recordings, search the commands typed, and admins end a running session. See [ssh.md](ssh.md) |
| Remote access | Everyone | Done: the databases, Kubernetes clusters, desktops and cloud consoles you hold access to. A token with the command to connect for a database or cluster (a kubeconfig for a cluster), a desktop in a new tab, or the cloud console signed in (with CLI credentials on AWS). Admins add them; admins and auditors list every session with its queries, requests or desktop recording, search them, and admins end a running session. See [gateways.md](gateways.md) |
| Approvals | Managers, application owners, admins | Done: the requests waiting on you, soonest deadline first, with how long each step has waited, approve or reject (a comment is required to reject). Admins accept a separation of duties risk on the request itself, with a reason and how long it lasts |
| Access reviews | Managers, application owners, admins, auditors | Done: reviewers keep or revoke what they are asked to review, one by one or in bulk (bulk revoke asks for confirmation). Find by person, access or application, filter by risk, by how the access was given and by what Grantra suggests (keep or revoke, with why), and sort by person, access, risk or age; bulk actions only reach what is shown. Admins launch campaigns (by manager or by owner, all access or some applications) and close them; admins and auditors follow each campaign's progress |
| People | Admins, helpdesk, auditors | Done: search everyone by name or email and filter by status. Each person shows their details from HR, manager, access and accounts; admins and auditors also see their Grantra roles, their joiner, mover and leaver events and their audit history. Admins give (optionally until a date) and remove Grantra roles, never to themselves, and make people eligible for just-in-time access |
| Security → Threats | Admins; auditors read only | Done: open threat alerts with their evidence, ending sessions, requiring a second factor, suspending and reinstating from the alert, resolving or dismissing it with a note; the latest sign-ins and where they come from; and every detection, to turn off, tune or let respond on its own. See [threat-detection.md](threat-detection.md). An alert also shows the high and critical risk access the person's account reaches |
| Security → Attack paths | Admins; auditors read only | Done: every high and critical risk entitlement someone can reach, with how many people reach it for good, through a shared account, outside Grantra and through accounts nobody owns; each one opens every path, step by step, and what to do about it. See [access-insights.md](access-insights.md) |
| Governance → Service accounts and agents | Admins; helpdesk and auditors read only | Done: service accounts, AI agents and workloads with their owner, accounts, last sign-in and end date, flagged when they have no owner, keep a high-risk secret outside the vault, went unused for 90 days or end soon; add one, change its owner, purpose, SPIFFE ID or end date, or retire it from its page; and the accounts nobody owns, the ones that look like a program's first, each made a service account with an owner in one step. Owners see theirs as a manager sees a report. See [non-human-identities.md](non-human-identities.md) |
| Governance → Insights | Admins; auditors read only | Done. **Unused access**: access given by hand, by request or as an eligibility and not used in 90 days, made just in time or removed in one click. **Role suggestions**: entitlements most of a department or job title hold one by one, turned into a role and its birthright rule. See [access-insights.md](access-insights.md) |
| Governance → Applications, Roles and rules, Separation of duties, Standing access | Admins; auditors read only | Done. **Applications**: add applications and entitlements, see each one's connector settings (secret references only), read its accounts now (or, for an application with no API, set up and import account files, checked first), set provisioning (mode, change limit, removing memberships Grantra did not grant) and plan a dry run, browse accounts and orphans, see past runs, and make an entitlement just in time only. **Roles and rules**: business roles and birthright rules, read only. **Separation of duties**: add and delete policies, check for violations now, and accept a violation's risk for a set time with a reason. **Standing access**: high and critical risk access held with no end date, turned into eligibility in one click |
| Settings → General, HR sources, Connectors, Grantra Login, API tokens, Modules | Admins; auditors read only | Done. **General**: time limits, reminders and thresholds for requests, reviews, separation of duties, privileged access, threat detection, insights and console sign-in, each with its default and range; a less strict change waits for a second admin, see [settings.md](settings.md). **Connectors**: the applications already connected, and a gallery of what Grantra connects to (SCIM 2.0, LDAP and Active Directory, Grantra Login, CSV files; Entra ID, Google Workspace, AWS, GitHub, Okta, Salesforce, Workday and BambooHR marked as planned). Connecting takes three steps: pick or name the application, fill in its settings (secrets as `env:GRANTRA_SECRET_…` references), then a first read to prove it works. **Modules**: turn privileged access or Grantra Login off for the whole company. Their pages, search results and inbox tasks disappear; nobody's access changes, the gateways keep running and the API keeps answering, so it hides features rather than securing them. **HR sources**: add or edit a CSV source, with the column that holds each field (a sample file shows which it already has), check an export (a dry run that lists every joiner, mover and leaver, errors and warnings), then apply it; a sync stopped by the leaver limit can be forced after a confirmation; run history. **Grantra Login**: see [grantra-login.md](grantra-login.md). **API tokens**: create [service tokens](service-tokens.md) for scripts that send HR exports and account files, each shown once with a `curl` example, and revoke them |
| Audit log | Admins, auditors | Done: every event newest first, with who did it and on what in words, the raw details on demand, and a filter over the loaded events |
| Help | Everyone | Done: the pages in [docs/help](help/README.md) for what the person does (approvers, helpdesk, auditors and admins see more), rendered from the same Markdown files. A **?** next to each CSV upload shows the file's columns and rules, with a template to download |
| Email notifications | Everyone, from the account menu in the header | Done: turn each kind of email on or off. Says so when this install sends no email or you have no address |

"Manager" and "application owner" come from the data (someone reports to you,
or you own an application), as returned by `GET /api/v1/me`; platform roles
are the Grantra entitlements described in [access.md](access.md).

Still only through the API: changing an existing connector's settings, and
editing roles and birthright rules.

## How it is served

- The core binary embeds the Vite build (`core/internal/webui`) and serves it
  at `/`. Any path without a file extension that is not an API or login route
  returns `index.html`, so deep links and reloads work.
- Same origin as the API, with the existing server-side session: no tokens in
  the browser, and the console needs no CORS.
- Its Content-Security-Policy only allows the console's own scripts, styles
  and API calls. Hashed files under `/assets/` are cached for a year;
  `index.html` is never cached.
- A binary built without the console (plain `go build`) serves a small
  placeholder page instead.

Development instructions are in [web/README.md](../web/README.md).
