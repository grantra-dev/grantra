package itdr

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	e := &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "itdr")}
	if err := authz.EnsureBuiltinApp(context.Background(), pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) tx(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(context.Background(), e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), sql, args...)
		return err
	})
}

func (e *env) identity(name string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email) VALUES ($1, $2, $3) RETURNING id`,
			e.tenant, name, strings.ToLower(name)+"@example.com").Scan(&id)
	})
	return id
}

func (e *env) admin(name string) string {
	e.t.Helper()
	id := e.identity(name)
	e.tx(func(tx pgx.Tx) error {
		return authz.Grant(context.Background(), tx, e.tenant, authz.RoleAdmin, id, nil)
	})
	return id
}

var seq int

// send stores sign-ins as a script would, under the source "okta".
func (e *env) send(events ...SignIn) int {
	e.t.Helper()
	for i := range events {
		if events[i].ID == "" {
			seq++
			events[i].ID = fmt.Sprintf("evt-%d", seq)
		}
	}
	var n int
	e.tx(func(tx pgx.Tx) (err error) {
		n, err = Receive(context.Background(), tx, e.tenant, "test", "okta", events)
		return err
	})
	return n
}

func (e *env) scan() ScanResult {
	e.t.Helper()
	res, err := Scanner{Pool: e.pool, TenantID: e.tenant, Secrets: noSecrets}.Scan(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

func noSecrets(string) (string, error) { return "", errors.New("no secrets in tests") }

func (e *env) alerts(f AlertFilter) []Alert {
	e.t.Helper()
	if f.Limit == 0 {
		f.Limit = 100
	}
	var out []Alert
	e.tx(func(tx pgx.Tx) (err error) {
		out, err = Alerts(context.Background(), tx, f)
		return err
	})
	return out
}

func (e *env) only(detection string) Alert {
	e.t.Helper()
	list := e.alerts(AlertFilter{Detection: detection})
	if len(list) != 1 {
		e.t.Fatalf("%s alerts = %d, want 1: %+v", detection, len(list), list)
	}
	return list[0]
}

func (e *env) none(detection string) {
	e.t.Helper()
	if list := e.alerts(AlertFilter{Detection: detection}); len(list) != 0 {
		e.t.Fatalf("%s alerts = %+v, want none", detection, list)
	}
}

func (e *env) set(detection string, in SettingInput) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := SaveSetting(context.Background(), tx, e.tenant, "test", detection, in)
		return err
	})
}

func at(ago time.Duration) time.Time { return time.Now().Add(-ago).UTC() }

func ptr(f float64) *float64 { return &f }

func TestReceiveChecksAndMatchesPeople(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana")
	ctx := context.Background()

	bad := []struct {
		source string
		ev     SignIn
		want   string
	}{
		{"Okta!", SignIn{ID: "1", Time: at(0), User: "x", Outcome: Success}, "source"},
		{SourceGrantraLogin, SignIn{ID: "1", Time: at(0), User: "x", Outcome: Success}, "read from Grantra Login"},
		{"okta", SignIn{ID: "1", Time: at(0), User: "x", Outcome: "ok"}, "outcome"},
		{"okta", SignIn{ID: "1", Time: at(-time.Hour), User: "x", Outcome: Success}, "future"},
		{"okta", SignIn{ID: "1", User: "x", Outcome: Success}, "time is required"},
		{"okta", SignIn{ID: "1", Time: at(0), Outcome: Success}, "user is required"},
		{"okta", SignIn{ID: "1", Time: at(0), User: "x", Outcome: Success, IP: "300.1.1.1"}, "not an IP"},
		{"okta", SignIn{ID: "1", Time: at(0), User: "x", Outcome: Success, Country: "Spain"}, "two-letter"},
		{"okta", SignIn{ID: "1", Time: at(0), User: "x", Outcome: Success, Latitude: ptr(1)}, "both latitude"},
		{"okta", SignIn{ID: "1", Time: at(0), User: "x", Outcome: Success, Latitude: ptr(91), Longitude: ptr(0)}, "between -90"},
	}
	for _, b := range bad {
		err := db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
			_, err := Receive(ctx, tx, e.tenant, "test", b.source, []SignIn{b.ev})
			return err
		})
		var inv *apperr.Invalid
		if !errors.As(err, &inv) || !strings.Contains(err.Error(), b.want) {
			t.Errorf("Receive(%s, %+v) = %v, want an error about %q", b.source, b.ev, err, b.want)
		}
	}

	// By email in any case, or by an account's username; "es" becomes "ES".
	app := ""
	e.tx(func(tx pgx.Tx) error {
		a, err := catalog.CreateApplication(ctx, tx, e.tenant, "Okta", "")
		app = a.ID
		return err
	})
	e.exec(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username) VALUES ($1, $2, $3, 'u1', 'ana.okta')`,
		e.tenant, app, ana)
	first := []SignIn{
		{ID: "a", Time: at(time.Minute), User: "ANA@example.com", Outcome: Success, Country: "es"},
		{ID: "b", Time: at(time.Minute), User: "ana.okta", Outcome: Failure},
		{ID: "c", Time: at(time.Minute), User: "nobody", Outcome: Failure},
	}
	if n := e.send(first...); n != 3 {
		t.Fatalf("new = %d, want 3", n)
	}
	// Sending the same window again stores nothing twice.
	if n := e.send(first[0], SignIn{ID: "d", Time: at(0), User: "ana", Outcome: Success}); n != 1 {
		t.Fatalf("new on a resend = %d, want 1", n)
	}
	var events []Event
	e.tx(func(tx pgx.Tx) (err error) {
		events, err = Events(ctx, tx, EventFilter{Limit: 10})
		return err
	})
	who := map[string]string{}
	for _, ev := range events {
		who[ev.Username] = ev.IdentityID
		if ev.Username == "ANA@example.com" && ev.Country != "ES" {
			t.Errorf("country = %q, want ES", ev.Country)
		}
	}
	if who["ANA@example.com"] != ana || who["ana.okta"] != ana || who["nobody"] != "" || who["ana"] != "" {
		t.Errorf("matched = %v, want both of Ana's names on her and the others on nobody", who)
	}
	// Paging goes on after the last one.
	var page []Event
	e.tx(func(tx pgx.Tx) (err error) {
		page, err = Events(ctx, tx, EventFilter{Before: events[1].ID, Limit: 10})
		return err
	})
	if len(page) != len(events)-2 {
		t.Errorf("page after the second = %d events, want %d", len(page), len(events)-2)
	}
}

func TestBruteForceThenSuccess(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana")
	var evs []SignIn
	for i := range 9 {
		evs = append(evs, SignIn{Time: at(time.Duration(10-i) * time.Minute), User: "ana@example.com", Outcome: Failure, IP: "203.0.113.5"})
	}
	e.send(evs...)
	e.scan()
	e.none(BruteForce)

	e.send(SignIn{Time: at(time.Minute), User: "ana@example.com", Outcome: Failure, IP: "203.0.113.5"})
	res := e.scan()
	if res.Raised != 1 {
		t.Errorf("raised = %d, want 1", res.Raised)
	}
	a := e.only(BruteForce)
	if a.Severity != "medium" || a.IdentityID != ana || !strings.Contains(a.Summary, "10 wrong passwords") {
		t.Errorf("alert = %+v, want medium, about Ana, 10 wrong passwords", a)
	}
	// A sign-in right after: the password was guessed.
	e.send(SignIn{Time: at(0), User: "ana@example.com", Outcome: Success, IP: "203.0.113.5"})
	if res := e.scan(); res.Escalated != 1 {
		t.Errorf("escalated = %d, want 1", res.Escalated)
	}
	a = e.only(BruteForce)
	if a.Severity != "high" || !strings.Contains(a.Summary, "signed in right after") || a.Occurrences < 2 {
		t.Errorf("alert after the success = %+v, want high, signed in right after, seen twice", a)
	}
	// Scanning again looks at nothing new.
	if res := e.scan(); res.Evaluated != 0 || res.Raised+res.Escalated != 0 {
		t.Errorf("second scan = %+v, want nothing", res)
	}
}

func TestPasswordSpray(t *testing.T) {
	e := newEnv(t)
	var evs []SignIn
	for i := range 10 {
		evs = append(evs, SignIn{Time: at(time.Duration(10-i) * time.Minute), User: fmt.Sprintf("user%d", i), Outcome: Failure, IP: "198.51.100.9"})
	}
	// Spread over too long, the same address is not a spray.
	for i := range 10 {
		evs = append(evs, SignIn{Time: at(time.Duration(i+1) * time.Hour), User: fmt.Sprintf("slow%d", i), Outcome: Failure, IP: "198.51.100.10"})
	}
	e.send(evs...)
	e.scan()
	a := e.only(PasswordSpray)
	if a.Subject != "ip:198.51.100.9" || a.IdentityID != "" || a.Severity != "high" || !strings.Contains(a.Summary, "10 people") {
		t.Errorf("alert = %+v, want high, about the address, 10 people", a)
	}
}

func TestMFAFatigue(t *testing.T) {
	e := newEnv(t)
	e.identity("Luis")
	var evs []SignIn
	for i := range 5 {
		evs = append(evs, SignIn{Time: at(time.Duration(6-i) * time.Minute), User: "luis@example.com", Outcome: MFAFailure})
	}
	e.send(evs...)
	e.scan()
	if a := e.only(MFAFatigue); a.Severity != "high" {
		t.Errorf("severity = %s, want high", a.Severity)
	}
	e.send(SignIn{Time: at(0), User: "luis@example.com", Outcome: Success})
	e.scan()
	if a := e.only(MFAFatigue); a.Severity != "critical" || !strings.Contains(a.Summary, "signed in after 5 refused") {
		t.Errorf("after the success = %+v, want critical", a)
	}
}

func TestImpossibleTravel(t *testing.T) {
	e := newEnv(t)
	e.identity("Ana")
	e.identity("Luis")
	madrid := SignIn{User: "ana@example.com", Outcome: Success, Latitude: ptr(40.42), Longitude: ptr(-3.70), Country: "ES"}
	newYork := SignIn{User: "ana@example.com", Outcome: Success, Latitude: ptr(40.71), Longitude: ptr(-74.0), Country: "US"}
	madrid.Time, newYork.Time = at(2*time.Hour), at(time.Hour)
	// Madrid to Barcelona in an hour and a half could be a plane.
	luisMadrid, luisBarcelona := madrid, SignIn{User: "luis@example.com", Outcome: Success, Latitude: ptr(41.39), Longitude: ptr(2.17)}
	luisMadrid.User, luisBarcelona.Time = "luis@example.com", at(30*time.Minute)
	e.send(madrid, newYork, luisMadrid, luisBarcelona)
	e.scan()
	a := e.only(ImpossibleTravel)
	if !strings.Contains(a.Summary, "Ana signed in 5,7") || a.Evidence["previous_country"] != "ES" {
		t.Errorf("alert = %+v, want Ana, about 5,700 km from Spain", a)
	}
}

func TestNewCountry(t *testing.T) {
	e := newEnv(t)
	e.identity("Ana")
	e.identity("Luis")
	var evs []SignIn
	for i := range 5 {
		evs = append(evs, SignIn{Time: at(time.Duration(10-i) * 24 * time.Hour), User: "ana@example.com", Outcome: Success, Country: "ES"})
	}
	// Luis has too little history to tell.
	evs = append(evs,
		SignIn{Time: at(48 * time.Hour), User: "luis@example.com", Outcome: Success, Country: "ES"},
		SignIn{Time: at(time.Hour), User: "luis@example.com", Outcome: Success, Country: "FR"},
		SignIn{Time: at(time.Hour), User: "ana@example.com", Outcome: Success, Country: "FR"},
		// Back home is nothing new.
		SignIn{Time: at(0), User: "ana@example.com", Outcome: Success, Country: "ES"})
	e.send(evs...)
	e.scan()
	a := e.only(NewCountry)
	if !strings.HasPrefix(a.Summary, "Ana signed in from FR") {
		t.Errorf("alert = %+v, want Ana from FR", a)
	}
}

func TestDormantAccount(t *testing.T) {
	e := newEnv(t)
	e.identity("Ana")
	e.identity("Luis")
	e.send(
		SignIn{Time: at(100 * 24 * time.Hour), User: "ana@example.com", Outcome: Success},
		SignIn{Time: at(0), User: "ana@example.com", Outcome: Success},
		SignIn{Time: at(30 * 24 * time.Hour), User: "luis@example.com", Outcome: Success},
		SignIn{Time: at(0), User: "luis@example.com", Outcome: Success})
	e.scan()
	if a := e.only(DormantAccount); !strings.Contains(a.Summary, "Ana signed in after 100 days") {
		t.Errorf("alert = %+v, want Ana after 100 days", a)
	}
}

func TestLeaverSignIn(t *testing.T) {
	e := newEnv(t)
	marta := e.identity("Marta")
	e.exec(`UPDATE identities SET status = 'terminated', end_date = current_date - 2 WHERE id = $1`, marta)
	e.send(
		// Before her last day: fine, even if it arrives late.
		SignIn{Time: at(4 * 24 * time.Hour), User: "marta@example.com", Outcome: Success},
		SignIn{Time: at(time.Hour), User: "marta@example.com", Outcome: Success},
		// Failing to sign in is the account doing its job.
		SignIn{Time: at(time.Minute), User: "marta@example.com", Outcome: Failure})
	e.scan()
	a := e.only(LeaverSignIn)
	if a.Severity != "critical" || a.IdentityID != marta || a.Occurrences != 1 || !strings.Contains(a.Summary, "they left the company") {
		t.Errorf("alert = %+v, want one critical sign-in after she left", a)
	}
}

func TestOutsideGrantraAndNewAdmins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	ana, luis := e.identity("Ana"), e.identity("Luis")
	var app string
	ents := map[string]string{}
	e.tx(func(tx pgx.Tx) error {
		a, err := catalog.CreateApplication(ctx, tx, e.tenant, "ERP", "")
		if err != nil {
			return err
		}
		app = a.ID
		for name, risk := range map[string]string{"payments-admin": "critical", "reader": "low", "ops": "high"} {
			en, err := catalog.CreateEntitlement(ctx, tx, e.tenant, app, catalog.EntitlementInput{NativeID: name, Name: name, Risk: risk})
			if err != nil {
				return err
			}
			ents[name] = en.ID
		}
		return nil
	})
	account := func(identity, native string) string {
		var id string
		e.tx(func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username, created_at)
				VALUES ($1, $2, $3::uuid, $4, $4, now() - interval '1 day') RETURNING id`, e.tenant, app, db.Nullable(identity), native).Scan(&id)
		})
		return id
	}
	anaAcc, luisAcc, orphan := account(ana, "ana"), account(luis, "luis"), account("", "svc-old")
	// What was there before the first scan is for reviews, not alerts.
	e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3)`, e.tenant, anaAcc, ents["ops"])
	e.scan()
	e.none(OutsideGrantra)

	// Luis got ops through Grantra; Ana got payments-admin in the ERP
	// itself; reader is low risk; the orphan got ops.
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
		e.tenant, luis, ents["ops"])
	for acc, ent := range map[string]string{anaAcc: "payments-admin", luisAcc: "ops", orphan: "ops"} {
		e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3)`, e.tenant, acc, ents[ent])
	}
	e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3)`, e.tenant, luisAcc, ents["reader"])
	e.tx(func(tx pgx.Tx) error {
		if err := authz.Grant(ctx, tx, e.tenant, authz.RoleAdmin, luis, nil); err != nil {
			return err
		}
		return db.Audit(ctx, tx, e.tenant, "identity:someone", "platform_role.granted", "identity", luis, map[string]any{"role": authz.RoleAdmin})
	})
	e.scan()
	list := e.alerts(AlertFilter{Detection: OutsideGrantra})
	got := map[string]string{}
	for _, a := range list {
		got[a.Subject] = a.Severity
	}
	if len(list) != 2 || got[ana] != "critical" || got["account:"+orphan] != "high" {
		t.Errorf("outside Grantra = %v, want Ana's critical one and the orphan's", got)
	}
	if a := e.only(NewPlatformAdmin); a.IdentityID != luis || a.Evidence["granted_by"] != "identity:someone" {
		t.Errorf("new admin alert = %+v, want Luis granted by someone", a)
	}
	// Nothing new, nothing more.
	if res := e.scan(); res.Raised+res.Escalated != 0 {
		t.Errorf("rescan raised %+v", res)
	}
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	bad := []struct {
		id string
		in SettingInput
	}{
		{BruteForce, SettingInput{AutoResponse: Suspend}},
		{PasswordSpray, SettingInput{AutoResponse: RevokeSessions}},
		{BruteForce, SettingInput{Values: map[string]int{"failures": 1}}},
		{BruteForce, SettingInput{Values: map[string]int{"nope": 5}}},
		{BruteForce, SettingInput{Severity: "urgent"}},
	}
	for _, b := range bad {
		err := db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
			_, err := SaveSetting(ctx, tx, e.tenant, "test", b.id, b.in)
			return err
		})
		if !errors.As(err, new(*apperr.Invalid)) {
			t.Errorf("SaveSetting(%s, %+v) = %v, want invalid", b.id, b.in, err)
		}
	}
	err := db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := SaveSetting(ctx, tx, e.tenant, "test", "nope", SettingInput{})
		return err
	})
	if !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("unknown detection = %v, want not found", err)
	}

	// Lower the threshold, then turn the detection off.
	e.identity("Ana")
	e.set(BruteForce, SettingInput{Values: map[string]int{"failures": 3}, Severity: "low"})
	var list []Setting
	e.tx(func(tx pgx.Tx) (err error) {
		list, err = Settings(ctx, tx)
		return err
	})
	for _, s := range list {
		if s.ID == BruteForce && (s.Values["failures"] != 3 || s.Values["window_minutes"] != 15 || s.Severity != "low" || s.UpdatedBy != "test") {
			t.Errorf("brute force setting = %+v", s)
		}
	}
	fail := func(n int) {
		var evs []SignIn
		for i := range n {
			evs = append(evs, SignIn{Time: at(time.Duration(n-i) * time.Second), User: "ana@example.com", Outcome: Failure})
		}
		e.send(evs...)
		e.scan()
	}
	fail(3)
	if a := e.only(BruteForce); a.Severity != "low" {
		t.Errorf("severity = %s, want low", a.Severity)
	}
	e.exec(`DELETE FROM threat_alerts`)
	e.set(BruteForce, SettingInput{Enabled: new(false)})
	fail(5)
	e.none(BruteForce)
}

func TestCloseAlert(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.identity("Ana")
	burst := func() {
		var evs []SignIn
		for i := range 10 {
			evs = append(evs, SignIn{Time: at(time.Duration(10-i) * time.Second), User: "ana@example.com", Outcome: Failure})
		}
		e.send(evs...)
		e.scan()
	}
	burst()
	a := e.only(BruteForce)
	closeAlert := func(status, note string) error {
		return db.InTenant(ctx, e.pool, e.tenant, func(tx pgx.Tx) error {
			_, err := Close(ctx, tx, e.tenant, "identity:boss", a.ID, status, note)
			return err
		})
	}
	if err := closeAlert(Dismissed, " "); !errors.As(err, new(*apperr.Invalid)) {
		t.Errorf("dismiss without a note = %v, want invalid", err)
	}
	if err := closeAlert("open", "x"); !errors.As(err, new(*apperr.Invalid)) {
		t.Errorf("close to open = %v, want invalid", err)
	}
	if err := closeAlert(Dismissed, "Ana forgot her new password"); err != nil {
		t.Fatal(err)
	}
	if err := closeAlert(Resolved, ""); !errors.As(err, new(*apperr.Conflict)) {
		t.Errorf("closing twice = %v, want conflict", err)
	}
	// The next burst is a new alert.
	burst()
	if open := e.alerts(AlertFilter{Status: Open}); len(open) != 1 || open[0].ID == a.ID {
		t.Errorf("open after a new burst = %+v, want a new alert", open)
	}
	if closed := e.alerts(AlertFilter{Status: Dismissed}); len(closed) != 1 || closed[0].CloseNote != "Ana forgot her new password" || closed[0].ClosedBy != "identity:boss" {
		t.Errorf("dismissed = %+v", closed)
	}
}

func TestNotifiesAdminsButNotAboutThemselves(t *testing.T) {
	e := newEnv(t)
	boss, other := e.admin("Boss"), e.admin("Other")
	e.identity("Ana")
	var evs []SignIn
	for i := range 5 {
		evs = append(evs, SignIn{Time: at(time.Duration(5-i) * time.Second), User: "boss@example.com", Outcome: MFAFailure})
		evs = append(evs, SignIn{Time: at(time.Duration(5-i) * time.Second), User: "ana@example.com", Outcome: MFAFailure})
	}
	// Medium alerts do not send email.
	evs = append(evs, SignIn{Time: at(200 * 24 * time.Hour), User: "ana@example.com", Outcome: Success},
		SignIn{Time: at(0), User: "ana@example.com", Outcome: Success})
	e.send(evs...)
	e.scan()
	counts := map[string]float64{}
	e.tx(func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT identity_id::text, data FROM notification_outbox WHERE kind = 'threat_detected'`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var data map[string]any
			if err := rows.Scan(&id, &data); err != nil {
				return err
			}
			counts[id] = data["count"].(float64)
		}
		return rows.Err()
	})
	if counts[boss] != 1 || counts[other] != 2 || len(counts) != 2 {
		t.Errorf("threat emails = %v, want Boss told of Ana's only and Other of both", counts)
	}
}

func TestModuleOff(t *testing.T) {
	e := newEnv(t)
	e.identity("Ana")
	e.exec(`INSERT INTO tenant_modules (tenant_id, module, enabled, updated_by) VALUES ($1, $2, false, 'test')`, e.tenant, Module)
	var evs []SignIn
	for i := range 10 {
		evs = append(evs, SignIn{Time: at(time.Duration(10-i) * time.Second), User: "ana@example.com", Outcome: Failure})
	}
	e.send(evs...)
	if res := e.scan(); res.Evaluated != 0 {
		t.Errorf("scan with the module off = %+v, want nothing done", res)
	}
	e.none(BruteForce)
}

func TestRetention(t *testing.T) {
	e := newEnv(t)
	e.send(SignIn{Time: at(Retention + time.Hour), User: "x", Outcome: Failure}, SignIn{Time: at(0), User: "x", Outcome: Failure})
	if res := e.scan(); res.Deleted != 1 {
		t.Errorf("deleted = %d, want the old one", res.Deleted)
	}
}

func (e *env) respond(actor, identity string, in ResponseInput) (Response, error) {
	e.t.Helper()
	return Responder{Pool: e.pool, TenantID: e.tenant, Secrets: noSecrets}.Respond(context.Background(), actor, identity, in)
}

func (e *env) status(identity string) string {
	e.t.Helper()
	var s string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT status FROM identities WHERE id = $1`, identity).Scan(&s)
	})
	return s
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	e.tx(func(tx pgx.Tx) error { return tx.QueryRow(context.Background(), sql, args...).Scan(&n) })
	return n
}

func TestResponses(t *testing.T) {
	e := newEnv(t)
	boss := e.admin("Boss")
	ana := e.identity("Ana")
	e.exec(`INSERT INTO sessions (tenant_id, token_hash, identity_id, id_token, expires_at)
		SELECT $1, convert_to(gen_random_uuid()::text, 'UTF8'), who, '', now() + interval '1 hour' FROM unnest(ARRAY[$2, $2, $3]::uuid[]) who`, e.tenant, ana, boss)

	out, err := e.respond("identity:boss", ana, ResponseInput{Action: RevokeSessions, Reason: "odd sign-in"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Details["console_sessions"] != int64(2) || out.Error != "" {
		t.Errorf("revoke = %+v, want her 2 console sessions ended", out)
	}
	if n := e.count(`SELECT count(*) FROM sessions`); n != 1 {
		t.Errorf("sessions left = %d, want only Boss's", n)
	}

	bad := []struct {
		in   ResponseInput
		who  string
		want any
	}{
		{ResponseInput{Action: "delete"}, ana, new(*apperr.Invalid)},
		{ResponseInput{Action: RevokeSessions, Days: 3}, ana, new(*apperr.Invalid)},
		{ResponseInput{Action: RequireMFA, Days: 91}, ana, new(*apperr.Invalid)},
		// Without Grantra Login nobody can be asked for a second factor.
		{ResponseInput{Action: RequireMFA}, ana, new(*apperr.Conflict)},
		{ResponseInput{Action: Reinstate}, ana, new(*apperr.Conflict)},
		// The only admin cannot be suspended, by anyone.
		{ResponseInput{Action: Suspend}, boss, new(*apperr.Conflict)},
	}
	for _, b := range bad {
		_, err := e.respond("identity:x", b.who, b.in)
		if !errors.As(err, b.want) {
			t.Errorf("Respond(%+v) = %v, want %T", b.in, err, b.want)
		}
	}
	if _, err := e.respond("identity:x", "00000000-0000-0000-0000-000000000000", ResponseInput{Action: RevokeSessions}); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("unknown person = %v, want not found", err)
	}

	if _, err := e.respond("identity:boss", ana, ResponseInput{Action: Suspend}); err != nil {
		t.Fatal(err)
	}
	if s := e.status(ana); s != "suspended" {
		t.Errorf("status = %s, want suspended", s)
	}
	if _, err := e.respond("identity:boss", ana, ResponseInput{Action: Suspend}); !errors.As(err, new(*apperr.Conflict)) {
		t.Errorf("suspending twice = %v, want conflict", err)
	}
	if _, err := e.respond("identity:boss", ana, ResponseInput{Action: Reinstate, Reason: "password reset"}); err != nil {
		t.Fatal(err)
	}
	if s := e.status(ana); s != "active" {
		t.Errorf("status = %s, want active", s)
	}
	var list []Response
	e.tx(func(tx pgx.Tx) (err error) {
		list, err = IdentityResponses(context.Background(), tx, ana)
		return err
	})
	if len(list) != 3 || list[0].Action != Reinstate || list[2].Action != RevokeSessions || list[0].Reason != "password reset" {
		t.Errorf("responses = %+v, want reinstate, suspend, revoke", list)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action IN ('identity.suspended', 'identity.reinstated', 'itdr.response')`); n != 5 {
		t.Errorf("audited = %d, want 5", n)
	}
}

func TestResponseAlertMustBeAboutThePerson(t *testing.T) {
	e := newEnv(t)
	ana, luis := e.identity("Ana"), e.identity("Luis")
	var evs []SignIn
	for i := range 10 {
		evs = append(evs, SignIn{Time: at(time.Duration(10-i) * time.Second), User: "ana@example.com", Outcome: Failure})
	}
	e.send(evs...)
	e.scan()
	a := e.only(BruteForce)
	if _, err := e.respond("identity:x", luis, ResponseInput{Action: RevokeSessions, AlertID: a.ID}); !errors.As(err, new(*apperr.Invalid)) {
		t.Errorf("responding to Ana's alert on Luis = %v, want invalid", err)
	}
	if _, err := e.respond("identity:x", ana, ResponseInput{Action: RevokeSessions, AlertID: a.ID}); err != nil {
		t.Fatal(err)
	}
	var got Alert
	e.tx(func(tx pgx.Tx) (err error) {
		got, err = Get(context.Background(), tx, a.ID)
		return err
	})
	if len(got.Responses) != 1 || got.Responses[0].Actor != "identity:x" {
		t.Errorf("alert responses = %+v", got.Responses)
	}
}

func TestAutomaticResponse(t *testing.T) {
	e := newEnv(t)
	e.admin("Boss")
	luis := e.identity("Luis")
	e.set(MFAFatigue, SettingInput{AutoResponse: Suspend})
	var evs []SignIn
	for i := range 5 {
		evs = append(evs, SignIn{Time: at(time.Duration(5-i) * time.Second), User: "luis@example.com", Outcome: MFAFailure})
	}
	e.send(evs...)
	if res := e.scan(); res.Responses != 1 || res.ResponseErrors != 0 {
		t.Fatalf("scan = %+v, want one automatic response", res)
	}
	if s := e.status(luis); s != "suspended" {
		t.Errorf("status = %s, want suspended", s)
	}
	a := e.only(MFAFatigue)
	var got Alert
	e.tx(func(tx pgx.Tx) (err error) {
		got, err = Get(context.Background(), tx, a.ID)
		return err
	})
	if len(got.Responses) != 1 || got.Responses[0].Actor != Actor || !strings.HasPrefix(got.Responses[0].Reason, "Automatic response to: ") {
		t.Errorf("responses = %+v, want one automatic suspension", got.Responses)
	}
	// The alert growing more severe does not respond again.
	e.send(SignIn{Time: at(0), User: "luis@example.com", Outcome: Success})
	if res := e.scan(); res.Escalated != 1 || res.Responses != 0 {
		t.Errorf("rescan = %+v, want the alert escalated and no second suspension", res)
	}
}
