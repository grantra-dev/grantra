# Identity lifecycle (joiner / mover / leaver)

Grantra learns who works at the company from an **identity source**: the HR system, which is the authority on who joined, who changed jobs and who left. Every sync compares the source with the identities it owns and turns the differences into lifecycle events. Later stages (birthright access, deprovisioning, access reviews) act on those events.

The first kind of source is a **CSV export**, which almost every HR system can produce. Workday, BambooHR and SAP SuccessFactors connectors come later and reuse the same engine.

## The CSV file

One row per person, with a header row. Column names are matched loosely (`Employee ID` and `employee_id` are the same), and a source can map its own names with `config.columns`: in the console, **New HR source** or **Edit** has a box per field, and a sample file shows which fields it already has.

| Column | Required | Meaning |
|---|---|---|
| `employee_id` | yes | The person's stable id in HR. Never reuse one |
| `display_name`, or `first_name` + `last_name` | yes | |
| `email` | | Work email; also used once to link people who already exist in Grantra |
| `manager_id` | | The manager's `employee_id`. Managers may appear anywhere in the file |
| `department`, `title`, `location` | | Job attributes. A change in any of them, or of manager, makes the person a **mover** |
| `status` | | `active` (default) or `terminated` (also `inactive`, `left`) |
| `start_date`, `end_date` | | `YYYY-MM-DD`. A future start keeps the person `pending`; the day after `end_date` they are a leaver |

Any other column is kept as an attribute of the identity, under its name in snake_case. See [`deploy/samples/hr-employees.csv`](../deploy/samples/hr-employees.csv).

## What a sync does

| Situation | Result |
|---|---|
| New employee id | **Joiner**: a new identity, `pending` until its start date, then `active` |
| Department, title, location or manager changed | **Mover** |
| `status` terminated, `end_date` passed, or no longer in the file | **Leaver**: the identity becomes `terminated` and its console sessions end |
| A terminated person is back | **Rehire** |
| Name, email or other attributes changed | Update, no lifecycle event |

- **Linking.** The first sync takes over identities that no source owns, matching by employee id or by email, so the admin created at install does not end up duplicated.
- **Suspensions stick.** An identity suspended in Grantra stays suspended while HR says active; termination and rehire always follow HR.
- **Dates between syncs.** The server applies start and end dates every 15 minutes, so a daily export is enough.
- **Time zone.** Dates are compared with the current UTC date.

## Safety rails

A broken export must never offboard the company.

- **All or nothing.** A file with any error (duplicate employee ids, bad dates, unknown status, a missing column) applies nothing. The run is recorded as `invalid` with every error and its line.
- **Leaver limit.** A sync that would turn more than `max_leaver_percent` (default 10%) of the source's active people into leavers is `refused`. Up to three leavers always pass. If it is real, run it again with `force=true`.
- **Dry run.** `dry_run=true` returns exactly what would change, without changing it.
- **Feeds of changes only.** Set `"missing": "ignore"` when the file does not list everyone, so absent people are left alone.

Every run is stored in `identity_source_runs`, each change lands in `audit_events` (`identity.joined`, `identity.moved`, `identity.left`, `identity.rehired`, `identity.updated`, `identity.activated`) and lifecycle events go to `lifecycle_events`. Events start with `processed_at` empty: that table is the outbox the access engine consumes to give and take [birthright access](access.md).

## API

All endpoints need a signed-in session, except syncs, which a script can also send with a [service token](service-tokens.md) holding `identity_source.sync`. Creating and syncing sources needs `grantra-admin`; `grantra-auditor` can read everything.

| Endpoint | Action |
|---|---|
| `GET /api/v1/identity-sources` | `identity_source.read`: sources with their latest run |
| `POST /api/v1/identity-sources` | `identity_source.manage`: `{"name": "HR", "kind": "csv", "config": {...}}` |
| `GET /api/v1/identity-sources/{id}` | `identity_source.read` |
| `PUT /api/v1/identity-sources/{id}` | `identity_source.manage`: `{"name": "HR", "config": {...}}`, the whole config. The next sync uses it |
| `GET /api/v1/identity-sources/{id}/runs?limit=` | `identity_source.read` |
| `POST /api/v1/identity-sources/{id}/runs?dry_run=&force=` | `identity_source.sync`: the CSV file is the body (32 MiB max). `200` planned or applied, `422` invalid file, `409` leaver limit |
| `GET /api/v1/lifecycle-events?limit=&before=&identity=` | `lifecycle_event.read` |

```sh
# Create the source once, then feed it the export.
curl -b cookies -X POST localhost:8080/api/v1/identity-sources \
  -d '{"name": "HR", "kind": "csv"}'
curl -b cookies -X POST "localhost:8080/api/v1/identity-sources/$SOURCE/runs?dry_run=true" \
  --data-binary @deploy/samples/hr-employees.csv

# A nightly job does the same with a service token instead of a session.
curl -X POST -H "Authorization: Bearer $GRANTRA_TOKEN" -H 'Content-Type: text/csv' \
  --data-binary @export.csv "https://grantra.example.com/api/v1/identity-sources/$SOURCE/runs"
```

Source config:

```json
{
  "columns": {"employee_id": "Worker ID", "department": "Org Unit"},
  "missing": "leave",
  "max_leaver_percent": 10
}
```
