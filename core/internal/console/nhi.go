package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/nhi"
)

// listNonHuman pages through service accounts, agents and workloads:
// ?kind=&flag=&q=&retired=true&limit=&after=<id>.
func (a *API) listNonHuman(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := nhi.Filter{Kind: q.Get("kind"), Flag: q.Get("flag"), Retired: q.Get("retired") == "true",
		Search: q.Get("q"), After: q.Get("after"), Limit: limit}
	var out []nhi.Identity
	err := a.guardedOn(r, authz.ListIdentities, authz.DirectoryResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = nhi.List(r.Context(), tx, f)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"identities": httpjson.NonNil(out)}, err)
}

// nonHumanSummary counts the service accounts, agents and workloads that are
// not retired, and how many have each flag.
func (a *API) nonHumanSummary(w http.ResponseWriter, r *http.Request) {
	var out map[string]int
	err := a.guardedOn(r, authz.ListIdentities, authz.DirectoryResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = nhi.Summary(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// getNonHuman is one service account, agent or workload, for whoever may see
// it: its owner (as its manager), helpdesk, auditors and admins.
func (a *API) getNonHuman(w http.ResponseWriter, r *http.Request) {
	var out nhi.Identity
	err := a.guardedOnIdentity(r, authz.ReadIdentity, func(tx pgx.Tx, _, target authz.Subject) (err error) {
		out, err = nhi.Get(r.Context(), tx, target.ID)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// manageNonHuman runs fn for an admin managing non-human identities.
func (a *API) manageNonHuman(r *http.Request, fn func(tx pgx.Tx, me authz.Subject) error) error {
	return a.guardedOn(r, authz.ManageIdentities, authz.DirectoryResource(), fn)
}

const nonHumanExample = `{"kind": "service", "name": "billing-sync", "owner_id": "<person id>", "purpose": "Copies invoices to the ERP", "account_ids": ["<account id>"]}`

// createNonHuman adds a service account, agent or workload with its owner.
func (a *API) createNonHuman(w http.ResponseWriter, r *http.Request) {
	var in nhi.Input
	if !decodeBody(w, r, &in, nonHumanExample) {
		return
	}
	var out nhi.Identity
	err := a.manageNonHuman(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = nhi.Create(r.Context(), tx, a.tenantID, me.Actor(), in)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

// updateNonHuman changes a non-human identity's name, purpose, owner, SPIFFE
// ID or end date.
func (a *API) updateNonHuman(w http.ResponseWriter, r *http.Request) {
	var in nhi.Input
	if !decodeBody(w, r, &in, `{"owner_id": "<person id>", "end_date": "2027-03-31"}`) {
		return
	}
	var out nhi.Identity
	err := a.manageNonHuman(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = nhi.Update(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), in)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// retireNonHuman ends a non-human identity: {"reason": "..."}.
func (a *API) retireNonHuman(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body, `{"reason": "Replaced by billing-sync-v2"}`) {
		return
	}
	var out nhi.Identity
	err := a.manageNonHuman(r, func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = nhi.Retire(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), body.Reason)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// listNonHumanCandidates lists the accounts nobody owns, which may belong to
// a service account, agent or workload.
func (a *API) listNonHumanCandidates(w http.ResponseWriter, r *http.Request) {
	var out []nhi.Candidate
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = nhi.Candidates(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"accounts": httpjson.NonNil(out), "limit": nhi.MaxCandidates}, err)
}
