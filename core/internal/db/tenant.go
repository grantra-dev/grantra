package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InTenant runs fn in a transaction scoped to one tenant: row-level security
// hides every row that belongs to another tenant.
func InTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
			return fmt.Errorf("set tenant: %w", err)
		}
		return fn(tx)
	})
}

// EnsureTenant creates the tenant if it does not exist and returns its id.
// A self-hosted install runs a single tenant, created on first start.
func EnsureTenant(ctx context.Context, pool *pgxpool.Pool, slug, name string) (string, error) {
	var id string
	err := pool.QueryRow(ctx,
		`INSERT INTO tenants (slug, name) VALUES ($1, $2)
		 ON CONFLICT (slug) DO UPDATE SET slug = EXCLUDED.slug
		 RETURNING id`, slug, name).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("ensure tenant %q: %w", slug, err)
	}
	return id, nil
}

// BootstrapIdentity creates a first active identity with the given email when
// the tenant has none yet, so the first person can sign in before any HR
// source is connected. It reports whether it created one.
func BootstrapIdentity(ctx context.Context, pool *pgxpool.Pool, tenantID, email string) (bool, error) {
	var created bool
	err := InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`INSERT INTO identities (tenant_id, display_name, email)
			 SELECT $1, $2, $2
			 WHERE NOT EXISTS (SELECT 1 FROM identities)`, tenantID, email)
		created = tag.RowsAffected() == 1
		return err
	})
	if err != nil {
		return false, fmt.Errorf("bootstrap identity: %w", err)
	}
	return created, nil
}

// Audit appends an event to the tenant's audit log. targetType and targetID
// may be empty when the event has no target.
func Audit(ctx context.Context, tx pgx.Tx, tenantID, actor, action, targetType, targetID string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	var tt, ti *string
	if targetID != "" {
		tt, ti = &targetType, &targetID
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO audit_events (tenant_id, actor, action, target_type, target_id, details)
		 VALUES ($1, $2, $3, $4, $5, $6)`, tenantID, actor, action, tt, ti, details)
	if err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}
