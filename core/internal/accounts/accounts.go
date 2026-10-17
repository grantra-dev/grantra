// Package accounts keeps Grantra's picture of what exists in each application
// up to date. An aggregation reads an application through its connector and
// stores its entitlements, its accounts and who holds what. Correlation then
// links each account to the identity it belongs to; accounts that match
// nobody are orphans, a classic audit finding.
package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/flatfile"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Connectors that are not real connectors: manual applications have nothing
// to read, and the built-in Grantra application is its own source of truth.
const (
	manualConnector  = "manual"
	builtinConnector = "grantra"
)

// Correlation outcomes stored in accounts.correlated_by.
const (
	ByExternalID = "rule:external_id"
	ByEmail      = "rule:email"
	ByUsername   = "rule:username"
	ByHand       = "manual"
	// ByProvisioning marks accounts Grantra created for an identity.
	ByProvisioning = "provisioned"
)

// SetConnector configures how Grantra reads an application. The config is
// checked by building the connector, so a bad URL or a missing secret fails
// here rather than at the first aggregation. kind "manual" removes the
// connector.
func SetConnector(ctx context.Context, tx pgx.Tx, applicationID, kind string, config json.RawMessage, secrets connector.SecretResolver) error {
	if !db.ValidUUID(applicationID) {
		return apperr.ErrNotFound
	}
	var current string
	err := tx.QueryRow(ctx, `SELECT connector FROM applications WHERE id = $1 FOR UPDATE`, applicationID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if current == builtinConnector || kind == builtinConnector {
		return apperr.Invalidf("the built-in Grantra application has no connector")
	}
	if len(config) == 0 || string(config) == "null" {
		config = json.RawMessage(`{}`)
	}
	if kind != manualConnector {
		if _, err := connector.New(kind, config, secrets); err != nil {
			return &apperr.Invalid{Msg: err.Error()}
		}
	} else {
		config = json.RawMessage(`{}`)
	}
	_, err = tx.Exec(ctx, `UPDATE applications SET connector = $2, config = $3 WHERE id = $1`, applicationID, kind, config)
	return err
}

// Run is one connector run against an application.
type Run struct {
	ID            string         `json:"id"`
	ApplicationID string         `json:"application_id"`
	Kind          string         `json:"kind"`
	Actor         string         `json:"actor"`
	Status        string         `json:"status"`
	Counts        map[string]int `json:"counts"`
	Error         string         `json:"error,omitempty"`
	StartedAt     time.Time      `json:"started_at"`
	// FinishedAt is empty while a provisioning run is still running.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// Run statuses.
const (
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	// Provisioning runs can also be dry runs (planned), partly fail, or be
	// refused by a safety limit.
	RunPlanned = "planned"
	RunPartial = "partial"
	RunRefused = "refused"
)

// readTimeout bounds how long an aggregation may spend reading.
const readTimeout = 5 * time.Minute

// Aggregate reads an application through its connector and stores what it
// found. A failed read is recorded as a failed run and changes nothing; the
// error is returned only when Grantra itself failed.
func Aggregate(ctx context.Context, pool *pgxpool.Pool, tenantID, applicationID, actor string, secrets connector.SecretResolver) (Run, error) {
	run := newRun(applicationID, actor)
	kind, config, err := connectorOf(ctx, pool, tenantID, applicationID)
	if err != nil {
		return run, err
	}
	switch kind {
	case manualConnector, builtinConnector:
		return run, apperr.Invalidf("this application has no connector to read")
	case flatfile.Kind:
		return run, &apperr.Invalid{Msg: flatfile.ErrUploadOnly.Error()}
	}

	var snap connector.Snapshot
	reader, err := connector.New(kind, config, secrets)
	if err == nil {
		rctx, cancel := context.WithTimeout(ctx, readTimeout)
		snap, err = reader.Read(rctx)
		cancel()
	}
	if err != nil && ctx.Err() != nil {
		return run, ctx.Err()
	}
	return finish(ctx, pool, tenantID, run, snap, err, false)
}

// Import reads an application's accounts from an uploaded file, for
// applications whose connector is "csv", and stores them like an
// aggregation. A dry run stores nothing and returns what would change as a
// planned run. A file Grantra cannot use is a failed run, like an
// application that cannot be read.
func Import(ctx context.Context, pool *pgxpool.Pool, tenantID, applicationID, actor string, file io.Reader, dryRun bool) (Run, error) {
	run := newRun(applicationID, actor)
	kind, config, err := connectorOf(ctx, pool, tenantID, applicationID)
	if err != nil {
		return run, err
	}
	if kind != flatfile.Kind {
		return run, apperr.Invalidf("only applications with the csv connector take account files")
	}
	snap, err := flatfile.Parse(config, file)
	return finish(ctx, pool, tenantID, run, snap, err, dryRun)
}

func newRun(applicationID, actor string) Run {
	return Run{ApplicationID: applicationID, Kind: "aggregation", Actor: actor, StartedAt: time.Now(), Counts: map[string]int{}}
}

func connectorOf(ctx context.Context, pool *pgxpool.Pool, tenantID, applicationID string) (kind string, config json.RawMessage, err error) {
	if !db.ValidUUID(applicationID) {
		return "", nil, apperr.ErrNotFound
	}
	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT connector, config FROM applications WHERE id = $1`, applicationID).Scan(&kind, &config)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = apperr.ErrNotFound
	}
	return kind, config, err
}

// finish stores what a read found, unless readErr says the read failed,
// and records the run. A dry run throws the changes away once counted.
func finish(ctx context.Context, pool *pgxpool.Pool, tenantID string, run Run, snap connector.Snapshot, readErr error, dryRun bool) (Run, error) {
	failure := ""
	if readErr != nil {
		failure = readErr.Error()
	}
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if failure == "" {
			var refusal *refused
			switch err := store(ctx, tx, tenantID, run.ApplicationID, snap, run.Counts); {
			case errors.As(err, &refusal):
				failure = refusal.msg
			case err != nil:
				return err
			}
			// Throw away anything written before a refusal, and everything
			// a dry run wrote.
			if failure != "" || dryRun {
				if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT aggregation`); err != nil {
					return err
				}
			}
		}
		run.Status = RunSucceeded
		switch {
		case failure != "":
			run.Status, run.Error, run.Counts = RunFailed, failure, map[string]int{}
		case dryRun:
			run.Status = RunPlanned
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO connector_runs (tenant_id, application_id, kind, actor, status, counts, error, started_at)
			 VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8) RETURNING id, finished_at`,
			tenantID, run.ApplicationID, run.Kind, run.Actor, run.Status, run.Counts, run.Error, run.StartedAt).
			Scan(&run.ID, &run.FinishedAt); err != nil {
			return err
		}
		details := map[string]any{"run_id": run.ID, "status": run.Status, "counts": run.Counts}
		if run.Error != "" {
			details["error"] = run.Error
		}
		return db.Audit(ctx, tx, tenantID, run.Actor, "application.aggregated", "application", run.ApplicationID, details)
	})
	return run, err
}

// refused is an aggregation that read fine but looks too wrong to store.
type refused struct{ msg string }

func (e *refused) Error() string { return e.msg }

type existing struct {
	id, identity, correlatedBy, status string
}

// pendingAccount is an account read from the application, ready to store.
type pendingAccount struct {
	connector.Account
	prev         existing
	known        bool
	identity, by string
	status       string
	attrs        map[string]string
	want         []string
}

type candidate struct {
	byExternal map[string]string
	byEmail    map[string][]string
}

func (c candidate) match(a connector.Account) (string, string) {
	if id, ok := c.byExternal[a.ExternalID]; ok && a.ExternalID != "" {
		return id, ByExternalID
	}
	if ids := c.byEmail[strings.ToLower(a.Email)]; len(ids) == 1 && a.Email != "" {
		return ids[0], ByEmail
	}
	if strings.Contains(a.Username, "@") {
		if ids := c.byEmail[strings.ToLower(a.Username)]; len(ids) == 1 {
			return ids[0], ByUsername
		}
	}
	return "", ""
}

// store writes a snapshot. counts collects what changed. Everything it
// writes comes after the savepoint "aggregation", for the caller to roll
// back to.
func store(ctx context.Context, tx pgx.Tx, tenantID, appID string, snap connector.Snapshot, counts map[string]int) error {
	if _, err := tx.Exec(ctx, `SAVEPOINT aggregation`); err != nil {
		return err
	}
	// One aggregation of an application at a time.
	if _, err := tx.Exec(ctx, `SELECT 1 FROM applications WHERE id = $1 FOR UPDATE`, appID); err != nil {
		return err
	}
	// A provisioning run is changing the application right now; what was
	// read may already be out of date.
	var provisioning bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM connector_runs WHERE application_id = $1 AND status = 'running')`, appID).Scan(&provisioning); err != nil {
		return err
	}
	if provisioning {
		return &refused{"a provisioning run of this application is in progress; aggregate again once it finishes"}
	}

	var active int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM accounts WHERE application_id = $1 AND status <> 'deleted'`, appID).Scan(&active); err != nil {
		return err
	}
	if len(snap.Accounts) == 0 && active > 0 {
		return &refused{fmt.Sprintf("the application returned no accounts, but %d were there last time; nothing was changed", active)}
	}

	// Entitlements, sent as one batch: a round trip per entitlement adds up
	// on applications with thousands of groups.
	entID := map[string]string{}
	err := db.SendBatch(ctx, tx, snap.Entitlements, func(b *pgx.Batch, e connector.Entitlement) {
		b.Queue(`INSERT INTO entitlements (tenant_id, application_id, native_id, name, description)
			 VALUES ($1, $2, $3, $4, nullif($5, ''))
			 ON CONFLICT (application_id, native_id)
			   DO UPDATE SET name = EXCLUDED.name, description = coalesce(EXCLUDED.description, entitlements.description), missing_since = NULL
			 RETURNING id, xmax = 0`, tenantID, appID, e.NativeID, e.Name, e.Description)
	}, func(br pgx.BatchResults, e connector.Entitlement) error {
		var (
			id      string
			created bool
		)
		if err := br.QueryRow().Scan(&id, &created); err != nil {
			return fmt.Errorf("store entitlement %s: %w", e.NativeID, err)
		}
		entID[e.NativeID] = id
		if created {
			counts["entitlements_new"]++
		}
		return nil
	})
	if err != nil {
		return err
	}
	counts["entitlements"] = len(snap.Entitlements)
	natives := make([]string, 0, len(entID))
	for n := range entID {
		natives = append(natives, n)
	}
	tag, err := tx.Exec(ctx,
		`UPDATE entitlements SET missing_since = now()
		 WHERE application_id = $1 AND missing_since IS NULL AND NOT (native_id = ANY($2))`, appID, natives)
	if err != nil {
		return err
	}
	counts["entitlements_missing"] = int(tag.RowsAffected())

	// Who accounts may belong to.
	cand := candidate{byExternal: map[string]string{}, byEmail: map[string][]string{}}
	rows, err := tx.Query(ctx, `SELECT id, coalesce(external_id, ''), lower(coalesce(email, '')) FROM identities`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id, ext, email string
		if err := rows.Scan(&id, &ext, &email); err != nil {
			return err
		}
		if ext != "" {
			cand.byExternal[ext] = id
		}
		if email != "" {
			cand.byEmail[email] = append(cand.byEmail[email], id)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	old := map[string]existing{}
	rows, err = tx.Query(ctx,
		`SELECT native_id, id, coalesce(identity_id::text, ''), coalesce(correlated_by, ''), status
		 FROM accounts WHERE application_id = $1 FOR UPDATE`, appID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			native string
			e      existing
		)
		if err := rows.Scan(&native, &e.id, &e.identity, &e.correlatedBy, &e.status); err != nil {
			return err
		}
		old[native] = e
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Work out every account first, then store them in batches: one
	// statement per account (the account and its memberships together)
	// instead of three round trips each.
	var pending []pendingAccount
	seen := map[string]bool{}
	for _, a := range snap.Accounts {
		if a.NativeID == "" || seen[a.NativeID] {
			counts["accounts_skipped"]++
			continue
		}
		seen[a.NativeID] = true
		p := pendingAccount{Account: a}
		p.prev, p.known = old[a.NativeID]
		p.identity, p.by = p.prev.identity, p.prev.correlatedBy
		// Links made by hand or by provisioning are decisions, not guesses.
		if p.by != ByHand && p.by != ByProvisioning {
			p.identity, p.by = cand.match(a)
		}
		p.status = "disabled"
		if a.Active {
			p.status = "active"
		}
		p.attrs = map[string]string{}
		for k, v := range a.Attributes {
			p.attrs[k] = v
		}
		for k, v := range map[string]string{"display_name": a.DisplayName, "email": a.Email, "external_id": a.ExternalID} {
			if v != "" {
				p.attrs[k] = v
			}
		}
		p.want = []string{}
		for _, n := range a.Entitlements {
			if id, ok := entID[n]; ok {
				p.want = append(p.want, id)
			} else {
				counts["unknown_memberships"]++
			}
		}
		pending = append(pending, p)
	}
	err = db.SendBatch(ctx, tx, pending, func(b *pgx.Batch, p pendingAccount) {
		// The memberships the account should no longer hold are deleted and
		// the missing ones added in the same statement as the account. The
		// two touch different rows, so they cannot step on each other.
		b.Queue(`WITH acc AS (
			   INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username, status, attributes, last_seen_at, correlated_by)
			   VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, now(), $8)
			   ON CONFLICT (application_id, native_id) DO UPDATE SET
			     identity_id = EXCLUDED.identity_id, username = EXCLUDED.username, status = EXCLUDED.status,
			     attributes = EXCLUDED.attributes, last_seen_at = now(), correlated_by = EXCLUDED.correlated_by
			   RETURNING id
			 ), removed AS (
			   DELETE FROM account_entitlements ae USING acc
			   WHERE ae.account_id = acc.id AND NOT (ae.entitlement_id = ANY($9::uuid[]))
			   RETURNING 1
			 ), added AS (
			   INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id)
			   SELECT $1, acc.id, unnest($9::uuid[]) FROM acc
			   ON CONFLICT DO NOTHING
			   RETURNING 1
			 )
			 SELECT (SELECT count(*) FROM removed), (SELECT count(*) FROM added)`,
			tenantID, appID, db.Nullable(p.identity), p.NativeID, p.Username, p.status, p.attrs, db.Nullable(p.by), p.want)
	}, func(br pgx.BatchResults, p pendingAccount) error {
		var removed, added int
		if err := br.QueryRow().Scan(&removed, &added); err != nil {
			return fmt.Errorf("store account %s: %w", p.NativeID, err)
		}
		switch {
		case !p.known:
			counts["accounts_new"]++
		case p.prev.status != p.status:
			counts["accounts_"+p.status]++
		}
		if p.identity == "" {
			counts["orphans"]++
		} else if p.prev.identity != p.identity {
			counts["correlated"]++
		}
		counts["memberships_removed"] += removed
		counts["memberships_added"] += added
		return nil
	})
	if err != nil {
		return err
	}
	counts["accounts"] = len(seen)

	var gone []string
	for native, e := range old {
		if !seen[native] && e.status != "deleted" {
			gone = append(gone, e.id)
		}
	}
	if len(gone) > 0 {
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status = 'deleted' WHERE id = ANY($1::uuid[])`, gone); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM account_entitlements WHERE account_id = ANY($1::uuid[])`, gone)
		if err != nil {
			return err
		}
		counts["accounts_deleted"] += len(gone)
		counts["memberships_removed"] += int(tag.RowsAffected())
	}
	return nil
}

// AggregateDue aggregates every application with a connector whose last
// aggregation started more than every ago, or that was never aggregated. It
// returns the runs it made; one application failing does not stop the others.
func AggregateDue(ctx context.Context, pool *pgxpool.Pool, tenantID string, every time.Duration, secrets connector.SecretResolver) ([]Run, error) {
	var due []string
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT a.id FROM applications a
			 WHERE a.connector NOT IN ($1, $2, $4)
			   AND NOT EXISTS (SELECT 1 FROM connector_runs r
			                   WHERE r.application_id = a.id AND r.kind = 'aggregation' AND r.started_at > now() - make_interval(secs => $3))
			   AND NOT EXISTS (SELECT 1 FROM connector_runs r WHERE r.application_id = a.id AND r.status = 'running')
			 ORDER BY a.name`, manualConnector, builtinConnector, every.Seconds(), flatfile.Kind)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, err
	}
	var runs []Run
	for _, id := range due {
		run, err := Aggregate(ctx, pool, tenantID, id, "system:aggregation", secrets)
		if err != nil {
			return runs, fmt.Errorf("aggregate %s: %w", id, err)
		}
		runs = append(runs, run)
	}
	return runs, nil
}
