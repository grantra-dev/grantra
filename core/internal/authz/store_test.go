package authz

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

type fixture struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "authz")
	if err := EnsureBuiltinApp(context.Background(), pool, tenant); err != nil {
		t.Fatal(err)
	}
	// Running it again changes nothing.
	if err := EnsureBuiltinApp(context.Background(), pool, tenant); err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, pool: pool, tenant: tenant}
}

func (f *fixture) tx(fn func(pgx.Tx) error) error {
	return db.InTenant(context.Background(), f.pool, f.tenant, fn)
}

func (f *fixture) must(fn func(pgx.Tx) error) {
	f.t.Helper()
	if err := f.tx(fn); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) identity(email, status string) string {
	f.t.Helper()
	var id string
	f.must(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email, status) VALUES ($1, $2, $2, $3) RETURNING id`,
			f.tenant, email, status).Scan(&id)
	})
	return id
}

func (f *fixture) roles(id string) []string {
	f.t.Helper()
	var s Subject
	f.must(func(tx pgx.Tx) error {
		var err error
		s, err = LoadSubject(context.Background(), tx, id)
		return err
	})
	return s.Roles
}

func TestBuiltinApp(t *testing.T) {
	f := newFixture(t)
	var apps, ents int
	f.must(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(DISTINCT app.id), count(e.id) FROM applications app
			 JOIN entitlements e ON e.application_id = app.id WHERE app.connector = 'grantra'`).Scan(&apps, &ents)
	})
	if apps != 1 || ents != len(PlatformRoles) {
		t.Fatalf("built-in apps = %d with %d entitlements, want 1 with %d", apps, ents, len(PlatformRoles))
	}
}

func TestGrantAndRevoke(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ana := f.identity("ana@example.com", "active")
	luis := f.identity("luis@example.com", "active")

	f.must(func(tx pgx.Tx) error { return Grant(ctx, tx, f.tenant, RoleAdmin, ana, nil) })
	f.must(func(tx pgx.Tx) error { return Grant(ctx, tx, f.tenant, RoleAuditor, ana, nil) })
	if got := f.roles(ana); !slices.Equal(got, []string{RoleAdmin, RoleAuditor}) {
		t.Fatalf("roles = %v", got)
	}

	// Expired grants do not count.
	f.must(func(tx pgx.Tx) error { return Grant(ctx, tx, f.tenant, RoleHelpdesk, luis, nil) })
	f.must(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE account_entitlements ae SET expires_at = now() - interval '1 second'
			FROM accounts a WHERE a.id = ae.account_id AND a.identity_id = $1`, luis)
		return err
	})
	if got := f.roles(luis); len(got) != 0 {
		t.Fatalf("roles after expiry = %v, want none", got)
	}

	// The last active admin cannot be revoked, and the refusal changes nothing.
	if err := f.tx(func(tx pgx.Tx) error { return Revoke(ctx, tx, RoleAdmin, ana) }); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("revoke last admin = %v, want ErrLastAdmin", err)
	}
	if got := f.roles(ana); !slices.Contains(got, RoleAdmin) {
		t.Fatalf("roles after refused revoke = %v", got)
	}
	f.must(func(tx pgx.Tx) error { return Grant(ctx, tx, f.tenant, RoleAdmin, luis, nil) })
	f.must(func(tx pgx.Tx) error { return Revoke(ctx, tx, RoleAdmin, ana) })
	if got := f.roles(ana); !slices.Equal(got, []string{RoleAuditor}) {
		t.Fatalf("roles after revoke = %v", got)
	}

	// Revoking what is not held, or unknown roles and people, is not found.
	for name, fn := range map[string]func(pgx.Tx) error{
		"not held":     func(tx pgx.Tx) error { return Revoke(ctx, tx, RoleHelpdesk, ana) },
		"unknown role": func(tx pgx.Tx) error { return Grant(ctx, tx, f.tenant, "grantra-root", ana, nil) },
		"bad id":       func(tx pgx.Tx) error { return Grant(ctx, tx, f.tenant, RoleAdmin, "nope", nil) },
		"no such id": func(tx pgx.Tx) error {
			return Grant(ctx, tx, f.tenant, RoleAdmin, "00000000-0000-0000-0000-000000000000", nil)
		},
	} {
		if err := f.tx(fn); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", name, err)
		}
	}

	// Inactive people get no roles.
	gone := f.identity("gone@example.com", "terminated")
	if err := f.tx(func(tx pgx.Tx) error { return Grant(ctx, tx, f.tenant, RoleAuditor, gone, nil) }); !errors.Is(err, ErrInactive) {
		t.Fatalf("grant to terminated = %v, want ErrInactive", err)
	}
}

func TestBootstrapAdmin(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first := f.identity("First@example.com", "active")
	second := f.identity("second@example.com", "active")

	granted, err := BootstrapAdmin(ctx, f.pool, f.tenant, "first@example.com")
	if err != nil || !granted {
		t.Fatalf("BootstrapAdmin = %v, %v; want granted", granted, err)
	}
	if got := f.roles(first); !slices.Equal(got, []string{RoleAdmin}) {
		t.Fatalf("roles = %v", got)
	}
	// With an admin in place it does nothing, whoever it names.
	if granted, err := BootstrapAdmin(ctx, f.pool, f.tenant, "second@example.com"); err != nil || granted {
		t.Fatalf("second BootstrapAdmin = %v, %v; want no grant", granted, err)
	}
	if got := f.roles(second); len(got) != 0 {
		t.Fatalf("second roles = %v", got)
	}

	// Once the only admin is offboarded, the bootstrap email becomes admin again.
	f.must(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE identities SET status = 'terminated' WHERE id = $1`, first)
		return err
	})
	if granted, err := BootstrapAdmin(ctx, f.pool, f.tenant, "second@example.com"); err != nil || !granted {
		t.Fatalf("BootstrapAdmin after offboarding = %v, %v; want granted", granted, err)
	}
	var n int
	f.must(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events
			WHERE action = 'platform_role.granted' AND actor = 'system:bootstrap'`).Scan(&n)
	})
	if n != 2 {
		t.Errorf("bootstrap audit events = %d, want 2", n)
	}
}
