package console

import (
	"net/http"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/requests"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

func TestVault(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	admin2 := e.identity("admin2", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")
	luis := e.identity("luis", "")

	create := `{"name": "prod-db root", "username": "root", "secret": "s3cret-root", "address": "prod-db:5432"}`
	// With no key the vault keeps no secrets.
	if rec := e.do(admin, http.MethodPost, "/api/v1/vault/accounts", create); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), vault.KeyEnv) {
		t.Fatalf("create with the vault off = %d %s", rec.Code, rec.Body)
	}
	key, err := vault.NewLocalKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	e.api.Vault = vault.New(key, nil)

	if rec := e.do(auditor, http.MethodPost, "/api/v1/vault/accounts", create); rec.Code != http.StatusForbidden {
		t.Errorf("auditor creates = %d", rec.Code)
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/vault/accounts", create)
	acct := decode[vault.Account](t, rec)
	if rec.Code != http.StatusCreated || acct.Version != 1 {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	path := "/api/v1/vault/accounts/" + acct.ID

	// Admins manage the vault but do not read its secrets.
	checkout := path + "/checkouts"
	if rec := e.do(admin, http.MethodPost, checkout, `{"reason": "curious"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("admin without access checks out = %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'authz.denied' AND actor = 'identity:' || $1`, admin); n != 1 {
		t.Errorf("%d denials audited", n)
	}
	if rec := e.do(auditor, http.MethodPost, checkout, `{"reason": "audit"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor checks out = %d", rec.Code)
	}

	// Ana asks for it like any access, an admin approves, and she can use it.
	rec = e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+acct.EntitlementID+`", "justification": "on call this week", "duration_days": 7}`)
	req := decode[requests.Request](t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("request = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodPost, checkout, `{"reason": "before approval"}`); rec.Code != http.StatusForbidden {
		t.Fatalf("checkout before approval = %d", rec.Code)
	}
	for _, step := range []string{admin, admin2} {
		if rec := e.do(step, http.MethodPost, "/api/v1/access-requests/"+req.ID+"/decision", `{"decision": "approve"}`); rec.Code != http.StatusOK {
			t.Fatalf("approve = %d %s", rec.Code, rec.Body)
		}
		if e.count(`SELECT count(*) FROM access_requests WHERE id = $1 AND status = 'approved'`, req.ID) == 1 {
			break
		}
	}
	if rec := e.do(ana, http.MethodPost, checkout, `{"reason": ""}`); rec.Code != http.StatusBadRequest {
		t.Errorf("checkout without a reason = %d", rec.Code)
	}
	rec = e.do(ana, http.MethodPost, checkout, `{"reason": "INC-42 restart the replica"}`)
	lease := decode[vault.Lease](t, rec)
	if rec.Code != http.StatusCreated || lease.Secret != "s3cret-root" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("ana checks out = %d %s (cache %q)", rec.Code, rec.Body, rec.Header().Get("Cache-Control"))
	}

	// Ana sees her account; Luis sees nothing; the auditor sees everything
	// and why, Luis never Ana's reason.
	list := func(as string) []vault.Account {
		t.Helper()
		rec := e.do(as, http.MethodGet, "/api/v1/vault/accounts", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("list = %d %s", rec.Code, rec.Body)
		}
		return decode[struct {
			Enabled  bool            `json:"enabled"`
			Accounts []vault.Account `json:"accounts"`
		}](t, rec).Accounts
	}
	if l := list(ana); len(l) != 1 || !l[0].Held || len(l[0].Checkouts) != 1 || l[0].Checkouts[0].Reason == "" {
		t.Fatalf("ana's list = %+v", l)
	}
	if l := list(luis); len(l) != 0 {
		t.Fatalf("luis's list = %+v", l)
	}
	if l := list(auditor); len(l) != 1 || l[0].Held || l[0].Checkouts[0].Reason != "INC-42 restart the replica" {
		t.Fatalf("auditor's list = %+v", l)
	}
	if strings.Contains(e.do(auditor, http.MethodGet, "/api/v1/vault/accounts", "").Body.String(), "s3cret") {
		t.Fatal("a list carries the secret")
	}
	rec = e.do(auditor, http.MethodGet, path, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"recent_checkouts"`) || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
		t.Errorf("ana reads history = %d", rec.Code)
	}

	// Only Ana checks her lease in; an admin can end it.
	checkin := "/api/v1/vault/checkouts/" + lease.ID + "/checkin"
	if rec := e.do(luis, http.MethodPost, checkin, ""); rec.Code != http.StatusForbidden {
		t.Errorf("luis checks in ana's = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, checkin, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("admin ends = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodPost, checkin, ""); rec.Code != http.StatusConflict {
		t.Errorf("checking in an ended lease = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM vault_checkouts WHERE id = $1 AND ended_by = 'ended'`, lease.ID); n != 1 {
		t.Error("the admin's ending was not recorded as such")
	}

	// Settings, secrets and rotation are for vault admins.
	if rec := e.do(auditor, http.MethodPut, path+"/secret", `{"secret": "x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor sets secret = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPut, path+"/secret", `{"secret": "n3w"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("set secret = %d %s", rec.Code, rec.Body)
	}
	if l := decode[vault.Lease](t, e.do(ana, http.MethodPost, checkout, `{"reason": "again"}`)); l.Secret != "n3w" || l.Version != 2 {
		t.Fatalf("after set = %+v", l)
	}
	if rec := e.do(admin, http.MethodPut, path, `{"checkout_minutes": 30, "exclusive": true}`); rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(admin, http.MethodPost, path+"/rotations", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("rotate an unlinked account = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/vault/discovery", ""); rec.Code != http.StatusForbidden {
		t.Errorf("ana discovers = %d", rec.Code)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/vault/discovery", ""); rec.Code != http.StatusOK {
		t.Errorf("auditor discovers = %d %s", rec.Code, rec.Body)
	}

	if rec := e.do(auditor, http.MethodDelete, path, ""); rec.Code != http.StatusForbidden {
		t.Errorf("auditor deletes = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodDelete, path, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(ana, http.MethodPost, checkout, `{"reason": "gone"}`); rec.Code != http.StatusNotFound {
		t.Errorf("checkout of a deleted account = %d", rec.Code)
	}
}
