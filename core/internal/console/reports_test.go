package console

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/reports"
)

// id runs an INSERT ... RETURNING id in the tenant.
func (e *env) id(sql string, args ...any) string {
	e.t.Helper()
	var id string
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql, args...).Scan(&id)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// table finds a table of a report and maps its rows by column key.
func table(t *testing.T, res reports.Result, name string) []map[string]string {
	t.Helper()
	i := slices.IndexFunc(res.Tables, func(tr reports.TableResult) bool { return tr.Name == name })
	if i < 0 {
		t.Fatalf("%s has no table %s", res.Report.ID, name)
	}
	var out []map[string]string
	for _, row := range res.Tables[i].Rows {
		m := map[string]string{}
		for j, c := range res.Tables[i].Columns {
			m[c.Key] = row[j]
		}
		out = append(out, m)
	}
	return out
}

func facts(res reports.Result) map[string]int {
	out := map[string]int{}
	for _, f := range res.Facts {
		out[f.Label] = f.Value
	}
	return out
}

// Every report runs on real data and says what an auditor needs; downloads
// are audited with their hash, and a pack can be verified later.
func TestReports(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	luis := e.identity("Luis", "")
	ana := e.identity("Ana", luis)
	bea := e.identity("Bea", luis)
	e.exec(`INSERT INTO identities (tenant_id, display_name, kind) VALUES ($1, 'billing-bot', 'service')`, e.tenant)

	erp := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'ERP', 'csv') RETURNING id`, e.tenant)
	crm := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'CRM', 'csv') RETURNING id`, e.tenant)
	payments := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name, risk) VALUES ($1, $2, 'pay', 'Payments', 'high') RETURNING id`, e.tenant, erp)
	vendors := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name) VALUES ($1, $2, 'ven', 'Vendors') RETURNING id`, e.tenant, erp)
	leads := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name) VALUES ($1, $2, 'leads', 'Leads') RETURNING id`, e.tenant, crm)

	// Ana's ERP account still has Payments; an old account matches nobody.
	anaAccount := e.id(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id) VALUES ($1, $2, $3, 'ana') RETURNING id`, e.tenant, erp, ana)
	orphan := e.id(`INSERT INTO accounts (tenant_id, application_id, native_id) VALUES ($1, $2, 'old-admin') RETURNING id`, e.tenant, erp)
	crmAccount := e.id(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id) VALUES ($1, $2, $3, 'luis') RETURNING id`, e.tenant, crm, luis)
	e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3), ($1, $4, $3), ($1, $5, $6)`,
		e.tenant, anaAccount, payments, orphan, crmAccount, leads)

	// Luis asked for Vendors and his manager... nobody: an admin approved.
	req := e.id(`INSERT INTO access_requests (tenant_id, requester_id, beneficiary_id, entitlement_id, justification, status, closed_at, policy_name, policy_version)
		VALUES ($1, $2, $2, $3, 'pay suppliers', 'approved', now(), 'Default', 2) RETURNING id`, e.tenant, luis, vendors)
	e.exec(`INSERT INTO access_request_steps (tenant_id, request_id, position, kind, status, approver_ids, decided_by, decided_at)
		VALUES ($1, $2, 0, 'admin', 'approved', ARRAY[$3::uuid], 'identity:' || $3, now())`, e.tenant, req, admin)
	e.exec(`INSERT INTO access_request_approvals (tenant_id, request_id, position, identity_id) VALUES ($1, $2, 0, $3)`, e.tenant, req, admin)
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, request_id) VALUES ($1, $2, $3, 'request', 'identity:' || $4, $5)`,
		e.tenant, luis, vendors, admin, req)

	// A campaign revoked Ana's Payments, which the ERP still shows.
	camp := e.id(`INSERT INTO review_campaigns (tenant_id, name, reviewer, due_at, created_by) VALUES ($1, 'Q3 review', 'manager', now() + interval '7 days', 'identity:' || $2) RETURNING id`,
		e.tenant, admin)
	e.exec(`INSERT INTO review_items (tenant_id, campaign_id, identity_id, entitlement_id, origin, granted_at, reviewer_id, decision, decided_by, decided_at)
		VALUES ($1, $2, $3, $4, 'manual', now() - interval '1 year', $5::uuid, 'revoke', 'identity:' || $5, now()),
		       ($1, $2, $5::uuid, $6, 'request', now(), NULL, 'pending', NULL, NULL)`, e.tenant, camp, ana, payments, luis, vendors)

	// Ana left two hours ago and still has an account; Bea left two days
	// ago and her account closed after 30 hours, past the 24 hour target.
	e.exec(`UPDATE identities SET status = 'terminated' WHERE id IN ($1, $2)`, ana, bea)
	e.exec(`INSERT INTO lifecycle_events (tenant_id, identity_id, kind, effective_at) VALUES ($1, $2, 'leaver', now() - interval '2 hours'), ($1, $3, 'leaver', now() - interval '50 hours')`,
		e.tenant, ana, bea)
	e.exec(`INSERT INTO audit_events (tenant_id, occurred_at, actor, action, target_type, target_id, details)
		VALUES ($1, now() - interval '20 hours', 'system:provisioning', 'provisioning.disable_account', 'account', gen_random_uuid()::text,
		        jsonb_build_object('identity_id', $2::text, 'status', 'done'))`, e.tenant, bea)

	sod := e.id(`INSERT INTO sod_policies (tenant_id, name) VALUES ($1, 'Pay and vendors') RETURNING id`, e.tenant)
	e.exec(`INSERT INTO sod_policy_sides (tenant_id, policy_id, side, entitlement_id) VALUES ($1, $2, 'a', $3), ($1, $2, 'b', $4)`, e.tenant, sod, payments, vendors)
	e.exec(`INSERT INTO sod_violations (tenant_id, policy_id, identity_id, status, exception_by, exception_reason)
		VALUES ($1, $2, $3, 'excepted', 'identity:' || $4, 'small team')`, e.tenant, sod, luis, admin)

	e.exec(`INSERT INTO audit_events (tenant_id, actor, action, target_type, target_id, details)
		VALUES ($1, 'identity:' || $2::text, 'access.activated', 'identity', $2::text, jsonb_build_object('entitlement_id', $3::text, 'hours', 2,
		        'justification', 'month end', 'expires_at', now() + interval '2 hours'))`, e.tenant, luis, payments)
	vaultEnt := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name) VALUES ($1, $2, 'root', 'root') RETURNING id`, e.tenant, erp)
	vault := e.id(`INSERT INTO vault_accounts (tenant_id, name, username, kind, entitlement_id, created_by) VALUES ($1, 'ERP root', 'root', 'password', $2, 'test') RETURNING id`,
		e.tenant, vaultEnt)
	e.exec(`INSERT INTO vault_checkouts (tenant_id, vault_account_id, identity_id, version, reason, expires_at, break_glass, review_status)
		VALUES ($1, $2, $3, 1, 'outage', now() + interval '1 hour', true, 'pending')`, e.tenant, vault, luis)
	e.exec(`INSERT INTO ssh_sessions (tenant_id, target_name, login, identity_id, client_address, gateway, kind, recording_bytes)
		VALUES ($1, 'db-1', 'root', $2, '10.0.0.1', 'gw', 'shell', 10)`, e.tenant, luis)
	e.exec(`INSERT INTO threat_alerts (tenant_id, detection, severity, identity_id, subject, summary, first_seen_at, last_seen_at)
		VALUES ($1, 'leaver_sign_in', 'critical', $2::uuid, $2::text, 'Ana signed in after leaving', now(), now())`, e.tenant, ana)

	today := time.Now().UTC()
	period := "from=" + today.AddDate(0, 0, -7).Format(time.DateOnly) + "&to=" + today.Format(time.DateOnly)
	run := func(id, query string) reports.Result {
		t.Helper()
		rec := e.do(auditor, http.MethodGet, "/api/v1/reports/"+id+"?"+query, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s?%s = %d %s", id, query, rec.Code, rec.Body)
		}
		return decode[reports.Result](t, rec)
	}

	// Who may run reports.
	for _, tc := range []struct {
		who  string
		want int
	}{{auditor, http.StatusOK}, {admin, http.StatusOK}, {helpdesk, http.StatusForbidden}, {luis, http.StatusForbidden}} {
		if rec := e.do(tc.who, http.MethodGet, "/api/v1/reports", ""); rec.Code != tc.want {
			t.Errorf("list reports = %d, want %d", rec.Code, tc.want)
		}
		if rec := e.do(tc.who, http.MethodGet, "/api/v1/reports/access", ""); rec.Code != tc.want {
			t.Errorf("run report = %d, want %d", rec.Code, tc.want)
		}
	}
	list := decode[struct {
		Reports     []reports.Definition
		DefaultFrom string `json:"default_from"`
		DefaultTo   string `json:"default_to"`
	}](t, e.do(auditor, http.MethodGet, "/api/v1/reports", ""))
	if len(list.Reports) != len(reports.List()) || list.DefaultFrom == "" || list.DefaultTo < list.DefaultFrom {
		t.Errorf("list = %+v", list)
	}

	// Every report runs, with and without dates.
	for _, d := range reports.List() {
		run(d.ID, period)
		if res := run(d.ID, ""); res.Params.From.Format(time.DateOnly) != list.DefaultFrom {
			t.Errorf("%s without dates covers %v", d.ID, res.Params)
		}
	}

	rev := run(reports.Reviews, period)
	if f := facts(rev); f["Campaigns"] != 1 || f["Revoked"] != 1 || f["Not reviewed"] != 1 || f["Revoked but still held"] != 0 {
		t.Errorf("review facts = %v", f)
	}
	for _, d := range table(t, rev, "decisions") {
		switch d["person"] {
		case "Ana":
			if d["decided_by"] != "Luis" || d["still_held"] != "no" || d["still_in_application"] != "yes" || d["application"] != "ERP" || d["risk"] != "high" {
				t.Errorf("Ana's decision = %v", d)
			}
		case "Luis":
			if d["reviewer"] != "any admin" || d["still_held"] != "" {
				t.Errorf("Luis's item = %v", d)
			}
		}
	}
	if rows := table(t, run(reports.Reviews, period+"&application="+crm), "campaigns"); len(rows) != 0 {
		t.Errorf("a campaign with nothing in CRM shows for CRM: %v", rows)
	}

	lv := run(reports.Leavers, period)
	targets := map[string]string{}
	for _, l := range table(t, lv, "leavers") {
		targets[l["person"]] = l["target"]
		if l["person"] == "Ana" && (l["active_accounts"] != "1" || l["applications"] != "ERP") {
			t.Errorf("Ana = %v", l)
		}
		if l["person"] == "Bea" && l["hours"] != "30.0" {
			t.Errorf("Bea = %v", l)
		}
	}
	if targets["Ana"] != "open, in time" || targets["Bea"] != "no" {
		t.Errorf("targets = %v", targets)
	}
	orphans := table(t, lv, "orphans")
	if len(orphans) != 2 || orphans[0]["reason"] != "belongs to a leaver: Ana" || orphans[1]["reason"] != "matches nobody" || orphans[1]["privileged"] != "yes" {
		t.Errorf("orphans = %v", orphans)
	}
	// With a 1 hour target, Ana is late too.
	e.exec(`INSERT INTO tenant_settings (tenant_id, key, value, updated_by) VALUES ($1, 'reports.leaver_sla_hours', 1, 'test')`, e.tenant)
	if f := facts(run(reports.Leavers, period)); f["Closed late or still open"] != 2 {
		t.Errorf("with a 1 hour target = %v", f)
	}

	rq := table(t, run(reports.Requests, period), "requests")
	if len(rq) != 1 || rq[0]["approved_by"] != "admin" || rq[0]["policy"] != "Default (version 2)" || rq[0]["duration"] != "until removed" {
		t.Errorf("requests = %v", rq)
	}
	if steps := table(t, run(reports.Requests, period), "steps"); len(steps) != 1 || !strings.HasPrefix(steps[0]["approvals"], "admin at ") {
		t.Errorf("steps = %v", steps)
	}

	sd := run(reports.SoD, period)
	if p := table(t, sd, "policies"); len(p) != 1 || p[0]["side_a"] != "Payments" || p[0]["side_b"] != "Vendors" {
		t.Errorf("policies = %v", p)
	}
	if v := table(t, sd, "violations"); len(v) != 1 || v[0]["exception_by"] != "admin" || v[0]["exception_reason"] != "small team" {
		t.Errorf("violations = %v", v)
	}

	pr := run(reports.Privileged, period)
	if f := facts(pr); f["Activations"] != 1 || f["Break-glass uses"] != 1 || f["Break-glass not reviewed yet"] != 1 || f["Sessions"] != 1 {
		t.Errorf("privileged facts = %v", f)
	}
	if a := table(t, pr, "activations"); a[0]["access"] != "Payments" || a[0]["application"] != "ERP" || a[0]["justification"] != "month end" {
		t.Errorf("activation = %v", a)
	}

	ac := run(reports.Access, "")
	// Platform roles count too: admin (critical), auditor (high), helpdesk.
	if f := facts(ac); f["Memberships"] != 6 || f["High or critical risk"] != 4 || f["Not given through Grantra"] != 3 || f["Assignments"] != 1 {
		t.Errorf("access facts = %v", f)
	}
	if rows := table(t, run(reports.Access, "application="+crm), "memberships"); len(rows) != 1 || rows[0]["entitlement"] != "Leads" {
		t.Errorf("access in CRM = %v", rows)
	}

	ad := run(reports.Admin, period)
	if f := facts(ad); f["Admins"] != 1 {
		t.Errorf("admin facts = %v", f)
	}

	th := run(reports.Threats, period)
	if f := facts(th); f["Alerts"] != 1 || f["Critical or high"] != 1 || f["Non-human identities needing attention"] != 1 {
		t.Errorf("threat facts = %v", f)
	}

	// Mistakes.
	for _, q := range []string{
		"access?format=pdf", "access?format=csv", "access?format=csv&table=nope", "access?from=2026-13-01&to=2026-12-31",
		"access?from=2026-01-01", "reviews?from=2026-02-01&to=2026-01-01", "reviews?from=2020-01-01&to=2026-01-01",
		"sod?application=" + erp, "access?application=nope",
	} {
		if rec := e.do(auditor, http.MethodGet, "/api/v1/reports/"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", q, rec.Code, rec.Body)
		}
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/reports/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown report = %d", rec.Code)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/reports/access?application="+leads, ""); rec.Code != http.StatusNotFound {
		t.Errorf("unknown application = %d", rec.Code)
	}

	// Downloads go to the audit log with their hash.
	audited := func(sha string) int {
		return e.count(`SELECT count(*) FROM audit_events WHERE action = 'report.generated' AND details->>'sha256' = $1 AND actor = 'identity:' || $2`, sha, auditor)
	}
	rec := e.do(auditor, http.MethodGet, "/api/v1/reports/leavers?format=csv&table=orphans&"+period, "")
	csv := rec.Body.Bytes()
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		!strings.Contains(rec.Header().Get("Content-Disposition"), "grantra-leavers-") ||
		!bytes.HasPrefix(csv, []byte("\xef\xbb\xbfApplication,Account,")) || audited(reports.SHA256(csv)) != 1 {
		t.Errorf("csv = %d %v %q", rec.Code, rec.Header(), csv)
	}
	rec = e.do(auditor, http.MethodGet, "/api/v1/reports/reviews?format=html&"+period, "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Security-Policy") != reports.SummaryCSP ||
		!strings.Contains(rec.Body.String(), "<h1>Access reviews</h1>") || !strings.Contains(rec.Body.String(), "Ana") {
		t.Errorf("html = %d %v", rec.Code, rec.Header())
	}

	// An evidence pack verifies; a changed one does not.
	rec = e.do(auditor, http.MethodGet, "/api/v1/reports/requests?format=zip&"+period, "")
	pack := rec.Body.Bytes()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("zip = %d %s", rec.Code, rec.Body)
	}
	verify := func(data []byte) reports.Verification {
		t.Helper()
		rec := e.do(auditor, http.MethodPost, "/api/v1/report-verifications", string(data))
		if rec.Code != http.StatusOK {
			t.Fatalf("verify = %d %s", rec.Code, rec.Body)
		}
		return decode[reports.Verification](t, rec)
	}
	if v := verify(pack); !v.Valid || !v.Pack || v.GeneratedBy != "auditor" || v.Manifest.Report != reports.Requests || len(v.Manifest.Files) != 3 {
		t.Errorf("verify pack = %+v", v)
	}
	if v := verify(csv); !v.Valid || v.Pack {
		t.Errorf("verify csv = %+v", v)
	}
	if v := verify(append(slices.Clone(csv), "x"...)); v.Valid || len(v.Problems) != 1 {
		t.Errorf("verify changed csv = %+v", v)
	}
	if v := verify(rezip(t, pack, "requests/requests.csv", "edited")); v.Valid || !slices.Contains(v.Problems, "requests/requests.csv was changed") {
		t.Errorf("verify changed pack = %+v", v)
	}
	if rec := e.do(auditor, http.MethodPost, "/api/v1/report-verifications", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("verify nothing = %d", rec.Code)
	}

	// Frameworks: auditors read them, admins change them.
	fws := decode[struct{ Frameworks []reports.Framework }](t, e.do(auditor, http.MethodGet, "/api/v1/report-frameworks", "")).Frameworks
	if len(fws) != 4 || fws[2].ID != "soc2" || !fws[2].Builtin || fws[2].Customized || !fws[2].Enabled {
		t.Fatalf("frameworks = %+v", fws)
	}
	soc2 := `{"name": "ignored", "enabled": true, "controls": [{"id": "AC-1", "title": "Access approved", "reports": ["requests", "requests"]}]}`
	if rec := e.do(auditor, http.MethodPut, "/api/v1/report-frameworks/soc2", soc2); rec.Code != http.StatusForbidden {
		t.Errorf("auditor edits = %d", rec.Code)
	}
	fw := decode[reports.Framework](t, e.do(admin, http.MethodPut, "/api/v1/report-frameworks/soc2", soc2))
	if fw.Name != "SOC 2" || !fw.Customized || len(fw.Controls) != 1 || len(fw.Controls[0].Reports) != 1 {
		t.Errorf("edited soc2 = %+v", fw)
	}
	for _, body := range []string{
		`{"name": "X", "controls": [{"id": "A", "title": "T", "reports": ["nope"]}]}`,
		`{"name": "X", "controls": [{"id": "A", "title": "T", "reports": []}]}`,
		`{"name": "X", "controls": [{"id": "A", "title": "T", "reports": ["sod"]}, {"id": "a", "title": "U", "reports": ["sod"]}]}`,
		`{"name": " ", "controls": []}`,
	} {
		if rec := e.do(admin, http.MethodPost, "/api/v1/report-frameworks", body); rec.Code != http.StatusBadRequest {
			t.Errorf("save %s = %d", body, rec.Code)
		}
	}
	rec = e.do(admin, http.MethodPost, "/api/v1/report-frameworks", `{"name": "DORA", "enabled": true, "controls": [{"id": "Art. 9", "title": "Protection", "reports": ["access", "sod"]}]}`)
	dora := decode[reports.Framework](t, rec)
	if rec.Code != http.StatusCreated || dora.Builtin || dora.ID == "" {
		t.Fatalf("add framework = %d %+v", rec.Code, dora)
	}
	rec = e.do(auditor, http.MethodGet, "/api/v1/report-frameworks/"+dora.ID+"/pack?"+period, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("framework pack = %d %s", rec.Code, rec.Body)
	}
	if v := verify(rec.Body.Bytes()); !v.Valid || v.Manifest.Framework != dora.ID || len(v.Manifest.Files) != 6 || v.Manifest.Files[0].Path != "controls.csv" {
		t.Errorf("framework pack = %+v", v)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/report-frameworks/"+dora.ID+"/pack?application="+erp, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("framework pack for one application = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodDelete, "/api/v1/report-frameworks/"+dora.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPut, "/api/v1/report-frameworks/"+dora.ID, soc2); rec.Code != http.StatusNotFound {
		t.Errorf("edit deleted = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodDelete, "/api/v1/report-frameworks/soc2", ""); rec.Code != http.StatusNoContent {
		t.Errorf("reset = %d", rec.Code)
	}
	if fw := decode[struct{ Frameworks []reports.Framework }](t, e.do(admin, http.MethodGet, "/api/v1/report-frameworks", "")).Frameworks[2]; fw.Customized || len(fw.Controls) != 5 {
		t.Errorf("reset soc2 = %+v", fw)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action LIKE 'report_framework.%'`); n != 4 {
		t.Errorf("framework changes audited = %d", n)
	}
	if rows := table(t, run(reports.Admin, period), "changes"); !slices.ContainsFunc(rows, func(r map[string]string) bool {
		return r["action"] == "report_framework.saved" && r["who"] == "admin"
	}) {
		t.Errorf("admin changes miss the framework edits: %v", rows)
	}

	// Scripts download with a report.read token, and nothing else does.
	secret := decode[struct{ Secret string }](t, e.do(admin, http.MethodPost, "/api/v1/service-tokens",
		`{"name": "Evidence", "scopes": ["report.read"], "expires_in_days": 30}`)).Secret
	if code := e.bearer(secret, http.MethodGet, "/api/v1/reports/access?format=zip", ""); code != http.StatusOK {
		t.Errorf("token download = %d", code)
	}
	if code := e.bearer(secret, http.MethodGet, "/api/v1/report-frameworks/soc2/pack", ""); code != http.StatusOK {
		t.Errorf("token pack = %d", code)
	}
	if code := e.bearer(secret, http.MethodGet, "/api/v1/report-frameworks", ""); code != http.StatusUnauthorized {
		t.Errorf("token on a session route = %d", code)
	}
	other := decode[struct{ Secret string }](t, e.do(admin, http.MethodPost, "/api/v1/service-tokens",
		`{"name": "HR", "scopes": ["sign_in_event.send"], "expires_in_days": 30}`)).Secret
	if code := e.bearer(other, http.MethodGet, "/api/v1/reports/access", ""); code != http.StatusForbidden {
		t.Errorf("token without the scope = %d", code)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'report.generated' AND actor LIKE 'service_token:%'`); n != 2 {
		t.Errorf("token downloads audited = %d", n)
	}

	// Another tenant sees none of it, and cannot verify this tenant's pack.
	e2 := newEnv(t)
	auditor2 := e2.identity("auditor", "", authz.RoleAuditor)
	if f := facts(decode[reports.Result](t, e2.do(auditor2, http.MethodGet, "/api/v1/reports/access", ""))); f["Memberships"] != 1 || f["Not given through Grantra"] != 0 {
		t.Errorf("other tenant's access = %v", f)
	}
	if v := decode[reports.Verification](t, e2.do(auditor2, http.MethodPost, "/api/v1/report-verifications", string(pack))); v.Valid {
		t.Errorf("other tenant verifies the pack: %+v", v)
	}
}

// rezip copies a zip with one file's content replaced.
func rezip(t *testing.T, data []byte, name, content string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for _, f := range zr.File {
		w, err := zw.Create(f.Name)
		if err != nil {
			t.Fatal(err)
		}
		if f.Name == name {
			io.WriteString(w, content) //nolint:errcheck
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(w, rc) //nolint:errcheck
		rc.Close()
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
