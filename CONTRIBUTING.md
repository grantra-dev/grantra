# Contributing to Grantra

Thanks for your interest! Grantra is young, so this is a great moment to shape it.

## Ways to help

- Try the dev stack and open an issue when something breaks or is unclear.
- Pick an issue labelled `good first issue` or `help wanted`.
- Propose bigger changes as an issue first (an RFC), so we agree on the design before code is written.

## Development workflow

1. Fork the repo and create a branch from `main`.
2. Make your change with tests. Run `make lint test` (and `make test-db` when touching the database).
3. Open a pull request describing what changed and why. CI must be green.
4. A maintainer reviews it. We squash-merge.

## Guidelines

- Go code is formatted with `gofmt` and must pass `go vet` and `staticcheck` (`make lint` runs all three).
- Database changes go in a new numbered file under `core/internal/db/migrations/`. Never edit a migration that has already been merged.
- Every tenant-owned table needs a `tenant_id` column and the tenant isolation policy.
- Never commit secrets, real credentials or customer data.
- Reuse the shared building blocks instead of copying code:
  - Domain packages return errors from `core/internal/apperr` (`ErrNotFound`, `Invalid`, `Conflict`); the API turns them into 404, 400 and 409.
  - Console handlers use the helpers in `core/internal/console/helpers.go` (`guardedOn`, `guardedOnIdentity`, `allowed`, `may` to show more or less by role, `decodeBody`, `pageLimit`, `respond`, `noContent`), so a handler only holds what is particular to its endpoint.
  - Audit events name a person acting with `authz.Subject.Actor()` or `db.IdentityActor(id)`.
  - Queries use the helpers in `core/internal/db`: `QueryOne` for a row that may not exist (it answers `apperr.ErrNotFound`), `SearchPattern` for a search box (`ContainsPattern` against `lower(column)` when no validation is needed), `SendBatch` with `QueueAudit` for many changes at once (one round trip per 1,000), and `WriteRows` to stream something large such as a recording.
  - "Does this person hold this access right now?" is `catalog.HeldSQL` (or `HeldWindowSQL` for how long), and who does is `catalog.Holders`; Grantra's own applications (vault, SSH, remote access) come from `catalog.EnsureApplication`.
  - `db.IsUniqueViolation(err)` turns a duplicate name into an `apperr.Conflict`; uploaded CSV files are read with `core/internal/csvfile` (loose column names, mappings, row by row); secrets for programs (API keys, service tokens) come from `core/internal/apikey`.
  - Endpoints that scripts call take a service token through `sessionOrToken` and check the caller with `caller` (`core/internal/console/tokens.go`).
  - Compare a uuid column with a uuid (`col = $1::uuid`, or `nullif($1, '')::uuid` for an optional filter), never `col::text = $1`: the cast keeps Postgres from using the column's index.
  - Privileged targets (vault accounts, SSH servers, remote access targets) are listed with `List(ctx, tx, identity, onlyHeld)`, which marks what the person holds in the same query.
  - Web console calls go through `api` and `send` in `web/src/lib/api.ts`. Filter boxes use `FilterBox` and match with `hasWords` (`web/src/lib/utils.ts`); gateway session lists and searches of what was typed use `SessionList` and `RecordedSearch` (`web/src/components/Sessions.tsx`); someone's access is shown with `AccessTable`.
- Sign off your commits (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).

## Code of conduct

By taking part you agree to follow our [Code of Conduct](CODE_OF_CONDUCT.md).
