package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/flatfile"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

// fake is a connector whose snapshot the test sets, per application name
// given in its config.
var (
	fakeMu    sync.Mutex
	fakeSnaps = map[string]connector.Snapshot{}
	fakeErrs  = map[string]error{}
)

type fakeReader struct{ name string }

func (f fakeReader) Read(context.Context) (connector.Snapshot, error) {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	return fakeSnaps[f.name], fakeErrs[f.name]
}

func init() {
	connector.Register("fake", func(raw json.RawMessage, _ connector.SecretResolver) (connector.Reader, error) {
		var cfg struct{ Name string }
		if err := json.Unmarshal(raw, &cfg); err != nil || cfg.Name == "" {
			return nil, errors.New("fake needs a name")
		}
		return fakeReader{cfg.Name}, nil
	})
}

func noSecrets(string) (string, error) { return "", errors.New("no secrets in tests") }

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	app    string
	name   string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	e := &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "accounts")}
	if err := authz.EnsureBuiltinApp(context.Background(), pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	e.name = e.tenant
	e.tx(func(tx pgx.Tx) error {
		app, err := catalog.CreateApplication(context.Background(), tx, e.tenant, "CRM", "")
		if err != nil {
			return err
		}
		e.app = app.ID
		return SetConnector(context.Background(), tx, app.ID, "fake", json.RawMessage(`{"name": "`+e.name+`"}`), noSecrets)
	})
	return e
}

func (e *env) tx(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(context.Background(), e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) set(s connector.Snapshot, err error) {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	fakeSnaps[e.name], fakeErrs[e.name] = s, err
}

func (e *env) aggregate() Run {
	e.t.Helper()
	run, err := Aggregate(context.Background(), e.pool, e.tenant, e.app, "identity:tester", noSecrets)
	if err != nil {
		e.t.Fatal(err)
	}
	return run
}

func (e *env) identity(name, email, ext string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email, external_id) VALUES ($1, $2, nullif($3, ''), nullif($4, '')) RETURNING id`,
			e.tenant, name, email, ext).Scan(&id)
	})
	return id
}

func (e *env) accounts(orphans bool) map[string]Account {
	e.t.Helper()
	out := map[string]Account{}
	e.tx(func(tx pgx.Tx) error {
		list, err := List(context.Background(), tx, e.app, Filter{Orphans: orphans, Limit: 100})
		for _, a := range list {
			out[a.NativeID] = a
		}
		return err
	})
	return out
}

func TestAggregateAndCorrelate(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana", "Ana@Example.com", "E1")
	luis := e.identity("Luis", "luis@example.com", "E2")
	e.identity("Twin 1", "twin@example.com", "")
	e.identity("Twin 2", "twin@example.com", "")

	e.set(connector.Snapshot{
		Entitlements: []connector.Entitlement{{NativeID: "g1", Name: "Admins"}, {NativeID: "g2", Name: "Sales"}},
		Accounts: []connector.Account{
			{NativeID: "u1", Username: "ana", ExternalID: "E1", Active: true, Entitlements: []string{"g1", "g2"}},
			{NativeID: "u2", Username: "luis@example.com", Active: false, Entitlements: []string{"g2", "g9"}},
			{NativeID: "u3", Username: "twin", Email: "twin@example.com", Active: true},
			{NativeID: "u4", Username: "svc-backup", Active: true},
		},
	}, nil)
	run := e.aggregate()
	if run.Status != RunSucceeded {
		t.Fatalf("run = %+v", run)
	}
	if run.Counts["accounts_new"] != 4 || run.Counts["entitlements_new"] != 2 || run.Counts["orphans"] != 2 ||
		run.Counts["correlated"] != 2 || run.Counts["unknown_memberships"] != 1 || run.Counts["memberships_added"] != 3 {
		t.Errorf("counts = %v", run.Counts)
	}
	got := e.accounts(false)
	if got["u1"].IdentityID != ana || got["u1"].CorrelatedBy != ByExternalID || len(got["u1"].Entitlements) != 2 {
		t.Errorf("u1 = %+v", got["u1"])
	}
	if got["u2"].IdentityID != luis || got["u2"].CorrelatedBy != ByUsername || got["u2"].Status != "disabled" {
		t.Errorf("u2 = %+v", got["u2"])
	}
	if got["u3"].IdentityID != "" {
		t.Errorf("an email shared by two people must not correlate: %+v", got["u3"])
	}
	if o := e.accounts(true); len(o) != 2 {
		t.Errorf("orphans = %v", o)
	}

	// The backup account is a service login: mark it as nobody's by hand,
	// and give the twin account to Ana by hand. Both survive re-aggregation.
	e.tx(func(tx pgx.Tx) error {
		if _, err := Link(context.Background(), tx, got["u4"].ID, ""); err != nil {
			return err
		}
		_, err := Link(context.Background(), tx, got["u3"].ID, ana)
		return err
	})

	// Next read: u2 gone, u1 lost Admins, Admins group deleted.
	e.set(connector.Snapshot{
		Entitlements: []connector.Entitlement{{NativeID: "g2", Name: "Sales team"}},
		Accounts: []connector.Account{
			{NativeID: "u1", Username: "ana", ExternalID: "E1", Active: true, Entitlements: []string{"g2"}},
			{NativeID: "u3", Username: "twin", Email: "twin@example.com", Active: true},
			{NativeID: "u4", Username: "svc-backup", Active: true},
		},
	}, nil)
	run = e.aggregate()
	if run.Counts["accounts_deleted"] != 1 || run.Counts["entitlements_missing"] != 1 || run.Counts["memberships_removed"] != 2 {
		t.Errorf("second counts = %v", run.Counts)
	}
	got = e.accounts(false)
	if got["u2"].Status != "deleted" || len(got["u2"].Entitlements) != 0 {
		t.Errorf("u2 = %+v", got["u2"])
	}
	if got["u3"].IdentityID != ana || got["u3"].CorrelatedBy != ByHand {
		t.Errorf("manual link lost: %+v", got["u3"])
	}
	if got["u4"].IdentityID != "" || got["u4"].CorrelatedBy != ByHand {
		t.Errorf("manual unlink lost: %+v", got["u4"])
	}
	if o := e.accounts(true); len(o) != 1 {
		t.Errorf("orphans now = %v, want only the service login", o)
	}
	e.tx(func(tx pgx.Tx) error {
		mine, err := ForIdentity(context.Background(), tx, ana)
		if len(mine) != 2 {
			t.Errorf("ana's accounts = %+v", mine)
		}
		return err
	})
}

func TestAggregateFailuresChangeNothing(t *testing.T) {
	e := newEnv(t)
	e.set(connector.Snapshot{Accounts: []connector.Account{{NativeID: "u1", Active: true}}}, nil)
	e.aggregate()

	e.set(connector.Snapshot{}, errors.New("connection refused"))
	if run := e.aggregate(); run.Status != RunFailed || run.Error != "connection refused" {
		t.Errorf("read error run = %+v", run)
	}
	// An empty answer where accounts used to be looks like a broken read.
	e.set(connector.Snapshot{}, nil)
	if run := e.aggregate(); run.Status != RunFailed {
		t.Errorf("empty read run = %+v", run)
	}
	if a := e.accounts(false); a["u1"].Status != "active" {
		t.Errorf("u1 = %+v", a["u1"])
	}
	e.tx(func(tx pgx.Tx) error {
		runs, err := Runs(context.Background(), tx, e.app, 10)
		if len(runs) != 3 || runs[0].Status != RunFailed {
			t.Errorf("runs = %+v", runs)
		}
		return err
	})

	// Manual and built-in applications have nothing to aggregate.
	var builtin string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT id FROM applications WHERE connector = 'grantra'`).Scan(&builtin)
	})
	var inv *apperr.Invalid
	if _, err := Aggregate(context.Background(), e.pool, e.tenant, builtin, "x", noSecrets); !errors.As(err, &inv) {
		t.Errorf("built-in aggregate = %v", err)
	}
	e.tx(func(tx pgx.Tx) error {
		if err := SetConnector(context.Background(), tx, builtin, "fake", json.RawMessage(`{"name":"x"}`), noSecrets); !errors.As(err, &inv) {
			t.Errorf("connector on built-in = %v", err)
		}
		if err := SetConnector(context.Background(), tx, e.app, "fake", json.RawMessage(`{}`), noSecrets); !errors.As(err, &inv) {
			t.Errorf("bad config = %v", err)
		}
		if err := SetConnector(context.Background(), tx, e.app, "ldap", nil, noSecrets); !errors.As(err, &inv) {
			t.Errorf("unknown kind = %v", err)
		}
		return nil
	})
}

func TestAggregateDue(t *testing.T) {
	e := newEnv(t)
	e.set(connector.Snapshot{Accounts: []connector.Account{{NativeID: "u1", Active: true}}}, nil)
	runs, err := AggregateDue(context.Background(), e.pool, e.tenant, time.Hour, noSecrets)
	if err != nil || len(runs) != 1 {
		t.Fatalf("first pass = %v, %v", runs, err)
	}
	runs, err = AggregateDue(context.Background(), e.pool, e.tenant, time.Hour, noSecrets)
	if err != nil || len(runs) != 0 {
		t.Errorf("second pass within the hour = %v, %v", runs, err)
	}
}

// TestAggregateLargeApplication stores more accounts and entitlements than
// fit in one batch, then drops some, so every batch boundary is crossed.
func TestAggregateLargeApplication(t *testing.T) {
	e := newEnv(t)
	n := db.BatchSize*2 + 7
	snap := connector.Snapshot{}
	for i := range n {
		snap.Entitlements = append(snap.Entitlements, connector.Entitlement{NativeID: fmt.Sprintf("g%d", i), Name: fmt.Sprintf("Group %d", i)})
		snap.Accounts = append(snap.Accounts, connector.Account{
			NativeID: fmt.Sprintf("u%d", i), Username: fmt.Sprintf("user%d", i), Active: true,
			Entitlements: []string{fmt.Sprintf("g%d", i), fmt.Sprintf("g%d", (i+1)%n)},
		})
	}
	e.set(snap, nil)
	run := e.aggregate()
	if run.Status != RunSucceeded || run.Counts["accounts_new"] != n || run.Counts["entitlements_new"] != n || run.Counts["memberships_added"] != 2*n {
		t.Fatalf("first run = %s %v", run.Status, run.Counts)
	}

	// Every account drops its second group, and the last ten accounts go.
	for i := range snap.Accounts {
		snap.Accounts[i].Entitlements = snap.Accounts[i].Entitlements[:1]
	}
	snap.Accounts = snap.Accounts[:n-10]
	e.set(snap, nil)
	run = e.aggregate()
	if run.Status != RunSucceeded || run.Counts["accounts_new"] != 0 || run.Counts["accounts_deleted"] != 10 ||
		run.Counts["memberships_added"] != 0 || run.Counts["memberships_removed"] != (n-10)+20 {
		t.Fatalf("second run = %s %v", run.Status, run.Counts)
	}
}

// Applications with no API load their accounts from a file: checked first
// with a dry run that changes nothing, then imported, then imported again
// with changes, like any aggregation.
func TestImportFile(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana", "ana@example.com", "E1")
	ctx := context.Background()
	e.tx(func(tx pgx.Tx) error {
		return SetConnector(ctx, tx, e.app, flatfile.Kind, json.RawMessage(`{"columns": {"username": "Login"}}`), noSecrets)
	})
	imp := func(file string, dryRun bool) Run {
		t.Helper()
		run, err := Import(ctx, e.pool, e.tenant, e.app, "identity:tester", strings.NewReader(file), dryRun)
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	file := "Login,email,employee_id,status,entitlements,last_login\n" +
		"ana,ana@example.com,E1,active,Admin;Billing,2026-10-01\n" +
		"ghost,,,disabled,Billing,\n"

	run := imp(file, true)
	if run.Status != RunPlanned || run.Counts["accounts_new"] != 2 || run.Counts["memberships_added"] != 3 || run.Counts["orphans"] != 1 {
		t.Fatalf("dry run = %s %v %s", run.Status, run.Counts, run.Error)
	}
	if got := e.accounts(false); len(got) != 0 {
		t.Fatalf("dry run stored accounts: %v", got)
	}
	var n int
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM entitlements WHERE application_id = $1`, e.app).Scan(&n)
	})
	if n != 0 {
		t.Fatalf("dry run stored %d entitlements", n)
	}

	run = imp(file, false)
	if run.Status != RunSucceeded || run.Counts["accounts_new"] != 2 || run.Counts["entitlements_new"] != 2 {
		t.Fatalf("import = %s %v %s", run.Status, run.Counts, run.Error)
	}
	got := e.accounts(false)
	if a := got["ana"]; a.IdentityID != ana || a.Status != "active" || len(a.Entitlements) != 2 {
		t.Errorf("ana = %+v", a)
	}
	if g := got["ghost"]; g.IdentityID != "" || g.Status != "disabled" {
		t.Errorf("ghost = %+v", g)
	}

	// Ana loses Admin and the ghost account is gone from the next export.
	run = imp("Login,entitlements\nana,Billing\n", false)
	if run.Status != RunSucceeded || run.Counts["accounts_deleted"] != 1 || run.Counts["memberships_removed"] != 2 {
		t.Fatalf("second import = %s %v %s", run.Status, run.Counts, run.Error)
	}

	// A file that says nothing of sign-ins, or an older one, keeps the
	// latest sign-in known.
	imp("Login,entitlements,last_login\nana,Billing,2026-09-01\n", false)
	var last time.Time
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT last_login_at FROM accounts WHERE application_id = $1 AND native_id = 'ana'`, e.app).Scan(&last)
	})
	if !last.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ana's last login = %v", last)
	}

	// A broken file is a failed run that changes nothing.
	run = imp("Login,status\nana,on leave\n", false)
	if run.Status != RunFailed || !strings.Contains(run.Error, `unknown status "on leave"`) {
		t.Errorf("bad file = %s %q", run.Status, run.Error)
	}
	if a := e.accounts(false)["ana"]; a.Status != "active" {
		t.Errorf("bad file changed ana: %+v", a)
	}
	var runs, audits int
	e.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM connector_runs WHERE application_id = $1`, e.app).Scan(&runs); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'application.aggregated' AND target_id = $1`, e.app).Scan(&audits)
	})
	if runs != 5 || audits != 5 {
		t.Errorf("runs = %d, audit events = %d, want 5 each", runs, audits)
	}

	// The connector cannot be read without a file, and the background
	// aggregation leaves it alone.
	if _, err := Aggregate(ctx, e.pool, e.tenant, e.app, "identity:tester", noSecrets); !errors.As(err, new(*apperr.Invalid)) {
		t.Errorf("aggregate a csv application = %v", err)
	}
	due, err := AggregateDue(ctx, e.pool, e.tenant, time.Nanosecond, noSecrets)
	if err != nil || len(due) != 0 {
		t.Errorf("due = %v, %v", due, err)
	}

	// Only csv applications take files.
	e.tx(func(tx pgx.Tx) error {
		return SetConnector(ctx, tx, e.app, "fake", json.RawMessage(`{"name": "`+e.name+`"}`), noSecrets)
	})
	if _, err := Import(ctx, e.pool, e.tenant, e.app, "identity:tester", strings.NewReader(file), false); !errors.As(err, new(*apperr.Invalid)) {
		t.Errorf("import into a scim application = %v", err)
	}
	if _, err := Import(ctx, e.pool, e.tenant, "nope", "identity:tester", strings.NewReader(file), false); !errors.Is(err, apperr.ErrNotFound) {
		t.Errorf("import into an unknown application = %v", err)
	}
}
