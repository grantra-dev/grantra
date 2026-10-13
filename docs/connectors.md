# Connectors

A connector is how Grantra talks to an application. This first version **reads**: an *aggregation* fetches the application's entitlements (groups, roles) and accounts, with who holds what, and stores them as the actual state next to the assignments (the desired state, see [access.md](access.md)). Writing changes back (provisioning) comes next.

## SCIM 2.0

The first connector speaks [SCIM 2.0](https://scim.cloud/), which Okta, Entra ID, Slack, GitHub Enterprise, Zoom and most SaaS tools offer.

```sh
export CRM_SCIM_TOKEN=...   # in the server's environment
curl -b cookies -X PUT localhost:8080/api/v1/applications/$APP/connector -d '{
  "kind": "scim",
  "config": {"base_url": "https://crm.example.com/scim/v2", "token": "env:CRM_SCIM_TOKEN"}
}'
curl -b cookies -X POST localhost:8080/api/v1/applications/$APP/aggregations
```

| Config | Meaning |
|---|---|
| `base_url` | The SCIM endpoint. Must be https; `allow_http: true` is for labs only |
| `token` | A **reference** to the bearer token, `env:NAME`. Secrets never go in the config, so the config is safe to show and audit |
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

## API

| Endpoint | Action |
|---|---|
| `PUT /api/v1/applications/{id}/connector` | `catalog.manage`: `{"kind": "scim", "config": {...}}`; `"kind": "manual"` removes it |
| `POST /api/v1/applications/{id}/aggregations` | `catalog.manage`: `200` with the run, `502` if the application could not be read |
| `GET /api/v1/applications/{id}/runs?limit=` | `catalog.read` |
| `GET /api/v1/applications/{id}/accounts?orphans=&after=&limit=` | `catalog.read` |
| `PUT /api/v1/accounts/{id}/identity` | `catalog.manage`: `{"identity_id": "..."}` or `null` |
| `GET /api/v1/identities/{id}/accounts` | `identity.read`: the person, their manager, helpdesk, auditors and admins |

## Writing a connector

Implement `connector.Reader` (`Read(ctx) (Snapshot, error)`) in a package under `core/internal/connector/`, register a `Factory` in `init`, and import the package in `cmd/grantra`. The factory must reject unknown config fields and resolve secrets through the `SecretResolver` it is given. `Read` must fail rather than return a partial snapshot. See `connector/scim` for an example with a fake server in its tests.
