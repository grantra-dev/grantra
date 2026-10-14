package sod

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
)

// digests returns the data of each queued SoD email by recipient, and
// empties the outbox.
func (e *env) digests() map[string]map[string]any {
	e.t.Helper()
	out := map[string]map[string]any{}
	e.tx(func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(),
			`DELETE FROM notification_outbox WHERE kind = 'sod_violation' RETURNING identity_id, data`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var data map[string]any
			if err := rows.Scan(&id, &data); err != nil {
				return err
			}
			out[id] = data
		}
		return rows.Err()
	})
	return out
}

func TestScanNotifiesAdmins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	create, approve := e.entitlement("create-payments"), e.entitlement("approve-payments")
	admin := e.identity("Admin")
	ana, bob := e.identity("Ana"), e.identity("Bob")
	e.exec(`UPDATE identities SET email = display_name || '@example.com' WHERE tenant_id = $1`, e.tenant)
	e.tx(func(tx pgx.Tx) error {
		if err := authz.Grant(ctx, tx, e.tenant, authz.RoleAdmin, admin, nil); err != nil {
			return err
		}
		_, err := Save(ctx, tx, e.tenant, "", Input{Name: "Payments", Mode: Detect, Severity: "high",
			SideA: []Ref{{EntitlementID: create}}, SideB: []Ref{{EntitlementID: approve}}})
		return err
	})
	for _, id := range []string{ana, bob} {
		e.assign(id, create)
		e.assign(id, approve)
	}
	e.scan()
	got := e.digests()
	if len(got) != 1 || got[admin] == nil {
		t.Fatalf("digests = %v, want one for the admin", got)
	}
	if got[admin]["count"] != float64(2) || len(got[admin]["violations"].([]any)) != 2 || got[admin]["more"] != float64(0) {
		t.Errorf("digest = %v", got[admin])
	}
	// Nothing new, nothing sent.
	e.scan()
	if got := e.digests(); len(got) != 0 {
		t.Errorf("second scan sent %v", got)
	}

	// An exception that ends brings the violation back to the admins.
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT id FROM sod_violations WHERE identity_id = $1`, ana).Scan(&id)
	})
	e.exec(`UPDATE sod_violations SET status = 'excepted', exception_by = 'x', exception_reason = 'x', exception_until = now() - interval '1 minute' WHERE id = $1`, id)
	e.scan()
	got = e.digests()
	v := got[admin]["violations"].([]any)
	if got[admin]["count"] != float64(1) || len(v) != 1 || v[0].(map[string]any)["reopened"] != true {
		t.Errorf("reopened digest = %v", got[admin])
	}
}
