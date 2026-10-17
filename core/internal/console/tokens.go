package console

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/servicetoken"
)

func (a *API) listServiceTokens(w http.ResponseWriter, r *http.Request) {
	var out []servicetoken.Token
	err := a.guardedOn(r, authz.ReadServiceTokens, authz.ServiceTokensResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = servicetoken.List(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"tokens": nonNil(out)}, err)
}

// createServiceToken makes a token: {"name": "Nightly HR export", "scopes":
// ["identity_source.sync:<source id>"], "expires_in_days": 365}. The answer
// carries the secret, shown this once.
func (a *API) createServiceToken(w http.ResponseWriter, r *http.Request) {
	var body servicetoken.New
	if !decodeBody(w, r, &body, `{"name": "Nightly HR export", "scopes": ["identity_source.sync"], "expires_in_days": 365}`) {
		return
	}
	var (
		tok    servicetoken.Token
		secret string
	)
	err := a.guardedOn(r, authz.ManageServiceTokens, authz.ServiceTokensResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		tok, secret, err = servicetoken.Create(r.Context(), tx, a.tenantID, body, me.Actor())
		return err
	})
	a.respond(w, r, http.StatusCreated, map[string]any{"token": tok, "secret": secret}, err)
}

func (a *API) revokeServiceToken(w http.ResponseWriter, r *http.Request) {
	err := a.guardedOn(r, authz.ManageServiceTokens, authz.ServiceTokensResource(), func(tx pgx.Tx, me authz.Subject) error {
		return servicetoken.Revoke(r.Context(), tx, a.tenantID, r.PathValue("id"), me.Actor())
	})
	a.noContent(w, r, err)
}

type tokenKey struct{}

// sessionOrToken lets a request through with a service token in an
// "Authorization: Bearer grantra_st_..." header, or else with a session as
// require checks it. Handlers behind it find out who is calling with
// a.caller.
func (a *API) sessionOrToken(require func(http.Handler) http.Handler, next http.Handler) http.Handler {
	withSession := require(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			withSession.ServeHTTP(w, r)
			return
		}
		var tok servicetoken.Token
		err := db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) (err error) {
			tok, err = servicetoken.Authenticate(r.Context(), tx, strings.TrimSpace(secret))
			return err
		})
		switch {
		case errors.Is(err, servicetoken.ErrBadToken):
			httpjson.Error(w, http.StatusUnauthorized, err.Error())
		case err != nil:
			a.log.ErrorContext(r.Context(), "service token lookup failed", "err", err)
			httpjson.Error(w, http.StatusInternalServerError, "internal error")
		default:
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), tokenKey{}, tok)))
		}
	})
}

// caller checks that whoever calls may do this: a signed-in person when the
// policies allow action on resource, or a service token holding scope for
// target (the id of the source or application). It returns how the audit
// log names the caller. Refused tokens are audited, as refused people are.
func (a *API) caller(r *http.Request, action authz.Action, resource authz.Resource, scope, target string) (string, error) {
	tok, ok := r.Context().Value(tokenKey{}).(servicetoken.Token)
	if !ok {
		me, err := a.allowed(r, action, resource)
		return me.Actor(), err
	}
	if tok.Allows(scope, target) {
		return tok.Actor(), nil
	}
	a.log.InfoContext(r.Context(), "request denied", "token", tok.ID, "scope", scope, "target", target)
	ctx := context.WithoutCancel(r.Context())
	err := db.InTenant(ctx, a.pool, a.tenantID, func(tx pgx.Tx) error {
		return db.Audit(ctx, tx, a.tenantID, tok.Actor(), "authz.denied", "", "",
			map[string]any{"scope": scope, "target": target, "scopes": tok.Scopes})
	})
	if err != nil {
		a.log.ErrorContext(ctx, "audit denial", "err", err)
	}
	return "", errDenied
}
