package provisioning

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

// fakeApp is an application in memory that the fake connector reads and
// writes, so aggregations see what provisioning did.
type fakeApp struct {
	groups   []string
	users    map[string]*connector.Account
	failAdd  map[string]bool // groups AddMember fails on
	calls    []string
	nextUser int
	// afterAdd, when set, runs after each successful AddMember.
	afterAdd func()
}

var (
	fakeMu   sync.Mutex
	fakeApps = map[string]*fakeApp{}
)

type fake struct{ name string }

func (f fake) app() *fakeApp { return fakeApps[f.name] }

func (f fake) Read(context.Context) (connector.Snapshot, error) {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	var s connector.Snapshot
	for _, g := range f.app().groups {
		s.Entitlements = append(s.Entitlements, connector.Entitlement{NativeID: g, Name: g})
	}
	for _, u := range f.app().users {
		c := *u
		c.Entitlements = slices.Clone(u.Entitlements)
		s.Accounts = append(s.Accounts, c)
	}
	return s, nil
}

func (f fake) CreateAccount(_ context.Context, a connector.NewAccount) (string, error) {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	app := f.app()
	app.nextUser++
	id := fmt.Sprintf("new%d", app.nextUser)
	app.users[id] = &connector.Account{NativeID: id, Username: a.Username, Email: a.Email, ExternalID: a.ExternalID, Active: true}
	app.calls = append(app.calls, "create "+a.Username)
	return id, nil
}

func (f fake) SetActive(_ context.Context, id string, active bool) error {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	f.app().users[id].Active = active
	f.app().calls = append(f.app().calls, fmt.Sprintf("active %s %t", id, active))
	return nil
}

func (f fake) AddMember(_ context.Context, group, id string) error {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	if f.app().failAdd[group] {
		return errors.New("403 Forbidden")
	}
	u := f.app().users[id]
	if !slices.Contains(u.Entitlements, group) {
		u.Entitlements = append(u.Entitlements, group)
	}
	f.app().calls = append(f.app().calls, "add "+group+" "+id)
	if f.app().afterAdd != nil {
		f.app().afterAdd()
	}
	return nil
}

func (f fake) RemoveMember(_ context.Context, group, id string) error {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	u := f.app().users[id]
	u.Entitlements = slices.DeleteFunc(u.Entitlements, func(g string) bool { return g == group })
	f.app().calls = append(f.app().calls, "remove "+group+" "+id)
	return nil
}

type readOnly struct{}

func (readOnly) Read(context.Context) (connector.Snapshot, error) { return connector.Snapshot{}, nil }

func init() {
	connector.Register("fakewriter", func(raw json.RawMessage, _ connector.SecretResolver) (connector.Reader, error) {
		var cfg struct{ Name string }
		if err := json.Unmarshal(raw, &cfg); err != nil || cfg.Name == "" {
			return nil, errors.New("fakewriter needs a name")
		}
		return fake{cfg.Name}, nil
	})
	connector.Register("fakereader", func(json.RawMessage, connector.SecretResolver) (connector.Reader, error) {
		return readOnly{}, nil
	})
}

func noSecrets(string) (string, error) { return "", errors.New("no secrets in tests") }

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	app    string
	fake   *fakeApp
}

func newEnv(t *testing.T, groups ...string) *env {
	t.Helper()
	pool := dbtest.Open(t)
	e := &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "provisioning")}
	if err := authz.EnsureBuiltinApp(context.Background(), pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	e.fake = &fakeApp{groups: groups, users: map[string]*connector.Account{}, failAdd: map[string]bool{}}
	fakeMu.Lock()
	fakeApps[e.tenant] = e.fake
	fakeMu.Unlock()
	e.tx(func(tx pgx.Tx) error {
		app, err := catalog.CreateApplication(context.Background(), tx, e.tenant, "CRM", "")
		if err != nil {
			return err
		}
		e.app = app.ID
		return accounts.SetConnector(context.Background(), tx, app.ID, "fakewriter", json.RawMessage(`{"name": "`+e.tenant+`"}`), noSecrets)
	})
	return e
}

func (e *env) tx(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(context.Background(), e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) user(id, email string, active bool, groups ...string) {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	e.fake.users[id] = &connector.Account{NativeID: id, Username: email, Email: email, Active: active, Entitlements: groups}
}

func (e *env) calls() []string {
	fakeMu.Lock()
	defer fakeMu.Unlock()
	c := e.fake.calls
	e.fake.calls = nil
	sort.Strings(c)
	return c
}

func (e *env) identity(name, email, status string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email, status) VALUES ($1, $2, nullif($3, ''), $4) RETURNING id`,
			e.tenant, name, email, status).Scan(&id)
	})
	return id
}

func (e *env) entitlement(native string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT id FROM entitlements WHERE application_id = $1 AND native_id = $2`, e.app, native).Scan(&id)
	})
	return id
}

// assign gives an identity an entitlement directly, or a new role holding
// the given entitlements.
func (e *env) assign(identity string, entitlements ...string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		ctx := context.Background()
		if len(entitlements) == 1 {
			return tx.QueryRow(ctx,
				`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by)
				 VALUES ($1, $2, $3, 'manual', 'test') RETURNING id`, e.tenant, identity, entitlements[0]).Scan(&id)
		}
		var role string
		if err := tx.QueryRow(ctx, `INSERT INTO roles (tenant_id, name) VALUES ($1, 'Seller') RETURNING id`, e.tenant).Scan(&role); err != nil {
			return err
		}
		for _, ent := range entitlements {
			if _, err := tx.Exec(ctx, `INSERT INTO role_entitlements (tenant_id, role_id, entitlement_id) VALUES ($1, $2, $3)`, e.tenant, role, ent); err != nil {
				return err
			}
		}
		return tx.QueryRow(ctx,
			`INSERT INTO access_assignments (tenant_id, identity_id, role_id, origin, granted_by)
			 VALUES ($1, $2, $3, 'manual', 'test') RETURNING id`, e.tenant, identity, role).Scan(&id)
	})
	return id
}

func (e *env) aggregate() {
	e.t.Helper()
	run, err := accounts.Aggregate(context.Background(), e.pool, e.tenant, e.app, "test", noSecrets)
	if err != nil || run.Status != accounts.RunSucceeded {
		e.t.Fatalf("aggregate = %+v, %v", run, err)
	}
}

func (e *env) settings(s Settings) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := SetSettings(context.Background(), tx, e.app, s, noSecrets)
		return err
	})
}

func (e *env) provision(opts Options) Run {
	e.t.Helper()
	run, err := Provision(context.Background(), e.pool, e.tenant, e.app, "test", opts, noSecrets)
	if err != nil {
		e.t.Fatal(err)
	}
	return run
}

func ops(run Run) []string {
	var out []string
	for _, a := range run.Actions {
		out = append(out, a.Op+" "+a.NativeAccount+" "+a.NativeEntitlement+" "+a.Status)
	}
	sort.Strings(out)
	return out
}

func TestProvision(t *testing.T) {
	e := newEnv(t, "sales", "admins")
	ana := e.identity("Ana", "ana@example.com", "active")
	luis := e.identity("Luis", "luis@example.com", "active")
	nomail := e.identity("No Mail", "", "active")
	pepe := e.identity("Pepe", "pepe@example.com", "terminated")
	e.user("u1", "ana@example.com", true, "admins") // admins granted outside Grantra
	e.user("u3", "pepe@example.com", true, "sales")
	e.user("u9", "svc-backup", true, "admins") // an orphan

	ctx := context.Background()
	var conflict *apperr.Conflict
	if _, err := Provision(ctx, e.pool, e.tenant, e.app, "test", Options{}, noSecrets); !errors.As(err, &conflict) {
		t.Errorf("provision while off = %v", err)
	}
	if _, err := Provision(ctx, e.pool, e.tenant, e.app, "test", Options{DryRun: true}, noSecrets); !errors.As(err, &conflict) {
		t.Errorf("dry run without an aggregation = %v", err)
	}
	e.aggregate()
	sales, admins := e.entitlement("sales"), e.entitlement("admins")
	e.assign(ana, sales)
	e.assign(luis, sales, admins)
	e.assign(nomail, sales)

	// A dry run works while provisioning is off and changes nothing.
	run := e.provision(Options{DryRun: true})
	want := []string{
		"add_member  admins planned",
		"add_member  sales planned",
		"add_member u1 sales planned",
		"create_account   planned",
		"disable_account u3  planned",
	}
	if got := ops(run); !slices.Equal(got, want) {
		t.Errorf("plan = %q, want %q", got, want)
	}
	if run.Status != accounts.RunPlanned || run.Counts["skipped_no_email"] != 1 || run.Counts["unassigned_kept"] != 2 {
		t.Errorf("dry run = %s %v", run.Status, run.Counts)
	}
	if c := e.calls(); len(c) != 0 {
		t.Errorf("dry run wrote %v", c)
	}

	e.settings(Settings{Mode: ModeOn})
	run = e.provision(Options{})
	if run.Status != accounts.RunSucceeded || run.ID == "" {
		t.Fatalf("run = %+v", run)
	}
	wantCalls := []string{"active u3 false", "add admins new1", "add sales new1", "add sales u1", "create luis@example.com"}
	if c := e.calls(); !slices.Equal(c, wantCalls) {
		t.Errorf("calls = %q, want %q", c, wantCalls)
	}
	var correlated string
	var provisioned int
	e.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT correlated_by FROM accounts WHERE native_id = 'new1' AND identity_id = $1`, luis).Scan(&correlated); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM account_entitlements WHERE provisioned_at IS NOT NULL`).Scan(&provisioned)
	})
	if correlated != accounts.ByProvisioning || provisioned != 3 {
		t.Errorf("local state: correlated_by %q, %d provisioned memberships", correlated, provisioned)
	}

	// An aggregation sees the same thing, keeps the link and the marks, and
	// the next run has nothing to do.
	e.aggregate()
	if run := e.provision(Options{}); len(run.Actions) != 0 || run.Status != accounts.RunSucceeded {
		t.Errorf("second run = %+v", run)
	}
	e.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT correlated_by FROM accounts WHERE native_id = 'new1'`).Scan(&correlated); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM account_entitlements WHERE provisioned_at IS NOT NULL`).Scan(&provisioned)
	})
	if correlated != accounts.ByProvisioning || provisioned != 3 {
		t.Errorf("after aggregation: correlated_by %q, %d provisioned memberships", correlated, provisioned)
	}

	// Ana loses her assignment: Grantra takes back what it gave, and leaves
	// admins, which it did not give, unless told it may.
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM access_assignments WHERE identity_id = $1`, ana)
		return err
	})
	run = e.provision(Options{})
	if got := ops(run); !slices.Equal(got, []string{"remove_member u1 sales done"}) {
		t.Errorf("after unassigning = %q", got)
	}
	e.settings(Settings{Mode: ModeOn, RemoveUnassigned: true})
	run = e.provision(Options{DryRun: true})
	want = []string{"remove_member u1 admins planned", "remove_member u3 sales planned"}
	if got := ops(run); !slices.Equal(got, want) {
		t.Errorf("with remove_unassigned = %q, want %q", got, want)
	}

	// A failing action does not stop the others.
	e.settings(Settings{Mode: ModeOn})
	e.assign(pepe, admins) // terminated: still holds nothing
	e.assign(ana, admins)  // already has it
	eva := e.identity("Eva", "eva@example.com", "active")
	e.assign(eva, sales)
	e.assign(ana, sales)
	e.fake.failAdd["sales"] = true
	run = e.provision(Options{})
	if run.Status != accounts.RunPartial || run.Counts["failed"] != 2 {
		t.Errorf("run = %s %v %q", run.Status, run.Counts, ops(run))
	}
	var audited int
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action LIKE 'provisioning.%' AND details->>'status' = 'failed'`).Scan(&audited)
	})
	if audited != 2 {
		t.Errorf("failed actions audited = %d", audited)
	}
}

func TestProvisionSafetyLimit(t *testing.T) {
	e := newEnv(t, "sales")
	// One leaver with many groups counts once.
	e.fake.groups = []string{"sales", "a", "b", "c", "d"}
	gone := e.identity("Gone", "gone@example.com", "active")
	e.user("g1", "gone@example.com", true, "sales", "a", "b", "c", "d")
	for i := range 9 {
		e.user(fmt.Sprintf("x%d", i), fmt.Sprintf("x%d", i), true)
	}
	e.aggregate()
	e.settings(Settings{Mode: ModeOn, RemoveUnassigned: true})
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE identities SET status = 'terminated' WHERE id = $1`, gone)
		return err
	})
	if run := e.provision(Options{}); run.Status != accounts.RunSucceeded || len(run.Actions) != 6 {
		t.Errorf("one leaver = %s %q", run.Status, ops(run))
	}
	e.calls()

	for i := range 5 {
		email := fmt.Sprintf("p%d@example.com", i)
		e.identity(email, email, "terminated")
		e.user(fmt.Sprintf("u%d", i), email, true)
	}
	e.aggregate()
	run := e.provision(Options{})
	if run.Status != accounts.RunRefused || run.Error == "" || len(e.calls()) != 0 {
		t.Errorf("run past the limit = %+v", run)
	}
	run = e.provision(Options{Force: true})
	if run.Status != accounts.RunSucceeded || len(e.calls()) != 5 {
		t.Errorf("forced run = %+v", run)
	}
	e.tx(func(tx pgx.Tx) error {
		runs, err := accounts.Runs(context.Background(), tx, e.app, 10)
		if len(runs) != 5 || runs[0].Kind != "provisioning" || runs[1].Status != accounts.RunRefused {
			t.Errorf("runs = %+v", runs)
		}
		return err
	})
}

func TestSettingsAndDue(t *testing.T) {
	e := newEnv(t, "sales")
	ctx := context.Background()
	var inv *apperr.Invalid
	e.tx(func(tx pgx.Tx) error {
		for _, s := range []Settings{{Mode: "maybe"}, {Mode: ModeOn, MaxChangePercent: 101}} {
			if _, err := SetSettings(ctx, tx, e.app, s, noSecrets); !errors.As(err, &inv) {
				t.Errorf("settings %+v = %v", s, err)
			}
		}
		s, err := SetSettings(ctx, tx, e.app, Settings{Mode: ModeOn}, noSecrets)
		if err != nil || s.MaxChangePercent != 10 {
			t.Errorf("default settings = %+v, %v", s, err)
		}
		// Read-only and manual applications cannot be provisioned.
		other, err := catalog.CreateApplication(ctx, tx, e.tenant, "Wiki", "")
		if err != nil {
			return err
		}
		if _, err := SetSettings(ctx, tx, other.ID, Settings{Mode: ModeOn}, noSecrets); !errors.As(err, &inv) {
			t.Errorf("manual app = %v", err)
		}
		if err := accounts.SetConnector(ctx, tx, other.ID, "fakereader", nil, noSecrets); err != nil {
			return err
		}
		if _, err := SetSettings(ctx, tx, other.ID, Settings{Mode: ModeOn}, noSecrets); !errors.As(err, &inv) {
			t.Errorf("read-only connector = %v", err)
		}
		return nil
	})

	// Nothing is due until the application has been aggregated, and runs
	// with nothing to do are not recorded.
	if runs, err := ProvisionDue(ctx, e.pool, e.tenant, noSecrets); err != nil || len(runs) != 0 {
		t.Errorf("before aggregation = %v, %v", runs, err)
	}
	e.aggregate()
	ana := e.identity("Ana", "ana@example.com", "active")
	e.assign(ana, e.entitlement("sales"))
	runs, err := ProvisionDue(ctx, e.pool, e.tenant, noSecrets)
	if err != nil || len(runs) != 1 || runs[0].Counts[OpCreateAccount] != 1 {
		t.Errorf("due = %+v, %v", runs, err)
	}
	if runs, err := ProvisionDue(ctx, e.pool, e.tenant, noSecrets); err != nil || len(runs) != 0 {
		t.Errorf("nothing left to do = %+v, %v", runs, err)
	}
}

// A run that stops halfway keeps what it already changed on record, and the
// next run picks up the rest.
func TestProvisionInterrupted(t *testing.T) {
	e := newEnv(t, "sales", "admins")
	ana := e.identity("Ana", "ana@example.com", "active")
	e.user("u1", "ana@example.com", true)
	e.aggregate()
	e.settings(Settings{Mode: ModeOn})
	e.assign(ana, e.entitlement("sales"))
	e.assign(ana, e.entitlement("admins"))

	// The caller goes away right after the first change.
	ctx, cancel := context.WithCancel(context.Background())
	e.fake.afterAdd = cancel
	run, err := Provision(ctx, e.pool, e.tenant, e.app, "test", Options{}, noSecrets)
	e.fake.afterAdd = nil
	if err != nil || run.Status != RunInterrupted || run.FinishedAt == nil {
		t.Fatalf("interrupted run = %+v, %v", run, err)
	}
	if got := len(e.calls()); got != 1 {
		t.Errorf("changes made = %d, want 1", got)
	}
	var stored []Action
	var status string
	var memberships, audited int
	e.tx(func(tx pgx.Tx) error {
		ctx := context.Background()
		if err := tx.QueryRow(ctx, `SELECT status, actions FROM connector_runs WHERE id = $1`, run.ID).Scan(&status, &stored); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM account_entitlements WHERE provisioned_at IS NOT NULL`).Scan(&memberships); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'provisioning.add_member'`).Scan(&audited)
	})
	done := 0
	for _, a := range stored {
		if a.Status == ActionDone {
			done++
		}
	}
	if status != RunInterrupted || len(stored) != 2 || done != 1 || memberships != 1 || audited != 1 {
		t.Errorf("on record: status %s, actions %+v, memberships %d, audited %d", status, stored, memberships, audited)
	}

	if run := e.provision(Options{}); run.Status != accounts.RunSucceeded || len(run.Actions) != 1 {
		t.Errorf("next run = %s %q", run.Status, ops(run))
	}
}

// Only one run writes to an application at a time; a run left "running" by a
// crash is closed once it is past the time any run may take.
func TestProvisionOneRunAtATime(t *testing.T) {
	e := newEnv(t, "sales")
	ctx := context.Background()
	ana := e.identity("Ana", "ana@example.com", "active")
	e.aggregate()
	e.settings(Settings{Mode: ModeOn})
	e.assign(ana, e.entitlement("sales"))

	var stale string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO connector_runs (tenant_id, application_id, kind, actor, status, started_at, finished_at)
			 VALUES ($1, $2, 'provisioning', 'test', 'running', now(), NULL) RETURNING id`, e.tenant, e.app).Scan(&stale)
	})
	if _, err := Provision(ctx, e.pool, e.tenant, e.app, "test", Options{}, noSecrets); !errors.Is(err, ErrRunning) {
		t.Errorf("second run = %v, want ErrRunning", err)
	}
	if runs, err := ProvisionDue(ctx, e.pool, e.tenant, noSecrets); err != nil || len(runs) != 0 {
		t.Errorf("due while running = %+v, %v", runs, err)
	}
	if run, err := accounts.Aggregate(ctx, e.pool, e.tenant, e.app, "test", noSecrets); err != nil || run.Status != accounts.RunFailed {
		t.Errorf("aggregation while running = %+v, %v", run, err)
	}
	if len(e.calls()) != 0 {
		t.Error("the application was changed while another run was in progress")
	}

	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE connector_runs SET started_at = now() - interval '1 hour' WHERE id = $1`, stale)
		return err
	})
	if run := e.provision(Options{}); run.Status != accounts.RunSucceeded {
		t.Errorf("run after a stale one = %+v", run)
	}
	var status string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT status FROM connector_runs WHERE id = $1`, stale).Scan(&status)
	})
	if status != RunInterrupted {
		t.Errorf("stale run = %s, want interrupted", status)
	}
}
