# Changelog

All notable changes to Grantra are listed here. Versions follow [Semantic Versioning](https://semver.org/); until 1.0, a minor version may change behavior or the API.

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

[0.1.0]: https://github.com/grantra-dev/grantra/releases/tag/v0.1.0
