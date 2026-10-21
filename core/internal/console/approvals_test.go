package console

import (
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/approvals"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/requests"
)

func TestApprovalPolicies(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	boss := e.identity("boss", "")
	ana := e.identity("ana", boss)
	carla := e.identity("carla", "")
	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Payroll"}`))
	ent := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements",
		`{"native_id": "admin", "name": "Payroll admin", "risk": "high"}`))

	payroll := `{"name": "Payroll", "conditions": {"application_ids": ["` + app.ID + `"]},
		"steps": [{"approver": "manager"}, {"approver": "people", "people_ids": ["` + carla + `"], "label": "Payroll lead"}]}`
	for _, tc := range []struct {
		who, method, path, body string
		want                    int
	}{
		{ana, http.MethodGet, "/api/v1/approval-policies", "", http.StatusForbidden},
		{auditor, http.MethodGet, "/api/v1/approval-policies", "", http.StatusOK},
		{auditor, http.MethodPost, "/api/v1/approval-policies", payroll, http.StatusForbidden},
		{admin, http.MethodPost, "/api/v1/approval-policies", `{"name": "Nothing"}`, http.StatusBadRequest},
		{admin, http.MethodPut, "/api/v1/approval-policies/not-an-id", payroll, http.StatusNotFound},
		{admin, http.MethodPost, "/api/v1/approval-policies/simulate", `{"entitlement_id": "` + ent.ID + `"}`, http.StatusBadRequest},
		{ana, http.MethodPost, "/api/v1/approval-policies/simulate", `{"beneficiary_id": "` + ana + `", "entitlement_id": "` + ent.ID + `"}`, http.StatusForbidden},
	} {
		if rec := e.do(tc.who, tc.method, tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s %s %s = %d, want %d: %s", tc.who, tc.method, tc.path, rec.Code, tc.want, rec.Body)
		}
	}

	rec := e.do(admin, http.MethodPost, "/api/v1/approval-policies", payroll)
	p := decode[approvals.Policy](t, rec)
	if rec.Code != http.StatusCreated || len(p.Steps) != 2 || p.Steps[1].People[0].Name != "carla" {
		t.Fatalf("create = %d %+v", rec.Code, p)
	}
	other := decode[approvals.Policy](t, e.do(admin, http.MethodPost, "/api/v1/approval-policies", `{"name": "Admins", "steps": [{"approver": "admin"}]}`))
	list := decode[struct {
		Policies []approvals.Policy
		Builtin  approvals.Policy
	}](t, e.do(auditor, http.MethodGet, "/api/v1/approval-policies", ""))
	if len(list.Policies) != 2 || list.Policies[0].ID != p.ID || len(list.Builtin.Steps) != 2 {
		t.Errorf("list = %+v", list)
	}
	rec = e.do(admin, http.MethodPut, "/api/v1/approval-policies/order", `{"ids": ["`+other.ID+`", "`+p.ID+`"]}`)
	if got := decode[struct{ Policies []approvals.Policy }](t, rec).Policies; rec.Code != http.StatusOK || got[0].ID != other.ID {
		t.Errorf("reorder = %d %+v", rec.Code, got)
	}

	// The simulator tries a draft first, and the saved policies after it.
	simulate := func(body string) approvals.Plan {
		rec := e.do(auditor, http.MethodPost, "/api/v1/approval-policies/simulate", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("simulate = %d %s", rec.Code, rec.Body)
		}
		return decode[struct{ Plan approvals.Plan }](t, rec).Plan
	}
	ask := `"beneficiary_id": "` + ana + `", "entitlement_id": "` + ent.ID + `"`
	if plan := simulate(`{` + ask + `}`); plan.PolicyName != "Admins" || plan.Risk != "high" || len(plan.Steps) != 1 || plan.Steps[0].Kind != approvals.Admin {
		t.Errorf("plan = %+v", plan)
	}
	if plan := simulate(`{` + ask + `, "policy": ` + payroll + `}`); plan.PolicyName != "Payroll" || plan.PolicyID != "" ||
		len(plan.Steps) != 2 || plan.Steps[0].Approvers[0].Name != "boss" || plan.Steps[1].Label != "Payroll lead" {
		t.Errorf("draft plan = %+v", plan)
	}
	if rec := e.do(admin, http.MethodDelete, "/api/v1/approval-policies/"+other.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}

	// A request remembers the policy it followed, and its access says so.
	req := decode[requests.Request](t, e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+ent.ID+`", "justification": "payslips"}`))
	if req.PolicyID != p.ID || req.PolicyName != "Payroll" || req.Steps[1].Label != "Payroll lead" {
		t.Fatalf("request = %+v", req)
	}
	for _, who := range []string{boss, carla} {
		if rec := e.do(who, http.MethodPost, "/api/v1/access-requests/"+req.ID+"/decision", `{"decision": "approve"}`); rec.Code != http.StatusOK {
			t.Fatalf("%s approves = %d %s", who, rec.Code, rec.Body)
		}
	}
	access := decode[struct {
		Assignments []struct {
			RequestID string `json:"request_id"`
		}
	}](t,
		e.do(admin, http.MethodGet, "/api/v1/identities/"+ana+"/access", ""))
	if len(access.Assignments) != 1 || access.Assignments[0].RequestID != req.ID {
		t.Errorf("access = %+v", access)
	}
}
