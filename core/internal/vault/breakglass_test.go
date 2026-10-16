package vault

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
)

func (e *env) platformRole(role, identity string) {
	e.t.Helper()
	e.exec(`UPDATE identities SET email = display_name || '@example.com' WHERE id = $1`, identity)
	e.must(func(tx pgx.Tx) error { return authz.Grant(e.ctx, tx, e.tenant, role, identity, nil) })
}

func (e *env) breakGlass(identity, id string) (Lease, error) {
	var l Lease
	err := e.tx(func(tx pgx.Tx) (err error) {
		l, err = e.v.BreakGlass(e.ctx, tx, e.tenant, identity, id, "INC-9: production is down")
		return err
	})
	return l, err
}

func (e *env) responders(id string) ([]string, bool) {
	e.t.Helper()
	var (
		ids   []string
		glass bool
	)
	e.must(func(tx pgx.Tx) (err error) { ids, glass, err = Responders(e.ctx, tx, id); return err })
	return ids, glass
}

func TestBreakGlassFlow(t *testing.T) {
	e := newEnv(t)
	if err := authz.EnsureBuiltinApp(e.ctx, e.pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	admin, other := e.identity("Admin", "human"), e.identity("Other admin", "human")
	auditor, ana, luis := e.identity("Auditor", "human"), e.identity("Ana", "human"), e.identity("Luis", "human")
	e.platformRole(authz.RoleAdmin, admin)
	e.platformRole(authz.RoleAdmin, other)
	e.platformRole(authz.RoleAuditor, auditor)
	// The alert cannot be turned off.
	e.must(func(tx pgx.Tx) error {
		if err := notify.SetEnabled(e.ctx, tx, e.tenant, auditor, notify.BreakGlassUsed, false); !isInvalid(err) {
			t.Errorf("turning the break-glass alert off = %v", err)
		}
		_, err := tx.Exec(e.ctx, `INSERT INTO notification_opt_outs (tenant_id, identity_id, kind) VALUES ($1, $2, $3)`,
			e.tenant, auditor, notify.BreakGlassUsed)
		return err
	})

	acct := e.targetAccount("dba")
	plain := e.create(Input{Name: "plain", Username: "root", Secret: "pw"})
	a := e.create(Input{Name: "emergency", AccountID: acct, Secret: "glass-pw",
		Settings: Settings{CheckoutMinutes: 240, Exclusive: true, BreakGlass: true}})
	if !a.BreakGlass || len(a.Responders) != 0 {
		t.Fatalf("account = %+v", a)
	}
	if _, err := e.breakGlass(admin, plain.ID); !isConflict(err) {
		t.Errorf("break-glass on a plain account = %v", err)
	}
	if ids, glass := e.responders(plain.ID); glass || len(ids) != 0 {
		t.Errorf("plain account responders = %v, %v", ids, glass)
	}
	// No responders listed: the admins.
	if ids, glass := e.responders(a.ID); !glass || len(ids) != 2 {
		t.Errorf("default responders = %v, want the two admins", ids)
	}

	// Someone has it out exclusively; an emergency does not wait.
	e.grant(luis, a.EntitlementID, nil)
	if _, err := e.checkout(luis, a.ID); err != nil {
		t.Fatal(err)
	}
	l, err := e.breakGlass(admin, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l.Secret != "glass-pw" || !l.BreakGlass || l.ReviewStatus != "pending" || l.AccountName != "emergency" {
		t.Errorf("lease = %+v", l)
	}
	if d := time.Until(l.ExpiresAt); d > 61*time.Minute || d < 59*time.Minute {
		t.Errorf("break-glass lasts %v, want at most an hour", d)
	}
	sent := e.one(`SELECT string_agg(i.display_name, ',' ORDER BY i.display_name) FROM notification_outbox o JOIN identities i ON i.id = o.identity_id
	               WHERE o.kind = 'break_glass_used'`)
	if sent != "Admin,Auditor,Other admin" {
		t.Errorf("alerted %q, want every admin and auditor, opt-outs ignored", sent)
	}
	if n := e.one(`SELECT count(*)::text FROM audit_events WHERE action = 'vault.break_glass_opened' AND target_id = $1`, a.ID); n != "1" {
		t.Errorf("audit events = %s", n)
	}

	// When the last check-out ends, the exposed password rotates even
	// though the account does not rotate after check-in.
	e.exec(`UPDATE vault_checkouts SET ended_at = now(), ended_by = 'checked_in' WHERE identity_id = $1`, luis)
	e.must(func(tx pgx.Tx) error {
		return Checkin(e.ctx, tx, e.tenant, db.IdentityActor(admin), l.ID, "checked_in")
	})
	if _, at, _ := rotationState(e, a.ID); at == nil || time.Until(*at) > 0 {
		t.Errorf("rotate_at = %v after break-glass, want now", at)
	}
	// Once rotated, a plain check-out does not make it due again.
	if r, err := e.v.Rotate(e.ctx, e.pool, e.tenant, "system:test", a.ID); err != nil || !r.Rotated {
		t.Fatal(r, err)
	}
	l2, err := e.checkout(luis, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE vault_accounts SET rotate_at = NULL WHERE id = $1`, a.ID)
	e.must(func(tx pgx.Tx) error {
		return Checkin(e.ctx, tx, e.tenant, db.IdentityActor(luis), l2.ID, "checked_in")
	})
	if _, at, _ := rotationState(e, a.ID); at != nil {
		t.Errorf("rotate_at = %v after a plain check-out of the new password", at)
	}

	// Listed responders replace the admins.
	e.must(func(tx pgx.Tx) error {
		_, err := e.v.Update(e.ctx, tx, e.tenant, "identity:admin", a.ID,
			Settings{CheckoutMinutes: 240, BreakGlass: true, Responders: []string{ana}})
		return err
	})
	if ids, _ := e.responders(a.ID); len(ids) != 1 || ids[0] != ana {
		t.Errorf("responders = %v, want ana", ids)
	}
	e.must(func(tx pgx.Tx) error {
		if _, err := e.v.Update(e.ctx, tx, e.tenant, "identity:admin", a.ID, Settings{Responders: []string{ana}}); !isInvalid(err) {
			t.Errorf("responders without break-glass = %v", err)
		}
		if _, err := e.v.Update(e.ctx, tx, e.tenant, "identity:admin", a.ID, Settings{BreakGlass: true, Responders: []string{e.tenant}}); !isInvalid(err) {
			t.Errorf("a responder who is nobody = %v", err)
		}
		return nil
	})

	// Review: pending until an admin decides; flagging needs a reason.
	var uses []Checkout
	e.must(func(tx pgx.Tx) (err error) { uses, err = BreakGlassUses(e.ctx, tx, true); return err })
	if len(uses) != 1 || uses[0].ID != l.ID {
		t.Fatalf("pending uses = %+v", uses)
	}
	e.must(func(tx pgx.Tx) error {
		if _, err := ReviewBreakGlass(e.ctx, tx, e.tenant, db.IdentityActor(other), l.ID, false, " "); !isInvalid(err) {
			t.Errorf("flag without a reason = %v", err)
		}
		if _, err := ReviewBreakGlass(e.ctx, tx, e.tenant, db.IdentityActor(other), l2.ID, true, ""); err == nil {
			t.Error("reviewed a plain check-out")
		}
		c, err := ReviewBreakGlass(e.ctx, tx, e.tenant, db.IdentityActor(other), l.ID, false, "no incident INC-9 exists")
		if err != nil {
			return err
		}
		if c.ReviewStatus != "flagged" || c.ReviewedAt == nil || c.ReviewComment != "no incident INC-9 exists" {
			t.Errorf("reviewed = %+v", c)
		}
		if _, err := ReviewBreakGlass(e.ctx, tx, e.tenant, db.IdentityActor(other), l.ID, true, ""); !isConflict(err) {
			t.Errorf("reviewing twice = %v", err)
		}
		return nil
	})
}

func TestBreakGlassReminders(t *testing.T) {
	e := newEnv(t)
	if err := authz.EnsureBuiltinApp(e.ctx, e.pool, e.tenant); err != nil {
		t.Fatal(err)
	}
	admin, other := e.identity("Admin", "human"), e.identity("Other admin", "human")
	e.platformRole(authz.RoleAdmin, admin)
	e.platformRole(authz.RoleAdmin, other)
	a := e.create(Input{Name: "emergency", Username: "root", Secret: "pw", Settings: Settings{BreakGlass: true}})
	l, err := e.breakGlass(admin, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	remind := func() int {
		n, err := RemindDue(e.ctx, e.pool, e.tenant)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := remind(); n != 0 {
		t.Errorf("reminded about a fresh use: %d", n)
	}
	e.exec(`UPDATE vault_checkouts SET checked_out_at = now() - interval '25 hours' WHERE id = $1`, l.ID)
	if n := remind(); n != 1 {
		t.Errorf("reminded %d, want 1", n)
	}
	// Only the other admin hears: nobody reviews their own use.
	if to := e.one(`SELECT string_agg(identity_id::text, ',') FROM notification_outbox WHERE kind = 'break_glass_review'`); to != other {
		t.Errorf("reminder went to %s, want only the other admin", to)
	}
	if n := remind(); n != 0 {
		t.Errorf("reminded again within a day: %d", n)
	}
}
