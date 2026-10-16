package console

import (
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/requests"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

func TestJustInTime(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")
	luis := e.identity("luis", "")

	rec := e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Cloud"}`)
	app := decode[catalog.Application](t, rec)
	rec = e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"name": "Org admin", "risk": "critical"}`)
	ent := decode[catalog.Entitlement](t, rec)
	if ent.MaxActivationHours != catalog.DefaultActivationHours || ent.JITOnly {
		t.Fatalf("new entitlement = %+v", ent)
	}

	// Settings: admins only.
	jit := "/api/v1/entitlements/" + ent.ID + "/jit"
	if rec := e.do(auditor, http.MethodPut, jit, `{"jit_only": true}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor sets just-in-time = %d", rec.Code)
	}
	rec = e.do(admin, http.MethodPut, jit, `{"jit_only": true, "max_activation_hours": 2}`)
	if got := decode[catalog.Entitlement](t, rec); rec.Code != http.StatusOK || !got.JITOnly || got.MaxActivationHours != 2 {
		t.Fatalf("set just-in-time = %d %s", rec.Code, rec.Body)
	}

	// Eligibility: admins make others eligible, never themselves.
	body := `{"entitlement_id": "` + ent.ID + `"}`
	if rec := e.do(admin, http.MethodPost, "/api/v1/identities/"+admin+"/eligibilities", body); rec.Code != http.StatusForbidden {
		t.Errorf("admin makes self eligible = %d", rec.Code)
	}
	if rec := e.do(luis, http.MethodPost, "/api/v1/identities/"+ana+"/eligibilities", body); rec.Code != http.StatusForbidden {
		t.Errorf("employee makes others eligible = %d", rec.Code)
	}
	rec = e.do(admin, http.MethodPost, "/api/v1/identities/"+ana+"/eligibilities", `{"entitlement_id": "`+ent.ID+`", "duration_days": 30}`)
	el := decode[access.Eligibility](t, rec)
	if rec.Code != http.StatusCreated || el.ExpiresAt == nil || el.MaxActivationHours != 2 {
		t.Fatalf("make eligible = %d %s", rec.Code, rec.Body)
	}
	rec = e.do(ana, http.MethodGet, "/api/v1/identities/"+ana+"/eligibilities", "")
	if list := decode[struct{ Eligibilities []access.Eligibility }](t, rec).Eligibilities; len(list) != 1 {
		t.Errorf("ana's eligibilities = %s", rec.Body)
	}
	if rec := e.do(luis, http.MethodGet, "/api/v1/identities/"+ana+"/eligibilities", ""); rec.Code != http.StatusForbidden {
		t.Errorf("a peer reads ana's eligibilities = %d", rec.Code)
	}

	// Only ana activates her eligibility, admins included.
	activate := "/api/v1/eligibilities/" + el.ID + "/activations"
	if rec := e.do(admin, http.MethodPost, activate, `{"hours": 1, "justification": "for her"}`); rec.Code != http.StatusForbidden {
		t.Errorf("admin activates for ana = %d", rec.Code)
	}
	if rec := e.do(ana, http.MethodPost, activate, `{"hours": 3, "justification": "deploy"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("over the max hours = %d", rec.Code)
	}
	rec = e.do(ana, http.MethodPost, activate, `{"hours": 2, "justification": "deploy"}`)
	if res := decode[requests.ActivationResult](t, rec); rec.Code != http.StatusCreated || res.Activation == nil {
		t.Fatalf("activate = %d %s", rec.Code, rec.Body)
	}
	if e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1 AND origin = 'activation'`, ana) != 1 {
		t.Error("no activation")
	}
	if rec := e.do(luis, http.MethodPost, "/api/v1/eligibilities/"+el.ID+"/deactivate", ""); rec.Code != http.StatusForbidden {
		t.Errorf("peer deactivates = %d", rec.Code)
	}
	if rec := e.do(ana, http.MethodPost, "/api/v1/eligibilities/"+el.ID+"/deactivate", ""); rec.Code != http.StatusNoContent {
		t.Errorf("deactivate = %d %s", rec.Code, rec.Body)
	}

	// Standing access: auditors read the report, admins convert it, never
	// their own.
	// Standing access from before the entitlement was just-in-time only.
	if rec := e.do(admin, http.MethodPut, jit, `{"jit_only": false}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by)
	        SELECT $1, i, e.id, 'manual', 'test' FROM unnest($2::uuid[]) i, entitlements e WHERE e.id = $3`,
		e.tenant, []string{luis, admin}, ent.ID)
	if rec := e.do(admin, http.MethodPut, jit, `{"jit_only": true, "max_activation_hours": 2}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	rec = e.do(auditor, http.MethodGet, "/api/v1/standing-access", "")
	standing := decode[struct{ Standing []access.Standing }](t, rec).Standing
	if rec.Code != http.StatusOK || len(standing) != 2 {
		t.Fatalf("standing = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/standing-access", ""); rec.Code != http.StatusForbidden {
		t.Errorf("employee reads standing access = %d", rec.Code)
	}
	for _, s := range standing {
		rec := e.do(admin, http.MethodPost, "/api/v1/assignments/"+s.AssignmentID+"/eligibility", "")
		want := http.StatusCreated
		if s.IdentityID == admin {
			want = http.StatusForbidden
		}
		if rec.Code != want {
			t.Errorf("convert %s's = %d, want %d", s.IdentityName, rec.Code, want)
		}
	}
	if rec := e.do(admin, http.MethodDelete, "/api/v1/eligibilities/"+el.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("remove eligibility = %d %s", rec.Code, rec.Body)
	}
}

func TestBreakGlassAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	admin2 := e.identity("admin2", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")
	key, err := vault.NewLocalKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	e.api.Vault = vault.New(key, nil)

	rec := e.do(admin, http.MethodPost, "/api/v1/vault/accounts",
		`{"name": "emergency", "username": "root", "secret": "glass", "break_glass": true, "break_glass_responders": ["`+ana+`", "`+admin+`"]}`)
	acct := decode[vault.Account](t, rec)
	if rec.Code != http.StatusCreated || len(acct.Responders) != 2 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	// The vault entitlement is just-in-time only.
	if e.count(`SELECT count(*) FROM entitlements WHERE id = $1 AND jit_only`, acct.EntitlementID) != 1 {
		t.Error("vault entitlement is not just-in-time only")
	}
	if rec := e.do(admin, http.MethodPut, "/api/v1/entitlements/"+acct.EntitlementID+"/jit", `{"jit_only": false}`); rec.Code != http.StatusBadRequest {
		t.Errorf("turning just-in-time off for the vault = %d", rec.Code)
	}

	// Ana, a responder, sees it and can break the glass; admin2 cannot.
	rec = e.do(ana, http.MethodGet, "/api/v1/vault/accounts", "")
	list := decode[struct{ Accounts []vault.Account }](t, rec).Accounts
	if len(list) != 1 || !list[0].Responder || list[0].Held {
		t.Fatalf("ana's list = %s", rec.Body)
	}
	glass := "/api/v1/vault/accounts/" + acct.ID + "/break-glass"
	if rec := e.do(admin2, http.MethodPost, glass, `{"reason": "curious"}`); rec.Code != http.StatusForbidden {
		t.Errorf("admin not a responder breaks glass = %d", rec.Code)
	}
	// Admin set it up, so admin is no responder despite the list.
	if rec := e.do(admin, http.MethodPost, glass, `{"reason": "mine now"}`); rec.Code != http.StatusForbidden {
		t.Errorf("admin who armed it breaks glass = %d", rec.Code)
	}
	if rec := e.do(ana, http.MethodPost, glass, `{"reason": ""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no reason = %d", rec.Code)
	}
	rec = e.do(ana, http.MethodPost, glass, `{"reason": "INC-9 production down"}`)
	lease := decode[vault.Lease](t, rec)
	if rec.Code != http.StatusCreated || lease.Secret != "glass" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("break glass = %d %s", rec.Code, rec.Body)
	}

	// Review: admins, not the auditor, not the person who used it.
	rec = e.do(auditor, http.MethodGet, "/api/v1/vault/break-glass", "")
	uses := decode[struct{ Uses []vault.Checkout }](t, rec).Uses
	if rec.Code != http.StatusOK || len(uses) != 1 || uses[0].ReviewStatus != "pending" {
		t.Fatalf("uses = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/vault/break-glass", ""); rec.Code != http.StatusForbidden {
		t.Errorf("employee lists uses = %d", rec.Code)
	}
	review := "/api/v1/vault/break-glass/" + lease.ID + "/review"
	if rec := e.do(auditor, http.MethodPost, review, `{"justified": true}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor reviews = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, review, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("review without a verdict = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, review, `{"justified": true, "comment": "INC-9 is real"}`); rec.Code != http.StatusOK {
		t.Errorf("review = %d %s", rec.Code, rec.Body)
	}

	// Once another admin sets the responders, admin may open it, but does
	// not review their own use.
	settings := `{"checkout_minutes": 60, "break_glass": true, "break_glass_responders": ["` + admin + `"]}`
	if rec := e.do(admin2, http.MethodPut, "/api/v1/vault/accounts/"+acct.ID, settings); rec.Code != http.StatusOK {
		t.Fatalf("admin2 sets responders = %d %s", rec.Code, rec.Body)
	}
	rec = e.do(admin, http.MethodPost, glass, `{"reason": "INC-10"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin breaks glass = %d %s", rec.Code, rec.Body)
	}
	own := decode[vault.Lease](t, rec)
	if rec := e.do(admin, http.MethodPost, "/api/v1/vault/break-glass/"+own.ID+"/review", `{"justified": true}`); rec.Code != http.StatusForbidden {
		t.Errorf("self review = %d", rec.Code)
	}
	if rec := e.do(admin2, http.MethodPost, "/api/v1/vault/break-glass/"+own.ID+"/review", `{"justified": false, "comment": "no ticket"}`); rec.Code != http.StatusOK {
		t.Errorf("other admin flags = %d %s", rec.Code, rec.Body)
	}
}
