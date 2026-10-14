package requests

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/notify"
)

type mail struct {
	to, kind, status string
}

// outbox returns the queued notifications, oldest first, and empties it.
func (e *env) outbox() []mail {
	e.t.Helper()
	var out []mail
	e.tx(func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`DELETE FROM notification_outbox RETURNING id, identity_id, kind, coalesce(data->>'status', '')`)
		if err != nil {
			return err
		}
		type row struct {
			id int64
			m  mail
		}
		list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var x row
			err := r.Scan(&x.id, &x.m.to, &x.m.kind, &x.m.status)
			return x, err
		})
		slices.SortFunc(list, func(a, b row) int { return int(a.id - b.id) })
		for _, x := range list {
			out = append(out, x.m)
		}
		return err
	})
	return out
}

func TestRequestNotifications(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	owner := e.identity("Owner", "")
	crm := e.entitlement("CRM", owner)

	r, err := e.create(ana, Input{EntitlementID: crm, Justification: "I sell things"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := e.outbox(), []mail{{boss, "approval_needed", "pending"}}; !slices.Equal(got, want) {
		t.Errorf("after create = %v, want %v", got, want)
	}
	e.decide(boss, r.ID, true)
	if got, want := e.outbox(), []mail{{owner, "approval_needed", "pending"}}; !slices.Equal(got, want) {
		t.Errorf("after manager = %v, want %v", got, want)
	}
	e.decide(owner, r.ID, true)
	// Ana asked for herself: one email, not two.
	if got, want := e.outbox(), []mail{{ana, "request_decided", "approved"}}; !slices.Equal(got, want) {
		t.Errorf("after owner = %v, want %v", got, want)
	}

	// A manager asking for a report approves their own step; the owner is
	// asked, and both hear about the rejection.
	bob := e.identity("Bob", boss)
	r, err = e.create(boss, Input{BeneficiaryID: bob, EntitlementID: crm, Justification: "new hire"})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := e.outbox(), []mail{{owner, "approval_needed", "pending"}}; !slices.Equal(got, want) {
		t.Errorf("manager asking = %v, want %v", got, want)
	}
	e.decide(owner, r.ID, false)
	got := e.outbox()
	if len(got) != 2 || got[0].kind != "request_decided" || got[0].status != "rejected" ||
		!slices.ContainsFunc(got, func(m mail) bool { return m.to == boss }) ||
		!slices.ContainsFunc(got, func(m mail) bool { return m.to == bob }) {
		t.Errorf("after rejection = %v, want boss and bob told", got)
	}

	// Withdrawing a request tells nobody.
	r, err = e.create(bob, Input{EntitlementID: crm, Justification: "again"})
	if err != nil {
		t.Fatal(err)
	}
	e.outbox()
	e.tx(func(tx pgx.Tx) error {
		_, err := Cancel(context.Background(), tx, e.tenant, "identity:"+bob, r)
		return err
	})
	if got := e.outbox(); len(got) != 0 {
		t.Errorf("after cancel = %v, want nothing", got)
	}
}

func TestAdminStepNotifiesAdmins(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("Admin", "")
	other := e.identity("Other admin", "")
	loner := e.identity("Loner", "")
	optedOut := e.identity("Quiet admin", "")
	e.tx(func(tx pgx.Tx) error {
		for _, id := range []string{admin, other, optedOut} {
			if err := authz.Grant(context.Background(), tx, e.tenant, authz.RoleAdmin, id, nil); err != nil {
				return err
			}
		}
		return notify.SetEnabled(context.Background(), tx, e.tenant, optedOut, notify.ApprovalNeeded, false)
	})
	crm := e.entitlement("CRM", other)
	e.outbox()
	// Loner has no manager: the first step goes to the admins.
	if _, err := e.create(loner, Input{EntitlementID: crm, Justification: "please"}); err != nil {
		t.Fatal(err)
	}
	var to []string
	for _, m := range e.outbox() {
		if m.kind != "approval_needed" {
			t.Errorf("unexpected %v", m)
		}
		to = append(to, m.to)
	}
	slices.Sort(to)
	want := []string{admin, other}
	slices.Sort(want)
	if !slices.Equal(to, want) {
		t.Errorf("admin step told %v, want the two admins who want it", to)
	}
}

func TestRemindDue(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	crm := e.entitlement("CRM", e.identity("Owner", ""))
	r, err := e.create(ana, Input{EntitlementID: crm, Justification: "I sell things"})
	if err != nil {
		t.Fatal(err)
	}
	e.outbox()
	ctx := context.Background()
	if n, err := RemindDue(ctx, e.pool, e.tenant); err != nil || n != 0 {
		t.Fatalf("RemindDue a week out = %d, %v; want 0", n, err)
	}
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE access_request_steps SET due_at = now() + interval '3 hours' WHERE request_id = $1 AND status = 'pending'`, r.ID)
		return err
	})
	for range 2 {
		if _, err := RemindDue(ctx, e.pool, e.tenant); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := e.outbox(), []mail{{boss, "request_expiring", "pending"}}; !slices.Equal(got, want) {
		t.Errorf("reminders = %v, want one to boss", got)
	}
}
