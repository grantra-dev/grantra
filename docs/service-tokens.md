# Service tokens

Scripts and scheduled jobs use a **service token** to send Grantra files without anyone signing in: a nightly job that posts the HR export, or one that sends the account list of an application with no API. A token:

- can only do what its **scopes** say, each one either anywhere or pinned to one HR source or application;
- always **expires**, after 1 to 365 days, and can be **revoked** at any time;
- is shown **once**, when it is created. Grantra keeps only a SHA-256 of it, and its first characters (`grantra_st_abc123…`) to tell tokens apart;
- acts under its own name: every run and every audit event it causes says `service_token:<id>`, and refused attempts are audited as `authz.denied`.

Admins create and revoke tokens in **Settings → API tokens**; auditors can see them. Tokens belong to the company, not to whoever made them, so revoke the ones nobody looks after any more.

When the person who created a token leaves the company, the token **keeps working**: the job using it is the company's, and it may well be the HR feed that processed their exit. Instead, the admins get one email (`service_token_creator_left`) listing that person's working tokens, each one is audited as `service_token.creator_left`, and the list marks it *Creator left* until someone revokes it or it expires. Replace it with a token made by an admin who stays if the job should keep running.

## Scopes

| Scope | Lets the token call |
|---|---|
| `identity_source.sync` | `POST /api/v1/identity-sources/{id}/runs`: feed an [HR source](identity-lifecycle.md), with `dry_run` and `force` |
| `application.import_accounts` | `POST /api/v1/applications/{id}/account-imports`: load an [account file](connectors.md#csv-files) |

Pin a scope to one source or application with its id, `identity_source.sync:<source id>`, so a leaked token cannot touch anything else. Tokens open only these two endpoints; anything else answers `401`.

## Using one

Send it as a bearer token:

```sh
curl -fsS -X POST -H "Authorization: Bearer $GRANTRA_TOKEN" -H 'Content-Type: text/csv' \
  --data-binary @hr-export.csv "https://grantra.example.com/api/v1/identity-sources/$SOURCE/runs"
```

| Answer | Meaning |
|---|---|
| `200` | Planned (`dry_run=true`) or applied |
| `401` | Unknown, expired or revoked token |
| `403` | The token has no scope for this source or application |
| `409` | HR sync stopped by the leaver limit; nothing changed |
| `413` | The file is over 32 MiB |
| `422` | The file has errors, listed in the answer; nothing changed |

`curl -f` makes a cron job fail visibly on any of these. Keep the token where the job's other secrets live (a CI secret, a Kubernetes secret), never in the script itself.

## API

| Endpoint | Action |
|---|---|
| `GET /api/v1/service-tokens` | `service_token.read`: every token, without secrets |
| `POST /api/v1/service-tokens` | `service_token.manage`: `{"name": "Nightly HR export", "scopes": ["identity_source.sync:<id>"], "expires_in_days": 365}`. `201` with `{"token": {...}, "secret": "grantra_st_..."}` |
| `DELETE /api/v1/service-tokens/{id}` | `service_token.manage`: revokes it. `204` |
