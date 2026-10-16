# Just-in-time access

Privileged access is safest when nobody holds it until they need it. Grantra
lets you mark an entitlement **just in time only**: people are made
**eligible** for it, and **activate** it for a few hours, with a reason, when
they need it. When the hours run out, the access goes away on its own and
provisioning takes it back from the application.

## Settings on an entitlement

In **Administration → Applications → (application) → Entitlements**, click
**Change** under *Held*, or call the API:

```sh
curl -b cookies -X PUT localhost:8080/api/v1/entitlements/$ID/jit \
  -d '{"jit_only": true, "max_activation_hours": 2, "activation_approval": false}'
```

| Setting | Meaning |
|---|---|
| `jit_only` | Nobody holds it with no end date. Requests for the access itself need a duration, and birthright rules cannot give it |
| `max_activation_hours` | The longest one activation lasts, 1 to 24 (default 4) |
| `activation_approval` | Each activation goes through the usual approval chain (manager, then owner) before it starts |

A business role takes the strictest of its entitlements: it is just in time
only if any of them is, its activations last at most the shortest maximum,
and they need approval if any entitlement asks for it.

Rules that keep standing privileges from coming back:

- The database refuses an assignment with no end date to just-in-time access, whatever path it comes from.
- An entitlement cannot become just in time only while a birthright rule gives it (directly or through a role); change the rule first.
- [Vault](vault.md) entitlements are always just in time only.

## Eligibility

Someone becomes eligible in one of two ways:

- An admin makes them eligible on their page in **People** (optionally for a number of days, up to 365). Admins never make themselves eligible.
- They ask for it in **Request access**: for just-in-time access the form offers *To be eligible* (the default) or *The access itself, once, for a set time*. An eligibility request goes through the same approvals as any request.

Eligibilities show in **My access** and on the person's page, and end on
their own when their days run out. Leavers lose them. Access reviews list
running activations; revoking one also removes the eligibility behind it.
Eligibilities with no activation running are not in reviews yet.

## Activating

In **My access**, click **Activate**, choose the hours (up to the maximum)
and say why. Only the person can activate their own eligibility.

- With no approval needed, the access starts at once (`access.activated`). Provisioning carries it to the application within about 30 seconds. An activation never lasts past the eligibility's own end.
- Memberships the person already had in the application, found by reading it, become Grantra's when they activate or their standing access is converted, so provisioning takes them away when the activation ends even if the application keeps memberships Grantra did not grant.
- When the entitlement asks for approval, or the access would break a [separation of duties](separation-of-duties.md) policy, it becomes an *activation* request; the access starts for the chosen hours once it is approved.
- **Done, end it now** ends it early; admins can end anyone's.
- The job **apply just-in-time access** runs every 30 seconds: it removes activations whose hours have run out (`access.removed` with reason `expired`) and eligibilities whose days have, and provisions those changes.

## Standing access report

**Administration → Standing access** lists high and critical risk access held
with no end date, and any just-in-time only access still held from before it
became so. **Make it just in time** removes the access and makes the
person eligible instead, keeping the entitlement's settings. Birthright
access cannot be converted there (change the rule), and admins cannot
convert their own.

## API

| Endpoint | Who |
|---|---|
| `GET /api/v1/identities/{id}/eligibilities` | Whoever may see the person (themselves, their manager, admins, helpdesk, auditors) |
| `POST /api/v1/identities/{id}/eligibilities` | `access.manage` (admins, never for themselves): `{"role_id" \| "entitlement_id", "duration_days"}` |
| `DELETE /api/v1/eligibilities/{id}` | `access.manage`. Also ends an activation in progress |
| `POST /api/v1/eligibilities/{id}/activations` | `access.activate`: only the person. `{"hours", "justification"}`; `201` with `{"activation": {...}}`, or `{"request": {...}}` when it needs approval |
| `POST /api/v1/eligibilities/{id}/deactivate` | The person, or `access.manage` |
| `PUT /api/v1/entitlements/{id}/jit` | `catalog.manage` (admins) |
| `GET /api/v1/standing-access?limit=` | `catalog.read` (admins, auditors) |
| `POST /api/v1/assignments/{id}/eligibility` | `access.manage`, never on your own access |

Access requests take `"kind": "access" | "eligibility"` and, besides
`duration_days`, `duration_hours` (1 to 24). Activation requests show
`"kind": "activation"`.

Audit events: `access.eligible`, `access.eligibility_removed`,
`access.activated`, `access.removed`, `entitlement.jit_updated`.
