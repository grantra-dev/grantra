package requests

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
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
	e := &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "requests")}
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

func (e *env) identity(name, manager string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email, manager_id) VALUES ($1, $2, $2 || '@example.com', nullif($3, '')::uuid) RETURNING id`,
			e.tenant, name, manager).Scan(&id)
	})
	return id
}

// entitlement creates an application owned by owner with one entitlement.
func (e *env) entitlement(app, owner string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		a, err := catalog.CreateApplication(context.Background(), tx, e.tenant, app, owner)
		if err != nil {
			return err
		}
		ent, err := catalog.CreateEntitlement(context.Background(), tx, e.tenant, a.ID, catalog.EntitlementInput{NativeID: "users", Name: app + " users"})
		id = ent.ID
		return err
	})
	return id
}

func (e *env) create(requester string, in Input) (Request, error) {
	e.t.Helper()
	var (
		r   Request
		err error
	)
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		r, err = Create(context.Background(), tx, e.tenant, requester, in)
		return err
	})
	return r, err
}

func (e *env) decide(actor, id string, approve bool) Request {
	e.t.Helper()
	var r Request
	e.tx(func(tx pgx.Tx) error {
		req, err := Get(context.Background(), tx, id, true)
		if err != nil {
			return err
		}
		r, err = Decide(context.Background(), tx, e.tenant, "identity:"+actor, req, approve, "")
		return err
	})
	return r
}

func (e *env) assignments(identity string) int {
	e.t.Helper()
	var n int
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT count(*) FROM access_assignments WHERE identity_id = $1 AND origin = 'request' AND request_id IS NOT NULL`, identity).Scan(&n)
	})
	return n
}

func kinds(r Request) []string {
	var out []string
	for _, s := range r.Steps {
		out = append(out, s.Kind+":"+s.Status)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRequestFlow(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	owner := e.identity("Owner", "")
	crm := e.entitlement("CRM", owner)

	in := Input{EntitlementID: crm, Justification: "  I sell things  ", DurationDays: new(int)}
	*in.DurationDays = 30
	var inv *apperr.Invalid
	if _, err := e.create(ana, Input{EntitlementID: crm}); !errors.As(err, &inv) {
		t.Errorf("no justification = %v", err)
	}
	r, err := e.create(ana, in)
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != Pending || r.Justification != "I sell things" || !equal(kinds(r), []string{"manager:pending", "owner:waiting"}) {
		t.Fatalf("request = %+v", r)
	}
	if c := r.Current(); c == nil || c.ApproverID != boss || c.DueAt == nil {
		t.Errorf("current step = %+v", c)
	}
	var conflict *apperr.Conflict
	if _, err := e.create(ana, in); !errors.As(err, &conflict) {
		t.Errorf("second open request = %v", err)
	}

	// Waiting on the boss: the owner does not see it yet.
	e.tx(func(tx pgx.Tx) error {
		for who, want := range map[string]int{boss: 1, owner: 0, ana: 0} {
			list, err := List(context.Background(), tx, Filter{Scope: ScopeApprovals, Me: who, Limit: 10})
			if err != nil {
				return err
			}
			if len(list) != want {
				t.Errorf("approvals of %s = %d, want %d", who, len(list), want)
			}
		}
		mine, err := List(context.Background(), tx, Filter{Scope: ScopeMine, Me: ana, Limit: 10})
		if len(mine) != 1 || len(mine[0].Steps) != 2 {
			t.Errorf("ana's requests = %+v", mine)
		}
		return err
	})

	r = e.decide(boss, r.ID, true)
	if !equal(kinds(r), []string{"manager:approved", "owner:pending"}) || r.Status != Pending {
		t.Errorf("after the boss = %v %s", kinds(r), r.Status)
	}
	r = e.decide(owner, r.ID, true)
	if r.Status != Approved || r.ClosedAt == nil || e.assignments(ana) != 1 {
		t.Errorf("approved = %+v", r)
	}
	e.tx(func(tx pgx.Tx) error {
		var days float64
		err := tx.QueryRow(context.Background(),
			`SELECT extract(epoch FROM expires_at - now()) / 86400 FROM access_assignments WHERE identity_id = $1`, ana).Scan(&days)
		if days < 29.9 || days > 30.1 {
			t.Errorf("access lasts %.2f days", days)
		}
		return err
	})
	e.tx(func(tx pgx.Tx) error {
		_, err := Decide(context.Background(), tx, e.tenant, "identity:"+owner, r, true, "")
		if !errors.As(err, &conflict) {
			t.Errorf("deciding a closed request = %v", err)
		}
		return nil
	})
	if _, err := e.create(ana, in); !errors.As(err, &conflict) {
		t.Errorf("asking for access already held = %v", err)
	}

	// Rejected by the manager: the owner is never asked.
	luis := e.identity("Luis", boss)
	r, err = e.create(luis, in)
	if err != nil {
		t.Fatal(err)
	}
	r = e.decide(boss, r.ID, false)
	if r.Status != Rejected || !equal(kinds(r), []string{"manager:rejected", "owner:skipped"}) || e.assignments(luis) != 0 {
		t.Errorf("rejected = %v %s", kinds(r), r.Status)
	}
}

func TestApprovalChain(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	orphan := e.identity("Orphan", "")
	crm := e.entitlement("CRM", boss)       // the boss owns it too
	wiki := e.entitlement("Wiki", "")       // nobody owns it
	mine := e.entitlement("Bossware", boss) // the boss asks for their own app
	ent := func(id string) Input { return Input{EntitlementID: id, Justification: "work"} }

	cases := []struct {
		name      string
		requester string
		in        Input
		want      []string
		status    string
	}{
		{"manager and owner are one person", ana, ent(crm), []string{"manager:pending"}, Pending},
		{"nobody owns it", ana, ent(wiki), []string{"manager:pending", "admin:waiting"}, Pending},
		{"no manager, no owner", orphan, ent(wiki), []string{"admin:pending"}, Pending},
		{"the boss asks for a report", boss, Input{BeneficiaryID: orphan, EntitlementID: crm, Justification: "work"}, []string{"admin:pending", "owner:approved"}, Pending},
		{"nobody approves their own access", boss, ent(mine), []string{"admin:pending"}, Pending},
	}
	for _, c := range cases {
		r, err := e.create(c.requester, c.in)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !equal(kinds(r), c.want) || r.Status != c.status {
			t.Errorf("%s: steps %v %s, want %v %s", c.name, kinds(r), r.Status, c.want, c.status)
		}
	}

	// A manager asking for their report approves the manager step by
	// asking; when they also own the application, nothing is left to ask.
	r, err := e.create(boss, Input{BeneficiaryID: ana, EntitlementID: mine, Justification: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != Approved || !equal(kinds(r), []string{"manager:approved"}) || e.assignments(ana) != 1 {
		t.Errorf("boss for report on own app = %v %s", kinds(r), r.Status)
	}

	// Admin steps show up for admins only.
	e.tx(func(tx pgx.Tx) error {
		list, err := List(context.Background(), tx, Filter{Scope: ScopeApprovals, Me: ana, Admin: true, Limit: 50})
		if len(list) != 3 {
			t.Errorf("admin approvals = %d", len(list))
		}
		return err
	})
}

func TestExpireDue(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	luis := e.identity("Luis", boss)
	crm := e.entitlement("CRM", "")
	slow, err := e.create(ana, Input{EntitlementID: crm, Justification: "work"})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := e.create(luis, Input{EntitlementID: crm, Justification: "work"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	e.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE access_request_steps SET due_at = now() - interval '1 minute' WHERE request_id = $1`, slow.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE identities SET status = 'terminated' WHERE id = $1`, luis); err != nil {
			return err
		}
		// Some access whose time is up.
		_, err := tx.Exec(ctx,
			`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
			 VALUES ($1, $2, $3, 'manual', 'test', now() - interval '1 second')`, e.tenant, boss, crm)
		return err
	})
	h, err := ExpireDue(ctx, e.pool, e.tenant)
	if err != nil || h != (Housekeeping{Expired: 1, Cancelled: 1, AccessEnded: 1}) {
		t.Fatalf("housekeeping = %+v, %v", h, err)
	}
	e.tx(func(tx pgx.Tx) error {
		for id, want := range map[string]string{slow.ID: Expired, gone.ID: Cancelled} {
			r, err := Get(ctx, tx, id, false)
			if err != nil {
				return err
			}
			if r.Status != want || r.Reason == "" || r.Current() != nil {
				t.Errorf("request = %s %q %v, want %s", r.Status, r.Reason, kinds(r), want)
			}
		}
		return nil
	})
	if h, err := ExpireDue(ctx, e.pool, e.tenant); err != nil || h != (Housekeeping{}) {
		t.Errorf("second pass = %+v, %v", h, err)
	}

	// Platform roles are not requestable.
	e.tx(func(tx pgx.Tx) error {
		items, err := ListRequestable(ctx, tx)
		if len(items) != 1 || items[0].ID != crm || items[0].Application != "CRM" {
			t.Errorf("requestable = %+v", items)
		}
		var builtin string
		if err := tx.QueryRow(ctx, `SELECT e.id FROM entitlements e JOIN applications a ON a.id = e.application_id WHERE a.connector = 'grantra' LIMIT 1`).Scan(&builtin); err != nil {
			return err
		}
		var inv *apperr.Invalid
		if _, err := Create(ctx, tx, e.tenant, ana, Input{EntitlementID: builtin, Justification: "please"}); !errors.As(err, &inv) {
			t.Errorf("request a platform role = %v", err)
		}
		return err
	})
}
