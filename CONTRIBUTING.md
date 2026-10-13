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

- Go code is formatted with `gofmt` and must pass `go vet`.
- Database changes go in a new numbered file under `core/internal/db/migrations/`. Never edit a migration that has already been merged.
- Every tenant-owned table needs a `tenant_id` column and the tenant isolation policy.
- Never commit secrets, real credentials or customer data.
- Sign off your commits (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).

## Code of conduct

By taking part you agree to follow our [Code of Conduct](CODE_OF_CONDUCT.md).
