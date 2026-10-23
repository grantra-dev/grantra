package settings

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/notify"
)

func TestDefinitions(t *testing.T) {
	groups := []string{GroupRequests, GroupReviews, GroupSoD, GroupPAM, GroupThreats, GroupInsights, GroupReports, GroupConsole}
	seen := map[string]bool{}
	for _, d := range List() {
		switch {
		case seen[d.Key]:
			t.Errorf("%s is defined twice", d.Key)
		case strings.Count(d.Key, ".") != 1:
			t.Errorf("%s: keys are module.name", d.Key)
		case d.Title == "" || d.Help == "":
			t.Errorf("%s needs a title and help", d.Key)
		case !slices.Contains(groups, d.Group):
			t.Errorf("%s: unknown group %q", d.Key, d.Group)
		case d.Min > d.Default || d.Default > d.Max:
			t.Errorf("%s: default %d is outside %d..%d", d.Key, d.Default, d.Min, d.Max)
		case d.Kind == Switch && (d.Min != 0 || d.Max != 1 || d.Unit != ""):
			t.Errorf("%s: a switch is 0 or 1, with no unit", d.Key)
		case d.Kind != Switch && d.Kind != Number:
			t.Errorf("%s: unknown kind %q", d.Key, d.Kind)
		case d.Kind == Number && d.Unit == "":
			t.Errorf("%s: say what the number counts", d.Key)
		case d.Looser != "" && d.Looser != Higher && d.Looser != Lower:
			t.Errorf("%s: looser must be higher or lower", d.Key)
		case d.Module != "" && d.Module != "pam" && d.Module != "itdr":
			t.Errorf("%s: unknown module %q", d.Key, d.Module)
		}
		seen[d.Key] = true
	}
	// SQL quotes keys, so they must never need escaping.
	for k := range seen {
		if strings.ContainsAny(k, `'\`) {
			t.Errorf("%s cannot go in SQL as is", k)
		}
	}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		key  string
		v    int
		want string
	}{
		{BreakGlassMinutes, 90, "90 minutes"},
		{SessionMaxHours, 1, "1 hour"},
		{RoleMinSharePercent, 75, "75%"},
		{SoDExceptionsWithoutEnd, 0, "off"},
		{SoDExceptionsWithoutEnd, 1, "on"},
	} {
		if got := describe(mustLookup(c.key), c.v); got != c.want {
			t.Errorf("describe(%s, %d) = %q, want %q", c.key, c.v, got, c.want)
		}
	}
}

func TestCap(t *testing.T) {
	saved := slices.Clone(definitions)
	t.Cleanup(func() { definitions = saved })
	Cap(SessionMaxHours, 4)
	if d := mustLookup(SessionMaxHours); d.Max != 4 || d.Default != 4 || d.Min != 1 {
		t.Errorf("capped at 4 hours = %+v, want default and max 4", d)
	}
	Cap(SessionIdleMinutes, 2)
	if d := mustLookup(SessionIdleMinutes); d.Max != 2 || d.Min != 2 || d.Default != 2 {
		t.Errorf("capped under the minimum = %+v, want the operator's 2 minutes", d)
	}
	Cap(BreakGlassMinutes, 10000)
	if d := mustLookup(BreakGlassMinutes); d.Max != 8*60 || d.Default != 60 {
		t.Errorf("a cap above the maximum = %+v, want it unchanged", d)
	}
}

type env struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	tenant string
}

func newEnv(t *testing.T) *env {
	pool := dbtest.Open(t)
	e := &env{t: t, ctx: context.Background(), pool: pool, tenant: dbtest.Tenant(t, pool, "settings")}
	if err := authz.EnsureBuiltinApp(e.ctx, pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) tx(fn func(pgx.Tx) error) error { return db.InTenant(e.ctx, e.pool, e.tenant, fn) }

func (e *env) must(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := e.tx(fn); err != nil {
		e.t.Fatal(err)
	}
}

// admin adds an active admin and returns how the audit log names them.
func (e *env) admin(name string) (id, actor string) {
	e.t.Helper()
	e.must(func(tx pgx.Tx) error {
		if err := tx.QueryRow(e.ctx, `INSERT INTO identities (tenant_id, display_name, email, status) VALUES ($1, $2, $2 || '@example.com', 'active') RETURNING id`,
			e.tenant, name).Scan(&id); err != nil {
			return err
		}
		return authz.Grant(e.ctx, tx, e.tenant, authz.RoleAdmin, id, nil)
	})
	return id, db.IdentityActor(id)
}

func (e *env) get(key string) int {
	e.t.Helper()
	var v int
	e.must(func(tx pgx.Tx) (err error) { v, err = Get(e.ctx, tx, key); return err })
	return v
}

func (e *env) save(actor, key string, value int) (Setting, error) {
	var s Setting
	err := e.tx(func(tx pgx.Tx) (err error) {
		s, err = Save(e.ctx, tx, e.tenant, actor, key, value, "INC-7: more time to respond")
		return err
	})
	return s, err
}

func (e *env) decide(actor, id string, approve bool) (Change, error) {
	var c Change
	err := e.tx(func(tx pgx.Tx) (err error) {
		c, err = Decide(e.ctx, tx, e.tenant, actor, id, approve, "ok")
		return err
	})
	return c, err
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	e.must(func(tx pgx.Tx) error { return tx.QueryRow(e.ctx, sql, args...).Scan(&n) })
	return n
}

func isInvalid(err error) bool  { return errors.As(err, new(*apperr.Invalid)) }
func isConflict(err error) bool { return errors.As(err, new(*apperr.Conflict)) }

func TestReadingValues(t *testing.T) {
	e := newEnv(t)
	if v := e.get(BreakGlassMinutes); v != 60 {
		t.Errorf("default break-glass = %d, want 60", v)
	}
	// A value saved under older limits is read within today's.
	e.must(func(tx pgx.Tx) error {
		_, err := tx.Exec(e.ctx, `INSERT INTO tenant_settings (tenant_id, key, value, updated_by) VALUES ($1, $2, 5000, 'old'), ($1, $3, 40, 'x')`,
			e.tenant, BreakGlassMinutes, PasswordLength)
		return err
	})
	if v := e.get(BreakGlassMinutes); v != 8*60 {
		t.Errorf("break-glass saved too long = %d, want the 480 maximum", v)
	}
	var sqlValue, other int
	var values map[string]int
	e.must(func(tx pgx.Tx) (err error) {
		if err := tx.QueryRow(e.ctx, `SELECT `+SQL(BreakGlassMinutes)+`, `+SQL(UnusedDays)).Scan(&sqlValue, &other); err != nil {
			return err
		}
		values, err = Values(e.ctx, tx, PasswordLength, UnusedDays, BreakGlassMinutes)
		return err
	})
	if sqlValue != 8*60 || other != 90 {
		t.Errorf("SQL = %d, %d; want 480 and the default 90", sqlValue, other)
	}
	if values[PasswordLength] != 40 || values[UnusedDays] != 90 || values[BreakGlassMinutes] != 8*60 {
		t.Errorf("values = %v", values)
	}
	// Another tenant sees its own.
	other2 := dbtest.Tenant(t, e.pool, "settings")
	if err := db.InTenant(e.ctx, e.pool, other2, func(tx pgx.Tx) (err error) {
		v, err := Get(e.ctx, tx, PasswordLength)
		if v != 24 {
			t.Errorf("another tenant's password length = %d, want the default", v)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSaveChecks(t *testing.T) {
	e := newEnv(t)
	_, ana := e.admin("Ana")
	for _, c := range []struct {
		key   string
		value int
		check func(error) bool
	}{
		{"nope.unknown", 1, func(err error) bool { return errors.Is(err, apperr.ErrNotFound) }},
		{BreakGlassMinutes, 10, isInvalid},
		{BreakGlassMinutes, 481, isInvalid},
		{SoDExceptionsWithoutEnd, 2, isInvalid},
	} {
		if _, err := e.save(ana, c.key, c.value); !c.check(err) {
			t.Errorf("save %s = %d: %v", c.key, c.value, err)
		}
	}
	long := strings.Repeat("x", 2001)
	if err := e.tx(func(tx pgx.Tx) error {
		_, err := Save(e.ctx, tx, e.tenant, ana, UnusedDays, 60, long)
		return err
	}); !isInvalid(err) {
		t.Errorf("long reason = %v", err)
	}
}

func TestOneAdminChangesAtOnce(t *testing.T) {
	e := newEnv(t)
	_, ana := e.admin("Ana")
	// Stricter, at once.
	s, err := e.save(ana, BreakGlassMinutes, 30)
	if err != nil || s.Value != 30 || s.Pending != nil || s.UpdatedBy != ana {
		t.Fatalf("save = %+v, %v", s, err)
	}
	// Less strict, with nobody else to ask: at once too, and the audit log
	// says nobody else saw it.
	if s, err = e.save(ana, BreakGlassMinutes, 120); err != nil || s.Value != 120 || s.Pending != nil {
		t.Fatalf("loosen alone = %+v, %v", s, err)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'setting.changed' AND target_id = $1 AND details ->> 'no_second_admin' = 'true'`, BreakGlassMinutes); n != 1 {
		t.Errorf("audited %d loosening changes with no second admin, want 1", n)
	}
	// Back to the default: no row, so a newer default would apply.
	if s, err = e.save(ana, BreakGlassMinutes, 60); err != nil || s.Value != 60 || s.UpdatedBy != "" {
		t.Fatalf("reset = %+v, %v", s, err)
	}
	if n := e.count(`SELECT count(*) FROM tenant_settings`); n != 0 {
		t.Errorf("%d rows after going back to the default", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'setting.changed' AND target_id = $1`, BreakGlassMinutes); n != 3 {
		t.Errorf("audited %d changes, want 3", n)
	}
	// Saving the same value again changes nothing.
	if _, err := e.save(ana, BreakGlassMinutes, 60); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'setting.changed'`); n != 3 {
		t.Errorf("an unchanged value was audited")
	}
}

func TestFourEyes(t *testing.T) {
	e := newEnv(t)
	_, ana := e.admin("Ana")
	luisID, luis := e.admin("Luis")
	// Ana makes break-glass check-outs longer: it waits for Luis.
	s, err := e.save(ana, BreakGlassMinutes, 120)
	if err != nil || s.Value != 60 || s.Pending == nil || s.Pending.Value != 120 || s.Pending.OldValue != 60 ||
		s.Pending.ProposedByName != "Ana" || s.Pending.Reason != "INC-7: more time to respond" {
		t.Fatalf("loosen = %+v (pending %+v), %v", s, s.Pending, err)
	}
	if v := e.get(BreakGlassMinutes); v != 60 {
		t.Errorf("value before approval = %d, want 60", v)
	}
	if n := e.count(`SELECT count(*) FROM notification_outbox WHERE kind = $1 AND identity_id = $2`, notify.SettingChange, luisID); n != 1 {
		t.Errorf("Luis got %d emails about it, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM notification_outbox WHERE kind = $1`, notify.SettingChange); n != 1 {
		t.Errorf("%d emails, want only Luis's", n)
	}
	// One change at a time, even a stricter one.
	if _, err := e.save(luis, BreakGlassMinutes, 30); !isConflict(err) {
		t.Errorf("save while a change waits = %v", err)
	}
	// Nobody approves their own.
	if _, err := e.decide(ana, s.Pending.ID, true); !isConflict(err) {
		t.Errorf("Ana approving her own = %v", err)
	}
	var pending []Change
	e.must(func(tx pgx.Tx) (err error) { pending, err = PendingChanges(e.ctx, tx); return err })
	if len(pending) != 1 || pending[0].Title != "Longest break-glass check-out" || pending[0].Unit != "minutes" {
		t.Fatalf("pending = %+v", pending)
	}
	c, err := e.decide(luis, s.Pending.ID, true)
	if err != nil || c.Status != Approved || c.DecidedBy != luis {
		t.Fatalf("Luis approves = %+v, %v", c, err)
	}
	if v := e.get(BreakGlassMinutes); v != 120 {
		t.Errorf("value after approval = %d, want 120", v)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'setting.changed' AND actor = $1 AND details ->> 'proposed_by' = $2`, luis, ana); n != 1 {
		t.Errorf("the change is not audited as Luis's approval of Ana's proposal")
	}
	if _, err := e.decide(luis, s.Pending.ID, false); !isConflict(err) {
		t.Errorf("deciding twice = %v", err)
	}

	// Rejected: nothing changes.
	s, _ = e.save(luis, PasswordLength, 16)
	if s.Pending == nil {
		t.Fatalf("shorter passwords did not wait: %+v", s)
	}
	if c, err := e.decide(ana, s.Pending.ID, false); err != nil || c.Status != Rejected {
		t.Fatalf("reject = %+v, %v", c, err)
	}
	if v := e.get(PasswordLength); v != 24 {
		t.Errorf("rejected change applied: %d", v)
	}
	// Withdrawn by whoever proposed it.
	s, _ = e.save(luis, SoDExceptionsWithoutEnd, 0) // stricter: at once
	if s.Pending != nil || s.Value != 0 {
		t.Fatalf("turning a looser switch off = %+v", s)
	}
	s, _ = e.save(luis, SoDExceptionsWithoutEnd, 1)
	if s.Pending == nil {
		t.Fatalf("turning it back on did not wait: %+v", s)
	}
	if c, err := e.decide(luis, s.Pending.ID, false); err != nil || c.Status != Withdrawn {
		t.Fatalf("withdraw = %+v, %v", c, err)
	}
	if ok, _ := settingOn(e, SoDExceptionsWithoutEnd); ok {
		t.Errorf("withdrawn change applied")
	}
	// Settings with no direction never wait.
	if s, err := e.save(ana, UnusedDays, 30); err != nil || s.Pending != nil || s.Value != 30 {
		t.Errorf("neutral setting = %+v, %v", s, err)
	}
	for _, action := range []string{"setting.change_proposed", "setting.change_approved", "setting.change_rejected", "setting.change_withdrawn"} {
		if n := e.count(`SELECT count(*) FROM audit_events WHERE action = $1`, action); n == 0 {
			t.Errorf("no %s in the audit log", action)
		}
	}
	var second bool
	e.must(func(tx pgx.Tx) (err error) { second, err = SecondAdmin(e.ctx, tx, ana); return err })
	if !second {
		t.Errorf("Ana has Luis as a second admin")
	}
}

func settingOn(e *env, key string) (bool, error) {
	var on bool
	err := e.tx(func(tx pgx.Tx) (err error) { on, err = On(e.ctx, tx, key); return err })
	return on, err
}

func TestLoad(t *testing.T) {
	e := newEnv(t)
	_, ana := e.admin("Ana")
	e.admin("Luis")
	if _, err := e.save(ana, UnusedDays, 60); err != nil {
		t.Fatal(err)
	}
	if _, err := e.save(ana, SessionMaxHours, 12); err != nil {
		t.Fatal(err)
	}
	var all []Setting
	e.must(func(tx pgx.Tx) (err error) { all, err = Load(e.ctx, tx); return err })
	if len(all) != len(definitions) {
		t.Fatalf("loaded %d settings, want %d", len(all), len(definitions))
	}
	by := map[string]Setting{}
	for _, s := range all {
		by[s.Key] = s
	}
	if s := by[UnusedDays]; s.Value != 60 || s.UpdatedBy != ana || s.UpdatedAt == nil {
		t.Errorf("unused days = %+v", s)
	}
	if s := by[SessionMaxHours]; s.Value != 8 || s.Pending == nil || s.Pending.Value != 12 {
		t.Errorf("session max = %+v, want 8 with 12 pending", s)
	}
	if s := by[BreakGlassMinutes]; s.Value != 60 || s.UpdatedBy != "" || s.Pending != nil {
		t.Errorf("break-glass = %+v, want the untouched default", s)
	}
	var public map[string]int
	e.must(func(tx pgx.Tx) (err error) { public, err = Public(e.ctx, tx); return err })
	if public[UnusedDays] != 60 || len(public) != 1 {
		t.Errorf("public = %v", public)
	}
	if _, ok := public[PasswordLength]; ok {
		t.Errorf("public settings include the vault's password length")
	}
}
