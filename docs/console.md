# Web console

One web app for everyone in the company. Each person sees the sections their
relationships and platform roles give them; the API checks every call with
the Cedar policies regardless of what the console shows.

| Section | Who sees it | Status |
|---|---|---|
| Home | Everyone | Done |
| My access | Everyone | Done: what you have, why (birthright rule, approved request, granted by an admin, activated just in time) and until when. What you are eligible for, activated for some hours with a reason and ended early. See [just-in-time.md](just-in-time.md) |
| Request access | Everyone | Done: search the catalog, ask with a reason and an optional duration (hours or days). For just-in-time access, ask to be eligible or for the access itself for a set time. Separation of duties conflicts in `prevent` mode show before sending, with what happens next: an admin has to accept the risk |
| My requests | Everyone | Done: each request with its approval steps, who decides now and until when, and a button to withdraw it while pending |
| Vault | Everyone | Done: the privileged accounts you hold access to, checked out with a reason and checked back in, and a **Break glass** button for responders. Admins add accounts, choose break-glass responders, type secrets, rotate passwords and add what Discovery finds; admins and auditors see every account with its history and every break-glass use, which admins review. See [vault.md](vault.md) |
| Servers | Everyone | Done: the SSH servers you hold access to, and a certificate for your public key with the command to connect. Admins add servers and see the key servers trust; admins and auditors list every gateway session, play recordings, search the commands typed, and admins end a running session. See [ssh.md](ssh.md) |
| Databases & clusters | Everyone | Done: the databases and Kubernetes clusters you hold access to, and a token with the command to connect (a kubeconfig for a cluster). Admins add them; admins and auditors list every session with its queries or requests, search them, and admins end a running session. See [gateways.md](gateways.md) |
| Approvals | Managers, application owners, admins | Done: the requests waiting on you, soonest deadline first, approve or reject (a comment is required to reject). Admins accept a separation of duties risk on the request itself, with a reason and how long it lasts |
| Access reviews | Managers, application owners, admins, auditors | Done: reviewers keep or revoke what they are asked to review, one by one or in bulk (bulk revoke asks for confirmation). Admins launch campaigns (by manager or by owner, all access or some applications) and close them; admins and auditors follow each campaign's progress |
| People | Admins, helpdesk, auditors | Done: search everyone by name or email and filter by status. Each person shows their details from HR, manager, access and accounts; admins and auditors also see their Grantra roles, their joiner, mover and leaver events and their audit history. Admins give (optionally until a date) and remove Grantra roles, never to themselves, and make people eligible for just-in-time access |
| Administration | Admins | Done. **HR sources**: add a CSV source, check an export (a dry run that lists every joiner, mover and leaver, errors and warnings), then apply it; a sync stopped by the leaver limit can be forced after a confirmation; run history. **Applications**: add applications and entitlements, see each one's connector settings (secret references only), read its accounts now, set provisioning (mode, change limit, removing memberships Grantra did not grant) and plan a dry run, browse accounts and orphans, see past runs, and make an entitlement just in time only. **Roles and rules**: business roles and birthright rules, read only. **Separation of duties**: add and delete policies, check for violations now, and accept a violation's risk for a set time with a reason. **Standing access**: high and critical risk access held with no end date, turned into eligibility in one click |
| Audit log | Admins, auditors | Done: every event newest first, with who did it and on what in words, the raw details on demand, and a filter over the loaded events |
| Email notifications | Everyone, from the bell in the header | Done: turn each kind of email on or off. Says so when this install sends no email or you have no address |

"Manager" and "application owner" come from the data (someone reports to you,
or you own an application), as returned by `GET /api/v1/me`; platform roles
are the Grantra entitlements described in [access.md](access.md).

Still only through the API: connector settings (they hold secret references),
column mappings for HR sources, and editing roles, birthright rules and
separation of duties policies.

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
