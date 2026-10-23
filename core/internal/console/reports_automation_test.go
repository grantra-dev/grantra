package console

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/reports"
)

// Scheduled packs are made once per period for admins and auditors, kept
// to download and verify, and a pack that cannot be made says why.
func TestReportSchedules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	luis := e.identity("Luis", "")

	save := func(as, method, path, body string, want int) reports.Schedule {
		t.Helper()
		rec := e.do(as, method, path, body)
		if rec.Code != want {
			t.Fatalf("%s %s %s = %d %s", method, path, body, rec.Code, rec.Body)
		}
		if want >= 300 {
			return reports.Schedule{}
		}
		return decode[reports.Schedule](t, rec)
	}
	schedule := func(fields string, recipients ...string) string {
		return fmt.Sprintf(`{%s, "recipients": ["%s"], "enabled": true}`, fields, joinIDs(recipients))
	}
	for _, body := range []string{
		schedule(`"name": "SOC 2", "framework": "soc2", "months": 3`, luis),                                   // not an admin or auditor
		schedule(`"name": "SOC 2", "framework": "soc2", "report": "leavers", "months": 3`, auditor),           // both
		schedule(`"name": "SOC 2", "framework": "soc2", "months": 3, "application_id": "`+admin+`"`, auditor), // framework and application
		schedule(`"name": "SOC 2", "framework": "soc2", "months": 13`, auditor),                               // too long
		schedule(`"name": "SOC 2", "framework": "nope", "months": 3`, auditor),                                // no framework
		schedule(`"name": "Admin", "report": "admin", "months": 1, "application_id": "`+admin+`"`, auditor),   // cannot narrow
		`{"name": "SOC 2", "framework": "soc2", "months": 3, "recipients": [], "enabled": true}`,              // nobody
	} {
		save(admin, http.MethodPost, "/api/v1/report-schedules", body, http.StatusBadRequest)
	}
	save(auditor, http.MethodPost, "/api/v1/report-schedules", schedule(`"name": "SOC 2", "framework": "soc2", "months": 3`, auditor), http.StatusForbidden)

	now := time.Now().UTC()
	soc2 := save(admin, http.MethodPost, "/api/v1/report-schedules",
		schedule(`"name": "SOC 2 quarterly", "framework": "soc2", "months": 3`, auditor, admin, auditor), http.StatusCreated)
	if len(soc2.Recipients) != 2 || !soc2.Recipients[0].Allowed || soc2.Last != nil {
		t.Errorf("soc2 = %+v", soc2)
	}
	lastFrom, lastTo := reports.LastPeriod(now, 3, 1)
	if want := lastTo.AddDate(0, 0, 1).Format(time.DateOnly); soc2.NextFrom != want {
		t.Errorf("next period starts %s, want %s", soc2.NextFrom, want)
	}
	leavers := save(admin, http.MethodPost, "/api/v1/report-schedules",
		schedule(`"name": "Leavers", "report": "leavers", "months": 1`, auditor), http.StatusCreated)
	off := save(admin, http.MethodPost, "/api/v1/report-schedules",
		`{"name": "Off", "report": "access", "months": 1, "recipients": ["`+auditor+`"], "enabled": false}`, http.StatusCreated)
	if off.Enabled {
		t.Error("a schedule saved off is on")
	}

	sent, err := reports.SendScheduled(ctx, e.pool, e.tenant, now)
	if err != nil || sent.Ready != 2 || sent.Failed != 0 {
		t.Fatalf("first pass = %+v, %v", sent, err)
	}
	// Once per period.
	if sent, err := reports.SendScheduled(ctx, e.pool, e.tenant, now); err != nil || sent.Ready+sent.Failed != 0 {
		t.Errorf("second pass = %+v, %v", sent, err)
	}
	if n := e.count(`SELECT count(*) FROM notification_outbox WHERE kind = 'report_ready'`); n != 3 {
		t.Errorf("report_ready notifications = %d, want 3", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'report.generated' AND actor = $1 AND details->>'schedule_id' = $2
		AND details->>'from' = $3`, reports.ScheduleActor, soc2.ID, lastFrom.Format(time.DateOnly)); n != 1 {
		t.Errorf("soc2 pack audited %d times", n)
	}

	list := decode[struct{ Schedules []reports.Schedule }](t, e.do(auditor, http.MethodGet, "/api/v1/report-schedules", "")).Schedules
	if len(list) != 3 {
		t.Fatalf("schedules = %+v", list)
	}
	i := slices.IndexFunc(list, func(s reports.Schedule) bool { return s.ID == soc2.ID })
	last := list[i].Last
	if last == nil || last.Status != "ready" || last.From != lastFrom.Format(time.DateOnly) || last.Recipients != 2 ||
		last.FileName != "grantra-soc2-"+lastFrom.Format(time.DateOnly)+"-"+lastTo.Format(time.DateOnly)+".zip" || last.Bytes == 0 {
		t.Errorf("soc2 last pack = %+v", last)
	}

	// Recipients download the pack, the download is audited, and it verifies.
	rec := e.do(auditor, http.MethodGet, "/api/v1/report-deliveries/"+last.ID+"/file", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" {
		t.Fatalf("download = %d %s", rec.Code, rec.Header())
	}
	if v := decode[reports.Verification](t, e.do(auditor, http.MethodPost, "/api/v1/report-verifications", rec.Body.String())); !v.Valid || v.GeneratedBy != reports.ScheduleActor {
		t.Errorf("verify = %+v", v)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'report.downloaded' AND target_id = $1 AND actor = 'identity:' || $2`, last.ID, auditor); n != 1 {
		t.Errorf("download audited %d times", n)
	}
	if rec := e.do(luis, http.MethodGet, "/api/v1/report-deliveries/"+last.ID+"/file", ""); rec.Code != http.StatusForbidden {
		t.Errorf("someone else downloads = %d", rec.Code)
	}
	deliveries := decode[struct{ Deliveries []reports.Delivery }](t,
		e.do(auditor, http.MethodGet, "/api/v1/report-deliveries?schedule="+leavers.ID, "")).Deliveries
	if len(deliveries) != 1 || deliveries[0].Title != "Leavers and orphan accounts" || deliveries[0].Recipients != 1 {
		t.Errorf("leavers deliveries = %+v", deliveries)
	}

	// A framework with no controls cannot make a pack: it fails once, says
	// why, and is not tried again.
	empty := decode[reports.Framework](t, e.do(admin, http.MethodPost, "/api/v1/report-frameworks", `{"name": "Empty", "enabled": true, "controls": []}`))
	bad := save(admin, http.MethodPost, "/api/v1/report-schedules",
		schedule(`"name": "Empty", "framework": "`+empty.ID+`", "months": 12`, auditor), http.StatusCreated)
	if sent, err := reports.SendScheduled(ctx, e.pool, e.tenant, now); err != nil || sent.Failed != 1 || sent.Ready != 0 {
		t.Errorf("pass with a bad schedule = %+v, %v", sent, err)
	}
	if sent, _ := reports.SendScheduled(ctx, e.pool, e.tenant, now); sent.Failed != 0 {
		t.Error("a failed pack was tried again")
	}
	failed := decode[struct{ Deliveries []reports.Delivery }](t, e.do(auditor, http.MethodGet, "/api/v1/report-deliveries?schedule="+bad.ID, "")).Deliveries
	if len(failed) != 1 || failed[0].Status != "failed" || failed[0].Error != "Empty has no controls yet" {
		t.Errorf("failed deliveries = %+v", failed)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/report-deliveries/"+failed[0].ID+"/file", ""); rec.Code != http.StatusNotFound {
		t.Errorf("download a failed pack = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM notification_outbox WHERE kind = 'report_ready' AND data->>'error' = 'Empty has no controls yet'`); n != 1 {
		t.Errorf("failure notifications = %d", n)
	}

	// Someone who loses the auditor role stays listed but gets nothing.
	e.exec(`DELETE FROM account_entitlements ae USING accounts a WHERE ae.account_id = a.id AND a.identity_id = $1`, auditor)
	got := save(admin, http.MethodPut, "/api/v1/report-schedules/"+soc2.ID,
		schedule(`"name": "SOC 2 quarterly", "framework": "soc2", "months": 3`, admin), http.StatusOK)
	if len(got.Recipients) != 1 || got.Recipients[0].ID != admin {
		t.Errorf("after the change = %+v", got.Recipients)
	}
	if rec := e.do(admin, http.MethodDelete, "/api/v1/report-schedules/"+leavers.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}
	// Its packs stay.
	if n := e.count(`SELECT count(*) FROM report_deliveries WHERE schedule_id IS NULL AND title = 'Leavers and orphan accounts'`); n != 1 {
		t.Errorf("packs of a deleted schedule = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action IN ('report_schedule.saved', 'report_schedule.deleted')`); n != 6 {
		t.Errorf("schedule changes audited = %d, want 6", n)
	}
}

func joinIDs(ids []string) string {
	out := ""
	for i, id := range ids {
		if i > 0 {
			out += `", "`
		}
		out += id
	}
	return out
}

// An external auditor reads reports until their last day, gets no
// birthright access, and becomes a leaver when an admin ends it early.
func TestExternalAuditors(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	e.identity("Luis", "")
	day := func(days int) string { return time.Now().UTC().AddDate(0, 0, days).Format(time.DateOnly) }
	invite := func(as, until, email string, want int) reports.ExternalAuditor {
		t.Helper()
		rec := e.do(as, http.MethodPost, "/api/v1/external-auditors",
			`{"name": "Ines Ruiz", "email": "`+email+`", "company": "Audit & Co", "until": "`+until+`", "login": true}`)
		if rec.Code != want {
			t.Fatalf("invite until %s as %s = %d %s", until, as, rec.Code, rec.Body)
		}
		if want >= 300 {
			return reports.ExternalAuditor{}
		}
		out := decode[struct {
			reports.ExternalAuditor
			LoginError string `json:"login_error"`
		}](t, rec)
		if out.LoginError == "" {
			t.Error("no Grantra Login, and no word about it")
		}
		return out.ExternalAuditor
	}
	invite(admin, day(-1), "ines@audit.example", http.StatusBadRequest)
	invite(admin, day(91), "ines@audit.example", http.StatusBadRequest)
	invite(admin, day(30), "not an email", http.StatusBadRequest)
	invite(admin, day(30), "Luis@example.com", http.StatusConflict)
	invite(auditor, day(30), "ines@audit.example", http.StatusForbidden)

	ines := invite(admin, day(30), "ines@audit.example", http.StatusCreated)
	if ines.Status != "active" || ines.Until != day(30) || ines.Company != "Audit & Co" || ines.SponsorID != admin || ines.Login != "" {
		t.Errorf("invited = %+v", ines)
	}
	roleEnds := func() string {
		t.Helper()
		var out string
		err := db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
			members, err := authz.Members(ctx, tx)
			for _, m := range members {
				if m.IdentityID == ines.ID && m.Role == authz.RoleAuditor && m.ExpiresAt != nil {
					out = m.ExpiresAt.UTC().Format(time.DateOnly)
				}
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := roleEnds(); got != day(31) {
		t.Errorf("auditor role ends %q, want the midnight after %s", got, day(30))
	}
	if rec := e.do(ines.ID, http.MethodGet, "/api/v1/reports", ""); rec.Code != http.StatusOK {
		t.Errorf("the external auditor reads reports = %d", rec.Code)
	}
	if rec := e.do(ines.ID, http.MethodGet, "/api/v1/external-auditors", ""); rec.Code != http.StatusOK {
		t.Errorf("the external auditor sees who else is invited = %d", rec.Code)
	}

	// A rule for everyone does not give them anything.
	err := db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
		_, everyone, err := access.Preview(ctx, tx, access.Conditions{}, 100)
		if slices.ContainsFunc(everyone, func(m access.Match) bool { return m.ID == ines.ID }) {
			t.Error("a rule for everyone matches an external auditor")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	// Their last day moves, within the limit.
	if rec := e.do(admin, http.MethodPut, "/api/v1/external-auditors/"+ines.ID, `{"until": "`+day(120)+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("past the limit = %d", rec.Code)
	}
	moved := decode[reports.ExternalAuditor](t, e.do(admin, http.MethodPut, "/api/v1/external-auditors/"+ines.ID, `{"until": "`+day(60)+`"}`))
	if moved.Until != day(60) || roleEnds() != day(61) {
		t.Errorf("moved = %+v, role ends %s", moved, roleEnds())
	}

	// Ending it early makes them a leaver now.
	if rec := e.do(admin, http.MethodDelete, "/api/v1/external-auditors/"+ines.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("end = %d %s", rec.Code, rec.Body)
	}
	list := decode[struct{ Auditors []reports.ExternalAuditor }](t, e.do(admin, http.MethodGet, "/api/v1/external-auditors", "")).Auditors
	if len(list) != 1 || list[0].Status != "terminated" || list[0].Until != day(0) {
		t.Errorf("after ending = %+v", list)
	}
	if roleEnds() != "" || e.count(`SELECT count(*) FROM lifecycle_events WHERE identity_id = $1 AND kind = 'leaver'`, ines.ID) != 1 {
		t.Error("ended without a leaver event, or with the role")
	}
	if rec := e.do(ines.ID, http.MethodGet, "/api/v1/reports", ""); rec.Code == http.StatusOK {
		t.Error("an ended external auditor still reads reports")
	}
	if rec := e.do(admin, http.MethodPut, "/api/v1/external-auditors/"+ines.ID, `{"until": "`+day(10)+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("extend after the end = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action IN ('external_auditor.invited', 'external_auditor.extended', 'external_auditor.ended')
		AND target_id = $1`, ines.ID); n != 3 {
		t.Errorf("external auditor changes audited = %d", n)
	}
	// Ending twice does nothing.
	if rec := e.do(admin, http.MethodDelete, "/api/v1/external-auditors/"+ines.ID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("end again = %d", rec.Code)
	}

	// Invited again, the same person comes back; inviting someone still
	// invited, or anyone else in Grantra, is refused.
	again := invite(admin, day(10), "INES@audit.example", http.StatusCreated)
	if again.ID != ines.ID || again.Status != "active" || again.Until != day(10) || roleEnds() != day(11) {
		t.Errorf("invited again = %+v, role ends %s", again, roleEnds())
	}
	invite(admin, day(10), "ines@audit.example", http.StatusConflict)
}

// The history of who held what answers for a past date.
func TestAccessHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("Ana", "")
	bea := e.identity("Bea", "")
	erp := e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'ERP', 'csv') RETURNING id`, e.tenant)
	pay := e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name, risk) VALUES ($1, $2, 'pay', 'Payments', 'high') RETURNING id`, e.tenant, erp)
	acct := e.id(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id) VALUES ($1, $2, $3, 'ana') RETURNING id`, e.tenant, erp, ana)
	e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id, granted_at) VALUES ($1, $2, $3, now() - interval '10 days')`,
		e.tenant, acct, pay)

	record := func(started, ended int) {
		t.Helper()
		h, err := reports.RecordHistory(ctx, e.pool, e.tenant)
		if err != nil || h.Started != started || h.Ended != ended {
			t.Fatalf("history = %+v, %v; want %d started and %d ended", h, err, started, ended)
		}
	}
	// The auditor's platform role counts too.
	record(2, 0)
	record(0, 0)
	e.exec(`DELETE FROM account_entitlements WHERE account_id = $1`, acct)
	record(0, 1)

	day := func(days int) string { return time.Now().UTC().AddDate(0, 0, days).Format(time.DateOnly) }
	run := func(to string) reports.Result {
		t.Helper()
		rec := e.do(auditor, http.MethodGet, "/api/v1/reports/access_history?application="+erp+"&from="+day(-30)+"&to="+to, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("access_history = %d %s", rec.Code, rec.Body)
		}
		return decode[reports.Result](t, rec)
	}
	// Ana held Payments at the end of yesterday, not at the end of today.
	yesterday := run(day(-1))
	if held := table(t, yesterday, "held"); len(held) != 1 || held[0]["person"] != "Ana" || held[0]["entitlement"] != "Payments" || held[0]["until"] == "" {
		t.Errorf("held yesterday = %v", held)
	}
	if f := facts(yesterday); f["Started in the period"] != 1 || f["Ended in the period"] != 0 || f["High or critical risk"] != 1 {
		t.Errorf("facts yesterday = %v", f)
	}
	today := run(day(0))
	if held := table(t, today, "held"); len(held) != 0 {
		t.Errorf("held today = %v", held)
	}
	if f := facts(today); f["Ended in the period"] != 1 {
		t.Errorf("facts today = %v", f)
	}
	if rec := table(t, today, "recorded"); len(rec) != 1 || rec[0]["since"] == "" {
		t.Errorf("recorded since = %v", rec)
	}

	// Given back, it starts again after it ended; an account that changes
	// hands ends for one person and starts for the other.
	e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id, granted_at) VALUES ($1, $2, $3, now() - interval '1 year')`,
		e.tenant, acct, pay)
	record(1, 0)
	if n := e.count(`SELECT count(*) FROM access_history a JOIN access_history b ON a.account_id = b.account_id
		WHERE a.held_until IS NOT NULL AND b.held_until IS NULL AND b.held_from >= a.held_until`); n != 1 {
		t.Error("given back, it starts before it last ended")
	}
	e.exec(`UPDATE accounts SET identity_id = $2 WHERE id = $1`, acct, bea)
	record(1, 1)
}
