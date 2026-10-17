# Changelog

All notable changes to Grantra are listed here. Versions follow [Semantic Versioning](https://semver.org/); until 1.0, a minor version may change behavior or the API.

## [Unreleased]

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
- Access requests take `kind` (`access` or `eligibility`) and `duration_hours`.
- Leavers lose their eligibilities along with their access.
- Vault check-outs end no later than the access they come from, and within a minute once it is gone.
- Access reviews certify eligibilities as their own items (*Eligible to activate*), with the same reviewers; revoking one removes it and ends any activation running with it. Activations themselves, which last hours, are no longer review items.

### Changed

- The console's Content Security Policy allows `'wasm-unsafe-eval'`, which the session player needs to run its terminal emulator. It does not allow eval of JavaScript.

- Vault entitlements are always just in time only: requests for them need a duration, and the existing ones are switched on upgrade. Existing assignments with no end date stay, and show in the Standing access report to convert.

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

[Unreleased]: https://github.com/grantra-dev/grantra/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/grantra-dev/grantra/releases/tag/v0.2.0
[0.1.0]: https://github.com/grantra-dev/grantra/releases/tag/v0.1.0
