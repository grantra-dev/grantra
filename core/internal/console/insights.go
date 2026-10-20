package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/attackpaths"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/insights"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// attackPaths runs fn on the tenant's attack path graph, for the people
// who watch for threats.
func (a *API) attackPaths(r *http.Request, fn func(g *attackpaths.Graph) error) error {
	return a.guardedOn(r, authz.ReadThreats, authz.ThreatsResource(), func(tx pgx.Tx, _ authz.Subject) error {
		g, err := attackpaths.Load(r.Context(), tx)
		if err != nil {
			return err
		}
		return fn(g)
	})
}

// listAttackPaths lists the high and critical risk access people reach,
// riskiest first.
func (a *API) listAttackPaths(w http.ResponseWriter, r *http.Request) {
	var out []attackpaths.Target
	err := a.attackPaths(r, func(g *attackpaths.Graph) error {
		out = g.Targets()
		return nil
	})
	a.respond(w, r, http.StatusOK, map[string]any{"targets": httpjson.NonNil(out)}, err)
}

// getAttackPaths is one target and every way people reach it.
func (a *API) getAttackPaths(w http.ResponseWriter, r *http.Request) {
	var out attackpaths.Detail
	err := a.attackPaths(r, func(g *attackpaths.Graph) (err error) {
		out, err = g.Detail(r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// identityReach lists the high and critical risk access someone reaches:
// what whoever takes over their account gets.
func (a *API) identityReach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var out []attackpaths.Reach
	err := a.attackPaths(r, func(g *attackpaths.Graph) error {
		if !db.ValidUUID(id) {
			return apperr.ErrNotFound
		}
		out = g.Reach(id)
		return nil
	})
	a.respond(w, r, http.StatusOK, map[string]any{"targets": httpjson.NonNil(out)}, err)
}

// listUnusedAccess pages through access nobody used lately:
// ?identity=&limit=&after=<id>.
func (a *API) listUnusedAccess(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	identity, ok := identityParam(w, r)
	if !ok {
		return
	}
	var out []insights.Unused
	var days int
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		if days, err = settings.Get(r.Context(), tx, settings.UnusedDays); err != nil {
			return err
		}
		out, err = insights.UnusedAccess(r.Context(), tx, insights.UnusedFilter{IdentityID: identity,
			After: r.URL.Query().Get("after"), Limit: limit})
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"access": httpjson.NonNil(out), "days": days}, err)
}

// listRoleSuggestions lists the business roles Grantra finds in what people
// hold one by one.
func (a *API) listRoleSuggestions(w http.ResponseWriter, r *http.Request) {
	var out []insights.RoleSuggestion
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = insights.SuggestRoles(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"suggestions": httpjson.NonNil(out)}, err)
}
