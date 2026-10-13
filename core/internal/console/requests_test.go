package console

import (
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/requests"
)

func TestAccessRequests(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	boss := e.identity("boss", "")
	ana := e.identity("ana", boss)
	luis := e.identity("luis", "")

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM", "owner_id": "`+luis+`"}`))
	ent := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"native_id": "users", "name": "CRM users"}`))

	items := decode[struct{ Items []requests.Requestable }](t, e.do(ana, http.MethodGet, "/api/v1/requestable-access", ""))
	if len(items.Items) != 1 || items.Items[0].ID != ent.ID {
		t.Fatalf("requestable = %+v", items.Items)
	}

	body := `{"entitlement_id": "` + ent.ID + `", "justification": "sales pipeline"}`
	if rec := e.do(luis, http.MethodPost, "/api/v1/access-requests", `{"beneficiary_id": "`+ana+`", "entitlement_id": "`+ent.ID+`", "justification": "x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("luis asks for ana = %d", rec.Code)
	}
	if rec := e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+ent.ID+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no justification = %d", rec.Code)
	}
	rec := e.do(ana, http.MethodPost, "/api/v1/access-requests", body)
	req := decode[requests.Request](t, rec)
	if rec.Code != http.StatusCreated || req.Status != requests.Pending {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodPost, "/api/v1/access-requests", body); rec.Code != http.StatusConflict {
		t.Errorf("duplicate = %d", rec.Code)
	}
	path := "/api/v1/access-requests/" + req.ID

	// Who sees it.
	for who, want := range map[string]int{ana: 200, boss: 200, luis: 200, auditor: 200, admin: 200} {
		if rec := e.do(who, http.MethodGet, path, ""); rec.Code != want {
			t.Errorf("read as %s = %d", who, rec.Code)
		}
	}
	other := e.identity("other", "")
	if rec := e.do(other, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
		t.Errorf("read as an outsider = %d", rec.Code)
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/access-requests?scope=all", ""); rec.Code != http.StatusForbidden {
		t.Errorf("ana lists all = %d", rec.Code)
	}
	all := decode[struct{ Requests []requests.Request }](t, e.do(auditor, http.MethodGet, "/api/v1/access-requests?scope=all", ""))
	if len(all.Requests) != 1 {
		t.Errorf("auditor lists all = %+v", all.Requests)
	}
	waiting := decode[struct{ Requests []requests.Request }](t, e.do(boss, http.MethodGet, "/api/v1/access-requests?scope=approvals", ""))
	if len(waiting.Requests) != 1 {
		t.Errorf("boss's approvals = %+v", waiting.Requests)
	}

	// Decisions, in order.
	decision := path + "/decision"
	if rec := e.do(luis, http.MethodPost, decision, `{"decision": "approve"}`); rec.Code != http.StatusForbidden {
		t.Errorf("owner before the manager = %d", rec.Code)
	}
	if rec := e.do(ana, http.MethodPost, decision, `{"decision": "approve"}`); rec.Code != http.StatusForbidden {
		t.Errorf("ana approves herself = %d", rec.Code)
	}
	if rec := e.do(boss, http.MethodPost, decision, `{"decision": "maybe"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad decision = %d", rec.Code)
	}
	if rec := e.do(boss, http.MethodPost, decision, `{"decision": "approve", "comment": "ok"}`); rec.Code != http.StatusOK {
		t.Fatalf("boss approves = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(boss, http.MethodPost, path+"/cancel", ""); rec.Code != http.StatusForbidden {
		t.Errorf("approver cancels = %d", rec.Code)
	}
	rec = e.do(luis, http.MethodPost, decision, `{"decision": "approve"}`)
	if got := decode[requests.Request](t, rec); rec.Code != http.StatusOK || got.Status != requests.Approved {
		t.Fatalf("owner approves = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(luis, http.MethodPost, decision, `{"decision": "approve"}`); rec.Code != http.StatusForbidden && rec.Code != http.StatusConflict {
		t.Errorf("decide again = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1 AND origin = 'request'`, ana); n != 1 {
		t.Errorf("assignments = %d", n)
	}

	// Ana withdraws a second request; admins decide steps with nobody to ask.
	wiki := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Wiki"}`))
	page := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+wiki.ID+"/entitlements", `{"native_id": "edit", "name": "Editors"}`))
	second := decode[requests.Request](t, e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+page.ID+`", "justification": "docs"}`))
	if rec := e.do(ana, http.MethodPost, "/api/v1/access-requests/"+second.ID+"/cancel", ""); rec.Code != http.StatusOK {
		t.Errorf("cancel = %d %s", rec.Code, rec.Body)
	}
	third := decode[requests.Request](t, e.do(luis, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+page.ID+`", "justification": "docs"}`))
	adminQueue := decode[struct{ Requests []requests.Request }](t, e.do(admin, http.MethodGet, "/api/v1/access-requests?scope=approvals", ""))
	if len(adminQueue.Requests) != 1 || adminQueue.Requests[0].ID != third.ID {
		t.Errorf("admin approvals = %+v", adminQueue.Requests)
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/access-requests/"+third.ID+"/decision", `{"decision": "reject", "comment": "use the shared space"}`); rec.Code != http.StatusOK {
		t.Errorf("admin rejects = %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action LIKE 'access_request.%'`); n != 9 {
		t.Errorf("request audit events = %d", n)
	}
}
