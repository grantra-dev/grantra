<p align="center">
  <img src="site/favicon.svg" width="64" height="64" alt="Grantra logo">
</p>

<h1 align="center">Grantra</h1>

<p align="center">
  <strong>Open-source identity governance: who has access to what, why, and until when.</strong>
</p>

<p align="center">
  <a href="https://github.com/grantra-dev/grantra/actions/workflows/ci.yml"><img src="https://github.com/grantra-dev/grantra/actions/workflows/ci.yml/badge.svg?branch=main" alt="CI"></a>
  <a href="https://github.com/grantra-dev/grantra/releases"><img src="https://img.shields.io/github/v/release/grantra-dev/grantra?include_prereleases&sort=semver" alt="Latest release"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue" alt="License: Apache 2.0"></a>
</p>

Grantra is an open-source alternative to SailPoint and CyberArk. It starts with identity governance (IGA): it keeps track of who has access to which applications, gives and removes access as people join, move and leave, lets people ask for access and their managers approve it, runs access reviews, and stops toxic combinations of access before they happen.

It works with the identity provider you already run (Entra ID, Okta, Keycloak). Companies without one can use **Grantra Login**, a Keycloak that ships preconfigured with Grantra and is managed from the Grantra console: single sign-on, MFA and passkeys, and a gradual move over from Entra ID or Okta.

For privileged access, it keeps admin passwords and keys in a vault, hands out admin rights just in time, and records every session to servers, databases, clusters, desktops and cloud consoles.

<p align="center">
  <img src="site/screenshots/home.png" width="800" alt="The Grantra console home page, with shortcuts to My access, Request access, Approvals, Access reviews, People, Administration and the Audit log">
</p>

> **Status:** v0.3 adds privileged access (PAM) to identity governance and Grantra Login. It is ready to try and to pilot, not yet battle-tested in production. Feedback and issues are very welcome.

## Try it in five minutes

Requires Docker.

```sh
git clone https://github.com/grantra-dev/grantra.git
cd grantra
make up
```

Open http://localhost:8080 and sign in as `admin` / `admin`. The stack loads a small sample company: every person signs in with their first name and the password `demo` (`marta`, `luis`, `sara`...). Emails Grantra sends land in Mailpit at http://localhost:8025.

[docs/demo.md](docs/demo.md) walks you through a new hire, an access request and its approval, a separation of duties risk, an access review and a leaver. `make reset` starts over.

<details>
<summary>All the services in the dev stack</summary>

| Service | URL | Dev credentials |
|---|---|---|
| Grantra console | http://localhost:8080 | `admin` / `admin`, or a demo person with `demo` |
| Grantra API | http://localhost:8080/healthz | |
| Grantra Login (Keycloak) | http://localhost:8081 | `admin` / `admin` (realm `grantra`, user `admin` / `admin`) |
| PostgreSQL | localhost:5432 | `grantra_app` / `grantra` |
| Mailpit | http://localhost:8025 | |

</details>

## What it does today

| | |
|---|---|
| **Joiner, mover, leaver** | Reads people from an HR source (CSV) and gives or removes access when someone joins, changes team or leaves. A safety brake refuses a sync that would remove too many people at once. Nightly jobs can send the export with a scoped [service token](docs/service-tokens.md). [Docs](docs/identity-lifecycle.md), [CSV format](docs/help/csv-files.md) |
| **Catalog and birthright access** | Applications, entitlements and business roles, plus rules such as "Engineering gets the Engineer role". [Docs](docs/access.md) |
| **Connectors** | SCIM and LDAP / Active Directory: reads accounts, links them to people, flags orphans, and can provision changes back (off by default, with a dry run). Applications with no API load their accounts from a CSV export. [Docs](docs/connectors.md) |
| **Access requests** | People ask for access from the catalog; their manager and the application owner approve it, optionally until a date. [Docs](docs/access-requests.md) |
| **Access reviews** | Campaigns where managers or owners keep or revoke the access their people hold. [Docs](docs/access-reviews.md) |
| **Separation of duties** | Policies such as "nobody both creates and approves payments", detected every 30 minutes or prevented at request time, with time-boxed exceptions. [Docs](docs/separation-of-duties.md) |
| **Web console and emails** | One app for employees, managers, owners and admins, plus email notifications. [Docs](docs/console.md), [emails](docs/notifications.md) |
| **Grantra Login** | Single sign-on for your apps, MFA and passkeys (required only for people with sensitive access, if you like), sign-in with Entra ID, Okta, OIDC or SAML, and migration one email domain at a time. [Docs](docs/grantra-login.md) |
| **Authorization for your apps** | Your apps ask Grantra "may this person do this?" through an API, answered by Cedar policies with the person's department, roles and access filled in. [Docs](docs/app-authorization.md) |
| **Credential vault** | Passwords and SSH keys of privileged accounts, encrypted, checked out with a reason by the people who hold access to them, and rotated in Active Directory, LDAP and Grantra Login after use or on a schedule. Finds privileged and service accounts that belong in it. Break glass for emergencies, always alerted and reviewed. The master key can stay in a cloud KMS, OpenBao/Vault Transit or an HSM. [Docs](docs/vault.md) |
| **SSH access** | Short-lived SSH certificates for the servers people hold access to, valid only while that access lasts: no personal keys on servers. A gateway records every session for playback and indexes the commands typed, so you can search who ran `DROP TABLE` where. [Docs](docs/ssh.md) |
| **Remote access** | PostgreSQL, MySQL and Kubernetes through the same gateway, with a token that lasts only as long as the access; Windows desktops in the browser (RDP, through Apache Guacamole's guacd), recorded; AWS, Azure and Google Cloud consoles signed in with a role that Grantra takes back. Nobody sees a password or key; every query and cluster request is logged and searchable, and sessions end when the access does. [Docs](docs/gateways.md) |
| **Just-in-time access** | Zero standing privileges: people are eligible for privileged access and activate it for a few hours with a reason, with or without approval; it ends on its own. A report finds standing privileged access and converts it. [Docs](docs/just-in-time.md) |
| **Threat detection** | Alerts for leavers who still sign in, impossible travel, password spray, MFA fatigue, high-risk access granted outside Grantra and more, with responses from the alert (end sessions, require MFA, suspend) or on their own. [Docs](docs/threat-detection.md) |
| **Attack paths and insights** | Who can reach your riskiest access and every way how, shared accounts and access given outside Grantra included; access nobody used in 90 days; keep or revoke suggestions in reviews, with why; and business roles found in what departments hold one by one. [Docs](docs/access-insights.md) |
| **Audit log** | Every change and decision, searchable. |
| **Policies as code** | Authorization inside Grantra runs on [Cedar](https://www.cedarpolicy.com/). |

<table>
  <tr>
    <td width="50%"><img src="site/screenshots/approvals.png" alt="A manager's approvals inbox with an access request to GitHub Org admins, its reason and its deadline"></td>
    <td width="50%"><img src="site/screenshots/separation-of-duties.png" alt="Separation of duties policies and an open violation, with a button to accept the risk"></td>
  </tr>
  <tr>
    <td align="center">Approvals</td>
    <td align="center">Separation of duties</td>
  </tr>
</table>

## Run it for real

- **Kubernetes:** the Helm chart in [`deploy/helm/grantra`](deploy/helm/grantra) runs Grantra with separate database roles for migrations and the app, and any number of replicas. See [docs/production.md](docs/production.md).
- **Observability:** traces, metrics and logs go to any OpenTelemetry backend, with a Grafana dashboard included. See [docs/observability.md](docs/observability.md).
- **Supply chain:** every release ships a multi-arch image on `ghcr.io/grantra-dev/grantra` (and `ghcr.io/grantra-dev/grantra-login` for Grantra Login) and binaries for Linux, macOS and Windows, all signed with cosign (keyless) and shipped with an SPDX SBOM. See [docs/releases.md](docs/releases.md).

## Roadmap

1. **Foundations and identity governance** *(done in v0.1)*.
2. **Integrated IAM** *(done in v0.2)*: Grantra Login managed from the console, single sign-on and MFA policies, gradual migration from another identity provider, and authorization for your apps.
3. **Privileged access (PAM)** *(done in v0.3)*: credential vault, just-in-time access, break glass, SSH certificates and session recording, KMS/HSM keys, a gateway for databases, Kubernetes, desktops (RDP) and cloud consoles.
4. **Identity threat detection** *(next)* and AI-assisted recommendations.

The plan lives in the [GitHub milestones](https://github.com/grantra-dev/grantra/milestones).

## Repository layout

| Path | What lives there |
|---|---|
| `core/` | Go backend (modular monolith): API, identity model, migrations |
| `web/` | React console ([docs](docs/console.md)) |
| `deploy/` | docker-compose dev stack, Keycloak realm, Helm chart, Grafana dashboard |
| `docs/` | Guides, architecture notes and design decisions ([architecture](docs/architecture.md)) |
| `site/` | The grantra.dev landing page |

## Development

Requires Go 1.26+ (CI builds with the toolchain pinned in `core/go.mod`) and Node 22+ for the console.

```sh
make build     # console + core binary in bin/grantra
make test      # unit tests
make lint      # go vet + gofmt
make web-lint  # console lint + typecheck
make web-test  # console tests
make test-db GRANTRA_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable
```

See [CONTRIBUTING.md](CONTRIBUTING.md) to get involved, [SECURITY.md](SECURITY.md) to report a vulnerability, and [CHANGELOG.md](CHANGELOG.md) for what changed in each release.

## License

[Apache 2.0](LICENSE)
