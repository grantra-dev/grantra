# Connectors

A connector is how Grantra talks to an application. An *aggregation* **reads** the application's entitlements (groups, roles) and accounts, with who holds what, and stores them as the actual state next to the assignments (the desired state, see [access.md](access.md)). *Provisioning* **writes**: it changes the application so the actual state matches the desired one.

## SCIM 2.0

The first connector speaks [SCIM 2.0](https://scim.cloud/), which Okta, Entra ID, Slack, GitHub Enterprise, Zoom and most SaaS tools offer.

```sh
export GRANTRA_SECRET_CRM_SCIM_TOKEN=...   # in the server's environment
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/connector -d '{
  "kind": "scim",
  "config": {"base_url": "https://crm.example.com/scim/v2", "token": "env:GRANTRA_SECRET_CRM_SCIM_TOKEN"}
}'
curl -b cookies -X POST localhost:8080/api/v1/applications/$APP/aggregations
```

| Config | Meaning |
|---|---|
| `base_url` | The SCIM endpoint. Must be https; `allow_http: true` is for labs only |
| `token` | A **reference** to the bearer token, `env:NAME`, where `NAME` must start with `GRANTRA_SECRET_`. Secrets never go in the config, so the config is safe to show and audit. The prefix keeps a connector config from naming the server's own settings (such as `GRANTRA_DATABASE_URL`) and sending them to an outside URL |
| `page_size` | Resources per request, default 100 |

Groups become entitlements. Membership comes from each group's `members` and each user's `groups`. Users bring `userName`, primary email, `externalId` (or the enterprise `employeeNumber`), `active`, `title` and `department`.

The config is checked when it is saved (URL, token reference present in the environment), so mistakes show up before the first aggregation. Only admins configure connectors; they point the server at URLs of their choosing, so treat that permission accordingly.

## Aggregation

- Runs on demand (`POST /aggregations`) and on its own once an hour for every application with a connector.
- Each run is recorded in `connector_runs` with what changed (new accounts, orphans, membership changes...) and audited as `application.aggregated`.
- **A failed read changes nothing.** Neither does a read that returns no accounts where there were some last time: that looks like a broken service, not a company that deleted every user. The API answers `502` and the run is recorded as failed.
- Accounts no longer returned become `deleted`; entitlements no longer returned get `missing_since`. History is kept.

## Correlation

Each account is matched to the identity it belongs to, first match wins:

1. `externalId` equals the identity's employee id (`rule:external_id`).
2. The account email equals the identity's email (`rule:email`).
3. The username is an email that equals the identity's email (`rule:username`).

A match must be unique: an email two people share links nobody. Accounts that match nobody are **orphans**, listed with `GET /accounts?orphans=true`. An admin can link an account by hand (`PUT /api/v1/accounts/{id}/identity`), or mark it as nobody's (a shared or service login) with `{"identity_id": null}`. Manual decisions survive every aggregation.

## Provisioning

Provisioning compares what people should hold in an application (their assignments, direct or through business roles) with what the last aggregation found, and fixes the difference through the connector:

| Action | When |
|---|---|
| `create_account` | An active person should have access and has no account. The username is their email; people without an email are skipped and counted as `skipped_no_email`. If the application already has an account with that username that correlation did not link, creation fails until someone links it by hand |
| `enable_account` | An active person should have access and their account is disabled |
| `add_member` | An active person should hold an entitlement and none of their accounts does. It goes on their main account (the active one, else the oldest) |
| `remove_member` | Someone holds an entitlement nobody assigned them, **and** Grantra granted it itself, or the application has `remove_unassigned` on. Leavers, suspended people and people who have not started should hold nothing |
| `disable_account` | The person left or is suspended. Accounts are disabled, never deleted |

Orphan accounts are never touched: whose they are is for a person to decide (see Correlation).

### Turning it on

Provisioning is **off** for every application until an admin turns it on:

```sh
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/provisioning -d '{"mode": "on"}'
curl -b cookies -X POST "localhost:8080/api/v1/applications/$APP/provisioning-runs?dry_run=true"   # see the plan
curl -b cookies -X POST localhost:8080/api/v1/applications/$APP/provisioning-runs                  # do it
```

| Setting | Meaning |
|---|---|
| `mode` | `off` (default: Grantra only reads) or `on`. Turning it on needs a connector that can write |
| `remove_unassigned` | Default `false`: Grantra only removes memberships it granted itself, so turning provisioning on never strips access people got outside Grantra. Set it to `true` once the assignments describe everyone's access |
| `max_change_percent` | Default 10. A run that would remove or disable more than this share of the application's accounts is refused; up to three such changes always pass |

### Safety

- **Dry runs** (`?dry_run=true`) show every action and change nothing. They work while provisioning is off, to see what turning it on would do.
- **Plans start from what is really there.** A run needs a successful aggregation of the application in the last 24 hours; otherwise the API answers `409` and asks for one.
- **Big runs are refused** past `max_change_percent` (`409`, recorded as `refused` with its plan). Check the plan, then repeat with `?force=true` if it is right.
- **One run at a time per application.** A run locks the application, so aggregations and other runs of it wait.
- **Failures don't stop the run.** Each action is tried and recorded with its outcome; the run is `succeeded`, `partial` or `failed` (`502` for the last two). The next run tries again what is still missing.
- **Everything is audited**: one `provisioning.<action>` event per action with its outcome, and `application.provisioned` per run. Runs and their actions are in `connector_runs`.
- Accounts Grantra creates are linked to their person as `provisioned`, and memberships it adds are marked `provisioned_at`. Both survive aggregations.

With provisioning on, Grantra provisions every application on its own every five minutes; runs with nothing to do are not recorded.

### SCIM

The SCIM connector writes with `POST /Users` (`userName`, `displayName`, primary work email, `externalId`), `PATCH /Users/{id}` (`active`) and `PATCH /Groups/{id}` (`add` / `remove` `members`). The token needs write access to users and groups.

## API

| Endpoint | Action |
|---|---|
| `PUT /api/v1/applications/{id}/connector` | `catalog.manage`: `{"kind": "scim", "config": {...}}`; `"kind": "manual"` removes it |
| `POST /api/v1/applications/{id}/aggregations` | `catalog.manage`: `200` with the run, `502` if the application could not be read |
| `PUT /api/v1/applications/{id}/provisioning` | `catalog.manage`: `{"mode": "on", "remove_unassigned": false, "max_change_percent": 10}` |
| `POST /api/v1/applications/{id}/provisioning-runs?dry_run=&force=` | `catalog.manage`: `200` with the run and its actions, `409` when refused or not possible yet, `502` when actions failed |
| `GET /api/v1/applications/{id}/runs?limit=` | `catalog.read`: aggregation and provisioning runs |
| `GET /api/v1/applications/{id}/accounts?orphans=&after=&limit=` | `catalog.read` |
| `PUT /api/v1/accounts/{id}/identity` | `catalog.manage`: `{"identity_id": "..."}` or `null` |
| `GET /api/v1/identities/{id}/accounts` | `identity.read`: the person, their manager, helpdesk, auditors and admins |

## Writing a connector

Implement `connector.Reader` (`Read(ctx) (Snapshot, error)`) in a package under `core/internal/connector/`, register a `Factory` in `init`, and import the package in `cmd/grantra`. The factory must reject unknown config fields and resolve secrets through the `SecretResolver` it is given. `Read` must fail rather than return a partial snapshot. To provision, also implement `connector.Writer` (`CreateAccount`, `SetActive`, `AddMember`, `RemoveMember`); all but `CreateAccount` must be safe to repeat. See `connector/scim` for an example with a fake server in its tests.
