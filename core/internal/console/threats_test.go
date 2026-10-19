package console

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/itdr"
)

// failures is a body of n wrong passwords for user, from a script.
func failures(user string, n int) string {
	var evs []string
	now := time.Now().UTC()
	for i := range n {
		evs = append(evs, fmt.Sprintf(`{"id": "%s-%d-%d", "time": %q, "user": %q, "outcome": "failure", "ip": "203.0.113.4"}`,
			user, now.UnixNano(), i, now.Add(-time.Duration(n-i)*time.Second).Format(time.RFC3339Nano), user))
	}
	return `{"source": "okta", "events": [` + strings.Join(evs, ",") + `]}`
}

func TestThreats(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	other := e.identity("other", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")

	// Scripts send sign-ins with a token that may do only that.
	tok := decode[created](t, e.do(admin, http.MethodPost, "/api/v1/service-tokens",
		`{"name": "Okta sign-ins", "scopes": ["sign_in_event.send"], "expires_in_days": 30}`))
	if tok.Secret == "" {
		t.Fatal("no token")
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/service-tokens",
		`{"name": "x", "scopes": ["sign_in_event.send:`+ana+`"], "expires_in_days": 30}`); rec.Code != http.StatusBadRequest {
		t.Errorf("pinned sign-in scope = %d, want 400", rec.Code)
	}
	if code := e.bearer(tok.Secret, http.MethodPost, "/api/v1/sign-in-events", failures("ana@example.com", 10)); code != http.StatusOK {
		t.Fatalf("send sign-ins = %d", code)
	}
	if code := e.bearer(tok.Secret, http.MethodGet, "/api/v1/sign-in-events", ""); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Errorf("token lists sign-ins = %d, want refused", code)
	}
	hr := decode[created](t, e.do(admin, http.MethodPost, "/api/v1/service-tokens",
		`{"name": "HR", "scopes": ["identity_source.sync"], "expires_in_days": 30}`))
	if code := e.bearer(hr.Secret, http.MethodPost, "/api/v1/sign-in-events", failures("ana@example.com", 1)); code != http.StatusForbidden {
		t.Errorf("HR token sends sign-ins = %d, want 403", code)
	}
	for _, as := range []string{auditor, ana} {
		if rec := e.do(as, http.MethodPost, "/api/v1/sign-in-events", failures("x", 1)); rec.Code != http.StatusForbidden {
			t.Errorf("%s sends sign-ins = %d, want 403", as, rec.Code)
		}
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/sign-in-events", `{"source": "okta", "events": []}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no events = %d, want 400", rec.Code)
	}

	if rec := e.do(auditor, http.MethodPost, "/api/v1/threats/scan", ""); rec.Code != http.StatusForbidden {
		t.Errorf("auditor scans = %d", rec.Code)
	}
	scan := decode[struct{ Result itdr.ScanResult }](t, e.do(admin, http.MethodPost, "/api/v1/threats/scan", ""))
	if scan.Result.Raised != 1 {
		t.Fatalf("scan = %+v, want one alert", scan.Result)
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/threats/alerts", ""); rec.Code != http.StatusForbidden {
		t.Errorf("employee lists alerts = %d", rec.Code)
	}
	list := decode[struct{ Alerts []itdr.Alert }](t, e.do(auditor, http.MethodGet, "/api/v1/threats/alerts?status=open", ""))
	if len(list.Alerts) != 1 || list.Alerts[0].IdentityID != ana || list.Alerts[0].Title != "Password guessing" {
		t.Fatalf("alerts = %+v", list.Alerts)
	}
	alert := list.Alerts[0]
	if rec := e.do(auditor, http.MethodGet, "/api/v1/threats/alerts?severity=urgent", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad severity = %d", rec.Code)
	}
	events := decode[struct{ Events []itdr.Event }](t, e.do(auditor, http.MethodGet, "/api/v1/sign-in-events?identity="+ana+"&outcome=failure&limit=5", ""))
	if len(events.Events) != 5 || events.Events[0].IdentityName != "ana" {
		t.Errorf("sign-ins = %+v", events.Events)
	}
	more := decode[struct{ Events []itdr.Event }](t, e.do(auditor, http.MethodGet,
		fmt.Sprintf("/api/v1/sign-in-events?identity=%s&before=%d", ana, events.Events[4].ID), ""))
	if len(more.Events) != 5 {
		t.Errorf("next page = %d sign-ins, want 5", len(more.Events))
	}
	overview := decode[struct {
		Open    itdr.Counts
		Sources []signInSource
	}](t, e.do(auditor, http.MethodGet, "/api/v1/threats", ""))
	if overview.Open["medium"] != 1 || len(overview.Sources) != 1 || overview.Sources[0].Source != "okta" || overview.Sources[0].Identified != 10 {
		t.Errorf("overview = %+v", overview)
	}

	// Detections: auditors read them, admins change them.
	if rec := e.do(auditor, http.MethodPut, "/api/v1/threats/detections/brute_force", `{"severity": "high"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor changes a detection = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPut, "/api/v1/threats/detections/brute_force", `{"auto_response": "suspend"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("suspending on failures only = %d, want 400", rec.Code)
	}
	if rec := e.do(admin, http.MethodPut, "/api/v1/threats/detections/nope", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown detection = %d, want 404", rec.Code)
	}
	set := decode[itdr.Setting](t, e.do(admin, http.MethodPut, "/api/v1/threats/detections/brute_force", `{"values": {"failures": 20}}`))
	if set.Values["failures"] != 20 {
		t.Errorf("setting = %+v", set)
	}
	dets := decode[struct{ Detections []itdr.Setting }](t, e.do(auditor, http.MethodGet, "/api/v1/threats/detections", ""))
	if len(dets.Detections) != len(itdr.Definitions) {
		t.Errorf("detections = %d", len(dets.Detections))
	}

	// Responses: admins, and never about themselves.
	resp := "/api/v1/identities/" + ana + "/threat-responses"
	if rec := e.do(auditor, http.MethodPost, resp, `{"action": "revoke_sessions"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor responds = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/identities/"+admin+"/threat-responses", `{"action": "revoke_sessions"}`); rec.Code != http.StatusForbidden {
		t.Errorf("admin responds about themselves = %d", rec.Code)
	}
	r := decode[itdr.Response](t, e.do(admin, http.MethodPost, resp, `{"action": "suspend", "alert_id": "`+alert.ID+`", "reason": "looks like an attack"}`))
	if r.Action != itdr.Suspend || r.Actor != "identity:"+admin {
		t.Errorf("response = %+v", r)
	}
	if n := e.count(`SELECT count(*) FROM identities WHERE id = $1 AND status = 'suspended'`, ana); n != 1 {
		t.Error("ana is not suspended")
	}
	// Suspended, she can do nothing, not even ask for access.
	if rec := e.do(ana, http.MethodPost, "/api/v1/access-requests", `{"entitlement_id": "x"}`); rec.Code != http.StatusForbidden {
		t.Errorf("suspended ana asks for access = %d, want 403", rec.Code)
	}
	if got := decode[struct{ Responses []itdr.Response }](t, e.do(auditor, http.MethodGet, resp, "")); len(got.Responses) != 1 {
		t.Errorf("responses = %+v", got.Responses)
	}

	// Closing: an alert about an admin is for another admin to close.
	e.exec(`UPDATE threat_alerts SET identity_id = $1, subject = $2 WHERE id = $3`, admin, admin, alert.ID)
	closeURL := "/api/v1/threats/alerts/" + alert.ID + "/close"
	if rec := e.do(admin, http.MethodPost, closeURL, `{"status": "dismissed", "note": "it was me"}`); rec.Code != http.StatusForbidden {
		t.Errorf("admin closes an alert about themselves = %d", rec.Code)
	}
	if rec := e.do(auditor, http.MethodPost, closeURL, `{"status": "resolved"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor closes = %d", rec.Code)
	}
	closed := decode[itdr.Alert](t, e.do(other, http.MethodPost, closeURL, `{"status": "resolved", "note": "reset"}`))
	if closed.Status != itdr.Resolved || closed.ClosedBy != "identity:"+other {
		t.Errorf("closed = %+v", closed)
	}
	if rec := e.do(other, http.MethodGet, "/api/v1/threats/alerts/"+alert.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("get alert = %d", rec.Code)
	}
	if rec := e.do(other, http.MethodGet, "/api/v1/threats/alerts/nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown alert = %d", rec.Code)
	}
}

func TestThreatsInInboxAndHome(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	other := e.identity("other", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	e.exec(`INSERT INTO threat_alerts (tenant_id, detection, severity, identity_id, subject, summary, first_seen_at, last_seen_at) VALUES
		($1, 'mfa_fatigue', 'high', $2, $3, 'about admin', now(), now()),
		($1, 'password_spray', 'critical', NULL, 'ip:1.2.3.4', 'spray', now(), now()),
		($1, 'new_country', 'medium', NULL, 'ip:5.6.7.8', 'medium one', now(), now())`, e.tenant, admin, admin)

	type inboxOut struct {
		Threats []threatTask
		Total   int
	}
	in := decode[inboxOut](t, e.do(admin, http.MethodGet, "/api/v1/me/inbox", ""))
	if len(in.Threats) != 1 || in.Threats[0].Summary != "spray" || in.Threats[0].Title != "Password spray" || in.Total != 1 {
		t.Errorf("admin's inbox = %+v, want only the spray", in)
	}
	if in := decode[inboxOut](t, e.do(other, http.MethodGet, "/api/v1/me/inbox", "")); len(in.Threats) != 2 {
		t.Errorf("other admin's inbox = %+v, want both serious ones", in)
	}
	if in := decode[inboxOut](t, e.do(auditor, http.MethodGet, "/api/v1/me/inbox", "")); len(in.Threats) != 0 {
		t.Errorf("auditor's inbox = %+v, want no tasks", in)
	}
	type homeOut struct {
		Security *securityHome
	}
	if h := decode[homeOut](t, e.do(auditor, http.MethodGet, "/api/v1/me/home", "")); h.Security == nil || h.Security.OpenAlerts["critical"] != 1 || h.Security.OpenAlerts["medium"] != 1 {
		t.Errorf("auditor's home = %+v", h.Security)
	}
	ana := e.identity("ana", "")
	if h := decode[homeOut](t, e.do(ana, http.MethodGet, "/api/v1/me/home", "")); h.Security != nil {
		t.Errorf("employee's home has security: %+v", h.Security)
	}

	// With threat detection off, it is out of the inbox and the home page.
	if rec := e.do(admin, http.MethodPut, "/api/v1/modules/itdr", `{"enabled": false}`); rec.Code != http.StatusOK {
		t.Fatalf("turn off = %d %s", rec.Code, rec.Body)
	}
	if in := decode[inboxOut](t, e.do(other, http.MethodGet, "/api/v1/me/inbox", "")); len(in.Threats) != 0 {
		t.Errorf("inbox with the module off = %+v", in)
	}
	if h := decode[homeOut](t, e.do(auditor, http.MethodGet, "/api/v1/me/home", "")); h.Security != nil {
		t.Errorf("home with the module off = %+v", h.Security)
	}
}
