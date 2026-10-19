package insights

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Unused is access someone was given by hand or by request, or may
// activate, and has not used in UnusedDays. Birthright access is left out:
// rules give it, and the rule is what to change.
type Unused struct {
	// ID is the assignment's id, or the eligibility's when Eligible.
	ID           string `json:"id"`
	Eligible     bool   `json:"eligible"`
	IdentityID   string `json:"identity_id"`
	IdentityName string `json:"identity_name"`
	// Type is "role" or "entitlement".
	Type        string     `json:"type"`
	TargetID    string     `json:"target_id"`
	Name        string     `json:"name"`
	Application string     `json:"application,omitempty"`
	Risk        string     `json:"risk"`
	Origin      string     `json:"origin"`
	GrantedAt   time.Time  `json:"granted_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
	LastUsedAt  *time.Time `json:"last_used_at,omitempty"`
}

// UnusedFilter selects unused access.
type UnusedFilter struct {
	IdentityID string
	// After is the id of the last one of the previous page.
	After string
	Limit int
}

// UnusedAccess lists the access nobody used in UnusedDays, by id, a page
// at a time.
func UnusedAccess(ctx context.Context, tx pgx.Tx, f UnusedFilter) ([]Unused, error) {
	if f.After != "" && !db.ValidUUID(f.After) {
		return nil, apperr.Invalidf("after must be an id from the list")
	}
	since := unusedSince(time.Now())
	rows, err := tx.Query(ctx, `
		WITH c AS (
			SELECT x.id, false AS eligible, x.identity_id, x.role_id, x.entitlement_id, x.origin, x.granted_at, x.expires_at
			FROM access_assignments x
			WHERE x.origin IN ('manual', 'request') AND x.granted_at < $1
			  AND (x.expires_at IS NULL OR x.expires_at > now())
			UNION ALL
			SELECT el.id, true, el.identity_id, el.role_id, el.entitlement_id, 'eligibility', el.granted_at, el.expires_at
			FROM access_eligibilities el
			WHERE el.granted_at < $1 AND (el.expires_at IS NULL OR el.expires_at > now())
			  AND (el.last_activated_at IS NULL OR el.last_activated_at < $1)
		)
		SELECT c.id, c.eligible, c.identity_id, i.display_name,
		       CASE WHEN c.role_id IS NULL THEN 'entitlement' ELSE 'role' END, coalesce(c.role_id, c.entitlement_id),
		       coalesce(ro.name, e.name), coalesce(app.name, ''),
		       coalesce(e.risk, (SELECT ($5::text[])[max(array_position($5::text[], re_e.risk))]
		                 FROM role_entitlements re JOIN entitlements re_e ON re_e.id = re.entitlement_id
		                 WHERE re.role_id = c.role_id), 'low'),
		       c.origin, c.granted_at, c.expires_at, u.at
		FROM c
		JOIN identities i ON i.id = c.identity_id
		LEFT JOIN roles ro ON ro.id = c.role_id
		LEFT JOIN entitlements e ON e.id = c.entitlement_id
		LEFT JOIN applications app ON app.id = e.application_id
		CROSS JOIN LATERAL (`+useSQL("c.identity_id", "c.role_id", "c.entitlement_id", "CASE WHEN c.eligible THEN c.id END")+`) u
		WHERE i.status = 'active' AND u.tracked AND (u.at IS NULL OR u.at < $1)
		  AND ($2 = '' OR c.identity_id = nullif($2, '')::uuid)
		  AND ($3 = '' OR c.id > nullif($3, '')::uuid)
		ORDER BY c.id LIMIT $4`, since, f.IdentityID, f.After, f.Limit, catalog.Risks)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Unused, error) {
		var u Unused
		err := row.Scan(&u.ID, &u.Eligible, &u.IdentityID, &u.IdentityName, &u.Type, &u.TargetID, &u.Name, &u.Application,
			&u.Risk, &u.Origin, &u.GrantedAt, &u.ExpiresAt, &u.LastUsedAt)
		return u, err
	})
}
