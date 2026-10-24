package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

func TestConditions(t *testing.T) {
	sales := map[string]string{"department": "Sales", "location": " Madrid "}
	guest := map[string]string{"department": "Sales", GuestAttribute: "auditor"}
	tests := []struct {
		conds Conditions
		attrs map[string]string
		kind  string
		want  bool
	}{
		{Conditions{}, sales, "human", true},
		{Conditions{"department": {"sales"}}, sales, "human", true},
		{Conditions{"department": {"Engineering", "Sales"}}, sales, "human", true},
		{Conditions{"department": {"Sales"}, "location": {"madrid"}}, sales, "human", true},
		{Conditions{"department": {"Sales"}, "location": {"Paris"}}, sales, "human", false},
		{Conditions{"title": {"CTO"}}, sales, "human", false},
		{Conditions{"kind": {"service"}}, sales, "human", false},
		{Conditions{"kind": {"human"}, "department": {"Sales"}}, sales, "human", true},
		{Conditions{}, sales, "service", false},
		{Conditions{"department": {"Sales"}}, sales, "agent", false},
		{Conditions{"kind": {"service"}, "department": {"Sales"}}, sales, "service", true},
		// Guests only match rules that name them.
		{Conditions{}, guest, "human", false},
		{Conditions{"department": {"Sales"}}, guest, "human", false},
		{Conditions{GuestAttribute: {"auditor"}}, guest, "human", true},
	}
	for _, tt := range tests {
		if got := tt.conds.Match(tt.attrs, tt.kind); got != tt.want {
			t.Errorf("%v.Match(%v) = %v, want %v", tt.conds, tt.attrs, got, tt.want)
		}
	}
	for _, bad := range []Conditions{
		{"": {"x"}},
		{" department": {"x"}},
		{"department": {}},
		{"department": {" "}},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	source string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	e := &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "access")}
	if err := authz.EnsureBuiltinApp(context.Background(), pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	e.tx(func(tx pgx.Tx) error {
		s, err := lifecycle.CreateSource(context.Background(), tx, e.tenant, "HR", lifecycle.KindCSV, lifecycle.Config{MaxLeaverPercent: 100}, nil)
		e.source = s.ID
		return err
	})
	return e
}

func (e *env) tx(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(context.Background(), e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) sync(feed string) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		res, err := lifecycle.Sync(context.Background(), tx, e.tenant, e.source, strings.NewReader(feed),
			lifecycle.Options{Actor: "identity:tester", Now: time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)})
		if err == nil && res.Status != lifecycle.RunApplied {
			e.t.Fatalf("sync = %s %v", res.Status, res.Errors)
		}
		return err
	})
	if _, _, err := ProcessLifecycle(context.Background(), e.pool, e.tenant); err != nil {
		e.t.Fatal(err)
	}
}

// access returns "origin:name" for each assignment of the employee.
func (e *env) access(ext string) []string {
	e.t.Helper()
	var out []string
	e.tx(func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(context.Background(), `SELECT id FROM identities WHERE external_id = $1`, ext).Scan(&id); err != nil {
			return err
		}
		as, err := IdentityAccess(context.Background(), tx, id)
		for _, a := range as {
			out = append(out, a.Origin+":"+a.Name)
		}
		return err
	})
	return out
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	e.tx(func(tx pgx.Tx) error { return tx.QueryRow(context.Background(), sql, args...).Scan(&n) })
	return n
}

const header = "employee_id,display_name,department,location,status\n"

func TestBirthrightFollowsLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// Catalog: a CRM with two entitlements, a "Sales rep" role bundling one.
	var crmUser, crmAdmin, salesRole, ruleID string
	e.tx(func(tx pgx.Tx) error {
		app, err := catalog.CreateApplication(ctx, tx, e.tenant, "CRM", "")
		if err != nil {
			return err
		}
		u, err := catalog.CreateEntitlement(ctx, tx, e.tenant, app.ID, catalog.EntitlementInput{Name: "CRM user"})
		if err != nil {
			return err
		}
		ad, err := catalog.CreateEntitlement(ctx, tx, e.tenant, app.ID, catalog.EntitlementInput{Name: "CRM admin", Risk: "high"})
		if err != nil {
			return err
		}
		role, err := catalog.CreateRole(ctx, tx, e.tenant, catalog.RoleInput{Name: "Sales rep", EntitlementIDs: []string{u.ID}})
		if err != nil {
			return err
		}
		crmUser, crmAdmin, salesRole = u.ID, ad.ID, role.ID
		return nil
	})

	e.sync(header +
		"E1,Ana,Sales,Madrid,\n" +
		"E2,Luis,Engineering,Madrid,\n")

	// A rule for Sales applies at once to people already there.
	e.tx(func(tx pgx.Tx) error {
		r, err := CreateRule(ctx, tx, e.tenant, RuleInput{Name: "Sales", Conditions: Conditions{"department": {"Sales"}}, RoleIDs: []string{salesRole}})
		if err != nil {
			return err
		}
		ruleID = r.ID
		ch, err := Reconcile(ctx, tx, e.tenant, "identity:tester", nil)
		if err != nil {
			return err
		}
		if ch.Added != 1 || ch.Removed != 0 {
			t.Errorf("reconcile after create = %+v", ch)
		}
		// Everyone in Madrid gets the CRM user entitlement too.
		if _, err := CreateRule(ctx, tx, e.tenant, RuleInput{Name: "Madrid", Conditions: Conditions{"location": {"Madrid"}}, EntitlementIDs: []string{crmUser}}); err != nil {
			return err
		}
		_, err = Reconcile(ctx, tx, e.tenant, "identity:tester", nil)
		return err
	})
	if got := strings.Join(e.access("E1"), ","); got != "birthright:CRM user,birthright:Sales rep" {
		t.Errorf("ana = %s", got)
	}
	if got := strings.Join(e.access("E2"), ","); got != "birthright:CRM user" {
		t.Errorf("luis = %s", got)
	}

	// A manual assignment survives moves but not leaving.
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by)
			 SELECT $1, id, $2, 'manual', 'identity:tester' FROM identities WHERE external_id = 'E2'`, e.tenant, crmAdmin)
		return err
	})

	// Ana is eligible for it too; leaving takes that away.
	e.tx(func(tx pgx.Tx) error {
		var ana string
		if err := tx.QueryRow(ctx, `SELECT id FROM identities WHERE external_id = 'E1'`).Scan(&ana); err != nil {
			return err
		}
		_, err := MakeEligible(ctx, tx, e.tenant, "identity:tester", ana, Target{EntitlementID: crmAdmin}, nil, "")
		return err
	})

	// Joiner, mover and leaver in one sync: Marta joins Sales, Luis moves to
	// Sales and Paris, Ana leaves.
	e.sync(header +
		"E1,Ana,Sales,Madrid,terminated\n" +
		"E2,Luis,Sales,Paris,\n" +
		"E3,Marta,Sales,Madrid,\n")
	if got := e.access("E1"); len(got) != 0 {
		t.Errorf("leaver keeps %v", got)
	}
	if got := strings.Join(e.access("E2"), ","); got != "manual:CRM admin,birthright:Sales rep" {
		t.Errorf("mover = %s", got)
	}
	if got := strings.Join(e.access("E3"), ","); got != "birthright:CRM user,birthright:Sales rep" {
		t.Errorf("joiner = %s", got)
	}
	if n := e.count(`SELECT count(*) FROM access_eligibilities`); n != 0 {
		t.Errorf("leaver keeps %d eligibilities", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'access.eligibility_removed' AND details->>'reason' = 'identity terminated'`); n != 1 {
		t.Errorf("eligibility removals audited = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM lifecycle_events WHERE processed_at IS NULL`); n != 0 {
		t.Errorf("%d lifecycle events left unprocessed", n)
	}

	// Disabling a rule takes its access away; deleting another does too.
	e.tx(func(tx pgx.Tx) error {
		off := false
		if _, err := UpdateRule(ctx, tx, e.tenant, ruleID, RuleInput{Name: "Sales", Conditions: Conditions{"department": {"Sales"}}, RoleIDs: []string{salesRole}, Enabled: &off}); err != nil {
			return err
		}
		ch, err := Reconcile(ctx, tx, e.tenant, "identity:tester", nil)
		if ch.Removed != 2 {
			t.Errorf("disable removed %d, want 2", ch.Removed)
		}
		return err
	})
	if got := strings.Join(e.access("E3"), ","); got != "birthright:CRM user" {
		t.Errorf("after disable = %s", got)
	}
	e.tx(func(tx pgx.Tx) error {
		rules, err := ListRules(ctx, tx)
		if err != nil {
			return err
		}
		for _, r := range rules {
			if r.Name == "Madrid" {
				if r.Holders != 1 {
					t.Errorf("Madrid holders = %d, want 1", r.Holders)
				}
				ch, err := DeleteRule(ctx, tx, e.tenant, "identity:tester", r.ID)
				if err != nil || ch.Removed != 1 {
					t.Errorf("delete = %+v, %v", ch, err)
				}
			}
		}
		return nil
	})
	if got := e.access("E3"); len(got) != 0 {
		t.Errorf("after delete = %v", got)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'access.removed' AND details->>'reason' = 'identity terminated'`); n != 2 {
		t.Errorf("leaver removals audited = %d, want 2", n)
	}
}

func TestRuleValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.tx(func(tx pgx.Tx) error {
		var platformRole string
		if err := tx.QueryRow(ctx, `SELECT e.id FROM entitlements e JOIN applications a ON a.id = e.application_id
			WHERE a.connector = 'grantra' AND e.native_id = 'grantra-admin'`).Scan(&platformRole); err != nil {
			return err
		}
		var inv *apperr.Invalid
		for name, in := range map[string]RuleInput{
			"no name":        {Conditions: Conditions{}, RoleIDs: []string{"x"}},
			"gives nothing":  {Name: "Empty"},
			"unknown role":   {Name: "R", RoleIDs: []string{"00000000-0000-0000-0000-000000000000"}},
			"platform role":  {Name: "Admins", EntitlementIDs: []string{platformRole}},
			"bad conditions": {Name: "C", Conditions: Conditions{"department": {}}, EntitlementIDs: []string{platformRole}},
		} {
			if _, err := CreateRule(ctx, tx, e.tenant, in); !errors.As(err, &inv) {
				t.Errorf("%s: err = %v, want invalid", name, err)
			}
		}
		if _, err := GetRule(ctx, tx, "nope"); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("get bad id = %v", err)
		}
		if _, err := DeleteRule(ctx, tx, e.tenant, "x", "00000000-0000-0000-0000-000000000000"); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("delete unknown = %v", err)
		}
		return nil
	})
}

// Reconciling sends its changes in batches; every change, in every batch,
// still gets its own audit event naming the assignment.
func TestReconcileInBatches(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var ent string
	e.tx(func(tx pgx.Tx) error {
		app, err := catalog.CreateApplication(ctx, tx, e.tenant, "Wiki", "")
		if err != nil {
			return err
		}
		en, err := catalog.CreateEntitlement(ctx, tx, e.tenant, app.ID, catalog.EntitlementInput{NativeID: "reader", Name: "Reader"})
		ent = en.ID
		return err
	})
	n := db.BatchSize + 5
	var feed strings.Builder
	feed.WriteString(header)
	for i := range n {
		fmt.Fprintf(&feed, "E%04d,Person %d,Sales,Madrid,active\n", i, i)
	}
	e.sync(feed.String())

	var ruleID string
	e.tx(func(tx pgx.Tx) error {
		r, err := CreateRule(ctx, tx, e.tenant, RuleInput{Name: "Madrid readers", Conditions: Conditions{"location": {"Madrid"}}, EntitlementIDs: []string{ent}})
		if err != nil {
			return err
		}
		ruleID = r.ID
		ch, err := Reconcile(ctx, tx, e.tenant, "identity:tester", nil)
		if err == nil && (ch.Added != n || ch.Removed != 0) {
			t.Errorf("reconcile = %+v, want %d added", ch, n)
		}
		return err
	})
	if got := e.count(`SELECT count(*) FROM access_assignments WHERE entitlement_id = $1`, ent); got != n {
		t.Errorf("assignments = %d, want %d", got, n)
	}
	// Each event names its own assignment, person, rule and origin.
	if got := e.count(`SELECT count(*) FROM audit_events ev JOIN access_assignments x ON x.id::text = ev.details->>'assignment_id'
		WHERE ev.action = 'access.assigned' AND ev.target_id = x.identity_id::text AND ev.actor = 'identity:tester'
		  AND ev.details->>'entitlement_id' = $1 AND ev.details->>'rule_id' = $2 AND ev.details->>'rule' = 'Madrid readers'
		  AND ev.details->>'origin' = 'birthright'`, ent, ruleID); got != n {
		t.Errorf("access.assigned events = %d, want %d", got, n)
	}
	// Nothing new: a second pass changes and audits nothing.
	e.tx(func(tx pgx.Tx) error {
		ch, err := Reconcile(ctx, tx, e.tenant, "identity:tester", nil)
		if err == nil && (ch.Added != 0 || ch.Removed != 0) {
			t.Errorf("second reconcile = %+v", ch)
		}
		return err
	})
	if got := e.count(`SELECT count(*) FROM audit_events WHERE action = 'access.assigned'`); got != n {
		t.Errorf("access.assigned events after a second pass = %d, want %d", got, n)
	}

	e.tx(func(tx pgx.Tx) error {
		ch, err := DeleteRule(ctx, tx, e.tenant, "identity:tester", ruleID)
		if err == nil && ch.Removed != n {
			t.Errorf("delete rule = %+v, want %d removed", ch, n)
		}
		return err
	})
	if got := e.count(`SELECT count(*) FROM access_assignments WHERE entitlement_id = $1`, ent); got != 0 {
		t.Errorf("assignments left = %d", got)
	}
	if got := e.count(`SELECT count(*) FROM audit_events WHERE action = 'access.removed' AND details->>'reason' = 'rule deleted'
		AND details->>'rule_id' = $1 AND details->>'entitlement_id' = $2 AND details ? 'assignment_id'`, ruleID, ent); got != n {
		t.Errorf("access.removed events = %d, want %d", got, n)
	}
}
