// Package notify sends email about the things people need to act on or
// know about: approvals to give, decisions on their requests, reviews to do
// and separation of duties violations.
//
// Flows queue a notification in their own transaction with Enqueue; a
// background Sender delivers the queue over SMTP. A failed send is retried
// later and never blocks or undoes the flow that caused it.
package notify

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
)

// Kind is a kind of notification. People can turn each kind off.
type Kind string

const (
	ApprovalNeeded  Kind = "approval_needed"
	RequestDecided  Kind = "request_decided"
	RequestExpiring Kind = "request_expiring"
	ReviewAssigned  Kind = "review_assigned"
	CampaignDueSoon Kind = "campaign_due_soon"
	SoDViolation    Kind = "sod_violation"
)

// KindInfo describes a kind of notification to the people who get it.
type KindInfo struct {
	Kind        Kind   `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

// Kinds lists every kind of notification, in the order the console shows them.
var Kinds = []KindInfo{
	{ApprovalNeeded, "Approvals waiting for you", "Someone asked for access and you are the one to approve it."},
	{RequestExpiring, "Approval reminders", "A request you have not decided expires within a day."},
	{RequestDecided, "Decisions on requests", "A request you made, or one for you, was approved, rejected or expired."},
	{ReviewAssigned, "Access reviews to do", "A review campaign started and asks you to keep or revoke some access."},
	{CampaignDueSoon, "Review reminders", "A campaign ends within two days and you still have access to review."},
	{SoDViolation, "Separation of duties violations", "For admins: a check found people holding access that should not go together."},
}

// Known reports whether k is a kind of notification.
func Known(k Kind) bool {
	return slices.ContainsFunc(Kinds, func(i KindInfo) bool { return i.Kind == k })
}

// Enqueue queues one notification of kind for each identity in to, in the
// caller's transaction, with the data its template needs. Identities that
// are not active, have no email or turned this kind off get nothing, and
// each identity gets one notification however often it is listed.
func Enqueue(ctx context.Context, tx pgx.Tx, tenantID string, kind Kind, to []string, data map[string]any) error {
	if !Known(kind) {
		return apperr.Invalidf("unknown notification kind %q", kind)
	}
	to = slices.Compact(slices.Sorted(slices.Values(to)))
	to = slices.DeleteFunc(to, func(id string) bool { return id == "" })
	if len(to) == 0 {
		return nil
	}
	if data == nil {
		data = map[string]any{}
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO notification_outbox (tenant_id, identity_id, kind, data)
		 SELECT $1, i.id, $2, $3 FROM identities i
		 WHERE i.id = ANY($4::uuid[]) AND i.status = 'active' AND coalesce(i.email, '') <> ''
		   AND NOT EXISTS (SELECT 1 FROM notification_opt_outs o WHERE o.identity_id = i.id AND o.kind = $2)`,
		tenantID, kind, data, to)
	return err
}

// Admins returns the identities that hold the admin platform role now.
func Admins(ctx context.Context, tx pgx.Tx) ([]string, error) {
	members, err := authz.Members(ctx, tx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range members {
		if m.Role == authz.RoleAdmin && m.Status == "active" {
			ids = append(ids, m.IdentityID)
		}
	}
	return ids, nil
}

// Setting is whether one person gets one kind of notification.
type Setting struct {
	KindInfo
	Enabled bool `json:"enabled"`
}

// Settings returns which kinds of notification identityID gets.
func Settings(ctx context.Context, tx pgx.Tx, identityID string) ([]Setting, error) {
	rows, err := tx.Query(ctx, `SELECT kind FROM notification_opt_outs WHERE identity_id = $1`, identityID)
	if err != nil {
		return nil, err
	}
	off, err := pgx.CollectRows(rows, pgx.RowTo[Kind])
	if err != nil {
		return nil, err
	}
	out := make([]Setting, len(Kinds))
	for i, k := range Kinds {
		out[i] = Setting{KindInfo: k, Enabled: !slices.Contains(off, k.Kind)}
	}
	return out, nil
}

// SetEnabled turns one kind of notification on or off for identityID.
func SetEnabled(ctx context.Context, tx pgx.Tx, tenantID, identityID string, kind Kind, enabled bool) error {
	if !Known(kind) {
		return apperr.ErrNotFound
	}
	var err error
	if enabled {
		_, err = tx.Exec(ctx, `DELETE FROM notification_opt_outs WHERE identity_id = $1 AND kind = $2`, identityID, kind)
	} else {
		_, err = tx.Exec(ctx,
			`INSERT INTO notification_opt_outs (tenant_id, identity_id, kind) VALUES ($1, $2, $3)
			 ON CONFLICT DO NOTHING`, tenantID, identityID, kind)
	}
	return err
}
