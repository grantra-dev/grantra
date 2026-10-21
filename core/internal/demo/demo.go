// Package demo loads a small sample company, so Grantra can be tried in
// minutes: an HR feed, a few applications with their entitlements, business
// roles given by department, a manual grant, a service account, an AI agent
// and a workload, and a separation of duties policy. See docs/demo.md for the walkthrough it supports.
package demo

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
	"github.com/grantra-dev/grantra/core/internal/nhi"
	"github.com/grantra-dev/grantra/core/internal/sod"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/vault"
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

// Options tune the sample company.
type Options struct {
	// LoginURL is where Grantra reaches Grantra Login. When set, the demo
	// adds Grantra Login as an application read through its connector, with
	// the client secret in GRANTRA_SECRET_GRANTRA_LOGIN.
	LoginURL string
	// Vault, when it can store secrets, gets a sample privileged account
	// that people request and check out.
	Vault *vault.Vault
}

// Seed loads the sample company into a tenant that has no HR source yet. It
// returns false, and changes nothing, when the tenant already has one.
func Seed(ctx context.Context, pool *pgxpool.Pool, tenantID string, opts Options) (bool, error) {
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
		if err := seed(ctx, tx, tenantID, opts); err != nil {
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

func seed(ctx context.Context, tx pgx.Tx, tenantID string, opts Options) error {
	src, err := lifecycle.CreateSource(ctx, tx, tenantID, "HR (demo)", lifecycle.KindCSV, lifecycle.Config{}, nil)
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

	// Nobody holds GitHub org admin for good: Luis, the CTO, activates it
	// for up to two hours when he needs it.
	if _, err := catalog.SetJIT(ctx, tx, tenantID, actor, ents["GitHub/Org admins"],
		catalog.JIT{JITOnly: true, MaxActivationHours: 2}); err != nil {
		return err
	}
	if _, err := access.MakeEligible(ctx, tx, tenantID, actor, people["luis.perez@example.com"],
		access.Target{EntitlementID: ents["GitHub/Org admins"]}, nil, ""); err != nil {
		return err
	}

	// Luis can also activate SSH to the production database server, through
	// the gateway, which records the session.
	target, err := sshaccess.Create(ctx, tx, tenantID, actor, sshaccess.Input{Name: "prod-db", Login: "postgres",
		Settings: sshaccess.Settings{Address: "db.prod.example.com:22", MaxCertificateHours: 4,
			Notes: "Production PostgreSQL server. Sessions are recorded."}})
	if err != nil {
		return err
	}
	if _, err := access.MakeEligible(ctx, tx, tenantID, actor, people["luis.perez@example.com"],
		access.Target{EntitlementID: target.EntitlementID}, nil, ""); err != nil {
		return err
	}

	if opts.LoginURL != "" {
		// Its groups and the people in them show up after the first
		// aggregation, a few minutes after start.
		app, err := catalog.CreateApplication(ctx, tx, tenantID, "Grantra Login", people["ana.garcia@example.com"])
		if err != nil {
			return err
		}
		cfg, err := json.Marshal(map[string]any{
			"url": opts.LoginURL, "client_secret": "env:GRANTRA_SECRET_GRANTRA_LOGIN",
			"allow_http": strings.HasPrefix(opts.LoginURL, "http://"),
		})
		if err != nil {
			return err
		}
		if err := accounts.SetConnector(ctx, tx, app.ID, grantralogin.Kind, cfg, connector.EnvSecrets); err != nil {
			return fmt.Errorf("connect Grantra Login: %w", err)
		}
	}

	if opts.Vault.Enabled() {
		// Luis's team can ask for the production database's root password,
		// and Luis, on call, can break the glass in an emergency.
		if _, err := opts.Vault.Create(ctx, tx, tenantID, actor, vault.Input{
			Name: "Production database root", Username: "postgres", Secret: "demo-root-password",
			Settings: vault.Settings{Address: "db.prod.example.com:5432", CheckoutMinutes: 30, Exclusive: true,
				Notes: "For incidents only. Say which in the reason.", BreakGlass: true,
				Responders: []string{people["luis.perez@example.com"]}},
		}); err != nil {
			return err
		}
		// Luis can also activate the shop database through the gateway,
		// which logs in as its owner and logs every query. In the Docker
		// Compose stack it is the shop database on the demo's own Postgres.
		owner, err := opts.Vault.Create(ctx, tx, tenantID, actor, vault.Input{
			Name: "Shop database owner", Username: "shop_owner", Secret: "shop-demo-password",
			Settings: vault.Settings{Address: "postgres:5432", Notes: "The gateway logs in with it: nobody needs to check it out."},
		})
		if err != nil {
			return err
		}
		shop, err := gateways.Create(ctx, tx, tenantID, actor, gateways.Input{Name: "shop-db", Kind: gateways.Postgres,
			Settings: gateways.Settings{Address: ptr("postgres:5432"), Database: ptr("shop"), TLSMode: ptr(gateways.TLSDisable),
				VaultAccountID: &owner.ID, MaxTokenHours: ptr(4), Notes: ptr("The online shop's orders. Every query is logged.")}})
		if err != nil {
			return err
		}
		if _, err := access.MakeEligible(ctx, tx, tenantID, actor, people["luis.perez@example.com"],
			access.Target{EntitlementID: shop.EntitlementID}, nil, ""); err != nil {
			return err
		}
		// And a desktop in the browser, recorded. In the Docker Compose
		// stack it is the test desktop of the rdp profile: xrdp, without
		// NLA and with a self-signed certificate.
		desk, err := opts.Vault.Create(ctx, tx, tenantID, actor, vault.Input{
			Name: "Office desktop login", Username: "ubuntu", Secret: "ubuntu",
			Settings: vault.Settings{Address: "desktop:3389", Notes: "The gateway logs in with it: nobody needs to check it out."},
		})
		if err != nil {
			return err
		}
		office, err := gateways.Create(ctx, tx, tenantID, actor, gateways.Input{Name: "office-pc", Kind: gateways.RDP,
			Settings: gateways.Settings{Address: ptr("desktop:3389"), TLSMode: ptr(gateways.TLSRequire),
				RDPSecurity: ptr(gateways.SecurityTLS), VaultAccountID: &desk.ID, MaxTokenHours: ptr(4), Notes: ptr("A shared office desktop. The screen is recorded.")}})
		if err != nil {
			return err
		}
		if _, err := access.MakeEligible(ctx, tx, tenantID, actor, people["luis.perez@example.com"],
			access.Target{EntitlementID: office.EntitlementID}, nil, ""); err != nil {
			return err
		}
	}

	// A service account, an AI agent and a workload, each with its owner.
	end := time.Now().AddDate(0, 6, 0).Format(time.DateOnly)
	for _, n := range []struct {
		kind, name, owner, purpose, spiffe, end, entitlement string
	}{
		{"service", "erp-bank-sync", "pedro.lopez@example.com", "Sends approved supplier payments from the ERP to the bank.", "", "", "ERP/Approve payments"},
		{"agent", "support-assistant", "lucia.torres@example.com", "AI agent that answers customer emails, reading deals in the CRM.", "", end, "CRM/Sales users"},
		{"workload", "billing-api", "luis.perez@example.com", "The billing service in production, asking Grantra who may see each invoice.", "spiffe://example.com/ns/prod/sa/billing-api", "", ""},
	} {
		created, err := nhi.Create(ctx, tx, tenantID, actor, nhi.Input{Kind: &n.kind, Name: &n.name, OwnerID: ptr(people[n.owner]),
			Purpose: &n.purpose, SpiffeID: &n.spiffe, EndDate: &n.end})
		if err != nil {
			return err
		}
		if n.entitlement != "" {
			if _, err := tx.Exec(ctx,
				`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, $4, $5)`,
				tenantID, created.ID, ents[n.entitlement], access.OriginManual, actor); err != nil {
				return err
			}
		}
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

func ptr[T any](v T) *T { return &v }
