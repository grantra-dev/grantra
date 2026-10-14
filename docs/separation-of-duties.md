# Separation of duties

Separation of duties (SoD) keeps any one person from holding access that should be split between people. A classic example is creating payments and also approving them. In Grantra, a **policy** has two sides, each a set of business roles and entitlements. Anyone who holds something from side A and something from side B **violates** the policy.

## What counts as holding

- **Assignments**: an entitlement held directly, a business role, or the entitlements inside that role (see [access.md](access.md)).
- **What the applications really have**: the memberships of the person's active accounts, as found by the last aggregation (see [connectors.md](connectors.md)). This catches access granted outside Grantra too.

## Policies

```sh
curl -b cookies -X POST localhost:8080/api/v1/sod-policies -d '{
  "name": "Payments",
  "severity": "critical",
  "mode": "prevent",
  "side_a": [{"entitlement_id": "<create payments>"}],
  "side_b": [{"entitlement_id": "<approve payments>"}, {"role_id": "<finance approver>"}]
}'
```

| Field | Meaning |
|---|---|
| `side_a`, `side_b` | Business roles (`role_id`) and entitlements (`entitlement_id`). Nothing can be on both sides |
| `mode` | `detect` (default) reports violations. `prevent` also stops [access requests](access-requests.md) that would create one: they are granted only once an admin accepts the risk on the request itself |
| `severity` | `low`, `medium`, `high` (default) or `critical` |
| `enabled` | Default `true`. A disabled policy finds and prevents nothing; its open violations resolve at the next scan |

Platform role entitlements can be used too, for example to keep the same person from being both admin and auditor.

## Violations

A scan runs every 30 minutes, and admins can also start one with `POST /api/v1/sod-violations/scan`. Each scan does three things:
- Records every new person who holds both sides of a policy (`sod.violation_detected`).
- Marks violations that no longer apply as `resolved` (`sod.violation_resolved`), for example once someone lost one side.
- Reopens exceptions whose time is up (`sod.exception_expired`).

When a scan records new violations or reopens exceptions, the admins get one email listing them. See [notifications](notifications.md).

## Exceptions

Sometimes the risk is accepted, for example in a small team or while someone covers a leave. An admin turns an open violation into an exception with a reason (required) and an optional number of days (0 means no end date):

```sh
curl -b cookies -X POST localhost:8080/api/v1/sod-violations/$ID/exception \
  -d '{"reason": "two-person team; payments checked weekly by audit", "days": 60}'
```

- Nobody accepts their own violation.
- While the exception lasts, `prevent` policies let that person's requests through.
- Exceptions are audited as `sod.exception_granted`.

An admin can also accept the risk **before** the access exists, on the access request that would create the violation: see [access-requests.md](access-requests.md#separation-of-duties-risk). That exception needs an end date (up to 365 days), and the violation carries the `request_id` it was accepted on.

## API

| Endpoint | Who |
|---|---|
| `GET /api/v1/sod-policies`, `GET /api/v1/sod-policies/{id}` | Admins and auditors |
| `POST /api/v1/sod-policies`, `PUT` and `DELETE /api/v1/sod-policies/{id}` | Admins |
| `GET /api/v1/sod-violations?status=open\|excepted\|resolved&policy_id=&limit=&after=` | Admins and auditors. Exceptions accepted on a request carry its `request_id` |
| `POST /api/v1/sod-violations/scan` | Admins |
| `POST /api/v1/sod-violations/{id}/exception` | Admins: `{"reason": "...", "days": 30}` |
