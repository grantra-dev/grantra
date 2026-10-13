# Architecture

Grantra starts as a **modular monolith** in Go: one binary with well-separated modules (identity, connectors, governance, policy, audit). Services get split out only when there is a concrete reason. Connectors will run as separate workers from day one so they can scale independently.

## Decisions

| Topic | Decision |
|---|---|
| License | Apache 2.0 |
| Backend | Go |
| Database | PostgreSQL, with row-level security for tenant isolation |
| Policy engine | Cedar (embedded via `cedar-go`), optional OPA integration |
| Identity provider | No home-grown IdP. Keycloak ships preconfigured and can be replaced by the customer's IdP |
| Events / workflows | NATS JetStream and Temporal (to be added when the first workflows land) |
| Frontend | React + TypeScript |

## Core data model

- **Tenant**: an isolated customer or organization.
- **Identity**: a person or non-human principal (service account, AI agent) that Grantra governs.
- **Application**: a system Grantra connects to (Entra ID, LDAP, GitHub, AWS…).
- **Account**: a login inside an application, linked to an identity. Accounts with no identity are *orphans*.
- **Entitlement**: a permission inside an application (a group, a policy, a repo role).
- **Role**: a business role bundling entitlements across applications.
- **Audit event**: an append-only record of everything that happens.

## Console login

People sign in through OpenID Connect, either with **Grantra Login** (the bundled Keycloak, realm `grantra`) or with the company's own IdP (Entra ID, Okta...). Grantra never stores passwords.

- **Flow**: authorization code with PKCE, a `nonce` checked in the ID token, and a short-lived cookie that ties the callback to the browser that started the login. Grantra is a confidential client.
- **Who you are**: a login is linked to an identity by `(issuer, subject)` in `identity_logins`. The first login links by *verified* email; later ones go by subject. Logins never create identities: someone the IdP knows but Grantra does not gets a 403.
- **Sessions**: server-side, in `sessions`. The browser holds only an opaque `HttpOnly`, `SameSite=Lax` cookie; the database holds its SHA-256. Sessions end after `GRANTRA_SESSION_IDLE` of inactivity (30 min), after `GRANTRA_SESSION_MAX` (8 h), on logout (which also ends the IdP session), or as soon as the identity stops being `active`, so offboarding cuts access at the next request.
- **Audit**: `auth.login`, `auth.login_denied` and `auth.logout` land in `audit_events`.

| Variable | Default | Purpose |
|---|---|---|
| `GRANTRA_PUBLIC_URL` | `http://localhost:8080` | Browser-facing URL; builds the callback URL. Cookies are `Secure` when it is https |
| `GRANTRA_OIDC_ISSUER` | (none: login off) | Issuer URL, exactly as in tokens |
| `GRANTRA_OIDC_DISCOVERY_URL` | the issuer | Where to fetch provider metadata when it differs (e.g. inside docker-compose) |
| `GRANTRA_OIDC_CLIENT_ID` / `_SECRET` | `grantra-console` / none | Client registered at the IdP |
| `GRANTRA_TENANT` / `GRANTRA_TENANT_NAME` | `default` / `Default` | The single tenant of a self-hosted install, created on start |
| `GRANTRA_BOOTSTRAP_EMAIL` | none | Creates a first active identity with this email when the tenant has none, and makes it admin while the tenant has no active admin |

## Console authorization

Every console API request is a question for the policy engine: *may this principal perform this action on this resource?* The answer comes from [Cedar](https://www.cedarpolicy.com/) policies evaluated in-process with `cedar-go`. Nothing is allowed unless a `permit` matches, and any matching `forbid` wins.

- **Policies** live in `core/internal/authz/policies/*.cedar`, each with a unique `@id`. Tests validate them against `schema.cedarschema`, so a typo in an attribute or action fails CI.
- **Platform roles** (`grantra-admin`, `grantra-helpdesk`, `grantra-auditor`) are entitlements of a built-in application named **Grantra** (connector `grantra`) that every tenant gets on start. Holding a role means having an active account in that application with the role's entitlement, so platform roles are governed like any other access: they can expire (`account_entitlements.expires_at`) and will be requested, approved and reviewed through the same flows. In Cedar the identity becomes a member of `Role::"<name>"`.
- **Relationships need no assignment.** You can see your direct reports because their `manager_id` points at you; nobody grants "manager".
- **Guards outside Cedar**: the last active admin cannot be revoked, roles go only to active identities, and refused grants and revokes land in the audit log as `authz.denied`.
- **First admin**: when `GRANTRA_BOOTSTRAP_EMAIL` is set and the tenant has no active admin, the active identity with that email becomes admin on start (audited as `system:bootstrap`). Unset it once real admins exist if you do not want it as a recovery path.

| Role | Can |
|---|---|
| Everyone | Read their own identity; managers read their direct reports |
| `grantra-helpdesk` | Look up and list identities. Cannot grant anything |
| `grantra-auditor` | Read everything (identities, audit log, platform role members). Changes nothing |
| `grantra-admin` | Everything, except granting a platform role to themselves |

### Console API

All endpoints need a signed-in session and answer JSON. `403` means the policies said no.

| Endpoint | Action |
|---|---|
| `GET /api/v1/me` | Who you are, your platform roles, and whether you manage people or own applications |
| `GET /api/v1/identities?limit=&after=` | `identity.list` |
| `GET /api/v1/identities/{id}` | `identity.read` |
| `GET /api/v1/audit-events?limit=&before=` | `audit.read` |
| `GET /api/v1/platform-roles` | `platform_role.read` (roles with their current members) |
| `PUT /api/v1/platform-roles/{role}/members/{identity}` | `platform_role.grant`, optional body `{"expires_at": "..."}` |
| `DELETE /api/v1/platform-roles/{role}/members/{identity}` | `platform_role.revoke` |
| `/api/v1/identity-sources`, `/api/v1/lifecycle-events` | HR sources and joiner/mover/leaver events, see [identity-lifecycle.md](identity-lifecycle.md) |
| `/api/v1/applications`, `/api/v1/roles`, `/api/v1/access-rules`, `/api/v1/identities/{id}/access` | Catalog, birthright rules and assigned access, see [access.md](access.md) |

## Identity lifecycle

HR is the authority on who works at the company. An **identity source** (a CSV export today) is synced into identities, and every difference becomes a joiner, mover, leaver or rehire event in `lifecycle_events`, the outbox later stages consume. Syncs are all or nothing, can be dry-run, and refuse to offboard more than a set share of people at once. Details in [identity-lifecycle.md](identity-lifecycle.md).

## Assigned access

An **assignment** says an identity should have a business role or an entitlement, and why (`birthright`, `manual` or `request`). It is the desired state; accounts are the actual state, reconciled by connectors. **Birthright rules** give access by HR attributes and take it back when people stop matching; leavers lose everything. The engine consumes the lifecycle outbox. Details in [access.md](access.md).

## Tenant isolation

Every tenant-owned table has a `tenant_id` column and a row-level security policy. The server sets the tenant for each transaction:

```sql
SET LOCAL app.tenant_id = '<tenant uuid>';
```

Rows from other tenants are invisible and cannot be written. Superusers and roles with `BYPASSRLS` skip these policies, so **the server must connect as an ordinary role** (`grantra_app` in the dev stack).

## Audit log

`audit_events` rejects `UPDATE` and `DELETE` through a trigger. Shipping events to external storage (SIEM, object storage with retention lock) is planned.
