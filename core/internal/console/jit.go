package console

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/requests"
)

// identityEligibilities lists what someone may activate. Whoever may see the
// person sees them.
func (a *API) identityEligibilities(w http.ResponseWriter, r *http.Request) {
	var out []access.Eligibility
	err := a.guardedOnIdentity(r, authz.ReadIdentity, func(tx pgx.Tx, _, target authz.Subject) (err error) {
		out, err = access.Eligibilities(r.Context(), tx, target.ID)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"eligibilities": httpjson.NonNil(out)}, err)
}

// makeEligible lets someone activate a role or entitlement: admins do it
// for others, never for themselves.
func (a *API) makeEligible(w http.ResponseWriter, r *http.Request) {
	var in struct {
		access.Target
		DurationDays *int `json:"duration_days"`
	}
	if !decodeBody(w, r, &in, `{"entitlement_id": "<entitlement id>", "duration_days": 90}`) {
		return
	}
	var out access.Eligibility
	err := a.guardedOnIdentity(r, authz.ManageAccess, func(tx pgx.Tx, me, target authz.Subject) error {
		var expires *time.Time
		if in.DurationDays != nil {
			if *in.DurationDays < 1 || *in.DurationDays > 365 {
				return apperr.Invalidf("duration_days must be between 1 and 365")
			}
			t := time.Now().Add(time.Duration(*in.DurationDays) * 24 * time.Hour)
			expires = &t
		}
		id, err := access.MakeEligible(r.Context(), tx, a.tenantID, me.Actor(), target.ID, in.Target, expires, "")
		if err != nil {
			return err
		}
		out, err = access.GetEligibility(r.Context(), tx, id, false)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

// onEligibility loads the eligibility named by {id} and checks that the
// policies allow action on its person.
func (a *API) onEligibility(r *http.Request, tx pgx.Tx, me authz.Subject, action authz.Action) (access.Eligibility, error) {
	el, err := access.GetEligibility(r.Context(), tx, r.PathValue("id"), false)
	if err != nil {
		return el, err
	}
	_, err = a.checkIdentity(r, tx, me, action, el.IdentityID)
	return el, err
}

func (a *API) removeEligibility(w http.ResponseWriter, r *http.Request) {
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		el, err := a.onEligibility(r, tx, me, authz.ManageAccess)
		if err != nil {
			return err
		}
		return access.RemoveEligibility(r.Context(), tx, a.tenantID, me.Actor(), el.ID)
	})
	a.noContent(w, r, err)
}

// activateEligibility turns one's own eligibility into access for hours,
// at once or through an approval.
func (a *API) activateEligibility(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hours         int    `json:"hours"`
		Justification string `json:"justification"`
	}
	if !decodeBody(w, r, &in, `{"hours": 2, "justification": "INC-1234: fix the payment run"}`) {
		return
	}
	var out requests.ActivationResult
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		el, err := a.onEligibility(r, tx, me, authz.ActivateAccess)
		if err != nil {
			return err
		}
		out, err = requests.Activate(r.Context(), tx, a.tenantID, me.ID, el.ID, in.Hours, in.Justification)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

// deactivateEligibility ends an activation early: the person themselves, or
// an admin.
func (a *API) deactivateEligibility(w http.ResponseWriter, r *http.Request) {
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		el, err := access.GetEligibility(r.Context(), tx, r.PathValue("id"), true)
		if err != nil {
			return err
		}
		action := authz.ActivateAccess
		if el.IdentityID != me.ID {
			action = authz.ManageAccess
		}
		if _, err := a.checkIdentity(r, tx, me, action, el.IdentityID); err != nil {
			return err
		}
		return access.Deactivate(r.Context(), tx, a.tenantID, me.Actor(), el)
	})
	a.noContent(w, r, err)
}

func (a *API) setEntitlementJIT(w http.ResponseWriter, r *http.Request) {
	var in catalog.JIT
	if !decodeBody(w, r, &in, `{"jit_only": true, "max_activation_hours": 4, "activation_approval": false}`) {
		return
	}
	var out catalog.Entitlement
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = catalog.SetJIT(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), in)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// standingAccess lists access to high and critical risk entitlements held
// with no end date: ?limit= (default 50, at most 200).
func (a *API) standingAccess(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var out []access.Standing
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = access.StandingAccess(r.Context(), tx, limit)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"standing": httpjson.NonNil(out)}, err)
}

// convertToEligibility turns standing access into eligibility.
func (a *API) convertToEligibility(w http.ResponseWriter, r *http.Request) {
	var out access.Eligibility
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		var identity string
		err := pgx.ErrNoRows
		if id := r.PathValue("id"); db.ValidUUID(id) {
			err = tx.QueryRow(r.Context(), `SELECT identity_id FROM access_assignments WHERE id = $1`, id).Scan(&identity)
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := a.checkIdentity(r, tx, me, authz.ManageAccess, identity); err != nil {
			return err
		}
		id, err := access.ConvertToEligibility(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
		if err != nil {
			return err
		}
		out, err = access.GetEligibility(r.Context(), tx, id, false)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}
