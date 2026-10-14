# Grantra

**Open-source identity security: governance (IGA), access management (IAM) and privileged access (PAM) in one platform.**

Grantra aims to be an open alternative to SailPoint and CyberArk. It starts with identity governance: an inventory of who has access to what, joiner/mover/leaver automation, access requests and access reviews. It integrates with the identity provider you already run (Keycloak, Entra ID, Okta) instead of replacing it.

> ⚠️ Early days. Nothing here is production-ready yet.

## Roadmap

1. **Foundations**: core data model, multi-tenancy, audit log, API, CI, console login and platform roles.
2. **Identity Hub + IGA** *(in progress)*: HR sources and joiner/mover/leaver automation ([docs](docs/identity-lifecycle.md)), catalog and birthright access ([docs](docs/access.md)), SCIM and LDAP / Active Directory connectors with account correlation and provisioning ([docs](docs/connectors.md)), access requests with approvals ([docs](docs/access-requests.md)), access reviews ([docs](docs/access-reviews.md)) and separation of duties ([docs](docs/separation-of-duties.md)); more connectors (Entra ID…) next.
3. **Integrated IAM**: Keycloak bundled and preconfigured, swappable for your own IdP.
4. **PAM**: credential vault, just-in-time access, session recording.
5. **Identity threat detection** and AI-assisted recommendations.

## Repository layout

| Path | What lives there |
|---|---|
| `core/` | Go backend (modular monolith): API, identity model, migrations |
| `web/` | React console ([docs](docs/console.md)) |
| `deploy/` | docker-compose dev stack, Keycloak realm, later the Helm chart |
| `docs/` | Architecture notes and design decisions |

Planned: `connectors/` and `sdk/`.

## Quick start

Requires Docker.

```sh
make up
```

| Service | URL | Dev credentials |
|---|---|---|
| Grantra console | http://localhost:8080 | Sign in with `admin` / `admin`, or as a demo person with `demo` |
| Grantra API | http://localhost:8080/healthz | |
| Grantra Login (Keycloak) | http://localhost:8081 | `admin` / `admin` (realm `grantra`, user `admin` / `admin`) |
| PostgreSQL | localhost:5432 | `grantra_app` / `grantra` |
| Mailpit (emails Grantra sends) | http://localhost:8025 | |

The stack loads a small sample company. [docs/demo.md](docs/demo.md) walks through a joiner, an access request and its approval, a separation of duties block, an access review and a leaver in five minutes. `make reset` starts over.

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

See [CONTRIBUTING.md](CONTRIBUTING.md) to get involved and [SECURITY.md](SECURITY.md) to report a vulnerability.

## License

[Apache 2.0](LICENSE)
