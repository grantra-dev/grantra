# Approval flows

Who approves an [access request](access-requests.md) is set up per tenant in the console, under **Governance → Approval flows**. Admins write approval policies: when each applies and who approves, in order. Without any, Grantra uses the built-in flow: the person's manager, then the owner of the role or of the entitlement's application.

## How a request picks its flow

Policies are kept in an order. When someone asks for access, Grantra tries the enabled policies from the top, and the first one whose conditions all hold decides the steps. When none does, the built-in flow does. To change what happens when nothing else fits, add a policy with no conditions at the end.

The steps are worked out when the request is made, with the people they ask, and kept on the request with the policy's name and version. Changing or deleting a policy later does not change requests already open.

## Conditions

Every condition a policy sets must hold. A condition left empty matches everything.

| Condition | Matches when |
|---|---|
| `kinds` | The request is for the access itself (`access`), to be made eligible (`eligibility`) or to activate [just-in-time](just-in-time.md) access (`activation`) |
| `application_ids` | The entitlement is in one of these applications. A role matches when any of its entitlements is |
| `role_ids` | The request is for one of these business roles |
| `risks` | The access is `low`, `medium`, `high` or `critical`. A role is as risky as its riskiest entitlement |
| `departments` | The person the access is for is in one of these departments, as HR names them (case does not matter) |
| `identity_kinds` | The access is for a person (`human`), a service account, an AI agent or a workload ([non-human identities](non-human-identities.md)) |
| `longer_than_days` | The access is asked for with no end, or for longer than this many days |

## Steps

A policy has up to 10 steps, decided one after the other.

| Field | Meaning |
|---|---|
| `approver` | `manager`, `owner`, `people` (named in the policy, up to 20), `holders` (the people who hold a business role, such as "IT security") or `admin` (any Grantra admin) |
| `level` | For `manager`: 1 is the manager, 2 the manager's manager, up to 5 |
| `people_ids` | For `people` |
| `role_id` | For `holders` |
| `required` | How many of the step's approvers must approve (1 when not set) |
| `all` | Every one of them must approve |
| `timeout_hours` | How long the step waits, 1 to 720 hours. Without it, the tenant's *time to decide each request step* ([settings](settings.md)) |
| `on_timeout` | What happens when nobody decides in time: `expire` (the default) closes the request as expired, `reject` rejects it, `escalate` asks the approvers' managers once, with the same time again (the admins when they have none) |
| `label` | The name people see for the step, such as "Payroll leads" |

Approvals already given count after an escalation: the managers give the rest, as many as there are of them. Admins can still decide any step: an admin's approval counts as one of the approvals a step needs, and one rejection, by an approver or an admin, rejects the request.

## What no policy changes

- **Nobody approves their own access**, not even an admin. The person the access is for drops out of every step; a step with nobody left goes to the admins.
- **Steps with nobody to ask go to the admins**: people without a manager, applications without an owner, approvers who left.
- **Asking counts as approving.** When the requester is one of a step's approvers, for example a manager asking for a report, their approval is given by asking.
- **The same approvers are not asked twice in a row.** When a step would ask the same people as the one before it and needs no more approvals, it is left out.
- **Only low risk access can go with no step.** A policy with no steps must be limited to `low` risk. If it still has none for riskier access, an admin step is added.
- **Separation of duties risks are decided by an admin**, after the policy's steps (see [Separation of duties risk](access-requests.md#separation-of-duties-risk)).

## Trying a policy

The page has a simulator: pick a person, what they would ask for and for how long, and it shows who would approve it, step by step, with the policy that decides and why a step differs from what the policy says. While editing a policy, the simulator tries the edited version first, before it is saved.

The request form shows the same plan as people fill it in (**Who approves it**), and every request shows its flow: who approved each step and when, who it waits on now and until when, escalations, and the policy it followed. On **My access** and in a person's access, **How it was approved** opens the flow of the request that granted it.

## API

| Endpoint | Who |
|---|---|
| `GET /api/v1/approval-policies` | Admins and auditors. Answers `{"policies": [...], "builtin": {...}}` |
| `POST /api/v1/approval-policies` | Admins. A new policy goes last |
| `PUT /api/v1/approval-policies/{id}` | Admins. Each change adds one to `version` |
| `DELETE /api/v1/approval-policies/{id}` | Admins |
| `PUT /api/v1/approval-policies/order` | Admins. `{"ids": [...]}`, naming every policy |
| `POST /api/v1/approval-policies/simulate` | Admins and auditors. The body of a request (`beneficiary_id` required, no `justification`), optional `requester_id` and a draft `policy`. Answers `{"plan": {...}, "sod_conflicts": [...]}` |

```sh
curl -b cookies -X POST localhost:8080/api/v1/approval-policies -d '{
  "name": "Payroll",
  "conditions": {"application_ids": ["<payroll app id>"], "risks": ["high", "critical"]},
  "steps": [
    {"approver": "manager", "timeout_hours": 48, "on_timeout": "escalate"},
    {"approver": "people", "label": "Payroll leads", "people_ids": ["<id>", "<id>"], "all": true}
  ]
}'
```

Saving, deleting and reordering are audited with the whole policy (`approval_policy.saved`, `approval_policy.deleted`, `approval_policy.reordered`). A request records the policy it followed in `access_request.created`; a partial approval is logged as `access_request.approval_added`, and an escalation as `access_request.step_escalated`.
