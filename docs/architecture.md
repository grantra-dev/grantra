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

## Tenant isolation

Every tenant-owned table has a `tenant_id` column and a row-level security policy. The server sets the tenant for each transaction:

```sql
SET LOCAL app.tenant_id = '<tenant uuid>';
```

Rows from other tenants are invisible and cannot be written. Superusers and roles with `BYPASSRLS` skip these policies, so **the server must connect as an ordinary role** (`grantra_app` in the dev stack).

## Audit log

`audit_events` rejects `UPDATE` and `DELETE` through a trigger. Shipping events to external storage (SIEM, object storage with retention lock) is planned.
