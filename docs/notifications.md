# Notifications

Grantra emails people when they need to act or when something they asked
for is decided. Email is plain text, in English, and links to the console
page where the person acts.

| Kind | Who gets it | When |
|---|---|---|
| `approval_needed` | The approver of the step, or every admin when the step has nobody else to ask | An access request starts waiting for them |
| `request_expiring` | The same people | Once, when the step they have not decided expires within 24 hours |
| `request_decided` | The requester and the person the access is for | The request is approved, rejected (with who rejected it and their comment) or expired |

Nobody is emailed about their own access as an approver. Withdrawn requests,
and requests cancelled because the person left, send nothing.

Email for access reviews and separation of duties violations comes next.

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
  kind off. Changes are audited as `notifications.updated`.

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
