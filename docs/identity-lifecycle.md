# Identity lifecycle (joiner / mover / leaver)

Grantra learns who works at the company from an **identity source**: the HR system, which is the authority on who joined, who changed jobs and who left. Every sync compares the source with the identities it owns and turns the differences into lifecycle events. Later stages (birthright access, deprovisioning, access reviews) act on those events.

A source is fed in one of two ways:

- **CSV exports**, which almost every HR system can produce, sent by a person in the console or by a nightly job.
- **An HR system Grantra reads itself**: [Workday](#workday) or [BambooHR](#bamboohr). Grantra reads it every `every_hours` (24 by default) and on **Check now** in the console. What it reads goes through exactly the same rules as a file: the column mapping, every check, the leaver limit and dry runs.

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

Any other column is kept as an attribute of the identity, under its name in snake_case. See [`deploy/samples/hr-employees.csv`](../deploy/samples/hr-employees.csv). [CSV files](help/csv-files.md) has every rule of the format, a template and what each error means.

## HR systems Grantra reads itself

In the console: **Settings → HR sources → New HR source**, then pick the system under **People come from** and fill in how to reach it. Type the password or API key in: Grantra keeps it encrypted with the [vault key](vault.md), as it does for [connector secrets](connectors.md#secrets), or give an `env:GRANTRA_SECRET_…` reference. Grantra checks the settings when you save. Then **Check now** shows what would change, and **Apply** applies it; after that Grantra reads the system on its own.

The config of these sources has two more keys:

| Key | Meaning |
|---|---|
| `connection` | How to reach the system, below |
| `every_hours` | How often Grantra reads it on its own, 1 to 168. Default 24. It counts from the last run that was not a dry run |

An HR system lists everyone, so people missing from it become leavers (`missing` stays `leave`). A read that fails (a wrong password, the service down, a report that answers nobody) is recorded as an `invalid` run with the reason and changes nothing; Grantra tries again at the next `every_hours`.

### Workday

Grantra reads a **custom report shared as a web service** (Reports as a Service, in JSON), so the report decides which fields Grantra sees and it works with any Workday tenant.

1. Create an integration system user (ISU) for Grantra, with a security group that can see the workers' job data.
2. Create an **Advanced** custom report on workers, with one row per worker, current and terminated (so leavers show up), and **Enable as Web Service**. Give each column a name like Grantra's fields (`employee_id`, `email`, `manager_id`, `department`, `status`, `start_date`, `end_date`…), or map the names in the source's columns. A date with its time zone (`2026-10-01-07:00`) becomes `2026-10-01`. Share it with the ISU.
3. Copy the report's JSON web service address (**Actions → Web Service → View URLs**).

| Setting | Meaning |
|---|---|
| `report_url` | The report's address: `https://<host>/ccx/service/customreport2/<tenant>/<owner>/<report>`. Other query parameters, such as report prompts, are kept; `format=json` is added |
| `username` | The ISU, such as `ISU_Grantra` |
| `password` | Its password: a `secret:` or `env:` reference |

```json
{"name": "Workday", "kind": "workday", "config": {"connection": {
  "report_url": "https://wd2-impl-services1.workday.com/ccx/service/customreport2/acme/ISU_Grantra/Grantra_Workers",
  "username": "ISU_Grantra", "password": "env:GRANTRA_SECRET_WORKDAY"}}}
```

Columns that hold several values (lists, groups of fields) are left out: give the report single values.

### BambooHR

Grantra runs a custom report through the BambooHR API, former employees included, and names the fields itself, so there is nothing to map.

1. In BambooHR, create an API key for a user who can see every employee's job information (**Account → API keys**). An admin's, or a user with an access level made for Grantra.
2. Give Grantra the company's subdomain and the key.

| Setting | Meaning |
|---|---|
| `company` | `acme` for `acme.bamboohr.com` |
| `api_key` | The API key: a `secret:` or `env:` reference |

| BambooHR field | Grantra field |
|---|---|
| Employee # (`employeeNumber`), or BambooHR's own id when empty | `employee_id` |
| First name, last name, display name | `first_name`, `last_name`, `display_name` |
| Work email | `email` |
| Supervisor (`supervisorEId`) | `manager_id` |
| Department, job title, location, division | `department`, `title`, `location`, `division` (an attribute) |
| Status (`Active`, `Inactive`) | `status` |
| Hire date, termination date | `start_date`, `end_date` |

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
| `POST /api/v1/identity-sources` | `identity_source.manage`: `{"name": "HR", "kind": "csv", "config": {...}}`. `kind` is `csv`, `workday` or `bamboohr`; the kind cannot change later |
| `GET /api/v1/identity-sources/{id}` | `identity_source.read` |
| `PUT /api/v1/identity-sources/{id}` | `identity_source.manage`: `{"name": "HR", "config": {...}}`, the whole config. The next sync uses it |
| `GET /api/v1/identity-sources/{id}/runs?limit=` | `identity_source.read` |
| `POST /api/v1/identity-sources/{id}/runs?dry_run=&force=` | `identity_source.sync`: the CSV file is the body (32 MiB max). `200` planned or applied, `422` invalid file, `409` leaver limit |
| `POST /api/v1/identity-sources/{id}/pulls?dry_run=&force=` | `identity_source.sync`: reads the source's HR system now. Same answers, and `422` also when it could not be read |
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
