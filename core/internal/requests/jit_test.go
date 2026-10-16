package requests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/sod"
)

func (e *env) setJIT(ent string, j catalog.JIT) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := catalog.SetJIT(context.Background(), tx, e.tenant, "identity:admin", ent, j)
		return err
	})
}

func (e *env) eligible(identity string, t access.Target, expires *time.Time) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) (err error) {
		id, err = access.MakeEligible(context.Background(), tx, e.tenant, "identity:admin", identity, t, expires, "")
		return err
	})
	return id
}

func (e *env) activate(identity, eligibility string, hours int, why string) (ActivationResult, error) {
	e.t.Helper()
	var (
		res ActivationResult
		err error
	)
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		res, err = Activate(context.Background(), tx, e.tenant, identity, eligibility, hours, why)
		return err
	})
	return res, err
}

func (e *env) eligibility(id string) access.Eligibility {
	e.t.Helper()
	var el access.Eligibility
	e.tx(func(tx pgx.Tx) (err error) {
		el, err = access.GetEligibility(context.Background(), tx, id, false)
		return err
	})
	return el
}

func (e *env) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	e.tx(func(tx pgx.Tx) error { return tx.QueryRow(context.Background(), q, args...).Scan(&n) })
	return n
}

func (e *env) exec(q string, args ...any) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), q, args...)
		return err
	})
}

func TestActivateWithoutApproval(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	luis := e.identity("Luis", boss)
	ent := e.entitlement("ERP", "")
	e.setJIT(ent, catalog.JIT{MaxActivationHours: 2})
	el := e.eligible(ana, access.Target{EntitlementID: ent}, nil)

	var inv *apperr.Invalid
	var conflict *apperr.Conflict
	if _, err := e.activate(ana, el, 3, "month end"); !errors.As(err, &inv) {
		t.Errorf("3 hours over a max of 2 = %v", err)
	}
	if _, err := e.activate(ana, el, 1, "  "); !errors.As(err, &inv) {
		t.Errorf("no justification = %v", err)
	}
	if _, err := e.activate(luis, el, 1, "not mine"); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("someone else's eligibility = %v", err)
	}
	if e.count(`SELECT count(*) FROM jit_provisioning_queue`) != 0 {
		t.Fatal("queue not empty before activating")
	}

	res, err := e.activate(ana, el, 2, " month end ")
	if err != nil || res.Activation == nil || res.Request != nil {
		t.Fatalf("activate = %+v, %v", res, err)
	}
	if d := time.Until(res.Activation.ExpiresAt); d < 119*time.Minute || d > 121*time.Minute {
		t.Errorf("activation lasts %v, want 2 hours", d)
	}
	if e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1 AND origin = 'activation'
	            AND eligibility_id = $2 AND justification = 'month end' AND expires_at IS NOT NULL`, ana, el) != 1 {
		t.Error("no activation assignment")
	}
	if e.count(`SELECT count(*) FROM jit_provisioning_queue q JOIN entitlements e ON e.application_id = q.application_id WHERE e.id = $1`, ent) != 1 {
		t.Error("the application is not queued for provisioning")
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'access.activated' AND target_id = $1`, ana) != 1 {
		t.Error("no access.activated audit event")
	}
	if got := e.eligibility(el); got.Active == nil || got.Active.Justification != "month end" {
		t.Errorf("eligibility = %+v, want it active", got)
	}
	if _, err := e.activate(ana, el, 1, "again"); !errors.As(err, &conflict) {
		t.Errorf("activating twice = %v", err)
	}

	// Deactivating ends it at once.
	e.exec(`DELETE FROM jit_provisioning_queue`)
	e.tx(func(tx pgx.Tx) error {
		got, err := access.GetEligibility(context.Background(), tx, el, true)
		if err != nil {
			return err
		}
		return access.Deactivate(context.Background(), tx, e.tenant, "identity:"+ana, got)
	})
	if e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1`, ana) != 0 {
		t.Error("deactivating left the access")
	}
	if e.count(`SELECT count(*) FROM jit_provisioning_queue`) != 1 {
		t.Error("deactivating did not queue provisioning")
	}

	// It runs out on its own.
	if _, err := e.activate(ana, el, 1, "again"); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE access_assignments SET expires_at = now() - interval '1 second' WHERE identity_id = $1`, ana)
	// The hourly request housekeeping leaves activations to EndActivations.
	if _, err := ExpireDue(context.Background(), e.pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1`, ana) != 1 {
		t.Error("ExpireDue removed an activation; EndActivations should, with provisioning")
	}
	e.exec(`DELETE FROM jit_provisioning_queue`)
	var h access.JITHousekeeping
	e.tx(func(tx pgx.Tx) (err error) {
		h, err = access.EndActivations(context.Background(), tx, e.tenant)
		return err
	})
	if h.ActivationsEnded != 1 || e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1`, ana) != 0 {
		t.Errorf("end activations = %+v", h)
	}
	if e.count(`SELECT count(*) FROM jit_provisioning_queue`) != 1 {
		t.Error("an ended activation is not queued for provisioning")
	}
	if e.count(`SELECT count(*) FROM audit_events WHERE action = 'access.removed' AND target_id = $1 AND details->>'reason' = 'expired'`, ana) != 1 {
		t.Error("no audit event for the expired activation")
	}

	// An eligibility that ends takes its activation with it.
	if _, err := e.activate(ana, el, 1, "last time"); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE access_eligibilities SET expires_at = now() - interval '1 second' WHERE id = $1`, el)
	e.tx(func(tx pgx.Tx) (err error) {
		h, err = access.EndActivations(context.Background(), tx, e.tenant)
		return err
	})
	if h.EligibilitiesEnded != 1 || e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1`, ana) != 0 ||
		e.count(`SELECT count(*) FROM access_eligibilities WHERE identity_id = $1`, ana) != 0 {
		t.Errorf("ended eligibility = %+v", h)
	}
}

func TestActivateWithApproval(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	owner := e.identity("Owner", "")
	ent := e.entitlement("ERP", owner)
	e.setJIT(ent, catalog.JIT{MaxActivationHours: 4, ActivationApproval: true})
	el := e.eligible(ana, access.Target{EntitlementID: ent}, nil)

	res, err := e.activate(ana, el, 3, "close the books")
	if err != nil || res.Activation != nil || res.Request == nil {
		t.Fatalf("activate = %+v, %v", res, err)
	}
	r := *res.Request
	if r.Kind != KindActivation || r.DurationHours == nil || *r.DurationHours != 3 || r.Justification != "close the books" {
		t.Errorf("request = %+v", r)
	}
	if !equal(kinds(r), []string{"manager:pending", "owner:waiting"}) {
		t.Errorf("steps = %v", kinds(r))
	}
	var conflict *apperr.Conflict
	if _, err := e.activate(ana, el, 1, "again"); !errors.As(err, &conflict) {
		t.Errorf("a second activation while one waits = %v", err)
	}
	e.decide(boss, r.ID, true)
	r = e.decide(owner, r.ID, true)
	if r.Status != Approved {
		t.Fatalf("status = %s", r.Status)
	}
	got := e.eligibility(el)
	if got.Active == nil || time.Until(got.Active.ExpiresAt) < 179*time.Minute || time.Until(got.Active.ExpiresAt) > 181*time.Minute {
		t.Fatalf("eligibility after approval = %+v", got)
	}
	if e.count(`SELECT count(*) FROM access_assignments WHERE request_id = $1 AND origin = 'activation'`, r.ID) != 1 {
		t.Error("the activation does not point to its request")
	}

	// Removing the eligibility while a request waits cancels it when
	// approved.
	e.exec(`DELETE FROM access_assignments WHERE identity_id = $1`, ana)
	res, err = e.activate(ana, el, 1, "once more")
	if err != nil || res.Request == nil {
		t.Fatal(res, err)
	}
	e.tx(func(tx pgx.Tx) error {
		return access.RemoveEligibility(context.Background(), tx, e.tenant, "identity:admin", el)
	})
	e.decide(boss, res.Request.ID, true)
	r = e.decide(owner, res.Request.ID, true)
	if r.Status != Cancelled || e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1`, ana) != 0 {
		t.Errorf("approving after the eligibility went = %s %q", r.Status, r.Reason)
	}
}

func TestActivationBreakingSoD(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	a := e.entitlement("Vendors", "")
	b := e.entitlement("Payments", "")
	e.tx(func(tx pgx.Tx) error {
		_, err := sod.Save(context.Background(), tx, e.tenant, "", sod.Input{
			Name: "Pay yourself", Mode: sod.Prevent, SideA: []sod.Ref{{EntitlementID: a}}, SideB: []sod.Ref{{EntitlementID: b}},
		})
		return err
	})
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
		e.tenant, ana, a)
	el := e.eligible(ana, access.Target{EntitlementID: b}, nil)
	res, err := e.activate(ana, el, 1, "pay a vendor")
	if err != nil || res.Request == nil {
		t.Fatalf("activation breaking SoD = %+v, %v; want a request with a risk step", res, err)
	}
	if !equal(kinds(*res.Request), []string{"manager:pending", "admin:waiting", "risk:waiting"}) {
		t.Errorf("steps = %v", kinds(*res.Request))
	}
}

func TestJITOnly(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	ent := e.entitlement("Cloud", boss)
	e.setJIT(ent, catalog.JIT{JITOnly: true, MaxActivationHours: 8})

	var inv *apperr.Invalid
	if _, err := e.create(ana, Input{EntitlementID: ent, Justification: "deploy"}); !errors.As(err, &inv) {
		t.Errorf("standing request for just-in-time only access = %v", err)
	}
	hours := 6
	r, err := e.create(ana, Input{EntitlementID: ent, Justification: "deploy", DurationHours: &hours})
	if err != nil {
		t.Fatal(err)
	}
	if r.Kind != KindAccess || r.DurationHours == nil || *r.DurationHours != 6 {
		t.Errorf("request = %+v", r)
	}
	r = e.decide(boss, r.ID, true)
	var expires time.Time
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT expires_at FROM access_assignments WHERE request_id = $1`, r.ID).Scan(&expires)
	})
	if d := time.Until(expires); d < 359*time.Minute || d > 361*time.Minute {
		t.Errorf("access lasts %v, want 6 hours", d)
	}

	// Asking to be eligible needs no duration, and gives no access.
	e.exec(`DELETE FROM access_assignments WHERE identity_id = $1`, ana)
	days := 30
	if _, err := e.create(ana, Input{EntitlementID: ent, Kind: KindEligibility, Justification: "on call", DurationHours: &hours}); !errors.As(err, &inv) {
		t.Errorf("eligibility in hours = %v", err)
	}
	r, err = e.create(ana, Input{EntitlementID: ent, Kind: KindEligibility, Justification: "on call", DurationDays: &days})
	if err != nil {
		t.Fatal(err)
	}
	r = e.decide(boss, r.ID, true)
	if r.Status != Approved || r.Kind != KindEligibility {
		t.Fatalf("eligibility request = %+v", r)
	}
	if e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1`, ana) != 0 {
		t.Error("an eligibility request gave access")
	}
	var el access.Eligibility
	e.tx(func(tx pgx.Tx) (err error) {
		el, err = access.FindEligibility(context.Background(), tx, ana, access.Target{EntitlementID: ent})
		return err
	})
	if el.RequestID != r.ID || el.ExpiresAt == nil || time.Until(*el.ExpiresAt) < 29*24*time.Hour || el.MaxActivationHours != 8 {
		t.Errorf("eligibility = %+v", el)
	}
	var conflict *apperr.Conflict
	if _, err := e.create(ana, Input{EntitlementID: ent, Kind: KindEligibility, Justification: "again"}); !errors.As(err, &conflict) {
		t.Errorf("asking again while eligible = %v", err)
	}
	if _, err := e.create(ana, Input{EntitlementID: ent, Kind: KindActivation, Justification: "sneaky", DurationHours: &hours}); !errors.As(err, &inv) {
		t.Errorf("activation through Create = %v", err)
	}

	// Birthright rules cannot give it, directly or through a role.
	role := ""
	e.tx(func(tx pgx.Tx) error {
		ro, err := catalog.CreateRole(context.Background(), tx, e.tenant, catalog.RoleInput{Name: "Ops", EntitlementIDs: []string{ent}})
		role = ro.ID
		return err
	})
	for _, in := range []access.RuleInput{{Name: "Everyone", EntitlementIDs: []string{ent}}, {Name: "Ops", RoleIDs: []string{role}}} {
		err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
			_, err := access.CreateRule(context.Background(), tx, e.tenant, in)
			return err
		})
		if !errors.As(err, &inv) {
			t.Errorf("rule %s = %v", in.Name, err)
		}
	}
	// Nor can anything write standing access to it: the database refuses.
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO access_assignments (tenant_id, identity_id, role_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
			e.tenant, ana, role)
		return err
	})
	if err == nil {
		t.Error("the database took standing access to a role with just-in-time only access")
	}

	// An entitlement a rule gives cannot become just-in-time only.
	other := e.entitlement("Wiki", "")
	e.tx(func(tx pgx.Tx) error {
		_, err := access.CreateRule(context.Background(), tx, e.tenant, access.RuleInput{Name: "Wiki", EntitlementIDs: []string{other}})
		return err
	})
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := catalog.SetJIT(context.Background(), tx, e.tenant, "identity:admin", other, catalog.JIT{JITOnly: true})
		return err
	})
	if !errors.As(err, &conflict) {
		t.Errorf("just-in-time only while a rule gives it = %v", err)
	}
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := catalog.SetJIT(context.Background(), tx, e.tenant, "identity:admin", other, catalog.JIT{MaxActivationHours: 25})
		return err
	})
	if !errors.As(err, &inv) {
		t.Errorf("25 hours = %v", err)
	}
}

func TestStandingAccess(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	low := e.entitlement("Wiki", "")
	high := e.entitlement("Prod", "")
	e.exec(`UPDATE entitlements SET risk = 'critical' WHERE id = $1`, high)
	role := ""
	e.tx(func(tx pgx.Tx) error {
		ro, err := catalog.CreateRole(context.Background(), tx, e.tenant, catalog.RoleInput{Name: "SRE", EntitlementIDs: []string{high, low}})
		role = ro.ID
		return err
	})
	assign := func(col, target, origin string, expires any) string {
		var id string
		e.tx(func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(),
				`INSERT INTO access_assignments (tenant_id, identity_id, `+col+`, origin, granted_by, expires_at)
				 VALUES ($1, $2, $3, $4, 'test', $5) RETURNING id`, e.tenant, ana, target, origin, expires).Scan(&id)
		})
		return id
	}
	assign("entitlement_id", low, "manual", nil)
	direct := assign("entitlement_id", high, "manual", nil)
	viaRole := assign("role_id", role, "request", nil)
	assign("entitlement_id", high, "request", time.Now().Add(time.Hour))

	var list []access.Standing
	e.tx(func(tx pgx.Tx) (err error) {
		list, err = access.StandingAccess(context.Background(), tx, 50)
		return err
	})
	if len(list) != 2 {
		t.Fatalf("standing = %+v, want the critical entitlement and the role, not the low risk or ending access", list)
	}
	for _, s := range list {
		if s.Risk != "critical" || !s.Convertible || (s.AssignmentID != direct && s.AssignmentID != viaRole) {
			t.Errorf("standing = %+v", s)
		}
	}

	var id string
	e.tx(func(tx pgx.Tx) (err error) {
		id, err = access.ConvertToEligibility(context.Background(), tx, e.tenant, "identity:admin", viaRole)
		return err
	})
	el := e.eligibility(id)
	if el.Type != "role" || el.TargetID != role || el.ExpiresAt != nil || el.Active != nil {
		t.Errorf("converted = %+v", el)
	}
	if e.count(`SELECT count(*) FROM access_assignments WHERE id = $1`, viaRole) != 0 {
		t.Error("the standing access stayed")
	}
	var conflict *apperr.Conflict
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := access.ConvertToEligibility(context.Background(), tx, e.tenant, "identity:admin", viaRole)
		return err
	})
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("converting twice = %v", err)
	}
	birthright := ""
	e.tx(func(tx pgx.Tx) error {
		r, err := access.CreateRule(context.Background(), tx, e.tenant, access.RuleInput{Name: "All", EntitlementIDs: []string{high}})
		if err != nil {
			return err
		}
		return tx.QueryRow(context.Background(),
			`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, rule_id, granted_by)
			 VALUES ($1, $2, $3, 'birthright', $4, 'rule') RETURNING id`, e.tenant, boss, high, r.ID).Scan(&birthright)
	})
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := access.ConvertToEligibility(context.Background(), tx, e.tenant, "identity:admin", birthright)
		return err
	})
	if !errors.As(err, &conflict) {
		t.Errorf("converting birthright access = %v", err)
	}
}

// Requests made before the rules changed close cleanly when approved.
func TestStaleRequests(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	owner := e.identity("Owner", "")
	ent := e.entitlement("ERP", owner)

	// Asked for with no end date, then the access became just in time only.
	r, err := e.create(ana, Input{EntitlementID: ent, Justification: "month end"})
	if err != nil {
		t.Fatal(err)
	}
	e.setJIT(ent, catalog.JIT{JITOnly: true, MaxActivationHours: 4, ActivationApproval: true})
	e.decide(boss, r.ID, true)
	r = e.decide(owner, r.ID, true)
	if r.Status != Cancelled || e.assignments(ana) != 0 {
		t.Errorf("standing request after just-in-time = %s %q", r.Status, r.Reason)
	}

	// An activation asked for 4 hours, then the maximum went down to 2.
	el := e.eligible(ana, access.Target{EntitlementID: ent}, nil)
	res, err := e.activate(ana, el, 4, "deploy")
	if err != nil || res.Request == nil {
		t.Fatal(res, err)
	}
	e.setJIT(ent, catalog.JIT{JITOnly: true, MaxActivationHours: 2, ActivationApproval: true})
	e.decide(boss, res.Request.ID, true)
	if r := e.decide(owner, res.Request.ID, true); r.Status != Approved {
		t.Fatalf("activation after the maximum went down = %s %q", r.Status, r.Reason)
	}
	if got := e.eligibility(el); got.Active == nil || time.Until(got.Active.ExpiresAt) > 121*time.Minute {
		t.Errorf("activation = %+v, want at most 2 hours", got.Active)
	}
}
