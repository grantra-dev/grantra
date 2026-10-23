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
  - Audit events name a person acting with `authz.Subject.Actor()` or `db.IdentityActor(id)`; in SQL, the identity behind an `actor` column is `db.ActorIdentitySQL` and a text id becomes a uuid safely with `db.IdentityIDSQL`.
  - Queries use the helpers in `core/internal/db`: `QueryOne` for a row that may not exist (it answers `apperr.ErrNotFound`), `SearchPattern` for a search box (`ContainsPattern` against `lower(column)` when no validation is needed), `SendBatch` with `QueueAudit` for many changes at once (one round trip per 1,000), and `WriteRows` to stream something large such as a recording.
  - "Does this person hold this access right now?" is `catalog.HeldSQL` (or `HeldWindowSQL` for how long), and who does is `catalog.Holders`; Grantra's own applications (vault, SSH, remote access) come from `catalog.EnsureApplication`. "Is this high or critical risk?" is `catalog.RiskySQL`, and "does this account hold such access right now?" is `catalog.AccountRiskySQL`. "Can people ask for this entitlement?" (not built in, not retired, still listed by its application) is `catalog.RequestableSQL`; a new table pointing at entitlements or applications goes in `entitlementUses` (`core/internal/catalog/lifecycle.go`) or the list in its test, so deleting one never loses history.
  - `db.IsUniqueViolation(err)` turns a duplicate name into an `apperr.Conflict`; uploaded CSV files are read with `core/internal/csvfile` (loose column names, mappings, row by row) and CSV files people download are written with `csvfile.Write` (byte order mark for Excel, cells that look like formulas made safe); secrets for programs (API keys, service tokens) come from `core/internal/apikey`.
  - Lists sent as JSON go through `httpjson.NonNil` so an empty one is `[]`, not `null`; text from outside is shortened with `textutil.Clip` (it never cuts a character in half) and counts are written with `textutil.Plural`.
  - Endpoints that scripts call take a service token through `sessionOrToken` and check the caller with `caller` (`core/internal/console/tokens.go`).
  - Work that spans several transactions and must never run twice at once (a scan that can also be started by hand) runs under `db.TryLocked`; background jobs already take their own lock. Whether a tenant has a console module on is `db.ModuleOn`.
  - A queue of rows for a job to look at once is a boolean column and a partial index on the rows still to do, read with `FOR UPDATE SKIP LOCKED` (as `signin_events.evaluated` and `account_entitlements.threat_checked`), not a cursor on a time or an id: a transaction that commits late writes rows with a time and id before rows already read.
  - Compare a uuid column with a uuid (`col = $1::uuid`, or `nullif($1, '')::uuid` for an optional filter), never `col::text = $1`: the cast keeps Postgres from using the column's index.
  - Privileged targets (vault accounts, SSH servers, remote access targets) are listed with `List(ctx, tx, identity, onlyHeld)`, which marks what the person holds in the same query.
  - A time limit, reminder or threshold a tenant may want to change is a setting in `core/internal/settings/registry.go`, read with `settings.Get` / `settings.Values` or inlined in SQL with `settings.SQL`, never a constant or an environment variable; it shows up under Settings → General on its own ([docs/settings.md](docs/settings.md)).
  - Who approves a request is `approvals.Resolve` (what a draft policy would do is `ResolveWith`); the console's previews go through `requests.Preview`, which adds separation of duties conflicts. A request's flow is shown with `ApprovalFlow`, a plan with `PlanSteps` (`web/src/components/ApprovalFlow.tsx`). A picker of roles and entitlements uses `useAccessOptions` and `refOf` (`web/src/lib/admin.ts`) with `<AccessOptions>`, or `<AccessPicker>` for a multi-select (`web/src/pages/admin/shared.tsx`), and the picked values become ids with `idsOf`; roles and birthright rules are edited with `RoleForm` and `RuleForm` (`web/src/pages/admin/AccessEditors.tsx`), wherever they are written; test fixtures of request steps use `step()`, and a signed-in admin `adminMe`, from `web/src/test-utils.tsx`.
  - A connector that calls a web API builds on `core/internal/connector/httpapi` (`BaseURL` to check an address, `At` for the API's root under it, `New` or `NewOAuth2` for the client, `Basic`, `Bearer` or `Header` to sign in, `Pages`, `LinkPages` or `AllLinkPages` for lists, `Ignore` for "already done" answers) and `connector.DecodeConfig`, `connector.Secret` and `connector.Builder`; its tests fake the API with `connector/connectortest`. An HR system is a `lifecycle.Feed`, registered with `lifecycle.RegisterFeed`, answering rows that are read like a file; JSON values become text with `lifecycle.Text`.
  - Web console calls go through `api` and `send` in `web/src/lib/api.ts`. A failed query or change is shown with `ErrorAlert` (`web/src/components/ui/alert.tsx`), and a form ends with `FormActions` (error, submit, Cancel). Settings typed for a connection (an application or an HR system) use `useConnection` (`web/src/lib/connectors.ts`) with `ConnectionFields` (`web/src/pages/admin/ConnectorSettings.tsx`). Filter boxes use `FilterBox` and match with `hasWords` (`web/src/lib/utils.ts`); gateway session lists and searches of what was typed use `SessionList` and `RecordedSearch` (`web/src/components/Sessions.tsx`); someone's access is shown with `AccessTable`. A list the API gives page by page uses `usePages` (`web/src/lib/hooks.ts`) with `<ShowMore>` under it; picking a person uses `PersonSearch`, or `PickPerson` for one person shown once picked (`web/src/components/PersonSearch.tsx`); identifiers shown as words use `humanize` and `capitalize` (`web/src/lib/utils.ts`); a risk picker is a `Select` with `<RiskOptions />` (`web/src/components/RiskBadge.tsx`); a numbered flow shows where it is with `Steps` (`web/src/components/Steps.tsx`). In Go, the display name of an owner column comes from `catalog.ownerName`.
- Sign off your commits (`git commit -s`) to certify the [Developer Certificate of Origin](https://developercertificate.org/).

## Code of conduct

By taking part you agree to follow our [Code of Conduct](CODE_OF_CONDUCT.md).
