package console

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/appauthz"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

func TestAppAuthorization(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	boss := e.identity("boss", "")
	ana := e.identity("ana", boss)
	luis := e.identity("luis", "")
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
	exec(`UPDATE identities SET attributes = '{"department": "Finance", "title": "Accountant"}' WHERE id = $1`, ana)
	// A workload the boss owns, which names itself by its SPIFFE ID.
	exec(`INSERT INTO identities (tenant_id, kind, display_name, manager_id, spiffe_id) VALUES ($1, 'workload', 'reports-job', $2, 'spiffe://example.com/reports')`,
		e.tenant, boss)

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Billing"}`))
	approve := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements",
		`{"native_id": "approver", "name": "Approve invoices"}`))
	view := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements",
		`{"native_id": "viewer", "name": "See invoices"}`))
	// Ana is assigned approver in Grantra and holds viewer in the app.
	exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
		e.tenant, ana, approve.ID)
	exec(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username) VALUES ($1, $2, $3, 'u-ana', 'ana.b')`,
		e.tenant, app.ID, ana)
	exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id)
		SELECT $1, id, $2 FROM accounts WHERE native_id = 'u-ana'`, e.tenant, view.ID)

	base := "/api/v1/applications/" + app.ID + "/authz"
	got := decode[appAuthz](t, e.do(auditor, http.MethodGet, base, ""))
	if got.Source != "" || len(got.Keys) != 0 {
		t.Fatalf("new app = %+v", got)
	}
	if rec := e.do(auditor, http.MethodPut, base+"/policies", `{"policies": ""}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor sets policies = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPut, base+"/policies", `{"policies": "permit (principal"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad policies = %d %s", rec.Code, rec.Body)
	}
	policies := `@id("finance approves")
permit (principal, action == Action::"approve", resource is Invoice)
when { principal.entitlements.contains("approver") && principal.department == "Finance" && resource.amount < 10000 };

@id("no self approval")
forbid (principal, action == Action::"approve", resource is Invoice)
when { resource has submitter && resource.submitter == principal.email };

permit (principal, action == Action::"view", resource)
when { principal.entitlements.contains("viewer") || principal.manager.email == "boss@example.com" };`
	if rec := e.do(admin, http.MethodPut, base+"/policies", `{"policies": `+quote(policies)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("set policies = %d %s", rec.Code, rec.Body)
	}

	// The playground shows the person as the policies see them.
	rec := e.do(auditor, http.MethodPost, base+"/test",
		`{"principal": {"username": "ANA.B"}, "action": "approve", "resource": {"type": "Invoice", "id": "1", "attributes": {"amount": 500}}}`)
	tried := decode[struct {
		appauthz.Answer
		Person *appauthz.Person `json:"person"`
	}](t, rec)
	if !tried.Allowed || !slices.Equal(tried.Policies, []string{"finance approves"}) || tried.Person == nil ||
		!slices.Equal(tried.Person.Entitlements, []string{"approver", "viewer"}) || tried.Person.Attributes["department"] != "Finance" {
		t.Fatalf("test = %d %+v", rec.Code, tried)
	}
	draft := decode[appauthz.Answer](t, e.do(admin, http.MethodPost, base+"/test",
		`{"policies": "", "principal": {"id": "`+ana+`"}, "action": "approve", "resource": {"type": "Invoice", "id": "1", "attributes": {"amount": 500}}}`))
	if draft.Allowed {
		t.Error("an empty draft allowed")
	}

	// Keys.
	rec = e.do(admin, http.MethodPost, base+"/keys", `{"name": "billing backend"}`)
	created := decode[struct {
		Key    appauthz.Key `json:"key"`
		Secret string       `json:"secret"`
	}](t, rec)
	if rec.Code != http.StatusCreated || !strings.HasPrefix(created.Secret, "grantra_ak_") || !strings.HasPrefix(created.Secret, created.Key.Prefix) {
		t.Fatalf("create key = %d %+v", rec.Code, created)
	}
	if e.count(`SELECT count(*) FROM app_api_keys WHERE hash = sha256($1::bytea)`, created.Secret) != 1 {
		t.Error("key not stored as its hash")
	}
	if strings.Contains(e.do(admin, http.MethodGet, base, "").Body.String(), created.Secret) {
		t.Error("the key's secret is readable after creation")
	}

	check := func(key, body string) (int, appauthz.Answer) {
		t.Helper()
		rec := e.do("", http.MethodPost, "/api/v1/authz/check", body, "Authorization", "Bearer "+key)
		if rec.Code != http.StatusOK {
			return rec.Code, appauthz.Answer{}
		}
		return rec.Code, decode[appauthz.Answer](t, rec)
	}
	invoice := func(who, extra string) string {
		return `{"principal": ` + who + `, "action": "approve", "resource": {"type": "Invoice", "id": "7", "attributes": {"amount": 1200` + extra + `}}}`
	}
	for _, tc := range []struct {
		name, body string
		allowed    bool
		reason     string
	}{
		{"ana by email", invoice(`{"email": "ANA@example.com"}`, ""), true, ""},
		{"too much", `{"principal": {"email": "ana@example.com"}, "action": "approve", "resource": {"type": "Invoice", "id": "7", "attributes": {"amount": 20000}}}`, false, ""},
		{"own invoice", invoice(`{"email": "ana@example.com"}`, `, "submitter": "ana@example.com"`), false, ""},
		{"luis", invoice(`{"id": "`+luis+`"}`, ""), false, ""},
		{"stranger", invoice(`{"email": "nobody@example.com"}`, ""), false, appauthz.ReasonUnknown},
		{"bad id", invoice(`{"id": "not-a-uuid"}`, ""), false, appauthz.ReasonUnknown},
		{"manager", `{"principal": {"id": "` + ana + `"}, "action": "view", "resource": {"type": "Report", "id": "q3"}}`, true, ""},
		{"workload", `{"principal": {"spiffe_id": "spiffe://example.com/reports"}, "action": "view", "resource": {"type": "Report", "id": "q3"}}`, true, ""},
		{"unknown workload", `{"principal": {"spiffe_id": "spiffe://example.com/other"}, "action": "view", "resource": {"type": "Report", "id": "q3"}}`, false, appauthz.ReasonUnknown},
	} {
		code, a := check(created.Secret, tc.body)
		if code != http.StatusOK || a.Allowed != tc.allowed || a.Reason != tc.reason {
			t.Errorf("%s: %d %+v", tc.name, code, a)
		}
	}
	if code, _ := check(created.Secret, `{"principal": {}, "action": "approve", "resource": {"type": "Invoice", "id": "7"}}`); code != http.StatusBadRequest {
		t.Errorf("no principal = %d", code)
	}
	if code, _ := check(created.Secret, invoice(`{"id": "`+ana+`"}`, `, "x": 1.5`)); code != http.StatusBadRequest {
		t.Errorf("bad attributes = %d", code)
	}
	if code, _ := check(created.Secret, `{"principal": {"id": "`+ana+`"}, "action": "approve", "resource": {"type": "Person", "id": "x"}}`); code != http.StatusBadRequest {
		t.Errorf("reserved type = %d", code)
	}

	// Leavers are denied whatever the policies say.
	exec(`UPDATE identities SET status = 'terminated' WHERE id = $1`, ana)
	if _, a := check(created.Secret, invoice(`{"id": "`+ana+`"}`, "")); a.Allowed || a.Reason != appauthz.ReasonInactive {
		t.Errorf("leaver = %+v", a)
	}

	// A key only answers for its own application.
	other := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Wiki"}`))
	otherKey := decode[struct {
		Secret string `json:"secret"`
	}](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+other.ID+"/authz/keys", `{"name": "wiki"}`))
	exec(`UPDATE identities SET status = 'active' WHERE id = $1`, ana)
	if _, a := check(otherKey.Secret, invoice(`{"id": "`+ana+`"}`, "")); a.Allowed {
		t.Error("another app's key used Billing's policies")
	}

	for _, bad := range []string{"", "grantra_ak_nope", "something"} {
		if code, _ := check(bad, invoice(`{"id": "`+ana+`"}`, "")); code != http.StatusUnauthorized {
			t.Errorf("key %q = %d", bad, code)
		}
	}
	if rec := e.do("", http.MethodPost, "/api/v1/authz/check", invoice(`{"id": "`+ana+`"}`, "")); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodDelete, base+"/keys/"+created.Key.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d", rec.Code)
	}
	if code, _ := check(created.Secret, invoice(`{"id": "`+ana+`"}`, "")); code != http.StatusUnauthorized {
		t.Errorf("revoked key = %d", code)
	}
	if rec := e.do(admin, http.MethodDelete, base+"/keys/00000000-0000-0000-0000-000000000000", ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoke unknown key = %d", rec.Code)
	}

	if n := e.count(`SELECT count(*) FROM audit_events WHERE action LIKE 'app\_authz.%'`); n != 4 {
		t.Errorf("authz audit events = %d; want policies set, two keys created, one revoked", n)
	}
}

func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
