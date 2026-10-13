package console

import (
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

func TestCatalogAndBirthright(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)

	post := func(as, path, body string, want int) {
		t.Helper()
		if rec := e.do(as, http.MethodPost, path, body); rec.Code != want {
			t.Fatalf("POST %s = %d, want %d: %s", path, rec.Code, want, rec.Body)
		}
	}

	// Only admins shape the catalog; auditors read it.
	if rec := e.do(auditor, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor creates application = %d", rec.Code)
	}
	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM", "owner_id": "`+admin+`"}`))
	if app.Connector != catalog.ManualConnector || app.OwnerID != admin {
		t.Errorf("application = %+v", app)
	}
	post(admin, "/api/v1/applications", `{"name": "CRM"}`, http.StatusConflict)
	post(admin, "/api/v1/applications", `{"name": "X", "owner_id": "00000000-0000-0000-0000-000000000000"}`, http.StatusBadRequest)

	ents := "/api/v1/applications/" + app.ID + "/entitlements"
	ent := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, ents, `{"name": "CRM user", "native_id": "crm-users"}`))
	if ent.Risk != "low" || ent.ApplicationName != "CRM" {
		t.Errorf("entitlement = %+v", ent)
	}
	post(admin, ents, `{"name": "Again", "native_id": "crm-users"}`, http.StatusConflict)
	post(admin, ents, `{"name": "Root", "risk": "extreme"}`, http.StatusBadRequest)
	post(admin, "/api/v1/applications/00000000-0000-0000-0000-000000000000/entitlements", `{"name": "X"}`, http.StatusNotFound)

	// Platform roles stay out of the catalog's reach.
	apps := decode[struct{ Applications []catalog.Application }](t, e.do(auditor, http.MethodGet, "/api/v1/applications", ""))
	var builtin string
	for _, a := range apps.Applications {
		if a.Connector == "grantra" {
			builtin = a.ID
		}
	}
	post(admin, "/api/v1/applications/"+builtin+"/entitlements", `{"name": "grantra-root"}`, http.StatusBadRequest)

	role := decode[catalog.Role](t, e.do(admin, http.MethodPost, "/api/v1/roles", `{"name": "Sales rep", "entitlement_ids": ["`+ent.ID+`", "`+ent.ID+`"]}`))
	if len(role.Entitlements) != 1 || role.Entitlements[0].ID != ent.ID {
		t.Errorf("role = %+v", role)
	}
	post(admin, "/api/v1/roles", `{"name": "Sales rep"}`, http.StatusConflict)
	post(admin, "/api/v1/roles", `{"name": "Bad", "entitlement_ids": ["nope"]}`, http.StatusBadRequest)
	if rec := e.do(admin, http.MethodPut, "/api/v1/roles/"+role.ID, `{"name": "Sales representative", "entitlement_ids": []}`); rec.Code != http.StatusOK {
		t.Errorf("update role = %d %s", rec.Code, rec.Body)
	}
	if got := decode[catalog.Role](t, e.do(auditor, http.MethodGet, "/api/v1/roles/"+role.ID, "")); got.Name != "Sales representative" || len(got.Entitlements) != 0 {
		t.Errorf("updated role = %+v", got)
	}
	e.do(admin, http.MethodPut, "/api/v1/roles/"+role.ID, `{"name": "Sales rep", "entitlement_ids": ["`+ent.ID+`"]}`)
	for _, path := range []string{"/api/v1/applications", ents, "/api/v1/roles", "/api/v1/access-rules"} {
		if rec := e.do(helpdesk, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("GET %s as helpdesk = %d", path, rec.Code)
		}
		if rec := e.do(auditor, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s as auditor = %d", path, rec.Code)
		}
	}

	// HR brings two people.
	src := decode[lifecycle.Source](t, e.do(admin, http.MethodPost, "/api/v1/identity-sources", `{"name": "HR", "kind": "csv"}`))
	runs := "/api/v1/identity-sources/" + src.ID + "/runs"
	if rec := e.do(admin, http.MethodPost, runs, "employee_id,display_name,department\nE1,Ana,Sales\nE2,Luis,Engineering\n"); rec.Code != http.StatusOK {
		t.Fatalf("sync = %d %s", rec.Code, rec.Body)
	}
	people := decode[struct{ Identities []identity }](t, e.do(admin, http.MethodGet, "/api/v1/identities", ""))
	ids := map[string]string{}
	for _, i := range people.Identities {
		ids[i.ExternalID] = i.ID
	}

	// Preview, then create a rule: it applies at once.
	pv := decode[struct {
		Matches int
		Sample  []access.Match
	}](t, e.do(auditor, http.MethodPost, "/api/v1/access-rules/preview", `{"conditions": {"department": ["sales"]}}`))
	if pv.Matches != 1 || pv.Sample[0].ID != ids["E1"] {
		t.Errorf("preview = %+v", pv)
	}
	ruleBody := `{"name": "Sales", "conditions": {"department": ["Sales"]}, "role_ids": ["` + role.ID + `"]}`
	if rec := e.do(auditor, http.MethodPost, "/api/v1/access-rules", ruleBody); rec.Code != http.StatusForbidden {
		t.Errorf("auditor creates rule = %d", rec.Code)
	}
	rule := decode[ruleChange](t, e.do(admin, http.MethodPost, "/api/v1/access-rules", ruleBody))
	if rule.Changes.Added != 1 || rule.Holders != 1 || len(rule.Roles) != 1 {
		t.Errorf("rule = %+v", rule)
	}
	post(admin, "/api/v1/access-rules", ruleBody, http.StatusConflict)
	post(admin, "/api/v1/access-rules", `{"name": "Nothing"}`, http.StatusBadRequest)

	// Ana sees her own access; Luis cannot see hers.
	got := decode[struct{ Assignments []access.Assignment }](t, e.do(ids["E1"], http.MethodGet, "/api/v1/identities/"+ids["E1"]+"/access", ""))
	if len(got.Assignments) != 1 || got.Assignments[0].Name != "Sales rep" || got.Assignments[0].RuleName != "Sales" {
		t.Errorf("ana's access = %+v", got.Assignments)
	}
	if rec := e.do(ids["E2"], http.MethodGet, "/api/v1/identities/"+ids["E1"]+"/access", ""); rec.Code != http.StatusForbidden {
		t.Errorf("peer reads access = %d", rec.Code)
	}

	// Luis moves to Sales: the sync gives him the role right away.
	if rec := e.do(admin, http.MethodPost, runs, "employee_id,display_name,department\nE1,Ana,Sales\nE2,Luis,Sales\n"); rec.Code != http.StatusOK {
		t.Fatalf("second sync = %d %s", rec.Code, rec.Body)
	}
	got = decode[struct{ Assignments []access.Assignment }](t, e.do(helpdesk, http.MethodGet, "/api/v1/identities/"+ids["E2"]+"/access", ""))
	if len(got.Assignments) != 1 {
		t.Errorf("mover's access = %+v", got.Assignments)
	}

	// Widening the rule to Engineering changes nothing more; deleting it
	// takes the role from both.
	if rec := e.do(admin, http.MethodPut, "/api/v1/access-rules/"+rule.ID, `{"name": "Sales", "conditions": {"department": ["Sales", "Engineering"]}, "role_ids": ["`+role.ID+`"]}`); rec.Code != http.StatusOK {
		t.Errorf("update rule = %d %s", rec.Code, rec.Body)
	}
	del := decode[struct{ Changes access.Changes }](t, e.do(admin, http.MethodDelete, "/api/v1/access-rules/"+rule.ID, ""))
	if del.Changes.Removed != 2 {
		t.Errorf("delete removed %d, want 2", del.Changes.Removed)
	}
	if rec := e.do(admin, http.MethodGet, "/api/v1/access-rules/"+rule.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("deleted rule = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'access.assigned'`); n != 2 {
		t.Errorf("access.assigned audit events = %d, want 2", n)
	}
}
