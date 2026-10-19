package attackpaths

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	e := &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "paths")}
	if err := authz.EnsureBuiltinApp(context.Background(), pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) tx(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(context.Background(), e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

// id runs an INSERT ... RETURNING id.
func (e *env) id(sql string, args ...any) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql, append([]any{e.tenant}, args...)...).Scan(&id)
	})
	return id
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), sql, append([]any{e.tenant}, args...)...)
		return err
	})
}

func (e *env) person(name, status string) string {
	return e.id(`INSERT INTO identities (tenant_id, display_name, status) VALUES ($1, $2, $3) RETURNING id`, name, status)
}

func (e *env) account(app, owner, user string, ents ...string) string {
	id := e.id(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username)
		VALUES ($1, $2, nullif($3, '')::uuid, $4, $4) RETURNING id`, app, owner, user)
	for _, ent := range ents {
		e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3)`, id, ent)
	}
	return id
}

func (e *env) graph() *Graph {
	e.t.Helper()
	var g *Graph
	e.tx(func(tx pgx.Tx) (err error) {
		g, err = Load(context.Background(), tx)
		return err
	})
	return g
}

// describe writes a path the way a test reads it: "Dan -requested-> vault_account backup -logs_in_as-> ...".
func describe(p Path) string {
	var b strings.Builder
	for i, s := range p.Steps {
		if i > 0 {
			b.WriteString(" -" + s.Edge + "-> ")
		}
		if s.Kind != KindPerson {
			b.WriteString(s.Kind + " ")
		}
		b.WriteString(s.Name)
	}
	return b.String()
}

func TestPathsToATarget(t *testing.T) {
	e := newEnv(t)
	ad := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'Active Directory', 'ldap') RETURNING id`)
	vaultApp := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'Privileged vault', 'manual') RETURNING id`)
	da := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name, risk) VALUES ($1, $2, 'da', 'Domain Admins', 'critical') RETURNING id`, ad)
	low := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name) VALUES ($1, $2, 'users', 'Domain Users') RETURNING id`, ad)
	useBackup := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name, risk) VALUES ($1, $2, 'backup', 'Use backup', 'high') RETURNING id`, vaultApp)
	useDB := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name) VALUES ($1, $2, 'db', 'Connect db') RETURNING id`, vaultApp)

	ana := e.person("Ana", "active")
	ops := e.id(`INSERT INTO roles (tenant_id, name) VALUES ($1, 'Ops') RETURNING id`)
	e.exec(`INSERT INTO role_entitlements (tenant_id, role_id, entitlement_id) VALUES ($1, $2, $3)`, ops, da)
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, role_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`, ana, ops)
	// Ana's account in the directory holds Domain Admins because the role
	// gave it: the same path, not another one.
	e.account(ad, ana, "ana", da, low)

	bob := e.person("Bob", "active")
	e.exec(`INSERT INTO access_eligibilities (tenant_id, identity_id, entitlement_id, granted_by) VALUES ($1, $2, $3, 'test')`, bob, da)

	carla := e.person("Carla", "active")
	e.account(ad, carla, "carla", da)

	// The backup service account holds Domain Admins, and the vault hands
	// out its password; a database target logs in with it too.
	svc := e.account(ad, "", "svc-backup", da)
	backup := e.id(`INSERT INTO vault_accounts (tenant_id, name, username, kind, account_id, entitlement_id, created_by)
		VALUES ($1, 'backup', 'svc-backup', 'password', $2, $3, 'test') RETURNING id`, svc, useBackup)
	e.exec(`INSERT INTO gateway_targets (tenant_id, name, kind, address, vault_account_id, entitlement_id, created_by)
		VALUES ($1, 'backup-db', 'postgres', 'db:5432', $2, $3, 'test')`, backup, useDB)
	dan := e.person("Dan", "active")
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'request', 'test')`, dan, useBackup)
	eve := e.person("Eve", "active")
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
		VALUES ($1, $2, $3, 'manual', 'test', now() + interval '1 day')`, eve, useDB)

	frank := e.person("Frank", "terminated")
	e.account(ad, frank, "frank-admin", da)

	gina := e.person("Gina", "active")
	rule := e.id(`INSERT INTO access_rules (tenant_id, name) VALUES ($1, 'Admins') RETURNING id`)
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, rule_id, granted_by) VALUES ($1, $2, $3, 'birthright', $4, 'test')`, gina, da, rule)

	g := e.graph()
	d, err := g.Detail(da)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range d.Paths {
		got = append(got, describe(p))
	}
	want := []string{
		"Ana -assigned-> role Ops -includes-> entitlement Domain Admins",
		"Bob -eligible-> entitlement Domain Admins",
		"Carla -owns-> account carla -holds-> entitlement Domain Admins",
		"Dan -requested-> vault_account backup -logs_in_as-> account svc-backup -holds-> entitlement Domain Admins",
		"Eve -assigned-> gateway_target backup-db -logs_in_as-> account svc-backup -holds-> entitlement Domain Admins",
		"Gina -matches-> rule Admins -gives-> entitlement Domain Admins",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("paths:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	want2 := Target{ID: da, Name: "Domain Admins", Application: "Active Directory", Risk: "critical",
		People: 6, Standing: 4, Credential: 2, Outside: 1, Orphans: 1}
	if d.Target != want2 {
		t.Fatalf("target = %+v, want %+v", d.Target, want2)
	}
	// Ana, Carla, Dan and Gina hold it for good; Bob must turn it on and
	// Eve's access ends tomorrow.
	if !d.Paths[3].Standing || d.Paths[1].Standing || d.Paths[4].Standing || d.Paths[4].Until == nil {
		t.Fatalf("standing: %+v", d.Paths)
	}
	if len(d.OrphanAccounts) != 1 || d.OrphanAccounts[0].Account != "frank-admin" || d.OrphanAccounts[0].Owner != "Frank" || d.OrphanAccounts[0].OwnerStatus != "terminated" {
		t.Fatalf("orphans = %+v, want Frank's account only (the vaulted one is handed out)", d.OrphanAccounts)
	}

	// Whoever takes over Dan's account reaches Domain Admins through the
	// vault, and the vault account itself.
	reach := g.Reach(dan)
	if len(reach) != 2 || reach[0].ID != da || !reach[0].Paths[0].Credential || reach[1].ID != useBackup {
		t.Fatalf("Dan reaches %+v", reach)
	}
	if r := g.Reach(frank); len(r) != 0 {
		t.Fatalf("a terminated person reaches %+v", r)
	}

	targets := g.Targets()
	if len(targets) != 2 || targets[0].ID != da || targets[1].ID != useBackup || targets[1].People != 1 {
		t.Fatalf("targets = %+v, want Domain Admins then the backup vault account; low risk access is no target", targets)
	}
	if _, err := g.Detail(low); err != apperr.ErrNotFound {
		t.Fatalf("detail of low risk access: %v", err)
	}
	if _, err := g.Detail("nope"); err != apperr.ErrNotFound {
		t.Fatalf("detail of nothing: %v", err)
	}
}

func TestPlatformRolesAreTargets(t *testing.T) {
	e := newEnv(t)
	ada := e.person("Ada", "active")
	e.tx(func(tx pgx.Tx) error {
		return authz.Grant(context.Background(), tx, e.tenant, authz.RoleAdmin, ada, nil)
	})
	targets := e.graph().Targets()
	i := slices.IndexFunc(targets, func(t Target) bool { return t.Name == "Grantra admin" })
	if i < 0 || targets[i].People != 1 || targets[i].Outside != 0 || targets[i].Standing != 1 {
		t.Fatalf("targets = %+v, want the admin role held by Ada, given in Grantra", targets)
	}
}

func TestCredentialLoopEnds(t *testing.T) {
	e := newEnv(t)
	app := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'Vault', 'manual') RETURNING id`)
	key := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name, risk) VALUES ($1, $2, 'k', 'Key', 'critical') RETURNING id`, app)
	// An account that holds the very entitlement handing out its own
	// password.
	acct := e.account(app, "", "loop", key)
	e.exec(`INSERT INTO vault_accounts (tenant_id, name, username, kind, account_id, entitlement_id, created_by)
		VALUES ($1, 'loop', 'loop', 'password', $2, $3, 'test')`, acct, key)
	ana := e.person("Ana", "active")
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`, ana, key)
	d, err := e.graph().Detail(key)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Paths) != 1 || d.Paths[0].Credential {
		t.Fatalf("paths = %+v, want Ana's assignment once", d.Paths)
	}
}
