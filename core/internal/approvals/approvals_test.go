package approvals

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
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
	return &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "approvals")}
}

func (e *env) tx(fn func(context.Context, pgx.Tx) error) error {
	return db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error { return fn(context.Background(), tx) })
}

func (e *env) must(fn func(context.Context, pgx.Tx) error) {
	e.t.Helper()
	if err := e.tx(fn); err != nil {
		e.t.Fatal(err)
	}
}

// person adds an active person with a manager and a department.
func (e *env) person(name, manager, department string) string {
	e.t.Helper()
	var id string
	e.must(func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO identities (tenant_id, display_name, email, manager_id, attributes)
			 VALUES ($1, $2, $2 || '@example.com', nullif($3, '')::uuid, jsonb_build_object('department', $4::text)) RETURNING id`,
			e.tenant, name, manager, department).Scan(&id)
	})
	return id
}

// app adds an application owned by owner with an entitlement of risk.
func (e *env) app(name, owner, risk string) (app, ent string) {
	e.t.Helper()
	e.must(func(ctx context.Context, tx pgx.Tx) error {
		a, err := catalog.CreateApplication(ctx, tx, e.tenant, name, owner)
		if err != nil {
			return err
		}
		en, err := catalog.CreateEntitlement(ctx, tx, e.tenant, a.ID, catalog.EntitlementInput{NativeID: "admin", Name: name + " admin", Risk: risk})
		app, ent = a.ID, en.ID
		return err
	})
	return app, ent
}

func (e *env) save(id string, in Input) (Policy, error) {
	var p Policy
	err := e.tx(func(ctx context.Context, tx pgx.Tx) (err error) {
		p, err = Save(ctx, tx, e.tenant, "identity:admin", id, in)
		return err
	})
	return p, err
}

func (e *env) resolve(r Request) Plan {
	e.t.Helper()
	var p Plan
	e.must(func(ctx context.Context, tx pgx.Tx) (err error) {
		p, err = Resolve(ctx, tx, r)
		return err
	})
	return p
}

// names writes a plan as "kind:approver,approver/required" per step.
func names(p Plan) string {
	var steps []string
	for _, s := range p.Steps {
		var who []string
		for _, a := range s.Approvers {
			who = append(who, a.Name)
		}
		step := s.Kind + ":" + strings.Join(who, ",")
		if s.Required > 1 {
			step += "/" + string(rune('0'+s.Required))
		}
		if s.RequesterApproves {
			step += "*"
		}
		steps = append(steps, step)
	}
	return strings.Join(steps, " ")
}

func TestSaveChecks(t *testing.T) {
	e := newEnv(t)
	ana := e.person("Ana", "", "")
	app, _ := e.app("Payroll", "", "high")
	for _, tc := range []struct {
		name string
		in   Input
	}{
		{"no name", Input{Steps: []Step{{Approver: Manager}}}},
		{"unknown kind", Input{Name: "x", Conditions: Conditions{Kinds: []string{"party"}}, Steps: []Step{{Approver: Manager}}}},
		{"unknown risk", Input{Name: "x", Conditions: Conditions{Risks: []string{"spicy"}}, Steps: []Step{{Approver: Manager}}}},
		{"unknown application", Input{Name: "x", Conditions: Conditions{ApplicationIDs: []string{ana}}, Steps: []Step{{Approver: Manager}}}},
		{"no steps for any risk", Input{Name: "x"}},
		{"no steps for medium risk", Input{Name: "x", Conditions: Conditions{Risks: []string{"low", "medium"}}}},
		{"unknown approver", Input{Name: "x", Steps: []Step{{Approver: "boss"}}}},
		{"manager too far up", Input{Name: "x", Steps: []Step{{Approver: Manager, Level: 6}}}},
		{"nobody named", Input{Name: "x", Steps: []Step{{Approver: People}}}},
		{"more required than named", Input{Name: "x", Steps: []Step{{Approver: People, PeopleIDs: []string{ana}, Required: 2}}}},
		{"all and a number", Input{Name: "x", Steps: []Step{{Approver: People, PeopleIDs: []string{ana}, All: true, Required: 2}}}},
		{"holders of no role", Input{Name: "x", Steps: []Step{{Approver: Holders, RoleID: app}}}},
		{"long timeout", Input{Name: "x", Steps: []Step{{Approver: Owner, TimeoutHours: 721}}}},
		{"escalate the admins", Input{Name: "x", Steps: []Step{{Approver: Admin, OnTimeout: Escalate}}}},
		{"unknown timeout", Input{Name: "x", Steps: []Step{{Approver: Owner, OnTimeout: "shrug"}}}},
	} {
		var bad *apperr.Invalid
		if _, err := e.save("", tc.in); !errors.As(err, &bad) {
			t.Errorf("%s: err = %v", tc.name, err)
		}
	}

	// Only what each approver uses is kept, and defaults are filled in.
	p, err := e.save("", Input{Name: " Payroll ", Conditions: Conditions{ApplicationIDs: []string{app, app}, Departments: []string{" Finance ", ""}},
		Steps: []Step{{Approver: Manager, PeopleIDs: []string{ana}, Required: 3}, {Approver: People, PeopleIDs: []string{ana}, Level: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Payroll" || p.Version != 1 || p.Position != 1 || !p.Enabled || len(p.Conditions.ApplicationIDs) != 1 ||
		len(p.Conditions.Departments) != 1 || p.Conditions.Departments[0] != "Finance" {
		t.Errorf("policy = %+v", p)
	}
	if s := p.Steps[0]; s.Level != 1 || s.PeopleIDs != nil || s.Required != 0 || s.OnTimeout != Expire {
		t.Errorf("manager step = %+v", s)
	}
	if s := p.Steps[1]; s.Level != 0 || len(s.People) != 1 || s.People[0].Name != "Ana" {
		t.Errorf("people step = %+v", s)
	}
	var conflict *apperr.Conflict
	if _, err := e.save("", Input{Name: "payroll", Steps: []Step{{Approver: Owner}}}); err != nil {
		t.Errorf("names differ in case: %v", err)
	}
	if _, err := e.save("", Input{Name: "Payroll", Steps: []Step{{Approver: Owner}}}); !errors.As(err, &conflict) {
		t.Errorf("same name = %v", err)
	}
	p, err = e.save(p.ID, Input{Name: "Payroll", Steps: []Step{{Approver: Owner}}})
	if err != nil || p.Version != 2 || len(p.Steps) != 1 {
		t.Errorf("update = %+v %v", p, err)
	}
	if _, err := e.save(ana, Input{Name: "Nope", Steps: []Step{{Approver: Owner}}}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("update of no policy = %v", err)
	}
	// Low risk access may be approved with no step.
	if _, err := e.save("", Input{Name: "Self-service", Conditions: Conditions{Risks: []string{"low"}}}); err != nil {
		t.Errorf("no steps for low risk: %v", err)
	}
}

func TestOrder(t *testing.T) {
	e := newEnv(t)
	var ids []string
	for _, name := range []string{"A", "B", "C"} {
		p, err := e.save("", Input{Name: name, Steps: []Step{{Approver: Owner}}})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, p.ID)
	}
	var bad *apperr.Invalid
	if err := e.tx(func(ctx context.Context, tx pgx.Tx) error {
		return Reorder(ctx, tx, e.tenant, "identity:admin", ids[:2])
	}); !errors.As(err, &bad) {
		t.Errorf("reorder without one = %v", err)
	}
	e.must(func(ctx context.Context, tx pgx.Tx) error {
		if err := Reorder(ctx, tx, e.tenant, "identity:admin", []string{ids[2], ids[0], ids[1]}); err != nil {
			return err
		}
		if err := Delete(ctx, tx, e.tenant, "identity:admin", ids[0]); err != nil {
			return err
		}
		list, err := List(ctx, tx)
		if err != nil {
			return err
		}
		if len(list) != 2 || list[0].Name != "C" || list[1].Name != "B" {
			t.Errorf("order = %+v", list)
		}
		return nil
	})
}

func TestResolve(t *testing.T) {
	e := newEnv(t)
	cfo := e.person("Carla CFO", "", "Finance")
	boss := e.person("Boss", cfo, "Finance")
	ana := e.person("Ana", boss, "Finance")
	ivan := e.person("Ivan", boss, "Finance")
	owner := e.person("Olga Owner", "", "IT")
	sec1, sec2 := e.person("Sec One", "", "IT"), e.person("Sec Two", "", "IT")
	payroll, payrollAdmin := e.app("Payroll", owner, "critical")
	_, wiki := e.app("Wiki", "", "low")
	_, crm := e.app("CRM", owner, "medium")
	var security string
	e.must(func(ctx context.Context, tx pgx.Tx) error {
		r, err := catalog.CreateRole(ctx, tx, e.tenant, catalog.RoleInput{Name: "IT security"})
		security = r.ID
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO access_assignments (tenant_id, identity_id, role_id, origin, granted_by)
		                       SELECT $1, unnest($2::uuid[]), $3, 'manual', 'test'`, e.tenant, []string{sec1, sec2}, security)
		return err
	})
	ask := func(requester, beneficiary, ent string) Request {
		return Request{Kind: "access", EntitlementID: ent, RequesterID: requester, BeneficiaryID: beneficiary}
	}

	// With no policy, the built-in flow: manager, then owner, and the
	// admins for an application nobody owns.
	if got := names(e.resolve(ask(ana, ana, crm))); got != "manager:Boss owner:Olga Owner" {
		t.Errorf("built in = %q", got)
	}
	if got := names(e.resolve(ask(ana, ana, wiki))); got != "manager:Boss admin:" {
		t.Errorf("no owner = %q", got)
	}
	// A manager asking for a report approves by asking.
	if got := names(e.resolve(ask(boss, ana, crm))); got != "manager:Boss* owner:Olga Owner" {
		t.Errorf("manager asking = %q", got)
	}

	if _, err := e.save("", Input{Name: "Payroll", Conditions: Conditions{ApplicationIDs: []string{payroll}},
		Steps: []Step{
			{Approver: Manager, Level: 2, Label: "Finance director"},
			{Approver: Holders, RoleID: security, All: true, OnTimeout: Escalate},
			{Approver: People, PeopleIDs: []string{ana, ivan, cfo}, Required: 2},
		}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.save("", Input{Name: "Low risk", Conditions: Conditions{Risks: []string{"low"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.save("", Input{Name: "Off", Enabled: new(bool), Steps: []Step{{Approver: Admin}}}); err != nil {
		t.Fatal(err)
	}
	p := e.resolve(ask(ana, ana, payrollAdmin))
	if p.PolicyName != "Payroll" || p.PolicyVersion != 1 || p.Risk != "critical" {
		t.Errorf("plan = %+v", p)
	}
	// Ana drops out of the people she is one of: two of the other two.
	if got := names(p); got != "manager:Carla CFO holders:Sec One,Sec Two/2 people:Carla CFO,Ivan/2" {
		t.Errorf("payroll = %q", got)
	}
	if p.Steps[0].Label != "Finance director" || p.Steps[1].OnTimeout != Escalate || p.Steps[2].Note == "" {
		t.Errorf("steps = %+v", p.Steps)
	}
	// The boss's manager's manager is nobody: an admin decides.
	if got := names(e.resolve(ask(boss, boss, payrollAdmin))); got != "admin: holders:Sec One,Sec Two/2 people:Ana,Carla CFO,Ivan/2" {
		t.Errorf("payroll for the boss = %q", got)
	}
	// Low risk access needs nobody; the disabled policy matches nothing.
	if got := names(e.resolve(ask(ana, ana, wiki))); got != "" {
		t.Errorf("low risk = %q", got)
	}
}

func TestConditions(t *testing.T) {
	days := func(n int) *int { return &n }
	c := Conditions{Kinds: []string{"access"}, Risks: []string{"high"}, Departments: []string{"finance"}, IdentityKinds: []string{"human"},
		ApplicationIDs: []string{"payroll"}, LongerThanDays: 30}
	f := facts{identityKind: "human", department: "Finance", risk: "high", applications: []string{"payroll", "erp"}}
	r := Request{Kind: "access", DurationDays: days(60)}
	if !c.matches(r, f) {
		t.Errorf("does not match")
	}
	for name, change := range map[string]func(*Request, *facts){
		"eligibility":   func(r *Request, _ *facts) { r.Kind = "eligibility" },
		"low risk":      func(_ *Request, f *facts) { f.risk = "low" },
		"sales":         func(_ *Request, f *facts) { f.department = "Sales" },
		"a service":     func(_ *Request, f *facts) { f.identityKind = "service" },
		"another app":   func(_ *Request, f *facts) { f.applications = []string{"wiki"} },
		"30 days":       func(r *Request, _ *facts) { r.DurationDays = days(30) },
		"a few hours":   func(r *Request, _ *facts) { r.DurationDays, r.DurationHours = nil, days(8) },
		"another role":  func(r *Request, _ *facts) { r.RoleID = "r2" },
		"no role asked": func(r *Request, _ *facts) {},
	} {
		r2, f2 := r, f
		change(&r2, &f2)
		c2 := c
		if strings.HasSuffix(name, "role") || name == "no role asked" {
			c2.RoleIDs = []string{"r1"}
		}
		if c2.matches(r2, f2) {
			t.Errorf("%s matches", name)
		}
	}
	// No end is longer than anything.
	if r.DurationDays = nil; !c.matches(r, f) {
		t.Errorf("access with no end does not match")
	}
}
