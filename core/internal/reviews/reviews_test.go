package reviews

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	e := &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "reviews")}
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

func (e *env) identity(name, manager string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, manager_id) VALUES ($1, $2, nullif($3, '')::uuid) RETURNING id`,
			e.tenant, name, manager).Scan(&id)
	})
	return id
}

// app creates an application owned by owner with one entitlement.
func (e *env) app(name, owner string) (string, string) {
	e.t.Helper()
	var app, ent string
	e.tx(func(tx pgx.Tx) error {
		a, err := catalog.CreateApplication(context.Background(), tx, e.tenant, name, owner)
		if err != nil {
			return err
		}
		en, err := catalog.CreateEntitlement(context.Background(), tx, e.tenant, a.ID, catalog.EntitlementInput{NativeID: "users", Name: name + " users"})
		app, ent = a.ID, en.ID
		return err
	})
	return app, ent
}

func (e *env) assign(identity, entitlement, origin string) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		var rule *string
		if origin == "birthright" {
			var id string
			if err := tx.QueryRow(context.Background(), `INSERT INTO access_rules (tenant_id, name) VALUES ($1, 'everyone') RETURNING id`, e.tenant).Scan(&id); err != nil {
				return err
			}
			rule = &id
		}
		_, err := tx.Exec(context.Background(),
			`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, rule_id, granted_by) VALUES ($1, $2, $3, $4, $5, 'test')`,
			e.tenant, identity, entitlement, origin, rule)
		return err
	})
}

func (e *env) launch(in Input) (Campaign, error) {
	e.t.Helper()
	var (
		c   Campaign
		err error
	)
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		c, err = Launch(context.Background(), tx, e.tenant, "identity:admin", in)
		return err
	})
	return c, err
}

func (e *env) items(f ItemFilter) []Item {
	e.t.Helper()
	if f.Limit == 0 {
		f.Limit = 100
	}
	var out []Item
	e.tx(func(tx pgx.Tx) (err error) {
		out, err = Items(context.Background(), tx, f)
		return err
	})
	return out
}

func (e *env) decide(actor, id, decision string) (Item, error) {
	e.t.Helper()
	var (
		it  Item
		err error
	)
	err = db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		cur, err := GetItem(context.Background(), tx, id)
		if err != nil {
			return err
		}
		it, err = Decide(context.Background(), tx, e.tenant, "identity:"+actor, cur, decision, "")
		return err
	})
	return it, err
}

func (e *env) holds(identity, entitlement string) bool {
	e.t.Helper()
	var ok bool
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`SELECT EXISTS (SELECT 1 FROM access_assignments WHERE identity_id = $1 AND entitlement_id = $2)`, identity, entitlement).Scan(&ok)
	})
	return ok
}

func TestCampaign(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	luis := e.identity("Luis", boss)
	orphan := e.identity("Orphan", "")
	owner := e.identity("Owner", "")
	crmApp, crm := e.app("CRM", owner)
	_, wiki := e.app("Wiki", "")
	e.assign(ana, crm, "manual")
	e.assign(luis, crm, "request")
	e.assign(orphan, crm, "manual")
	e.assign(ana, wiki, "manual")
	e.assign(luis, wiki, "birthright") // rules give it: not reviewed

	var inv *apperr.Invalid
	if _, err := e.launch(Input{Name: "x", Reviewer: "hr"}); !errors.As(err, &inv) {
		t.Errorf("bad reviewer = %v", err)
	}
	c, err := e.launch(Input{Name: "Q4", ApplicationIDs: []string{crmApp}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Status != Active || c.Progress[Pending] != 3 || c.Reviewer != ByManager || c.OnMissing != Keep {
		t.Fatalf("campaign = %+v", c)
	}
	var conflict *apperr.Conflict
	if _, err := e.launch(Input{Name: "Q4"}); !errors.As(err, &conflict) {
		t.Errorf("same name = %v", err)
	}

	mine := e.items(ItemFilter{Mine: true, Me: boss})
	if len(mine) != 2 {
		t.Fatalf("boss reviews %d items", len(mine))
	}
	if adm := e.items(ItemFilter{Mine: true, Me: owner, Admin: true}); len(adm) != 1 || adm[0].IdentityID != orphan {
		t.Errorf("admin queue = %+v", adm)
	}
	var anaItem, luisItem Item
	for _, it := range mine {
		switch it.IdentityID {
		case ana:
			anaItem = it
		case luis:
			luisItem = it
		}
	}
	if luisItem.Origin != "request" || luisItem.Application != "CRM" {
		t.Errorf("item = %+v", luisItem)
	}

	if _, err := e.decide(boss, anaItem.ID, "maybe"); !errors.As(err, &inv) {
		t.Errorf("bad decision = %v", err)
	}
	if it, err := e.decide(boss, anaItem.ID, Revoke); err != nil || it.Decision != Revoke {
		t.Fatalf("revoke = %+v, %v", it, err)
	}
	if e.holds(ana, crm) || !e.holds(ana, wiki) {
		t.Errorf("revoking the CRM review touched the wrong access")
	}
	if _, err := e.decide(boss, anaItem.ID, Keep); !errors.As(err, &conflict) {
		t.Errorf("deciding twice = %v", err)
	}
	if _, err := e.decide(boss, luisItem.ID, Keep); err != nil {
		t.Fatal(err)
	}
	if !e.holds(luis, crm) {
		t.Error("kept access is gone")
	}

	// The last item closes the campaign.
	adm := e.items(ItemFilter{Mine: true, Me: owner, Admin: true})
	if _, err := e.decide(owner, adm[0].ID, Keep); err != nil {
		t.Fatal(err)
	}
	e.tx(func(tx pgx.Tx) error {
		c, err = Get(context.Background(), tx, c.ID)
		return err
	})
	if c.Status != Completed || c.ClosedAt == nil || c.Progress[Keep] != 2 || c.Progress[Revoke] != 1 {
		t.Errorf("campaign after the last item = %+v", c)
	}
	if left := e.items(ItemFilter{Mine: true, Me: boss}); len(left) != 0 {
		t.Errorf("finished campaigns stay in the queue: %+v", left)
	}
}

func TestOwnersAndDeadlines(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	owner := e.identity("Owner", "")
	_, crm := e.app("CRM", owner)
	_, own := e.app("Ownerware", owner)
	e.assign(ana, crm, "manual")
	e.assign(owner, own, "manual") // the owner's own access goes to admins

	c, err := e.launch(Input{Name: "Owners", Reviewer: ByOwner, OnMissing: Revoke, DueInDays: 7})
	if err != nil {
		t.Fatal(err)
	}
	// Left out, reviewers get what the tenant set.
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `INSERT INTO tenant_settings (tenant_id, key, value, updated_by) VALUES ($1, $2, 30, 'test')`,
			e.tenant, settings.ReviewDefaultDueDays)
		return err
	})
	if d, err := e.launch(Input{Name: "Default length"}); err != nil || time.Until(d.DueAt) < 29*24*time.Hour || time.Until(d.DueAt) > 30*24*time.Hour {
		t.Errorf("campaign with no length = due %v, %v; want the tenant's 30 days", d.DueAt, err)
	}
	if got := e.items(ItemFilter{Mine: true, Me: owner}); len(got) != 1 || got[0].IdentityID != ana {
		t.Errorf("owner reviews = %+v", got)
	}
	if got := e.items(ItemFilter{Mine: true, Me: owner, Admin: true}); len(got) != 1 {
		t.Errorf("an admin never reviews their own access: %+v", got)
	}

	// Nobody reviewed in time: on_missing revokes everything left.
	ctx := context.Background()
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE review_campaigns SET due_at = now() - interval '1 minute' WHERE id = $1`, c.ID)
		return err
	})
	if n, err := CloseDue(ctx, e.pool, e.tenant); err != nil || n != 1 {
		t.Fatalf("close due = %d, %v", n, err)
	}
	if e.holds(ana, crm) || e.holds(owner, own) {
		t.Error("access nobody reviewed was kept")
	}
	all := e.items(ItemFilter{CampaignID: c.ID})
	for _, it := range all {
		if it.Decision != Revoke || it.DecidedBy != "system:reviews" || it.Comment == "" {
			t.Errorf("item = %+v", it)
		}
	}
	if n, err := CloseDue(ctx, e.pool, e.tenant); err != nil || n != 0 {
		t.Errorf("second pass = %d, %v", n, err)
	}

	// Closing by hand with on_missing keep; and an empty campaign closes at once.
	e.assign(ana, crm, "request")
	c2, err := e.launch(Input{Name: "Manual close"})
	if err != nil {
		t.Fatal(err)
	}
	e.tx(func(tx pgx.Tx) error {
		c2, err = Close(ctx, tx, e.tenant, "identity:admin", c2.ID)
		return err
	})
	if c2.Status != Completed || c2.Progress[Keep] != 1 || !e.holds(ana, crm) {
		t.Errorf("closed by hand = %+v", c2)
	}
	emptyApp, _ := e.app("Empty", "")
	c3, err := e.launch(Input{Name: "Nothing", ApplicationIDs: []string{emptyApp}})
	if err != nil || c3.Status != Completed {
		t.Errorf("empty campaign = %+v, %v", c3, err)
	}
}

// Eligibilities are reviewed as their own items, whether an activation is
// running or not; activations are not. Revoking one ends the activation too.
func TestReviewEligibilities(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	boss := e.identity("Boss", "")
	ana, luis, eva := e.identity("Ana", boss), e.identity("Luis", boss), e.identity("Eva", boss)
	_, ent := e.app("Cloud", "")
	eligible := func(who string, expires *time.Time) string {
		var id string
		e.tx(func(tx pgx.Tx) (err error) {
			id, err = access.MakeEligible(ctx, tx, e.tenant, "identity:admin", who, access.Target{EntitlementID: ent}, expires, "")
			return err
		})
		return id
	}
	eligible(ana, nil)
	luisEl := eligible(luis, nil)
	// An eligibility that has ended is not reviewed.
	evaEl := eligible(eva, nil)
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE access_eligibilities SET expires_at = now() - interval '1 minute' WHERE id = $1`, evaEl)
		return err
	})
	e.tx(func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `SELECT id FROM access_eligibilities WHERE identity_id = $1`, ana).Scan(&id); err != nil {
			return err
		}
		el, err := access.GetEligibility(ctx, tx, id, true)
		if err != nil {
			return err
		}
		_, err = access.Activate(ctx, tx, e.tenant, "identity:"+ana, el, 1, "deploy", "")
		return err
	})
	if _, err := e.launch(Input{Name: "Q4"}); err != nil {
		t.Fatal(err)
	}
	items := e.items(ItemFilter{Mine: true, Me: boss})
	who := map[string]Item{}
	for _, it := range items {
		if it.Origin != OriginEligibility || it.TargetID != ent {
			t.Errorf("item = %+v, want only eligibilities", it)
		}
		who[it.IdentityID] = it
	}
	if len(items) != 2 || who[ana].ID == "" || who[luis].ID == "" {
		t.Fatalf("items = %+v", items)
	}
	if _, err := e.decide(boss, who[ana].ID, Revoke); err != nil {
		t.Fatal(err)
	}
	if _, err := e.decide(boss, who[luis].ID, Keep); err != nil {
		t.Fatal(err)
	}
	var left []string
	e.tx(func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM access_eligibilities WHERE identity_id IN ($1, $2)`, ana, luis)
		if err != nil {
			return err
		}
		left, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if len(left) != 1 || left[0] != luisEl || e.holds(ana, ent) {
		t.Errorf("after the review: eligibilities %v (want Luis's), Ana holds = %v", left, e.holds(ana, ent))
	}
	// The audit log ties the removal to the review decision.
	var linked int
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE action = 'review_item.decided' AND target_id = $1 AND details->>'eligibility_id' IS NOT NULL`, who[ana].ID).Scan(&linked)
	})
	if linked != 1 {
		t.Errorf("%d review decisions name the eligibility they removed", linked)
	}
}

// Revoking a just-in-time activation also takes away the eligibility behind
// it, or it could be activated again at once. Campaigns launched before
// eligibilities were reviewed have such items.
func TestRevokeActivation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	_, ent := e.app("Cloud", "")
	var assignment string
	e.tx(func(tx pgx.Tx) error {
		id, err := access.MakeEligible(ctx, tx, e.tenant, "identity:admin", ana, access.Target{EntitlementID: ent}, nil, "")
		if err != nil {
			return err
		}
		el, err := access.GetEligibility(ctx, tx, id, true)
		if err != nil {
			return err
		}
		a, err := access.Activate(ctx, tx, e.tenant, "identity:"+ana, el, 1, "deploy", "")
		assignment = a.AssignmentID
		return err
	})
	c, err := e.launch(Input{Name: "Q4"})
	if err != nil {
		t.Fatal(err)
	}
	var item string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`INSERT INTO review_items (tenant_id, campaign_id, assignment_id, identity_id, entitlement_id, origin, granted_at, reviewer_id)
			 VALUES ($1, $2, $3, $4, $5, 'activation', now(), $6) RETURNING id`, e.tenant, c.ID, assignment, ana, ent, boss).Scan(&item)
	})
	if _, err := e.decide(boss, item, Revoke); err != nil {
		t.Fatal(err)
	}
	var n int
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM access_eligibilities WHERE identity_id = $1`, ana).Scan(&n)
	})
	if n != 0 || e.holds(ana, ent) {
		t.Errorf("after revoking the activation: %d eligibilities, holds = %v", n, e.holds(ana, ent))
	}
}
