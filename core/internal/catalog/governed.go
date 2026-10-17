package catalog

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
)

// EnsureApplication returns the application Grantra keeps for one of its
// own features (the vault, SSH servers, remote access), creating it the
// first time. It is governed by hand: there is nothing to read or
// provision, only access to grant. feature names the feature in the
// conflict when another application with a connector already has the name.
func EnsureApplication(ctx context.Context, tx pgx.Tx, tenantID, name, feature string) (string, error) {
	var id, kind string
	err := tx.QueryRow(ctx, `SELECT id, connector FROM applications WHERE name = $1`, name).Scan(&id, &kind)
	if errors.Is(err, pgx.ErrNoRows) {
		app, err := CreateApplication(ctx, tx, tenantID, name, "")
		return app.ID, err
	}
	if err != nil {
		return "", err
	}
	if kind != ManualConnector {
		return "", apperr.Conflictf("the application %q has a connector; %s needs that name for its own application, so rename the other one", name, feature)
	}
	return id, nil
}

// currentAssignments are the unexpired assignments x of active identities i
// that give the entitlement %[1]s, directly or through a business role.
const currentAssignments = `access_assignments x JOIN identities i ON i.id = x.identity_id
	WHERE i.status = 'active' AND (x.expires_at IS NULL OR x.expires_at > now())
	  AND (x.entitlement_id = %[1]s
	       OR x.role_id IN (SELECT role_id FROM role_entitlements re WHERE re.entitlement_id = %[1]s))`

// HeldSQL is an SQL condition, true when the identity identity, active,
// holds the entitlement entitlement right now: an unexpired assignment,
// directly or through a business role. Both are SQL expressions, such as
// a column or a parameter.
func HeldSQL(identity, entitlement string) string {
	return `EXISTS (SELECT 1 FROM ` + fmt.Sprintf(currentAssignments, entitlement) + ` AND x.identity_id = ` + identity + `)`
}

// HeldWindowSQL is an SQL subquery giving, for the identity identity and
// the entitlement entitlement (SQL expressions), how many current
// assignments give it (n), when the last of them ends (until) and whether
// one never ends (forever): what something handed out for that access may
// last. Use it as a LATERAL join.
func HeldWindowSQL(identity, entitlement string) string {
	return `SELECT count(*) AS n, max(x.expires_at) AS until, bool_or(x.expires_at IS NULL) AS forever
	FROM ` + fmt.Sprintf(currentAssignments, entitlement) + ` AND x.identity_id = ` + identity
}

// Holders returns the active identities who hold an entitlement right
// now, directly or through a business role.
func Holders(ctx context.Context, tx pgx.Tx, entitlementID string) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT DISTINCT x.identity_id::text FROM `+fmt.Sprintf(currentAssignments, "$1::uuid")+` ORDER BY 1`,
		entitlementID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
