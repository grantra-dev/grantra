package catalog

import (
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// DefaultReadEveryHours is how often a new application is read.
const DefaultReadEveryHours = 1

// MaxReadEveryHours is the longest gap between scheduled reads: a week.
const MaxReadEveryHours = 168

func errRetired(app string) error {
	return apperr.Conflictf("%s is retired; bring it back before giving new access to it", app)
}

// ApplicationInput is what an admin can change about an application.
// Its connector and provisioning have their own calls.
type ApplicationInput struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	OwnerID        string `json:"owner_id"`
	ReadEveryHours int    `json:"read_every_hours"`
}

// GetApplication returns one application.
func GetApplication(ctx context.Context, tx pgx.Tx, id string) (Application, error) {
	if !db.ValidUUID(id) {
		return Application{}, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT `+applicationColumns+` FROM applications a WHERE a.id = $1`, id)
	if err != nil {
		return Application{}, err
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanApplication)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, apperr.ErrNotFound
	}
	return a, err
}

// editable locks an application an admin is about to change, refusing the
// built-in one.
func editable(ctx context.Context, tx pgx.Tx, id string) (Application, error) {
	if !db.ValidUUID(id) {
		return Application{}, apperr.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM applications WHERE id = $1 FOR UPDATE`, id); err != nil {
		return Application{}, err
	}
	a, err := GetApplication(ctx, tx, id)
	if err == nil && a.Connector == builtinConnector {
		err = apperr.Invalidf("the built-in Grantra application cannot be changed")
	}
	return a, err
}

// UpdateApplication replaces an application's name, description, owner and
// read schedule. The owner is who approves requests for its access under
// the default approval flow.
func UpdateApplication(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, in ApplicationInput) (Application, error) {
	before, err := editable(ctx, tx, id)
	if err != nil {
		return before, err
	}
	name, err := checkName(in.Name)
	if err != nil {
		return before, err
	}
	desc := strings.TrimSpace(in.Description)
	if len(desc) > 2000 {
		return before, apperr.Invalidf("description must be at most 2000 characters")
	}
	if in.ReadEveryHours < 0 || in.ReadEveryHours > MaxReadEveryHours {
		return before, apperr.Invalidf("read_every_hours must be between 0 (only when asked) and %d", MaxReadEveryHours)
	}
	owner, err := checkOwner(ctx, tx, in.OwnerID)
	if err != nil {
		return before, err
	}
	_, err = tx.Exec(ctx,
		`UPDATE applications SET name = $2, description = nullif($3, ''), owner_id = $4, read_every_hours = $5 WHERE id = $1`,
		id, name, desc, owner, in.ReadEveryHours)
	if db.IsUniqueViolation(err) {
		return before, apperr.Conflictf("an application named %q already exists", name)
	}
	if err != nil {
		return before, err
	}
	after, err := GetApplication(ctx, tx, id)
	if err != nil {
		return after, err
	}
	changes := map[string]any{}
	diff(changes, "name", before.Name, after.Name)
	diff(changes, "description", before.Description, after.Description)
	diff(changes, "owner_id", before.OwnerID, after.OwnerID)
	diff(changes, "read_every_hours", before.ReadEveryHours, after.ReadEveryHours)
	if len(changes) == 0 {
		return after, nil
	}
	return after, db.Audit(ctx, tx, tenantID, actor, "application.updated", "application", id, changes)
}

// entitlementUses are the tables that point at an entitlement and hold
// something worth keeping: who holds it, who asked or was asked about it,
// and what gives it or guards it. An entitlement or application named in any
// of them cannot be deleted, only retired. The other references (connector
// runs, authorization policies, keys) go with the application.
var entitlementUses = []struct{ table, what string }{
	{"account_entitlements", "accounts hold it"},
	{"access_assignments", "people are assigned it"},
	{"access_eligibilities", "people are eligible for it"},
	{"access_requests", "access requests name it"},
	{"review_items", "access reviews name it"},
	{"role_entitlements", "a business role includes it"},
	{"access_rule_grants", "a birthright rule gives it"},
	{"sod_policy_sides", "a separation of duties policy names it"},
	{"vault_accounts", "a vault account uses it"},
	{"ssh_targets", "an SSH target uses it"},
	{"gateway_targets", "a gateway target uses it"},
}

// inUse tells the first reason any of the entitlements cannot be deleted,
// or "" when none applies.
func inUse(ctx context.Context, tx pgx.Tx, ids []string) (string, error) {
	if len(ids) == 0 {
		return "", nil
	}
	parts := make([]string, len(entitlementUses))
	for i, u := range entitlementUses {
		parts[i] = `SELECT '` + u.what + `' WHERE EXISTS (SELECT 1 FROM ` + u.table + ` WHERE entitlement_id = ANY($1::uuid[]))`
	}
	var what string
	err := tx.QueryRow(ctx, strings.Join(parts, " UNION ALL ")+" LIMIT 1", ids).Scan(&what)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return what, err
}

// DeleteApplication removes an application added by mistake: one with no
// accounts and whose entitlements nobody holds, asked for or reviewed.
// Anything with history is retired instead, so the audit trail keeps
// pointing at something.
func DeleteApplication(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	app, err := editable(ctx, tx, id)
	if err != nil {
		return err
	}
	var accounts bool
	var ents []string
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM accounts WHERE application_id = $1),
		        coalesce((SELECT array_agg(id::text) FROM entitlements WHERE application_id = $1), '{}')`, id).
		Scan(&accounts, &ents); err != nil {
		return err
	}
	if accounts {
		return apperr.Conflictf("%s has accounts; retire it instead to keep its history", app.Name)
	}
	what, err := inUse(ctx, tx, ents)
	if err != nil {
		return err
	}
	if what != "" {
		return apperr.Conflictf("%s has history (%s); retire it instead", app.Name, what)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM applications WHERE id = $1`, id); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "application.deleted", "application", id, map[string]any{"name": app.Name})
}

// Retire takes an application out of service, or brings it back. A retired
// application is not read or written to, cannot be asked for and takes no
// new access; what people hold stays visible until a review or an admin
// removes it. Business roles, birthright rules and open requests would keep
// handing out its access, so it cannot be retired while they name it. Retiring turns
// provisioning off; bringing it back leaves it off.
func Retire(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, retired bool) (Application, error) {
	app, err := editable(ctx, tx, id)
	if err != nil {
		return app, err
	}
	if (app.RetiredAt != nil) == retired {
		return app, nil
	}
	if retired {
		var given bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM entitlements e
			                WHERE e.application_id = $1
			                  AND (EXISTS (SELECT 1 FROM role_entitlements re WHERE re.entitlement_id = e.id)
			                       OR EXISTS (SELECT 1 FROM access_rule_grants g WHERE g.entitlement_id = e.id)))`, id).
			Scan(&given); err != nil {
			return app, err
		}
		if given {
			return app, apperr.Conflictf("a business role or birthright rule includes access to %s; take it out first", app.Name)
		}
		var open bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM access_requests r JOIN entitlements e ON e.id = r.entitlement_id
			                WHERE e.application_id = $1 AND r.status = 'pending')`, id).Scan(&open); err != nil {
			return app, err
		}
		if open {
			return app, apperr.Conflictf("requests for access to %s are still open; decide or cancel them first", app.Name)
		}
		_, err = tx.Exec(ctx,
			`UPDATE applications SET retired_at = now(), provisioning = jsonb_set(provisioning, '{mode}', '"off"') WHERE id = $1`, id)
	} else {
		_, err = tx.Exec(ctx, `UPDATE applications SET retired_at = NULL WHERE id = $1`, id)
	}
	if err != nil {
		return app, err
	}
	action := "application.restored"
	if retired {
		action = "application.retired"
	}
	if err := db.Audit(ctx, tx, tenantID, actor, action, "application", id, map[string]any{"name": app.Name}); err != nil {
		return app, err
	}
	return GetApplication(ctx, tx, id)
}

// EntitlementChange is what an admin can change about an entitlement. The
// name of one read from the application comes from the application.
type EntitlementChange struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
}

// UpdateEntitlement changes an entitlement's name, description and risk.
// Risk decides approval flows, separation of duties risk steps and who
// needs a second factor, so every change is audited with its old value.
func UpdateEntitlement(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, in EntitlementChange) (Entitlement, error) {
	before, err := lockEntitlement(ctx, tx, id)
	if err != nil {
		return before.Entitlement, err
	}
	name, err := checkName(in.Name)
	if err != nil {
		return before.Entitlement, err
	}
	if !slices.Contains(Risks, in.Risk) {
		return before.Entitlement, apperr.Invalidf("risk must be one of %s", strings.Join(Risks, ", "))
	}
	if name != before.Name && before.connector != ManualConnector {
		return before.Entitlement, apperr.Invalidf("%s names this entitlement; change its name there", before.ApplicationName)
	}
	desc := strings.TrimSpace(in.Description)
	if _, err := tx.Exec(ctx, `UPDATE entitlements SET name = $2, description = nullif($3, ''), risk = $4 WHERE id = $1`,
		id, name, desc, in.Risk); err != nil {
		return before.Entitlement, err
	}
	after, err := entitlementByID(ctx, tx, id)
	if err != nil {
		return after, err
	}
	changes := map[string]any{}
	diff(changes, "name", before.Name, after.Name)
	diff(changes, "description", before.Description, after.Description)
	diff(changes, "risk", before.Risk, after.Risk)
	if len(changes) == 0 {
		return after, nil
	}
	return after, db.Audit(ctx, tx, tenantID, actor, "entitlement.updated", "entitlement", id, changes)
}

// DeleteEntitlement removes an entitlement added by hand by mistake. One
// read from the application would come back at the next read, and one with
// history stays for the audit trail.
func DeleteEntitlement(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	e, err := lockEntitlement(ctx, tx, id)
	if err != nil {
		return err
	}
	if e.connector != ManualConnector {
		return apperr.Conflictf("%s lists this entitlement; it goes when the application stops listing it", e.ApplicationName)
	}
	what, err := inUse(ctx, tx, []string{id})
	if err != nil {
		return err
	}
	if what != "" {
		return apperr.Conflictf("%s cannot be deleted: %s", e.Name, what)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM entitlements WHERE id = $1`, id); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "entitlement.deleted", "entitlement", id,
		map[string]any{"application_id": e.ApplicationID, "name": e.Name, "native_id": e.NativeID})
}

// diff records a field that changed, for the audit trail.
func diff(changes map[string]any, key string, from, to any) {
	if from != to {
		changes[key] = map[string]any{"from": from, "to": to}
	}
}

// lockedEntitlement is an entitlement with its application's connector.
type lockedEntitlement struct {
	Entitlement
	connector string
}

func lockEntitlement(ctx context.Context, tx pgx.Tx, id string) (lockedEntitlement, error) {
	var out lockedEntitlement
	if !db.ValidUUID(id) {
		return out, apperr.ErrNotFound
	}
	err := tx.QueryRow(ctx,
		`SELECT a.connector FROM entitlements e JOIN applications a ON a.id = e.application_id WHERE e.id = $1 FOR UPDATE OF e`, id).
		Scan(&out.connector)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, apperr.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	if out.connector == builtinConnector {
		return out, apperr.Invalidf("platform roles are fixed; manage their members under platform roles")
	}
	out.Entitlement, err = entitlementByID(ctx, tx, id)
	return out, err
}

func entitlementByID(ctx context.Context, tx pgx.Tx, id string) (Entitlement, error) {
	rows, err := tx.Query(ctx, `SELECT `+entitlementColumns+` FROM entitlements e JOIN applications a ON a.id = e.application_id WHERE e.id = $1`, id)
	if err != nil {
		return Entitlement{}, err
	}
	return pgx.CollectExactlyOneRow(rows, scanEntitlement)
}
