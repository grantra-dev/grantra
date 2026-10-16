package console

import (
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/reviews"
)

// launchCampaign starts an access review:
// {"name": "Q4 review", "reviewer": "manager", "application_ids": [], "on_missing": "keep", "due_in_days": 14}.
func (a *API) launchCampaign(w http.ResponseWriter, r *http.Request) {
	var in reviews.Input
	if !decodeBody(w, r, &in, `{"name": "Q4 review", "reviewer": "manager", "due_in_days": 14}`) {
		return
	}
	var out reviews.Campaign
	err := a.guardedOn(r, authz.ManageCampaigns, authz.ReviewsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = reviews.Launch(r.Context(), tx, a.tenantID, me.Actor(), in)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

func (a *API) listCampaigns(w http.ResponseWriter, r *http.Request) {
	var out []reviews.Campaign
	err := a.guardedOn(r, authz.ReadCampaigns, authz.ReviewsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = reviews.List(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"campaigns": nonNil(out)}, err)
}

func (a *API) getCampaign(w http.ResponseWriter, r *http.Request) {
	var out reviews.Campaign
	err := a.guardedOn(r, authz.ReadCampaigns, authz.ReviewsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = reviews.Get(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// closeCampaign ends a campaign now; what nobody reviewed is kept or revoked
// as the campaign says.
func (a *API) closeCampaign(w http.ResponseWriter, r *http.Request) {
	var out reviews.Campaign
	err := a.guardedOn(r, authz.ManageCampaigns, authz.ReviewsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = reviews.Close(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// listReviewItems lists access to review: ?scope=mine (what you review in
// active campaigns, the default) or all (admins and auditors), with
// &campaign_id=, &decision=, &limit= and &after=<item id>.
func (a *API) listReviewItems(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := reviews.ItemFilter{CampaignID: q.Get("campaign_id"), Decision: q.Get("decision"), After: q.Get("after"), Limit: limit}
	switch q.Get("scope") {
	case "", "mine":
		f.Mine = true
	case "all":
	default:
		httpjson.Error(w, http.StatusBadRequest, "scope must be mine or all")
		return
	}
	var out []reviews.Item
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		if !f.Mine {
			if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadCampaigns, Resource: authz.ReviewsResource()}); err != nil {
				return err
			}
		}
		f.Me, f.Admin = me.ID, slices.Contains(me.Roles, authz.RoleAdmin)
		out, err = reviews.Items(r.Context(), tx, f)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"items": nonNil(out)}, err)
}

// decideReviewItem keeps or revokes one access:
// {"decision": "revoke", "comment": "moved to another team"}.
func (a *API) decideReviewItem(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Decision string `json:"decision"`
		Comment  string `json:"comment"`
	}
	if !decodeBody(w, r, &body, `{"decision": "keep", "comment": "still on the project"}`) {
		return
	}
	var out reviews.Item
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = reviews.GetItem(r.Context(), tx, r.PathValue("id")); err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.DecideReview, Resource: out.Resource()}); err != nil {
			return err
		}
		out, err = reviews.Decide(r.Context(), tx, a.tenantID, me.Actor(), out, body.Decision, body.Comment)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}
