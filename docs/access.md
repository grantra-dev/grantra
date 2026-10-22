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
| `manual` | Granted by hand |
| `request` | An approved [access request](access-requests.md); it can end on its own after a set number of days |

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

- Every attribute must match one of its values, ignoring case and surrounding spaces. Attributes come from HR (`department`, `title`, `location`, and any extra column). `kind` matches the identity kind (`human`, `service`, `agent`, `workload`). `{}` matches every person. [Non-human identities](non-human-identities.md) only match rules that name their `kind`, so a rule for everyone never gives a service account an employee's access.
- **Joiners** get matching access as soon as their sync is applied, already while `pending`, so it can be ready on day one. **Movers** gain what their new attributes match and lose birthright access they no longer match; manual and requested access stays. **Leavers** lose every assignment, whatever its origin. Suspended identities keep theirs.
- Creating, changing, disabling or deleting a rule applies to everyone at once. `POST /api/v1/access-rules/preview` shows who a set of conditions selects before saving, and `GET /api/v1/access-rules/attributes` lists the attributes people have with their most common values. The console's rule editor (Governance → Roles and rules, and the **Add application** wizard) uses both.
- Platform roles cannot be given by a rule.

The engine reads lifecycle events from the outbox (`lifecycle_events` with `processed_at` empty) right after each sync and every 30 seconds, so start and end dates applied between syncs also take effect. Every change is audited as `access.assigned` or `access.removed`, with the rule and the reason.

## Changing and retiring applications

Everything about an application is set from its page in the console (Governance → Applications): its owner (who approves requests for its access under the default approval flow), description, how often Grantra reads it (every hour by default, from once a week to only when someone asks), and the name, description and risk of each entitlement. Risk is what approval flows, the separation of duties risk step and Grantra Login's sensitive-access sign-in policy look at, so give the entitlements a connector brings in (all low at first) the risk they deserve.

**Delete** is for mistakes: an application with no accounts whose entitlements nobody holds, is assigned or eligible for, asked for, reviewed, or names in a role, birthright rule, separation of duties policy, vault account or remote access target. The same goes for an entitlement added by hand. Anything else has history the audit trail points at, so it is **retired** instead:

- nobody can ask for its access, and roles, birthright rules and eligibilities cannot take it in;
- Grantra stops reading it and provisioning turns off;
- what people still hold stays, visible in access reviews and on each person's page, until a review or an admin removes it.

It cannot be retired while a business role or birthright rule includes its access, or a request for it is still open, because they would keep handing it out: take it out of them, and decide or cancel the requests, first. **Bring back into service** undoes it; provisioning stays off until turned on again.

## API

All endpoints need a signed-in session. Writes need `grantra-admin`; `grantra-auditor` reads everything.

| Endpoint | Action |
|---|---|
| `GET` / `POST /api/v1/applications` | `catalog.read` / `catalog.manage`: `{"name", "owner_id"}` |
| `PUT /api/v1/applications/{id}` | `catalog.manage`: `{"name", "description", "owner_id", "read_every_hours"}`, audited with old and new values |
| `PUT /api/v1/applications/{id}/retired` | `catalog.manage`: `{"retired": true}` retires it, `false` brings it back (see below) |
| `DELETE /api/v1/applications/{id}` | `catalog.manage`: only an application without history (below); `409` otherwise |
| `GET` / `POST /api/v1/applications/{id}/entitlements` | `catalog.read` / `catalog.manage`: `{"name", "native_id", "description", "risk"}` |
| `PUT` / `DELETE /api/v1/entitlements/{id}` | `catalog.manage`: `{"name", "description", "risk"}`, audited with old and new values. The name of one read from a connected application comes from the application. Only an entitlement added by hand and without history can be deleted |
| `GET` / `POST /api/v1/roles`, `GET` / `PUT /api/v1/roles/{id}` | `catalog.read` / `catalog.manage`: `{"name", "description", "owner_id", "entitlement_ids"}` |
| `GET` / `POST /api/v1/access-rules`, `GET` / `PUT` / `DELETE /api/v1/access-rules/{id}` | `access_rule.read` / `access_rule.manage`. Writes answer with `changes: {added, removed}` |
| `POST /api/v1/access-rules/preview` | `access_rule.read`: `{"conditions": {...}}` → `{matches, sample}` |
| `GET /api/v1/access-rules/attributes` | `access_rule.read`: `{attributes: [{name, values}]}`, the HR attributes of people still here (and `kind`), most used first, with up to 50 values each |
| `GET /api/v1/identities/{id}/access` | `identity.read`: the person, their manager, helpdesk, auditors and admins |
