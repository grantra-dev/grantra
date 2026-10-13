# Web console

One web app for everyone in the company. Each person sees the sections their
relationships and platform roles give them; the API checks every call with
the Cedar policies regardless of what the console shows.

| Section | Who sees it | Status |
|---|---|---|
| Home | Everyone | Done |
| My access | Everyone | Done: what you have, why (birthright rule, approved request, granted by an admin) and until when |
| Request access | Everyone | Done: search the catalog, ask with a reason and an optional duration. Separation of duties conflicts in `prevent` mode show before sending |
| My requests | Everyone | Done: each request with its approval steps, who decides now and until when, and a button to withdraw it while pending |
| Approvals | Managers, application owners, admins | [#22](https://github.com/grantra-dev/grantra/issues/22) |
| Access reviews | Managers, application owners, admins | [#23](https://github.com/grantra-dev/grantra/issues/23) |
| People | Admins, helpdesk, auditors | [#24](https://github.com/grantra-dev/grantra/issues/24) |
| Administration | Admins | [#24](https://github.com/grantra-dev/grantra/issues/24) |
| Audit log | Admins, auditors | [#24](https://github.com/grantra-dev/grantra/issues/24) |

"Manager" and "application owner" come from the data (someone reports to you,
or you own an application), as returned by `GET /api/v1/me`; platform roles
are the Grantra entitlements described in [access.md](access.md).

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
