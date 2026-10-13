package db

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests need a disposable PostgreSQL database. They are skipped unless
// GRANTRA_TEST_DATABASE_URL is set (CI sets it).
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("GRANTRA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("GRANTRA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestMigrateIsIdempotent(t *testing.T) {
	pool := testPool(t)
	if err := Migrate(context.Background(), pool); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestTenantIsolation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var a, b string
	for slug, id := range map[string]*string{"rls-a": &a, "rls-b": &b} {
		err := pool.QueryRow(ctx,
			`INSERT INTO tenants (slug, name) VALUES ($1, $1)
			 ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`, slug).Scan(id)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Superusers and BYPASSRLS roles skip row-level security, so in that case
	// run the checks as an ordinary role, the way the server must in production.
	var privileged bool
	if err := pool.QueryRow(ctx,
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&privileged); err != nil {
		t.Fatal(err)
	}
	if privileged {
		// Test packages run in parallel and concurrent GRANTs on the same
		// tables fail, so every test role setup takes the same advisory lock.
		_, err := pool.Exec(ctx, `SELECT pg_advisory_xact_lock(7244618);
		DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grantra_rls_test') THEN
				CREATE ROLE grantra_rls_test NOLOGIN NOSUPERUSER NOBYPASSRLS;
			END IF;
		END $$;
		GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA public TO grantra_rls_test;`)
		if err != nil {
			t.Fatal(err)
		}
	}

	inTenant := func(tenant string, fn func(pgx.Tx) error) error {
		return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if privileged {
				if _, err := tx.Exec(ctx, `SET LOCAL ROLE grantra_rls_test`); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
				return err
			}
			return fn(tx)
		})
	}

	err := inTenant(a, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO identities (tenant_id, external_id, display_name)
			VALUES ($1, 'rls-test', 'Ada') ON CONFLICT DO NOTHING`, a)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	count := func(tenant string) int {
		var n int
		err := inTenant(tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM identities WHERE external_id = 'rls-test'`).Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if got := count(a); got != 1 {
		t.Errorf("tenant A sees %d identities, want 1", got)
	}
	if got := count(b); got != 0 {
		t.Errorf("tenant B sees %d of tenant A's identities, want 0", got)
	}

	err = inTenant(b, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO identities (tenant_id, display_name) VALUES ($1, 'Mallory')`, a)
		return err
	})
	if err == nil {
		t.Error("tenant B was able to write a row into tenant A")
	}
}

func TestAuditEventsAreAppendOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var tenant string
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (slug, name) VALUES ('audit-test', 'audit')
		 ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name RETURNING id`).Scan(&tenant); err != nil {
		t.Fatal(err)
	}
	err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenant); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events (tenant_id, actor, action) VALUES ($1, 'test', 'test.created')`, tenant); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE audit_events SET action = 'tampered' WHERE tenant_id = $1`, tenant)
		return err
	})
	if err == nil {
		t.Fatal("expected UPDATE on audit_events to fail")
	}
}

// Every table that holds tenant data must be fenced off by row-level
// security, including the ones later migrations add.
func TestEveryTenantTableHasRowLevelSecurity(t *testing.T) {
	pool := testPool(t)
	rows, err := pool.Query(context.Background(),
		`SELECT c.relname FROM pg_class c
		 JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
		 JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'tenant_id' AND NOT a.attisdropped
		 WHERE c.relkind = 'r' AND NOT (c.relrowsecurity AND c.relforcerowsecurity
		   AND EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid = c.oid))`)
	if err != nil {
		t.Fatal(err)
	}
	open, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if len(open) > 0 {
		t.Errorf("tables with tenant_id but no forced row-level security policy: %v", open)
	}
}
