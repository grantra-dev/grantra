package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
)

// The plumbing every console handler shares: transactions with the signed-in
// person, policy checks, reading requests and writing answers. A handler
// should only hold what is particular to its endpoint.

// errDenied ends a transaction whose request the policies refused.
var errDenied = errors.New("denied")

// inTenant runs fn in a tenant transaction with the signed-in person loaded
// as a policy subject.
func (a *API) inTenant(r *http.Request, fn func(tx pgx.Tx, me authz.Subject) error) error {
	p, ok := auth.PrincipalFrom(r.Context())
	if !ok {
		return errors.New("console handler reached without a signed-in person")
	}
	return db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) error {
		me, err := authz.LoadSubject(r.Context(), tx, p.IdentityID)
		if err != nil {
			return err
		}
		return fn(tx, me)
	})
}

// check asks the policies about req. Refused writes are recorded in the audit
// log, from their own transaction so that they survive the refusal.
func (a *API) check(ctx context.Context, req authz.Request) error {
	d := a.authz.Authorize(req)
	if len(d.Errors) > 0 {
		a.log.ErrorContext(ctx, "policy evaluation errors", "action", req.Action, "errors", d.Errors)
	}
	if d.Allowed {
		return nil
	}
	a.log.InfoContext(ctx, "request denied", "identity", req.Principal.ID, "action", req.Action, "policies", d.Policies)
	if req.Action.Writes() {
		details := map[string]any{"action": string(req.Action), "policies": d.Policies}
		target := ""
		if req.Target != nil {
			target = req.Target.ID
		}
		actx := context.WithoutCancel(ctx)
		err := db.InTenant(actx, a.pool, a.tenantID, func(tx pgx.Tx) error {
			return db.Audit(actx, tx, a.tenantID, req.Principal.Actor(), "authz.denied", "identity", target, details)
		})
		if err != nil {
			a.log.ErrorContext(ctx, "audit denial", "err", err)
		}
	}
	return errDenied
}

// guarded runs fn in a tenant transaction once the policies allow action on
// the catalog.
func (a *API) guarded(r *http.Request, action authz.Action, fn func(tx pgx.Tx, me authz.Subject) error) error {
	return a.guardedOn(r, action, authz.CatalogResource(), fn)
}

// guardedOn runs fn in a tenant transaction once the policies allow action
// on resource.
func (a *API) guardedOn(r *http.Request, action authz.Action, resource authz.Resource, fn func(tx pgx.Tx, me authz.Subject) error) error {
	return a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: action, Resource: resource}); err != nil {
			return err
		}
		return fn(tx, me)
	})
}

// guardedOnIdentity runs fn in a tenant transaction once the policies allow
// action on the identity named by the {id} path value, which fn gets as
// target.
func (a *API) guardedOnIdentity(r *http.Request, action authz.Action, fn func(tx pgx.Tx, me, target authz.Subject) error) error {
	return a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		target, err := a.checkIdentity(r, tx, me, action, r.PathValue("id"))
		if err != nil {
			return err
		}
		return fn(tx, me, target)
	})
}

// checkIdentity loads the identity id and asks the policies whether me may
// do action on it.
func (a *API) checkIdentity(r *http.Request, tx pgx.Tx, me authz.Subject, action authz.Action, id string) (authz.Subject, error) {
	target, err := authz.LoadSubject(r.Context(), tx, id)
	if err != nil {
		return authz.Subject{}, err
	}
	return target, a.check(r.Context(), authz.Request{Principal: me, Action: action, Resource: authz.IdentityResource(target)})
}

// allowed checks that the policies allow action on resource and returns the
// signed-in person, for handlers that do their work outside a console
// transaction (a sync or a scan that opens its own).
func (a *API) allowed(r *http.Request, action authz.Action, resource authz.Resource) (authz.Subject, error) {
	var me authz.Subject
	err := a.guardedOn(r, action, resource, func(_ pgx.Tx, s authz.Subject) error {
		me = s
		return nil
	})
	return me, err
}

// respond answers out with status, or the error when there is one.
func (a *API) respond(w http.ResponseWriter, r *http.Request, status int, out any, err error) {
	if err != nil {
		a.fail(w, r, err)
		return
	}
	httpjson.Write(w, status, out)
}

// noContent answers 204, or the error when there is one.
func (a *API) noContent(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail turns an error from a handler into a response.
func (a *API) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errDenied):
		httpjson.Error(w, http.StatusForbidden, "you do not have permission to do this")
	case errors.Is(err, apperr.ErrNotFound):
		httpjson.Error(w, http.StatusNotFound, "not found")
	case errors.Is(err, authz.ErrLastAdmin):
		httpjson.Error(w, http.StatusConflict, "this is the last active admin; grant the role to someone else first")
	case errors.Is(err, authz.ErrInactive):
		httpjson.Error(w, http.StatusConflict, "platform roles can only be granted to active identities")
	case errors.As(err, new(*apperr.Invalid)):
		httpjson.Error(w, http.StatusBadRequest, err.Error())
	case errors.As(err, new(*apperr.Conflict)):
		httpjson.Error(w, http.StatusConflict, err.Error())
	default:
		a.log.ErrorContext(r.Context(), "console request failed", "path", r.URL.Path, "err", err)
		httpjson.Error(w, http.StatusInternalServerError, "internal error")
	}
}

// decodeBody reads a small JSON body into v, refusing unknown fields. It
// answers 400 itself and returns false when the body is unusable.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, example string) bool {
	return decodeBodyMax(w, r, v, 256<<10, example)
}

// decodeBodyMax is decodeBody for bodies that must stay under limit bytes.
func decodeBodyMax(w http.ResponseWriter, r *http.Request, v any, limit int64, example string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		httpjson.Error(w, http.StatusBadRequest, "body must be JSON like "+example)
		return false
	}
	return true
}

// pageLimit reads ?limit=, 50 when absent. It answers 400 itself and returns
// false when the value is out of range.
func pageLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	v := r.URL.Query().Get("limit")
	if v == "" {
		return 50, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 200 {
		httpjson.Error(w, http.StatusBadRequest, "limit must be between 1 and 200")
		return 0, false
	}
	return n, true
}

// beforeCursor reads ?before=<event id> for lists that page backwards from
// the newest event, 0 when absent. It answers 400 itself and returns false
// when the value is not a positive id.
func beforeCursor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := r.URL.Query().Get("before")
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		httpjson.Error(w, http.StatusBadRequest, "before must be a positive event id")
		return 0, false
	}
	return n, true
}

// identityParam reads an optional ?identity=<id> filter. It answers 400
// itself and returns false when the value is not an id.
func identityParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.URL.Query().Get("identity")
	if id != "" && !db.ValidUUID(id) {
		httpjson.Error(w, http.StatusBadRequest, "identity must be an identity id")
		return "", false
	}
	return id, true
}

func parseBool(v string) (bool, error) {
	if v == "" {
		return false, nil
	}
	return strconv.ParseBool(v)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
