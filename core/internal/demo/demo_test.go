package demo

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/sod"
)

func TestSeed(t *testing.T) {
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "demo")
	ctx := context.Background()
	if err := authz.EnsureBuiltinApp(ctx, pool, tenant); err != nil {
		t.Fatal(err)
	}
	seeded, err := Seed(ctx, pool, tenant)
	if err != nil || !seeded {
		t.Fatalf("Seed = %v, %v", seeded, err)
	}
	if again, err := Seed(ctx, pool, tenant); err != nil || again {
		t.Fatalf("second Seed = %v, %v; want nothing done", again, err)
	}

	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.InTenant(ctx, pool, tenant, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, q, args...).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM identities WHERE status = 'active'`); n != 8 {
		t.Errorf("active people = %d, want 8", n)
	}
	if n := count(`SELECT count(*) FROM applications WHERE connector IS DISTINCT FROM $1`, authz.BuiltinConnector); n != 4 {
		t.Errorf("applications = %d, want 4", n)
	}
	if n := count(`SELECT count(*) FROM access_rules`); n != 3 {
		t.Errorf("rules = %d, want 3", n)
	}
	// Birthright access followed the HR feed: Sara is a finance clerk.
	if n := count(`SELECT count(*) FROM access_assignments x JOIN identities i ON i.id = x.identity_id
		JOIN roles r ON r.id = x.role_id WHERE i.email = 'sara.martin@example.com' AND r.name = 'Finance clerk' AND x.origin = 'birthright'`); n != 1 {
		t.Errorf("Sara's birthright roles = %d, want 1", n)
	}
	// The head of finance holds both sides of the payments policy, which a
	// scan finds.
	res, err := sod.Scan(ctx, pool, tenant)
	if err != nil {
		t.Fatal(err)
	}
	if res.Detected != 1 {
		t.Errorf("scan = %+v, want Pedro's violation", res)
	}
}
