package console

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/itdr"
)

// expiringAccess is access of yours that ends soon.
type expiringAccess struct {
	Name        string    `json:"name"`
	Application string    `json:"application,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// failedRun is an application or HR source whose last run failed.
type failedRun struct {
	// Kind is "aggregation" or "provisioning" for an application, "hr" for
	// an HR source.
	Kind  string    `json:"kind"`
	ID    string    `json:"id"`
	Name  string    `json:"name"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

// orphans counts the accounts of one application that belong to nobody.
type orphans struct {
	ApplicationID string `json:"application_id"`
	Application   string `json:"application"`
	Count         int    `json:"count"`
}

// governanceHome is what the people who look after the catalog need to know.
type governanceHome struct {
	FailedRuns []failedRun `json:"failed_runs"`
	Orphans    []orphans   `json:"orphans"`
	// Undescribed is how many roles and entitlements people can ask for
	// have no description: names like APP_FIN_RW_PRD tell them nothing.
	Undescribed int `json:"undescribed"`
	// OpenViolations is how many separation of duties violations are open,
	// when the person may see them.
	OpenViolations *int `json:"open_violations,omitempty"`
}

type home struct {
	// ExpiringAccess is your access that ends within the next 7 days.
	ExpiringAccess []expiringAccess `json:"expiring_access"`
	// PendingRequests is how many of your requests still wait for a decision.
	PendingRequests int `json:"pending_requests"`
	// Governance is there for whoever may read the catalog: admins and
	// auditors.
	Governance *governanceHome `json:"governance,omitempty"`
	// Security is there for whoever may read threat alerts, while threat
	// detection is on.
	Security *securityHome `json:"security,omitempty"`
}

// securityHome is what the people who watch for threats need to know.
type securityHome struct {
	// OpenAlerts is how many alerts are open, per severity.
	OpenAlerts itdr.Counts `json:"open_alerts"`
}

// homeWindow is how far ahead the home page warns about access ending.
const homeWindow = 7 * 24 * time.Hour

// failingWindow is how far back the home page looks for failed runs.
const failingWindow = 24 * time.Hour

// getHome gathers the cards of the signed-in person's home page. Each part
// is only there when the person may see what it counts.
func (a *API) getHome(w http.ResponseWriter, r *http.Request) {
	out := home{ExpiringAccess: []expiringAccess{}}
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		ctx := r.Context()
		// Activations last hours and My access shows them; this is about
		// access that goes away when nobody renews it.
		rows, err := tx.Query(ctx,
			`SELECT coalesce(ro.name, e.name), coalesce(app.name, ''), x.expires_at
			 FROM access_assignments x
			 LEFT JOIN roles ro ON ro.id = x.role_id
			 LEFT JOIN entitlements e ON e.id = x.entitlement_id
			 LEFT JOIN applications app ON app.id = e.application_id
			 WHERE x.identity_id = $1 AND x.origin <> 'activation'
			   AND x.expires_at > now() AND x.expires_at <= now() + make_interval(secs => $2)
			 ORDER BY x.expires_at, 1`, me.ID, homeWindow.Seconds())
		if err != nil {
			return err
		}
		if out.ExpiringAccess, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (expiringAccess, error) {
			var e expiringAccess
			err := row.Scan(&e.Name, &e.Application, &e.ExpiresAt)
			return e, err
		}); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM access_requests WHERE status = 'pending' AND (requester_id = $1 OR beneficiary_id = $1)`,
			me.ID).Scan(&out.PendingRequests); err != nil {
			return err
		}

		if a.may(me, authz.ReadThreats, authz.ThreatsResource()) {
			on, err := moduleOn(ctx, tx, ModuleITDR)
			if err != nil {
				return err
			}
			if on {
				open, err := itdr.OpenCounts(ctx, tx, "")
				if err != nil {
					return err
				}
				out.Security = &securityHome{OpenAlerts: open}
			}
		}

		if !a.may(me, authz.ReadCatalog, authz.CatalogResource()) {
			return nil
		}
		g := &governanceHome{}
		// What is failing now: the last finished run of each application
		// (per kind) and of each HR source in the window, when that run did
		// not do all it should. Dry runs are someone checking a file or a
		// plan, not a failure to act on.
		rows, err = tx.Query(ctx,
			`SELECT kind, id, name, error, at FROM (
			   SELECT DISTINCT ON (cr.application_id, cr.kind) cr.kind, cr.application_id AS id, app.name,
			          coalesce(cr.error, '') AS error, cr.started_at AS at,
			          cr.status IN ('failed', 'partial', 'refused', 'interrupted') AS failed
			   FROM connector_runs cr JOIN applications app ON app.id = cr.application_id
			   WHERE cr.started_at > now() - make_interval(secs => $1) AND cr.status NOT IN ('planned', 'running')
			   ORDER BY cr.application_id, cr.kind, cr.started_at DESC) apps
			 WHERE failed
			 UNION ALL
			 SELECT 'hr', id, name, error, at FROM (
			   SELECT DISTINCT ON (sr.source_id) sr.source_id AS id, s.name,
			          coalesce(sr.errors->0->>'message', '') AS error, sr.created_at AS at,
			          sr.status IN ('invalid', 'refused') AS failed
			   FROM identity_source_runs sr JOIN identity_sources s ON s.id = sr.source_id
			   WHERE sr.created_at > now() - make_interval(secs => $1) AND NOT sr.dry_run
			   ORDER BY sr.source_id, sr.created_at DESC) hr
			 WHERE failed
			 ORDER BY at DESC`, failingWindow.Seconds())
		if err != nil {
			return err
		}
		if g.FailedRuns, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (failedRun, error) {
			var f failedRun
			err := row.Scan(&f.Kind, &f.ID, &f.Name, &f.Error, &f.At)
			return f, err
		}); err != nil {
			return err
		}
		// The same accounts an application's orphans filter shows, per
		// application, most first.
		rows, err = tx.Query(ctx,
			`SELECT app.id, app.name, count(*) FROM accounts a JOIN applications app ON app.id = a.application_id
			 WHERE a.identity_id IS NULL AND a.status <> 'deleted'
			 GROUP BY app.id ORDER BY count(*) DESC, app.name`)
		if err != nil {
			return err
		}
		if g.Orphans, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (orphans, error) {
			var o orphans
			err := row.Scan(&o.ApplicationID, &o.Application, &o.Count)
			return o, err
		}); err != nil {
			return err
		}
		// The same roles and entitlements Request access offers.
		if err := tx.QueryRow(ctx,
			`SELECT (SELECT count(*) FROM roles WHERE coalesce(btrim(description), '') = '')
			      + (SELECT count(*) FROM entitlements e JOIN applications app ON app.id = e.application_id
			         WHERE app.connector <> $1 AND e.missing_since IS NULL AND coalesce(btrim(e.description), '') = '')`,
			authz.BuiltinConnector).Scan(&g.Undescribed); err != nil {
			return err
		}
		if a.may(me, authz.ReadSoD, authz.SoDResource()) {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM sod_violations WHERE status = 'open'`).Scan(&n); err != nil {
				return err
			}
			g.OpenViolations = &n
		}
		g.FailedRuns, g.Orphans = httpjson.NonNil(g.FailedRuns), httpjson.NonNil(g.Orphans)
		out.Governance = g
		return nil
	})
	a.respond(w, r, http.StatusOK, out, err)
}
