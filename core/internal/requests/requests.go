// Package requests lets people ask for access. A request names a business
// role or an entitlement, who it is for (the requester or one of their
// reports) and why. Approvers decide in order: the person's manager, then the
// owner of the role or of the entitlement's application. Once every step is
// approved the request becomes an assignment with origin "request", which
// provisioning carries to the application.
//
// Steps with nobody to ask (no manager, no owner, or an approver who would be
// approving their own access) go to the admins. A step the requester would
// approve themselves, such as a manager asking for a report, counts as
// approved: asking is the approval. Steps not decided within StepTimeout
// expire the request.
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
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
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

// Step statuses.
const (
	StepWaiting  = "waiting"
	StepPending  = "pending"
	StepApproved = "approved"
	StepRejected = "rejected"
	StepSkipped  = "skipped"
)

// Step kinds.
const (
	KindManager = "manager"
	KindOwner   = "owner"
	KindAdmin   = "admin"
	// KindRisk is decided by admins: approving it accepts a separation of
	// duties risk.
	KindRisk = "risk"
)

// StepTimeout is how long an approver has to decide before the request
// expires.
const StepTimeout = 7 * 24 * time.Hour

// Step is one approval a request needs.
type Step struct {
	Position int    `json:"position"`
	Kind     string `json:"kind"`
	// ApproverID is empty when any admin decides.
	ApproverID   string     `json:"approver_id,omitempty"`
	ApproverName string     `json:"approver_name,omitempty"`
	Status       string     `json:"status"`
	DueAt        *time.Time `json:"due_at,omitempty"`
	DecidedBy    string     `json:"decided_by,omitempty"`
	DecidedAt    *time.Time `json:"decided_at,omitempty"`
	Comment      string     `json:"comment,omitempty"`
	// ExceptionDays is how long an accepted risk lasts (risk steps).
	ExceptionDays *int `json:"exception_days,omitempty"`

	approverActive bool
}

// Request is an access request with its approval steps.
type Request struct {
	ID              string `json:"id"`
	RequesterID     string `json:"requester_id"`
	RequesterName   string `json:"requester_name"`
	BeneficiaryID   string `json:"beneficiary_id"`
	BeneficiaryName string `json:"beneficiary_name"`
	// Type is "role" or "entitlement".
	Type          string     `json:"type"`
	TargetID      string     `json:"target_id"`
	TargetName    string     `json:"target_name"`
	Application   string     `json:"application,omitempty"`
	Justification string     `json:"justification"`
	DurationDays  *int       `json:"duration_days,omitempty"`
	Status        string     `json:"status"`
	Reason        string     `json:"reason,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
	Steps         []Step     `json:"steps"`
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
		if s.ApproverID != "" {
			approvers = append(approvers, s.ApproverID)
		}
	}
	if c := r.Current(); c != nil && c.ApproverID != "" && c.approverActive {
		current = []string{c.ApproverID}
	}
	return authz.AccessRequestResource(r.ID, r.RequesterID, r.BeneficiaryID, approvers, current)
}

// Input is what someone asks for. Exactly one of RoleID and EntitlementID is
// set; BeneficiaryID defaults to the requester.
type Input struct {
	BeneficiaryID string `json:"beneficiary_id,omitempty"`
	RoleID        string `json:"role_id,omitempty"`
	EntitlementID string `json:"entitlement_id,omitempty"`
	Justification string `json:"justification"`
	DurationDays  *int   `json:"duration_days,omitempty"`
}

func (in *Input) check() error {
	if (in.RoleID == "") == (in.EntitlementID == "") {
		return apperr.Invalidf("ask for exactly one of role_id and entitlement_id")
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

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Create records a request from requesterID and works out its approvals. The
// caller has checked that the requester may ask for the beneficiary. A
// request whose every step is approved at once (an admin asking, or a manager
// who also owns the application) is granted right away.
func Create(ctx context.Context, tx pgx.Tx, tenantID, requesterID string, in Input) (Request, error) {
	if in.BeneficiaryID == "" {
		in.BeneficiaryID = requesterID
	}
	if err := in.check(); err != nil {
		return Request{}, err
	}
	var owner, manager, status string
	var err error
	if in.RoleID != "" {
		if err = catalog.CheckRoles(ctx, tx, []string{in.RoleID}); err != nil {
			return Request{}, err
		}
		err = tx.QueryRow(ctx, `SELECT coalesce(owner_id::text, '') FROM roles WHERE id = $1`, in.RoleID).Scan(&owner)
	} else {
		if err = catalog.CheckEntitlements(ctx, tx, []string{in.EntitlementID}); err != nil {
			return Request{}, err
		}
		err = tx.QueryRow(ctx,
			`SELECT coalesce(a.owner_id::text, '') FROM entitlements e JOIN applications a ON a.id = e.application_id WHERE e.id = $1`,
			in.EntitlementID).Scan(&owner)
	}
	if err != nil {
		return Request{}, err
	}
	err = tx.QueryRow(ctx, `SELECT status, coalesce(manager_id::text, '') FROM identities WHERE id = $1`, in.BeneficiaryID).Scan(&status, &manager)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, apperr.ErrNotFound
	}
	if err != nil {
		return Request{}, err
	}
	if status != "active" {
		return Request{}, apperr.Conflictf("access can only be requested for active people")
	}
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
	conflicts, err := sod.Conflicts(ctx, tx, in.BeneficiaryID, in.RoleID, in.EntitlementID)
	if err != nil {
		return Request{}, err
	}

	var id string
	err = tx.QueryRow(ctx,
		`INSERT INTO access_requests (tenant_id, requester_id, beneficiary_id, role_id, entitlement_id, justification, duration_days)
		 VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		tenantID, requesterID, in.BeneficiaryID, nullable(in.RoleID), nullable(in.EntitlementID), in.Justification, in.DurationDays).Scan(&id)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Request{}, apperr.Conflictf("there is already an open request for this access")
	}
	if err != nil {
		return Request{}, err
	}

	// The chain: manager, then owner. Nobody approves their own access, and
	// the same person is asked once.
	type plan struct{ kind, approver string }
	var steps []plan
	for _, s := range []plan{{KindManager, manager}, {KindOwner, owner}} {
		if s.approver == in.BeneficiaryID {
			s.approver = ""
		}
		if s.approver == "" {
			s.kind = KindAdmin
		}
		if len(steps) > 0 && steps[len(steps)-1].approver == s.approver {
			continue
		}
		steps = append(steps, s)
	}
	if len(conflicts) > 0 {
		steps = append(steps, plan{KindRisk, ""})
	}
	for n, s := range steps {
		st, decidedBy, comment := StepWaiting, "", ""
		if s.approver != "" && s.approver == requesterID {
			st, decidedBy, comment = StepApproved, "identity:"+requesterID, "asked for it"
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO access_request_steps (tenant_id, request_id, position, kind, approver_id, status, decided_by, decided_at, comment)
			 VALUES ($1, $2, $3, $4, $5, $6, nullif($7, ''), CASE WHEN $7 = '' THEN NULL ELSE now() END, nullif($8, ''))`,
			tenantID, id, n+1, s.kind, nullable(s.approver), st, decidedBy, comment); err != nil {
			return Request{}, err
		}
	}
	actor := "identity:" + requesterID
	if err := db.Audit(ctx, tx, tenantID, actor, "access_request.created", "access_request", id,
		map[string]any{"beneficiary_id": in.BeneficiaryID, "role_id": in.RoleID, "entitlement_id": in.EntitlementID,
			"duration_days": in.DurationDays, "sod_conflicts": conflicts}); err != nil {
		return Request{}, err
	}
	if err := advance(ctx, tx, tenantID, actor, id); err != nil {
		return Request{}, err
	}
	return Get(ctx, tx, id, false)
}

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
		`UPDATE access_request_steps SET status = 'pending', due_at = now() + make_interval(secs => $2)
		 WHERE request_id = $1 AND position = (
		   SELECT min(position) FROM access_request_steps WHERE request_id = $1 AND status = 'waiting')
		   AND NOT EXISTS (SELECT 1 FROM access_request_steps WHERE request_id = $1 AND status = 'pending')`,
		id, StepTimeout.Seconds())
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
		beneficiary string
		role, ent   *string
		days        *int
	)
	if err := tx.QueryRow(ctx,
		`SELECT beneficiary_id, role_id, entitlement_id, duration_days FROM access_requests WHERE id = $1`, id).
		Scan(&beneficiary, &role, &ent, &days); err != nil {
		return err
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
	// An earlier, expired grant from a request makes way for this one.
	if _, err := tx.Exec(ctx,
		`DELETE FROM access_assignments WHERE identity_id = $1 AND origin = 'request'
		   AND coalesce(role_id, entitlement_id) = coalesce($2::uuid, $3::uuid)`, beneficiary, role, ent); err != nil {
		return err
	}
	var expires *time.Time
	if days != nil {
		t := time.Now().Add(time.Duration(*days) * 24 * time.Hour)
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
		 SELECT $1, $2, max(position) + 1, 'risk', 'pending', now() + make_interval(secs => $3)
		 FROM access_request_steps WHERE request_id = $2`, tenantID, id, StepTimeout.Seconds()); err != nil {
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
// checked that actor may decide it. Approving a risk step accepts the risk:
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
	case step.Kind == KindRisk && d.Approve && (d.ExceptionDays < 1 || d.ExceptionDays > 365):
		return r, apperr.Invalidf("exception_days must be between 1 and 365: an accepted risk needs an end date")
	case step.Kind == KindRisk && d.Approve:
		days = &d.ExceptionDays
	}
	if step.Kind == KindRisk && actor == "identity:"+r.BeneficiaryID {
		return r, apperr.Conflictf("nobody accepts their own separation of duties risk")
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
	       r.justification, r.duration_days, r.status, coalesce(r.reason, ''), r.created_at, r.closed_at
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
		&r.Justification, &r.DurationDays, &r.Status, &r.Reason, &r.CreatedAt, &r.ClosedAt)
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
	rows, err := tx.Query(ctx, selectRequests+` WHERE r.id = $1`, id)
	if err != nil {
		return Request{}, err
	}
	r, err := pgx.CollectExactlyOneRow(rows, scan)
	if errors.Is(err, pgx.ErrNoRows) {
		return Request{}, apperr.ErrNotFound
	}
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
	rows, err := tx.Query(ctx,
		`SELECT s.request_id, s.position, s.kind, coalesce(s.approver_id::text, ''), coalesce(i.display_name, ''),
		        coalesce(i.status = 'active', false), s.status, s.due_at, coalesce(s.decided_by, ''), s.decided_at, coalesce(s.comment, ''),
		        s.exception_days
		 FROM access_request_steps s LEFT JOIN identities i ON i.id = s.approver_id
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
		if err := rows.Scan(&req, &s.Position, &s.Kind, &s.ApproverID, &s.ApproverName, &s.approverActive,
			&s.Status, &s.DueAt, &s.DecidedBy, &s.DecidedAt, &s.Comment, &s.ExceptionDays); err != nil {
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
		me := arg(f.Me)
		cond := "s.approver_id = " + me + " AND a.status = 'active'"
		if f.Admin {
			cond = "(" + cond + ") OR s.approver_id IS NULL OR a.status <> 'active'"
		}
		where = append(where, `r.status = 'pending' AND r.beneficiary_id <> `+me+` AND EXISTS (
			SELECT 1 FROM access_request_steps s LEFT JOIN identities a ON a.id = s.approver_id
			WHERE s.request_id = r.id AND s.status = 'pending' AND (`+cond+`))`)
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
}

// ListRequestable returns the roles and entitlements people can ask for:
// all of them but the platform roles, which have their own guarded flow.
func ListRequestable(ctx context.Context, tx pgx.Tx) ([]Requestable, error) {
	rows, err := tx.Query(ctx,
		`SELECT 'role', id, name, coalesce(description, ''), '', '' FROM roles
		 UNION ALL
		 SELECT 'entitlement', e.id, e.name, coalesce(e.description, ''), a.name, e.risk
		 FROM entitlements e JOIN applications a ON a.id = e.application_id
		 WHERE a.connector <> $1 AND e.missing_since IS NULL
		 ORDER BY 1 DESC, 5, 3`, authz.BuiltinConnector)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Requestable, error) {
		var r Requestable
		err := row.Scan(&r.Type, &r.ID, &r.Name, &r.Description, &r.Application, &r.Risk)
		return r, err
	})
}

// Housekeeping counts what ExpireDue did.
type Housekeeping struct {
	Expired     int `json:"expired"`
	Cancelled   int `json:"cancelled"`
	AccessEnded int `json:"access_ended"`
}

// ExpireDue closes requests nobody decided in time and requests for people
// who are no longer active, and removes access whose time is up.
func ExpireDue(ctx context.Context, pool *pgxpool.Pool, tenantID string) (Housekeeping, error) {
	var h Housekeeping
	const actor = "system:requests"
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		type due struct{ id, status, reason string }
		rows, err := tx.Query(ctx,
			`SELECT r.id, CASE WHEN b.status <> 'active' THEN 'cancelled' ELSE 'expired' END,
			        CASE WHEN b.status <> 'active' THEN 'the person is no longer active'
			             ELSE 'nobody decided in time' END
			 FROM access_requests r JOIN identities b ON b.id = r.beneficiary_id
			 WHERE r.status = 'pending' AND (b.status <> 'active' OR EXISTS (
			   SELECT 1 FROM access_request_steps s WHERE s.request_id = r.id AND s.status = 'pending' AND s.due_at <= now()))
			 FOR UPDATE OF r SKIP LOCKED`)
		if err != nil {
			return err
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (due, error) {
			var d due
			err := row.Scan(&d.id, &d.status, &d.reason)
			return d, err
		})
		if err != nil {
			return err
		}
		for _, d := range list {
			if err := closeRequest(ctx, tx, tenantID, actor, d.id, d.status, d.reason); err != nil {
				return err
			}
			if d.status == Expired {
				h.Expired++
			} else {
				h.Cancelled++
			}
		}

		rows, err = tx.Query(ctx,
			`DELETE FROM access_assignments WHERE expires_at <= now()
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
