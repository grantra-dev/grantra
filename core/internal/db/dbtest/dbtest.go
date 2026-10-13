// Package dbtest gives tests a migrated PostgreSQL database and a fresh
// tenant in it, connected the way the server must be: as a role without
// BYPASSRLS, so row-level security applies.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
)

// Open returns a pool on the database in GRANTRA_TEST_DATABASE_URL, skipping
// the test when it is not set (CI sets it).
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("GRANTRA_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GRANTRA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Open(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}

	var privileged bool
	if err := pool.QueryRow(ctx,
		`SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&privileged); err != nil {
		t.Fatal(err)
	}
	if !privileged {
		return pool
	}
	// Test packages run in parallel and concurrent GRANTs on the same tables
	// fail, so every test role setup takes the same advisory lock.
	_, err = pool.Exec(ctx, `SELECT pg_advisory_xact_lock(7244618);
	DO $$ BEGIN
		IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grantra_auth_test') THEN
			CREATE ROLE grantra_auth_test LOGIN PASSWORD 'grantra_auth_test' NOSUPERUSER NOBYPASSRLS;
		END IF;
	END $$;
	GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO grantra_auth_test;
	GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO grantra_auth_test;`)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword("grantra_auth_test", "grantra_auth_test")
	ordinary, err := db.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ordinary.Close)
	return ordinary
}

// Tenant creates a tenant with a random slug starting with prefix.
func Tenant(t *testing.T, pool *pgxpool.Pool, prefix string) string {
	t.Helper()
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	id, err := db.EnsureTenant(context.Background(), pool, prefix+"-"+hex.EncodeToString(b), prefix)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
