# Authorization for your apps

Your company's own apps can ask Grantra "may this person do this?" instead of coding the rules themselves. Each application in the catalog has [Cedar](https://www.cedarpolicy.com/) policies, written in the console, and Grantra answers with them, filling in the person from what it already knows: their department and title, their manager, their business roles and their entitlements in that application. When someone moves team, gets access through a request or leaves, the answers change with no change in the app.

## Setting it up

Open the application in **Governance** → **Applications**, then the **Authorization** tab. Admins change things; auditors can look.

1. **Policies**: write the rules. **Start from an example** fills in a sample. With no policies, every question is denied.
2. **Try it**: ask a question as the app would and see the answer, the policies that decided, and what the policies see of the person. It uses the policies being edited, so you can test before saving.
3. **API keys**: create a key for each backend (or environment) that asks. The key is shown once; Grantra keeps only a hash of it. Revoke a key and apps using it get 401 straight away. A key only gets answers from its own application's policies.

Saving policies and creating or revoking keys are in the audit log (`app_authz.*`). Answers are not audited one by one, since apps may ask on every request.

## Asking

```sh
curl -X POST https://grantra.example.com/api/v1/authz/check \
  -H "Authorization: Bearer grantra_ak_..." \
  -H "Content-Type: application/json" \
  -d '{
    "principal": {"email": "ana@example.com"},
    "action": "approve",
    "resource": {"type": "Invoice", "id": "42", "attributes": {"amount": 1200, "submitter": "luis@example.com"}},
    "context": {"mfa": true}
  }'
```

```json
{"allowed": true, "policies": ["finance approves invoices"]}
```

| Field | What it is |
|---|---|
| `principal` | The person, by one of `id` (Grantra's), `email`, `external_id` (employee id) or `username` (their account in this application) |
| `action` | What they want to do, in the app's own words: becomes `Action::"approve"` |
| `resource` | What they want to do it to: `type` and `id` become `Invoice::"42"`, and `attributes` are the resource's attributes |
| `context` | Anything else the policies look at, such as whether the person used MFA or the time of day |

`attributes` and `context` are JSON objects of [Cedar values](https://docs.cedarpolicy.com/auth/entities-syntax.html): strings, whole numbers, booleans, lists, objects, and `{"__entity": {"type": "Invoice", "id": "7"}}` for entities. The answer has `allowed`, the ids of the `policies` that decided (the permits that allowed it, or the forbids that denied it), any policy `errors`, and a `reason` when Grantra denied before asking the policies: `unknown_person` when nobody matches the principal, `person_not_active` for people who are not active (leavers, suspended people and people who have not started yet are always denied).

The endpoint needs the console to be on (`GRANTRA_OIDC_ISSUER`); a missing or revoked key gets 401, a malformed question 400.

## What the policies see

The principal is a `Person` entity:

| Attribute | Value |
|---|---|
| `email`, `display_name` | From the person in Grantra |
| `status` | `active` (others never reach the policies) |
| `kind` | `human`, `service` or `agent` |
| `department`, `title`, `location`... | Every attribute the HR source sends, also under `attributes` |
| `manager` | The manager, as an entity with an `email` |
| `roles` | Names of the business roles assigned to the person |
| `entitlements` | Native ids of this application's entitlements the person holds in it or is assigned in Grantra (directly or through a role) |

An attribute the HR source does not send for someone is missing: check it with `has` (`principal has department && principal.department == "Finance"`), or the policy errors and Cedar skips it.

## Writing policies

```cedar
@id("finance approves invoices")
permit (principal, action == Action::"approve", resource is Invoice)
when {
  principal.entitlements.contains("approver") &&
  principal.department == "Finance" &&
  resource.amount < 10000
};

@id("nobody approves their own invoice")
forbid (principal, action == Action::"approve", resource is Invoice)
when { resource has submitter && resource.submitter == principal.email };

@id("managers see their team's reports")
permit (principal, action == Action::"view", resource is Report)
when { resource has owner_manager && resource.owner_manager == principal.email };
```

Cedar denies unless a policy permits, and any matching `forbid` wins over every `permit`. `@id` names a policy in answers; policies without one are `policy0`, `policy1`... by position. `Person` and `Action` are taken; any other type name (`Invoice`, `Billing::Invoice`) is the app's.
