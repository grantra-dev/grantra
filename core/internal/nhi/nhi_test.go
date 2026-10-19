package nhi

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

func TestValidSpiffeID(t *testing.T) {
	for s, want := range map[string]bool{
		"spiffe://example.org":                    true,
		"spiffe://example.org/ns/prod/sa/billing": true,
		"spiffe://prod.example-1.org/a_b/c.d":     true,
		"https://example.org/ns/prod":             false,
		"spiffe://Example.org/x":                  false,
		"spiffe://example.org/":                   false,
		"spiffe://example.org//x":                 false,
		"spiffe://example.org/a/../b":             false,
		"spiffe://example.org:8443/x":             false,
		"spiffe://user@example.org/x":             false,
		"spiffe://example.org/x?y=1":              false,
		"spiffe://example.org/x#y":                false,
		"spiffe:///x":                             false,
	} {
		if got := validSpiffeID(s); got != want {
			t.Errorf("validSpiffeID(%q) = %v, want %v", s, got, want)
		}
	}
}

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	return &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "nhi")}
}

func (e *env) tx(fn func(pgx.Tx) error) error {
	return db.InTenant(context.Background(), e.pool, e.tenant, fn)
}

func (e *env) must(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := e.tx(fn); err != nil {
		e.t.Fatal(err)
	}
}

// id runs an INSERT ... RETURNING id, with the tenant as $1.
func (e *env) id(sql string, args ...any) string {
	e.t.Helper()
	var id string
	e.must(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql, append([]any{e.tenant}, args...)...).Scan(&id)
	})
	return id
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	e.must(func(tx pgx.Tx) error { return tx.QueryRow(context.Background(), sql, args...).Scan(&n) })
	return n
}

func (e *env) person(name, status, manager string) string {
	return e.id(`INSERT INTO identities (tenant_id, display_name, email, status, manager_id)
		VALUES ($1, $2, $2 || '@example.com', $3, nullif($4, '')::uuid) RETURNING id`, name, status, manager)
}

func (e *env) create(in Input) (Identity, error) {
	var out Identity
	err := e.tx(func(tx pgx.Tx) (err error) {
		out, err = Create(context.Background(), tx, e.tenant, "test", in)
		return err
	})
	return out, err
}

func str(s string) *string { return &s }

func invalid(err error) bool {
	var inv *apperr.Invalid
	return errors.As(err, &inv)
}

func TestCreateChecksTheOwner(t *testing.T) {
	e := newEnv(t)
	ana := e.person("Ana", "active", "")
	gone := e.person("Gone", "terminated", "")
	svc, err := e.create(Input{Kind: str("service"), Name: str(" billing-sync "), OwnerID: &ana,
		Purpose: str("Copies invoices"), SpiffeID: str("spiffe://example.org/billing")})
	if err != nil {
		t.Fatal(err)
	}
	if svc.Name != "billing-sync" || svc.OwnerName != "Ana" || svc.Status != "active" || svc.Kind != "service" ||
		svc.SpiffeID != "spiffe://example.org/billing" || len(svc.Flags) != 0 {
		t.Errorf("created %+v", svc)
	}
	tomorrow := time.Now().AddDate(0, 0, 1).Format(time.DateOnly)
	for name, in := range map[string]Input{
		"no owner":          {Kind: str("service"), Name: str("x")},
		"owner left":        {Kind: str("service"), Name: str("x"), OwnerID: &gone},
		"owned by a robot":  {Kind: str("agent"), Name: str("x"), OwnerID: &svc.ID},
		"not an id":         {Kind: str("agent"), Name: str("x"), OwnerID: str("ana")},
		"a person":          {Kind: str("human"), Name: str("x"), OwnerID: &ana},
		"empty name":        {Kind: str("agent"), Name: str("  "), OwnerID: &ana},
		"bad SPIFFE ID":     {Kind: str("workload"), Name: str("x"), OwnerID: &ana, SpiffeID: str("spiffe://x/")},
		"ended":             {Kind: str("agent"), Name: str("x"), OwnerID: &ana, EndDate: str("2020-01-01")},
		"not a date":        {Kind: str("agent"), Name: str("x"), OwnerID: &ana, EndDate: str("soon")},
		"unknown account":   {Kind: str("agent"), Name: str("x"), OwnerID: &ana, AccountIDs: []string{ana}},
		"fine but too many": {Kind: str("agent"), Name: str("x"), OwnerID: &ana, EndDate: &tomorrow, AccountIDs: make([]string, 101)},
	} {
		if _, err := e.create(in); !invalid(err) {
			t.Errorf("%s: err = %v, want invalid", name, err)
		}
	}
	_, err = e.create(Input{Kind: str("workload"), Name: str("copy"), OwnerID: &ana, SpiffeID: str("spiffe://example.org/billing")})
	var conflict *apperr.Conflict
	if !errors.As(err, &conflict) {
		t.Errorf("same SPIFFE ID: err = %v, want conflict", err)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'identity.created' AND target_id = $1`, svc.ID); n != 1 {
		t.Errorf("audits = %d", n)
	}
	// Programs never get platform roles: they call Grantra with tokens.
	err = e.tx(func(tx pgx.Tx) error {
		return authz.Grant(context.Background(), tx, e.tenant, authz.RoleAdmin, svc.ID, nil)
	})
	if !invalid(err) {
		t.Errorf("platform role for a service: err = %v", err)
	}
}

func TestListFlags(t *testing.T) {
	e := newEnv(t)
	ana := e.person("Ana", "active", "")
	gone := e.person("Gone", "terminated", "")
	app := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'AD', 'csv') RETURNING id`)
	root := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name, risk) VALUES ($1, $2, 'da', 'Domain Admins', 'critical') RETURNING id`, app)
	// account opens an account holding ents, last signed in days ago (none
	// when negative).
	account := func(name string, days int, ents ...string) string {
		id := e.id(`INSERT INTO accounts (tenant_id, application_id, native_id, username, last_login_at)
			VALUES ($1, $2, $3, $3, CASE WHEN $4 >= 0 THEN now() - make_interval(days => $4) END) RETURNING id`, app, name, days)
		for _, ent := range ents {
			e.id(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3) RETURNING account_id`, id, ent)
		}
		return id
	}
	mk := func(kind, name, owner string, accounts ...string) Identity {
		t.Helper()
		n, err := e.create(Input{Kind: &kind, Name: &name, OwnerID: &ana, AccountIDs: accounts})
		if err != nil {
			t.Fatal(err)
		}
		if owner != ana {
			e.must(func(tx pgx.Tx) error {
				_, err := tx.Exec(context.Background(), `UPDATE identities SET manager_id = $2 WHERE id = $1`, n.ID, owner)
				return err
			})
		}
		return n
	}
	mk("service", "backup", ana, account("svc-backup", 3, root))
	inVault := account("svc-vaulted", 2, root)
	mk("service", "vaulted", ana, inVault)
	key := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name) VALUES ($1, $2, 'vault-key', 'Key') RETURNING id`, app)
	e.id(`INSERT INTO vault_accounts (tenant_id, name, username, kind, account_id, entitlement_id, created_by)
		VALUES ($1, 'Vaulted', 'svc-vaulted', 'password', $2, $3, 'test') RETURNING id`, inVault, key)
	mk("agent", "stale", ana, account("agent-stale", 120))
	mk("workload", "orphan", gone)
	soon := mk("agent", "soon", ana)
	e.must(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE identities SET end_date = current_date + 10 WHERE id = $1`, soon.ID)
		return err
	})

	var list []Identity
	var counts map[string]int
	e.must(func(tx pgx.Tx) (err error) {
		if list, err = List(context.Background(), tx, Filter{Limit: 50}); err != nil {
			return err
		}
		counts, err = Summary(context.Background(), tx)
		return err
	})
	flags := map[string][]string{}
	for _, n := range list {
		flags[n.Name] = n.Flags
	}
	want := map[string][]string{
		"backup":  {FlagUnvaulted},
		"vaulted": {},
		"stale":   {FlagUnused},
		"orphan":  {FlagNoOwner},
		"soon":    {FlagExpiring},
	}
	for name, w := range want {
		if !slices.Equal(flags[name], w) {
			t.Errorf("%s flags = %v, want %v", name, flags[name], w)
		}
	}
	if counts["total"] != 5 || counts[FlagUnvaulted] != 1 || counts[FlagNoOwner] != 1 || counts[FlagUnused] != 1 || counts[FlagExpiring] != 1 {
		t.Errorf("counts = %v", counts)
	}
	for _, n := range list {
		if n.Name == "backup" && (n.Accounts != 1 || n.Vaulted != 0 || n.RiskyHeld != 1 || n.LastUsedAt == nil) {
			t.Errorf("backup = %+v", n)
		}
	}

	only := func(f Filter) []string {
		t.Helper()
		f.Limit = 50
		var got []Identity
		e.must(func(tx pgx.Tx) (err error) {
			got, err = List(context.Background(), tx, f)
			return err
		})
		var names []string
		for _, n := range got {
			names = append(names, n.Name)
		}
		slices.Sort(names)
		return names
	}
	if got := only(Filter{Kind: "agent"}); !slices.Equal(got, []string{"soon", "stale"}) {
		t.Errorf("agents = %v", got)
	}
	if got := only(Filter{Flag: FlagNoOwner}); !slices.Equal(got, []string{"orphan"}) {
		t.Errorf("no owner = %v", got)
	}
	if got := only(Filter{Search: "BACK"}); !slices.Equal(got, []string{"backup"}) {
		t.Errorf("search = %v", got)
	}
	e.must(func(tx pgx.Tx) error {
		if err := (Filter{Flag: "weird"}).Validate(); !invalid(err) {
			t.Errorf("bad flag: %v", err)
		}
		_, err := List(context.Background(), tx, Filter{Kind: "human", Limit: 1})
		if !invalid(err) {
			t.Errorf("kind human: %v", err)
		}
		return nil
	})
}

func TestUpdateAndRetire(t *testing.T) {
	e := newEnv(t)
	ana := e.person("Ana", "active", "")
	bob := e.person("Bob", "active", "")
	n, err := e.create(Input{Kind: str("agent"), Name: str("helper"), OwnerID: &ana, Purpose: str("Answers tickets")})
	if err != nil {
		t.Fatal(err)
	}
	update := func(in Input) (Identity, error) {
		var out Identity
		err := e.tx(func(tx pgx.Tx) (err error) {
			out, err = Update(context.Background(), tx, e.tenant, "test", n.ID, in)
			return err
		})
		return out, err
	}
	next := time.Now().AddDate(0, 2, 0).Format(time.DateOnly)
	got, err := update(Input{OwnerID: &bob, Purpose: str(""), EndDate: &next})
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerName != "Bob" || got.Purpose != "" || got.EndDate != next || got.Name != "helper" {
		t.Errorf("updated %+v", got)
	}
	if got, err = update(Input{EndDate: str("")}); err != nil || got.EndDate != "" {
		t.Errorf("cleared end date: %+v, %v", got, err)
	}
	if _, err := update(Input{Kind: str("service")}); !invalid(err) {
		t.Errorf("changing kind: %v", err)
	}
	if _, err := update(Input{OwnerID: &n.ID}); !invalid(err) {
		t.Errorf("owning itself: %v", err)
	}
	e.must(func(tx pgx.Tx) error {
		if _, err := Update(context.Background(), tx, e.tenant, "test", ana, Input{Name: str("x")}); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("updating a person: %v", err)
		}
		return nil
	})

	e.must(func(tx pgx.Tx) (err error) {
		got, err = Retire(context.Background(), tx, e.tenant, "test", n.ID, "Replaced")
		return err
	})
	if got.Status != "terminated" || got.EndDate != time.Now().Format(time.DateOnly) || len(got.Flags) != 0 {
		t.Errorf("retired %+v", got)
	}
	// Retiring is a leaver event: the birthright job removes its access.
	if c := e.count(`SELECT count(*) FROM lifecycle_events WHERE identity_id = $1 AND kind = 'leaver' AND processed_at IS NULL`, n.ID); c != 1 {
		t.Errorf("leaver events = %d", c)
	}
	if _, err := update(Input{Name: str("again")}); !invalid(err) {
		t.Errorf("updating a retired one: %v", err)
	}
	e.must(func(tx pgx.Tx) error {
		list, err := List(context.Background(), tx, Filter{Limit: 10})
		if len(list) != 0 {
			t.Errorf("retired listed by default: %v", list)
		}
		if list, _ = List(context.Background(), tx, Filter{Retired: true, Limit: 10}); len(list) != 1 {
			t.Errorf("retired = %v", list)
		}
		return err
	})
}

func TestOwnersLeft(t *testing.T) {
	e := newEnv(t)
	boss := e.person("Boss", "active", "")
	ana := e.person("Ana", "active", boss)
	luis := e.person("Luis", "active", "")
	mine, _ := e.create(Input{Kind: str("service"), Name: str("ana-svc"), OwnerID: &ana})
	other, _ := e.create(Input{Kind: str("agent"), Name: str("luis-agent"), OwnerID: &luis})
	kept, _ := e.create(Input{Kind: str("agent"), Name: str("kept"), OwnerID: &boss})
	e.must(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE identities SET status = 'terminated' WHERE id = ANY($1::uuid[])`, []string{ana, luis})
		if err != nil {
			return err
		}
		return OwnersLeft(context.Background(), tx, e.tenant, "system:lifecycle", []string{ana, luis})
	})
	owner := func(id string) string {
		var o *string
		e.must(func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `SELECT manager_id::text FROM identities WHERE id = $1`, id).Scan(&o)
		})
		if o == nil {
			return ""
		}
		return *o
	}
	if owner(mine.ID) != boss || owner(other.ID) != "" || owner(kept.ID) != boss {
		t.Errorf("owners = %s %s %s", owner(mine.ID), owner(other.ID), owner(kept.ID))
	}
	if c := e.count(`SELECT count(*) FROM audit_events WHERE action = 'identity.owner_changed'`); c != 2 {
		t.Errorf("audits = %d", c)
	}
	// The boss hears they own ana-svc now; admins (there are none) would
	// hear about both.
	if c := e.count(`SELECT count(*) FROM notification_outbox WHERE identity_id = $1 AND kind = 'non_human_owner_left' AND (data ->> 'count')::int = 1`, boss); c != 1 {
		t.Errorf("boss notifications = %d", c)
	}
}

func TestCandidates(t *testing.T) {
	e := newEnv(t)
	ana := e.person("Ana", "active", "")
	app := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'AD', 'csv') RETURNING id`)
	builtin := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'Grantra', $2) RETURNING id`, authz.BuiltinConnector)
	risky := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name, risk) VALUES ($1, $2, 'da', 'DA', 'high') RETURNING id`, app)
	account := func(app, name string, owner *string) string {
		return e.id(`INSERT INTO accounts (tenant_id, application_id, native_id, username, identity_id) VALUES ($1, $2, $3, $3, $4) RETURNING id`,
			app, name, owner)
	}
	account(app, "maria", nil)
	admin := account(app, "admin2", nil)
	e.id(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3) RETURNING account_id`, admin, risky)
	account(app, "svc-backup", nil)
	account(app, "deploy_bot", nil)
	account(app, "ana", &ana)
	account(builtin, "grantra-x", nil)
	var got []Candidate
	e.must(func(tx pgx.Tx) (err error) {
		got, err = Candidates(context.Background(), tx)
		return err
	})
	var names []string
	for _, c := range got {
		names = append(names, c.Username)
	}
	if !slices.Equal(names, []string{"deploy_bot", "svc-backup", "admin2", "maria"}) {
		t.Errorf("candidates = %v", names)
	}
	if !got[0].LooksLikeService || got[2].LooksLikeService || !got[2].Risky || got[3].Risky {
		t.Errorf("candidates = %+v", got)
	}
}
