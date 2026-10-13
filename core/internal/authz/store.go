package authz

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
)

// BuiltinConnector marks the tenant's built-in "Grantra" application.
const BuiltinConnector = "grantra"

// ErrNotFound is returned when an identity or role does not exist.
var ErrNotFound = errors.New("not found")

// ErrLastAdmin is returned when a revoke would leave the tenant without an
// active admin, and so without anyone able to grant roles again.
var ErrLastAdmin = errors.New("cannot revoke the last active admin")

// ErrInactive is returned when granting a role to an identity that is not active.
var ErrInactive = errors.New("identity is not active")

// EnsureBuiltinApp creates the tenant's built-in "Grantra" application and its
// platform role entitlements when they are missing.
func EnsureBuiltinApp(ctx context.Context, pool *pgxpool.Pool, tenantID string) error {
	return db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'Grantra', $2)
			 ON CONFLICT (tenant_id) WHERE connector = 'grantra' DO NOTHING`, tenantID, BuiltinConnector); err != nil {
			return fmt.Errorf("ensure built-in application: %w", err)
		}
		for _, r := range PlatformRoles {
			if _, err := tx.Exec(ctx,
				`INSERT INTO entitlements (tenant_id, application_id, native_id, name, description, risk)
				 SELECT $1, id, $3, $4, $5, $6 FROM applications WHERE connector = $2
				 ON CONFLICT (application_id, native_id) DO NOTHING`,
				tenantID, BuiltinConnector, r.Name, r.DisplayName, r.Description, r.Risk); err != nil {
				return fmt.Errorf("ensure platform role %s: %w", r.Name, err)
			}
		}
		return nil
	})
}

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidID reports whether id has the shape of an identity id (a UUID).
func ValidID(id string) bool { return uuidPattern.MatchString(id) }

// LoadSubject returns the identity with id, with the platform roles it holds
// right now: grants that have not expired, on an active account.
func LoadSubject(ctx context.Context, tx pgx.Tx, id string) (Subject, error) {
	if !ValidID(id) {
		return Subject{}, ErrNotFound
	}
	var s Subject
	err := tx.QueryRow(ctx,
		`SELECT i.id, i.status, i.kind, coalesce(i.manager_id::text, ''),
		        ARRAY(SELECT e.native_id
		              FROM accounts a
		              JOIN applications app ON app.id = a.application_id AND app.connector = $2
		              JOIN account_entitlements ae ON ae.account_id = a.id
		              JOIN entitlements e ON e.id = ae.entitlement_id
		              WHERE a.identity_id = i.id AND a.status = 'active'
		                AND (ae.expires_at IS NULL OR ae.expires_at > now())
		              ORDER BY 1)
		 FROM identities i WHERE i.id = $1`, id, BuiltinConnector).
		Scan(&s.ID, &s.Status, &s.Kind, &s.ManagerID, &s.Roles)
	if errors.Is(err, pgx.ErrNoRows) {
		return Subject{}, ErrNotFound
	}
	return s, err
}

// Member is someone holding a platform role.
type Member struct {
	Role        string     `json:"role"`
	IdentityID  string     `json:"identity_id"`
	DisplayName string     `json:"display_name"`
	Email       string     `json:"email,omitempty"`
	Status      string     `json:"status"`
	GrantedAt   time.Time  `json:"granted_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// Members lists who holds each platform role right now.
func Members(ctx context.Context, tx pgx.Tx) ([]Member, error) {
	rows, err := tx.Query(ctx,
		`SELECT e.native_id, i.id, i.display_name, coalesce(i.email, ''), i.status, ae.granted_at, ae.expires_at
		 FROM entitlements e
		 JOIN applications app ON app.id = e.application_id AND app.connector = $1
		 JOIN account_entitlements ae ON ae.entitlement_id = e.id
		 JOIN accounts a ON a.id = ae.account_id AND a.status = 'active'
		 JOIN identities i ON i.id = a.identity_id
		 WHERE ae.expires_at IS NULL OR ae.expires_at > now()
		 ORDER BY e.native_id, i.display_name, i.id`, BuiltinConnector)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Member, error) {
		var m Member
		err := row.Scan(&m.Role, &m.IdentityID, &m.DisplayName, &m.Email, &m.Status, &m.GrantedAt, &m.ExpiresAt)
		return m, err
	})
}

// Grant gives the identity a platform role, until expiresAt when it is not
// nil. Granting a role the identity already holds updates its expiry. The
// identity gets an account in the built-in application on first grant.
func Grant(ctx context.Context, tx pgx.Tx, tenantID, role, identityID string, expiresAt *time.Time) error {
	if !IsPlatformRole(role) || !ValidID(identityID) {
		return ErrNotFound
	}
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM identities WHERE id = $1`, identityID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return ErrInactive
	}

	var accountID string
	err = tx.QueryRow(ctx,
		`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username)
		 SELECT $1, app.id, i.id, i.id::text, i.email
		 FROM applications app, identities i
		 WHERE app.connector = $2 AND i.id = $3
		 ON CONFLICT (application_id, native_id)
		   DO UPDATE SET status = 'active', identity_id = EXCLUDED.identity_id
		 RETURNING id`, tenantID, BuiltinConnector, identityID).Scan(&accountID)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("built-in application missing for tenant %s", tenantID)
	}
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx,
		`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id, expires_at)
		 SELECT $1, $2, e.id, $5
		 FROM entitlements e JOIN applications app ON app.id = e.application_id AND app.connector = $3
		 WHERE e.native_id = $4
		 ON CONFLICT (account_id, entitlement_id)
		   DO UPDATE SET expires_at = EXCLUDED.expires_at, granted_at = now()`,
		tenantID, accountID, BuiltinConnector, role, expiresAt)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("platform role %s missing for tenant %s", role, tenantID)
	}
	return nil
}

// Revoke takes a platform role away from the identity. It returns ErrNotFound
// when the identity did not hold the role, and ErrLastAdmin when it would
// leave the tenant without an active admin.
func Revoke(ctx context.Context, tx pgx.Tx, role, identityID string) error {
	if !IsPlatformRole(role) || !ValidID(identityID) {
		return ErrNotFound
	}
	// Lock the role's entitlement so two admins revoking each other at the
	// same time cannot both succeed and leave nobody in charge.
	var entitlementID string
	err := tx.QueryRow(ctx,
		`SELECT e.id FROM entitlements e
		 JOIN applications app ON app.id = e.application_id AND app.connector = $1
		 WHERE e.native_id = $2 FOR UPDATE OF e`, BuiltinConnector, role).Scan(&entitlementID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	tag, err := tx.Exec(ctx,
		`DELETE FROM account_entitlements ae USING accounts a
		 WHERE ae.account_id = a.id AND ae.entitlement_id = $1 AND a.identity_id = $2`, entitlementID, identityID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	if role == RoleAdmin {
		var admins int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM account_entitlements ae
			 JOIN accounts a ON a.id = ae.account_id AND a.status = 'active'
			 JOIN identities i ON i.id = a.identity_id AND i.status = 'active'
			 WHERE ae.entitlement_id = $1 AND (ae.expires_at IS NULL OR ae.expires_at > now())`,
			entitlementID).Scan(&admins); err != nil {
			return err
		}
		if admins == 0 {
			return ErrLastAdmin
		}
	}
	return nil
}

// BootstrapAdmin makes the active identity with the given email an admin when
// the tenant has no active admin, so a fresh install (or one whose admins all
// left) has someone who can hand out roles. It reports whether it granted.
func BootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, tenantID, email string) (bool, error) {
	var granted bool
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		var hasAdmin bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM entitlements e
			   JOIN applications app ON app.id = e.application_id AND app.connector = $1
			   JOIN account_entitlements ae ON ae.entitlement_id = e.id
			   JOIN accounts a ON a.id = ae.account_id AND a.status = 'active'
			   JOIN identities i ON i.id = a.identity_id AND i.status = 'active'
			   WHERE e.native_id = $2 AND (ae.expires_at IS NULL OR ae.expires_at > now()))`,
			BuiltinConnector, RoleAdmin).Scan(&hasAdmin); err != nil {
			return err
		}
		if hasAdmin {
			return nil
		}
		rows, err := tx.Query(ctx,
			`SELECT id FROM identities WHERE lower(email) = lower($1) AND status = 'active' LIMIT 2`, email)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if len(ids) != 1 {
			return nil
		}
		if err := Grant(ctx, tx, tenantID, RoleAdmin, ids[0], nil); err != nil {
			return err
		}
		granted = true
		return db.Audit(ctx, tx, tenantID, "system:bootstrap", "platform_role.granted", "identity", ids[0],
			map[string]any{"role": RoleAdmin, "reason": "no active admin"})
	})
	if err != nil {
		return false, fmt.Errorf("bootstrap admin: %w", err)
	}
	return granted, nil
}
