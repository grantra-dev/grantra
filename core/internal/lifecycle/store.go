package lifecycle

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
	"github.com/grantra-dev/grantra/core/internal/db"
)

// ErrNotFound is returned for a source that does not exist.
var ErrNotFound = apperr.ErrNotFound

// ErrNameTaken is returned when creating a source with a name in use.
var ErrNameTaken error = &apperr.Conflict{Msg: "a source with this name already exists"}

// Kinds of source Grantra can read.
const KindCSV = "csv"

// Source is a configured identity source.
type Source struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Config    Config    `json:"config"`
	CreatedAt time.Time `json:"created_at"`
	LastRun   *Run      `json:"last_run,omitempty"`
}

// Run is one sync of a source.
type Run struct {
	ID        string       `json:"id"`
	SourceID  string       `json:"source_id"`
	Actor     string       `json:"actor"`
	DryRun    bool         `json:"dry_run"`
	Status    string       `json:"status"`
	Counts    map[Kind]int `json:"counts"`
	Errors    []Issue      `json:"errors"`
	Warnings  []Issue      `json:"warnings"`
	CreatedAt time.Time    `json:"created_at"`
}

// Run statuses.
const (
	RunPlanned = "planned"
	RunApplied = "applied"
	RunInvalid = "invalid"
	RunRefused = "refused"
)

// CreateSource adds an identity source.
func CreateSource(ctx context.Context, tx pgx.Tx, tenantID, name, kind string, cfg Config) (Source, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return Source{}, &apperr.Invalid{Msg: "name must be between 1 and 200 characters"}
	}
	if kind != KindCSV {
		return Source{}, &apperr.Invalid{Msg: `kind must be "csv"`}
	}
	if err := cfg.Validate(); err != nil {
		return Source{}, &apperr.Invalid{Msg: err.Error()}
	}
	s := Source{Name: name, Kind: kind, Config: cfg}
	err := tx.QueryRow(ctx,
		`INSERT INTO identity_sources (tenant_id, name, kind, config) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, name) DO NOTHING
		 RETURNING id, created_at`, tenantID, name, kind, cfg).Scan(&s.ID, &s.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNameTaken
	}
	return s, err
}

const sourceColumns = `s.id, s.name, s.kind, s.config, s.created_at,
	r.id, r.actor, r.dry_run, r.status, r.counts, r.errors, r.warnings, r.created_at`

const sourceFrom = ` FROM identity_sources s
	LEFT JOIN LATERAL (
	  SELECT * FROM identity_source_runs WHERE source_id = s.id ORDER BY created_at DESC LIMIT 1
	) r ON true`

func scanSource(row pgx.CollectableRow) (Source, error) {
	var (
		s   Source
		run struct {
			ID, Actor, Status *string
			DryRun            *bool
			Counts            map[Kind]int
			Errors, Warnings  []Issue
			CreatedAt         *time.Time
		}
	)
	err := row.Scan(&s.ID, &s.Name, &s.Kind, &s.Config, &s.CreatedAt,
		&run.ID, &run.Actor, &run.DryRun, &run.Status, &run.Counts, &run.Errors, &run.Warnings, &run.CreatedAt)
	if err != nil {
		return s, err
	}
	if run.ID != nil {
		s.LastRun = &Run{ID: *run.ID, SourceID: s.ID, Actor: *run.Actor, DryRun: *run.DryRun, Status: *run.Status,
			Counts: run.Counts, Errors: run.Errors, Warnings: run.Warnings, CreatedAt: *run.CreatedAt}
	}
	return s, nil
}

// ListSources returns every source with its latest run.
func ListSources(ctx context.Context, tx pgx.Tx) ([]Source, error) {
	rows, err := tx.Query(ctx, `SELECT `+sourceColumns+sourceFrom+` ORDER BY s.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSource)
}

// GetSource returns one source with its latest run.
func GetSource(ctx context.Context, tx pgx.Tx, id string) (Source, error) {
	if !db.ValidUUID(id) {
		return Source{}, ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT `+sourceColumns+sourceFrom+` WHERE s.id = $1`, id)
	if err != nil {
		return Source{}, err
	}
	s, err := pgx.CollectExactlyOneRow(rows, scanSource)
	if errors.Is(err, pgx.ErrNoRows) {
		return Source{}, ErrNotFound
	}
	return s, err
}

// ListRuns returns the latest runs of a source, newest first.
func ListRuns(ctx context.Context, tx pgx.Tx, sourceID string, limit int) ([]Run, error) {
	if _, err := GetSource(ctx, tx, sourceID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx,
		`SELECT id, source_id, actor, dry_run, status, counts, errors, warnings, created_at
		 FROM identity_source_runs WHERE source_id = $1 ORDER BY created_at DESC LIMIT $2`, sourceID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Run, error) {
		var r Run
		err := row.Scan(&r.ID, &r.SourceID, &r.Actor, &r.DryRun, &r.Status, &r.Counts, &r.Errors, &r.Warnings, &r.CreatedAt)
		return r, err
	})
}

// Event is a lifecycle event: someone joined, moved, left or came back.
type Event struct {
	ID          int64           `json:"id"`
	IdentityID  string          `json:"identity_id"`
	DisplayName string          `json:"display_name"`
	Kind        Kind            `json:"kind"`
	RunID       string          `json:"run_id,omitempty"`
	Details     json.RawMessage `json:"details"`
	EffectiveAt time.Time       `json:"effective_at"`
	CreatedAt   time.Time       `json:"created_at"`
}

// ListEvents pages backwards from the newest event. before is 0 for the first
// page; identityID, when not empty, keeps one person's events.
func ListEvents(ctx context.Context, tx pgx.Tx, identityID string, before int64, limit int) ([]Event, error) {
	rows, err := tx.Query(ctx,
		`SELECT e.id, e.identity_id, i.display_name, e.kind, coalesce(e.run_id::text, ''), e.changes, e.effective_at, e.created_at
		 FROM lifecycle_events e JOIN identities i ON i.id = e.identity_id
		 WHERE ($1 = 0 OR e.id < $1) AND ($2 = '' OR e.identity_id = $2::uuid)
		 ORDER BY e.id DESC LIMIT $3`, before, identityID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var e Event
		err := row.Scan(&e.ID, &e.IdentityID, &e.DisplayName, &e.Kind, &e.RunID, &e.Details, &e.EffectiveAt, &e.CreatedAt)
		return e, err
	})
}

// Options control a sync.
type Options struct {
	// DryRun plans without changing anything.
	DryRun bool
	// Force applies a sync that breaks the leaver safety limit.
	Force bool
	// Actor is who started the sync, as written in the audit log.
	Actor string
	// Now is the time of the sync; its UTC date decides which start and end
	// dates have passed.
	Now time.Time
}

// SyncResult is the run with the full plan behind it.
type SyncResult struct {
	Run
	Changes   []Change `json:"changes"`
	Unchanged int      `json:"unchanged"`
}

// maxStoredIssues caps the errors and warnings kept with a run; the full list
// is in the response to the sync.
const maxStoredIssues = 500

// Sync reads a feed for the source and, unless it is a dry run or the plan has
// errors or breaks the leaver limit, applies it. Every outcome is recorded as
// a run. It must run in a tenant transaction.
func Sync(ctx context.Context, tx pgx.Tx, tenantID, sourceID string, feed io.Reader, opts Options) (SyncResult, error) {
	if !db.ValidUUID(sourceID) {
		return SyncResult{}, ErrNotFound
	}
	// Locking the source keeps two syncs of it from interleaving.
	var (
		kind string
		cfg  Config
	)
	err := tx.QueryRow(ctx, `SELECT kind, config FROM identity_sources WHERE id = $1 FOR UPDATE`, sourceID).Scan(&kind, &cfg)
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncResult{}, ErrNotFound
	}
	if err != nil {
		return SyncResult{}, err
	}
	if err := cfg.Validate(); err != nil {
		return SyncResult{}, fmt.Errorf("source %s has a bad config: %w", sourceID, err)
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}

	res := SyncResult{Run: Run{SourceID: sourceID, Actor: opts.Actor, DryRun: opts.DryRun}}
	var plan Result
	records, issues, perr := ParseCSV(feed, cfg)
	switch {
	case perr != nil:
		plan = Result{Errors: []Issue{{Message: perr.Error()}}, Warnings: []Issue{}}
	case len(issues) > 0:
		// Planning around bad lines would show their people as leavers.
		plan = Result{Errors: issues, Warnings: []Issue{}}
	default:
		current, err := loadCurrent(ctx, tx)
		if err != nil {
			return SyncResult{}, err
		}
		plan = Plan(sourceID, cfg, records, current, opts.Now)
	}
	res.Changes, res.Unchanged = nonNil(plan.Changes), plan.Unchanged
	res.Errors, res.Warnings, res.Counts = plan.Errors, plan.Warnings, plan.Counts()

	switch {
	case len(plan.Errors) > 0:
		res.Status = RunInvalid
	case !opts.Force && plan.TooManyLeavers(cfg.MaxLeaverPercent):
		res.Status = RunRefused
		res.Errors = append(res.Errors, Issue{Message: fmt.Sprintf(
			"%d of %d active people would become leavers, more than the %d%% limit; check the file, or force the sync if this is right",
			res.Counts[Leaver], plan.Active, cfg.MaxLeaverPercent)})
	case opts.DryRun:
		res.Status = RunPlanned
	default:
		res.Status = RunApplied
	}

	err = tx.QueryRow(ctx,
		`INSERT INTO identity_source_runs (tenant_id, source_id, actor, dry_run, status, counts, errors, warnings)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id, created_at`,
		tenantID, sourceID, opts.Actor, opts.DryRun, res.Status, res.Counts,
		capIssues(res.Errors), capIssues(res.Warnings)).Scan(&res.ID, &res.CreatedAt)
	if err != nil {
		return SyncResult{}, fmt.Errorf("record run: %w", err)
	}
	if res.Status == RunApplied {
		if err := apply(ctx, tx, tenantID, sourceID, res.ID, res.Changes, opts); err != nil {
			return SyncResult{}, err
		}
	}
	err = db.Audit(ctx, tx, tenantID, opts.Actor, "identity_source.synced", "identity_source", sourceID,
		map[string]any{"run_id": res.ID, "status": res.Status, "dry_run": opts.DryRun, "forced": opts.Force, "counts": res.Counts})
	return res, err
}

func loadCurrent(ctx context.Context, tx pgx.Tx) ([]Current, error) {
	rows, err := tx.Query(ctx,
		`SELECT i.id, coalesce(i.source_id::text, ''), coalesce(i.external_id, ''), i.kind, i.status,
		        i.display_name, coalesce(i.email, ''), coalesce(m.external_id, ''), i.start_date, i.end_date, i.attributes
		 FROM identities i LEFT JOIN identities m ON m.id = i.manager_id
		 FOR UPDATE OF i`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Current, error) {
		var (
			c     Current
			attrs map[string]any
		)
		err := row.Scan(&c.ID, &c.SourceID, &c.ExternalID, &c.Kind, &c.Status, &c.DisplayName, &c.Email,
			&c.ManagerExternalID, &c.StartDate, &c.EndDate, &attrs)
		c.Attributes = make(map[string]string, len(attrs))
		for k, v := range attrs {
			if s, ok := v.(string); ok {
				c.Attributes[k] = s
			} else {
				b, _ := json.Marshal(v)
				c.Attributes[k] = string(b)
			}
		}
		return c, err
	})
}

var auditAction = map[Kind]string{
	Joiner: "identity.joined",
	Mover:  "identity.moved",
	Leaver: "identity.left",
	Rehire: "identity.rehired",
	Update: "identity.updated",
}

func apply(ctx context.Context, tx pgx.Tx, tenantID, sourceID, runID string, changes []Change, opts Options) error {
	today := dateOnly(opts.Now)
	// Employee id to identity id, to point managers at the right people once
	// every joiner exists.
	ids := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT external_id, id FROM identities WHERE external_id IS NOT NULL`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var ext, id string
		if err := rows.Scan(&ext, &id); err != nil {
			return err
		}
		ids[ext] = id
	}
	if err := rows.Err(); err != nil {
		return err
	}

	created := map[string]bool{}
	for i := range changes {
		ch := &changes[i]
		rec := ch.record
		switch {
		case rec == nil:
			// In the store but no longer in the feed.
			_, err = tx.Exec(ctx,
				`UPDATE identities SET status = 'terminated', end_date = coalesce(end_date, $2), updated_at = now()
				 WHERE id = $1`, ch.IdentityID, today)
		case ch.IdentityID == "":
			err = tx.QueryRow(ctx,
				`INSERT INTO identities (tenant_id, source_id, external_id, status, display_name, email, start_date, end_date, attributes)
				 VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7, $8, $9) RETURNING id`,
				tenantID, sourceID, rec.ExternalID, ch.Status, rec.DisplayName, rec.Email,
				rec.StartDate, rec.EndDate, rec.Attributes).Scan(&ch.IdentityID)
			ids[rec.ExternalID] = ch.IdentityID
			created[ch.IdentityID] = true
		default:
			_, err = tx.Exec(ctx,
				`UPDATE identities SET source_id = $2, external_id = $3, status = $4, display_name = $5,
				        email = nullif($6, ''), start_date = $7, end_date = $8, attributes = $9, updated_at = now()
				 WHERE id = $1`,
				ch.IdentityID, sourceID, rec.ExternalID, ch.Status, rec.DisplayName, rec.Email,
				rec.StartDate, rec.EndDate, rec.Attributes)
			ids[rec.ExternalID] = ch.IdentityID
		}
		if err != nil {
			return fmt.Errorf("apply %s %s: %w", ch.Kind, ch.ExternalID, err)
		}
	}

	for i := range changes {
		ch := &changes[i]
		if rec := ch.record; rec != nil {
			_, managerChanged := ch.Changes[FieldManagerID]
			if managerChanged || (created[ch.IdentityID] && rec.ManagerExternalID != "") {
				var mgr *string
				if id, ok := ids[rec.ManagerExternalID]; ok && rec.ManagerExternalID != "" {
					mgr = &id
				}
				if _, err := tx.Exec(ctx, `UPDATE identities SET manager_id = $2 WHERE id = $1`, ch.IdentityID, mgr); err != nil {
					return fmt.Errorf("set manager of %s: %w", ch.ExternalID, err)
				}
			}
		}
		if err := record(ctx, tx, tenantID, "identity_source:"+sourceID, runID, ch, opts); err != nil {
			return err
		}
	}
	return nil
}

// record writes the lifecycle event and the audit event for one change, and
// ends a leaver's console sessions.
func record(ctx context.Context, tx pgx.Tx, tenantID, actor, runID string, ch *Change, opts Options) error {
	details := map[string]any{"external_id": ch.ExternalID, "status": ch.Status}
	if len(ch.Changes) > 0 {
		details["changes"] = ch.Changes
	}
	if ch.Reason != "" {
		details["reason"] = ch.Reason
	}
	if ch.Kind != Update {
		effective := opts.Now
		if (ch.Kind == Joiner || ch.Kind == Rehire) && ch.record != nil && ch.record.StartDate != nil {
			effective = *ch.record.StartDate
		}
		var run *string
		if runID != "" {
			run = &runID
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO lifecycle_events (tenant_id, identity_id, kind, run_id, changes, effective_at)
			 VALUES ($1, $2, $3, $4, $5, $6)`, tenantID, ch.IdentityID, ch.Kind, run, details, effective); err != nil {
			return fmt.Errorf("lifecycle event for %s: %w", ch.ExternalID, err)
		}
	}
	if ch.Kind == Leaver {
		if _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE identity_id = $1`, ch.IdentityID); err != nil {
			return err
		}
	}
	audit := map[string]any{"display_name": ch.DisplayName}
	for k, v := range details {
		audit[k] = v
	}
	if runID != "" {
		audit["run_id"] = runID
	}
	if opts.Actor != "" && opts.Actor != actor {
		audit["started_by"] = opts.Actor
	}
	if ch.Linked {
		audit["linked"] = true
	}
	return db.Audit(ctx, tx, tenantID, actor, auditAction[ch.Kind], "identity", ch.IdentityID, audit)
}

// ApplyDueDates carries out the start and end dates that passed since the
// last sync: pending people whose first day has come become active, and
// people past their last day become leavers. Syncs do the same; this covers
// the days in between.
func ApplyDueDates(ctx context.Context, pool *pgxpool.Pool, tenantID string, now time.Time) (activated, left int, err error) {
	today := dateOnly(now)
	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		activated, left = 0, 0
		rows, err := tx.Query(ctx,
			`UPDATE identities SET status = 'active', updated_at = now()
			 WHERE status = 'pending' AND start_date <= $1 AND (end_date IS NULL OR end_date >= $1)
			 RETURNING id, coalesce(external_id, ''), display_name`, today)
		if err != nil {
			return err
		}
		started, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dueRow])
		if err != nil {
			return err
		}
		for _, r := range started {
			if err := db.Audit(ctx, tx, tenantID, "system:lifecycle", "identity.activated", "identity", r.ID,
				map[string]any{"external_id": r.ExternalID, "display_name": r.DisplayName, "reason": "start date reached"}); err != nil {
				return err
			}
		}
		activated = len(started)

		rows, err = tx.Query(ctx,
			`WITH due AS (
			   SELECT id, status FROM identities
			   WHERE status <> 'terminated' AND end_date < $1 FOR UPDATE
			 )
			 UPDATE identities i SET status = 'terminated', updated_at = now()
			 FROM due WHERE i.id = due.id
			 RETURNING i.id, coalesce(i.external_id, ''), i.display_name, due.status`, today)
		if err != nil {
			return err
		}
		ended, err := pgx.CollectRows(rows, pgx.RowToStructByPos[dueLeaver])
		if err != nil {
			return err
		}
		for _, r := range ended {
			ch := &Change{Kind: Leaver, IdentityID: r.ID, ExternalID: r.ExternalID, DisplayName: r.DisplayName,
				Status: "terminated", Reason: ReasonEndDate, Changes: map[string]FieldChange{"status": {r.Status, "terminated"}}}
			if err := record(ctx, tx, tenantID, "system:lifecycle", "", ch, Options{Now: now}); err != nil {
				return err
			}
		}
		left = len(ended)
		return nil
	})
	return activated, left, err
}

type dueRow struct {
	ID, ExternalID, DisplayName string
}

type dueLeaver struct {
	ID, ExternalID, DisplayName, Status string
}

func capIssues(in []Issue) []Issue {
	if in == nil {
		return []Issue{}
	}
	if len(in) > maxStoredIssues {
		return append(in[:maxStoredIssues:maxStoredIssues], Issue{Message: fmt.Sprintf("and %d more", len(in)-maxStoredIssues)})
	}
	return in
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
