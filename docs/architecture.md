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
| Events / workflows | NATS JetStream and Temporal (to be added when flows need them; access request approvals run as Postgres state) |
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
| `GRANTRA_DEMO` | `false` | Loads the sample company of [demo.md](demo.md) on start when the tenant has no HR source. Never for real data |
| `GRANTRA_DEMO_LOGIN_URL` | | With `GRANTRA_DEMO`, also adds Grantra Login as an application read through the [`grantra-login` connector](connectors.md#grantra-login) at this URL, with the secret in `GRANTRA_SECRET_GRANTRA_LOGIN` |

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
| `GET /api/v1/me/notifications`, `PUT /api/v1/me/notifications/{kind}` | Your own email settings, see [notifications.md](notifications.md). No policy: everyone manages their own |
| `GET /api/v1/identities?limit=&after=&q=&status=` | `identity.list`. `q` matches name or email, ignoring case |
| `GET /api/v1/identities/{id}` | `identity.read` |
| `GET /api/v1/audit-events?limit=&before=&identity=` | `audit.read`. `identity` keeps what that identity did or had done to it; events name the identities behind actor and target |
| `GET /api/v1/platform-roles` | `platform_role.read` (roles with their current members) |
| `PUT /api/v1/platform-roles/{role}/members/{identity}` | `platform_role.grant`, optional body `{"expires_at": "..."}` |
| `DELETE /api/v1/platform-roles/{role}/members/{identity}` | `platform_role.revoke` |
| `/api/v1/identity-sources`, `/api/v1/lifecycle-events` | HR sources and joiner/mover/leaver events, see [identity-lifecycle.md](identity-lifecycle.md) |
| `/api/v1/applications`, `/api/v1/roles`, `/api/v1/access-rules`, `/api/v1/identities/{id}/access` | Catalog, birthright rules and assigned access, see [access.md](access.md) |
| `/api/v1/applications/{id}/connector`, `/aggregations`, `/accounts`, `/provisioning`, `/provisioning-runs`, `/api/v1/accounts/{id}/identity` | Connectors, aggregation, correlation and provisioning, see [connectors.md](connectors.md) |
| `/api/v1/requestable-access`, `/api/v1/access-requests` | Access requests and approvals, see [access-requests.md](access-requests.md) |
| `/api/v1/review-campaigns`, `/api/v1/review-items` | Access reviews, see [access-reviews.md](access-reviews.md) |
| `/api/v1/sod-policies`, `/api/v1/sod-violations` | Separation of duties, see [separation-of-duties.md](separation-of-duties.md) |
| `/api/v1/vault/...` | Credential vault: accounts, check-outs, break glass, rotation and discovery, see [vault.md](vault.md) |
| `/api/v1/ssh/...` | SSH targets, certificates, gateway sessions, recordings and commands, see [ssh.md](ssh.md) |
| `/api/v1/identities/{id}/eligibilities`, `/api/v1/eligibilities/...`, `/api/v1/entitlements/{id}/jit`, `/api/v1/standing-access`, `/api/v1/assignments/{id}/eligibility` | Just-in-time access, see [just-in-time.md](just-in-time.md) |

## Identity lifecycle

HR is the authority on who works at the company. An **identity source** (a CSV export today) is synced into identities, and every difference becomes a joiner, mover, leaver or rehire event in `lifecycle_events`, the outbox later stages consume. Syncs are all or nothing, can be dry-run, and refuse to offboard more than a set share of people at once. Details in [identity-lifecycle.md](identity-lifecycle.md).

## Assigned access

An **assignment** says an identity should have a business role or an entitlement, and why (`birthright`, `manual` or `request`). It is the desired state; accounts are the actual state, reconciled by connectors. **Birthright rules** give access by HR attributes and take it back when people stop matching; leavers lose everything. The engine consumes the lifecycle outbox. Details in [access.md](access.md).

## Connectors

Connectors read applications (SCIM 2.0, and LDAP for Active Directory and OpenLDAP): an aggregation stores their entitlements and accounts, and correlation links accounts to identities by employee id or email; the rest are orphans. Secrets stay out of the database as `env:` references. Details in [connectors.md](connectors.md).

## Tenant isolation

Every tenant-owned table has a `tenant_id` column and a row-level security policy. The server sets the tenant for each transaction:

```sql
SET LOCAL app.tenant_id = '<tenant uuid>';
```

Rows from other tenants are invisible and cannot be written. Superusers and roles with `BYPASSRLS` skip these policies, so **the server must connect as an ordinary role** (`grantra_app` in the dev stack).

## Notifications

Flows queue emails in an outbox table inside their own transaction; a background sender delivers them over SMTP and retries failures, so mail never blocks or undoes a flow. See [notifications.md](notifications.md).

## Audit log

`audit_events` rejects `UPDATE` and `DELETE` through a trigger. Shipping events to external storage (SIEM, object storage with retention lock) is planned.
