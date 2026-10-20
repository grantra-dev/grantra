package access

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Standing is access someone holds with no end date to a high or critical
// risk entitlement, directly or through a role.
type Standing struct {
	AssignmentID string `json:"assignment_id"`
	IdentityID   string `json:"identity_id"`
	IdentityName string `json:"identity_name"`
	// Type is "role" or "entitlement".
	Type        string `json:"type"`
	TargetID    string `json:"target_id"`
	Name        string `json:"name"`
	Application string `json:"application,omitempty"`
	// Risk is the highest risk the access carries.
	Risk      string    `json:"risk"`
	JITOnly   bool      `json:"jit_only"`
	Origin    string    `json:"origin"`
	RuleName  string    `json:"rule_name,omitempty"`
	GrantedBy string    `json:"granted_by"`
	GrantedAt time.Time `json:"granted_at"`
	// Convertible says whether it can become eligibility: access from a
	// birthright rule changes with the rule instead.
	Convertible bool `json:"convertible"`
}

// StandingAccess lists standing access of active people to high and
// critical risk entitlements, or to just-in-time only ones (held from before
// they became so), just-in-time only access first, then riskiest first.
func StandingAccess(ctx context.Context, tx pgx.Tx, limit int) ([]Standing, error) {
	rows, err := tx.Query(ctx, `
		WITH risky AS (
			SELECT x.id, max(CASE e.risk WHEN 'critical' THEN 3 WHEN 'high' THEN 2 WHEN 'medium' THEN 1 ELSE 0 END) AS level,
			       bool_or(e.jit_only) AS jit
			FROM access_assignments x
			JOIN entitlements e ON e.id = x.entitlement_id
			   OR e.id IN (SELECT entitlement_id FROM role_entitlements WHERE role_id = x.role_id)
			WHERE x.expires_at IS NULL AND (`+catalog.RiskySQL("e.risk")+` OR e.jit_only)
			GROUP BY x.id
		)
		SELECT x.id, x.identity_id, i.display_name, CASE WHEN x.role_id IS NULL THEN 'entitlement' ELSE 'role' END,
		       coalesce(x.role_id, x.entitlement_id), coalesce(ro.name, e.name), coalesce(app.name, ''),
		       (ARRAY['low', 'medium', 'high', 'critical'])[r.level + 1], r.jit, x.origin, coalesce(ru.name, ''),
		       x.granted_by, x.granted_at, x.origin <> 'birthright'
		FROM risky r
		JOIN access_assignments x ON x.id = r.id
		JOIN identities i ON i.id = x.identity_id
		LEFT JOIN roles ro ON ro.id = x.role_id
		LEFT JOIN entitlements e ON e.id = x.entitlement_id
		LEFT JOIN applications app ON app.id = e.application_id
		LEFT JOIN access_rules ru ON ru.id = x.rule_id
		WHERE i.status = 'active'
		ORDER BY r.jit DESC, r.level DESC, i.display_name, 6, x.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Standing, error) {
		var s Standing
		err := row.Scan(&s.AssignmentID, &s.IdentityID, &s.IdentityName, &s.Type, &s.TargetID, &s.Name, &s.Application,
			&s.Risk, &s.JITOnly, &s.Origin, &s.RuleName, &s.GrantedBy, &s.GrantedAt, &s.Convertible)
		return s, err
	})
}

// ConvertToEligibility turns standing access into eligibility: the
// assignment goes, and the person may activate the same access when they
// need it. Access from a birthright rule cannot be converted; change the
// rule instead.
func ConvertToEligibility(ctx context.Context, tx pgx.Tx, tenantID, actor, assignmentID string) (string, error) {
	if !db.ValidUUID(assignmentID) {
		return "", apperr.ErrNotFound
	}
	var (
		identity, origin string
		t                Target
		expires          *time.Time
	)
	err := tx.QueryRow(ctx,
		`SELECT identity_id, coalesce(role_id::text, ''), coalesce(entitlement_id::text, ''), origin, expires_at
		 FROM access_assignments WHERE id = $1 FOR UPDATE`, assignmentID).Scan(&identity, &t.RoleID, &t.EntitlementID, &origin, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperr.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	switch {
	case origin == OriginBirthright:
		return "", apperr.Conflictf("a birthright rule gives this access; change the rule instead")
	case origin == OriginActivation || expires != nil:
		return "", apperr.Conflictf("this access already ends on its own")
	}
	id, err := MakeEligible(ctx, tx, tenantID, actor, identity, t, nil, "")
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM access_assignments WHERE id = $1`, assignmentID); err != nil {
		return "", err
	}
	if err := adopt(ctx, tx, identity, t); err != nil {
		return "", err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access.removed", "identity", identity,
		t.details(map[string]any{"assignment_id": assignmentID, "origin": origin, "reason": "turned into eligibility",
			"eligibility_id": id})); err != nil {
		return "", err
	}
	return id, queueProvisioning(ctx, tx, tenantID, t)
}
