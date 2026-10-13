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
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector"
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
	run := Run{ApplicationID: applicationID, Kind: "aggregation", Actor: actor, StartedAt: time.Now(), Counts: map[string]int{}}
	if !db.ValidUUID(applicationID) {
		return run, apperr.ErrNotFound
	}
	var (
		kind   string
		config json.RawMessage
	)
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT connector, config FROM applications WHERE id = $1`, applicationID).Scan(&kind, &config)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return run, apperr.ErrNotFound
	}
	if err != nil {
		return run, err
	}
	if kind == manualConnector || kind == builtinConnector {
		return run, apperr.Invalidf("this application has no connector to read")
	}

	var (
		snap    connector.Snapshot
		failure string
	)
	reader, err := connector.New(kind, config, secrets)
	if err == nil {
		rctx, cancel := context.WithTimeout(ctx, readTimeout)
		snap, err = reader.Read(rctx)
		cancel()
	}
	if err != nil {
		if ctx.Err() != nil {
			return run, ctx.Err()
		}
		failure = err.Error()
	}
	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if failure == "" {
			var refusal *refused
			switch err := store(ctx, tx, tenantID, applicationID, snap, run.Counts); {
			case errors.As(err, &refusal):
				// Throw away anything written before the refusal.
				if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT aggregation`); err != nil {
					return err
				}
				failure = refusal.msg
			case err != nil:
				return err
			}
		}
		run.Status = RunSucceeded
		if failure != "" {
			run.Status, run.Error, run.Counts = RunFailed, failure, map[string]int{}
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO connector_runs (tenant_id, application_id, kind, actor, status, counts, error, started_at)
			 VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8) RETURNING id, finished_at`,
			tenantID, applicationID, run.Kind, actor, run.Status, run.Counts, run.Error, run.StartedAt).
			Scan(&run.ID, &run.FinishedAt); err != nil {
			return err
		}
		details := map[string]any{"run_id": run.ID, "status": run.Status, "counts": run.Counts}
		if run.Error != "" {
			details["error"] = run.Error
		}
		return db.Audit(ctx, tx, tenantID, actor, "application.aggregated", "application", applicationID, details)
	})
	return run, err
}

// refused is an aggregation that read fine but looks too wrong to store.
type refused struct{ msg string }

func (e *refused) Error() string { return e.msg }

type existing struct {
	id, identity, correlatedBy, status string
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

// store writes a snapshot. counts collects what changed.
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

	// Entitlements.
	entID := map[string]string{}
	for _, e := range snap.Entitlements {
		var (
			id      string
			created bool
		)
		if err := tx.QueryRow(ctx,
			`INSERT INTO entitlements (tenant_id, application_id, native_id, name, description)
			 VALUES ($1, $2, $3, $4, nullif($5, ''))
			 ON CONFLICT (application_id, native_id)
			   DO UPDATE SET name = EXCLUDED.name, description = coalesce(EXCLUDED.description, entitlements.description), missing_since = NULL
			 RETURNING id, xmax = 0`, tenantID, appID, e.NativeID, e.Name, e.Description).Scan(&id, &created); err != nil {
			return fmt.Errorf("store entitlement %s: %w", e.NativeID, err)
		}
		entID[e.NativeID] = id
		if created {
			counts["entitlements_new"]++
		}
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

	seen := map[string]bool{}
	for _, a := range snap.Accounts {
		if a.NativeID == "" || seen[a.NativeID] {
			counts["accounts_skipped"]++
			continue
		}
		seen[a.NativeID] = true
		prev, known := old[a.NativeID]
		identity, by := prev.identity, prev.correlatedBy
		// Links made by hand or by provisioning are decisions, not guesses.
		if by != ByHand && by != ByProvisioning {
			identity, by = cand.match(a)
		}
		var ident, corr *string
		if identity != "" {
			ident = &identity
		}
		if by != "" {
			corr = &by
		}
		status := "disabled"
		if a.Active {
			status = "active"
		}
		attrs := map[string]string{}
		for k, v := range a.Attributes {
			attrs[k] = v
		}
		for k, v := range map[string]string{"display_name": a.DisplayName, "email": a.Email, "external_id": a.ExternalID} {
			if v != "" {
				attrs[k] = v
			}
		}
		var accountID string
		if err := tx.QueryRow(ctx,
			`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username, status, attributes, last_seen_at, correlated_by)
			 VALUES ($1, $2, $3, $4, nullif($5, ''), $6, $7, now(), $8)
			 ON CONFLICT (application_id, native_id) DO UPDATE SET
			   identity_id = EXCLUDED.identity_id, username = EXCLUDED.username, status = EXCLUDED.status,
			   attributes = EXCLUDED.attributes, last_seen_at = now(), correlated_by = EXCLUDED.correlated_by
			 RETURNING id`, tenantID, appID, ident, a.NativeID, a.Username, status, attrs, corr).Scan(&accountID); err != nil {
			return fmt.Errorf("store account %s: %w", a.NativeID, err)
		}
		switch {
		case !known:
			counts["accounts_new"]++
		case prev.status != status:
			counts["accounts_"+status]++
		}
		if identity == "" {
			counts["orphans"]++
		} else if prev.identity != identity {
			counts["correlated"]++
		}

		want := []string{}
		for _, n := range a.Entitlements {
			if id, ok := entID[n]; ok {
				want = append(want, id)
			} else {
				counts["unknown_memberships"]++
			}
		}
		tag, err := tx.Exec(ctx,
			`DELETE FROM account_entitlements WHERE account_id = $1 AND NOT (entitlement_id = ANY($2::uuid[]))`, accountID, want)
		if err != nil {
			return err
		}
		counts["memberships_removed"] += int(tag.RowsAffected())
		tag, err = tx.Exec(ctx,
			`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id)
			 SELECT $1, $2, unnest($3::uuid[]) ON CONFLICT DO NOTHING`, tenantID, accountID, want)
		if err != nil {
			return err
		}
		counts["memberships_added"] += int(tag.RowsAffected())
	}
	counts["accounts"] = len(seen)

	for native, e := range old {
		if seen[native] || e.status == "deleted" {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE accounts SET status = 'deleted' WHERE id = $1`, e.id); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `DELETE FROM account_entitlements WHERE account_id = $1`, e.id)
		if err != nil {
			return err
		}
		counts["accounts_deleted"]++
		counts["memberships_removed"] += int(tag.RowsAffected())
	}
	_, err = tx.Exec(ctx, `RELEASE SAVEPOINT aggregation`)
	return err
}

// AggregateDue aggregates every application with a connector whose last
// aggregation started more than every ago, or that was never aggregated. It
// returns the runs it made; one application failing does not stop the others.
func AggregateDue(ctx context.Context, pool *pgxpool.Pool, tenantID string, every time.Duration, secrets connector.SecretResolver) ([]Run, error) {
	var due []string
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT a.id FROM applications a
			 WHERE a.connector NOT IN ($1, $2)
			   AND NOT EXISTS (SELECT 1 FROM connector_runs r
			                   WHERE r.application_id = a.id AND r.kind = 'aggregation' AND r.started_at > now() - make_interval(secs => $3))
			   AND NOT EXISTS (SELECT 1 FROM connector_runs r WHERE r.application_id = a.id AND r.status = 'running')
			 ORDER BY a.name`, manualConnector, builtinConnector, every.Seconds())
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
