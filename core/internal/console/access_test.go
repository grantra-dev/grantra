package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
	"github.com/grantra-dev/grantra/core/internal/requests"
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

// TestApplicationLifecycle runs an application from the console end to end:
// edit it, fix its entitlements, delete what was added by mistake and
// retire what has history.
func TestApplicationLifecycle(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	owner := e.identity("owner", "")
	want := func(as, method, path, body string, code int) *httptest.ResponseRecorder {
		t.Helper()
		rec := e.do(as, method, path, body)
		if rec.Code != code {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, rec.Code, code, rec.Body)
		}
		return rec
	}

	app := decode[catalog.Application](t, want(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`, http.StatusCreated))
	if app.ReadEveryHours != 1 || app.RetiredAt != nil {
		t.Errorf("new application = %+v", app)
	}
	path := "/api/v1/applications/" + app.ID
	want(admin, http.MethodPost, "/api/v1/applications", `{"name": "ERP"}`, http.StatusCreated)

	// Edit: owner, description and schedule; audited with old and new.
	body := `{"name": "Sales CRM", "description": " Pipeline ", "owner_id": "` + owner + `", "read_every_hours": 6}`
	want(auditor, http.MethodPut, path, body, http.StatusForbidden)
	got := decode[catalog.Application](t, want(admin, http.MethodPut, path, body, http.StatusOK))
	if got.Name != "Sales CRM" || got.Description != "Pipeline" || got.OwnerID != owner || got.OwnerName != "owner" || got.ReadEveryHours != 6 {
		t.Errorf("updated = %+v", got)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'application.updated' AND target_id = $1
	                 AND details->'owner_id'->>'to' = $2 AND details->'read_every_hours'->>'from' = '1'`, app.ID, owner); n != 1 {
		t.Errorf("application.updated events = %d", n)
	}
	want(admin, http.MethodPut, path, body, http.StatusOK)
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'application.updated'`); n != 1 {
		t.Errorf("saving without changes audited: %d events", n)
	}
	want(admin, http.MethodPut, path, `{"name": "ERP", "read_every_hours": 1}`, http.StatusConflict)
	want(admin, http.MethodPut, path, `{"name": "CRM", "read_every_hours": 169}`, http.StatusBadRequest)
	want(admin, http.MethodPut, path, `{"name": "CRM", "read_every_hours": -1}`, http.StatusBadRequest)
	want(admin, http.MethodPut, path, `{"name": "CRM", "owner_id": "nope"}`, http.StatusBadRequest)
	var builtinID string
	for _, a := range decode[struct{ Applications []catalog.Application }](t, want(admin, http.MethodGet, "/api/v1/applications", "", http.StatusOK)).Applications {
		if a.Connector == "grantra" {
			builtinID = a.ID
		}
	}
	want(admin, http.MethodPut, "/api/v1/applications/"+builtinID, `{"name": "Mine"}`, http.StatusBadRequest)
	want(admin, http.MethodDelete, "/api/v1/applications/"+builtinID, "", http.StatusBadRequest)
	want(admin, http.MethodPut, "/api/v1/applications/"+builtinID+"/retired", `{"retired": true}`, http.StatusBadRequest)

	// Entitlements: risk can always change, the name only while Grantra
	// names it.
	ents := path + "/entitlements"
	typo := decode[catalog.Entitlement](t, want(admin, http.MethodPost, ents, `{"name": "Sales usr"}`, http.StatusCreated))
	user := decode[catalog.Entitlement](t, want(admin, http.MethodPost, ents, `{"name": "Sales user"}`, http.StatusCreated))
	entPath := "/api/v1/entitlements/" + user.ID
	want(auditor, http.MethodPut, entPath, `{"name": "Sales user", "risk": "high"}`, http.StatusForbidden)
	want(admin, http.MethodPut, entPath, `{"name": "Sales user", "risk": "extreme"}`, http.StatusBadRequest)
	ent := decode[catalog.Entitlement](t, want(admin, http.MethodPut, entPath, `{"name": "Sales rep", "description": "Deals", "risk": "high"}`, http.StatusOK))
	if ent.Name != "Sales rep" || ent.Description != "Deals" || ent.Risk != "high" || ent.NativeID != "Sales user" {
		t.Errorf("updated entitlement = %+v", ent)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'entitlement.updated' AND target_id = $1
	                 AND details->'risk'->>'from' = 'low' AND details->'risk'->>'to' = 'high'`, user.ID); n != 1 {
		t.Errorf("entitlement.updated events = %d", n)
	}
	want(admin, http.MethodPut, "/api/v1/entitlements/00000000-0000-0000-0000-000000000000", `{"name": "X", "risk": "low"}`, http.StatusNotFound)

	// A business role uses the entitlement: it stays, the typo goes.
	role := decode[catalog.Role](t, want(admin, http.MethodPost, "/api/v1/roles", `{"name": "Sales", "entitlement_ids": ["`+user.ID+`"]}`, http.StatusCreated))
	if rec := want(admin, http.MethodDelete, entPath, "", http.StatusConflict); !strings.Contains(rec.Body.String(), "business role") {
		t.Errorf("delete used entitlement = %s", rec.Body)
	}
	want(admin, http.MethodDelete, "/api/v1/entitlements/"+typo.ID, "", http.StatusNoContent)
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'entitlement.deleted' AND target_id = $1`, typo.ID); n != 1 {
		t.Errorf("entitlement.deleted events = %d", n)
	}

	// The application has history now: it cannot be deleted, and cannot be
	// retired while the role still hands its access out.
	want(admin, http.MethodDelete, path, "", http.StatusConflict)
	want(admin, http.MethodPut, path+"/retired", `{"retired": true}`, http.StatusConflict)
	want(admin, http.MethodPut, "/api/v1/roles/"+role.ID, `{"name": "Sales", "entitlement_ids": []}`, http.StatusOK)
	// Nor while a request for its access is open.
	open := decode[requests.Request](t, want(owner, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+user.ID+`", "justification": "x"}`, http.StatusCreated))
	if rec := want(admin, http.MethodPut, path+"/retired", `{"retired": true}`, http.StatusConflict); !strings.Contains(rec.Body.String(), "still open") {
		t.Errorf("retire with an open request = %s", rec.Body)
	}
	want(owner, http.MethodPost, "/api/v1/access-requests/"+open.ID+"/cancel", "", http.StatusOK)
	e.exec(`UPDATE applications SET provisioning = '{"mode": "on", "max_changes": 5}' WHERE id = $1`, app.ID)
	retired := decode[catalog.Application](t, want(admin, http.MethodPut, path+"/retired", `{"retired": true}`, http.StatusOK))
	mode := func(a catalog.Application) (s struct {
		Mode       string `json:"mode"`
		MaxChanges int    `json:"max_changes"`
	}) {
		if err := json.Unmarshal(a.Provisioning, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if p := mode(retired); retired.RetiredAt == nil || p.Mode != "off" || p.MaxChanges != 5 {
		t.Errorf("retired = %+v %s", retired, retired.Provisioning)
	}

	// Retired: out of the catalog, takes no new access, not read or written.
	asked := decode[struct{ Items []requests.Requestable }](t, want(owner, http.MethodGet, "/api/v1/requestable-access", "", http.StatusOK))
	for _, it := range asked.Items {
		if it.ID == user.ID {
			t.Errorf("retired application's entitlement is requestable")
		}
	}
	want(owner, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+user.ID+`", "justification": "x"}`, http.StatusConflict)
	want(admin, http.MethodPost, ents, `{"name": "New"}`, http.StatusConflict)
	want(admin, http.MethodPost, "/api/v1/roles", `{"name": "Again", "entitlement_ids": ["`+user.ID+`"]}`, http.StatusConflict)
	want(admin, http.MethodPut, path+"/provisioning", `{"mode": "on"}`, http.StatusConflict)
	e.exec(`UPDATE applications SET connector = 'csv', config = '{"columns": {"native_id": "id"}}' WHERE id = $1`, app.ID)
	want(admin, http.MethodPost, path+"/account-imports", "id\nu1\n", http.StatusConflict)

	// Back in service: requestable again, provisioning still off.
	back := decode[catalog.Application](t, want(admin, http.MethodPut, path+"/retired", `{"retired": false}`, http.StatusOK))
	if back.RetiredAt != nil || mode(back).Mode != "off" {
		t.Errorf("restored = %+v", back)
	}
	want(owner, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+user.ID+`", "justification": "x"}`, http.StatusCreated)
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action IN ('application.retired', 'application.restored') AND target_id = $1`, app.ID); n != 2 {
		t.Errorf("retire events = %d", n)
	}

	// Connected applications name their entitlements and list them.
	if rec := want(admin, http.MethodPut, entPath, `{"name": "Renamed", "risk": "high"}`, http.StatusBadRequest); !strings.Contains(rec.Body.String(), "change its name there") {
		t.Errorf("rename on csv = %s", rec.Body)
	}
	want(admin, http.MethodPut, entPath, `{"name": "Sales rep", "risk": "critical"}`, http.StatusOK)

	// An application added by mistake, with an unused entitlement, goes.
	erp := decode[catalog.Application](t, want(admin, http.MethodPost, "/api/v1/applications", `{"name": "Typo"}`, http.StatusCreated))
	want(admin, http.MethodPost, "/api/v1/applications/"+erp.ID+"/entitlements", `{"name": "x"}`, http.StatusCreated)
	want(auditor, http.MethodDelete, "/api/v1/applications/"+erp.ID, "", http.StatusForbidden)
	want(admin, http.MethodDelete, "/api/v1/applications/"+erp.ID, "", http.StatusNoContent)
	want(admin, http.MethodDelete, "/api/v1/applications/"+erp.ID, "", http.StatusNotFound)
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'application.deleted' AND target_id = $1`, erp.ID); n != 1 {
		t.Errorf("application.deleted events = %d", n)
	}
}
