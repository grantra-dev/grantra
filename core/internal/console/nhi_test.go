package console

import (
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/nhi"
)

func TestNonHumanIdentitiesAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	ana := e.identity("ana", "")
	luis := e.identity("luis", "")

	body := `{"kind": "agent", "name": "support-bot", "owner_id": "` + ana + `", "purpose": "Answers tickets"}`
	for who, code := range map[string]int{ana: http.StatusForbidden, auditor: http.StatusForbidden, helpdesk: http.StatusForbidden} {
		if rec := e.do(who, http.MethodPost, "/api/v1/non-human-identities", body); rec.Code != code {
			t.Errorf("POST by %s = %d", who, rec.Code)
		}
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/non-human-identities", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	bot := decode[nhi.Identity](t, rec)
	if bot.OwnerID != ana || bot.Kind != "agent" {
		t.Errorf("created %+v", bot)
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/non-human-identities", `{"kind": "agent", "name": "x"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no owner = %d", rec.Code)
	}

	// Helpdesk and auditors list; employees do not.
	if rec := e.do(ana, http.MethodGet, "/api/v1/non-human-identities", ""); rec.Code != http.StatusForbidden {
		t.Errorf("employee list = %d", rec.Code)
	}
	list := decode[struct{ Identities []nhi.Identity }](t, e.do(helpdesk, http.MethodGet, "/api/v1/non-human-identities?kind=agent", ""))
	counts := decode[map[string]int](t, e.do(helpdesk, http.MethodGet, "/api/v1/non-human-identities/summary", ""))
	if len(list.Identities) != 1 || counts["total"] != 1 || counts["no_owner"] != 0 {
		t.Errorf("list = %+v, counts = %v", list, counts)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/non-human-identities?flag=nope", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad flag = %d", rec.Code)
	}

	// The owner sees it, as its manager; someone else does not.
	if rec := e.do(ana, http.MethodGet, "/api/v1/non-human-identities/"+bot.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("owner GET = %d", rec.Code)
	}
	if rec := e.do(luis, http.MethodGet, "/api/v1/non-human-identities/"+bot.ID, ""); rec.Code != http.StatusForbidden {
		t.Errorf("stranger GET = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodGet, "/api/v1/non-human-identities/"+ana, ""); rec.Code != http.StatusNotFound {
		t.Errorf("a person as non-human = %d", rec.Code)
	}
	person := decode[identity](t, e.do(ana, http.MethodGet, "/api/v1/identities/"+bot.ID, ""))
	if person.Kind != "agent" || person.ManagerID != ana || person.Purpose != "Answers tickets" {
		t.Errorf("as an identity: %+v", person)
	}
	people := decode[struct{ Identities []identity }](t, e.do(helpdesk, http.MethodGet, "/api/v1/identities?kind=human", "")).Identities
	for _, p := range people {
		if p.Kind != "human" {
			t.Errorf("kind=human lists %+v", p)
		}
	}

	if rec := e.do(ana, http.MethodPatch, "/api/v1/non-human-identities/"+bot.ID, `{"owner_id": "`+luis+`"}`); rec.Code != http.StatusForbidden {
		t.Errorf("owner hands it over = %d", rec.Code)
	}
	moved := decode[nhi.Identity](t, e.do(admin, http.MethodPatch, "/api/v1/non-human-identities/"+bot.ID, `{"owner_id": "`+luis+`"}`))
	if moved.OwnerID != luis {
		t.Errorf("moved %+v", moved)
	}
	retired := decode[nhi.Identity](t, e.do(admin, http.MethodPost, "/api/v1/non-human-identities/"+bot.ID+"/retire", `{"reason": "Replaced"}`))
	if retired.Status != "terminated" {
		t.Errorf("retired %+v", retired)
	}

	if rec := e.do(helpdesk, http.MethodGet, "/api/v1/non-human-identities/candidates", ""); rec.Code != http.StatusForbidden {
		t.Errorf("helpdesk candidates = %d", rec.Code)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/non-human-identities/candidates", ""); rec.Code != http.StatusOK {
		t.Errorf("auditor candidates = %d", rec.Code)
	}
}
