package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/sod"
)

func (a *API) listSoDPolicies(w http.ResponseWriter, r *http.Request) {
	var out []sod.Policy
	err := a.guardedOn(r, authz.ReadSoD, authz.SoDResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = sod.List(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"policies": nonNil(out)}, err)
}

func (a *API) getSoDPolicy(w http.ResponseWriter, r *http.Request) {
	var out sod.Policy
	err := a.guardedOn(r, authz.ReadSoD, authz.SoDResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = sod.Get(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// saveSoDPolicy creates (POST) or replaces (PUT) a policy:
// {"name": "Payments", "mode": "prevent", "side_a": [{"entitlement_id": "..."}], "side_b": [{"role_id": "..."}]}.
func (a *API) saveSoDPolicy(w http.ResponseWriter, r *http.Request) {
	var in sod.Input
	if !decodeBody(w, r, &in, `{"name": "Payments", "side_a": [{"entitlement_id": "<id>"}], "side_b": [{"entitlement_id": "<id>"}]}`) {
		return
	}
	id := r.PathValue("id")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	var out sod.Policy
	err := a.guardedOn(r, authz.ManageSoD, authz.SoDResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = sod.Save(r.Context(), tx, a.tenantID, id, in); err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "sod.policy_saved", "sod_policy", out.ID, map[string]any{"policy": out})
	})
	a.respond(w, r, status, out, err)
}

func (a *API) deleteSoDPolicy(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.guardedOn(r, authz.ManageSoD, authz.SoDResource(), func(tx pgx.Tx, me authz.Subject) error {
		if err := sod.Delete(r.Context(), tx, id); err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "sod.policy_deleted", "sod_policy", id, nil)
	})
	a.noContent(w, r, err)
}

// listSoDViolations pages through violations: ?status=open&policy_id=&limit=&after=.
func (a *API) listSoDViolations(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var out []sod.Violation
	err := a.guardedOn(r, authz.ReadSoD, authz.SoDResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = sod.Violations(r.Context(), tx, sod.Filter{Status: q.Get("status"), PolicyID: q.Get("policy_id"), After: q.Get("after"), Limit: limit})
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"violations": nonNil(out)}, err)
}

// scanSoD looks for violations now.
func (a *API) scanSoD(w http.ResponseWriter, r *http.Request) {
	if _, err := a.allowed(r, authz.ManageSoD, authz.SoDResource()); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := sod.Scan(r.Context(), a.pool, a.tenantID)
	a.respond(w, r, http.StatusOK, res, err)
}

// exceptSoDViolation accepts a violation: {"reason": "...", "days": 90}.
func (a *API) exceptSoDViolation(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Reason string `json:"reason"`
		Days   int    `json:"days"`
	}
	if !decodeBody(w, r, &body, `{"reason": "covers the finance lead's leave; payments are checked by audit weekly", "days": 30}`) {
		return
	}
	var out sod.Violation
	err := a.guardedOn(r, authz.ManageSoD, authz.SoDResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = sod.Except(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), body.Reason, body.Days)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}
