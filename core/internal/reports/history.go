package reports

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// heldNow are the memberships that count as held right now: an active
// account holding an entitlement that has not ended.
const heldNow = `SELECT ae.tenant_id, ae.account_id, ae.entitlement_id, a.identity_id, ae.granted_at
	FROM account_entitlements ae JOIN accounts a ON a.id = ae.account_id
	WHERE a.status = 'active' AND (ae.expires_at IS NULL OR ae.expires_at > now())`

// History counts what one pass of RecordHistory changed.
type History struct {
	Started int `json:"started"`
	Ended   int `json:"ended"`
	Pruned  int `json:"pruned"`
}

// RecordHistory brings the history of who held what up to date: a
// membership that is gone (or whose account changed hands) ends now, and a
// new one starts when the application says it was granted, or now if that
// was before an earlier stretch ended. Memberships that ended longer ago
// than the tenant keeps them are forgotten.
func RecordHistory(ctx context.Context, pool *pgxpool.Pool, tenantID string) (History, error) {
	var h History
	_, err := db.TryLocked(ctx, pool, "access history: "+tenantID, func(ctx context.Context) error {
		return db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `WITH now_ AS (`+heldNow+`)
				UPDATE access_history h SET held_until = now()
				WHERE h.held_until IS NULL AND NOT EXISTS (
				  SELECT 1 FROM now_ n WHERE n.account_id = h.account_id AND n.entitlement_id = h.entitlement_id
				    AND n.identity_id IS NOT DISTINCT FROM h.identity_id)`)
			if err != nil {
				return err
			}
			h.Ended = int(tag.RowsAffected())
			tag, err = tx.Exec(ctx, `WITH now_ AS (`+heldNow+`)
				INSERT INTO access_history (tenant_id, account_id, entitlement_id, identity_id, held_from)
				SELECT n.tenant_id, n.account_id, n.entitlement_id, n.identity_id,
				       least(now(), greatest(n.granted_at, (SELECT max(x.held_until) FROM access_history x
				                                             WHERE x.account_id = n.account_id AND x.entitlement_id = n.entitlement_id)))
				FROM now_ n
				WHERE NOT EXISTS (SELECT 1 FROM access_history h
				                  WHERE h.held_until IS NULL AND h.account_id = n.account_id AND h.entitlement_id = n.entitlement_id)`)
			if err != nil {
				return err
			}
			h.Started = int(tag.RowsAffected())
			tag, err = tx.Exec(ctx, `DELETE FROM access_history
				WHERE held_until < now() - make_interval(days => `+settings.SQL(settings.AccessHistoryDays)+`)`)
			h.Pruned = int(tag.RowsAffected())
			return err
		})
	})
	return h, err
}

// heldAt is true when a history row h was held at the instant t.
func heldAt(t string) string {
	return `(h.held_from <= ` + t + ` AND (h.held_until IS NULL OR h.held_until > ` + t + `))`
}

// historyMembership is the columns that describe the membership of a
// history row h, joined to a (account), e (entitlement) and ap
// (application).
var historyMembership = `ap.name, a.native_id, coalesce(a.username, ''), ` + person("h.identity_id") + `, ` +
	email("h.identity_id") + `, e.name, e.risk`

const historyJoins = ` JOIN accounts a ON a.id = h.account_id JOIN entitlements e ON e.id = h.entitlement_id
	JOIN applications ap ON ap.id = a.application_id`

var historyColumns = []string{"application", "Application", "account", "Account", "username", "Username",
	"person", "Person", "email", "Email", "entitlement", "Entitlement", "risk", "Risk"}

// accessHistory is the report of who held what at the end of a period, and
// what changed in it.
var accessHistory = Definition{
	ID:          AccessHistory,
	Title:       "Who had access on a date",
	Description: "Everything everyone held at the end of the period, as applications reported it, and every membership that started or ended in it. The population at period end that auditors sample from.",
	Period:      true, Application: true,
	Tables: []Table{
		{
			Name: "held", Title: "Held at the end of the period",
			Description: "Memberships of active accounts at midnight UTC after the last day of the period. Grantra records them every few minutes from when it was updated to this version (see Recorded since).",
			Columns:     columns(append(historyColumns, "since", "Held since (UTC)", "until", "Held until (UTC)")...),
			sql: `SELECT ` + historyMembership + `, ` + ts("h.held_from") + `, ` + ts("h.held_until") + `
			 FROM p CROSS JOIN access_history h` + historyJoins + `
			 WHERE ` + heldAt("p.until") + ` AND (p.app IS NULL OR a.application_id = p.app)
			 ORDER BY ap.name, 4, a.native_id, e.name`,
		},
		{
			Name: "changes", Title: "Started or ended in the period",
			Description: "Each membership that started or ended within the period, in time order.",
			Columns:     columns(append([]string{"change", "Change", "at", "When (UTC)"}, historyColumns...)...),
			sql: `SELECT c.change, ` + ts("c.at") + `, ` + historyMembership + `
			 FROM p CROSS JOIN access_history h` + historyJoins + `
			 CROSS JOIN LATERAL (VALUES ('started', h.held_from), ('ended', h.held_until)) c (change, at)
			 WHERE ` + within("c.at") + ` AND (p.app IS NULL OR a.application_id = p.app)
			 ORDER BY c.at, 3, 4, 8`,
		},
		{
			Name: "recorded", Title: "Recorded since",
			Description: "When Grantra started recording memberships. Memberships it found then start when the application says they were granted; changes before this are not known.",
			Columns:     columns("since", "Recorded since (UTC)"),
			sql:         `SELECT ` + ts("min(recorded_at)") + ` FROM p CROSS JOIN access_history`,
		},
	},
	facts: []fact{
		{label: "Held at the end of the period", table: "held"},
		{label: "High or critical risk", table: "held", column: "risk", values: []string{"high", "critical"}},
		{label: "Started in the period", table: "changes", column: "change", values: []string{"started"}},
		{label: "Ended in the period", table: "changes", column: "change", values: []string{"ended"}},
	},
}
