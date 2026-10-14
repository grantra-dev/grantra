package console

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/requests"
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
	preview := decode[struct {
		SoDConflicts []string `json:"sod_conflicts"`
	}](t,
		e.do(ana, http.MethodPost, "/api/v1/access-requests/preview", `{"entitlement_id": "`+approve.ID+`"}`))
	if len(preview.SoDConflicts) != 1 || preview.SoDConflicts[0] != "Payments" {
		t.Errorf("preview = %+v", preview)
	}
	if rec := e.do(auditor, http.MethodPost, "/api/v1/access-requests/preview", `{"beneficiary_id": "`+ana+`", "entitlement_id": "`+approve.ID+`"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor previews for ana = %d", rec.Code)
	}
	// The conflicting request goes through, waiting on an admin to accept
	// the risk (TestRequestRisk follows one to the end).
	rec = e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+approve.ID+`", "justification": "approve them too"}`)
	req := decode[requests.Request](t, rec)
	if rec.Code != http.StatusCreated || len(req.SoDConflicts) != 1 || req.Steps[len(req.Steps)-1].Kind != requests.KindRisk {
		t.Errorf("conflicting request = %d %+v", rec.Code, req)
	}
	exec(`DELETE FROM access_requests WHERE id = $1`, req.ID)

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

func TestRequestRisk(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	boss := e.identity("boss", "")
	sara := e.identity("sara", boss)

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "ERP"}`))
	ents := "/api/v1/applications/" + app.ID + "/entitlements"
	create := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, ents, `{"native_id": "create", "name": "Create payments"}`))
	approve := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, ents, `{"native_id": "approve", "name": "Approve payments"}`))
	if rec := e.do(admin, http.MethodPost, "/api/v1/sod-policies",
		`{"name": "Payments", "mode": "prevent", "side_a": [{"entitlement_id": "`+create.ID+`"}], "side_b": [{"entitlement_id": "`+approve.ID+`"}]}`); rec.Code != http.StatusCreated {
		t.Fatalf("policy = %d %s", rec.Code, rec.Body)
	}
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
			e.tenant, sara, create.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := e.do(sara, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+approve.ID+`", "justification": "covering a leave"}`)
	req := decode[requests.Request](t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("request = %d", rec.Code)
	}
	decision := "/api/v1/access-requests/" + req.ID + "/decision"
	// Manager, then the ERP has no owner (admins), then the risk.
	for _, as := range []string{boss, admin} {
		if rec := e.do(as, http.MethodPost, decision, `{"decision": "approve"}`); rec.Code != http.StatusOK {
			t.Fatalf("approve as %s = %d %s", as, rec.Code, rec.Body)
		}
	}
	approvals := decode[struct{ Requests []requests.Request }](t, e.do(admin, http.MethodGet, "/api/v1/access-requests?scope=approvals", ""))
	if len(approvals.Requests) != 1 || approvals.Requests[0].Current().Kind != requests.KindRisk ||
		len(approvals.Requests[0].SoDConflicts) != 1 || approvals.Requests[0].SoDConflicts[0] != "Payments" {
		t.Fatalf("admin approvals = %+v", approvals.Requests)
	}
	for _, c := range []struct {
		as, body string
		code     int
	}{
		{boss, `{"decision": "approve", "comment": "fine", "exception_days": 30}`, http.StatusForbidden},
		{sara, `{"decision": "approve", "comment": "fine", "exception_days": 30}`, http.StatusForbidden},
		{admin, `{"decision": "approve", "comment": "fine"}`, http.StatusBadRequest},
		{admin, `{"decision": "approve", "exception_days": 30}`, http.StatusBadRequest},
	} {
		if rec := e.do(c.as, http.MethodPost, decision, c.body); rec.Code != c.code {
			t.Errorf("%s %s = %d %s, want %d", c.as, c.body, rec.Code, rec.Body, c.code)
		}
	}
	rec = e.do(admin, http.MethodPost, decision, `{"decision": "approve", "comment": "two-person team; audit checks payments weekly", "exception_days": 30}`)
	if out := decode[requests.Request](t, rec); rec.Code != http.StatusOK || out.Status != requests.Approved {
		t.Fatalf("accept risk = %d %+v", rec.Code, out)
	}
	vs := decode[struct{ Violations []sod.Violation }](t, e.do(admin, http.MethodGet, "/api/v1/sod-violations?status=excepted", ""))
	if len(vs.Violations) != 1 || vs.Violations[0].RequestID != req.ID || vs.Violations[0].ExceptionReason != "two-person team; audit checks payments weekly" {
		t.Fatalf("violations = %+v", vs.Violations)
	}

	// When the exception ends, the violation reopens.
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE sod_violations SET exception_until = now() - interval '1 minute'`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	scan := decode[sod.ScanResult](t, e.do(admin, http.MethodPost, "/api/v1/sod-violations/scan", ""))
	if scan.ExceptionsExpired != 1 || scan.Open != 1 {
		t.Errorf("scan = %+v", scan)
	}
}
