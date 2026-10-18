package console

import (
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/requests"
	"github.com/grantra-dev/grantra/core/internal/reviews"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// breakGlassTask is a break-glass use waiting for an admin to review it.
type breakGlassTask struct {
	ID           string    `json:"id"`
	Account      string    `json:"account"`
	Person       string    `json:"person"`
	Reason       string    `json:"reason"`
	CheckedOutAt time.Time `json:"checked_out_at"`
}

type inbox struct {
	// Approvals are requests waiting on your decision, the same ones the
	// Approvals page lists.
	Approvals []requests.Waiting `json:"approvals"`
	// Reviews are the campaigns where you still have access to review.
	Reviews []reviews.ToReview `json:"reviews"`
	// BreakGlass are break-glass uses for an admin to review; never your own.
	BreakGlass []breakGlassTask `json:"break_glass"`
	// Total is how many things wait on you: each approval, each access to
	// review and each break-glass use.
	Total int `json:"total"`
}

// getInbox lists everything that waits on the signed-in person, each with
// enough to link to the exact task. It only reads, and only what the person
// may act on.
func (a *API) getInbox(w http.ResponseWriter, r *http.Request) {
	var out inbox
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		ctx := r.Context()
		admin := slices.Contains(me.Roles, authz.RoleAdmin)
		if out.Approvals, err = requests.ListWaiting(ctx, tx, me.ID, admin); err != nil {
			return err
		}
		if out.Reviews, err = reviews.ListToReview(ctx, tx, me.ID, admin); err != nil {
			return err
		}
		out.Total = len(out.Approvals)
		for _, rv := range out.Reviews {
			out.Total += rv.Pending
		}
		if !admin {
			return nil
		}
		if on, err := moduleOn(ctx, tx, ModulePAM); err != nil || !on {
			return err
		}
		uses, err := vault.BreakGlassUses(ctx, tx, true)
		if err != nil {
			return err
		}
		for _, u := range uses {
			// Nobody reviews their own use, so it is not their task.
			if u.IdentityID == me.ID {
				continue
			}
			out.BreakGlass = append(out.BreakGlass, breakGlassTask{
				ID: u.ID, Account: u.AccountName, Person: u.IdentityName, Reason: u.Reason, CheckedOutAt: u.CheckedOutAt})
		}
		out.Total += len(out.BreakGlass)
		return nil
	})
	out.Approvals, out.Reviews, out.BreakGlass = nonNil(out.Approvals), nonNil(out.Reviews), nonNil(out.BreakGlass)
	a.respond(w, r, http.StatusOK, out, err)
}
