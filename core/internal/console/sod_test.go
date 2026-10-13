package console

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/sod"
)

func TestSeparationOfDuties(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "ERP"}`))
	ents := "/api/v1/applications/" + app.ID + "/entitlements"
	create := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, ents, `{"native_id": "create", "name": "Create payments"}`))
	approve := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, ents, `{"native_id": "approve", "name": "Approve payments"}`))

	body := `{"name": "Payments", "mode": "prevent", "side_a": [{"entitlement_id": "` + create.ID + `"}], "side_b": [{"entitlement_id": "` + approve.ID + `"}]}`
	if rec := e.do(auditor, http.MethodPost, "/api/v1/sod-policies", body); rec.Code != http.StatusForbidden {
		t.Errorf("auditor creates policy = %d", rec.Code)
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/sod-policies", body)
	p := decode[sod.Policy](t, rec)
	if rec.Code != http.StatusCreated || p.Mode != sod.Prevent {
		t.Fatalf("create policy = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/sod-policies", ""); rec.Code != http.StatusForbidden {
		t.Errorf("employee reads policies = %d", rec.Code)
	}

	// Ana asks for both sides: the second request is refused.
	if rec := e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+create.ID+`", "justification": "pay suppliers"}`); rec.Code != http.StatusCreated {
		t.Fatalf("first request = %d %s", rec.Code, rec.Body)
	}
	exec := func(sql string, args ...any) {
		t.Helper()
		err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), sql, args...)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// The first one is granted (by hand here, to keep the test short).
	exec(`UPDATE access_requests SET status = 'approved', closed_at = now()`)
	exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by)
	      SELECT tenant_id, beneficiary_id, entitlement_id, 'manual', 'test' FROM access_requests`)
	rec = e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+approve.ID+`", "justification": "approve them too"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("conflicting request = %d %s", rec.Code, rec.Body)
	}

	// Access granted outside requests is caught by a scan.
	exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by)
	      SELECT tenant_id, beneficiary_id, $1, 'manual', 'test' FROM access_requests`, approve.ID)
	scan := decode[sod.ScanResult](t, e.do(admin, http.MethodPost, "/api/v1/sod-violations/scan", ""))
	if scan.Detected != 1 {
		t.Errorf("scan = %+v", scan)
	}
	vs := decode[struct{ Violations []sod.Violation }](t, e.do(auditor, http.MethodGet, "/api/v1/sod-violations?status=open", ""))
	if len(vs.Violations) != 1 || vs.Violations[0].IdentityID != ana {
		t.Fatalf("violations = %+v", vs.Violations)
	}
	exc := "/api/v1/sod-violations/" + vs.Violations[0].ID + "/exception"
	if rec := e.do(auditor, http.MethodPost, exc, `{"reason": "ok"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor excepts = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, exc, `{"reason": "two-person team until March", "days": 60}`); rec.Code != http.StatusOK {
		t.Errorf("except = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(admin, http.MethodDelete, "/api/v1/sod-policies/"+p.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}
}
