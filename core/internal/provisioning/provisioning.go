// Package provisioning writes to applications so that what people hold there
// matches what Grantra says they should hold. It compares the assignments
// (desired state) with the accounts and memberships the last aggregation
// found (actual state), plans the difference and carries it out through the
// application's connector:
//
//   - people with access to an application but no account get one;
//   - disabled accounts of people who should have access are enabled;
//   - missing memberships are added, unwanted ones removed;
//   - accounts of people who left or are suspended are disabled.
//
// Provisioning is careful by default. It is off until an admin turns it on
// per application, only removes memberships Grantra granted itself unless told
// otherwise, never touches orphan accounts, needs a recent aggregation to plan
// from, and refuses runs that would remove or disable too much at once.
package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Modes.
const (
	ModeOff = "off"
	ModeOn  = "on"
)

// Settings is how Grantra provisions one application.
type Settings struct {
	// Mode is "off" (Grantra only reads the application) or "on".
	Mode string `json:"mode"`
	// RemoveUnassigned lets Grantra remove memberships it did not grant. Off
	// by default, so turning provisioning on never strips access people got
	// outside Grantra.
	RemoveUnassigned bool `json:"remove_unassigned,omitempty"`
	// MaxChangePercent refuses a run that would remove or disable more than
	// this share of the application's accounts. Default 10. Up to three such
	// changes always pass, so small applications are not stuck.
	MaxChangePercent int `json:"max_change_percent,omitempty"`
}

func (s *Settings) validate() error {
	switch s.Mode {
	case ModeOff, ModeOn:
	default:
		return apperr.Invalidf(`mode must be "off" or "on"`)
	}
	switch {
	case s.MaxChangePercent == 0:
		s.MaxChangePercent = 10
	case s.MaxChangePercent < 0 || s.MaxChangePercent > 100:
		return apperr.Invalidf("max_change_percent must be between 1 and 100")
	}
	return nil
}

// SetSettings configures provisioning for an application. Turning it on
// needs a connector that can write.
func SetSettings(ctx context.Context, tx pgx.Tx, applicationID string, s Settings, secrets connector.SecretResolver) (Settings, error) {
	if !db.ValidUUID(applicationID) {
		return s, apperr.ErrNotFound
	}
	if err := s.validate(); err != nil {
		return s, err
	}
	var (
		kind   string
		config json.RawMessage
	)
	err := tx.QueryRow(ctx, `SELECT connector, config FROM applications WHERE id = $1 FOR UPDATE`, applicationID).Scan(&kind, &config)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, apperr.ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if s.Mode == ModeOn {
		if _, err := writer(kind, config, secrets); err != nil {
			return s, err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE applications SET provisioning = $2 WHERE id = $1`, applicationID, s)
	return s, err
}

// writer builds an application's connector and checks that it can write.
func writer(kind string, config json.RawMessage, secrets connector.SecretResolver) (connector.Writer, error) {
	if kind == catalog.ManualConnector || kind == "grantra" {
		return nil, apperr.Invalidf("this application has no connector to provision with; set one up first")
	}
	c, err := connector.New(kind, config, secrets)
	if err != nil {
		return nil, &apperr.Invalid{Msg: err.Error()}
	}
	w, ok := c.(connector.Writer)
	if !ok {
		return nil, apperr.Invalidf("the %s connector can only read", kind)
	}
	return w, nil
}

// Operations a run carries out.
const (
	OpCreateAccount  = "create_account"
	OpEnableAccount  = "enable_account"
	OpDisableAccount = "disable_account"
	OpAddMember      = "add_member"
	OpRemoveMember   = "remove_member"
)

// Action outcomes.
const (
	ActionPlanned = "planned"
	ActionDone    = "done"
	ActionFailed  = "failed"
)

// Action is one change to an application.
type Action struct {
	Op         string `json:"op"`
	IdentityID string `json:"identity_id"`
	// AccountID is Grantra's id of the account; empty for an account the
	// run creates.
	AccountID         string `json:"account_id,omitempty"`
	NativeAccount     string `json:"native_account,omitempty"`
	Username          string `json:"username,omitempty"`
	EntitlementID     string `json:"entitlement_id,omitempty"`
	NativeEntitlement string `json:"native_entitlement,omitempty"`
	Status            string `json:"status"`
	Error             string `json:"error,omitempty"`
}

func (a Action) destructive() bool { return a.Op == OpRemoveMember || a.Op == OpDisableAccount }

// Run is a provisioning run: the connector run plus its actions.
type Run struct {
	accounts.Run
	Actions []Action `json:"actions"`
}

// Options change how a run behaves.
type Options struct {
	// DryRun plans without changing anything. Allowed even while
	// provisioning is off, to see what turning it on would do.
	DryRun bool
	// Force runs past the max_change_percent limit.
	Force bool
}

// MaxAggregationAge is how old the last successful aggregation may be for a
// run to plan from it.
const MaxAggregationAge = 24 * time.Hour

// runTimeout bounds how long a run may spend writing to the application.
const runTimeout = 10 * time.Minute

// recordTimeout bounds recording one action, or the end of a run, once the
// application has answered.
const recordTimeout = 30 * time.Second

// Provision plans the changes an application needs and, unless it is a dry
// run, carries them out. The run is recorded in connector_runs. Failures of
// single actions do not stop the others; the run is then partial or failed.
// The error is returned only when Grantra itself failed or the run could not
// start (not found, provisioning off, no recent aggregation, another run in
// progress).
//
// A run that writes is recorded as running, with its planned actions, before
// the first change, and each change is recorded as soon as the application
// confirms it. If Grantra stops halfway, what it already changed stays on
// record and the run shows as interrupted; it is never lost with a rolled
// back transaction.
func Provision(ctx context.Context, pool *pgxpool.Pool, tenantID, applicationID, actor string, opts Options, secrets connector.SecretResolver) (Run, error) {
	return provision(ctx, pool, tenantID, applicationID, actor, opts, secrets, true)
}

// ErrRunning is returned when a provisioning run of the application is
// already in progress.
var ErrRunning error = &apperr.Conflict{Msg: "a provisioning run of this application is already in progress"}

// Run statuses beyond the ones aggregations share.
const (
	// RunRunning is a run still carrying out its actions.
	RunRunning = "running"
	// RunInterrupted is a run that stopped before finishing its actions:
	// Grantra shut down or lost its database. Actions still "planned" did
	// not happen; the next run plans them again.
	RunInterrupted = "interrupted"
)

func provision(ctx context.Context, pool *pgxpool.Pool, tenantID, applicationID, actor string, opts Options, secrets connector.SecretResolver, recordEmpty bool) (Run, error) {
	run := Run{Run: accounts.Run{ApplicationID: applicationID, Kind: "provisioning", Actor: actor, StartedAt: time.Now(), Counts: map[string]int{}}, Actions: []Action{}}
	if !db.ValidUUID(applicationID) {
		return run, apperr.ErrNotFound
	}
	var (
		w connector.Writer
		p *plan
	)
	// Plan in one transaction that locks the application, so aggregations
	// and other runs wait and nobody plans from a state this run is about to
	// change. A run that writes leaves the transaction recorded as running;
	// that record keeps other runs and aggregations out until it finishes.
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var (
			kind     string
			config   json.RawMessage
			settings Settings
		)
		err := tx.QueryRow(ctx, `SELECT connector, config, provisioning FROM applications WHERE id = $1 FOR UPDATE`, applicationID).
			Scan(&kind, &config, &settings)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrNotFound
		}
		if err != nil {
			return err
		}
		if err := closeStale(ctx, tx, tenantID, applicationID); err != nil {
			return err
		}
		if err := settings.validate(); err != nil {
			return err
		}
		if settings.Mode != ModeOn && !opts.DryRun {
			return apperr.Conflictf("provisioning is off for this application; turn it on, or ask for a dry run")
		}
		if w, err = writer(kind, config, secrets); err != nil {
			return err
		}
		var fresh bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM connector_runs WHERE application_id = $1 AND kind = 'aggregation'
			                AND status = 'succeeded' AND started_at > now() - make_interval(secs => $2))`,
			applicationID, MaxAggregationAge.Seconds()).Scan(&fresh); err != nil {
			return err
		}
		if !fresh {
			return apperr.Conflictf("no successful aggregation of this application in the last %s; aggregate it first so Grantra plans from what is really there", MaxAggregationAge)
		}

		_, canCreate := w.(connector.AccountCreator)
		if p, err = loadPlan(ctx, tx, applicationID, settings, canCreate); err != nil {
			return err
		}
		run.Actions = p.actions
		for k, v := range p.counts {
			run.Counts[k] = v
		}
		for _, a := range run.Actions {
			run.Counts[a.Op]++
		}
		switch {
		case opts.DryRun:
			run.Status = accounts.RunPlanned
		case len(run.Actions) == 0 && !recordEmpty:
			run.Status = accounts.RunSucceeded
			return nil
		case p.tooMuch(settings.MaxChangePercent) && !opts.Force:
			run.Status = accounts.RunRefused
			run.Error = fmt.Sprintf("the run would remove or disable %d of %d accounts, more than the %d%% limit; nothing was changed (check the plan, then force it if it is right)",
				p.destructive(), p.accounts, settings.MaxChangePercent)
		default:
			run.Status = RunRunning
		}
		var finished *time.Time
		err = tx.QueryRow(ctx,
			`INSERT INTO connector_runs (tenant_id, application_id, kind, actor, status, counts, error, started_at, actions, finished_at)
			 VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), $8, $9, CASE WHEN $5 = 'running' THEN NULL ELSE now() END)
			 RETURNING id, finished_at`,
			tenantID, applicationID, run.Kind, actor, run.Status, run.Counts, run.Error, run.StartedAt, run.Actions).
			Scan(&run.ID, &finished)
		if err != nil {
			return err
		}
		run.FinishedAt = finished
		if run.Status == RunRunning {
			return nil
		}
		return audit(ctx, tx, tenantID, actor, run, opts)
	})
	if err != nil || run.Status != RunRunning {
		return run, err
	}

	// Carry out the actions. From here on the run must end on record even if
	// the caller goes away, so the work does not follow ctx's cancellation;
	// ctx ending only stops new actions from starting.
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runTimeout)
	defer cancel()
	if err := execute(ctx, wctx, pool, tenantID, applicationID, actor, w, p, run); err != nil {
		// The run stays "running" on record; the next run of the application
		// closes it as interrupted once it is past runTimeout.
		return run, err
	}
	run.Status = outcome(run.Actions)
	for _, a := range run.Actions {
		if a.Status == ActionFailed {
			run.Counts["failed"]++
		}
	}
	if ctx.Err() != nil || wctx.Err() != nil {
		run.Status = RunInterrupted
		run.Error = "stopped before every action was carried out; actions still planned did not happen"
	}
	fctx, fcancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer fcancel()
	err = db.InTenant(fctx, pool, tenantID, func(tx pgx.Tx) error {
		var finished time.Time
		if err := tx.QueryRow(fctx,
			`UPDATE connector_runs SET status = $2, counts = $3, error = nullif($4, ''), finished_at = now()
			 WHERE id = $1 RETURNING finished_at`, run.ID, run.Status, run.Counts, run.Error).Scan(&finished); err != nil {
			return err
		}
		run.FinishedAt = &finished
		return audit(fctx, tx, tenantID, actor, run, opts)
	})
	return run, err
}

// closeStale marks runs of the application that have been "running" for
// longer than any run may take as interrupted, and refuses to start while one
// is genuinely running. The caller holds the application's lock.
func closeStale(ctx context.Context, tx pgx.Tx, tenantID, applicationID string) error {
	rows, err := tx.Query(ctx,
		`UPDATE connector_runs SET status = 'interrupted', finished_at = now(),
		        error = 'Grantra stopped before the run finished; actions still planned did not happen'
		 WHERE application_id = $1 AND status = 'running' AND started_at < now() - make_interval(secs => $2)
		 RETURNING id`, applicationID, (runTimeout + time.Minute).Seconds())
	if err != nil {
		return err
	}
	stale, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, id := range stale {
		if err := db.Audit(ctx, tx, tenantID, "system:provisioning", "application.provisioning_interrupted", "application", applicationID,
			map[string]any{"run_id": id}); err != nil {
			return err
		}
	}
	var running bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM connector_runs WHERE application_id = $1 AND status = 'running')`, applicationID).Scan(&running); err != nil {
		return err
	}
	if running {
		return ErrRunning
	}
	return nil
}

func audit(ctx context.Context, tx pgx.Tx, tenantID, actor string, run Run, opts Options) error {
	details := map[string]any{"run_id": run.ID, "status": run.Status, "counts": run.Counts, "dry_run": opts.DryRun, "forced": opts.Force}
	if run.Error != "" {
		details["error"] = run.Error
	}
	return db.Audit(ctx, tx, tenantID, actor, "application.provisioned", "application", run.ApplicationID, details)
}

func outcome(actions []Action) string {
	failed := 0
	for _, a := range actions {
		if a.Status == ActionFailed {
			failed++
		}
	}
	switch {
	case failed == 0:
		return accounts.RunSucceeded
	case failed == len(actions):
		return accounts.RunFailed
	default:
		return accounts.RunPartial
	}
}

type account struct {
	id, native, identity, status string
	held                         map[string]bool // entitlement id -> provisioned by Grantra
}

type identity struct {
	id, status, email, name, externalID string
	want                                map[string]bool // entitlement ids
}

type plan struct {
	actions  []Action
	counts   map[string]int
	accounts int
	// identities and natives hold what execute needs.
	identities map[string]*identity
	natives    map[string]string // entitlement id -> native id
}

// destructive counts the accounts the plan removes access from or disables.
// It counts accounts, not actions, so one leaver with many groups is one.
func (p *plan) destructive() int {
	accts := map[string]bool{}
	for _, a := range p.actions {
		if a.destructive() {
			accts[a.AccountID] = true
		}
	}
	return len(accts)
}

func (p *plan) tooMuch(maxPercent int) bool {
	n := p.destructive()
	return n > 3 && n*100 > maxPercent*p.accounts
}

// loadPlan reads the desired and actual state of an application and plans
// the difference. canCreate says whether the connector can create accounts.
func loadPlan(ctx context.Context, tx pgx.Tx, appID string, s Settings, canCreate bool) (*plan, error) {
	p := &plan{actions: []Action{}, counts: map[string]int{}, identities: map[string]*identity{}, natives: map[string]string{}}

	rows, err := tx.Query(ctx, `SELECT id, native_id FROM entitlements WHERE application_id = $1 AND missing_since IS NULL`, appID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, native string
		if err := rows.Scan(&id, &native); err != nil {
			return nil, err
		}
		p.natives[id] = native
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	ident := func(id, status, email, name, ext string) *identity {
		i := p.identities[id]
		if i == nil {
			i = &identity{id: id, status: status, email: email, name: name, externalID: ext, want: map[string]bool{}}
			p.identities[id] = i
		}
		return i
	}

	// Desired: entitlements of this application that unexpired assignments
	// give, directly or through a business role.
	rows, err = tx.Query(ctx,
		`SELECT i.id, i.status, coalesce(i.email, ''), i.display_name, coalesce(i.external_id, ''), e.id, e.missing_since IS NOT NULL
		 FROM access_assignments x
		 JOIN identities i ON i.id = x.identity_id
		 LEFT JOIN role_entitlements re ON re.role_id = x.role_id
		 JOIN entitlements e ON e.id = coalesce(x.entitlement_id, re.entitlement_id)
		 WHERE e.application_id = $1 AND (x.expires_at IS NULL OR x.expires_at > now())`, appID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			id, status, email, name, ext, ent string
			missing                           bool
		)
		if err := rows.Scan(&id, &status, &email, &name, &ext, &ent, &missing); err != nil {
			return nil, err
		}
		i := ident(id, status, email, name, ext)
		if missing {
			// The application no longer has it; nothing to add it to.
			p.counts["missing_entitlements"]++
			continue
		}
		i.want[ent] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Actual: accounts linked to someone, with what they hold. Orphans are
	// left alone: whose they are is for a person to decide.
	byIdentity := map[string][]*account{}
	rows, err = tx.Query(ctx,
		`SELECT a.id, a.native_id, a.identity_id, a.status, i.status, coalesce(i.email, ''), i.display_name, coalesce(i.external_id, '')
		 FROM accounts a JOIN identities i ON i.id = a.identity_id
		 WHERE a.application_id = $1 AND a.status <> 'deleted'
		 ORDER BY a.status = 'active' DESC, a.created_at, a.id`, appID)
	if err != nil {
		return nil, err
	}
	accountByID := map[string]*account{}
	for rows.Next() {
		a := &account{held: map[string]bool{}}
		var status, email, name, ext string
		if err := rows.Scan(&a.id, &a.native, &a.identity, &a.status, &status, &email, &name, &ext); err != nil {
			return nil, err
		}
		ident(a.identity, status, email, name, ext)
		byIdentity[a.identity] = append(byIdentity[a.identity], a)
		accountByID[a.id] = a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.Query(ctx,
		`SELECT ae.account_id, ae.entitlement_id, ae.provisioned_at IS NOT NULL
		 FROM account_entitlements ae JOIN accounts a ON a.id = ae.account_id
		 WHERE a.application_id = $1 AND a.identity_id IS NOT NULL AND a.status <> 'deleted'`, appID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var acc, ent string
		var ours bool
		if err := rows.Scan(&acc, &ent, &ours); err != nil {
			return nil, err
		}
		if a := accountByID[acc]; a != nil {
			a.held[ent] = ours
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM accounts WHERE application_id = $1 AND status <> 'deleted'`, appID).Scan(&p.accounts); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(p.identities))
	for id := range p.identities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		i := p.identities[id]
		accts := byIdentity[id]
		want := i.want
		if i.status != "active" {
			// Leavers, suspended people and people who have not started yet
			// should hold nothing.
			want = map[string]bool{}
		}
		if i.status == "suspended" || i.status == "terminated" {
			for _, a := range accts {
				if a.status == "active" {
					p.actions = append(p.actions, Action{Op: OpDisableAccount, IdentityID: id, AccountID: a.id, NativeAccount: a.native})
				}
			}
		}

		// Give the person an active account, and add what is missing to
		// their main one.
		if len(want) > 0 {
			var main *account
			if len(accts) > 0 {
				main = accts[0] // active accounts sort first
			}
			var missing []string
			for ent := range want {
				held := false
				for _, a := range accts {
					if _, ok := a.held[ent]; ok {
						held = true
					}
				}
				if !held {
					missing = append(missing, ent)
				}
			}
			sort.Strings(missing)
			switch {
			case main == nil && !canCreate:
				// The connector cannot create accounts: someone has to make
				// it in the application, and the next aggregation links it.
				p.counts["skipped_no_account"]++
				missing = nil
			case main == nil && i.email == "":
				// The username is the email; without one there is nothing
				// sensible to create.
				p.counts["skipped_no_email"]++
				missing = nil
			case main == nil:
				p.actions = append(p.actions, Action{Op: OpCreateAccount, IdentityID: id, Username: i.email})
			case main.status != "active":
				p.actions = append(p.actions, Action{Op: OpEnableAccount, IdentityID: id, AccountID: main.id, NativeAccount: main.native})
			}
			for _, ent := range missing {
				a := Action{Op: OpAddMember, IdentityID: id, EntitlementID: ent, NativeEntitlement: p.natives[ent]}
				if main != nil {
					a.AccountID, a.NativeAccount = main.id, main.native
				}
				p.actions = append(p.actions, a)
			}
		}

		// Remove what nobody assigned, if Grantra granted it or may remove
		// anything.
		for _, a := range accts {
			held := make([]string, 0, len(a.held))
			for ent := range a.held {
				held = append(held, ent)
			}
			sort.Strings(held)
			for _, ent := range held {
				if want[ent] {
					continue
				}
				native, present := p.natives[ent]
				if !present {
					continue
				}
				if !a.held[ent] && !s.RemoveUnassigned {
					p.counts["unassigned_kept"]++
					continue
				}
				p.actions = append(p.actions, Action{Op: OpRemoveMember, IdentityID: id, AccountID: a.id, NativeAccount: a.native, EntitlementID: ent, NativeEntitlement: native})
			}
		}
	}
	for i := range p.actions {
		p.actions[i].Status = ActionPlanned
	}
	return p, nil
}

// execute carries out the run's actions in order. Each change goes to the
// application first and, once it answers, is recorded in its own
// transaction: the local state (so the next plan starts from what is true),
// the action's outcome on the run and the audit log. stop ending keeps
// further actions from starting; ctx bounds the calls and the writes.
func execute(stop, ctx context.Context, pool *pgxpool.Pool, tenantID, appID, actor string, w connector.Writer, p *plan, run Run) error {
	created := map[string]Action{} // identity -> its create action, once done
	for n := range run.Actions {
		if stop.Err() != nil || ctx.Err() != nil {
			return nil
		}
		a := &run.Actions[n]
		var err error
		switch a.Op {
		case OpCreateAccount:
			i := p.identities[a.IdentityID]
			c, ok := w.(connector.AccountCreator)
			if !ok {
				err = errors.New("this connector cannot create accounts")
				break
			}
			a.NativeAccount, err = c.CreateAccount(ctx, connector.NewAccount{
				Username: i.email, Email: i.email, DisplayName: i.name, ExternalID: i.externalID,
			})
		case OpEnableAccount, OpDisableAccount:
			err = w.SetActive(ctx, a.NativeAccount, a.Op == OpEnableAccount)
		case OpAddMember:
			if a.AccountID == "" {
				c, ok := created[a.IdentityID]
				if !ok {
					err = errors.New("the account could not be created")
					break
				}
				a.AccountID, a.NativeAccount = c.AccountID, c.NativeAccount
			}
			err = w.AddMember(ctx, a.NativeEntitlement, a.NativeAccount)
		case OpRemoveMember:
			err = w.RemoveMember(ctx, a.NativeEntitlement, a.NativeAccount)
		default:
			return fmt.Errorf("unknown provisioning action %q", a.Op)
		}
		a.Status = ActionDone
		if err != nil {
			a.Status, a.Error = ActionFailed, err.Error()
		}
		// The change happened: record it even if ctx ran out meanwhile.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		err = db.InTenant(rctx, pool, tenantID, func(tx pgx.Tx) error {
			return record(rctx, tx, tenantID, appID, actor, run.ID, n, a, p)
		})
		cancel()
		if err != nil {
			return fmt.Errorf("record %s for %s: %w", a.Op, a.NativeAccount, err)
		}
		if a.Op == OpCreateAccount && a.Status == ActionDone {
			created[a.IdentityID] = *a
		}
	}
	return nil
}

// record stores what one action did: the local state when it succeeded, its
// outcome on the run (action n) and an audit event.
func record(ctx context.Context, tx pgx.Tx, tenantID, appID, actor, runID string, n int, a *Action, p *plan) error {
	if a.Status == ActionDone {
		var err error
		switch a.Op {
		case OpCreateAccount:
			i := p.identities[a.IdentityID]
			attrs := map[string]string{"email": i.email, "display_name": i.name}
			if i.externalID != "" {
				attrs["external_id"] = i.externalID
			}
			err = tx.QueryRow(ctx,
				`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username, status, attributes, last_seen_at, correlated_by)
				 VALUES ($1, $2, $3, $4, $5, 'active', $6, now(), $7)
				 ON CONFLICT (application_id, native_id) DO UPDATE SET
				   identity_id = EXCLUDED.identity_id, status = 'active', correlated_by = EXCLUDED.correlated_by
				 RETURNING id`, tenantID, appID, a.IdentityID, a.NativeAccount, i.email, attrs, accounts.ByProvisioning).Scan(&a.AccountID)
		case OpEnableAccount, OpDisableAccount:
			status := "disabled"
			if a.Op == OpEnableAccount {
				status = "active"
			}
			_, err = tx.Exec(ctx, `UPDATE accounts SET status = $2 WHERE id = $1`, a.AccountID, status)
		case OpAddMember:
			_, err = tx.Exec(ctx,
				`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id, provisioned_at)
				 VALUES ($1, $2, $3, now())
				 ON CONFLICT (account_id, entitlement_id) DO UPDATE SET provisioned_at = now()`,
				tenantID, a.AccountID, a.EntitlementID)
		case OpRemoveMember:
			_, err = tx.Exec(ctx,
				`DELETE FROM account_entitlements WHERE account_id = $1 AND entitlement_id = $2`, a.AccountID, a.EntitlementID)
		}
		if err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE connector_runs SET actions = jsonb_set(actions, ARRAY[$2::text], $3) WHERE id = $1`,
		runID, strconv.Itoa(n), *a); err != nil {
		return err
	}
	details := map[string]any{"application_id": appID, "identity_id": a.IdentityID, "native_account": a.NativeAccount, "status": a.Status, "run_id": runID}
	if a.NativeEntitlement != "" {
		details["native_entitlement"] = a.NativeEntitlement
	}
	if a.Error != "" {
		details["error"] = a.Error
	}
	return db.Audit(ctx, tx, tenantID, actor, "provisioning."+a.Op, "account", a.AccountID, details)
}

// ProvisionDue provisions every application with provisioning on and a
// successful aggregation in the last MaxAggregationAge. Runs with nothing to
// do are not recorded. One application failing does not stop the others.
func ProvisionDue(ctx context.Context, pool *pgxpool.Pool, tenantID string, secrets connector.SecretResolver) ([]Run, error) {
	return provisionDue(ctx, pool, tenantID, secrets, false)
}

// ProvisionQueued provisions, like ProvisionDue, only the applications
// whose access changed through just-in-time activations since the last
// call (see access.Activate), so that an activation reaches the
// application in seconds rather than at the next regular pass.
func ProvisionQueued(ctx context.Context, pool *pgxpool.Pool, tenantID string, secrets connector.SecretResolver) ([]Run, error) {
	return provisionDue(ctx, pool, tenantID, secrets, true)
}

func provisionDue(ctx context.Context, pool *pgxpool.Pool, tenantID string, secrets connector.SecretResolver, queued bool) ([]Run, error) {
	var due []string
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var only []string
		if queued {
			rows, err := tx.Query(ctx, `DELETE FROM jit_provisioning_queue RETURNING application_id::text`)
			if err != nil {
				return err
			}
			if only, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil || len(only) == 0 {
				return err
			}
		}
		rows, err := tx.Query(ctx,
			`SELECT a.id FROM applications a
			 WHERE a.provisioning->>'mode' = 'on'
			   AND (NOT $2 OR a.id = ANY($3::uuid[]))
			   AND EXISTS (SELECT 1 FROM connector_runs r
			               WHERE r.application_id = a.id AND r.kind = 'aggregation' AND r.status = 'succeeded'
			                 AND r.started_at > now() - make_interval(secs => $1))
			 ORDER BY a.name`, MaxAggregationAge.Seconds(), queued, only)
		if err != nil {
			return err
		}
		due, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, err
	}
	var (
		runs []Run
		errs []error
	)
	for _, id := range due {
		run, err := provision(ctx, pool, tenantID, id, "system:provisioning", Options{}, secrets, false)
		if errors.Is(err, ErrRunning) {
			// Someone started one from the console. It may have read the
			// access before the change that queued this pass: try again on
			// the next one.
			if queued {
				if err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
					_, err := tx.Exec(ctx,
						`INSERT INTO jit_provisioning_queue (tenant_id, application_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, tenantID, id)
					return err
				}); err != nil {
					errs = append(errs, fmt.Errorf("queue %s again: %w", id, err))
				}
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return runs, ctx.Err()
			}
			errs = append(errs, fmt.Errorf("provision %s: %w", id, err))
			continue
		}
		if run.ID != "" {
			runs = append(runs, run)
		}
	}
	return runs, errors.Join(errs...)
}
