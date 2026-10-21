package console

import (
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/approvals"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/requests"
)

// listRequestable lists what people can ask for, with how many people on
// the asker's team (team_size) hold each item.
func (a *API) listRequestable(w http.ResponseWriter, r *http.Request) {
	var out []requests.Requestable
	var teamSize int
	err := a.guarded(r, authz.ReadRequestable, func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = requests.ListRequestable(r.Context(), tx); err != nil {
			return err
		}
		size, held, err := requests.TeamHolders(r.Context(), tx, me.ID)
		teamSize = size
		for i := range out {
			out[i].TeamHolders = held[out[i].Type+":"+out[i].ID]
		}
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"items": httpjson.NonNil(out), "team_size": teamSize}, err)
}

// createRequest asks for access, for yourself or for one of your reports:
// {"role_id": "...", "justification": "...", "duration_days": 30}, with
// "beneficiary_id" when it is for someone else.
func (a *API) createRequest(w http.ResponseWriter, r *http.Request) {
	var in requests.Input
	if !decodeBody(w, r, &in, `{"entitlement_id": "<entitlement id>", "justification": "why it is needed"}`) {
		return
	}
	var out requests.Request
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		if in.BeneficiaryID == "" {
			in.BeneficiaryID = me.ID
		}
		if _, err := a.checkIdentity(r, tx, me, authz.CreateRequest, in.BeneficiaryID); err != nil {
			return err
		}
		out, err = requests.Create(r.Context(), tx, a.tenantID, me.ID, in)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

// previewRequest tells, before asking, who would approve the request and
// which prevent-mode separation of duties policies it would break: the same
// body as createRequest, without the justification.
func (a *API) previewRequest(w http.ResponseWriter, r *http.Request) {
	var in requests.Input
	if !decodeBody(w, r, &in, `{"entitlement_id": "<entitlement id>"}`) {
		return
	}
	var (
		plan      approvals.Plan
		conflicts []string
	)
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		if in.BeneficiaryID == "" {
			in.BeneficiaryID = me.ID
		}
		if _, err := a.checkIdentity(r, tx, me, authz.CreateRequest, in.BeneficiaryID); err != nil {
			return err
		}
		plan, conflicts, err = requests.Preview(r.Context(), tx, me.ID, in, nil)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"sod_conflicts": httpjson.NonNil(conflicts), "plan": plan}, err)
}

// listRequests lists requests: ?scope=mine (asked by or for you, the
// default), approvals (waiting on you) or all (admins and auditors), with
// &status=, &limit= and &after=<request id>.
func (a *API) listRequests(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := requests.Filter{Scope: q.Get("scope"), Status: q.Get("status"), After: q.Get("after"), Limit: limit}
	if f.Scope == "" {
		f.Scope = requests.ScopeMine
	}
	var out []requests.Request
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		if f.Scope == requests.ScopeAll {
			if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ListRequests, Resource: authz.RequestsResource()}); err != nil {
				return err
			}
		}
		f.Me, f.Admin = me.ID, slices.Contains(me.Roles, authz.RoleAdmin)
		out, err = requests.List(r.Context(), tx, f)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"requests": httpjson.NonNil(out)}, err)
}

func (a *API) getRequest(w http.ResponseWriter, r *http.Request) {
	var out requests.Request
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = requests.Get(r.Context(), tx, r.PathValue("id"), false); err != nil {
			return err
		}
		return a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadRequest, Resource: out.Resource()})
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// decideRequest approves or rejects the step a request waits on:
// {"decision": "approve", "comment": "..."}. Accepting a separation of
// duties risk also takes "exception_days", and the comment is its reason.
func (a *API) decideRequest(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Decision      string `json:"decision"`
		Comment       string `json:"comment"`
		ExceptionDays int    `json:"exception_days"`
	}
	if !decodeBody(w, r, &body, `{"decision": "approve", "comment": "ok for the Q3 project"}`) {
		return
	}
	if body.Decision != "approve" && body.Decision != "reject" {
		httpjson.Error(w, http.StatusBadRequest, `decision must be "approve" or "reject"`)
		return
	}
	var out requests.Request
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = requests.Get(r.Context(), tx, r.PathValue("id"), true); err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.DecideRequest, Resource: out.Resource()}); err != nil {
			return err
		}
		out, err = requests.Decide(r.Context(), tx, a.tenantID, me.Actor(), out, requests.Decision{
			Approve: body.Decision == "approve", Comment: body.Comment, ExceptionDays: body.ExceptionDays,
		})
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// cancelRequest withdraws a pending request.
func (a *API) cancelRequest(w http.ResponseWriter, r *http.Request) {
	var out requests.Request
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = requests.Get(r.Context(), tx, r.PathValue("id"), true); err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.CancelRequest, Resource: out.Resource()}); err != nil {
			return err
		}
		out, err = requests.Cancel(r.Context(), tx, a.tenantID, me.Actor(), out)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}
