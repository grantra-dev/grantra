package requests

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/approvals"
	"github.com/grantra-dev/grantra/core/internal/db"
)

func (e *env) approvalPolicy(in approvals.Input) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := approvals.Save(context.Background(), tx, e.tenant, "identity:admin", "", in)
		return err
	})
}

func (e *env) tryDecide(actor, id string, approve bool) (Request, error) {
	var r Request
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		req, err := Get(context.Background(), tx, id, true)
		if err != nil {
			return err
		}
		r, err = Decide(context.Background(), tx, e.tenant, "identity:"+actor, req, Decision{Approve: approve})
		return err
	})
	return r, err
}

func (e *env) waiting(who string, admin bool) int {
	e.t.Helper()
	var n int
	e.tx(func(tx pgx.Tx) error {
		list, err := ListWaiting(context.Background(), tx, who, admin)
		n = len(list)
		return err
	})
	return n
}

// overdue makes the step a request waits on due now.
func (e *env) overdue(id string) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE access_request_steps SET due_at = now() - interval '1 minute' WHERE request_id = $1 AND status = 'pending'`, id)
		return err
	})
}

func (e *env) get(id string) Request {
	e.t.Helper()
	var r Request
	e.tx(func(tx pgx.Tx) (err error) {
		r, err = Get(context.Background(), tx, id, false)
		return err
	})
	return r
}

func TestSeveralApprovers(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	p1, p2, p3 := e.identity("Pia", ""), e.identity("Pablo", ""), e.identity("Paz", "")
	crm := e.entitlement("CRM", "")
	e.approvalPolicy(approvals.Input{Name: "Two of three", Steps: []approvals.Step{{Approver: approvals.People, PeopleIDs: []string{p1, p2, p3}, Required: 2, Label: "Sales leads"}}})

	r, err := e.create(ana, Input{EntitlementID: crm, Justification: "deals"})
	if err != nil {
		t.Fatal(err)
	}
	c := r.Current()
	if r.PolicyName != "Two of three" || r.PolicyVersion == nil || *r.PolicyVersion != 1 || c == nil || c.Kind != KindPeople ||
		c.Label != "Sales leads" || c.Required != 2 || len(c.Approvers) != 3 {
		t.Fatalf("request = %+v, step %+v", r, c)
	}
	for _, who := range []string{p1, p2, p3} {
		if n := e.waiting(who, false); n != 1 {
			t.Errorf("waiting on %s = %d", who, n)
		}
	}

	// One approval is not enough; nobody approves twice.
	r = e.decide(p1, r.ID, true)
	if c := r.Current(); r.Status != Pending || c == nil || len(c.Approvals) != 1 || c.Approvals[0].Name != "Pia" {
		t.Fatalf("after one = %+v", r)
	}
	if n := e.waiting(p1, false); n != 0 {
		t.Errorf("still waiting on the one who approved")
	}
	if ids, admins := r.Current().Deciders(); len(ids) != 2 || admins {
		t.Errorf("deciders = %v %v", ids, admins)
	}
	var conflict *apperr.Conflict
	if _, err := e.tryDecide(p1, r.ID, true); !errors.As(err, &conflict) {
		t.Errorf("approving twice = %v", err)
	}
	r = e.decide(p3, r.ID, true)
	if r.Status != Approved || e.assignments(ana) != 1 {
		t.Errorf("after two = %+v", r)
	}

	// When too few are left to approve, the admins decide too.
	ivan := e.identity("Ivan", boss)
	r, err = e.create(ivan, Input{EntitlementID: crm, Justification: "deals"})
	if err != nil {
		t.Fatal(err)
	}
	if n := e.waiting(boss, true); n != 0 {
		t.Errorf("waits on the admins while enough can approve")
	}
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE identities SET status = 'suspended' WHERE id = ANY($1::uuid[])`, []string{p1, p2})
		return err
	})
	if n := e.waiting(boss, true); n != 1 {
		t.Errorf("does not wait on the admins with one approver left")
	}

	// One rejection rejects it.
	if r = e.decide(p3, r.ID, false); r.Status != Rejected {
		t.Errorf("after a rejection = %+v", r)
	}
}

func TestAskingApproves(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("Boss", "")
	ana := e.identity("Ana", boss)
	other := e.identity("Otto", "")
	crm := e.entitlement("CRM", "")
	e.approvalPolicy(approvals.Input{Name: "Boss and Otto", Steps: []approvals.Step{{Approver: approvals.People, PeopleIDs: []string{boss, other}, All: true}}})

	// The boss asks for a report: their approval is given, Otto's is not.
	r, err := e.create(boss, Input{BeneficiaryID: ana, EntitlementID: crm, Justification: "deals"})
	if err != nil {
		t.Fatal(err)
	}
	c := r.Current()
	if c == nil || c.Required != 2 || len(c.Approvals) != 1 || c.Approvals[0].Comment != "asked for it" {
		t.Fatalf("step = %+v", c)
	}
	if r = e.decide(other, r.ID, true); r.Status != Approved {
		t.Errorf("after Otto = %+v", r)
	}
}

func TestTimeouts(t *testing.T) {
	e := newEnv(t)
	top := e.identity("Top", "")
	boss := e.identity("Boss", top)
	ana := e.identity("Ana", boss)
	crm := e.entitlement("CRM", "")
	wiki := e.entitlement("Wiki", "")
	e.tx(func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE entitlements SET risk = 'high' WHERE id = $1`, crm)
		return err
	})
	e.approvalPolicy(approvals.Input{Name: "Escalate", Conditions: approvals.Conditions{Risks: []string{"high"}},
		Steps: []approvals.Step{{Approver: approvals.Manager, TimeoutHours: 2, OnTimeout: approvals.Escalate}}})
	e.approvalPolicy(approvals.Input{Name: "Reject", Steps: []approvals.Step{{Approver: approvals.Manager, OnTimeout: approvals.Reject}}})
	expire := func() Housekeeping {
		t.Helper()
		h, err := ExpireDue(context.Background(), e.pool, e.tenant)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}

	r, err := e.create(ana, Input{EntitlementID: crm, Justification: "deals"})
	if err != nil {
		t.Fatal(err)
	}
	if c := r.Current(); c == nil || c.DueAt == nil || time.Until(*c.DueAt) > 2*time.Hour || c.OnTimeout != approvals.Escalate {
		t.Fatalf("step = %+v", c)
	}
	// Nobody decided in time: Boss's manager is asked, once.
	e.overdue(r.ID)
	if h := expire(); h.Escalated != 1 || h.Expired != 0 {
		t.Errorf("housekeeping = %+v", h)
	}
	r = e.get(r.ID)
	c := r.Current()
	if r.Status != Pending || c == nil || len(c.Approvers) != 1 || c.Approvers[0].ID != top || len(c.EscalatedFrom) != 1 ||
		c.EscalatedFrom[0] != "Boss" || c.EscalatedAt == nil || c.OnTimeout != approvals.Expire || time.Until(*c.DueAt) < time.Hour {
		t.Fatalf("escalated = %+v", c)
	}
	if e.waiting(top, false) != 1 || e.waiting(boss, false) != 0 {
		t.Errorf("waiting after escalation")
	}
	e.overdue(r.ID)
	if h := expire(); h.Expired != 1 || e.get(r.ID).Status != Expired {
		t.Errorf("second timeout = %+v", h)
	}

	// The other policy rejects requests nobody decided.
	r, err = e.create(ana, Input{EntitlementID: wiki, Justification: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	e.overdue(r.ID)
	if h := expire(); h.Rejected != 1 || e.get(r.ID).Status != Rejected {
		t.Errorf("reject = %+v", h)
	}
}

func TestNoStepForLowRisk(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana", "")
	wiki := e.entitlement("Wiki", "")
	e.approvalPolicy(approvals.Input{Name: "Self-service", Conditions: approvals.Conditions{Risks: []string{"low"}}})
	r, err := e.create(ana, Input{EntitlementID: wiki, Justification: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Status != Approved || len(r.Steps) != 0 || r.PolicyName != "Self-service" || e.assignments(ana) != 1 {
		t.Errorf("request = %+v", r)
	}
}
