# Tenant settings

Time limits, reminders and thresholds that used to be built into Grantra are set per tenant in the console, under **Settings → General**. Every setting starts with a default that suits most companies, has a lowest and highest value Grantra accepts, and is in the audit log when it changes (`setting.changed`, with the old and new value).

## Four eyes for less strict changes

Some settings make Grantra less strict in one direction: a longer break-glass check-out, a shorter generated password, a longer console session. When a tenant has more than one admin, such a change does not take effect at once: it waits for another admin, who approves or rejects it from their Inbox or from the setting itself, and gets an email (`setting_change_proposed`). The admin who proposed it may withdraw it; nobody approves their own. Only one change per setting waits at a time.

Stricter changes, and changes to settings that are neither, take effect at once. A tenant with a single admin changes everything at once, and the audit event says no second admin was there (`no_second_admin`).

## Settings

| Group | Setting | Key | Default | Range | Less strict when |
|---|---|---|---|---|---|
| Access requests | Time to decide each step | `requests.step_timeout_hours` | 168 hours | 24 to 720 | |
| | Remind approvers before a step expires | `requests.reminder_hours` | 24 hours | 1 to 168 | |
| | Smallest team whose access is counted | `requests.team_min_people` | 3 people | 2 to 50 | lower |
| Access reviews | Time reviewers get by default | `reviews.default_due_days` | 14 days | 1 to 90 | |
| | Remind reviewers before a campaign ends | `reviews.reminder_hours` | 48 hours | 1 to 336 | |
| Separation of duties | Longest exception | `sod.exception_max_days` | 365 days | 1 to 365 | higher |
| | Allow exceptions with no end date | `sod.exceptions_without_end` | on | on or off | on |
| Privileged access | Longest break-glass check-out | `vault.break_glass_minutes` | 60 minutes | 15 to 480 | higher |
| | Remind admins of break-glass uses to review | `vault.break_glass_reminder_hours` | 24 hours | 1 to 168 | |
| | Length of generated passwords | `vault.password_length` | 24 characters | 16 to 128 | lower |
| Threat detection | How long "Require MFA" lasts | `itdr.require_mfa_days` | 7 days | 1 to 90 | lower |
| | Keep sign-ins for | `itdr.sign_in_retention_days` | 400 days | 366 to 1825 | lower |
| Insights | Unused after | `insights.unused_days` | 90 days | 30 to 730 | |
| | Smallest group for a suggested role | `insights.role_min_people` | 5 people | 2 to 100 | |
| | Share of a group that must hold it | `insights.role_min_share_percent` | 80% | 50 to 100 | |
| | Fewest entitlements in a suggested role | `insights.role_min_entitlements` | 2 | 2 to 20 | |
| | Peers needed to call access common | `insights.common_min_peers` | 3 people | 2 to 50 | |
| | Peers needed to call access rare | `insights.rare_min_peers` | 5 people | 3 to 100 | |
| | Non-human identities expiring soon | `nhi.expiring_days` | 30 days | 1 to 180 | |
| Compliance reports | Time to close a leaver's accounts | `reports.leaver_sla_hours` | 24 hours | 1 to 720 | higher |
| | Period reports cover | `reports.period_months` | 3 months | 1 to 12 | |
| | First month of the fiscal year | `reports.fiscal_year_start_month` | 1 (January) | 1 to 12 | |
| | Longest access for an external auditor | `reports.external_auditor_max_days` | 90 days | 1 to 365 | higher |
| | Keep scheduled packs for | `reports.delivery_keep_days` | 400 days | 30 to 3650 | |
| | Keep the history of who held what for | `reports.access_history_keep_days` | 1095 days | 400 to 3650 | |
| Console sign-in | Sign out after inactivity | `console.session_idle_minutes` | 30 minutes | 5 to 480 | higher |
| | Longest console session | `console.session_max_hours` | 8 hours | 1 to 24 | higher |

Settings of a module the tenant turned off (privileged access, threat detection) are hidden. A change applies from then on. A request step, break-glass check-out or console session already running keeps the end it was given when it started; reminders, insights, reports and the inactivity limit read the setting each time.

## Limits set by the operator

`GRANTRA_SESSION_IDLE` and `GRANTRA_SESSION_MAX` are no longer the session lengths themselves: when set, they are the longest a tenant may pick (in whole minutes and whole hours), and a tenant's default comes down to them if it is longer. Unset, the ranges above apply.

## API

| Endpoint | What it does |
|---|---|
| `GET /api/v1/settings` | Every setting with its value, default, range and the change waiting on it, and whether a less strict change by you would wait for another admin. Admins and auditors |
| `PUT /api/v1/settings/{key}` `{"value": 120, "reason": "CHG-1234"}` | Change a setting. Answers 200 with the setting when it took effect, 202 when it waits for another admin. Admins |
| `POST /api/v1/setting-changes/{id}/decision` `{"approve": true, "comment": ""}` | Approve or reject a waiting change; the admin who proposed it rejects it to withdraw it. Admins |

`GET /api/v1/me` gives everyone the settings the console needs to explain what it shows (today, how many days count as unused).

## Adding a setting

A new value that a tenant may want to change is a `settings.Definition` in `core/internal/settings/registry.go`, never a constant: its key, group, unit, default, range and, when it has one, the direction that makes Grantra less strict. Code reads it with `settings.Get` (or `settings.Values` for several) inside the tenant's transaction, or inlines it in SQL with `settings.SQL(key)`, which already applies the default and the range. The console page shows it with no other change.
