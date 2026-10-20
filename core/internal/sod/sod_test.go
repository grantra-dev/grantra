package sod

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
	"github.com/grantra-dev/grantra/core/internal/settings"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	app    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	e := &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "sod")}
	if err := authz.EnsureBuiltinApp(context.Background(), pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	e.tx(func(tx pgx.Tx) error {
		a, err := catalog.CreateApplication(context.Background(), tx, e.tenant, "ERP", "")
		e.app = a.ID
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

func (e *env) entitlement(native string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		en, err := catalog.CreateEntitlement(context.Background(), tx, e.tenant, e.app, catalog.EntitlementInput{NativeID: native, Name: native})
		id = en.ID
		return err
	})
	return id
}

func (e *env) identity(name string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name) VALUES ($1, $2) RETURNING id`, e.tenant, name).Scan(&id)
	})
	return id
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), sql, args...)
		return err
	})
}

func (e *env) assign(identity, entitlement string) {
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
		e.tenant, identity, entitlement)
}

func (e *env) scan() ScanResult {
	e.t.Helper()
	res, err := Scan(context.Background(), e.pool, e.tenant)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func (e *env) check(identity, role, entitlement string) error {
	e.t.Helper()
	var err error
	e.tx(func(tx pgx.Tx) error {
		err = Check(context.Background(), tx, identity, role, entitlement)
		return nil
	})
	return err
}

func TestSoD(t *testing.T) {
	e := newEnv(t)
	create, approve, report := e.entitlement("create-payments"), e.entitlement("approve-payments"), e.entitlement("reports")
	ctx := context.Background()

	// A role holding the approving side, so roles are expanded.
	var approver string
	e.tx(func(tx pgx.Tx) error {
		r, err := catalog.CreateRole(ctx, tx, e.tenant, catalog.RoleInput{Name: "Approver", EntitlementIDs: []string{approve}})
		approver = r.ID
		return err
	})

	var inv *apperr.Invalid
	e.tx(func(tx pgx.Tx) error {
		for _, bad := range []Input{
			{Name: "x", SideA: []Ref{{EntitlementID: create}}},
			{Name: "x", SideA: []Ref{{EntitlementID: create}}, SideB: []Ref{{EntitlementID: create}}},
			{Name: "x", SideA: []Ref{{EntitlementID: create}}, SideB: []Ref{{RoleID: approver, EntitlementID: approve}}},
			{Name: "x", Mode: "block", SideA: []Ref{{EntitlementID: create}}, SideB: []Ref{{EntitlementID: approve}}},
		} {
			if _, err := Save(ctx, tx, e.tenant, "", bad); !errors.As(err, &inv) {
				t.Errorf("bad policy %+v = %v", bad, err)
			}
		}
		return nil
	})
	var p Policy
	e.tx(func(tx pgx.Tx) (err error) {
		p, err = Save(ctx, tx, e.tenant, "", Input{Name: "Payments", Mode: Prevent,
			SideA: []Ref{{EntitlementID: create}}, SideB: []Ref{{EntitlementID: approve}}})
		return err
	})
	if p.Severity != "high" || len(p.SideA) != 1 || p.SideB[0].Name != "ERP: approve-payments" {
		t.Errorf("policy = %+v", p)
	}

	ana, luis, eva := e.identity("Ana"), e.identity("Luis"), e.identity("Eva")
	e.assign(ana, create)
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, role_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`, e.tenant, ana, approver)
	e.assign(luis, create)
	e.assign(luis, report)
	// Eva approves through an account the application gave her directly.
	e.assign(eva, create)
	e.exec(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id) VALUES ($1, $2, $3, 'eva')`, e.tenant, e.app, eva)
	e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id)
	        SELECT $1, id, $2 FROM accounts WHERE native_id = 'eva'`, e.tenant, approve)

	res := e.scan()
	if res.Detected != 2 || res.Open != 2 {
		t.Fatalf("first scan = %+v", res)
	}
	if res := e.scan(); res.Detected != 0 || res.Resolved != 0 || res.Open != 2 {
		t.Errorf("second scan = %+v", res)
	}

	// Prevention: Luis cannot get the approving side, by entitlement or role.
	var conflict *apperr.Conflict
	if err := e.check(luis, "", approve); !errors.As(err, &conflict) {
		t.Errorf("check entitlement = %v", err)
	}
	if err := e.check(luis, approver, ""); !errors.As(err, &conflict) {
		t.Errorf("check role = %v", err)
	}
	if err := e.check(luis, "", report); err != nil {
		t.Errorf("unrelated access = %v", err)
	}

	// An exception for Eva; nobody excepts their own.
	var vs []Violation
	e.tx(func(tx pgx.Tx) (err error) {
		vs, err = Violations(ctx, tx, Filter{Status: Open, Limit: 10})
		return err
	})
	var evaV Violation
	for _, v := range vs {
		if v.IdentityID == eva {
			evaV = v
		}
	}
	err := db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := Except(ctx, tx, e.tenant, "identity:"+eva, evaV.ID, "I said so", 0)
		return err
	})
	if !errors.As(err, &conflict) {
		t.Errorf("self exception = %v", err)
	}
	err = db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := Except(ctx, tx, e.tenant, "identity:admin", evaV.ID, "", 0)
		return err
	})
	if !errors.As(err, &inv) {
		t.Errorf("exception without reason = %v", err)
	}
	// The tenant allows exceptions of up to 30 days, and none for good.
	e.exec(`INSERT INTO tenant_settings (tenant_id, key, value, updated_by) VALUES ($1, $2, 30, 'test'), ($1, $3, 0, 'test')`,
		e.tenant, settings.SoDExceptionMaxDays, settings.SoDExceptionsWithoutEnd)
	for _, days := range []int{0, 31} {
		err = db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
			_, err := Except(ctx, tx, e.tenant, "identity:admin", evaV.ID, "small team; weekly audit", days)
			return err
		})
		if !errors.As(err, &inv) {
			t.Errorf("exception for %d days = %v, want refused by the tenant's settings", days, err)
		}
	}
	e.tx(func(tx pgx.Tx) error {
		v, err := Except(ctx, tx, e.tenant, "identity:admin", evaV.ID, "small team; weekly audit", 30)
		if v.Status != Excepted || v.ExceptionUntil == nil {
			t.Errorf("excepted = %+v", v)
		}
		return err
	})
	if res := e.scan(); res.Open != 1 {
		t.Errorf("open after exception = %+v", res)
	}

	// Ana loses the role: resolved. An expired exception reopens.
	e.exec(`DELETE FROM access_assignments WHERE identity_id = $1 AND role_id IS NOT NULL`, ana)
	e.exec(`UPDATE sod_violations SET exception_until = now() - interval '1 second' WHERE id = $1`, evaV.ID)
	res = e.scan()
	if res.Resolved != 1 || res.ExceptionsExpired != 1 || res.Open != 1 {
		t.Errorf("after changes = %+v", res)
	}

	// Disabled policies find nothing; detect-only policies do not prevent.
	e.tx(func(tx pgx.Tx) error {
		off := false
		_, err := Save(ctx, tx, e.tenant, p.ID, Input{Name: "Payments", Enabled: &off,
			SideA: []Ref{{EntitlementID: create}}, SideB: []Ref{{EntitlementID: approve}}})
		return err
	})
	if res := e.scan(); res.Resolved != 1 || res.Open != 0 {
		t.Errorf("disabled policy scan = %+v", res)
	}
	if err := e.check(luis, "", approve); err != nil {
		t.Errorf("disabled policy prevents = %v", err)
	}
}
