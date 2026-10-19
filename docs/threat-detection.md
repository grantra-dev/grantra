# Identity threat detection and response

Grantra already knows who should have which access, who left and who is an admin. Threat detection (ITDR) adds what people actually do with their identities, sign-ins first, and raises **alerts** when that does not fit: a leaver who still signs in, a password being guessed, a second factor being pushed until someone accepts, high-risk access granted behind Grantra's back. Admins **respond** from the alert: end sessions, require a second factor, suspend. Some detections may respond on their own.

It is the `itdr` module: a company that does not want it turns it off in **Settings → Modules** and Grantra stops reading and looking. Who can reach the riskiest access, and how, is in [access-insights.md](access-insights.md). The console page is **Security → Threats**; the in-console help is [docs/help/threats.md](help/threats.md).

## Sign-ins

Sign-ins are stored in `signin_events`, one row per attempt, with the outcome (`success`, `failure` for a wrong password, `mfa_failure` for a refused or wrong second factor), address, country and location when the provider gives them. They are kept for 400 days, then deleted.

### From Grantra Login

Grantra reads Grantra Login's `LOGIN` and `LOGIN_ERROR` events from Keycloak's admin API every minute, from where it left off (the first read goes back one day). It turns event saving on in the realm if it is off, keeping Keycloak's copies for seven days. Only wrong passwords and wrong one-time codes count as failures; errors such as an expired cookie are ignored. People are matched through their Grantra Login account.

The connector's service account needs the `realm-management` roles `view-events` and `manage-events` on top of the ones in [connectors.md](connectors.md). [`deploy/keycloak/grantra-realm.json`](../deploy/keycloak/grantra-realm.json) has them for new installs; an existing realm needs them added by hand (or `make reset` in the demo).

### From another provider

A script sends sign-ins from Entra ID, Okta, Google or anything else with a [service token](service-tokens.md) that has the `sign_in_event.send` scope:

```sh
curl -X POST https://grantra.example.com/api/v1/sign-in-events \
  -H "Authorization: Bearer grantra_st_..." -H "Content-Type: application/json" \
  -d '{"source": "okta", "events": [{
        "id": "evt-81f2", "time": "2026-10-19T08:30:00Z", "user": "ana@example.com",
        "outcome": "failure", "ip": "203.0.113.7", "country": "ES",
        "latitude": 40.4, "longitude": -3.7, "application": "Salesforce"}]}'
```

| Field | |
|---|---|
| `source` | A short lowercase name for the provider (`okta`, `entra`); `grantra-login` is taken |
| `id` | Required, unique within the source: events already received are skipped, so overlapping windows are fine |
| `time` | Required, RFC 3339, not in the future |
| `user` | Required: the email or username that tried. Grantra matches it to the one person with that email or account username; otherwise the event is kept unmatched and still counts for password spray |
| `outcome` | `success`, `failure` or `mfa_failure` |
| `ip`, `country` | Optional. Country is a two-letter ISO code. Needed for new country |
| `latitude`, `longitude` | Optional, both or neither. Needed for impossible travel |
| `application`, `user_agent` | Optional, shown on the page |

Up to 1,000 events and 4 MiB per call. The answer is `{"received": 3, "new": 2}`. An admin may also call it signed in.

## Detections

Each detection has a default severity and values, which admins change in the **Detections** tab (`threat_detections`). Detections look at each sign-in once, within a minute of it arriving.

| Detection | Raises | Default | May respond on its own with |
|---|---|---|---|
| `leaver_sign_in` | A successful sign-in by someone who left or is suspended | critical | end sessions |
| `impossible_travel` | Two successful sign-ins over 500 km apart at over 900 km/h, within 24 hours | high | end sessions, require MFA, suspend |
| `new_country` | A successful sign-in from a country not seen in 90 days, for people with at least 5 sign-ins with a country | medium | end sessions, require MFA |
| `dormant_account` | A successful sign-in after 90 days without one | medium | end sessions, require MFA, suspend |
| `brute_force` | 10 wrong passwords for one person in 15 minutes; high if a sign-in then succeeds | medium | require MFA |
| `password_spray` | One address failing for 10 different people in 15 minutes | high | none |
| `mfa_fatigue` | 5 refused second factors for one person in 10 minutes; critical if a sign-in then succeeds | high | end sessions, suspend |
| `privilege_outside_grantra` | High or critical access read on an account that Grantra neither gave nor was asked for; critical access makes it critical | high | none |
| `new_platform_admin` | Someone given the Grantra admin role | medium | none |

Detections that compare with earlier sign-ins (new country, dormant account, impossible travel) only fire once Grantra has that history: Grantra Login's first read goes back one day, so dormant accounts are found once Grantra has seen people sign in before.

Failure-only detections never suspend: otherwise anyone could lock a colleague out by typing wrong passwords in their name. `privilege_outside_grantra` only looks at access read after the module was turned on, and ignores an application's first read, so existing access does not flood the page.

## Alerts

An alert (`threat_alerts`) is about a person, or for password spray about an address. While one is open, the same detection on the same subject adds to it (`occurrences`, `last_seen_at`) and raises its severity if the new finding is worse, instead of opening another. High and critical alerts, new or escalated, are emailed to the admins (`threat_detected`, one digest per scan) and listed in their Inbox. **Nobody is told about, or may close or respond to, an alert about themselves**: whoever took over their account would read it too. Cedar enforces it (`no-self-threat-response`, `no-self-threat-close`).

An alert is **resolved** when dealt with, or **dismissed** with a note when it was not a threat. Every change is audited (`itdr.alert_raised`, `itdr.alert_escalated`, `itdr.alert_resolved`, `itdr.alert_dismissed`).

## Responses

| Action | What happens |
|---|---|
| `revoke_sessions` | Signs the person out of Grantra Login (every session in Keycloak) and the console, ends their SSH and gateway sessions and checks in their vault check-outs. AWS console sessions cannot be ended early and last until they expire; the response says how many |
| `require_mfa` | For 1 to 90 days (7 by default), puts the person in the `grantra-mfa-required` role so Grantra Login asks for a one-time code, then signs them out. Needs a sign-in policy that asks for a second factor (*For people with sensitive access* or *For everyone*); under the first, the MFA sync keeps them in the role until it ends (`mfa_overrides`) |
| `suspend` | Sets the person `suspended`, ends their sessions and disables their Grantra Login account. Their access in Grantra is kept; applications Grantra provisions disable their accounts on the next run, as for any suspension. HR syncs do not lift it. The last active admin cannot be suspended |
| `reinstate` | Sets a suspended person back to `active` and re-enables the Grantra Login accounts the suspension disabled; provisioned applications give their access back on the next run |

Each response is stored in `threat_responses` and audited as `itdr.response`; a step that fails (Keycloak unreachable) is recorded in its `error` and the rest still happens. Automatic responses are made by `system:itdr`, at most once per alert and action.

## API

| Endpoint | Needs |
|---|---|
| `GET /api/v1/threats` | `threat.read`: open alerts per severity and the sign-in sources of the last 30 days |
| `GET /api/v1/threats/alerts?status=&severity=&identity=&detection=&limit=&before=` | `threat.read`, newest seen first |
| `GET /api/v1/threats/alerts/{id}` | `threat.read`, with its responses |
| `POST /api/v1/threats/alerts/{id}/close` | `threat_alert.close`: `{"status": "dismissed", "note": "travelling"}`; a note is required to dismiss |
| `GET /api/v1/threats/detections`, `PUT /api/v1/threats/detections/{id}` | `threat.read`, `threat.manage`: `{"enabled": true, "severity": "high", "auto_response": "require_mfa", "values": {"failures": 20}}` |
| `POST /api/v1/threats/scan` | `threat.manage`: reads and looks now instead of waiting for the minute |
| `GET /api/v1/sign-in-events?identity=&outcome=&ip=&limit=&before=` | `threat.read` |
| `POST /api/v1/sign-in-events` | `threat.manage`, or a service token with `sign_in_event.send` |
| `GET`, `POST /api/v1/identities/{id}/threat-responses` | `threat.read`, `threat.respond`: `{"action": "suspend", "alert_id": "...", "reason": "..."}`, with `days` for `require_mfa` |

Admins have every action; auditors read.
