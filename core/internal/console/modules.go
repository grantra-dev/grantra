package console

import (
	"context"
	"net/http"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Modules a tenant can turn off in the console. The platform and identity
// governance are what Grantra is, so they are always on.
const (
	ModulePAM   = "pam"
	ModuleLogin = "login"
)

type module struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
}

var modules = []module{
	{ID: ModulePAM, Title: "Privileged access",
		Description: "The vault, servers over SSH, and remote access to databases, clusters, desktops and cloud consoles."},
	{ID: ModuleLogin, Title: "Grantra Login",
		Description: "The sign-in that ships with Grantra, for companies without their own. Turn it off when everyone signs in with Entra ID, Okta or Google."},
}

// disabledModules returns the modules the tenant turned off.
func disabledModules(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT module FROM tenant_modules WHERE NOT enabled ORDER BY module`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// moduleOn reports whether the tenant shows module, for the endpoints that
// gather things from several modules (inbox, search, home).
func moduleOn(ctx context.Context, tx pgx.Tx, module string) (bool, error) {
	var off bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenant_modules WHERE module = $1 AND NOT enabled)`, module).Scan(&off)
	return !off, err
}

func (a *API) listModules(w http.ResponseWriter, r *http.Request) {
	var out []module
	err := a.guardedOn(r, authz.ReadModules, authz.ModulesResource(), func(tx pgx.Tx, _ authz.Subject) error {
		off, err := disabledModules(r.Context(), tx)
		if err != nil {
			return err
		}
		for _, m := range modules {
			m.Enabled = !slices.Contains(off, m.ID)
			out = append(out, m)
		}
		return nil
	})
	a.respond(w, r, http.StatusOK, map[string]any{"modules": nonNil(out)}, err)
}

// setModule turns a module on or off: {"enabled": false}.
func (a *API) setModule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeBody(w, r, &body, `{"enabled": false}`) {
		return
	}
	id := r.PathValue("module")
	i := slices.IndexFunc(modules, func(m module) bool { return m.ID == id })
	var out module
	err := a.guardedOn(r, authz.ManageModules, authz.ModulesResource(), func(tx pgx.Tx, me authz.Subject) error {
		if i < 0 {
			return apperr.ErrNotFound
		}
		if body.Enabled == nil {
			return apperr.Invalidf("say whether the module is enabled")
		}
		on, err := moduleOn(r.Context(), tx, id)
		if err != nil {
			return err
		}
		out = modules[i]
		out.Enabled = *body.Enabled
		if on == out.Enabled {
			return nil
		}
		if _, err := tx.Exec(r.Context(),
			`INSERT INTO tenant_modules (tenant_id, module, enabled, updated_by) VALUES ($1, $2, $3, $4)
			 ON CONFLICT (tenant_id, module) DO UPDATE SET enabled = $3, updated_by = $4, updated_at = now()`,
			a.tenantID, id, out.Enabled, me.Actor()); err != nil {
			return err
		}
		action := "module.disabled"
		if out.Enabled {
			action = "module.enabled"
		}
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), action, "module", id, map[string]any{"title": out.Title})
	})
	a.respond(w, r, http.StatusOK, out, err)
}
