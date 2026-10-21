// Package requests lets people ask for access. A request names a business
// role or an entitlement, who it is for (the requester or one of their
// reports) and why. Approvers decide in order, following the tenant's
// approval policy for the request (package approvals), copied into the
// request when it is made: by default the person's manager, then the owner
// of the role or of the entitlement's application. A step may ask several
// people and need one, some or all of them. Once every step is approved the
// request becomes an assignment with origin "request", which provisioning
// carries to the application.
//
// Steps with nobody to ask go to the admins. An approval the requester would
// give themselves, such as a manager asking for a report, is given by
// asking. A step not decided in time (its policy's, or the tenant's
// settings.RequestStepTimeoutHours) expires the request, rejects it or is
// escalated once to the approvers' managers, as its policy says.
//
// Access that would break a prevent-mode separation of duties policy adds a
// last "risk" step: an admin other than the person getting the access
// accepts the risk, with a reason and an end date, or rejects the request.
// When the access is granted the violations are recorded as exceptions.
package requests

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/approvals"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
	"github.com/grantra-dev/grantra/core/internal/settings"
	"github.com/grantra-dev/grantra/core/internal/sod"
)

// Request statuses.
const (
	Pending   = "pending"
	Approved  = "approved"
	Rejected  = "rejected"
	Cancelled = "cancelled"
	Expired   = "expired"
)

// Request kinds.
const (
	// KindAccess asks for the access itself.
	KindAccess = "access"
	// KindEligibility asks to be made eligible: to activate the access for
	// a few hours whenever it is needed.
	KindEligibility = "eligibility"
	// KindActivation activates an eligibility whose access needs approval.
	KindActivation = "activation"
)

// Step statuses.
const (
	StepWaiting  = "waiting"
	StepPending  = "pending"
	StepApproved = "approved"
	StepRejected = "rejected"
	StepSkipped  = "skipped"
)

// Step kinds: who decides (see package approvals).
const (
	KindManager = approvals.Manager
	KindOwner   = approvals.Owner
	KindPeople  = approvals.People
	KindHolders = approvals.Holders
	KindAdmin   = approvals.Admin
	// KindRisk is decided by admins: approving it accepts a separation of
	// duties risk.
	KindRisk = "risk"
)

// Approver is someone a step asks.
type Approver struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
}

// Approval is someone's approval of a step.
type Approval struct {
	IdentityID string    `json:"identity_id"`
	Name       string    `json:"name"`
	Comment    string    `json:"comment,omitempty"`
	At         time.Time `json:"at"`
}

// Step is one approval a request needs.
type Step struct {
	Position int    `json:"position"`
	Kind     string `json:"kind"`
	// Label is the step's name in its policy, if it has one.
	Label string `json:"label,omitempty"`
	// Approvers are who the step asks; none means any admin decides.
	Approvers []Approver `json:"approvers"`
	// Required is how many approvals the step needs.
	Required  int        `json:"required"`
	Approvals []Approval `json:"approvals"`
	Status    string     `json:"status"`
	DueAt     *time.Time `json:"due_at,omitempty"`
	// OnTimeout is what happens at DueAt: expire, escalate or reject.
	OnTimeout string `json:"on_timeout"`
	// EscalatedFrom names who the step asked before it was escalated.
	EscalatedFrom []string   `json:"escalated_from,omitempty"`
	EscalatedAt   *time.Time `json:"escalated_at,omitempty"`
	DecidedBy     string     `json:"decided_by,omitempty"`
	DecidedAt     *time.Time `json:"decided_at,omitempty"`
	Comment       string     `json:"comment,omitempty"`
	// ExceptionDays is how long an accepted risk lasts (risk steps).
	ExceptionDays *int `json:"exception_days,omitempty"`
}

func (s Step) approved(identity string) bool {
	return slices.ContainsFunc(s.Approvals, func(a Approval) bool { return a.IdentityID == identity })
}

// Deciders returns the approvers who may still approve the step: active,
// and not approved yet. admins says the step also waits on the admins,
// because it asks nobody or too few are left to give the approvals it needs.
func (s Step) Deciders() (ids []string, admins bool) {
	for _, a := range s.Approvers {
		if a.Active && !s.approved(a.ID) {
			ids = append(ids, a.ID)
		}
	}
	return ids, len(s.Approvers) == 0 || len(ids) < s.Required-len(s.Approvals)
}

// Request is an access request with its approval steps.
type Request struct {
	ID              string `json:"id"`
	RequesterID     string `json:"requester_id"`
	RequesterName   string `json:"requester_name"`
	BeneficiaryID   string `json:"beneficiary_id"`
	BeneficiaryName string `json:"beneficiary_name"`
	// Type is "role" or "entitlement".
	Type          string `json:"type"`
	TargetID      string `json:"target_id"`
	TargetName    string `json:"target_name"`
	Application   string `json:"application,omitempty"`
	Justification string `json:"justification"`
	// Kind is access, eligibility or activation.
	Kind          string     `json:"kind"`
	DurationDays  *int       `json:"duration_days,omitempty"`
	DurationHours *int       `json:"duration_hours,omitempty"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	// PolicyID, PolicyName and PolicyVersion are the approval policy the
	// request followed; with none, the built-in flow.
	PolicyID      string `json:"policy_id,omitempty"`
	PolicyName    string `json:"policy_name,omitempty"`
	PolicyVersion *int   `json:"policy_version,omitempty"`
	Steps         []Step `json:"steps"`
	// SoDConflicts names, while a risk step is undecided, the policies the
	// access would break.
	SoDConflicts []string `json:"sod_conflicts,omitempty"`
}

// Current returns the step the request waits on, if any.
func (r Request) Current() *Step {
	for i := range r.Steps {
		if r.Steps[i].Status == StepPending {
			return &r.Steps[i]
		}
	}
	return nil
}

// Resource describes the request to the policies.
func (r Request) Resource() authz.Resource {
	var approvers, current []string
	for _, s := range r.Steps {
		for _, a := range s.Approvers {
			approvers = append(approvers, a.ID)
		}
	}
	if c := r.Current(); c != nil {
		current, _ = c.Deciders()
	}
	return authz.AccessRequestResource(r.ID, r.RequesterID, r.BeneficiaryID, approvers, current)
}

// Input is what someone asks for. Exactly one of RoleID and EntitlementID is
// set; BeneficiaryID defaults to the requester. Kind is access (the default)
// or eligibility. Access lasts DurationDays or DurationHours, or until
// removed when neither is set; an eligibility lasts DurationDays.
type Input struct {
	BeneficiaryID string `json:"beneficiary_id,omitempty"`
	RoleID        string `json:"role_id,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Justification string `json:"justification"`
	DurationDays  *int   `json:"duration_days,omitempty"`
	DurationHours *int   `json:"duration_hours,omitempty"`
}

func (in *Input) target() access.Target {
	return access.Target{RoleID: in.RoleID, EntitlementID: in.EntitlementID}
}

func (in *Input) check() error {
	if (in.RoleID == "") == (in.EntitlementID == "") {
		return apperr.Invalidf("ask for exactly one of role_id and entitlement_id")
	}
	if in.Kind == "" {
		in.Kind = KindAccess
	}
	switch in.Kind {
	case KindAccess, KindEligibility, KindActivation:
	default:
		return apperr.Invalidf("kind must be access or eligibility")
	}
	if in.DurationDays != nil && in.DurationHours != nil {
		return apperr.Invalidf("give duration_days or duration_hours, not both")
	}
	if in.DurationHours != nil && (*in.DurationHours < 1 || *in.DurationHours > 24) {
		return apperr.Invalidf("duration_hours must be between 1 and 24")
	}
	if in.Kind == KindEligibility && in.DurationHours != nil {
		return apperr.Invalidf("an eligibility lasts days (duration_days); each activation lasts hours")
	}
	in.Justification = strings.TrimSpace(in.Justification)
	if in.Justification == "" {
		return apperr.Invalidf("justification is required: say why the access is needed")
	}
	if len(in.Justification) > 2000 {
		return apperr.Invalidf("justification is longer than 2000 characters")
	}
	if in.DurationDays != nil && (*in.DurationDays < 1 || *in.DurationDays > 365) {
		return apperr.Invalidf("duration_days must be between 1 and 365")
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// Create records a request from requesterID and works out its approvals. The
// caller has checked that the requester may ask for the beneficiary. A
// request whose every step is approved at once (an admin asking, or a manager
// who also owns the application) is granted right away.
func Create(ctx context.Context, tx pgx.Tx, tenantID, requesterID string, in Input) (Request, error) {
	if in.Kind == KindActivation {
		return Request{}, apperr.Invalidf("kind must be access or eligibility: activate an eligibility from its own page")
	}
	return create(ctx, tx, tenantID, requesterID, in)
}

func create(ctx context.Context, tx pgx.Tx, tenantID, requesterID string, in Input) (Request, error) {
	if in.BeneficiaryID == "" {
		in.BeneficiaryID = requesterID
	}
	if err := in.check(); err != nil {
		return Request{}, err
	}
	if err := checkTarget(ctx, tx, in); err != nil {
		return Request{}, err
	}
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM identities WHERE id = $1`, in.BeneficiaryID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, apperr.ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if status != "active" {
		return Request{}, apperr.Conflictf("access can only be requested for active people")
	}
	if in.Kind == KindEligibility {
		if _, err := access.FindEligibility(ctx, tx, in.BeneficiaryID, in.target()); err == nil {
			return Request{}, apperr.Conflictf("this person is already eligible for that access")
		} else if !errors.Is(err, apperr.ErrNotFound) {
			return Request{}, err
		}
	} else {
		var held bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM access_assignments
			                WHERE identity_id = $1 AND coalesce(role_id, entitlement_id) = $2::uuid
			                  AND (expires_at IS NULL OR expires_at > now()))`,
			in.BeneficiaryID, in.RoleID+in.EntitlementID).Scan(&held); err != nil {
			return Request{}, err
		}
		if held {
			return Request{}, apperr.Conflictf("this person already has that access")
		}
	}
	if in.Kind == KindAccess && in.DurationDays == nil && in.DurationHours == nil {
		jit, err := access.JITOnly(ctx, tx, in.target())
		if err != nil {
			return Request{}, err
		}
		if jit {
			return Request{}, apperr.Invalidf("nobody holds this access for good: ask for it for a number of hours or days, or to be made eligible")
		}
	}
	conflicts, err := sod.Conflicts(ctx, tx, in.BeneficiaryID, in.RoleID, in.EntitlementID)
	if err != nil {
		return Request{}, err
	}
	plan, err := approvals.Resolve(ctx, tx, in.approvalRequest(requesterID))
	if err != nil {
		return Request{}, err
	}

	var id string
	var version *int
	if plan.PolicyID != "" {
		version = &plan.PolicyVersion
	}
	err = tx.QueryRow(ctx,
		`INSERT INTO access_requests (tenant_id, requester_id, beneficiary_id, role_id, entitlement_id, justification,
		                             kind, duration_days, duration_hours, policy_id, policy_name, policy_version)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		tenantID, requesterID, in.BeneficiaryID, db.Nullable(in.RoleID), db.Nullable(in.EntitlementID), in.Justification,
		in.Kind, in.DurationDays, in.DurationHours, db.Nullable(plan.PolicyID), db.Nullable(plan.PolicyName), version).Scan(&id)
	if db.IsUniqueViolation(err) {
		return Request{}, apperr.Conflictf("there is already an open request for this access")
	}
	if err != nil {
		return Request{}, err
	}

	// The steps of the plan, then the separation of duties risk. An
	// approval the requester would give is given by asking.
	steps := plan.Steps
	if len(conflicts) > 0 {
		steps = append(steps, approvals.Planned{Kind: KindRisk, Approvers: []approvals.Person{}, Required: 1, OnTimeout: approvals.Expire})
	}
	for n, s := range steps {
		st, decidedBy, comment := StepWaiting, "", ""
		if s.RequesterApproves && s.Required == 1 {
			st, decidedBy, comment = StepApproved, db.IdentityActor(requesterID), "asked for it"
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO access_request_steps (tenant_id, request_id, position, kind, approver_ids, required, label, timeout_hours, on_timeout,
			                                   status, decided_by, decided_at, comment)
			 VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), nullif($8, 0), $9,
			         $10, nullif($11, ''), CASE WHEN $11 = '' THEN NULL ELSE now() END, nullif($12, ''))`,
			tenantID, id, n+1, s.Kind, s.IDs(), s.Required, s.Label, s.TimeoutHours, s.OnTimeout,
			st, decidedBy, comment); err != nil {
			return Request{}, err
		}
		if s.RequesterApproves {
			if err := addApproval(ctx, tx, tenantID, id, n+1, requesterID, "asked for it"); err != nil {
				return Request{}, err
			}
		}
	}
	actor := db.IdentityActor(requesterID)
	if err := db.Audit(ctx, tx, tenantID, actor, "access_request.created", "access_request", id,
		map[string]any{"beneficiary_id": in.BeneficiaryID, "role_id": in.RoleID, "entitlement_id": in.EntitlementID,
			"kind": in.Kind, "duration_days": in.DurationDays, "duration_hours": in.DurationHours, "sod_conflicts": conflicts,
			"policy_id": plan.PolicyID, "policy_name": plan.PolicyName, "policy_version": plan.PolicyVersion}); err != nil {
		return Request{}, err
	}
	if err := advance(ctx, tx, tenantID, actor, id); err != nil {
		return Request{}, err
	}
	return Get(ctx, tx, id, false)
}

// checkTarget checks that the role or entitlement in asks for exists.
func checkTarget(ctx context.Context, tx pgx.Tx, in Input) error {
	if in.RoleID != "" {
		return catalog.CheckRoles(ctx, tx, []string{in.RoleID})
	}
	return catalog.CheckEntitlements(ctx, tx, []string{in.EntitlementID})
}

func (in *Input) approvalRequest(requesterID string) approvals.Request {
	return approvals.Request{Kind: in.Kind, RoleID: in.RoleID, EntitlementID: in.EntitlementID, RequesterID: requesterID,
		BeneficiaryID: in.BeneficiaryID, DurationDays: in.DurationDays, DurationHours: in.DurationHours}
}

// Preview tells, before asking, who would approve what in asks for and
// which prevent-mode separation of duties policies it would break (which
// adds a step for an admin), as Create would work it out for requesterID.
// draft, when given, is a policy being edited, tried before the saved ones.
func Preview(ctx context.Context, tx pgx.Tx, requesterID string, in Input, draft *approvals.Policy) (approvals.Plan, []string, error) {
	if in.BeneficiaryID == "" {
		in.BeneficiaryID = requesterID
	}
	if (in.RoleID == "") == (in.EntitlementID == "") {
		return approvals.Plan{}, nil, apperr.Invalidf("ask for exactly one of role_id and entitlement_id")
	}
	if in.Kind == "" {
		in.Kind = KindAccess
	}
	if err := checkTarget(ctx, tx, in); err != nil {
		return approvals.Plan{}, nil, err
	}
	plan, err := approvals.ResolveWith(ctx, tx, in.approvalRequest(requesterID), draft)
	if err != nil {
		return plan, nil, err
	}
	conflicts, err := sod.Conflicts(ctx, tx, in.BeneficiaryID, in.RoleID, in.EntitlementID)
	return plan, conflicts, err
}

// addApproval records identity's approval of a step.
func addApproval(ctx context.Context, tx pgx.Tx, tenantID, request string, position int, identity, comment string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO access_request_approvals (tenant_id, request_id, position, identity_id, comment) VALUES ($1, $2, $3, $4, nullif($5, ''))`,
		tenantID, request, position, identity, comment)
	return err
}

// dueAt is when a step starting now must be decided, as SQL on the step's
// row: in its policy's time, or the tenant's.
var dueAt = `now() + make_interval(hours => coalesce(timeout_hours, ` + settings.SQL(settings.RequestStepTimeoutHours) + `))`

// advance moves a pending request to its next undecided step, or grants it
// when there is none. A risk step whose conflict went away meanwhile is
// skipped.
func advance(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	var (
		next        string
		beneficiary string
		role, ent   *string
	)
	err := tx.QueryRow(ctx,
		`SELECT s.kind, r.beneficiary_id, r.role_id, r.entitlement_id
		 FROM access_request_steps s JOIN access_requests r ON r.id = s.request_id
		 WHERE s.request_id = $1 AND s.status = 'waiting' ORDER BY s.position LIMIT 1`, id).Scan(&next, &beneficiary, &role, &ent)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if next == KindRisk {
		conflicts, err := sod.Conflicts(ctx, tx, beneficiary, deref(role), deref(ent))
		if err != nil {
			return err
		}
		if len(conflicts) == 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE access_request_steps SET status = 'skipped', comment = 'no separation of duties conflict any more'
				 WHERE request_id = $1 AND kind = 'risk' AND status = 'waiting'`, id); err != nil {
				return err
			}
		}
	}
	tag, err := tx.Exec(ctx,
		`UPDATE access_request_steps SET status = 'pending', due_at = `+dueAt+`
		 WHERE request_id = $1 AND position = (
		   SELECT min(position) FROM access_request_steps WHERE request_id = $1 AND status = 'waiting')
		   AND NOT EXISTS (SELECT 1 FROM access_request_steps WHERE request_id = $1 AND status = 'pending')`,
		id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		return notifyApprovers(ctx, tx, tenantID, id, notify.ApprovalNeeded)
	}
	var open bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM access_request_steps WHERE request_id = $1 AND status IN ('pending', 'waiting'))`, id).Scan(&open); err != nil || open {
		return err
	}
	return grant(ctx, tx, tenantID, actor, id)
}

// grant closes an approved request and gives the access.
func grant(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	var (
		beneficiary, kind, justification string
		role, ent                        *string
		days, hours                      *int
	)
	if err := tx.QueryRow(ctx,
		`SELECT beneficiary_id, role_id, entitlement_id, kind, justification, duration_days, duration_hours
		 FROM access_requests WHERE id = $1`, id).
		Scan(&beneficiary, &role, &ent, &kind, &justification, &days, &hours); err != nil {
		return err
	}
	target := access.Target{RoleID: deref(role), EntitlementID: deref(ent)}
	var el access.Eligibility
	switch kind {
	case KindEligibility:
		// Eligibility gives no access by itself: separation of duties is
		// checked when it is activated.
		if _, err := tx.Exec(ctx, `UPDATE access_requests SET status = 'approved', closed_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		var expires *time.Time
		if days != nil {
			t := time.Now().Add(time.Duration(*days) * 24 * time.Hour)
			expires = &t
		}
		if _, err := access.MakeEligible(ctx, tx, tenantID, "request:"+id, beneficiary, target, expires, id); err != nil {
			return err
		}
		if err := db.Audit(ctx, tx, tenantID, actor, "access_request.approved", "access_request", id, nil); err != nil {
			return err
		}
		return notifyDecided(ctx, tx, tenantID, id)
	case KindActivation:
		var err error
		el, err = access.FindEligibility(ctx, tx, beneficiary, target)
		if errors.Is(err, apperr.ErrNotFound) {
			return closeRequest(ctx, tx, tenantID, actor, id, Cancelled, "the person is no longer eligible")
		}
		if err != nil {
			return err
		}
		if el.Active != nil {
			return closeRequest(ctx, tx, tenantID, actor, id, Cancelled, "the access is already active")
		}
		// The maximum may have been lowered since it was asked for.
		*hours = min(*hours, el.MaxActivationHours)
	default:
		// The access may have become just-in-time only since it was asked
		// for with no end date.
		if days == nil && hours == nil {
			jit, err := access.JITOnly(ctx, tx, target)
			if err != nil {
				return err
			}
			if jit {
				return closeRequest(ctx, tx, tenantID, actor, id, Cancelled,
					"nobody holds this access for good any more: ask for it for a time, or to be made eligible")
			}
		}
	}
	// Separation of duties may have changed since the request was made: a
	// risk an admin accepted becomes exceptions, and a conflict nobody
	// accepted yet sends the request to the admins.
	conflicts, err := sod.Conflicts(ctx, tx, beneficiary, deref(role), deref(ent))
	if err != nil {
		return err
	}
	if len(conflicts) > 0 {
		var (
			by, reason string
			edays      *int
		)
		err := tx.QueryRow(ctx,
			`SELECT decided_by, coalesce(comment, ''), exception_days FROM access_request_steps
			 WHERE request_id = $1 AND kind = 'risk' AND status = 'approved' ORDER BY position DESC LIMIT 1`, id).Scan(&by, &reason, &edays)
		switch {
		case errors.Is(err, pgx.ErrNoRows) || (err == nil && edays == nil):
			return askRisk(ctx, tx, tenantID, actor, id, conflicts)
		case err != nil:
			return err
		}
		until := time.Now().Add(time.Duration(*edays) * 24 * time.Hour)
		if _, err := sod.AcceptOnRequest(ctx, tx, tenantID, by, id, beneficiary, deref(role), deref(ent), reason, until); err != nil {
			return err
		}
	}
	if err := sod.Check(ctx, tx, beneficiary, deref(role), deref(ent)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE access_requests SET status = 'approved', closed_at = now() WHERE id = $1`, id); err != nil {
		return err
	}
	if kind == KindActivation {
		if _, err := access.Activate(ctx, tx, tenantID, "request:"+id, el, *hours, justification, id); err != nil {
			return err
		}
		if err := db.Audit(ctx, tx, tenantID, actor, "access_request.approved", "access_request", id, nil); err != nil {
			return err
		}
		return notifyDecided(ctx, tx, tenantID, id)
	}
	// An earlier, expired grant from a request makes way for this one.
	if _, err := tx.Exec(ctx,
		`DELETE FROM access_assignments WHERE identity_id = $1 AND origin = 'request'
		   AND coalesce(role_id, entitlement_id) = coalesce($2::uuid, $3::uuid)`, beneficiary, role, ent); err != nil {
		return err
	}
	var expires *time.Time
	switch {
	case days != nil:
		t := time.Now().Add(time.Duration(*days) * 24 * time.Hour)
		expires = &t
	case hours != nil:
		t := time.Now().Add(time.Duration(*hours) * time.Hour)
		expires = &t
	}
	var assignment string
	if err := tx.QueryRow(ctx,
		`INSERT INTO access_assignments (tenant_id, identity_id, role_id, entitlement_id, origin, granted_by, expires_at, request_id)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id`,
		tenantID, beneficiary, role, ent, access.OriginRequest, "request:"+id, expires, id).Scan(&assignment); err != nil {
		return fmt.Errorf("assign: %w", err)
	}
	details := map[string]any{"assignment_id": assignment, "origin": access.OriginRequest, "request_id": id}
	if role != nil {
		details["role_id"] = *role
	} else {
		details["entitlement_id"] = *ent
	}
	if expires != nil {
		details["expires_at"] = expires
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access.assigned", "identity", beneficiary, details); err != nil {
		return err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access_request.approved", "access_request", id, nil); err != nil {
		return err
	}
	return notifyDecided(ctx, tx, tenantID, id)
}

// askRisk adds a risk step to a request whose access turned out to break
// separation of duties after it was made, and asks the admins.
func askRisk(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, conflicts []string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO access_request_steps (tenant_id, request_id, position, kind, status, due_at)
		 SELECT $1, $2, max(position) + 1, 'risk', 'pending', now() + make_interval(hours => `+settings.SQL(settings.RequestStepTimeoutHours)+`)
		 FROM access_request_steps WHERE request_id = $2`, tenantID, id); err != nil {
		return err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access_request.risk_review", "access_request", id,
		map[string]any{"sod_conflicts": conflicts}); err != nil {
		return err
	}
	return notifyApprovers(ctx, tx, tenantID, id, notify.ApprovalNeeded)
}

// Decision is how someone decides the step a request waits on.
type Decision struct {
	Approve bool
	Comment string
	// ExceptionDays, for risk steps, is how long the accepted risk lasts.
	ExceptionDays int
}

// Decide approves or rejects the step a request waits on. The caller has
// checked that actor may decide it. A step that needs several approvals is
// approved once it has them all; one rejection rejects the request. Approving a risk step accepts the risk:
// it needs a reason (the comment) and 1 to 365 exception days.
func Decide(ctx context.Context, tx pgx.Tx, tenantID, actor string, r Request, d Decision) (Request, error) {
	comment := strings.TrimSpace(d.Comment)
	if len(comment) > 2000 {
		return r, apperr.Invalidf("comment is longer than 2000 characters")
	}
	step := r.Current()
	if r.Status != Pending || step == nil {
		return r, apperr.Conflictf("this request is %s; there is nothing to decide", r.Status)
	}
	var days *int
	switch {
	case step.Kind != KindRisk && d.ExceptionDays != 0:
		return r, apperr.Invalidf("exception_days is only for accepting a separation of duties risk")
	case step.Kind == KindRisk && d.Approve && comment == "":
		return r, apperr.Invalidf("say why the risk is accepted and how it is mitigated: the comment is the exception's reason")
	case step.Kind == KindRisk && d.Approve:
		most, err := settings.Get(ctx, tx, settings.SoDExceptionMaxDays)
		if err != nil {
			return r, err
		}
		if d.ExceptionDays < 1 || d.ExceptionDays > most {
			return r, apperr.Invalidf("exception_days must be between 1 and %d: an accepted risk needs an end date", most)
		}
		days = &d.ExceptionDays
	}
	if step.Kind == KindRisk && actor == db.IdentityActor(r.BeneficiaryID) {
		return r, apperr.Conflictf("nobody accepts their own separation of duties risk")
	}
	// Each approval counts once; a step that needs more waits for them.
	if me, ok := strings.CutPrefix(actor, "identity:"); ok && d.Approve {
		if step.approved(me) {
			return r, apperr.Conflictf("you already approved this step; it waits for %d more", step.Required-len(step.Approvals))
		}
		if err := addApproval(ctx, tx, tenantID, r.ID, step.Position, me, comment); err != nil {
			return r, err
		}
		if n := len(step.Approvals) + 1; n < step.Required {
			if err := db.Audit(ctx, tx, tenantID, actor, "access_request.approval_added", "access_request", r.ID,
				map[string]any{"position": step.Position, "kind": step.Kind, "comment": comment, "approvals": n, "required": step.Required}); err != nil {
				return r, err
			}
			return Get(ctx, tx, r.ID, false)
		}
	}
	st := StepRejected
	if d.Approve {
		st = StepApproved
	}
	if _, err := tx.Exec(ctx,
		`UPDATE access_request_steps SET status = $3, decided_by = $4, decided_at = now(), comment = nullif($5, ''), exception_days = $6
		 WHERE request_id = $1 AND position = $2`, r.ID, step.Position, st, actor, comment, days); err != nil {
		return r, err
	}
	details := map[string]any{"position": step.Position, "kind": step.Kind, "comment": comment}
	if days != nil {
		details["exception_days"] = *days
		details["sod_conflicts"] = r.SoDConflicts
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access_request.step_"+st, "access_request", r.ID, details); err != nil {
		return r, err
	}
	approve := d.Approve
	if approve {
		if err := advance(ctx, tx, tenantID, actor, r.ID); err != nil {
			return r, err
		}
	} else if err := closeRequest(ctx, tx, tenantID, actor, r.ID, Rejected, ""); err != nil {
		return r, err
	}
	return Get(ctx, tx, r.ID, false)
}

// Cancel withdraws a pending request.
func Cancel(ctx context.Context, tx pgx.Tx, tenantID, actor string, r Request) (Request, error) {
	if r.Status != Pending {
		return r, apperr.Conflictf("this request is %s; only pending requests can be cancelled", r.Status)
	}
	if err := closeRequest(ctx, tx, tenantID, actor, r.ID, Cancelled, "withdrawn by "+actor); err != nil {
		return r, err
	}
	return Get(ctx, tx, r.ID, false)
}

func closeRequest(ctx context.Context, tx pgx.Tx, tenantID, actor, id, status, reason string) error {
	if _, err := tx.Exec(ctx,
		`UPDATE access_requests SET status = $2, reason = nullif($3, ''), closed_at = now() WHERE id = $1`, id, status, reason); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE access_request_steps SET status = 'skipped' WHERE request_id = $1 AND status IN ('pending', 'waiting')`, id); err != nil {
		return err
	}
	details := map[string]any{}
	if reason != "" {
		details["reason"] = reason
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access_request."+status, "access_request", id, details); err != nil {
		return err
	}
	return notifyDecided(ctx, tx, tenantID, id)
}

const selectRequests = `
	SELECT r.id, r.requester_id, rq.display_name, r.beneficiary_id, b.display_name,
	       CASE WHEN r.role_id IS NULL THEN 'entitlement' ELSE 'role' END,
	       coalesce(r.role_id, r.entitlement_id), coalesce(ro.name, e.name), coalesce(app.name, ''),
	       r.justification, r.kind, r.duration_days, r.duration_hours, r.status, coalesce(r.reason, ''), r.created_at, r.closed_at,
	       coalesce(r.policy_id::text, ''), coalesce(r.policy_name, ''), r.policy_version
	FROM access_requests r
	JOIN identities rq ON rq.id = r.requester_id
	JOIN identities b ON b.id = r.beneficiary_id
	LEFT JOIN roles ro ON ro.id = r.role_id
	LEFT JOIN entitlements e ON e.id = r.entitlement_id
	LEFT JOIN applications app ON app.id = e.application_id`

func scan(row pgx.CollectableRow) (Request, error) {
	var r Request
	err := row.Scan(&r.ID, &r.RequesterID, &r.RequesterName, &r.BeneficiaryID, &r.BeneficiaryName,
		&r.Type, &r.TargetID, &r.TargetName, &r.Application,
		&r.Justification, &r.Kind, &r.DurationDays, &r.DurationHours, &r.Status, &r.Reason, &r.CreatedAt, &r.ClosedAt,
		&r.PolicyID, &r.PolicyName, &r.PolicyVersion)
	r.Steps = []Step{}
	return r, err
}

// Get returns one request with its steps. lock holds it for a decision.
func Get(ctx context.Context, tx pgx.Tx, id string, lock bool) (Request, error) {
	if !db.ValidUUID(id) {
		return Request{}, apperr.ErrNotFound
	}
	if lock {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM access_requests WHERE id = $1 FOR UPDATE`, id); err != nil {
			return Request{}, err
		}
	}
	r, err := db.QueryOne(ctx, tx, scan, selectRequests+` WHERE r.id = $1`, id)
	if err != nil {
		return Request{}, err
	}
	list := []Request{r}
	if err := loadSteps(ctx, tx, list); err != nil {
		return Request{}, err
	}
	return list[0], nil
}

func loadSteps(ctx context.Context, tx pgx.Tx, list []Request) error {
	if len(list) == 0 {
		return nil
	}
	ids := make([]string, len(list))
	byID := map[string]*Request{}
	for i := range list {
		ids[i] = list[i].ID
		byID[list[i].ID] = &list[i]
	}
	// Approvers and approvals come as JSON arrays, in the order they were
	// asked and given.
	rows, err := tx.Query(ctx,
		`SELECT s.request_id, s.position, s.kind, coalesce(s.label, ''), s.required, s.status, s.due_at, s.on_timeout,
		        coalesce((SELECT array_agg(i.display_name ORDER BY i.display_name) FROM identities i WHERE i.id = ANY(s.escalated_from)), '{}'),
		        s.escalated_at, coalesce(s.decided_by, ''), s.decided_at, coalesce(s.comment, ''), s.exception_days,
		        coalesce((SELECT json_agg(json_build_object('id', u.id, 'name', coalesce(i.display_name, ''), 'active', coalesce(i.status = 'active', false))
		                                  ORDER BY u.n)
		                  FROM unnest(s.approver_ids) WITH ORDINALITY u(id, n) LEFT JOIN identities i ON i.id = u.id), '[]'),
		        coalesce((SELECT json_agg(json_build_object('identity_id', ap.identity_id, 'name', i.display_name, 'comment', coalesce(ap.comment, ''),
		                                                    'at', ap.approved_at) ORDER BY ap.approved_at)
		                  FROM access_request_approvals ap JOIN identities i ON i.id = ap.identity_id
		                  WHERE ap.request_id = s.request_id AND ap.position = s.position), '[]')
		 FROM access_request_steps s
		 WHERE s.request_id = ANY($1) ORDER BY s.request_id, s.position`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			req string
			s   Step
		)
		if err := rows.Scan(&req, &s.Position, &s.Kind, &s.Label, &s.Required, &s.Status, &s.DueAt, &s.OnTimeout,
			&s.EscalatedFrom, &s.EscalatedAt, &s.DecidedBy, &s.DecidedAt, &s.Comment, &s.ExceptionDays,
			&s.Approvers, &s.Approvals); err != nil {
			return err
		}
		byID[req].Steps = append(byID[req].Steps, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	// Name the conflicts of requests waiting on, or heading to, a risk step.
	for i := range list {
		r := &list[i]
		if r.Status != Pending || !slices.ContainsFunc(r.Steps, func(s Step) bool {
			return s.Kind == KindRisk && (s.Status == StepPending || s.Status == StepWaiting)
		}) {
			continue
		}
		var role, ent string
		if r.Type == "role" {
			role = r.TargetID
		} else {
			ent = r.TargetID
		}
		names, err := sod.Conflicts(ctx, tx, r.BeneficiaryID, role, ent)
		if err != nil {
			return err
		}
		r.SoDConflicts = names
	}
	return nil
}

// Scopes for List.
const (
	ScopeMine      = "mine"      // asked by me or for me
	ScopeApprovals = "approvals" // waiting on me
	ScopeAll       = "all"
)

// Filter selects requests.
type Filter struct {
	Scope string
	// Me is the person listing. Admin says whether they are an admin, who
	// also decide the steps that have nobody else to ask.
	Me     string
	Admin  bool
	Status string
	Limit  int
	// After is the id of the last request of the previous page.
	After string
}

// approvedBy is an SQL condition on the step s, true when who (an SQL
// expression) approved it.
func approvedBy(who string) string {
	return `EXISTS (SELECT 1 FROM access_request_approvals ap
	                WHERE ap.request_id = s.request_id AND ap.position = s.position AND ap.identity_id = ` + who + `)`
}

// approvalsGiven is how many approvals the step s has, as SQL.
const approvalsGiven = `(SELECT count(*) FROM access_request_approvals ap WHERE ap.request_id = s.request_id AND ap.position = s.position)`

// stuck is an SQL condition on the step s, true when it waits on the
// admins: the active approvers who have not approved are too few to give
// the approvals it still needs (which includes asking nobody). See
// Step.Deciders.
var stuck = `(SELECT count(*) FROM identities a WHERE a.id = ANY(s.approver_ids) AND a.status = 'active' AND NOT ` + approvedBy("a.id") + `)
	< s.required - ` + approvalsGiven

// waitingOn is an SQL condition on the request r, true when it waits on
// the decision of me (an SQL expression): an approver of its current step
// who has not approved it yet, or an admin (admin) when that step waits on
// the admins. Nobody decides on their own access.
func waitingOn(me string, admin bool) string {
	cond := me + " = ANY(s.approver_ids) AND NOT " + approvedBy(me)
	if admin {
		cond = "(" + cond + ") OR " + stuck
	}
	return `r.status = 'pending' AND r.beneficiary_id <> ` + me + ` AND EXISTS (
		SELECT 1 FROM access_request_steps s
		WHERE s.request_id = r.id AND s.status = 'pending' AND (` + cond + `))`
}

// Waiting is a request that waits on someone's decision, as an inbox lists it.
type Waiting struct {
	ID              string     `json:"id"`
	BeneficiaryName string     `json:"beneficiary_name"`
	TargetName      string     `json:"target_name"`
	Application     string     `json:"application,omitempty"`
	Kind            string     `json:"kind"`
	CreatedAt       time.Time  `json:"created_at"`
	DueAt           *time.Time `json:"due_at,omitempty"`
}

// ListWaiting returns the requests waiting on me's decision, the soonest
// deadline first: the same requests as List with ScopeApprovals.
func ListWaiting(ctx context.Context, tx pgx.Tx, me string, admin bool) ([]Waiting, error) {
	rows, err := tx.Query(ctx,
		`SELECT r.id, b.display_name, coalesce(ro.name, e.name), coalesce(app.name, ''), r.kind, r.created_at,
		        (SELECT s.due_at FROM access_request_steps s WHERE s.request_id = r.id AND s.status = 'pending' LIMIT 1) AS due
		 FROM access_requests r
		 JOIN identities b ON b.id = r.beneficiary_id
		 LEFT JOIN roles ro ON ro.id = r.role_id
		 LEFT JOIN entitlements e ON e.id = r.entitlement_id
		 LEFT JOIN applications app ON app.id = e.application_id
		 WHERE `+waitingOn("$1", admin)+`
		 ORDER BY due NULLS LAST, r.created_at, r.id`, me)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Waiting, error) {
		var w Waiting
		err := row.Scan(&w.ID, &w.BeneficiaryName, &w.TargetName, &w.Application, &w.Kind, &w.CreatedAt, &w.DueAt)
		return w, err
	})
}

// List returns requests, newest first.
func List(ctx context.Context, tx pgx.Tx, f Filter) ([]Request, error) {
	var where []string
	args := []any{}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	switch f.Scope {
	case ScopeMine:
		me := arg(f.Me)
		where = append(where, "(r.requester_id = "+me+" OR r.beneficiary_id = "+me+")")
	case ScopeApprovals:
		where = append(where, waitingOn(arg(f.Me), f.Admin))
	case ScopeAll:
	default:
		return nil, apperr.Invalidf("scope must be mine, approvals or all")
	}
	if f.Status != "" {
		switch f.Status {
		case Pending, Approved, Rejected, Cancelled, Expired:
		default:
			return nil, apperr.Invalidf("unknown status %q", f.Status)
		}
		where = append(where, "r.status = "+arg(f.Status))
	}
	if f.After != "" {
		if !db.ValidUUID(f.After) {
			return nil, apperr.Invalidf("after must be a request id")
		}
		a := arg(f.After)
		where = append(where, "(r.created_at, r.id) < (SELECT created_at, id FROM access_requests WHERE id = "+a+")")
	}
	q := selectRequests
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY r.created_at DESC, r.id DESC LIMIT " + arg(f.Limit)
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, scan)
	if err != nil {
		return nil, err
	}
	return list, loadSteps(ctx, tx, list)
}

// Requestable is something people can ask for.
type Requestable struct {
	// Type is "role" or "entitlement".
	Type        string `json:"type"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Application string `json:"application,omitempty"`
	Risk        string `json:"risk,omitempty"`
	// JITOnly says the access is only held for a time: ask for it with a
	// duration, or to be made eligible.
	JITOnly bool `json:"jit_only"`
	// TeamHolders is how many people on the asker's team hold it now: the
	// active people who share their manager. See TeamHolders.
	TeamHolders int `json:"team_holders,omitempty"`
}

// ListRequestable returns the roles and entitlements people can ask for:
// all of them but the platform roles, which have their own guarded flow.
func ListRequestable(ctx context.Context, tx pgx.Tx) ([]Requestable, error) {
	rows, err := tx.Query(ctx,
		`SELECT 'role', id, name, coalesce(description, ''), '', '', access_is_jit_only(id, NULL) FROM roles
		 UNION ALL
		 SELECT 'entitlement', e.id, e.name, coalesce(e.description, ''), a.name, e.risk, e.jit_only
		 FROM entitlements e JOIN applications a ON a.id = e.application_id
		 WHERE `+catalog.RequestableSQL("a", "e")+`
		 ORDER BY 1 DESC, 5, 3`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Requestable, error) {
		var r Requestable
		err := row.Scan(&r.Type, &r.ID, &r.Name, &r.Description, &r.Application, &r.Risk, &r.JITOnly)
		return r, err
	})
}

// TeamHolders tells what the team of identity holds, to help people pick
// from a catalog of names they may not know: how many active people share
// identity's manager (identity aside), and how many of them hold each role
// and entitlement now, keyed by Requestable.Type + ":" + ID. An identity
// with no manager has no team, and a team smaller than the tenant's
// settings.RequestTeamMinPeople gets its size only: in a team of one or two,
// a count tells what a given colleague holds.
func TeamHolders(ctx context.Context, tx pgx.Tx, identity string) (size int, held map[string]int, err error) {
	held = map[string]int{}
	err = tx.QueryRow(ctx,
		`SELECT count(*) FROM identities t JOIN identities me ON t.manager_id = me.manager_id
		 WHERE me.id = $1 AND t.id <> me.id AND t.status = 'active' AND t.kind = 'human'`, identity).Scan(&size)
	if err != nil {
		return size, held, err
	}
	smallest, err := settings.Get(ctx, tx, settings.RequestTeamMinPeople)
	if err != nil || size < smallest {
		return size, held, err
	}
	rows, err := tx.Query(ctx,
		`WITH team AS (
		   SELECT t.id FROM identities t JOIN identities me ON t.manager_id = me.manager_id
		   WHERE me.id = $1 AND t.id <> me.id AND t.status = 'active' AND t.kind = 'human'),
		 current AS (
		   SELECT x.* FROM access_assignments x JOIN team ON team.id = x.identity_id
		   WHERE x.expires_at IS NULL OR x.expires_at > now()),
		 holds AS (
		   SELECT identity_id, 'role' AS type, role_id AS id FROM current WHERE role_id IS NOT NULL
		   UNION
		   SELECT identity_id, 'entitlement', entitlement_id FROM current WHERE entitlement_id IS NOT NULL
		   UNION
		   SELECT c.identity_id, 'entitlement', re.entitlement_id FROM current c JOIN role_entitlements re ON re.role_id = c.role_id)
		 SELECT type || ':' || id, count(*) FROM holds GROUP BY type, id`, identity)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var n int
		if err := rows.Scan(&key, &n); err != nil {
			return 0, nil, err
		}
		held[key] = n
	}
	return size, held, rows.Err()
}

// Housekeeping counts what ExpireDue did.
type Housekeeping struct {
	Expired     int `json:"expired"`
	Rejected    int `json:"rejected"`
	Escalated   int `json:"escalated"`
	Cancelled   int `json:"cancelled"`
	AccessEnded int `json:"access_ended"`
}

// ExpireDue closes requests for people who are no longer active, does what
// the step says with steps nobody decided in time (expire the request,
// reject it, or escalate the step), and removes access whose time is up.
func ExpireDue(ctx context.Context, pool *pgxpool.Pool, tenantID string) (Housekeeping, error) {
	var h Housekeeping
	const actor = "system:requests"
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		type due struct {
			id, onTimeout string
			gone          bool
			position      *int
			admins        bool
		}
		rows, err := tx.Query(ctx,
			`SELECT r.id, b.status <> 'active', s.position, coalesce(s.on_timeout, ''), coalesce(cardinality(s.approver_ids) = 0, false)
			 FROM access_requests r JOIN identities b ON b.id = r.beneficiary_id
			 LEFT JOIN access_request_steps s ON s.request_id = r.id AND s.status = 'pending' AND s.due_at <= now()
			 WHERE r.status = 'pending' AND (b.status <> 'active' OR s.request_id IS NOT NULL)
			 FOR UPDATE OF r SKIP LOCKED`)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			err := row.Scan(&d.id, &d.gone, &d.position, &d.onTimeout, &d.admins)
			return d, err
		})
		if err != nil {
			return err
		}
		for _, d := range list {
			switch {
			case d.gone:
				h.Cancelled++
				err = closeRequest(ctx, tx, tenantID, actor, d.id, Cancelled, "the person is no longer active")
			case d.onTimeout == approvals.Reject:
				h.Rejected++
				err = closeRequest(ctx, tx, tenantID, actor, d.id, Rejected, "nobody decided in time, so the approval flow rejects it")
			// A step the admins decide has nobody to escalate to.
			case d.onTimeout == approvals.Escalate && !d.admins:
				h.Escalated++
				err = escalate(ctx, tx, tenantID, actor, d.id, *d.position)
			default:
				h.Expired++
				err = closeRequest(ctx, tx, tenantID, actor, d.id, Expired, "nobody decided in time")
			}
			if err != nil {
				return err
			}
		}

		// Activations end every 30 seconds, with a quick provisioning pass (see
		// access.EndActivations).
		rows, err = tx.Query(ctx,
			`DELETE FROM access_assignments WHERE expires_at <= now() AND origin <> 'activation'
			 RETURNING id, identity_id, coalesce(role_id::text, ''), coalesce(entitlement_id::text, ''), origin, coalesce(request_id::text, '')`)
		if err != nil {
			return err
		}
		type ended struct{ id, identity, role, ent, origin, request string }
		gone, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ended, error) {
			var e ended
			err := row.Scan(&e.id, &e.identity, &e.role, &e.ent, &e.origin, &e.request)
			return e, err
		})
		if err != nil {
			return err
		}
		for _, e := range gone {
			details := map[string]any{"assignment_id": e.id, "origin": e.origin, "reason": "expired"}
			if e.role != "" {
				details["role_id"] = e.role
			} else {
				details["entitlement_id"] = e.ent
			}
			if e.request != "" {
				details["request_id"] = e.request
			}
			if err := db.Audit(ctx, tx, tenantID, actor, "access.removed", "identity", e.identity, details); err != nil {
				return err
			}
		}
		h.AccessEnded = len(gone)
		return nil
	})
	return h, err
}

// escalate asks the managers of the approvers of a step nobody decided in
// time instead, or the admins when they have none, with the step's time
// again. A step is escalated once: when it times out again it expires.
func escalate(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, position int) error {
	var managers []string
	var done, required int
	if err := tx.QueryRow(ctx,
		`SELECT coalesce((SELECT array_agg(DISTINCT m.id::text) FROM identities a JOIN identities m ON m.id = a.manager_id
		                  WHERE a.id = ANY(s.approver_ids) AND m.status = 'active' AND m.kind = 'human'
		                    AND m.id <> r.beneficiary_id AND NOT m.id = ANY(s.approver_ids)), '{}'),
		        `+approvalsGiven+`,
		        s.required
		 FROM access_request_steps s JOIN access_requests r ON r.id = s.request_id
		 WHERE s.request_id = $1 AND s.position = $2`, id, position).Scan(&managers, &done, &required); err != nil {
		return err
	}
	// The approvals already given still count; the managers give the rest,
	// as many as there are of them.
	required = done + min(required-done, max(len(managers), 1))
	if _, err := tx.Exec(ctx,
		`UPDATE access_request_steps SET escalated_from = approver_ids, approver_ids = $3, required = $4, escalated_at = now(),
		        on_timeout = 'expire', reminded_at = NULL, due_at = `+dueAt+`
		 WHERE request_id = $1 AND position = $2`, id, position, managers, required); err != nil {
		return err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "access_request.step_escalated", "access_request", id,
		map[string]any{"position": position, "approver_ids": managers}); err != nil {
		return err
	}
	return notifyApprovers(ctx, tx, tenantID, id, notify.ApprovalNeeded)
}

// ActivationResult is what activating an eligibility did: gave the access at
// once, or opened a request for approval.
type ActivationResult struct {
	Activation *access.Activation `json:"activation,omitempty"`
	Request    *Request           `json:"request,omitempty"`
}

// Activate turns an eligibility of identityID into access for hours. It is
// immediate unless the access needs an approval to activate, or would break
// a prevent-mode separation of duties policy: then it becomes an activation
// request that approvers decide like any other.
func Activate(ctx context.Context, tx pgx.Tx, tenantID, identityID, eligibilityID string, hours int, justification string) (ActivationResult, error) {
	el, err := access.GetEligibility(ctx, tx, eligibilityID, true)
	if err != nil {
		return ActivationResult{}, err
	}
	if el.IdentityID != identityID {
		return ActivationResult{}, apperr.ErrNotFound
	}
	if justification, err = access.CheckActivation(el, hours, justification); err != nil {
		return ActivationResult{}, err
	}
	in := Input{BeneficiaryID: identityID, Kind: KindActivation, Justification: justification, DurationHours: &hours}
	if el.Type == "role" {
		in.RoleID = el.TargetID
	} else {
		in.EntitlementID = el.TargetID
	}
	conflicts, err := sod.Conflicts(ctx, tx, identityID, in.RoleID, in.EntitlementID)
	if err != nil {
		return ActivationResult{}, err
	}
	if el.ActivationApproval || len(conflicts) > 0 {
		r, err := create(ctx, tx, tenantID, identityID, in)
		if err != nil {
			return ActivationResult{}, err
		}
		if r.Status == Approved {
			// Nobody else had to approve (an approver activating): the
			// request granted it at once.
			el, err = access.GetEligibility(ctx, tx, eligibilityID, false)
			if err != nil {
				return ActivationResult{}, err
			}
			return ActivationResult{Activation: el.Active, Request: &r}, nil
		}
		return ActivationResult{Request: &r}, nil
	}
	var held bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM access_assignments
		                WHERE identity_id = $1 AND coalesce(role_id, entitlement_id) = $2::uuid
		                  AND (expires_at IS NULL OR expires_at > now()))`,
		identityID, el.TargetID).Scan(&held); err != nil {
		return ActivationResult{}, err
	}
	if held {
		return ActivationResult{}, apperr.Conflictf("you already have that access")
	}
	a, err := access.Activate(ctx, tx, tenantID, db.IdentityActor(identityID), el, hours, justification, "")
	if err != nil {
		return ActivationResult{}, err
	}
	return ActivationResult{Activation: &a}, nil
}
