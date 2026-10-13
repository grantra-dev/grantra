# Catalog, assignments and birthright access

## Catalog

The catalog is what access exists:

- **Applications**: systems people get access to. Applications created from the console use the `manual` connector: Grantra decides who should have what, and someone carries it out by hand until a connector is set up. The built-in **Grantra** application holds the platform roles and is managed under platform roles only.
- **Entitlements**: permissions inside an application (a group, a license, a repo role), each with a `native_id` (its id in the application) and a risk level (`low`, `medium`, `high`, `critical`).
- **Business roles**: bundles of entitlements across applications ("Sales rep" = CRM user + Sales Slack channels). Changing a role's entitlements changes them for everyone who holds the role.

## Assignments

An **assignment** says an identity *should* have a role or an entitlement, and why:

| Origin | Meaning |
|---|---|
| `birthright` | A birthright rule gives it, while the person matches the rule |
| `manual` | Granted by hand (coming with access requests) |
| `request` | Approved access request (coming next) |

The same access can be held for several reasons at once (two rules, a rule and a request); each is its own assignment, and losing one reason keeps the others. Assignments are the desired state. Accounts and their entitlements are the actual state in each application; connectors will reconcile one with the other.

## Birthright rules

A rule gives roles and entitlements to everyone whose attributes match its conditions:

```json
{
  "name": "Sales in Madrid",
  "conditions": {"department": ["Sales"], "location": ["Madrid", "Barcelona"]},
  "role_ids": ["<role id>"],
  "entitlement_ids": ["<entitlement id>"]
}
```

- Every attribute must match one of its values, ignoring case and surrounding spaces. Attributes come from HR (`department`, `title`, `location`, and any extra column). `kind` matches the identity kind (`human`, `service`, `agent`). `{}` matches everyone.
- **Joiners** get matching access as soon as their sync is applied, already while `pending`, so it can be ready on day one. **Movers** gain what their new attributes match and lose birthright access they no longer match; manual and requested access stays. **Leavers** lose every assignment, whatever its origin. Suspended identities keep theirs.
- Creating, changing, disabling or deleting a rule applies to everyone at once. `POST /api/v1/access-rules/preview` shows who a set of conditions selects before saving.
- Platform roles cannot be given by a rule.

The engine reads lifecycle events from the outbox (`lifecycle_events` with `processed_at` empty) right after each sync and every 30 seconds, so start and end dates applied between syncs also take effect. Every change is audited as `access.assigned` or `access.removed`, with the rule and the reason.

## API

All endpoints need a signed-in session. Writes need `grantra-admin`; `grantra-auditor` reads everything.

| Endpoint | Action |
|---|---|
| `GET` / `POST /api/v1/applications` | `catalog.read` / `catalog.manage`: `{"name", "owner_id"}` |
| `GET` / `POST /api/v1/applications/{id}/entitlements` | `catalog.read` / `catalog.manage`: `{"name", "native_id", "description", "risk"}` |
| `GET` / `POST /api/v1/roles`, `GET` / `PUT /api/v1/roles/{id}` | `catalog.read` / `catalog.manage`: `{"name", "description", "owner_id", "entitlement_ids"}` |
| `GET` / `POST /api/v1/access-rules`, `GET` / `PUT` / `DELETE /api/v1/access-rules/{id}` | `access_rule.read` / `access_rule.manage`. Writes answer with `changes: {added, removed}` |
| `POST /api/v1/access-rules/preview` | `access_rule.read`: `{"conditions": {...}}` → `{matches, sample}` |
| `GET /api/v1/identities/{id}/access` | `identity.read`: the person, their manager, helpdesk, auditors and admins |
