// Package notify sends email about the things people need to act on or
// know about: approvals to give, decisions on their requests, reviews to do,
// separation of duties violations and identity threats.
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
	BreakGlassUsed  Kind = "break_glass_used"
	BreakGlassDue   Kind = "break_glass_review"
	// TokenCreatorLeft is about API tokens whose creator left the company.
	TokenCreatorLeft Kind = "service_token_creator_left"
	// ThreatDetected is about high and critical identity threats.
	ThreatDetected Kind = "threat_detected"
	// OwnerLeft is about non-human identities whose owner left.
	OwnerLeft Kind = "non_human_owner_left"
	// SettingChange is a setting change waiting for a second admin.
	SettingChange Kind = "setting_change_proposed"
)

// KindInfo describes a kind of notification to the people who get it.
type KindInfo struct {
	Kind        Kind   `json:"kind"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// Required kinds cannot be turned off.
	Required bool `json:"required,omitempty"`
}

// Kinds lists every kind of notification, in the order the console shows them.
var Kinds = []KindInfo{
	{ApprovalNeeded, "Approvals waiting for you", "Someone asked for access and you are the one to approve it.", false},
	{RequestExpiring, "Approval reminders", "A request you have not decided expires soon.", false},
	{RequestDecided, "Decisions on requests", "A request you made, or one for you, was approved, rejected or expired.", false},
	{ReviewAssigned, "Access reviews to do", "A review campaign started and asks you to keep or revoke some access.", false},
	{CampaignDueSoon, "Review reminders", "A campaign ends soon and you still have access to review.", false},
	{SoDViolation, "Separation of duties violations", "For admins: a check found people holding access that should not go together.", false},
	{BreakGlassUsed, "Break-glass use", "For admins and auditors: someone opened a break-glass vault account in an emergency.", true},
	{BreakGlassDue, "Break-glass reviews", "For admins: a break-glass use still waits for someone to confirm it was justified.", false},
	{TokenCreatorLeft, "API tokens of people who left", "For admins: someone who left the company had created API tokens that still work.", false},
	{ThreatDetected, "Identity threats", "For admins: Grantra found a high or critical threat, such as a password spray or a leaver signing in.", false},
	{OwnerLeft, "Service accounts and agents whose owner left", "For admins, and for whoever takes them over: someone who left owned service accounts, AI agents or workloads, which passed to their manager.", false},
	{SettingChange, "Setting changes to approve", "For admins: another admin made a setting less strict, such as a longer break-glass check-out, and it waits for a second admin.", false},
}

// required reports whether nobody may turn k off.
func required(k Kind) bool {
	return slices.ContainsFunc(Kinds, func(i KindInfo) bool { return i.Kind == k && i.Required })
}

// Known reports whether k is a kind of notification.
func Known(k Kind) bool {
	return slices.ContainsFunc(Kinds, func(i KindInfo) bool { return i.Kind == k })
}

// Enqueue queues one notification of kind for each identity in to, in the
// caller's transaction, with the data its template needs. Identities that
// are not active, have no email or turned this kind off (unless it is
// required) get nothing, and each identity gets one notification however
// often it is listed.
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
		   AND ($5 OR NOT EXISTS (SELECT 1 FROM notification_opt_outs o WHERE o.identity_id = i.id AND o.kind = $2))`,
		tenantID, kind, data, to, required(kind))
	return err
}

// Admins returns the identities that hold the admin platform role now.
func Admins(ctx context.Context, tx pgx.Tx) ([]string, error) {
	return Members(ctx, tx, authz.RoleAdmin)
}

// Members returns the active identities that hold any of the platform roles
// now.
func Members(ctx context.Context, tx pgx.Tx, roles ...string) ([]string, error) {
	members, err := authz.Members(ctx, tx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, m := range members {
		if slices.Contains(roles, m.Role) && m.Status == "active" && !slices.Contains(ids, m.IdentityID) {
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
		out[i] = Setting{KindInfo: k, Enabled: k.Required || !slices.Contains(off, k.Kind)}
	}
	return out, nil
}

// SetEnabled turns one kind of notification on or off for identityID.
func SetEnabled(ctx context.Context, tx pgx.Tx, tenantID, identityID string, kind Kind, enabled bool) error {
	if !Known(kind) {
		return apperr.ErrNotFound
	}
	if required(kind) && !enabled {
		return apperr.Invalidf("this notification cannot be turned off")
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
