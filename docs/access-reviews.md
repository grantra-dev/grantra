# Access reviews

An access review, or certification, asks the right people whether others still need the access they have. Auditors expect to see one regularly. In Grantra a review runs as a **campaign**: it takes a snapshot of the access in scope, and a reviewer either **keeps** or **revokes** each item.

## What is reviewed

A campaign covers the assignments (see [access.md](access.md)) given **by hand** or by an approved **request** (see [access-requests.md](access-requests.md)):
- With `application_ids`, it covers the entitlements of those applications.
- Without them, it covers everything, business roles included.

Birthright access is left out, because rules give it and take it back. For birthright access, the thing to review is the rule itself. Platform roles are reviewed under platform roles.

## Who reviews

| `reviewer` | Reviewer of each item |
|---|---|
| `manager` (default) | The person's manager |
| `owner` | The owner of the role, or of the entitlement's application |

Items go to the **admins** when there is nobody to ask: no manager or owner, a reviewer who is no longer active, or a reviewer who would be reviewing their own access. Nobody reviews their own access, not even an admin. Admins can decide any other item.

## Decisions

- **Keep**: the access stays.
- **Revoke**: the assignment is removed right away. If the application is provisioned, Grantra then takes the access out of it (see [connectors.md](connectors.md)).

A comment is optional. Every decision is audited as `review_item.decided`; a revocation is also audited as `access.removed`, with the reason `revoked in access review`.

## End of a campaign

A campaign ends when every item is decided, when an admin closes it, or when `due_in_days` runs out (default 14, at most 90). Whatever is still pending then follows `on_missing`:
- `keep` (default) keeps it.
- `revoke` removes it, for teams that want "use it or lose it".

Either way the decision is recorded as made by `system:reviews`, with the comment "not reviewed before the campaign ended". A background job checks every fifteen minutes for campaigns past their due date.

## API

| Endpoint | Who |
|---|---|
| `POST /api/v1/review-campaigns` | Admins: `{"name": "Q4 review", "reviewer": "manager", "application_ids": [], "on_missing": "keep", "due_in_days": 14}` |
| `GET /api/v1/review-campaigns`, `GET /api/v1/review-campaigns/{id}` | Admins and auditors. Each campaign comes with its progress (items by decision) |
| `POST /api/v1/review-campaigns/{id}/close` | Admins |
| `GET /api/v1/review-items?scope=mine\|all&campaign_id=&decision=&limit=&after=` | `mine` (the default) is what you review in active campaigns; for admins it also includes the items with nobody else to ask. `all` is for admins and auditors |
| `POST /api/v1/review-items/{id}/decision` | The item's reviewer, or an admin, never the person whose access it is: `{"decision": "keep" \| "revoke", "comment": "..."}` |

Reviewers are not notified yet. They see their queue with `scope=mine`.
