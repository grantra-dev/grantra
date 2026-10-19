# Changelog

All notable changes to Grantra are listed here. Versions follow [Semantic Versioning](https://semver.org/); until 1.0, a minor version may change behavior or the API.

## [Unreleased]

### Upgrading

- **Threat detection reads Grantra Login's sign-ins**, which needs two more `realm-management` roles on the connector's service account: `view-events` and `manage-events`. New installs have them in `deploy/keycloak/grantra-realm.json`; on an existing realm add them in Keycloak (Clients → `grantra-connector` → Service account roles), or run `make reset` in the demo. Without them Grantra still looks at access and at the sign-ins scripts send; the job logs that it cannot read Grantra Login, and **Look now** on the Threats page shows the error.

### Added

- **Identity threat detection and response** ([docs](docs/threat-detection.md)). Security → Threats shows alerts for a leaver who still signs in, impossible travel, a new country, a dormant account used, password guessing, password spray, MFA fatigue, high-risk access granted outside Grantra and new Grantra admins. Sign-ins come from Grantra Login on their own and from other providers through `POST /api/v1/sign-in-events` with a service token (new scope `sign_in_event.send`). From an alert, admins end the person's sessions everywhere, require a second factor for some days, or suspend and reinstate them; each detection can be tuned, turned off or set to respond on its own with the responses safe for it. High and critical alerts go to the admins' Inbox and email, never to the person they are about. Turn it off in Settings → Modules.

- **Help in the console.** A Help section with pages for what each person does in Grantra (everyone, approvers and reviewers, helpdesk and auditors, admins), read from [`docs/help`](docs/help) so the same text is on GitHub.
- **Console search.** <kbd>⌘K</kbd> (or <kbd>Ctrl K</kbd>, or <kbd>/</kbd>) searches pages, people, roles, entitlements, applications, vault accounts, servers and remote targets, showing each person only what they may see, and opens the exact thing ([docs](docs/console.md)).
- **Inbox.** One place, in the header and at `/inbox`, for everything waiting on you: approvals, access reviews and break-glass reviews, with counts in the menu.
- **Home by role.** Home says what needs you today: tasks, access ending this week, pending requests; for admins and auditors also failing connections, orphan accounts, open separation of duties violations and access with no description.
- **Connector gallery.** Settings → Connectors connects SCIM, LDAP / Active Directory, Grantra Login and CSV applications in three steps, with a test read at the end; Entra ID, Google Workspace, AWS, GitHub, Okta, Salesforce, Workday and BambooHR are listed as planned.
- **Modules per tenant.** Settings → Modules turns privileged access or Grantra Login off for a company that does not use them: their pages, search results and tasks disappear. API: `GET /api/v1/modules`, `PUT /api/v1/modules/{module}`.
- **Review filters.** Access reviews can be searched, filtered by risk and by how access was given, and sorted; bulk actions only reach what is shown.
- **Clearer requests.** Request access shows how many of your teammates hold each item and flags items with no description; approval steps show how many days they have waited.
- **CSV formats documented.** [CSV files](docs/help/csv-files.md) has every column of HR exports and account files, the file rules (separator, encoding, dates, quotes) and what each error means and how to fix it. A **?** next to each upload shows the columns and offers a template to download.

### Changed

- **Console menu** grouped by what people come to do (For you, To do, Privileged access, Governance, Settings), with old `/admin` addresses redirecting. Email settings and signing out moved to the menu under your name.
- **Emails link to the exact task**: the request to approve or the campaign to review, not the whole list.
- **Auditors see Governance and Settings**, read only.
- New API endpoints for the console: `GET /api/v1/me/inbox`, `GET /api/v1/me/home`, `GET /api/v1/search`. People search by name or email now uses a trigram index when `pg_trgm` is available.

## [0.3.0] - 2026-10-17

Privileged access (PAM) arrives: a credential vault, just-in-time access, break glass, and recorded sessions to servers, databases, Kubernetes, Windows desktops and cloud consoles, all handed out and taken back by the same governance as everything else. Data also comes in more easily, from the console or from scripts.

### Upgrading from 0.2

- Migrations run as before (`grantra migrate`, or the Helm migration job). They try to create the `pg_trgm` extension for the gateway's search indexes; if the migration role may not, searches still work, without an index.
- The vault, SSH certificates and the gateway need a master key: set `vault.key`, `vault.existingSecret` or a key provider (`vault.provider`) before using them, and keep a copy of the key apart from the database backups ([docs](docs/vault.md#turning-it-on)). Without one, everything else keeps working.
- The gateway (`gateway.enabled`) and guacd for desktops (`desktops.enabled`) are off by default in Helm.
- Demo stack: run `make reset`, then `make up`. The sample database *shop-db* and the new demo data are only created on a fresh volume. `docker compose -f deploy/docker-compose.yml --profile rdp up` adds the test desktop.

### Privileged access (PAM)

- **Credential vault**: passwords and SSH keys of privileged accounts, sealed with envelope encryption under a master key (`GRANTRA_VAULT_KEY`). People who hold an account's access check it out with a reason for a limited time; exclusive accounts go to one person at a time. Admins manage the vault but cannot read secrets without the access ([docs](docs/vault.md)).
- **Password rotation** in Active Directory, OpenLDAP and Grantra Login, on a schedule and after each use, without ever losing a password the target accepted ([docs](docs/vault.md#rotation)).
- **Discovery** of privileged, orphaned and service accounts that belong in the vault.
- Helm: `vault.key` / `vault.existingSecret`.
- **Key providers**: keep the vault's master key in AWS KMS, Google Cloud KMS, Azure Key Vault (or Managed HSM), OpenBao or HashiCorp Vault Transit, or an HSM through PKCS#11 (in a build with `-tags pkcs11`), so Grantra never holds it. Wrapped data keys are bound to their tenant, account and version, and each provider unwraps only with its own key. `grantra vault rewrap` moves an existing vault and the SSH certificate authorities to a new key without decrypting secrets. Helm: `vault.provider`, `vault.kmsKey`, `vault.env`, `vault.volumes`, `vault.volumeMounts`, `serviceAccount.annotations`, `podLabels` ([docs](docs/vault.md#key-providers)).
- **Just-in-time access** (zero standing privileges): mark an entitlement *just in time only* with a maximum of hours per activation and optional approval. People are made eligible by an admin or ask to be eligible, then activate it for a few hours with a reason from **My access**; it ends on its own and provisioning takes it back within seconds. The database refuses standing assignments to such access, and birthright rules cannot give it ([docs](docs/just-in-time.md)).
- **Standing access report**: high and critical risk access held with no end date, converted into eligibility in one click.
- **Break glass**: responders open an emergency vault account without holding its access, for up to an hour, ignoring exclusivity. Whoever arms an account (turns break glass on or changes its responders) cannot open it. Every admin and auditor is emailed at once (an email nobody can turn off), the password rotates afterwards, and another admin reviews each use, with daily reminders ([docs](docs/vault.md#break-glass)).
- **SSH certificates**: Grantra keeps two SSH certificate authorities, sealed with the vault key: one for people and one for the gateway. Servers trust one of them once (`TrustedUserCAKeys`); certificates name their target (`grantra:<name>`), so they only work where they were meant to. Each server is a target whose access is an entitlement (just in time only by default); whoever holds it signs a certificate for their own key, valid only while the access lasts and at most the target's maximum ([docs](docs/ssh.md)).
- **SSH gateway** (`grantra gateway`): people reach targets through it with their certificate; it checks access every 30 seconds and ends the session when access goes or an admin ends it, refuses servers whose host key changed, and allows no forwarding. Every session is recorded in asciicast v2 and played back in the console, and the commands typed are indexed for search ([docs](docs/ssh.md#the-gateway)).
- Helm: `gateway.*` runs the gateway with its own Service and a host key kept across upgrades. docker-compose runs it on port 2222.
- **Database and cluster gateway**: the gateway also proxies PostgreSQL (15432), MySQL (13306) and Kubernetes (8443). Each database or cluster is a target whose access is an entitlement (just in time only by default); whoever holds it gets a token that works only for that target and never outlives the access. The gateway logs in with a vault account nobody sees, logs every query and cluster request (searchable in the console), checks access every 30 seconds and ends sessions when it goes or an admin ends them. Kubernetes requests act as the person (`grantra:<email>`, in the target's groups) through impersonation. Clients always use TLS, with a certificate from a per-tenant gateway CA sealed with the vault key, or the operator's own ([docs](docs/gateways.md)).
- Helm: `gateway.ports`, `gateway.publicAddresses`, `gateway.tls.*`, `gateway.kubernetes.inCluster`. docker-compose exposes the new listeners, and the demo adds the database *shop-db*.
- **Desktops (RDP)** in the browser: people click **Connect** and a Windows desktop opens in a new tab, logged in with a vault account, through Apache Guacamole's guacd (`GRANTRA_GUACD_ADDR`), always over TLS with Network Level Authentication or TLS alone (never the legacy RDP encryption). The whole screen is recorded and played back in the console (up to `GRANTRA_RDP_MAX_RECORDING_MB` per session); the clipboard can be paste only (the default), both ways or off, and drives, printing, audio and file transfer are off ([docs](docs/gateways.md#desktops-rdp)).
- **Cloud consoles**: AWS (an IAM role assumed for the person and federated sign-in, with CLI credentials), Azure (a role assignment on a subscription or resource group, removed when the session ends) and Google Cloud (an IAM binding with an expiry condition, removed when the session ends). The job *end cloud console sessions* takes grants back within 30 seconds of the access ending ([docs](docs/gateways.md#cloud-consoles)).
- The console's **Databases & clusters** page is now **Remote access** (`/remote-access`; `/databases` redirects), and its catalog application *Databases and clusters* is renamed *Remote access*.
- Helm: `desktops.*` runs guacd. docker-compose runs guacd, with a test desktop in the `rdp` profile, and the demo adds the desktop *office-pc*.
- Access requests take `kind` (`access` or `eligibility`) and `duration_hours`.
- Leavers lose their eligibilities along with their access.
- Vault check-outs end no later than the access they come from, and within a minute once it is gone.
- Access reviews certify eligibilities as their own items (*Eligible to activate*), with the same reviewers; revoking one removes it and ends any activation running with it. Activations themselves, which last hours, are no longer review items.

### Bringing data in

- **Column mapping in the console**: HR sources can be edited, and the source form has a box per field to name the column that holds it, with a sample file to pick from that shows which fields it already has (`PUT /api/v1/identity-sources/{id}`) ([docs](docs/identity-lifecycle.md#the-csv-file)).
- **Account files**: the `csv` connector loads the accounts of applications with no SCIM or LDAP from a CSV export, one row per account or per account and role, checked first with a dry run and refused whole on any error (`POST /api/v1/applications/{id}/account-imports`) ([docs](docs/connectors.md#csv-files)).
- **Service tokens**: scripts and scheduled jobs feed HR sources and account files without a session, with a token that can only do what its scopes say (optionally one source or application), expires within a year, can be revoked, and is audited under its own name. Administration has an **API tokens** tab ([docs](docs/service-tokens.md)). When a token's creator leaves the company it keeps working, so jobs don't break, and the admins are emailed and the tab marks it until someone acts.

### Changed

- HR syncs read the file row by row and write in batches of 1,000: importing 2,000 new people is about 3.5 times faster with the database on the same machine, and more when it is across a network. The file is no longer copied in memory, and the start and end dates job writes its events in batches too.
- The console's Content Security Policy allows `'wasm-unsafe-eval'`, which the session player needs to run its terminal emulator. It does not allow eval of JavaScript.

- Vault entitlements are always just in time only: requests for them need a duration, and the existing ones are switched on upgrade. Existing assignments with no end date stay, and show in the Standing access report to convert.
- Faster on large tenants: "does this person hold this access" checks (on every gateway connection, cluster request and session heartbeat) use their index; the audit log of one person, the job that ends expiring access, the notification cleanup and app account lookups get indexes; searches of gateway queries and SSH commands use trigram indexes (`pg_trgm`); apps' Cedar policies are parsed once per version instead of on every check; birthright changes are written in batches of 1,000; SSH recordings stream instead of loading whole; and the desktop gateway reads guacd's stream about 4 times faster.
- An SSH certificate is only signed for a person who is active, as gateway tokens already were.

## [0.2.0] - 2026-10-15

Grantra Login becomes a full identity provider you manage from the Grantra console, and your own apps can ask Grantra for authorization decisions.

### Grantra Login

- **Grantra Login theme** for the sign-in pages and account emails, light and dark, in English and Spanish ([docs](docs/grantra-login.md)).
- **`ghcr.io/grantra-dev/grantra-login`** image: Keycloak with the theme built in, used by the Helm chart.
- **Grantra Login connector**: Grantra reads and provisions users, groups and realm roles in Grantra Login. New users set their own password from an email ([docs](docs/connectors.md#grantra-login)).
- The demo connects Grantra Login as an application.
- **Grantra Login page** in the console: register the apps that sign in through it and rotate their secrets, and pick the sign-in policy ([docs](docs/grantra-login.md#the-console-page)).
- **MFA and passkeys**: ask for a second factor from nobody, everyone, or only people with sensitive access, and let people sign in with passkeys.
- **Risk-based MFA**: Grantra keeps the people who hold high or critical risk access in the `grantra-mfa-required` role that the sign-in flow asks for a second factor.
- **Identity providers**: people sign in to Grantra Login with Entra ID, Okta, or any OpenID Connect or SAML provider, and land in their existing account by verified email ([docs](docs/grantra-login.md#identity-providers)).
- **Gradual migration**: send each email domain to its old provider or to Grantra Login, one domain at a time, and import users (with their passwords), groups, roles and apps from another Keycloak after a dry run ([docs](docs/grantra-login.md#moving-over-one-domain-at-a-time)).

### Authorization for your apps

- **Cedar policies per application** and **`POST /api/v1/authz/check`**: your apps ask Grantra whether someone may do something, with an API key, and Grantra answers with the person's department, manager, roles and entitlements filled in. Leavers are always denied ([docs](docs/app-authorization.md)).
- **Policy playground** in the console: try questions against draft policies before saving them.

## [0.1.0] - 2026-10-14

The first public release: identity governance (IGA) you can run on your laptop in five minutes or on Kubernetes.

### Identity governance

- **Joiner, mover, leaver** from an HR source (CSV): start and end dates, a dry run before applying, and a brake that refuses a sync removing too many people at once ([docs](docs/identity-lifecycle.md)).
- **Catalog and birthright access**: applications, entitlements, business roles and attribute rules ([docs](docs/access.md)).
- **Connectors** for SCIM and LDAP / Active Directory: account aggregation, correlation to people, orphan accounts, and provisioning that is off by default, with a dry run, a size brake and a journal of every change ([docs](docs/connectors.md)).
- **Access requests** approved by the manager, then the application owner, with optional end dates and a 7-day timeout per step ([docs](docs/access-requests.md)).
- **Access review campaigns** by manager or owner, with keep or revoke decisions and a rule for missed ones ([docs](docs/access-reviews.md)).
- **Separation of duties** policies in detect or prevent mode, time-boxed exceptions, and risk acceptance by an admin when a request would break a policy ([docs](docs/separation-of-duties.md)).
- **Audit log** of every change and decision.

### Console and notifications

- Web console for employees, managers, application owners, auditors and admins: my access, request access, approvals, reviews, people, administration and audit log ([docs](docs/console.md)).
- Platform roles (admin, helpdesk, auditor) enforced with Cedar policies.
- Email notifications for approvals, reviews and decisions ([docs](docs/notifications.md)).
- Sign-in through OpenID Connect with Grantra Login (bundled Keycloak) or your own identity provider.

### Running Grantra

- Five-minute demo with a sample company ([docs](docs/demo.md)).
- Helm chart with a separate database role for migrations and safe background jobs across replicas ([docs](docs/production.md)).
- OpenTelemetry traces, metrics and logs, and a Grafana dashboard ([docs](docs/observability.md)).
- Multi-tenant PostgreSQL with row-level security.
- Container image and binaries signed with cosign (keyless), with an SPDX SBOM ([docs](docs/releases.md)).

[Unreleased]: https://github.com/grantra-dev/grantra/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/grantra-dev/grantra/releases/tag/v0.3.0
[0.2.0]: https://github.com/grantra-dev/grantra/releases/tag/v0.2.0
[0.1.0]: https://github.com/grantra-dev/grantra/releases/tag/v0.1.0
