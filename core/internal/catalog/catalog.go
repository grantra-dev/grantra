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
	"encoding/json"
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
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connector string `json:"connector"`
	// Config is the connector's settings. It never holds secrets, only
	// references to them such as "env:GRANTRA_SECRET_CRM_SCIM_TOKEN".
	Config json.RawMessage `json:"config"`
	// Provisioning is how Grantra writes to the application, see the
	// provisioning package.
	Provisioning json.RawMessage `json:"provisioning"`
	OwnerID      string          `json:"owner_id,omitempty"`
	OwnerName    string          `json:"owner_name,omitempty"`
	Description  string          `json:"description,omitempty"`
	// ReadEveryHours is how often the scheduled job reads the application
	// through its connector; 0 reads it only when someone asks.
	ReadEveryHours int `json:"read_every_hours"`
	// RetiredAt is set once the application is retired, see Retire.
	RetiredAt        *time.Time `json:"retired_at,omitempty"`
	EntitlementCount int        `json:"entitlement_count"`
	CreatedAt        time.Time  `json:"created_at"`
}

// Entitlement is a permission inside an application.
type Entitlement struct {
	ID              string `json:"id"`
	ApplicationID   string `json:"application_id"`
	ApplicationName string `json:"application_name"`
	NativeID        string `json:"native_id"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Risk            string `json:"risk"`
	JIT
	CreatedAt time.Time `json:"created_at"`
}

// JIT is how an entitlement is held just in time. JITOnly means nobody holds
// it without an end date: requests carry a duration, birthright rules cannot
// give it, and people are made eligible and activate it when they need it.
// An activation lasts at most MaxActivationHours and, with
// ActivationApproval, goes through the approvals of an access request.
type JIT struct {
	JITOnly            bool `json:"jit_only"`
	MaxActivationHours int  `json:"max_activation_hours"`
	ActivationApproval bool `json:"activation_approval"`
}

// Role is a business role: a bundle of entitlements given together.
type Role struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Description  string        `json:"description,omitempty"`
	OwnerID      string        `json:"owner_id,omitempty"`
	OwnerName    string        `json:"owner_name,omitempty"`
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
	a := Application{Name: name, Connector: ManualConnector, Config: json.RawMessage(`{}`), Provisioning: json.RawMessage(`{"mode": "off"}`), OwnerID: ownerID, ReadEveryHours: DefaultReadEveryHours}
	err = tx.QueryRow(ctx,
		`INSERT INTO applications (tenant_id, name, connector, owner_id) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (tenant_id, name) DO NOTHING RETURNING id, created_at`,
		tenantID, name, ManualConnector, owner).Scan(&a.ID, &a.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Application{}, apperr.Conflictf("an application named %q already exists", name)
	}
	return a, err
}

// ownerName is the SQL for the display name of the identity owner (an SQL
// expression such as a.owner_id), or empty when there is none.
func ownerName(owner string) string {
	return `coalesce((SELECT o.display_name FROM identities o WHERE o.id = ` + owner + `), '')`
}

var applicationColumns = `a.id, a.name, a.connector, a.config, a.provisioning, coalesce(a.owner_id::text, ''),
	` + ownerName("a.owner_id") + `, coalesce(a.description, ''), a.read_every_hours, a.retired_at,
	(SELECT count(*) FROM entitlements e WHERE e.application_id = a.id), a.created_at`

func scanApplication(row pgx.CollectableRow) (Application, error) {
	var a Application
	err := row.Scan(&a.ID, &a.Name, &a.Connector, &a.Config, &a.Provisioning, &a.OwnerID, &a.OwnerName,
		&a.Description, &a.ReadEveryHours, &a.RetiredAt, &a.EntitlementCount, &a.CreatedAt)
	return a, err
}

// ListApplications returns every application, the built-in one included.
func ListApplications(ctx context.Context, tx pgx.Tx) ([]Application, error) {
	rows, err := tx.Query(ctx, `SELECT `+applicationColumns+` FROM applications a ORDER BY a.name`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanApplication)
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
	if app.RetiredAt != nil {
		return Entitlement{}, errRetired(app.Name)
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
		Description: strings.TrimSpace(in.Description), Risk: in.Risk, JIT: JIT{MaxActivationHours: DefaultActivationHours}}
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

// DefaultActivationHours is how long an activation may last unless the
// entitlement says otherwise.
const DefaultActivationHours = 4

// SetJIT changes how an entitlement is held just in time. Making it
// just-in-time only is refused while a birthright rule gives it, and vault
// entitlements always stay just-in-time only. Standing access people already
// hold stays until it is turned into eligibility (see the standing access
// report).
func SetJIT(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, j JIT) (Entitlement, error) {
	if !db.ValidUUID(id) {
		return Entitlement{}, apperr.ErrNotFound
	}
	if j.MaxActivationHours == 0 {
		j.MaxActivationHours = DefaultActivationHours
	}
	if j.MaxActivationHours < 1 || j.MaxActivationHours > 24 {
		return Entitlement{}, apperr.Invalidf("max_activation_hours must be between 1 and 24")
	}
	var connector string
	var vault, birthright bool
	err := tx.QueryRow(ctx,
		`SELECT a.connector,
		        EXISTS (SELECT 1 FROM vault_accounts va WHERE va.entitlement_id = e.id),
		        EXISTS (SELECT 1 FROM access_rule_grants g WHERE g.entitlement_id = e.id
		                   OR g.role_id IN (SELECT role_id FROM role_entitlements WHERE entitlement_id = e.id))
		 FROM entitlements e JOIN applications a ON a.id = e.application_id WHERE e.id = $1 FOR UPDATE OF e`, id).
		Scan(&connector, &vault, &birthright)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entitlement{}, apperr.ErrNotFound
	}
	if err != nil {
		return Entitlement{}, err
	}
	switch {
	case connector == builtinConnector:
		return Entitlement{}, apperr.Invalidf("platform roles are fixed; manage their members under platform roles")
	case vault && !j.JITOnly:
		return Entitlement{}, apperr.Invalidf("vault access is always just-in-time only")
	case j.JITOnly && birthright:
		return Entitlement{}, apperr.Conflictf("a birthright rule gives this entitlement, directly or through a role; remove it from the rule first")
	}
	if _, err := tx.Exec(ctx,
		`UPDATE entitlements SET jit_only = $2, max_activation_hours = $3, activation_approval = $4 WHERE id = $1`,
		id, j.JITOnly, j.MaxActivationHours, j.ActivationApproval); err != nil {
		return Entitlement{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "entitlement.jit_updated", "entitlement", id, map[string]any{
		"jit_only": j.JITOnly, "max_activation_hours": j.MaxActivationHours, "activation_approval": j.ActivationApproval}); err != nil {
		return Entitlement{}, err
	}
	return entitlementByID(ctx, tx, id)
}

func lockApplication(ctx context.Context, tx pgx.Tx, id string) (Application, error) {
	var a Application
	if !db.ValidUUID(id) {
		return a, apperr.ErrNotFound
	}
	err := tx.QueryRow(ctx, `SELECT id, name, connector, retired_at FROM applications WHERE id = $1 FOR SHARE`, id).
		Scan(&a.ID, &a.Name, &a.Connector, &a.RetiredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, apperr.ErrNotFound
	}
	return a, err
}

const entitlementColumns = `e.id, e.application_id, a.name, e.native_id, e.name, coalesce(e.description, ''), e.risk,
	e.jit_only, e.max_activation_hours, e.activation_approval, e.created_at`

func scanEntitlement(row pgx.CollectableRow) (Entitlement, error) {
	var e Entitlement
	err := row.Scan(&e.ID, &e.ApplicationID, &e.ApplicationName, &e.NativeID, &e.Name, &e.Description, &e.Risk,
		&e.JITOnly, &e.MaxActivationHours, &e.ActivationApproval, &e.CreatedAt)
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
// one of the tenant's, outside the built-in application and retired ones.
// Platform roles are granted only through their own guarded flow.
func CheckEntitlements(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, id := range ids {
		var connector, app string
		var retired bool
		err := pgx.ErrNoRows
		if db.ValidUUID(id) {
			err = tx.QueryRow(ctx,
				`SELECT a.connector, a.name, a.retired_at IS NOT NULL
				 FROM entitlements e JOIN applications a ON a.id = e.application_id WHERE e.id = $1`, id).Scan(&connector, &app, &retired)
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
		if retired {
			return errRetired(app)
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
	if id != "" {
		// Birthright rules cannot give just-in-time only access (see
		// package access), so neither can a role they give.
		var jit bool
		err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM entitlements WHERE id = ANY($1::uuid[]) AND jit_only)
			   AND EXISTS (SELECT 1 FROM access_rule_grants WHERE role_id = $2)`, ents, id).Scan(&jit)
		if err != nil {
			return Role{}, err
		}
		if jit {
			return Role{}, apperr.Invalidf("a birthright rule gives this role, so it cannot include just-in-time only access")
		}
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
	if db.IsUniqueViolation(err) {
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
		`SELECT r.id, r.name, coalesce(r.description, ''), coalesce(r.owner_id::text, ''), `+ownerName("r.owner_id")+`, r.created_at
		 FROM roles r WHERE $1 = '' OR r.id = $1::uuid ORDER BY r.name, r.id`, id)
	if err != nil {
		return nil, err
	}
	roles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Role, error) {
		r := Role{Entitlements: []Entitlement{}}
		err := row.Scan(&r.ID, &r.Name, &r.Description, &r.OwnerID, &r.OwnerName, &r.CreatedAt)
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
		if err := rows.Scan(&roleID, &e.ID, &e.ApplicationID, &e.ApplicationName, &e.NativeID, &e.Name, &e.Description, &e.Risk,
			&e.JITOnly, &e.MaxActivationHours, &e.ActivationApproval, &e.CreatedAt); err != nil {
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
