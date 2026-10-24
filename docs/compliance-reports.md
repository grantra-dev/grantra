# Compliance reports and evidence packs

**Governance → Reports** turns what Grantra already records into the evidence auditors ask for in SOX, ISO 27001, SOC 2 and NIS2 audits: who reviewed what, how fast leavers lost their access, who approved each request, separation of duties, privileged access, who holds what, admin changes and identity threats. Admins and auditors (the `grantra-auditor` platform role) see it; only admins change the frameworks.

Nothing new is captured for it: every report reads the tables Grantra keeps anyway, so it covers everything since Grantra was installed.

## The reports

| Report | Tables | Period | One application |
|---|---|---|---|
| **Access reviews** (`reviews`) | Campaigns open in the period, with counts; every decision: person, access, reviewer, decision, who decided and when, comment, and for revoked access whether it is still held (in Grantra) and still in the application (as last read) | yes | yes |
| **Leavers and orphan accounts** (`leavers`) | Every leaver in the period: last day in HR, when Grantra recorded the leaver, accounts still active, when the last account was closed, hours it took and whether that was within the target. Active accounts now that match nobody or belong to a leaver, with whether they hold high or critical risk access | yes | yes |
| **Access requests and approvals** (`requests`) | Every request made in the period: who asked, for whom, what, why, for how long, the approval policy and its version, status, who approved, and whether a separation of duties risk was accepted. Every step: who was asked, how many approvals it needed, who approved and when, escalations | yes | yes |
| **Separation of duties** (`sod`) | The policies and their two sides; every violation open at some point of the period, with its exception (who accepted it, why, until when, on which request) | yes | no |
| **Privileged access** (`privileged`) | Just-in-time activations (from the audit log, so expired ones too), vault check-outs and break-glass uses with their reviews, and the SSH and gateway sessions with whether they were recorded | yes | yes (activations and check-outs) |
| **Who has access to what** (`access`) | Every membership as applications last reported it, Grantra's platform roles included, with whether Grantra gave it; and every assignment in Grantra with how it was given | now | yes |
| **Who had access on a date** (`access_history`) | Every membership held at the end of the period, with when it started (and ended, if it did since), every membership that started or ended in the period, and when Grantra started recording | yes | yes |
| **Administration changes** (`admin`) | Who holds the admin, helpdesk and auditor roles now, and every change to how Grantra is set up in the period (settings, policies, applications, connectors, roles, rules, tokens, modules, Grantra Login, frameworks…), refused attempts included | yes | no |
| **Identity threats and non-human identities** (`threats`) | Threat alerts open in the period with how long they took to close, the responses taken, and service accounts, agents and workloads with no owner, an owner who left, or past their end date | yes | no |

Periods are whole days in UTC, both ends included, at most three years. A report "for now" ignores the dates, which only name the period the evidence is for. Each report opens with a few counts (leavers closed in time, revoked access still held, break-glass uses nobody reviewed…), shows the first 100 rows of each table, and downloads them all.

**Who had access on a date** reads the history Grantra keeps of each membership (an active account holding an entitlement, as the application last reported it): Grantra checks every 15 minutes what started and what ended, and keeps memberships that ended for three years by default. The history starts when Grantra is updated to the version that brought it; memberships it found then count from when the application reported them first, and changes before that are not known.

A table with more than 100,000 rows asks for a shorter period or one application, rather than handing out a list cut short.

## Downloads

- **CSV** for each table: UTF-8 with a byte order mark so Excel opens it as it is, times as `YYYY-MM-DD HH:MM:SS` in UTC. A cell that starts like a formula (`=`, `+`, `-`, `@`) gets a `'` in front, so a spreadsheet shows it instead of running it.
- **Printable summary**: an HTML page with the counts and tables (the first 500 rows of each), made to print or **save as PDF** from the browser. It loads nothing from anywhere.
- **Evidence pack** (`.zip`): a CSV for each table under a folder named after the report, `summary.html`, and `manifest.json`, which says what the pack covers, who downloaded it and when, and the SHA-256 and size of every other file.

Every download goes to the audit log as `report.generated`, with the report or framework, the period, the format and the SHA-256 of the file (of `manifest.json` for a pack).

### Verifying a file

**Verify a file** (or `POST /api/v1/report-verifications` with the file as the body, up to 200 MB) says whether a file came out of this Grantra unchanged. For a pack it checks every file against the manifest (changed, missing or added files are listed) and that the audit log recorded handing out that manifest; for a single CSV or summary, that the audit log recorded handing out that file. An auditor can also check a pack by hand: `sha256sum` each file and compare it with the manifest.

The audit log cannot be edited or deleted through Grantra (a trigger refuses it), but someone with direct database access could add a row; keep database access as tight as the audit itself requires.

## Frameworks

A framework is a list of controls, each pointing to the reports that give its evidence. Grantra comes with four, as a suggested mapping to adjust to your own control matrix:

| Framework | Controls |
|---|---|
| SOX (IT general controls) | ITGC-AC-01 to 06: access approved, leavers removed on time, periodic reviews, privileged access, segregation of duties, population of user access (now and at period end) |
| ISO/IEC 27001:2022 | A.5.3, A.5.15, A.5.16, A.5.18, A.8.2, A.8.15, A.8.16 |
| SOC 2 | CC6.1, CC6.2, CC6.3, CC7.2, CC8.1 |
| NIS2 | Article 21(2)(b), (f) and (i) |

Admins can turn a framework off, rename its controls to their own ids (`ITGC-04`), change what each one asks and which reports cover it, add and remove controls, and bring a built-in one back to its defaults. They can also **add a framework** of their own (DORA, ENS, an internal policy). Every change is in the audit log as `report_framework.saved`, `report_framework.reset` or `report_framework.deleted`.

A framework's **evidence pack** has every report its controls point to, for the period, plus `controls.csv` (each control with its reports and their folders) and a summary that starts with the controls.

## Scheduled packs

**Scheduled** makes an evidence pack on its own as each period ends: the pack of one report (for all applications or one) or of a framework, every month, quarter, half year or year, or any number of months up to 12, counted from the first month of the fiscal year. Within 15 minutes of midnight UTC after the period's last day, Grantra makes the pack, records its manifest's SHA-256 in the audit log (`report.generated`, by `system:reports`, with the schedule), keeps it, and emails the people the schedule names (`report_ready`) a link to download it. Each schedule makes one pack per period. A new schedule makes the pack of the last full period on its next pass.

Packs go only to admins and auditors. Someone who loses both roles stays on the schedule, crossed out, and gets nothing until they have one again. Everyone who can see Reports can download the packs; each download is in the audit log (`report.downloaded`). A pack that cannot be made (a framework without controls, a table over the row limit, an application that was deleted) is listed as failed, with why, the recipients are told, and it is not tried again for that period. Packs are deleted after 400 days by default; their fingerprints stay in the audit log, so a copy someone kept can still be verified.

Every change to a schedule is in the audit log (`report_schedule.saved`, `report_schedule.deleted`).

## External auditors

**External auditors** lets an admin invite someone from outside the company, such as the audit firm's team, until a last day at most 90 days ahead (a setting). Grantra adds them as a person, sponsored by the admin who invited them, and gives them the auditor role until the midnight after their last day: they read every report, framework, scheduled pack and the audit log, and change nothing. With **Give them a Grantra Login account**, Grantra Login emails them a link to set their password; without Grantra Login, they sign in the way the company does (its identity provider must know them).

- Their last day can move, earlier or later, within the same limit counted from today.
- The day after their last day, they become a leaver, as when an HR end date passes; **End now** does it at once. Either way the role goes, their sessions end, and Grantra disables their Grantra Login account (whether provisioning is on or not).
- Birthright rules never give them anything unless a rule names the `grantra_guest` attribute, and requests they make go to whoever invited them.
- Every step is in the audit log (`external_auditor.invited`, `.extended`, `.ended`, `.login_created`, `.login_disabled`), with the auditor role granted and revoked as for anyone.


Under **Settings → General → Compliance reports** ([settings](settings.md)):

| Setting | Key | Default | What it does |
|---|---|---|---|
| Time to close a leaver's accounts | `reports.leaver_sla_hours` | 24 hours | The target the leavers report checks. Making it longer waits for a second admin |
| Period reports cover | `reports.period_months` | 3 months | Reports open on the last full period this long |
| Longest access for an external auditor | `reports.external_auditor_max_days` | 90 days | How far ahead an external auditor's last day can be. Making it longer waits for a second admin |
| Keep scheduled packs for | `reports.delivery_keep_days` | 400 days | How long scheduled packs can be downloaded again |
| Keep the history of who held what for | `reports.access_history_keep_days` | 1095 days | How long memberships that ended stay in the history |
| First month of the fiscal year | `reports.fiscal_year_start_month` | 1 (January) | Where periods start: with 4 and 3 months, quarters run April–June, July–September… |

How "within target" is worked out: hours count from when Grantra recorded the leaver to the last account it disabled or membership it removed. Someone with accounts still active is "open, in time" until the target passes, then "no, still open"; someone who was rehired since shows as "came back". Accounts closed outside Grantra (in the application itself) count as closed once Grantra reads them, without a time.

## API

All endpoints need an admin or auditor session, and changes an admin one; the three downloads also take a [service token](service-tokens.md) with the `report.read` scope, for a job that collects evidence every month.

| Endpoint | What it does |
|---|---|
| `GET /api/v1/reports` | The reports, their tables and columns, and the default period (`default_from`, `default_to`) |
| `GET /api/v1/reports/{id}?from=&to=&application=&format=` | Runs a report. `format=json` (the default) is a preview: counts and the first 100 rows of each table, with `row_count`. `csv&table=<name>` is one table, `html` the printable summary, `zip` the evidence pack. Without dates, the default period |
| `GET /api/v1/report-frameworks` | The frameworks as the tenant has them |
| `POST /api/v1/report-frameworks` | Adds one: `{"name": "DORA", "description": "", "enabled": true, "controls": [{"id": "Art. 9", "title": "Protection and prevention", "reports": ["access", "privileged"]}]}` |
| `PUT /api/v1/report-frameworks/{id}` | Changes one; a built-in one keeps its name and description |
| `DELETE /api/v1/report-frameworks/{id}` | Removes one the tenant added, or brings a built-in one back to its defaults |
| `GET /api/v1/report-frameworks/{id}/pack?from=&to=` | The framework's evidence pack |
| `POST /api/v1/report-verifications` | Verifies a downloaded file, sent as the body |
| `GET /api/v1/report-schedules` | The schedules, each with its last pack and the period of its next one |
| `POST /api/v1/report-schedules` | Adds one: `{"name": "SOC 2 for the auditors", "framework": "soc2", "months": 3, "recipients": ["<identity id>"], "enabled": true}`, or `"report": "leavers"` with an optional `"application_id"` |
| `PUT` / `DELETE /api/v1/report-schedules/{id}` | Changes or removes one; its packs stay |
| `GET /api/v1/report-deliveries?schedule=` | The packs schedules made, newest first |
| `GET /api/v1/report-deliveries/{id}/file` | Downloads one |
| `GET /api/v1/external-auditors` | The external auditors, those with access first |
| `POST /api/v1/external-auditors` | Invites one: `{"name": "Ines Ruiz", "email": "ines@auditfirm.com", "company": "Audit firm", "until": "2026-12-31", "login": true}`. The answer says in `login_error` why no Grantra Login account was made, if one was asked for |
| `PUT /api/v1/external-auditors/{id}` | Moves their last day: `{"until": "2027-01-31"}` |
| `DELETE /api/v1/external-auditors/{id}` | Ends their access now |

```sh
curl -H "Authorization: Bearer $GRANTRA_TOKEN" -o soc2-q3.zip \
  "https://grantra.example.com/api/v1/report-frameworks/soc2/pack?from=2026-07-01&to=2026-09-30"
```
