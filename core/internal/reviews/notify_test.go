package reviews

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
)

// mails returns how many items each queued email of kind names, by
// recipient, and empties the outbox.
func (e *env) mails(kind string) map[string]float64 {
	e.t.Helper()
	out := map[string]float64{}
	e.tx(func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`DELETE FROM notification_outbox WHERE kind = $1 RETURNING identity_id, (data->>'items')::float8`, kind)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var n float64
			if err := rows.Scan(&id, &n); err != nil {
				return err
			}
			out[id] = n
		}
		return rows.Err()
	})
	return out
}

func TestReviewNotifications(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	bob := e.identity("Bob", boss)
	loner := e.identity("Loner", "")
	admin := e.identity("Admin", "")
	_, crm := e.app("CRM", "")
	_, erp := e.app("ERP", "")
	e.tx(func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE identities SET email = display_name || '@example.com' WHERE tenant_id = $1`, e.tenant); err != nil {
			return err
		}
		return authz.Grant(ctx, tx, e.tenant, authz.RoleAdmin, admin, nil)
	})
	e.assign(ana, crm, "manual")
	e.assign(ana, erp, "manual")
	e.assign(bob, crm, "request")
	e.assign(loner, crm, "manual")
	e.mails("review_assigned")

	c, err := e.launch(Input{Name: "Q4"})
	if err != nil {
		t.Fatal(err)
	}
	// Boss reviews three; Loner has no manager, so the admin reviews one
	// (plus their own Grantra admin role, which campaigns leave out).
	got := e.mails("review_assigned")
	if len(got) != 2 || got[boss] != 3 || got[admin] != 1 {
		t.Errorf("assigned = %v, want boss 3 and admin 1", got)
	}

	if n, err := RemindDue(ctx, e.pool, e.tenant); err != nil || n != 0 {
		t.Fatalf("RemindDue two weeks out = %d, %v", n, err)
	}
	items := e.items(ItemFilter{CampaignID: c.ID, Mine: true, Me: boss})
	if _, err := e.decide(boss, items[0].ID, Keep); err != nil {
		t.Fatal(err)
	}
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE review_campaigns SET due_at = now() + interval '1 day' WHERE id = $1`, c.ID)
		return err
	})
	for range 2 {
		if _, err := RemindDue(ctx, e.pool, e.tenant); err != nil {
			t.Fatal(err)
		}
	}
	got = e.mails("campaign_due_soon")
	if len(got) != 2 || got[boss] != 2 || got[admin] != 1 {
		t.Errorf("reminders = %v, want boss 2 left and admin 1", got)
	}
}
