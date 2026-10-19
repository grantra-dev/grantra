package console

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/requests"
	"github.com/grantra-dev/grantra/core/internal/reviews"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// ids returns the id of each element.
func ids[T any](list []T, id func(T) string) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		out = append(out, id(v))
	}
	slices.Sort(out)
	return out
}

func TestInbox(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	admin2 := e.identity("admin2", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	boss := e.identity("boss", "")
	ana := e.identity("ana", boss)
	luis := e.identity("luis", "")
	key, err := vault.NewLocalKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	e.api.Vault = vault.New(key, nil)

	inboxOf := func(who string) inbox {
		t.Helper()
		rec := e.do(who, http.MethodGet, "/api/v1/me/inbox", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("inbox of %s = %d %s", who, rec.Code, rec.Body)
		}
		return decode[inbox](t, rec)
	}
	// The inbox holds exactly what the Approvals page lists.
	sameAsApprovals := func(who string) {
		t.Helper()
		page := decode[struct{ Requests []requests.Request }](t, e.do(who, http.MethodGet, "/api/v1/access-requests?scope=approvals&limit=200", ""))
		got := ids(inboxOf(who).Approvals, func(w requests.Waiting) string { return w.ID })
		want := ids(page.Requests, func(r requests.Request) string { return r.ID })
		if !slices.Equal(got, want) {
			t.Errorf("%s's inbox approvals = %v, Approvals page = %v", who, got, want)
		}
	}

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM", "owner_id": "`+luis+`"}`))
	ent := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"native_id": "users", "name": "CRM users"}`))
	rec := e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+ent.ID+`", "justification": "pipeline"}`)
	req := decode[requests.Request](t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("request = %d %s", rec.Code, rec.Body)
	}
	// An admin asking for access: nobody decides on their own access, so
	// it waits on the other admin.
	rec = e.do(admin, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+ent.ID+`", "justification": "testing"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin's request = %d %s", rec.Code, rec.Body)
	}

	boxes := map[string]inbox{}
	for _, who := range []string{admin, admin2, auditor, boss, ana, luis} {
		sameAsApprovals(who)
		boxes[who] = inboxOf(who)
	}
	if b := boxes[boss]; len(b.Approvals) != 1 || b.Approvals[0].ID != req.ID || b.Approvals[0].BeneficiaryName != "ana" ||
		b.Approvals[0].TargetName != "CRM users" || b.Approvals[0].Application != "CRM" || b.Approvals[0].DueAt == nil || b.Total != 1 {
		t.Errorf("boss's inbox = %+v", b)
	}
	for _, who := range []string{ana, luis, auditor} {
		if b := boxes[who]; b.Total != 0 || len(b.Approvals) != 0 {
			t.Errorf("inbox of %s = %+v, want empty", who, b)
		}
	}

	// When the approver stops being active, the admins decide instead.
	e.exec(`UPDATE identities SET status = 'suspended' WHERE id = $1`, boss)
	sameAsApprovals(admin)
	if b := inboxOf(admin2); !slices.ContainsFunc(b.Approvals, func(w requests.Waiting) bool { return w.ID == req.ID }) {
		t.Errorf("admin2's inbox without an active manager = %+v", b.Approvals)
	}
	e.exec(`UPDATE identities SET status = 'active' WHERE id = $1`, boss)

	// Reviews count each access still to review, per campaign.
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
		e.tenant, ana, ent.ID)
	rec = e.do(admin, http.MethodPost, "/api/v1/review-campaigns", `{"name": "Q4", "application_ids": ["`+app.ID+`"]}`)
	c := decode[reviews.Campaign](t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("launch = %d %s", rec.Code, rec.Body)
	}
	b := inboxOf(boss)
	if len(b.Reviews) != 1 || b.Reviews[0].CampaignID != c.ID || b.Reviews[0].CampaignName != "Q4" || b.Reviews[0].Pending != 1 || b.Total != 2 {
		t.Errorf("boss's inbox with a campaign = %+v", b)
	}
	items := decode[struct{ Items []reviews.Item }](t, e.do(boss, http.MethodGet, "/api/v1/review-items?decision=pending", ""))
	if len(items.Items) != b.Reviews[0].Pending {
		t.Errorf("review page = %d items, inbox = %d", len(items.Items), b.Reviews[0].Pending)
	}
	if rec := e.do(boss, http.MethodPost, "/api/v1/review-items/"+items.Items[0].ID+"/decision", `{"decision": "keep"}`); rec.Code != http.StatusOK {
		t.Fatalf("keep = %d %s", rec.Code, rec.Body)
	}
	if b := inboxOf(boss); len(b.Reviews) != 0 || b.Total != 1 {
		t.Errorf("boss's inbox after reviewing = %+v", b)
	}

	// Break-glass uses wait on the admins, never on whoever used them, and
	// not on auditors, who cannot review.
	rec = e.do(admin, http.MethodPost, "/api/v1/vault/accounts",
		`{"name": "emergency", "username": "root", "secret": "glass", "break_glass": true, "break_glass_responders": ["`+ana+`", "`+admin2+`"]}`)
	acct := decode[vault.Account](t, rec)
	if rec.Code != http.StatusCreated {
		t.Fatalf("vault account = %d %s", rec.Code, rec.Body)
	}
	glass := "/api/v1/vault/accounts/" + acct.ID + "/break-glass"
	for _, who := range []string{ana, admin2} {
		if rec := e.do(who, http.MethodPost, glass, `{"reason": "INC-9"}`); rec.Code != http.StatusCreated {
			t.Fatalf("break glass as %s = %d %s", who, rec.Code, rec.Body)
		}
	}
	if b := inboxOf(admin); len(b.BreakGlass) != 2 || b.BreakGlass[0].Account != "emergency" || b.BreakGlass[0].Reason != "INC-9" {
		t.Errorf("admin's break-glass tasks = %+v", b.BreakGlass)
	}
	if b := inboxOf(admin2); len(b.BreakGlass) != 1 || b.BreakGlass[0].Person != "ana" {
		t.Errorf("admin2's break-glass tasks = %+v, want only ana's use", b.BreakGlass)
	}
	for _, who := range []string{auditor, ana} {
		if b := inboxOf(who); len(b.BreakGlass) != 0 {
			t.Errorf("%s's break-glass tasks = %+v", who, b.BreakGlass)
		}
	}
	// With privileged access turned off, its tasks leave the inbox too.
	if rec := e.do(admin, http.MethodPut, "/api/v1/modules/pam", `{"enabled": false}`); rec.Code != http.StatusOK {
		t.Fatalf("turn pam off = %d %s", rec.Code, rec.Body)
	}
	if b := inboxOf(admin); len(b.BreakGlass) != 0 {
		t.Errorf("break-glass tasks with pam off = %+v", b.BreakGlass)
	}
	if rec := e.do("", http.MethodGet, "/api/v1/me/inbox", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("inbox signed out = %d", rec.Code)
	}
}

func TestHome(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	ana := e.identity("ana", "")

	homeOf := func(who string) home {
		t.Helper()
		rec := e.do(who, http.MethodGet, "/api/v1/me/home", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("home of %s = %d %s", who, rec.Code, rec.Body)
		}
		return decode[home](t, rec)
	}

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`))
	soon := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"native_id": "soon", "name": "Soon"}`))
	later := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements",
		`{"native_id": "later", "name": "Later", "description": "Reads the CRM"}`))
	hours := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"native_id": "hours", "name": "Hours"}`))
	grant := func(ent, origin, expires string) {
		e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
		        VALUES ($1, $2, $3, $4, 'test', now() + $5::interval)`, e.tenant, ana, ent, origin, expires)
	}
	grant(soon.ID, "manual", "3 days")
	grant(later.ID, "manual", "10 days")
	grant(hours.ID, "activation", "2 hours")
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'request', 'test', now() - interval '1 day')`, e.tenant, ana, later.ID)

	h := homeOf(ana)
	if len(h.ExpiringAccess) != 1 || h.ExpiringAccess[0].Name != "Soon" || h.ExpiringAccess[0].Application != "CRM" {
		t.Errorf("ana's expiring access = %+v, want only Soon", h.ExpiringAccess)
	}
	if h.Governance != nil || h.PendingRequests != 0 {
		t.Errorf("ana's home = %+v", h)
	}
	extra := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements",
		`{"native_id": "extra", "name": "Extra", "description": "More"}`))
	if rec := e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "`+extra.ID+`", "justification": "x"}`); rec.Code != http.StatusCreated {
		t.Fatalf("request = %d %s", rec.Code, rec.Body)
	}
	if h := homeOf(ana); h.PendingRequests != 1 {
		t.Errorf("pending requests = %d", h.PendingRequests)
	}
	if h := homeOf(helpdesk); h.Governance != nil {
		t.Errorf("helpdesk sees governance cards: %+v", h.Governance)
	}

	// Failing runs: the last finished run of each application and source.
	e.exec(`INSERT INTO connector_runs (tenant_id, application_id, kind, actor, status, error, started_at, finished_at)
	        VALUES ($1, $2, 'aggregation', 'test', 'failed', 'connection refused', now() - interval '2 hours', now())`, e.tenant, app.ID)
	e.exec(`INSERT INTO connector_runs (tenant_id, application_id, kind, actor, status, started_at, finished_at)
	        VALUES ($1, $2, 'provisioning', 'test', 'planned', now() - interval '1 hour', now())`, e.tenant, app.ID)
	src := decodeID(t, e.do(admin, http.MethodPost, "/api/v1/identity-sources", `{"name": "Workday export", "kind": "csv"}`))
	e.exec(`INSERT INTO identity_source_runs (tenant_id, source_id, actor, dry_run, status, errors)
	        VALUES ($1, $2, 'test', false, 'refused', '[{"message": "too many leavers"}]')`, e.tenant, src)
	e.exec(`INSERT INTO identity_source_runs (tenant_id, source_id, actor, dry_run, status, errors, created_at)
	        VALUES ($1, $2, 'test', true, 'invalid', '[{"message": "bad file"}]', now() + interval '1 second')`, e.tenant, src)

	// Orphans: linked to nobody and still there.
	e.exec(`INSERT INTO accounts (tenant_id, application_id, native_id, status, correlated_by) VALUES
	        ($1, $2, 'o1', 'active', NULL), ($1, $2, 'o2', 'disabled', 'rule:email'), ($1, $2, 'gone', 'deleted', NULL)`,
		e.tenant, app.ID)
	e.exec(`INSERT INTO sod_policies (tenant_id, name) VALUES ($1, 'Payments')`, e.tenant)
	e.exec(`INSERT INTO sod_violations (tenant_id, policy_id, identity_id)
	        SELECT $1, id, $2 FROM sod_policies WHERE name = 'Payments'`, e.tenant, ana)

	for _, who := range []string{admin, auditor} {
		g := homeOf(who).Governance
		if g == nil {
			t.Fatalf("%s has no governance cards", who)
		}
		kinds := ids(g.FailedRuns, func(f failedRun) string { return f.Kind + ":" + f.Name + ":" + f.Error })
		if !slices.Equal(kinds, []string{"aggregation:CRM:connection refused", "hr:Workday export:too many leavers"}) {
			t.Errorf("%s's failed runs = %v", who, kinds)
		}
		if len(g.Orphans) != 1 || g.Orphans[0].ApplicationID != app.ID || g.Orphans[0].Count != 2 {
			t.Errorf("%s's orphans = %+v", who, g.Orphans)
		}
		// Soon and Hours have no description; Later has one.
		if g.Undescribed != 2 {
			t.Errorf("%s's undescribed = %d", who, g.Undescribed)
		}
		if g.OpenViolations == nil || *g.OpenViolations != 1 {
			t.Errorf("%s's open violations = %v", who, g.OpenViolations)
		}
	}
	// A later run that works clears the failure.
	e.exec(`INSERT INTO connector_runs (tenant_id, application_id, kind, actor, status, started_at, finished_at)
	        VALUES ($1, $2, 'aggregation', 'test', 'succeeded', now() - interval '1 minute', now())`, e.tenant, app.ID)
	if g := homeOf(admin).Governance; len(g.FailedRuns) != 1 || g.FailedRuns[0].Kind != "hr" {
		t.Errorf("failed runs after a good one = %+v", g.FailedRuns)
	}
}

// decodeID reads the id of a created object.
func decodeID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	v := decode[struct{ ID string }](t, rec)
	if v.ID == "" {
		t.Fatalf("no id in %d response", rec.Code)
	}
	return v.ID
}

func TestSearch(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	ana := e.identity("Ana Crespo", "")
	e.identity("100% Sure", "")
	key, err := vault.NewLocalKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	e.api.Vault = vault.New(key, nil)

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Crm Cloud"}`))
	decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements",
		`{"native_id": "APP_FIN_RW", "name": "APP_FIN_RW", "description": "Edit invoices in the CRM"}`))
	acct := decode[vault.Account](t, e.do(admin, http.MethodPost, "/api/v1/vault/accounts", `{"name": "crm-root", "username": "root", "secret": "s"}`))

	searchAs := func(who, q string) []string {
		t.Helper()
		rec := e.do(who, http.MethodGet, "/api/v1/search?q="+q, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("search %q as %s = %d %s", q, who, rec.Code, rec.Body)
		}
		var out []string
		for _, h := range decode[struct{ Results []searchHit }](t, rec).Results {
			out = append(out, h.Kind+":"+h.Title)
		}
		slices.Sort(out)
		return out
	}

	for _, q := range []string{"", "c", "%20%20c%20", strings.Repeat("x", 101)} {
		if rec := e.do(ana, http.MethodGet, "/api/v1/search?q="+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("search %q = %d", q, rec.Code)
		}
	}
	// Ana finds what she could ask for, matched by name or description
	// (the vault account's entitlement too, as on Request access), but
	// nobody, no applications and no vault account she does not hold.
	if got := searchAs(ana, "CRM"); !slices.Equal(got, []string{"entitlement:APP_FIN_RW", "entitlement:Use crm-root"}) {
		t.Errorf("ana searches CRM = %v", got)
	}
	if got := searchAs(helpdesk, "cres"); !slices.Equal(got, []string{"person:Ana Crespo"}) {
		t.Errorf("helpdesk searches people = %v", got)
	}
	want := []string{"application:Crm Cloud", "entitlement:APP_FIN_RW", "entitlement:Use crm-root", "vault_account:crm-root"}
	for _, who := range []string{admin, auditor} {
		if got := searchAs(who, "crm"); !slices.Equal(got, want) {
			t.Errorf("%s searches crm = %v, want %v", who, got, want)
		}
	}
	// Holding the vault access shows the account.
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'manual', 'test', now() + interval '1 day')`, e.tenant, ana, acct.EntitlementID)
	if got := searchAs(ana, "root"); !slices.Equal(got, []string{"entitlement:Use crm-root", "vault_account:crm-root"}) {
		t.Errorf("ana searches her vault account = %v", got)
	}
	// LIKE wildcards in the text are just text.
	if got := searchAs(admin, "0%25"); !slices.Equal(got, []string{"person:100% Sure"}) {
		t.Errorf("search 0%% = %v", got)
	}
	if got := searchAs(admin, "a_p"); len(got) != 0 {
		t.Errorf("search a_p = %v, want nothing", got)
	}
	people := decode[struct{ Identities []identity }](t, e.do(admin, http.MethodGet, "/api/v1/identities?q=0%25", ""))
	if len(people.Identities) != 1 || people.Identities[0].DisplayName != "100% Sure" {
		t.Errorf("People page q=0%% = %+v", people.Identities)
	}
	// Privileged access turned off leaves it out of search.
	if rec := e.do(admin, http.MethodPut, "/api/v1/modules/pam", `{"enabled": false}`); rec.Code != http.StatusOK {
		t.Fatalf("turn pam off = %d", rec.Code)
	}
	if got := searchAs(admin, "crm"); slices.Contains(got, "vault_account:crm-root") {
		t.Errorf("vault account found with pam off: %v", got)
	}
}

func TestModules(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")

	disabled := func(who string) []string {
		t.Helper()
		return decode[me](t, e.do(who, http.MethodGet, "/api/v1/me", "")).DisabledModules
	}
	if got := disabled(ana); got == nil || len(got) != 0 {
		t.Errorf("disabled modules at first = %#v, want []", got)
	}
	list := decode[struct{ Modules []module }](t, e.do(auditor, http.MethodGet, "/api/v1/modules", ""))
	if len(list.Modules) != 3 || !list.Modules[0].Enabled || !list.Modules[1].Enabled || list.Modules[2].ID != ModuleITDR || !list.Modules[2].Enabled {
		t.Errorf("modules = %+v", list.Modules)
	}
	for _, tc := range []struct {
		who, module, body string
		want              int
	}{
		{ana, "pam", `{"enabled": false}`, http.StatusForbidden},
		{auditor, "pam", `{"enabled": false}`, http.StatusForbidden},
		{admin, "iga", `{"enabled": false}`, http.StatusNotFound},
		{admin, "platform", `{"enabled": false}`, http.StatusNotFound},
		{admin, "pam", `{}`, http.StatusBadRequest},
		{admin, "pam", `{"enabled": false, "x": 1}`, http.StatusBadRequest},
	} {
		if rec := e.do(tc.who, http.MethodPut, "/api/v1/modules/"+tc.module, tc.body); rec.Code != tc.want {
			t.Errorf("PUT %s %s as %s = %d, want %d", tc.module, tc.body, tc.who, rec.Code, tc.want)
		}
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/modules", ""); rec.Code != http.StatusForbidden {
		t.Errorf("ana lists modules = %d", rec.Code)
	}
	for range 2 {
		rec := e.do(admin, http.MethodPut, "/api/v1/modules/pam", `{"enabled": false}`)
		if m := decode[module](t, rec); rec.Code != http.StatusOK || m.Enabled || m.ID != "pam" {
			t.Fatalf("turn pam off = %d %+v", rec.Code, m)
		}
	}
	if got := disabled(ana); !slices.Equal(got, []string{"pam"}) {
		t.Errorf("disabled modules = %v", got)
	}
	// Saying it twice records it once.
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'module.disabled' AND target_id = 'pam' AND actor = $1`, "identity:"+admin); n != 1 {
		t.Errorf("module.disabled events = %d", n)
	}
	if rec := e.do(admin, http.MethodPut, "/api/v1/modules/pam", `{"enabled": true}`); rec.Code != http.StatusOK {
		t.Fatalf("turn pam on = %d", rec.Code)
	}
	if got := disabled(ana); len(got) != 0 || e.count(`SELECT count(*) FROM audit_events WHERE action = 'module.enabled'`) != 1 {
		t.Errorf("after turning pam on: disabled = %v", got)
	}
}

func TestRequestableTeam(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	boss := e.identity("boss", "")
	ana := e.identity("ana", boss)
	bea := e.identity("bea", boss)
	cid := e.identity("cid", boss)
	e.identity("dan", boss)
	e.identity("eva", boss)
	loner := e.identity("loner", "")
	pair := e.identity("pair", "")
	duo := e.identity("duo", pair)
	other := e.identity("other", pair)
	e.exec(`UPDATE identities SET status = 'terminated' WHERE id = $1`, cid)

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`))
	users := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"native_id": "users", "name": "CRM users"}`))
	reports := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"native_id": "reports", "name": "Reports"}`))
	unused := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"native_id": "unused", "name": "Unused"}`))
	role := decodeID(t, e.do(admin, http.MethodPost, "/api/v1/roles", `{"name": "Analyst", "entitlement_ids": ["`+reports.ID+`"]}`))
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, role_id, origin, granted_by, expires_at) VALUES
	        ($1, $2, $3, NULL, 'manual', 'test', NULL),
	        ($1, $2, NULL, $4, 'manual', 'test', NULL),
	        ($1, $5, NULL, $4, 'manual', 'test', NULL),
	        ($1, $2, $6, NULL, 'manual', 'test', now() - interval '1 day')`,
		e.tenant, bea, users.ID, role, cid, unused.ID)
	// Bea also holds Reports directly: still one person.
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'request', 'test')`,
		e.tenant, bea, reports.ID)

	type item struct {
		Type        string
		Name        string
		TeamHolders int `json:"team_holders"`
	}
	type list struct {
		Items    []item
		TeamSize int `json:"team_size"`
	}
	got := decode[list](t, e.do(ana, http.MethodGet, "/api/v1/requestable-access", ""))
	held := map[string]int{}
	for _, it := range got.Items {
		held[it.Type+":"+it.Name] = it.TeamHolders
	}
	// Cid left, so the team is bea, dan and eva; ana does not count.
	want := map[string]int{"role:Analyst": 1, "entitlement:CRM users": 1, "entitlement:Reports": 1, "entitlement:Unused": 0}
	if got.TeamSize != 3 || len(held) != len(want) {
		t.Fatalf("team size = %d, items = %v", got.TeamSize, held)
	}
	for k, v := range want {
		if held[k] != v {
			t.Errorf("%s held by %d of the team, want %d", k, held[k], v)
		}
	}
	if got := decode[list](t, e.do(loner, http.MethodGet, "/api/v1/requestable-access", "")); got.TeamSize != 0 {
		t.Errorf("loner's team = %d", got.TeamSize)
	}
	// Bea's team is ana, dan and eva, who hold nothing.
	if got := decode[list](t, e.do(bea, http.MethodGet, "/api/v1/requestable-access", "")); got.TeamSize != 3 {
		t.Errorf("bea's team = %d", got.TeamSize)
	} else {
		for _, it := range got.Items {
			if it.TeamHolders != 0 {
				t.Errorf("bea's team holds %s", it.Name)
			}
		}
	}
	// In a small team, a count would tell what a given colleague holds.
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'request', 'test')`,
		e.tenant, other, users.ID)
	if got := decode[list](t, e.do(duo, http.MethodGet, "/api/v1/requestable-access", "")); got.TeamSize != 1 {
		t.Errorf("duo's team = %d", got.TeamSize)
	} else {
		for _, it := range got.Items {
			if it.TeamHolders != 0 {
				t.Errorf("a team of one tells that %s is held", it.Name)
			}
		}
	}
}

// Auditors can open every Governance and Settings page: each page's data
// must read for them, while the pages hide what they cannot change.
func TestAuditorReadsGovernanceAndSettings(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`))
	src := decodeID(t, e.do(admin, http.MethodPost, "/api/v1/identity-sources", `{"name": "HR", "kind": "csv"}`))
	paths := []string{
		"/api/v1/applications",
		"/api/v1/applications/" + app.ID + "/entitlements",
		"/api/v1/applications/" + app.ID + "/accounts?limit=200",
		"/api/v1/applications/" + app.ID + "/runs",
		"/api/v1/applications/" + app.ID + "/authz",
		"/api/v1/roles",
		"/api/v1/access-rules",
		"/api/v1/standing-access?limit=200",
		"/api/v1/sod-policies",
		"/api/v1/sod-violations",
		"/api/v1/identity-sources",
		"/api/v1/identity-sources/" + src,
		"/api/v1/identity-sources/" + src + "/runs",
		"/api/v1/lifecycle-events",
		"/api/v1/service-tokens",
		"/api/v1/login",
		"/api/v1/modules",
		"/api/v1/platform-roles",
	}
	for _, p := range paths {
		if rec := e.do(auditor, http.MethodGet, p, ""); rec.Code != http.StatusOK {
			t.Errorf("auditor GET %s = %d %s", p, rec.Code, rec.Body)
		}
		// The menu shows these pages to admins and auditors only.
		if rec := e.do(helpdesk, http.MethodGet, p, ""); rec.Code != http.StatusForbidden {
			t.Errorf("helpdesk GET %s = %d", p, rec.Code)
		}
	}
}
