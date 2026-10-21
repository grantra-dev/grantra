# Notifications

Grantra emails people when they need to act or when something they asked
for is decided. Email is plain text, in English, and links to the exact task
in the console: approval emails to `/approvals?request=<id>`, decisions to
`/requests?request=<id>`, review emails to `/reviews?campaign=<id>`. The page
highlights that request or shows only that campaign, and says so when
someone already decided it.

| Kind | Who gets it | When |
|---|---|---|
| `approval_needed` | The approvers of the step who have not approved yet, or every admin when the step has nobody else to ask | An access request starts waiting for them, or a step that nobody decided in time is escalated to them |
| `request_expiring` | The same people | Once, when the step they have not decided expires within 24 hours (set under [Settings → General](settings.md)) |
| `request_decided` | The requester and the person the access is for | The request is approved, rejected (with who rejected it and their comment) or expired |
| `review_assigned` | Each reviewer, with how many items they have; admins also count the items with nobody else to review them | A review campaign is launched |
| `campaign_due_soon` | Reviewers who still have items pending, with how many | Once, when the campaign ends within 48 hours (set under [Settings → General](settings.md)) |
| `break_glass_used` | Every admin and auditor. **Cannot be turned off** | Someone breaks the glass on a vault account |
| `break_glass_review` | Admins, about uses other than their own | A break-glass use is still not reviewed a day after it started (set under [Settings → General](settings.md)), then daily |
| `sod_violation` | Admins | A separation of duties check (every 30 minutes, or run from the console) finds new violations or ends exceptions. One email per check lists up to 20, most severe first |
| `service_token_creator_left` | Admins | Someone who created [API tokens](service-tokens.md) that still work leaves the company. One email per HR sync or end-date check lists their tokens |
| `setting_change_proposed` | Admins other than the one who proposed it | An admin asks to make a [setting](settings.md) less strict, which waits for another admin |
| `non_human_owner_left` | Admins, and whoever takes them over | Someone who owned [service accounts, AI agents or workloads](non-human-identities.md) leaves. Admins get the whole list; each new owner (the leaver's manager) gets what is theirs now |

Nobody is emailed about their own access as an approver. Withdrawn requests,
and requests cancelled because the person left, send nothing.

## How it is sent

1. The flow that causes a notification writes a row in `notification_outbox`
   in its own transaction, with the data the email shows. If the flow rolls
   back, the email is never sent.
2. A background job, every 30 seconds, sends what is due, one email at a
   time. Before sending it checks again that the person is active, has an
   email and still wants that kind.
3. A failed send is retried after 1 minute, 5 minutes, 30 minutes, 2 hours
   and 6 hours, then given up as `failed`. A notification older than 3 days
   is `skipped` as stale.
4. Delivered, failed and skipped rows are deleted after 30 days.

Several Grantra servers can share a database: each row is locked while it is
sent, so it goes out once.

Every email carries `Auto-Submitted: auto-generated`, so out-of-office
replies are not sent back, and `X-Grantra-Notification: <kind>` for filters.

## Opting out

Everyone chooses which kinds they get, from the bell in the console header
(`/settings/notifications`) or the API:

- `GET /api/v1/me/notifications` returns `email_enabled` (whether this
  install sends email), `has_email` and each kind with `enabled`.
- `PUT /api/v1/me/notifications/{kind}` with `{"enabled": false}` turns a
  kind off. Changes are audited as `notifications.updated`. Kinds marked
  `required` (the break-glass alert) cannot be turned off.

Every email ends with a link to the settings page.

## Setup

| Variable | Default | Purpose |
|---|---|---|
| `GRANTRA_SMTP_HOST` | none: email off | SMTP server |
| `GRANTRA_SMTP_PORT` | `587` | |
| `GRANTRA_SMTP_TLS` | `starttls` | `starttls` (refuses a server that does not offer it), `tls` (implicit TLS, usually port 465) or `none` (for a local mail catcher) |
| `GRANTRA_SMTP_USERNAME` / `_PASSWORD` | none | Set both or neither. The password is never sent over a plain connection, except to localhost |
| `GRANTRA_SMTP_FROM` | required with a host | Sender, like `Grantra <grantra@example.com>` |

`GRANTRA_PUBLIC_URL` builds the links. Without `GRANTRA_SMTP_HOST`,
notifications are marked `skipped` instead of piling up, so turning email on
later does not send a backlog.

The dev stack in `deploy/docker-compose.yml` sends to Mailpit: open
http://localhost:8025 to read what Grantra sent.

## Using Microsoft 365, Google Workspace or another provider

Grantra speaks plain SMTP, so it works with any mail service that accepts
SMTP: Microsoft 365, Google Workspace, Gmail, Amazon SES, SendGrid, Mailgun,
Postmark, or your own Exchange or Postfix server. It signs in with
`AUTH PLAIN` (username and password) or not at all. It does not yet sign in
with OAuth (`XOAUTH2`), so pick one of the options below for your provider.

Whatever you use, the domain in `GRANTRA_SMTP_FROM` must allow that service to
send for it (SPF and DKIM), or the emails land in spam. Microsoft 365 and
Google Workspace already do for your own domain.

### Microsoft 365 (Exchange Online)

Microsoft is turning off username and password sign-in for SMTP
(`smtp.office365.com`), so do not point Grantra there with a mailbox password.
Use one of these instead:

- **Relay through a connector** (recommended). In the Exchange admin center,
  add a connector from "Your organization's email server" to "Office 365"
  that accepts mail from the public IP address Grantra sends from. Then send
  to your domain's MX host with no username. Grantra needs a fixed public IP
  and outbound port 25 open; many cloud providers block port 25 by default.

  ```sh
  GRANTRA_SMTP_HOST=example-com.mail.protection.outlook.com  # your MX record
  GRANTRA_SMTP_PORT=25
  GRANTRA_SMTP_TLS=starttls
  GRANTRA_SMTP_FROM="Grantra <grantra@example.com>"
  ```

- **A transactional email service** (see below), when you cannot open port 25
  or do not have a fixed IP.

### Google Workspace and Gmail

- **A mailbox with an app password**. Create a mailbox for Grantra (for
  example `grantra@example.com`), turn on 2-Step Verification for it and
  create an app password. A Workspace admin may have to allow app passwords
  first. The sender must be that mailbox or one of its aliases. Gmail caps
  how many emails a mailbox sends per day, which is plenty for most
  companies.

  ```sh
  GRANTRA_SMTP_HOST=smtp.gmail.com
  GRANTRA_SMTP_PORT=587
  GRANTRA_SMTP_TLS=starttls
  GRANTRA_SMTP_USERNAME=grantra@example.com
  GRANTRA_SMTP_PASSWORD=<the 16-letter app password>
  GRANTRA_SMTP_FROM="Grantra <grantra@example.com>"
  ```

- **The SMTP relay service** (Workspace only). In the Admin console, under
  Apps, Google Workspace, Gmail, Routing, add an SMTP relay rule that allows
  Grantra's public IP address. Then send with no username to
  `smtp-relay.gmail.com`, port 587, `starttls`. If the rule requires SMTP
  authentication instead, set the username and app password as above.

### Transactional email services

These give you SMTP credentials made for applications and work from any
network:

| Service | Host | Username | Password |
|---|---|---|---|
| Amazon SES | `email-smtp.<region>.amazonaws.com` | SMTP username from the SES console | SMTP password from the SES console |
| SendGrid | `smtp.sendgrid.net` | `apikey` | An API key |
| Mailgun | `smtp.mailgun.org` (`smtp.eu.mailgun.org` in the EU) | The domain's SMTP login | Its SMTP password |
| Postmark | `smtp.postmarkapp.com` | A server API token | The same token |

All of them take port 587 with `starttls`. Verify your sending domain with the
service first.

### With Helm

The chart's `smtp` values map to the same settings. Keep the password in a
Secret with the key `smtp-password`:

```yaml
smtp:
  host: smtp.gmail.com
  port: 587
  tls: starttls
  from: "Grantra <grantra@example.com>"
  username: grantra@example.com
  existingSecret: grantra-smtp   # holds smtp-password
```

For a relay with no sign-in, leave `username` empty.
