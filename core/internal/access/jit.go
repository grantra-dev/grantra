package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// OriginActivation marks access someone activated from an eligibility. It
// always has an end date.
const OriginActivation = "activation"

// Eligibility says an identity may activate a role or entitlement when they
// need it, for a few hours at a time.
type Eligibility struct {
	ID         string `json:"id"`
	IdentityID string `json:"identity_id"`
	// Type is "role" or "entitlement".
	Type        string `json:"type"`
	TargetID    string `json:"target_id"`
	Name        string `json:"name"`
	Application string `json:"application,omitempty"`
	// MaxActivationHours and ActivationApproval come from the entitlement,
	// or for a role from the strictest of its entitlements.
	MaxActivationHours int        `json:"max_activation_hours"`
	ActivationApproval bool       `json:"activation_approval"`
	GrantedBy          string     `json:"granted_by"`
	GrantedAt          time.Time  `json:"granted_at"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	RequestID          string     `json:"request_id,omitempty"`
	// Active is the activation running now, if any.
	Active *Activation `json:"active,omitempty"`
}

// Activation is access activated from an eligibility.
type Activation struct {
	AssignmentID  string    `json:"assignment_id"`
	EligibilityID string    `json:"eligibility_id"`
	Justification string    `json:"justification"`
	GrantedAt     time.Time `json:"granted_at"`
	ExpiresAt     time.Time `json:"expires_at"`
}

// Target is a role or an entitlement: exactly one is set.
type Target struct {
	RoleID        string `json:"role_id,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
}

func (t Target) check(ctx context.Context, tx pgx.Tx) error {
	if (t.RoleID == "") == (t.EntitlementID == "") {
		return apperr.Invalidf("give exactly one of role_id and entitlement_id")
	}
	if t.RoleID != "" {
		return catalog.CheckRoles(ctx, tx, []string{t.RoleID})
	}
	return catalog.CheckEntitlements(ctx, tx, []string{t.EntitlementID})
}

func (t Target) details(m map[string]any) map[string]any {
	if t.RoleID != "" {
		m["role_id"] = t.RoleID
	} else {
		m["entitlement_id"] = t.EntitlementID
	}
	return m
}

// JITOnly reports whether nobody may hold the target without an end date.
func JITOnly(ctx context.Context, tx pgx.Tx, t Target) (bool, error) {
	var jit bool
	err := tx.QueryRow(ctx, `SELECT access_is_jit_only($1, $2)`, db.Nullable(t.RoleID), db.Nullable(t.EntitlementID)).Scan(&jit)
	return jit, err
}

// ActivationRules returns how long an activation of the target may last and
// whether it needs an approval. A role takes the strictest of its
// entitlements; an empty role the defaults.
func ActivationRules(ctx context.Context, tx pgx.Tx, t Target) (maxHours int, approval bool, err error) {
	err = tx.QueryRow(ctx,
		`SELECT coalesce(min(max_activation_hours), $3), coalesce(bool_or(activation_approval), false)
		 FROM entitlements
		 WHERE id = $2 OR id IN (SELECT entitlement_id FROM role_entitlements WHERE role_id = $1)`,
		db.Nullable(t.RoleID), db.Nullable(t.EntitlementID), catalog.DefaultActivationHours).Scan(&maxHours, &approval)
	return maxHours, approval, err
}

// MakeEligible lets identityID activate the target, until expires (nil: until
// removed). Making someone eligible again replaces the end date.
func MakeEligible(ctx context.Context, tx pgx.Tx, tenantID, actor, identityID string, t Target, expires *time.Time, requestID string) (string, error) {
	if err := t.check(ctx, tx); err != nil {
		return "", err
	}
	var status string
	err := pgx.ErrNoRows
	if db.ValidUUID(identityID) {
		err = tx.QueryRow(ctx, `SELECT status FROM identities WHERE id = $1`, identityID).Scan(&status)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "", apperr.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if status != "active" {
		return "", apperr.Conflictf("only active people can be made eligible")
	}
	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO access_eligibilities (tenant_id, identity_id, role_id, entitlement_id, granted_by, expires_at, request_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (identity_id, coalesce(role_id, entitlement_id)) DO UPDATE
		   SET granted_by = EXCLUDED.granted_by, granted_at = now(), expires_at = EXCLUDED.expires_at, request_id = EXCLUDED.request_id
		 RETURNING id`,
		tenantID, identityID, db.Nullable(t.RoleID), db.Nullable(t.EntitlementID), actor, expires, db.Nullable(requestID)).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("make eligible: %w", err)
	}
	details := t.details(map[string]any{"eligibility_id": id})
	if expires != nil {
		details["expires_at"] = expires.UTC()
	}
	if requestID != "" {
		details["request_id"] = requestID
	}
	return id, db.Audit(ctx, tx, tenantID, actor, "access.eligible", "identity", identityID, details)
}

const eligibilityColumns = `el.id, el.identity_id, CASE WHEN el.role_id IS NULL THEN 'entitlement' ELSE 'role' END,
	coalesce(el.role_id, el.entitlement_id), coalesce(ro.name, e.name), coalesce(app.name, ''),
	el.granted_by, el.granted_at, el.expires_at, coalesce(el.request_id::text, ''),
	x.id, coalesce(x.justification, ''), x.granted_at, x.expires_at`

const eligibilityFrom = ` FROM access_eligibilities el
	LEFT JOIN roles ro ON ro.id = el.role_id
	LEFT JOIN entitlements e ON e.id = el.entitlement_id
	LEFT JOIN applications app ON app.id = e.application_id
	LEFT JOIN access_assignments x ON x.eligibility_id = el.id AND x.origin = 'activation' AND x.expires_at > now()`

func scanEligibility(row pgx.CollectableRow) (Eligibility, error) {
	var (
		el        Eligibility
		actID     *string
		just      string
		actAt     *time.Time
		actExpiry *time.Time
	)
	err := row.Scan(&el.ID, &el.IdentityID, &el.Type, &el.TargetID, &el.Name, &el.Application,
		&el.GrantedBy, &el.GrantedAt, &el.ExpiresAt, &el.RequestID, &actID, &just, &actAt, &actExpiry)
	if actID != nil {
		el.Active = &Activation{AssignmentID: *actID, EligibilityID: el.ID, Justification: just, GrantedAt: *actAt, ExpiresAt: *actExpiry}
	}
	return el, err
}

func (el *Eligibility) target() Target {
	if el.Type == "role" {
		return Target{RoleID: el.TargetID}
	}
	return Target{EntitlementID: el.TargetID}
}

func withRules(ctx context.Context, tx pgx.Tx, list []Eligibility) error {
	for i := range list {
		h, a, err := ActivationRules(ctx, tx, list[i].target())
		if err != nil {
			return err
		}
		list[i].MaxActivationHours, list[i].ActivationApproval = h, a
	}
	return nil
}

// Eligibilities lists what identityID may activate, unexpired, with the
// activations running now.
func Eligibilities(ctx context.Context, tx pgx.Tx, identityID string) ([]Eligibility, error) {
	rows, err := tx.Query(ctx, `SELECT `+eligibilityColumns+eligibilityFrom+`
		WHERE el.identity_id = $1 AND (el.expires_at IS NULL OR el.expires_at > now())
		ORDER BY 5, 4`, identityID)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scanEligibility)
	if err != nil {
		return nil, err
	}
	return list, withRules(ctx, tx, list)
}

// GetEligibility returns one eligibility, expired or not. lock holds it.
func GetEligibility(ctx context.Context, tx pgx.Tx, id string, lock bool) (Eligibility, error) {
	if !db.ValidUUID(id) {
		return Eligibility{}, apperr.ErrNotFound
	}
	if lock {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM access_eligibilities WHERE id = $1 FOR UPDATE`, id); err != nil {
			return Eligibility{}, err
		}
	}
	el, err := db.QueryOne(ctx, tx, scanEligibility, `SELECT `+eligibilityColumns+eligibilityFrom+` WHERE el.id = $1`, id)
	if err != nil {
		return Eligibility{}, err
	}
	list := []Eligibility{el}
	return list[0], withRules(ctx, tx, list)
}

// FindEligibility returns the unexpired eligibility of identityID for the
// target, or apperr.ErrNotFound.
func FindEligibility(ctx context.Context, tx pgx.Tx, identityID string, t Target) (Eligibility, error) {
	var id string
	err := tx.QueryRow(ctx,
		`SELECT id FROM access_eligibilities
		 WHERE identity_id = $1 AND coalesce(role_id, entitlement_id) = coalesce($2::uuid, $3::uuid)
		   AND (expires_at IS NULL OR expires_at > now())`,
		identityID, db.Nullable(t.RoleID), db.Nullable(t.EntitlementID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Eligibility{}, apperr.ErrNotFound
	}
	if err != nil {
		return Eligibility{}, err
	}
	return GetEligibility(ctx, tx, id, true)
}

// RemoveEligibility takes an eligibility away, ending its activation.
func RemoveEligibility(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	el, err := GetEligibility(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if el.Active != nil {
		if err := endActivation(ctx, tx, tenantID, actor, el, "eligibility removed"); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM access_eligibilities WHERE id = $1`, id); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "access.eligibility_removed", "identity", el.IdentityID,
		el.target().details(map[string]any{"eligibility_id": id}))
}

// CheckActivation validates an activation of el for hours.
func CheckActivation(el Eligibility, hours int, justification string) (string, error) {
	justification = strings.TrimSpace(justification)
	switch {
	case justification == "":
		return "", apperr.Invalidf("justification is required: say why you need the access now")
	case len(justification) > 2000:
		return "", apperr.Invalidf("justification is longer than 2000 characters")
	case hours < 1 || hours > el.MaxActivationHours:
		return "", apperr.Invalidf("hours must be between 1 and %d for %s", el.MaxActivationHours, el.Name)
	case el.ExpiresAt != nil && !el.ExpiresAt.After(time.Now()):
		return "", apperr.Conflictf("this eligibility has ended")
	case el.Active != nil:
		return "", apperr.Conflictf("%s is already active until %s", el.Name, el.Active.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return justification, nil
}

// Activate gives the access of a locked eligibility for hours. The caller
// has decided no approval is needed, or has it (requestID). Provisioning
// carries the change to the applications at its next quick pass.
func Activate(ctx context.Context, tx pgx.Tx, tenantID, actor string, el Eligibility, hours int, justification, requestID string) (Activation, error) {
	justification, err := CheckActivation(el, hours, justification)
	if err != nil {
		return Activation{}, err
	}
	var active bool
	if err := tx.QueryRow(ctx, `SELECT status = 'active' FROM identities WHERE id = $1`, el.IdentityID).Scan(&active); err != nil {
		return Activation{}, err
	}
	if !active {
		return Activation{}, apperr.Conflictf("only active people can activate access")
	}
	t := el.target()
	// An activation that ran out but is not cleaned up yet makes way.
	if _, err := tx.Exec(ctx,
		`DELETE FROM access_assignments WHERE eligibility_id = $1 AND origin = 'activation' AND expires_at <= now()`, el.ID); err != nil {
		return Activation{}, err
	}
	a := Activation{EligibilityID: el.ID, Justification: justification}
	err = tx.QueryRow(ctx,
		`INSERT INTO access_assignments (tenant_id, identity_id, role_id, entitlement_id, origin, granted_by, expires_at,
		                                 eligibility_id, justification, request_id)
		 VALUES ($1, $2, $3, $4, 'activation', $5, least(now() + make_interval(hours => $6), $10::timestamptz), $7, $8, $9)
		 ON CONFLICT DO NOTHING
		 RETURNING id, granted_at, expires_at`,
		tenantID, el.IdentityID, db.Nullable(t.RoleID), db.Nullable(t.EntitlementID), actor, hours, el.ID, justification,
		db.Nullable(requestID), el.ExpiresAt).Scan(&a.AssignmentID, &a.GrantedAt, &a.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Activation{}, apperr.Conflictf("%s is already active", el.Name)
	}
	if err != nil {
		return Activation{}, fmt.Errorf("activate: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE access_eligibilities SET last_activated_at = now() WHERE id = $1`, el.ID); err != nil {
		return Activation{}, err
	}
	details := t.details(map[string]any{"assignment_id": a.AssignmentID, "origin": OriginActivation, "eligibility_id": el.ID,
		"hours": hours, "justification": justification, "expires_at": a.ExpiresAt.UTC()})
	if requestID != "" {
		details["request_id"] = requestID
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access.activated", "identity", el.IdentityID, details); err != nil {
		return Activation{}, err
	}
	if err := adopt(ctx, tx, el.IdentityID, t); err != nil {
		return Activation{}, err
	}
	return a, queueProvisioning(ctx, tx, tenantID, t)
}

// adopt marks the memberships someone already has in applications for t as
// granted by Grantra, so provisioning removes them when the access ends.
// Memberships found by reading an application are otherwise left alone
// unless the application removes everything unassigned, and just-in-time
// access would stay in the application for good.
func adopt(ctx context.Context, tx pgx.Tx, identityID string, t Target) error {
	_, err := tx.Exec(ctx,
		`UPDATE account_entitlements ae SET provisioned_at = now()
		 FROM accounts a
		 WHERE a.id = ae.account_id AND a.identity_id = $1 AND ae.provisioned_at IS NULL
		   AND (ae.entitlement_id = $3 OR ae.entitlement_id IN (SELECT entitlement_id FROM role_entitlements WHERE role_id = $2))`,
		identityID, db.Nullable(t.RoleID), db.Nullable(t.EntitlementID))
	return err
}

// Deactivate ends a running activation before its time.
func Deactivate(ctx context.Context, tx pgx.Tx, tenantID, actor string, el Eligibility) error {
	if el.Active == nil {
		return apperr.Conflictf("%s is not active", el.Name)
	}
	return endActivation(ctx, tx, tenantID, actor, el, "deactivated")
}

func endActivation(ctx context.Context, tx pgx.Tx, tenantID, actor string, el Eligibility, reason string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM access_assignments WHERE id = $1`, el.Active.AssignmentID); err != nil {
		return err
	}
	t := el.target()
	if err := db.Audit(ctx, tx, tenantID, actor, "access.removed", "identity", el.IdentityID,
		t.details(map[string]any{"assignment_id": el.Active.AssignmentID, "origin": OriginActivation,
			"eligibility_id": el.ID, "reason": reason})); err != nil {
		return err
	}
	return queueProvisioning(ctx, tx, tenantID, t)
}

// queueProvisioning asks the next quick provisioning pass to carry a change
// of the target to its applications.
func queueProvisioning(ctx context.Context, tx pgx.Tx, tenantID string, t Target) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO jit_provisioning_queue (tenant_id, application_id)
		 SELECT DISTINCT $1::uuid, e.application_id FROM entitlements e
		 WHERE e.id = $3 OR e.id IN (SELECT entitlement_id FROM role_entitlements WHERE role_id = $2)
		 ON CONFLICT DO NOTHING`, tenantID, db.Nullable(t.RoleID), db.Nullable(t.EntitlementID))
	return err
}

// JITHousekeeping counts what EndActivations did.
type JITHousekeeping struct {
	ActivationsEnded   int `json:"activations_ended"`
	EligibilitiesEnded int `json:"eligibilities_ended"`
}

// EndActivations removes activations whose time ran out and eligibilities
// that ended, and queues the applications to provision (see
// provisioning.ProvisionQueued).
func EndActivations(ctx context.Context, tx pgx.Tx, tenantID string) (JITHousekeeping, error) {
	const actor = "system:jit"
	var h JITHousekeeping
	rows, err := tx.Query(ctx,
		`DELETE FROM access_assignments WHERE origin = 'activation' AND expires_at <= now()
		 RETURNING id, identity_id, coalesce(role_id::text, ''), coalesce(entitlement_id::text, ''), coalesce(eligibility_id::text, '')`)
	if err != nil {
		return h, err
	}
	type ended struct {
		id, identity, eligibility string
		t                         Target
	}
	gone, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ended, error) {
		var e ended
		err := row.Scan(&e.id, &e.identity, &e.t.RoleID, &e.t.EntitlementID, &e.eligibility)
		return e, err
	})
	if err != nil {
		return h, err
	}
	for _, e := range gone {
		details := e.t.details(map[string]any{"assignment_id": e.id, "origin": OriginActivation, "reason": "expired"})
		if e.eligibility != "" {
			details["eligibility_id"] = e.eligibility
		}
		if err := db.Audit(ctx, tx, tenantID, actor, "access.removed", "identity", e.identity, details); err != nil {
			return h, err
		}
		if err := queueProvisioning(ctx, tx, tenantID, e.t); err != nil {
			return h, err
		}
	}
	h.ActivationsEnded = len(gone)

	// An eligibility that ends takes a running activation with it.
	rows, err = tx.Query(ctx, `SELECT id FROM access_eligibilities WHERE expires_at <= now() FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return h, err
	}
	expired, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return h, err
	}
	for _, id := range expired {
		el, err := GetEligibility(ctx, tx, id, false)
		if err != nil {
			return h, err
		}
		if el.Active != nil {
			if err := endActivation(ctx, tx, tenantID, actor, el, "eligibility ended"); err != nil {
				return h, err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM access_eligibilities WHERE id = $1`, id); err != nil {
			return h, err
		}
		if err := db.Audit(ctx, tx, tenantID, actor, "access.eligibility_removed", "identity", el.IdentityID,
			el.target().details(map[string]any{"eligibility_id": id, "reason": "expired"})); err != nil {
			return h, err
		}
	}
	h.EligibilitiesEnded = len(expired)
	return h, nil
}
