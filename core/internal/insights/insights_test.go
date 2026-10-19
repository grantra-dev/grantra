package insights

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
	return &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "insights")}
}

func (e *env) tx(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(context.Background(), e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

// id runs an INSERT ... RETURNING id, with the tenant as $1.
func (e *env) id(sql string, args ...any) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql, append([]any{e.tenant}, args...)...).Scan(&id)
	})
	return id
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), sql, append([]any{e.tenant}, args...)...)
		return err
	})
}

func (e *env) app(name string) string {
	return e.id(`INSERT INTO applications (tenant_id, name, connector) VALUES ($1, $2, 'csv') RETURNING id`, name)
}

func (e *env) ent(app, name string) string {
	return e.id(`INSERT INTO entitlements (tenant_id, application_id, native_id, name) VALUES ($1, $2, $3, $3) RETURNING id`, app, name)
}

func (e *env) person(name, dept, title string) string {
	return e.id(`INSERT INTO identities (tenant_id, display_name, attributes)
		VALUES ($1, $2, jsonb_strip_nulls(jsonb_build_object('department', nullif($3, ''), 'title', nullif($4, '')))) RETURNING id`,
		name, dept, title)
}

// assign gives an entitlement, days ago.
func (e *env) assign(who, ent, origin string, days int) string {
	return e.id(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, granted_at, rule_id)
		VALUES ($1, $2, $3, $4, 'test', now() - make_interval(days => $5),
		        CASE WHEN $4 = 'birthright' THEN (SELECT id FROM access_rules LIMIT 1) END) RETURNING id`, who, ent, origin, days)
}

// account opens an account that holds ents; lastLogin is days ago, or none
// when negative.
func (e *env) account(app, who string, lastLogin int, ents ...string) {
	acct := e.id(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, last_login_at)
		VALUES ($1, $2, $3, gen_random_uuid()::text, CASE WHEN $4 >= 0 THEN now() - make_interval(days => $4) END) RETURNING id`,
		app, who, lastLogin)
	for _, ent := range ents {
		e.exec(`INSERT INTO account_entitlements (tenant_id, account_id, entitlement_id) VALUES ($1, $2, $3)`, acct, ent)
	}
}

func TestUnusedAccess(t *testing.T) {
	e := newEnv(t)
	crm, wiki, vaultApp := e.app("Salesforce"), e.app("Wiki"), e.app("Privileged vault")
	admin, reports, edit := e.ent(crm, "Admin"), e.ent(crm, "Reports"), e.ent(wiki, "Edit")
	root := e.ent(vaultApp, "Use root")
	rootAccount := e.id(`INSERT INTO vault_accounts (tenant_id, name, username, kind, entitlement_id, created_by)
		VALUES ($1, 'root', 'root', 'password', $2, 'test') RETURNING id`, root)
	e.id(`INSERT INTO access_rules (tenant_id, name) VALUES ($1, 'Everyone') RETURNING id`)

	ana := e.person("Ana", "", "")
	stale := e.assign(ana, admin, "manual", 200)
	e.account(crm, ana, 150)

	bob := e.person("Bob", "", "")
	e.assign(bob, admin, "request", 200)
	e.account(crm, bob, 5)

	// Hal's account says nothing, but his sign-ins name the application.
	hal := e.person("Hal", "", "")
	e.assign(hal, admin, "manual", 200)
	e.account(crm, hal, -1)
	e.exec(`INSERT INTO signin_events (tenant_id, source, external_id, occurred_at, identity_id, outcome, application)
		VALUES ($1, 'okta', 'e1', now() - interval '3 days', $2, 'success', 'SALESFORCE')`, hal)

	// Nothing says whether the wiki is used: no suggestion either way.
	carla := e.person("Carla", "", "")
	e.assign(carla, edit, "manual", 200)

	dan := e.person("Dan", "", "")
	e.assign(dan, reports, "manual", 10)

	// A rule gives birthright access; the rule is what to change.
	e.assign(e.person("Ivy", "", ""), admin, "birthright", 200)

	eve := e.person("Eve", "", "")
	eligible := e.id(`INSERT INTO access_eligibilities (tenant_id, identity_id, entitlement_id, granted_by, granted_at)
		VALUES ($1, $2, $3, 'test', now() - interval '200 days') RETURNING id`, eve, root)
	fay := e.person("Fay", "", "")
	e.exec(`INSERT INTO access_eligibilities (tenant_id, identity_id, entitlement_id, granted_by, granted_at, last_activated_at)
		VALUES ($1, $2, $3, 'test', now() - interval '200 days', now() - interval '10 days')`, fay, root)

	gus := e.person("Gus", "", "")
	e.assign(gus, root, "manual", 200)
	e.exec(`INSERT INTO vault_checkouts (tenant_id, vault_account_id, identity_id, version, reason, checked_out_at, expires_at)
		VALUES ($1, $2, $3, 1, 'patching', now() - interval '20 days', now() - interval '19 days')`, rootAccount, gus)

	var got []Unused
	e.tx(func(tx pgx.Tx) (err error) {
		got, err = UnusedAccess(context.Background(), tx, UnusedFilter{Limit: 50})
		return err
	})
	var ids []string
	for _, u := range got {
		ids = append(ids, u.ID)
	}
	want := []string{stale, eligible}
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("unused = %+v, want Ana's admin access and Eve's eligibility", got)
	}
	for _, u := range got {
		switch u.ID {
		case stale:
			if u.LastUsedAt == nil || time.Since(*u.LastUsedAt) < 149*24*time.Hour || u.Eligible || u.Application != "Salesforce" {
				t.Fatalf("Ana's = %+v", u)
			}
		case eligible:
			if u.LastUsedAt != nil || !u.Eligible || u.Origin != "eligibility" {
				t.Fatalf("Eve's = %+v", u)
			}
		}
	}

	// A page at a time, and for one person.
	e.tx(func(tx pgx.Tx) (err error) {
		got, err = UnusedAccess(context.Background(), tx, UnusedFilter{Limit: 1})
		if err != nil || len(got) != 1 || got[0].ID != want[0] {
			return fmt.Errorf("first page = %+v, %v", got, err)
		}
		got, err = UnusedAccess(context.Background(), tx, UnusedFilter{Limit: 1, After: want[0]})
		if err != nil || len(got) != 1 || got[0].ID != want[1] {
			return fmt.Errorf("second page = %+v, %v", got, err)
		}
		got, err = UnusedAccess(context.Background(), tx, UnusedFilter{Limit: 50, IdentityID: eve})
		if err != nil || len(got) != 1 || got[0].ID != eligible {
			return fmt.Errorf("Eve's = %+v, %v", got, err)
		}
		return nil
	})
}

func TestSuggestions(t *testing.T) {
	e := newEnv(t)
	crm, wiki := e.app("Salesforce"), e.app("Wiki")
	reports, read, wikiAdmin := e.ent(crm, "Reports"), e.ent(wiki, "Read"), e.ent(wiki, "Admin")
	// Salesforce tells when its accounts last signed in.
	someone := e.person("Someone", "Elsewhere", "")
	e.account(crm, someone, 1)

	var sales []string
	for i := range 6 {
		sales = append(sales, e.person(fmt.Sprintf("Seller %d", i), "Sales", ""))
	}
	zed := sales[0]
	e.assign(zed, wikiAdmin, "manual", 200)
	for _, p := range sales[:5] {
		e.assign(p, read, "manual", 200)
	}
	// Zed signed in to Salesforce long ago; Yan did yesterday. Most of
	// Sales holds Reports.
	e.account(crm, zed, 120, reports)
	yan := sales[1]
	e.account(crm, yan, 1, reports)
	e.account(crm, sales[2], 1, reports)
	e.account(crm, sales[3], 1, reports)

	// Without a department, peers are the people with the same manager.
	boss := e.person("Boss", "", "")
	var team []string
	for i := range 4 {
		team = append(team, e.id(`INSERT INTO identities (tenant_id, display_name, manager_id) VALUES ($1, $2, $3) RETURNING id`,
			fmt.Sprintf("Team %d", i), boss))
	}
	for _, p := range team {
		e.assign(p, read, "manual", 10)
	}

	long := time.Now().AddDate(0, 0, -200)
	items := []Access{
		{IdentityID: zed, EntitlementID: reports, GrantedAt: long},   // unused, however common
		{IdentityID: yan, EntitlementID: reports, GrantedAt: long},   // used and common
		{IdentityID: zed, EntitlementID: read, GrantedAt: long},      // untracked, common
		{IdentityID: zed, EntitlementID: wikiAdmin, GrantedAt: long}, // untracked, nobody else has it
		{IdentityID: team[0], EntitlementID: read, GrantedAt: long},  // the team has it
		{IdentityID: boss, EntitlementID: read, GrantedAt: long},     // nobody to compare with
	}
	var got []Suggestion
	e.tx(func(tx pgx.Tx) (err error) {
		got, err = Suggest(context.Background(), tx, items)
		return err
	})
	type want struct {
		action, group string
		signals       []string
		peers, held   int
	}
	for i, w := range []want{
		{"revoke", "department", []string{SignalUnused, SignalCommon}, 5, 3},
		{"keep", "department", []string{SignalUsed, SignalCommon}, 5, 3},
		{"keep", "department", []string{SignalCommon}, 5, 4},
		{"revoke", "department", []string{SignalRare}, 5, 0},
		{"keep", "team", []string{SignalCommon}, 3, 3},
		{"", "", []string{}, 0, 0},
	} {
		s := got[i]
		if s.Action != w.action || s.PeerGroup != w.group || !slices.Equal(s.Signals, w.signals) || s.Peers != w.peers || s.PeerHolders != w.held {
			t.Errorf("item %d: %+v, want %+v", i, s, w)
		}
	}
	if got[0].Department != "Sales" || got[4].Department != "" || !got[0].Tracked || got[2].Tracked {
		t.Errorf("departments and tracking: %+v", got)
	}
	e.tx(func(tx pgx.Tx) error {
		none, err := Suggest(context.Background(), tx, nil)
		if err != nil || len(none) != 0 {
			return fmt.Errorf("no items: %v, %v", none, err)
		}
		return nil
	})
}

func TestSuggestRoles(t *testing.T) {
	e := newEnv(t)
	gh, aws := e.app("GitHub"), e.app("AWS")
	devs, ci, pager, awsRead, all := e.ent(gh, "Developers"), e.ent(gh, "CI"), e.ent(aws, "PagerDuty"), e.ent(aws, "Read only"), e.ent(gh, "Everyone")
	jit := e.ent(aws, "Admin")
	e.exec(`UPDATE entitlements SET jit_only = true WHERE tenant_id = $1 AND id = $2`, jit)
	e.id(`INSERT INTO access_rules (tenant_id, name) VALUES ($1, 'Everyone') RETURNING id`)

	var eng []string
	for i := range 7 {
		title := "Developer"
		if i < 5 {
			title = "SRE"
		}
		eng = append(eng, e.person(fmt.Sprintf("Engineer %d", i), "Engineering", title))
	}
	// Six of seven engineers hold Developers and CI, one by one, some only
	// in GitHub itself. The SREs also hold PagerDuty and read AWS.
	for i, p := range eng[:6] {
		if i%2 == 0 {
			e.assign(p, devs, "manual", 30)
			e.assign(p, ci, "request", 30)
		} else {
			e.account(gh, p, -1, devs, ci)
		}
		e.assign(p, all, "birthright", 30)
		e.account(aws, p, -1, jit)
	}
	for _, p := range eng[:5] {
		e.account(aws, p, -1, pager, awsRead)
	}
	e.assign(eng[6], all, "birthright", 30)
	// Departments too small say nothing.
	for i := range 3 {
		p := e.person(fmt.Sprintf("Lawyer %d", i), "Legal", "")
		e.assign(p, devs, "manual", 30)
		e.assign(p, ci, "manual", 30)
	}

	var got []RoleSuggestion
	e.tx(func(tx pgx.Tx) (err error) {
		got, err = SuggestRoles(context.Background(), tx)
		return err
	})
	names := func(s RoleSuggestion) []string {
		var out []string
		for _, e := range s.Entitlements {
			out = append(out, e.Name)
		}
		return out
	}
	if len(got) != 2 {
		t.Fatalf("suggestions = %+v, want Engineering and its SREs", got)
	}
	// The department, whose role would replace the most grants, first. Then
	// the SREs: PagerDuty and AWS read (all five); what the whole of
	// Engineering would get anyway is left to the department's role.
	dept, sre := got[0], got[1]
	if sre.Name != "Engineering · SRE" || sre.People != 5 || sre.HaveAll != 5 ||
		!slices.Equal(names(sre), []string{"PagerDuty", "Read only"}) || sre.Conditions["title"][0] != "SRE" {
		t.Errorf("SRE suggestion = %+v", sre)
	}
	if dept.Name != "Engineering" || dept.People != 7 || dept.HaveAll != 6 || !slices.Equal(names(dept), []string{"CI", "Developers"}) ||
		dept.Entitlements[0].Holders != 6 || dept.Entitlements[0].Application != "GitHub" || len(dept.Conditions) != 1 {
		t.Errorf("Engineering suggestion = %+v", dept)
	}
}
