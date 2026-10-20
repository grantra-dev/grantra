# Service accounts, AI agents and workloads

Programs hold access too, and usually more of it than the people who wrote them: a backup service account in Domain Admins, an AI agent reading every deal in the CRM, a billing service allowed to call the payments API. Nobody joins or leaves with them, so their access never gets cleaned up. Grantra governs them as identities of their own, each with an **owner** who answers for it.

The console page is **Governance → Service accounts and agents**; the in-console help is [docs/help/non-human-identities.md](help/non-human-identities.md).

## Kinds

| Kind | What it is | How it is recognised |
|---|---|---|
| `service` | A login a program uses in an application (`svc-backup` in Active Directory, an integration user in Salesforce) | Its accounts, linked to it |
| `agent` | An AI agent or automation acting in applications on the company's behalf | Its accounts, or its service token or API key in the applications it uses |
| `workload` | A service identified by a [SPIFFE](https://spiffe.io) ID, such as one SPIRE issues to a Kubernetes pod | Its SPIFFE ID (`spiffe://example.org/ns/prod/sa/billing`) |

They are rows in `identities` with that `kind`, so everything that works on an identity works on them: access assigned by hand or requested, eligibilities, accounts, reviews, attack paths, separation of duties and threat detection. A few things are only for people:

- **Birthright rules** only reach a non-human identity when they name its `kind` (`{"kind": ["service"], ...}`). A rule for everyone, or for a department, never gives a service account an employee's access.
- **Platform roles** (admin, helpdesk, auditor) are refused: programs call Grantra with [service tokens](service-tokens.md).
- **Console sign-in** never links to one, even if an account's email matched.
- **Peer comparisons** in review suggestions and the request catalog's team hints leave them out: each one does its own job.

## Owners

The owner is stored as the identity's `manager_id`, so they do what a manager does for a person, with no extra rules:

- Access review campaigns **by manager** send them its access to keep or revoke.
- Access requests for it go to them at the manager step, and they may request access for it.
- They can see it, its access and its accounts in the console.

The owner must be an active person. When the owner becomes a leaver (an HR sync, or their end date), each identity they owned passes to **their manager**, audited as `identity.owner_changed`, and admins and the new owner get the `non_human_owner_left` email. A leaver with no active manager leaves it with no owner, flagged for an admin.

## Flags

Each non-human identity that is not retired is flagged when:

| Flag | When |
|---|---|
| `no_owner` | Nobody owns it, or its owner is not active |
| `unvaulted` | One of its accounts holds high or critical risk access and has no vault account: nobody knows who has its password or when it last changed. Put it in the [vault](vault.md) to check out and rotate it |
| `unused` | Its accounts' last sign-in (Active Directory, OpenLDAP or an account file's `last_login`) or its sign-ins say it has not signed in for 90 days. When no application says, Grantra does not guess |
| `expiring` | Its end date is within 30 days |

## Lifecycle

- **Create** it with a kind, a name, an owner and optionally a purpose, a SPIFFE ID, an end date and the accounts that are its own. Accounts are linked to it by hand, so aggregations stop correlating them to people.
- **Change** its name, purpose, owner, SPIFFE ID or end date. Its kind stays.
- **End date**: on that day the daily end-date check retires it, as it does for people.
- **Retire** ends it today. It is a leaver: Grantra removes the access it gave and, where provisioning is on, disables its accounts. Retiring cannot be undone.

## Accounts nobody owns

`GET /api/v1/non-human-identities/candidates` lists active accounts outside Grantra's own application that no identity owns, at most 500: first the ones whose name looks like a program's (`svc`, `srv`, `service`, `sa`, `bot`, `agent`, `app`, `api`, `system`, `daemon`, `batch`, `cron`, `job`, `ci`, `deploy`, `automation`, `integration`, `sync` as a word), then the ones holding high or critical risk access. In the console, **Give it an owner** creates a non-human identity with that account.

## Workloads and SPIFFE

A workload's SPIFFE ID is unique in the tenant. Applications using [Grantra for authorization](app-authorization.md) name the workload by it:

```sh
curl -X POST https://grantra.example.com/api/v1/authz/check \
  -H "Authorization: Bearer grantra_ak_..." -H "Content-Type: application/json" \
  -d '{"principal": {"spiffe_id": "spiffe://example.org/ns/prod/sa/billing"},
       "action": "read", "resource": {"type": "Invoice", "id": "42"}}'
```

The application checks the SPIFFE ID itself (from the workload's X.509 SVID in mTLS, or a JWT SVID) before asking; Grantra answers what that workload may do. Policies see `principal.kind == "workload"` and `principal.spiffe_id`, and can tell programs and people apart:

```cedar
@id("agents never approve payments")
forbid (principal, action == Action::"approve", resource is Payment)
when { principal.kind == "agent" };
```

## API

| Endpoint | |
|---|---|
| `GET /api/v1/non-human-identities?kind=&flag=&q=&retired=true&limit=&after=` | `identity.list` (admins, helpdesk, auditors): `{"identities": [...]}`, each with its owner, `accounts`, `vaulted`, `risky_held`, `last_used_at` and `flags`, paged by `after=<last id>`. Retired ones only with `retired=true` |
| `GET /api/v1/non-human-identities/summary` | `identity.list`: how many are not retired, in `total` and per flag |
| `GET /api/v1/non-human-identities/candidates` | `catalog.read`: `{"accounts": [...], "limit": 500}` |
| `GET /api/v1/non-human-identities/{id}` | Whoever may see the identity: its owner, helpdesk, auditors, admins |
| `POST /api/v1/non-human-identities` | `identity.manage` (admins): `{"kind": "service", "name": "billing-sync", "owner_id": "...", "purpose": "...", "spiffe_id": "...", "end_date": "2027-03-31", "account_ids": ["..."]}` |
| `PATCH /api/v1/non-human-identities/{id}` | `identity.manage`: any of `name`, `purpose`, `owner_id`, `spiffe_id`, `end_date`; `""` clears purpose, SPIFFE ID or end date |
| `POST /api/v1/non-human-identities/{id}/retire` | `identity.manage`: `{"reason": "..."}` |

`GET /api/v1/identities?kind=human` or `?kind=non_human` narrows the identity list, and identities carry `purpose` and `spiffe_id`. Audit events: `identity.created`, `identity.updated`, `identity.retired`, `identity.owner_changed`.
