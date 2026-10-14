# Access requests

People ask for the access they need, and the right people approve it. An approved request becomes an assignment with origin `request` (see [access.md](access.md)). With provisioning on, Grantra then carries it to the application (see [connectors.md](connectors.md)).

## Asking

Anyone signed in can see what can be requested and ask for it, for themselves. Managers can also ask for their direct reports.

```sh
curl -b cookies localhost:8080/api/v1/requestable-access
curl -b cookies -X POST localhost:8080/api/v1/access-requests -d '{
  "entitlement_id": "<entitlement id>",
  "justification": "I join the Q3 pipeline review",
  "duration_days": 30
}'
```

| Field | Meaning |
|---|---|
| `role_id` or `entitlement_id` | What is asked for: a business role or one entitlement. Platform roles cannot be requested; they have their own guarded flow |
| `justification` | Required. Why the access is needed; approvers and auditors read it |
| `duration_days` | Optional, 1 to 365. The access ends on its own after that. Without it, the access lasts until someone removes it |
| `beneficiary_id` | Who gets the access, when it is not the requester. Only their manager (or an admin) can ask for someone else |

A request is refused when the person already has that access, already has an open request for it, or is not active.

Access that would break a `prevent` [separation of duties](separation-of-duties.md) policy can still be asked for, but it needs an admin to accept the risk (see [Separation of duties risk](#separation-of-duties-risk)). To warn people before they write a justification, `POST /api/v1/access-requests/preview` takes the same body without `justification` and answers the `prevent` policies the access would break, as `{"sod_conflicts": ["Payments"]}`. The console uses it on the request form.

## Approvals

Each request goes through up to two approvals, in order:

1. The **manager** of the person who would get the access.
2. The **owner** of the role, or of the entitlement's application.

How the chain is built:
- When the manager and the owner are the same person, they are asked once.
- Nobody approves their own access, not even an admin. A step whose approver would be the person getting the access goes to the admins instead.
- Steps with nobody to ask also go to the admins. That covers people without a manager, applications without an owner, and approvers who are no longer active.
- A step the requester would approve counts as approved. A manager asking for a report has already said yes by asking.
- Admins can decide any step. Every decision is audited with who made it.

A rejection closes the request, and later steps are skipped. Once every step is approved, the access is granted.

## Separation of duties risk

When the access would break a `prevent` separation of duties policy, the request gets one more step after the others: **risk**, decided by the admins. While it waits, the request lists the policies in `sod_conflicts`.

- **Accepting** the risk approves the step. It needs a reason (the `comment`: why the access is needed and how the risk is kept in check) and `exception_days`, 1 to 365:
  ```sh
  curl -b cookies -X POST localhost:8080/api/v1/access-requests/$ID/decision -d '{
    "decision": "approve",
    "comment": "two-person team; audit checks payments weekly",
    "exception_days": 30
  }'
  ```
  When the access is granted, each violation is recorded as an exception with that reason and end date, pointing to the request. When the exception ends, the violation reopens like any other (see [separation-of-duties.md](separation-of-duties.md#exceptions)).
- **Rejecting** it closes the request, like any other step.
- **Nobody accepts their own risk**, not even an admin. Another admin has to.
- **Policies can change while a request is open.** If a conflict appears after the request was made, the last approval does not grant the access: it adds the risk step, and the request waits for the admins. If the conflict goes away before the risk step is reached, the step is skipped.

## Timers

A background job runs every five minutes:
- **Expires** requests whose current step has waited more than 7 days without a decision.
- **Cancels** open requests for people who are no longer active, for example leavers.
- **Removes** assignments whose `expires_at` has passed (requests with a `duration_days`, or any time-bound assignment). Provisioning then takes the access out of the application.

The approval flow is kept as state in Postgres. A workflow engine (Temporal) will come in when longer, multi-step flows need it.

## Audit

The audit log records each request as it moves:
- `access_request.created`
- `access_request.step_approved` and `access_request.step_rejected`. On a risk step, the approval also records `exception_days` and the policies
- `access_request.risk_review`, when a conflict found at the last approval sends the request to the admins
- `access_request.approved`, `access_request.rejected`, `access_request.cancelled` and `access_request.expired`

The access itself is logged as `access.assigned` when it is granted and `access.removed` when it ends. Both name the `request_id`. An accepted risk is also logged as `sod.exception_granted`, with the `request_id`.

## API

| Endpoint | Who |
|---|---|
| `GET /api/v1/requestable-access` | Everyone |
| `POST /api/v1/access-requests` | For yourself; managers for their reports; admins |
| `POST /api/v1/access-requests/preview` | Same as asking |
| `GET /api/v1/access-requests?scope=mine\|approvals\|all&status=&limit=&after=` | `mine` (asked by you or for you, the default) and `approvals` (waiting on you; for admins, also the steps with nobody else to ask) for everyone; `all` for admins and auditors |
| `GET /api/v1/access-requests/{id}` | The requester, the person getting the access, every approver, admins and auditors |
| `POST /api/v1/access-requests/{id}/decision` | `{"decision": "approve" \| "reject", "comment": "...", "exception_days": 30}` (`exception_days` only to accept a risk): the approver of the current step, or an admin, never the person getting the access. Risk steps are for admins only |
| `POST /api/v1/access-requests/{id}/cancel` | The requester or the person getting the access, while the request is pending |

These permissions are Cedar policies in `core/internal/authz/policies/platform.cedar`.

Approvers see their queue with `scope=approvals`, and Grantra emails them when a step starts waiting for them and once more a day before it expires. The requester and the person the access is for get an email when the request is approved, rejected or expires. See [notifications](notifications.md).
