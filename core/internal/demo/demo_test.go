package demo

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/sod"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

func TestSeed(t *testing.T) {
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "demo")
	ctx := context.Background()
	if err := authz.EnsureBuiltinApp(ctx, pool, tenant); err != nil {
		t.Fatal(err)
	}
	seeded, err := Seed(ctx, pool, tenant, Options{})
	if err != nil || !seeded {
		t.Fatalf("Seed = %v, %v", seeded, err)
	}
	if again, err := Seed(ctx, pool, tenant, Options{}); err != nil || again {
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
	if n := count(`SELECT count(*) FROM applications WHERE connector IS DISTINCT FROM $1`, authz.BuiltinConnector); n != 5 {
		t.Errorf("applications = %d, want 5 (4 and SSH servers)", n)
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

func TestSeedWithGrantraLogin(t *testing.T) {
	t.Setenv("GRANTRA_SECRET_GRANTRA_LOGIN", "grantra-dev-connector-secret")
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "demo-login")
	ctx := context.Background()
	if err := authz.EnsureBuiltinApp(ctx, pool, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(ctx, pool, tenant, Options{LoginURL: "http://keycloak:8080"}); err != nil {
		t.Fatal(err)
	}
	var kind string
	var cfg map[string]any
	if err := db.InTenant(ctx, pool, tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT connector, config FROM applications WHERE name = 'Grantra Login'`).Scan(&kind, &cfg)
	}); err != nil {
		t.Fatal(err)
	}
	if kind != "grantra-login" || cfg["url"] != "http://keycloak:8080" || cfg["allow_http"] != true {
		t.Errorf("Grantra Login = %s %v", kind, cfg)
	}
}

func TestSeedWithVault(t *testing.T) {
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "demo-vault")
	ctx := context.Background()
	if err := authz.EnsureBuiltinApp(ctx, pool, tenant); err != nil {
		t.Fatal(err)
	}
	key, err := vault.NewLocalKey("Z3JhbnRyYS1kZW1vLXZhdWx0LWtleS0wMDAwMDAwMDA=")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Seed(ctx, pool, tenant, Options{Vault: vault.New(key, nil)}); err != nil {
		t.Fatal(err)
	}
	var version int
	var entitlement string
	if err := db.InTenant(ctx, pool, tenant, func(tx pgx.Tx) error {
		list, err := vault.List(ctx, tx, "")
		// By name: the office desktop's login, the root password, the shop
		// database's owner.
		if err != nil || len(list) != 3 {
			t.Fatalf("vault accounts = %+v, %v", list, err)
		}
		root := list[1]
		if root.Name != "Production database root" {
			root = list[1]
		}
		version, entitlement = root.Version, root.EntitlementName
		// Luis, on call, is its only break-glass responder.
		ids, glass, err := vault.Responders(ctx, tx, root.ID)
		var luis string
		if err == nil {
			err = tx.QueryRow(ctx, `SELECT id FROM identities WHERE email = 'luis.perez@example.com'`).Scan(&luis)
		}
		if err != nil || !glass || len(ids) != 1 || ids[0] != luis {
			t.Fatalf("responders = %v, %v, %v", ids, glass, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if version != 1 || entitlement != "Use Production database root" {
		t.Fatalf("version %d, entitlement %q", version, entitlement)
	}
	// Luis may activate GitHub org admin, SSH to the production database
	// server, the shop database and the office desktop, which nobody holds
	// for good.
	if err := db.InTenant(ctx, pool, tenant, func(tx pgx.Tx) error {
		var luis string
		if err := tx.QueryRow(ctx, `SELECT id FROM identities WHERE email = 'luis.perez@example.com'`).Scan(&luis); err != nil {
			return err
		}
		list, err := access.Eligibilities(ctx, tx, luis)
		names := map[string]int{}
		for _, el := range list {
			names[el.Name] = el.MaxActivationHours
		}
		if err != nil || len(list) != 4 || names["Org admins"] != 2 || names["Log in to prod-db as postgres"] == 0 ||
			names["Connect to the shop-db database"] == 0 || names["Use the office-pc desktop"] == 0 {
			t.Fatalf("luis's eligibilities = %+v, %v", list, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
