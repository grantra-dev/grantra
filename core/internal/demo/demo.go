// Package demo loads a small sample company, so Grantra can be tried in
// minutes: an HR feed, a few applications with their entitlements, business
// roles given by department, a manual grant and a separation of duties
// policy. See docs/demo.md for the walkthrough it supports.
package demo

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
	"github.com/grantra-dev/grantra/core/internal/sod"
)

//go:embed hr.csv
var hrFeed []byte

// actor is how the demo data appears in the audit log.
const actor = "system:demo"

type entitlement struct {
	native, name, description, risk string
}

type application struct {
	name, owner  string
	entitlements []entitlement
}

// The sample company's applications, owned by the people who run them.
var applications = []application{
	{"GitHub", "luis.perez@example.com", []entitlement{
		{"developers", "Developers", "Push to company repositories", "low"},
		{"org-admins", "Org admins", "Manage the GitHub organization, its members and settings", "high"},
	}},
	{"ERP", "pedro.lopez@example.com", []entitlement{
		{"create-payments", "Create payments", "Enter supplier invoices and payments", "low"},
		{"approve-payments", "Approve payments", "Release payments to suppliers", "high"},
		{"reports", "Finance reports", "Read the ledger and finance reports", "low"},
	}},
	{"CRM", "lucia.torres@example.com", []entitlement{
		{"sales", "Sales users", "Work with leads, accounts and deals", "low"},
		{"crm-admin", "CRM admin", "Configure the CRM and export all customer data", "medium"},
	}},
	{"Slack", "ana.garcia@example.com", []entitlement{
		{"members", "Members", "Join the company workspace", "low"},
	}},
}

type role struct {
	name, description, owner, department string
	// entitlements are "Application/Entitlement" names.
	entitlements []string
}

// Business roles, each given to a department by a birthright rule.
var roles = []role{
	{"Engineer", "What every engineer needs", "luis.perez@example.com", "Engineering",
		[]string{"GitHub/Developers", "Slack/Members"}},
	{"Finance clerk", "Day-to-day finance work", "pedro.lopez@example.com", "Finance",
		[]string{"ERP/Create payments", "ERP/Finance reports", "Slack/Members"}},
	{"Sales rep", "Selling and following up deals", "lucia.torres@example.com", "Sales",
		[]string{"CRM/Sales users", "Slack/Members"}},
}

// Seed loads the sample company into a tenant that has no HR source yet. It
// returns false, and changes nothing, when the tenant already has one.
func Seed(ctx context.Context, pool *pgxpool.Pool, tenantID string) (bool, error) {
	seeded := false
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		// One server seeds at a time.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('demo:' || $1))`, tenantID); err != nil {
			return err
		}
		// The demo data is there already, or the tenant has HR data of
		// its own.
		var done bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identity_sources)`).Scan(&done); err != nil || done {
			return err
		}
		if err := seed(ctx, tx, tenantID); err != nil {
			return fmt.Errorf("demo data: %w", err)
		}
		seeded = true
		return nil
	})
	if err != nil || !seeded {
		return false, err
	}
	// Birthright access follows the joiners, as after any HR sync.
	_, _, err = access.ProcessLifecycle(ctx, pool, tenantID)
	return true, err
}

func seed(ctx context.Context, tx pgx.Tx, tenantID string) error {
	src, err := lifecycle.CreateSource(ctx, tx, tenantID, "HR (demo)", "csv", lifecycle.Config{})
	if err != nil {
		return err
	}
	res, err := lifecycle.Sync(ctx, tx, tenantID, src.ID, bytes.NewReader(hrFeed), lifecycle.Options{Actor: actor})
	if err != nil {
		return err
	}
	if res.Status != lifecycle.RunApplied {
		return fmt.Errorf("HR feed was %s: %v", res.Status, res.Errors)
	}

	people := map[string]string{}
	rows, err := tx.Query(ctx, `SELECT email, id FROM identities WHERE email IS NOT NULL`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var email, id string
		if err := rows.Scan(&email, &id); err != nil {
			return err
		}
		people[email] = id
	}
	if err := rows.Err(); err != nil {
		return err
	}

	ents := map[string]string{}
	for _, a := range applications {
		app, err := catalog.CreateApplication(ctx, tx, tenantID, a.name, people[a.owner])
		if err != nil {
			return err
		}
		for _, e := range a.entitlements {
			created, err := catalog.CreateEntitlement(ctx, tx, tenantID, app.ID,
				catalog.EntitlementInput{NativeID: e.native, Name: e.name, Description: e.description, Risk: e.risk})
			if err != nil {
				return err
			}
			ents[a.name+"/"+e.name] = created.ID
		}
	}
	for _, r := range roles {
		var ids []string
		for _, e := range r.entitlements {
			ids = append(ids, ents[e])
		}
		created, err := catalog.CreateRole(ctx, tx, tenantID,
			catalog.RoleInput{Name: r.name, Description: r.description, OwnerID: people[r.owner], EntitlementIDs: ids})
		if err != nil {
			return err
		}
		if _, err := access.CreateRule(ctx, tx, tenantID, access.RuleInput{
			Name:       r.department + " gets " + r.name,
			Conditions: access.Conditions{lifecycle.FieldDepartment: {r.department}},
			RoleIDs:    []string{created.ID},
		}); err != nil {
			return err
		}
	}

	// The head of finance approves payments, granted by hand.
	pedro := people["pedro.lopez@example.com"]
	var assignment string
	if err := tx.QueryRow(ctx,
		`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by)
		 VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tenantID, pedro, ents["ERP/Approve payments"], access.OriginManual, actor).Scan(&assignment); err != nil {
		return err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access.assigned", "identity", pedro, map[string]any{
		"assignment_id": assignment, "origin": access.OriginManual, "entitlement_id": ents["ERP/Approve payments"],
	}); err != nil {
		return err
	}

	_, err = sod.Save(ctx, tx, tenantID, "", sod.Input{
		Name:        "Create and approve payments",
		Description: "Whoever enters a payment must not also release it.",
		Severity:    "critical",
		Mode:        sod.Prevent,
		SideA:       []sod.Ref{{EntitlementID: ents["ERP/Create payments"]}},
		SideB:       []sod.Ref{{EntitlementID: ents["ERP/Approve payments"]}},
	})
	if err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "demo.seeded", "tenant", tenantID, nil)
}
