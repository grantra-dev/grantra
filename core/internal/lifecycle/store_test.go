package lifecycle

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
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

type dbEnv struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	source string
}

func newDBEnv(t *testing.T, cfg Config) *dbEnv {
	t.Helper()
	pool := dbtest.Open(t)
	e := &dbEnv{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "lifecycle")}
	e.tx(func(tx pgx.Tx) error {
		s, err := CreateSource(context.Background(), tx, e.tenant, "HR", KindCSV, cfg)
		e.source = s.ID
		return err
	})
	return e
}

func (e *dbEnv) tx(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(context.Background(), e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *dbEnv) sync(feed string, opts Options) SyncResult {
	e.t.Helper()
	var res SyncResult
	e.tx(func(tx pgx.Tx) error {
		var err error
		res, err = Sync(context.Background(), tx, e.tenant, e.source, strings.NewReader(feed), opts)
		return err
	})
	return res
}

func (e *dbEnv) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	e.tx(func(tx pgx.Tx) error { return tx.QueryRow(context.Background(), sql, args...).Scan(&n) })
	return n
}

type stored struct {
	ID, Status, Manager, Dept string
}

func (e *dbEnv) identity(ext string) stored {
	e.t.Helper()
	var s stored
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT i.id, i.status, coalesce(m.external_id, ''), coalesce(i.attributes->>'department', '')
			 FROM identities i LEFT JOIN identities m ON m.id = i.manager_id WHERE i.external_id = $1`, ext).
			Scan(&s.ID, &s.Status, &s.Manager, &s.Dept)
	})
	return s
}

var day1 = time.Date(2026, 10, 13, 9, 0, 0, 0, time.UTC)

func TestSyncLifecycle(t *testing.T) {
	e := newDBEnv(t, Config{MaxLeaverPercent: 100})
	opts := Options{Actor: "identity:tester", Now: day1}

	// A first admin exists before any HR data, as after the bootstrap.
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email) VALUES ($1, 'ana@example.com', 'ana@example.com')`, e.tenant)
		return err
	})

	// Managers can come after their reports in the file.
	first := header +
		"E2,Luis,luis@example.com,E1,Eng,,,\n" +
		"E1,Ana,ana@example.com,,Eng,,,\n" +
		"E3,Marta,marta@example.com,E1,Eng,,,\n" +
		"E4,Nuevo,nuevo@example.com,E1,Eng,,2026-11-02,\n"

	// A dry run plans and changes nothing.
	res := e.sync(first, Options{Actor: opts.Actor, Now: day1, DryRun: true})
	if res.Status != RunPlanned || res.Counts[Joiner] != 3 || res.Counts[Mover] != 1 {
		t.Fatalf("dry run = %s %v", res.Status, res.Counts)
	}
	if n := e.count(`SELECT count(*) FROM identities`); n != 1 {
		t.Fatalf("dry run created identities: %d", n)
	}

	res = e.sync(first, opts)
	if res.Status != RunApplied {
		t.Fatalf("first sync = %s %v", res.Status, res.Errors)
	}
	ana, luis, nuevo := e.identity("E1"), e.identity("E2"), e.identity("E4")
	if luis.Manager != "E1" || luis.Status != "active" || luis.Dept != "Eng" {
		t.Errorf("luis = %+v", luis)
	}
	if nuevo.Status != "pending" {
		t.Errorf("future starter = %+v", nuevo)
	}
	if n := e.count(`SELECT count(*) FROM identities`); n != 4 {
		t.Errorf("identities = %d, want 4 (the admin was linked, not duplicated)", n)
	}
	if n := e.count(`SELECT count(*) FROM lifecycle_events WHERE kind = 'joiner'`); n != 3 {
		t.Errorf("joiner events = %d, want 3", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'identity.joined' AND actor = $1`, "identity_source:"+e.source); n != 3 {
		t.Errorf("identity.joined audit events = %d, want 3", n)
	}

	// Luis signs in; his session must not outlive his job.
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO sessions (tenant_id, token_hash, identity_id, id_token, expires_at)
			 VALUES ($1, '\x01', $2, 'x', now() + interval '1 hour')`, e.tenant, luis.ID)
		return err
	})

	// Second day: Marta moves to Sales under Luis, Luis is terminated, and
	// Nuevo has disappeared from the export.
	res = e.sync(header+
		"E1,Ana,ana@example.com,,Eng,,,\n"+
		"E2,Luis,luis@example.com,E1,Eng,terminated,,2026-10-13\n"+
		"E3,Marta,marta@example.com,E2,Sales,,,\n", Options{Actor: opts.Actor, Now: day1.AddDate(0, 0, 1)})
	if res.Status != RunApplied || res.Counts[Mover] != 1 || res.Counts[Leaver] != 2 || res.Unchanged != 1 {
		t.Fatalf("second sync = %s %v unchanged %d %v", res.Status, res.Counts, res.Unchanged, res.Errors)
	}
	if m := e.identity("E3"); m.Manager != "E2" || m.Dept != "Sales" {
		t.Errorf("marta = %+v", m)
	}
	if s := e.identity("E2"); s.Status != "terminated" {
		t.Errorf("luis = %+v", s)
	}
	if s := e.identity("E4"); s.Status != "terminated" {
		t.Errorf("missing starter = %+v", s)
	}
	if n := e.count(`SELECT count(*) FROM sessions WHERE identity_id = $1`, luis.ID); n != 0 {
		t.Errorf("leaver still has %d sessions", n)
	}
	if ana.ID == "" || e.identity("E1").Status != "active" {
		t.Errorf("ana = %+v", e.identity("E1"))
	}

	// Runs and events can be read back.
	e.tx(func(tx pgx.Tx) error {
		runs, err := ListRuns(context.Background(), tx, e.source, 10)
		if err != nil {
			return err
		}
		if len(runs) != 3 || runs[0].Status != RunApplied || !runs[2].DryRun {
			t.Errorf("runs = %+v", runs)
		}
		events, err := ListEvents(context.Background(), tx, luis.ID, 0, 10)
		if err != nil {
			return err
		}
		if len(events) != 2 || events[0].Kind != Leaver || events[1].Kind != Joiner {
			t.Errorf("luis events = %+v", events)
		}
		src, err := GetSource(context.Background(), tx, e.source)
		if err != nil {
			return err
		}
		if src.LastRun == nil || src.LastRun.ID != runs[0].ID {
			t.Errorf("last run = %+v", src.LastRun)
		}
		return nil
	})
}

func TestSyncRefusesBadFiles(t *testing.T) {
	e := newDBEnv(t, Config{})
	opts := Options{Actor: "identity:tester", Now: day1}

	var feed strings.Builder
	feed.WriteString(header)
	for i := range 20 {
		ext := "E" + string(rune('a'+i))
		feed.WriteString(ext + ",P" + ext + ",,,Eng,,,\n")
	}
	if res := e.sync(feed.String(), opts); res.Status != RunApplied {
		t.Fatalf("first sync = %s %v", res.Status, res.Errors)
	}

	// A file with one bad line changes nothing at all.
	res := e.sync(feed.String()+"Ez,Bad,,,Eng,on holiday,,\n", opts)
	if res.Status != RunInvalid || len(res.Errors) != 1 {
		t.Errorf("bad line = %s %v", res.Status, res.Errors)
	}
	if res := e.sync("not,a,feed\n", opts); res.Status != RunInvalid {
		t.Errorf("bad header = %s", res.Status)
	}

	// A truncated export would offboard half the company: refused.
	half := strings.Join(strings.Split(feed.String(), "\n")[:11], "\n") + "\n"
	res = e.sync(half, opts)
	if res.Status != RunRefused || res.Counts[Leaver] != 10 {
		t.Fatalf("truncated = %s %v", res.Status, res.Counts)
	}
	if n := e.count(`SELECT count(*) FROM identities WHERE status = 'terminated'`); n != 0 {
		t.Fatalf("refused sync terminated %d identities", n)
	}
	// Forcing it goes through.
	res = e.sync(half, Options{Actor: opts.Actor, Now: day1, Force: true})
	if res.Status != RunApplied {
		t.Fatalf("forced = %s", res.Status)
	}
	if n := e.count(`SELECT count(*) FROM identities WHERE status = 'terminated'`); n != 10 {
		t.Errorf("forced sync terminated %d identities, want 10", n)
	}
	if n := e.count(`SELECT count(*) FROM identity_source_runs WHERE status IN ('invalid', 'refused')`); n != 3 {
		t.Errorf("recorded failed runs = %d, want 3", n)
	}

	var err error
	e.tx(func(tx pgx.Tx) error {
		_, err = Sync(context.Background(), tx, e.tenant, "00000000-0000-0000-0000-000000000000", strings.NewReader(header), opts)
		return nil
	})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown source = %v", err)
	}
}

func TestApplyDueDates(t *testing.T) {
	e := newDBEnv(t, Config{})
	e.sync(header+
		"E1,Ana,,,Eng,,2026-10-14,\n"+
		"E2,Luis,,,Eng,,,2026-10-14\n"+
		"E3,Marta,,,Eng,,,\n", Options{Actor: "identity:tester", Now: day1})
	if s := e.identity("E1"); s.Status != "pending" {
		t.Fatalf("ana = %+v", s)
	}

	// On the start date Ana starts; Luis works his last day.
	activated, left, err := ApplyDueDates(context.Background(), e.pool, e.tenant, day1.AddDate(0, 0, 1))
	if err != nil || activated != 1 || left != 0 {
		t.Fatalf("day 2 = %d activated, %d left, %v", activated, left, err)
	}
	// The day after, Luis is gone.
	activated, left, err = ApplyDueDates(context.Background(), e.pool, e.tenant, day1.AddDate(0, 0, 2))
	if err != nil || activated != 0 || left != 1 {
		t.Fatalf("day 3 = %d activated, %d left, %v", activated, left, err)
	}
	if e.identity("E1").Status != "active" || e.identity("E2").Status != "terminated" || e.identity("E3").Status != "active" {
		t.Errorf("statuses = %+v %+v %+v", e.identity("E1"), e.identity("E2"), e.identity("E3"))
	}
	if n := e.count(`SELECT count(*) FROM lifecycle_events WHERE kind = 'leaver' AND changes->>'reason' = $1`, ReasonEndDate); n != 1 {
		t.Errorf("end-date leaver events = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'identity.activated'`); n != 1 {
		t.Errorf("identity.activated audit events = %d, want 1", n)
	}
}

func TestCreateSource(t *testing.T) {
	e := newDBEnv(t, Config{})
	e.tx(func(tx pgx.Tx) error {
		ctx := context.Background()
		if _, err := CreateSource(ctx, tx, e.tenant, "HR", KindCSV, Config{}); !errors.Is(err, ErrNameTaken) {
			t.Errorf("duplicate name = %v", err)
		}
		var inv *apperr.Invalid
		if _, err := CreateSource(ctx, tx, e.tenant, "W", "workday", Config{}); !errors.As(err, &inv) {
			t.Errorf("unknown kind = %v", err)
		}
		if _, err := CreateSource(ctx, tx, e.tenant, " ", KindCSV, Config{}); !errors.As(err, &inv) {
			t.Errorf("empty name = %v", err)
		}
		if _, err := GetSource(ctx, tx, "nope"); !errors.Is(err, ErrNotFound) {
			t.Errorf("bad id = %v", err)
		}
		return nil
	})
}

// TestSyncLargeFeed syncs more people than fit in one batch, so every batch
// boundary is crossed, and checks every write landed: identities, managers
// (who often come after their reports), events and audit entries.
func TestSyncLargeFeed(t *testing.T) {
	e := newDBEnv(t, Config{MaxLeaverPercent: 100})
	n := db.BatchSize*2 + 7
	feed := func(dept func(i int) string, skip func(i int) bool) string {
		var b strings.Builder
		b.WriteString(header)
		for i := range n {
			if skip(i) {
				continue
			}
			// Everyone reports to the person ten rows further down, so
			// managers come after their reports; the last ten have none.
			mgr := ""
			if i+10 < n {
				mgr = fmt.Sprintf("E%d", i+10)
			}
			fmt.Fprintf(&b, "E%d,Person %d,p%d@example.com,%s,%s,,,\n", i, i, i, mgr, dept(i))
		}
		return b.String()
	}
	start := time.Now()
	res := e.sync(feed(func(int) string { return "Eng" }, func(int) bool { return false }), Options{Actor: "identity:tester", Now: day1})
	t.Logf("%d joiners in %s", n, time.Since(start))
	if res.Status != RunApplied || res.Counts[Joiner] != n {
		t.Fatalf("first sync = %s %v %v", res.Status, res.Counts, res.Errors)
	}
	if got := e.count(`SELECT count(*) FROM identities i JOIN identities m ON m.id = i.manager_id
		WHERE m.external_id = 'E' || (substr(i.external_id, 2)::int + 10)`); got != n-10 {
		t.Errorf("managers set = %d, want %d", got, n-10)
	}

	// Every third person moves to Sales and the last five leave.
	start = time.Now()
	res = e.sync(feed(func(i int) string {
		if i%3 == 0 {
			return "Sales"
		}
		return "Eng"
	}, func(i int) bool { return i >= n-5 }), Options{Actor: "identity:tester", Now: day1.AddDate(0, 0, 1)})
	t.Logf("%d movers and leavers in %s", res.Counts[Mover]+res.Counts[Leaver], time.Since(start))
	// Reports of the leavers keep them as managers: they are still known.
	movers := (n - 5 + 2) / 3
	if res.Status != RunApplied || res.Counts[Mover] != movers || res.Counts[Leaver] != 5 {
		t.Fatalf("second sync = %s %v, want %d movers %v", res.Status, res.Counts, movers, res.Errors)
	}
	if got := e.count(`SELECT count(*) FROM identities WHERE attributes->>'department' = 'Sales'`); got != (n-5+2)/3 {
		t.Errorf("in Sales = %d", got)
	}
	if got := e.count(`SELECT count(*) FROM identities WHERE status = 'terminated'`); got != 5 {
		t.Errorf("terminated = %d", got)
	}
	if got := e.count(`SELECT count(*) FROM identities WHERE manager_id IS NOT NULL`); got != n-10 {
		t.Errorf("with a manager = %d, want %d", got, n-10)
	}
	for kind, want := range map[Kind]int{Joiner: n, Mover: movers, Leaver: 5} {
		if got := e.count(`SELECT count(*) FROM lifecycle_events WHERE kind = $1`, kind); got != want {
			t.Errorf("%s events = %d, want %d", kind, got, want)
		}
		if got := e.count(`SELECT count(*) FROM audit_events WHERE action = $1`, auditAction[kind]); got != want {
			t.Errorf("%s audit events = %d, want %d", kind, got, want)
		}
	}
}
