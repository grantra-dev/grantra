// Package catalog manages what access exists: applications, the entitlements
// inside them, and business roles that bundle entitlements across
// applications.
//
// Applications created here use the "manual" connector: Grantra governs them,
// but someone fulfils changes by hand until a connector is configured. The
// built-in Grantra application and its platform roles are managed by package
// authz and cannot be changed here.
package catalog

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// ManualConnector marks applications that Grantra governs without a connector.
const ManualConnector = "manual"

// builtinConnector is the built-in Grantra application (see package authz).
const builtinConnector = "grantra"

// Risk levels of an entitlement.
var Risks = []string{"low", "medium", "high", "critical"}

// Application is a system people get access to.
type Application struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Connector        string    `json:"connector"`
	OwnerID          string    `json:"owner_id,omitempty"`
	EntitlementCount int       `json:"entitlement_count"`
	CreatedAt        time.Time `json:"created_at"`
}

// Entitlement is a permission inside an application.
type Entitlement struct {
	ID              string    `json:"id"`
	ApplicationID   string    `json:"application_id"`
	ApplicationName string    `json:"application_name"`
	NativeID        string    `json:"native_id"`
	Name            string    `json:"name"`
	Description     string    `json:"description,omitempty"`
	Risk            string    `json:"risk"`
	CreatedAt       time.Time `json:"created_at"`
}

// Role is a business role: a bundle of entitlements given together.
type Role struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	OwnerID      string        `json:"owner_id,omitempty"`
	Entitlements []Entitlement `json:"entitlements"`
	CreatedAt    time.Time     `json:"created_at"`
}

func checkName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return "", apperr.Invalidf("name must be between 1 and 200 characters")
	}
	return name, nil
}

// checkOwner verifies the owner, when given, is an identity of the tenant.
func checkOwner(ctx context.Context, tx pgx.Tx, ownerID string) (*string, error) {
	if ownerID == "" {
		return nil, nil
	}
	var ok bool
	if db.ValidUUID(ownerID) {
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM identities WHERE id = $1)`, ownerID).Scan(&ok); err != nil {
			return nil, err
		}
	}
	if !ok {
		return nil, apperr.Invalidf("owner %s is not an identity", ownerID)
	}
	return &ownerID, nil
}

func uniqueViolation(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == "23505"
}

// CreateApplication adds an application governed by hand.
func CreateApplication(ctx context.Context, tx pgx.Tx, tenantID, name, ownerID string) (Application, error) {
	name, err := checkName(name)
	if err != nil {
		return Application{}, err
	}
	owner, err := checkOwner(ctx, tx, ownerID)
	if err != nil {
		return Application{}, err
	}
	a := Application{Name: name, Connector: ManualConnector, OwnerID: ownerID}
	err = tx.QueryRow(ctx,
		`INSERT INTO applications (tenant_id, name, connector, owner_id) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, name) DO NOTHING RETURNING id, created_at`,
		tenantID, name, ManualConnector, owner).Scan(&a.ID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, apperr.Conflictf("an application named %q already exists", name)
	}
	return a, err
}

// ListApplications returns every application, the built-in one included.
func ListApplications(ctx context.Context, tx pgx.Tx) ([]Application, error) {
	rows, err := tx.Query(ctx,
		`SELECT a.id, a.name, a.connector, coalesce(a.owner_id::text, ''),
		        (SELECT count(*) FROM entitlements e WHERE e.application_id = a.id), a.created_at
		 FROM applications a ORDER BY a.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Application, error) {
		var a Application
		err := row.Scan(&a.ID, &a.Name, &a.Connector, &a.OwnerID, &a.EntitlementCount, &a.CreatedAt)
		return a, err
	})
}

// EntitlementInput describes a new entitlement. NativeID is its id inside the
// application (a group DN, a policy ARN); it defaults to the name.
type EntitlementInput struct {
	NativeID    string `json:"native_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Risk        string `json:"risk"`
}

// CreateEntitlement adds an entitlement to an application.
func CreateEntitlement(ctx context.Context, tx pgx.Tx, tenantID, applicationID string, in EntitlementInput) (Entitlement, error) {
	app, err := lockApplication(ctx, tx, applicationID)
	if err != nil {
		return Entitlement{}, err
	}
	if app.Connector == builtinConnector {
		return Entitlement{}, apperr.Invalidf("platform roles are fixed; manage their members under platform roles")
	}
	name, err := checkName(in.Name)
	if err != nil {
		return Entitlement{}, err
	}
	native := strings.TrimSpace(in.NativeID)
	if native == "" {
		native = name
	}
	if in.Risk == "" {
		in.Risk = "low"
	}
	if !slices.Contains(Risks, in.Risk) {
		return Entitlement{}, apperr.Invalidf("risk must be one of %s", strings.Join(Risks, ", "))
	}
	e := Entitlement{ApplicationID: app.ID, ApplicationName: app.Name, NativeID: native, Name: name,
		Description: strings.TrimSpace(in.Description), Risk: in.Risk}
	err = tx.QueryRow(ctx,
		`INSERT INTO entitlements (tenant_id, application_id, native_id, name, description, risk)
		 VALUES ($1, $2, $3, $4, nullif($5, ''), $6)
		 ON CONFLICT (application_id, native_id) DO NOTHING RETURNING id, created_at`,
		tenantID, app.ID, native, name, e.Description, e.Risk).Scan(&e.ID, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entitlement{}, apperr.Conflictf("%s already has an entitlement %q", app.Name, native)
	}
	return e, err
}

func lockApplication(ctx context.Context, tx pgx.Tx, id string) (Application, error) {
	var a Application
	if !db.ValidUUID(id) {
		return a, apperr.ErrNotFound
	}
	err := tx.QueryRow(ctx, `SELECT id, name, connector FROM applications WHERE id = $1 FOR SHARE`, id).
		Scan(&a.ID, &a.Name, &a.Connector)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, apperr.ErrNotFound
	}
	return a, err
}

const entitlementColumns = `e.id, e.application_id, a.name, e.native_id, e.name, coalesce(e.description, ''), e.risk, e.created_at`

func scanEntitlement(row pgx.CollectableRow) (Entitlement, error) {
	var e Entitlement
	err := row.Scan(&e.ID, &e.ApplicationID, &e.ApplicationName, &e.NativeID, &e.Name, &e.Description, &e.Risk, &e.CreatedAt)
	return e, err
}

// ListEntitlements returns the entitlements of an application.
func ListEntitlements(ctx context.Context, tx pgx.Tx, applicationID string) ([]Entitlement, error) {
	if _, err := lockApplication(ctx, tx, applicationID); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx,
		`SELECT `+entitlementColumns+` FROM entitlements e JOIN applications a ON a.id = e.application_id
		 WHERE e.application_id = $1 ORDER BY e.name, e.id`, applicationID)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanEntitlement)
}

// CheckEntitlements verifies every id is an entitlement Grantra may hand out:
// one of the tenant's, outside the built-in application. Platform roles are
// granted only through their own guarded flow.
func CheckEntitlements(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, id := range ids {
		var connector string
		err := pgx.ErrNoRows
		if db.ValidUUID(id) {
			err = tx.QueryRow(ctx,
				`SELECT a.connector FROM entitlements e JOIN applications a ON a.id = e.application_id WHERE e.id = $1`, id).Scan(&connector)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.Invalidf("entitlement %s does not exist", id)
		}
		if err != nil {
			return err
		}
		if connector == builtinConnector {
			return apperr.Invalidf("entitlement %s is a platform role; grant it under platform roles", id)
		}
	}
	return nil
}

// CheckRoles verifies every id is a role of the tenant.
func CheckRoles(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, id := range ids {
		var ok bool
		if db.ValidUUID(id) {
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM roles WHERE id = $1)`, id).Scan(&ok); err != nil {
				return err
			}
		}
		if !ok {
			return apperr.Invalidf("role %s does not exist", id)
		}
	}
	return nil
}

// RoleInput describes a role and the entitlements it bundles.
type RoleInput struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	OwnerID        string   `json:"owner_id"`
	EntitlementIDs []string `json:"entitlement_ids"`
}

// CreateRole adds a business role.
func CreateRole(ctx context.Context, tx pgx.Tx, tenantID string, in RoleInput) (Role, error) {
	return saveRole(ctx, tx, tenantID, "", in)
}

// UpdateRole replaces a role's name, description, owner and entitlements.
// People who hold the role get the new entitlements and lose the old ones
// with it: assignments point at the role, not at its contents.
func UpdateRole(ctx context.Context, tx pgx.Tx, tenantID, id string, in RoleInput) (Role, error) {
	if !db.ValidUUID(id) {
		return Role{}, apperr.ErrNotFound
	}
	return saveRole(ctx, tx, tenantID, id, in)
}

func saveRole(ctx context.Context, tx pgx.Tx, tenantID, id string, in RoleInput) (Role, error) {
	name, err := checkName(in.Name)
	if err != nil {
		return Role{}, err
	}
	owner, err := checkOwner(ctx, tx, in.OwnerID)
	if err != nil {
		return Role{}, err
	}
	ents := dedupe(in.EntitlementIDs)
	if err := CheckEntitlements(ctx, tx, ents); err != nil {
		return Role{}, err
	}
	desc := strings.TrimSpace(in.Description)
	if id == "" {
		err = tx.QueryRow(ctx,
			`INSERT INTO roles (tenant_id, name, description, owner_id) VALUES ($1, $2, nullif($3, ''), $4) RETURNING id`,
			tenantID, name, desc, owner).Scan(&id)
	} else {
		var tag pgconn.CommandTag
		tag, err = tx.Exec(ctx,
			`UPDATE roles SET name = $2, description = nullif($3, ''), owner_id = $4 WHERE id = $1`, id, name, desc, owner)
		if err == nil && tag.RowsAffected() == 0 {
			return Role{}, apperr.ErrNotFound
		}
	}
	if uniqueViolation(err) {
		return Role{}, apperr.Conflictf("a role named %q already exists", name)
	}
	if err != nil {
		return Role{}, err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM role_entitlements WHERE role_id = $1`, id); err != nil {
		return Role{}, err
	}
	for _, e := range ents {
		if _, err := tx.Exec(ctx,
			`INSERT INTO role_entitlements (tenant_id, role_id, entitlement_id) VALUES ($1, $2, $3)`, tenantID, id, e); err != nil {
			return Role{}, err
		}
	}
	return GetRole(ctx, tx, id)
}

// GetRole returns a role with its entitlements.
func GetRole(ctx context.Context, tx pgx.Tx, id string) (Role, error) {
	roles, err := listRoles(ctx, tx, id)
	if err != nil {
		return Role{}, err
	}
	if len(roles) == 0 {
		return Role{}, apperr.ErrNotFound
	}
	return roles[0], nil
}

// ListRoles returns every role with its entitlements.
func ListRoles(ctx context.Context, tx pgx.Tx) ([]Role, error) { return listRoles(ctx, tx, "") }

func listRoles(ctx context.Context, tx pgx.Tx, id string) ([]Role, error) {
	if id != "" && !db.ValidUUID(id) {
		return nil, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT id, name, coalesce(description, ''), coalesce(owner_id::text, ''), created_at
		 FROM roles WHERE $1 = '' OR id = $1::uuid ORDER BY name, id`, id)
	if err != nil {
		return nil, err
	}
	roles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Role, error) {
		r := Role{Entitlements: []Entitlement{}}
		err := row.Scan(&r.ID, &r.Name, &r.Description, &r.OwnerID, &r.CreatedAt)
		return r, err
	})
	if err != nil || len(roles) == 0 {
		return roles, err
	}
	rows, err = tx.Query(ctx,
		`SELECT re.role_id, `+entitlementColumns+`
		 FROM role_entitlements re
		 JOIN entitlements e ON e.id = re.entitlement_id
		 JOIN applications a ON a.id = e.application_id
		 WHERE $1 = '' OR re.role_id = $1::uuid
		 ORDER BY a.name, e.name, e.id`, id)
	if err != nil {
		return nil, err
	}
	byRole := map[string]int{}
	for i, r := range roles {
		byRole[r.ID] = i
	}
	for rows.Next() {
		var (
			roleID string
			e      Entitlement
		)
		if err := rows.Scan(&roleID, &e.ID, &e.ApplicationID, &e.ApplicationName, &e.NativeID, &e.Name, &e.Description, &e.Risk, &e.CreatedAt); err != nil {
			return nil, err
		}
		if i, ok := byRole[roleID]; ok {
			roles[i].Entitlements = append(roles[i].Entitlements, e)
		}
	}
	return roles, rows.Err()
}

func dedupe(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
