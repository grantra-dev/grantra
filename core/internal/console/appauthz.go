package console

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/appauthz"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
)

type appAuthz struct {
	appauthz.Policies
	Keys []appauthz.Key `json:"keys"`
}

// getAppAuthz answers an application's authorization policies and API keys.
func (a *API) getAppAuthz(w http.ResponseWriter, r *http.Request) {
	var out appAuthz
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		id := r.PathValue("id")
		if out.Policies, err = appauthz.GetPolicies(r.Context(), tx, id); err != nil {
			return err
		}
		out.Keys, err = appauthz.ListKeys(r.Context(), tx, id)
		out.Keys = httpjson.NonNil(out.Keys)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) setAppAuthzPolicies(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Policies string `json:"policies"`
	}
	if !decodeBody(w, r, &in, `{"policies": "permit (principal, action, resource) when { ... };"}`) {
		return
	}
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) error {
		return appauthz.SetPolicies(r.Context(), tx, a.tenantID, r.PathValue("id"), in.Policies, me.Actor())
	})
	a.respond(w, r, http.StatusOK, map[string]string{"policies": in.Policies}, err)
}

func (a *API) createAppAPIKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeBody(w, r, &in, `{"name": "billing backend (production)"}`) {
		return
	}
	var (
		key    appauthz.Key
		secret string
	)
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		key, secret, err = appauthz.CreateKey(r.Context(), tx, a.tenantID, r.PathValue("id"), in.Name, me.Actor())
		return err
	})
	a.respond(w, r, http.StatusCreated, map[string]any{"key": key, "secret": secret}, err)
}

func (a *API) revokeAppAPIKey(w http.ResponseWriter, r *http.Request) {
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) error {
		return appauthz.RevokeKey(r.Context(), tx, a.tenantID, r.PathValue("id"), r.PathValue("key"), me.Actor())
	})
	a.noContent(w, r, err)
}

// testAppAuthz is the console's policy playground: it answers a question
// about a person, with the stored policies or a draft, and shows what the
// policies see of the person.
func (a *API) testAppAuthz(w http.ResponseWriter, r *http.Request) {
	var in struct {
		appauthz.Question
		// Policies is a draft to try instead of the stored policies.
		Policies *string `json:"policies"`
	}
	if !decodeBody(w, r, &in, `{"principal": {"email": "ana@example.com"}, "action": "approve", "resource": {"type": "Invoice", "id": "42", "attributes": {"amount": 1200}}}`) {
		return
	}
	var out struct {
		appauthz.Answer
		Person *appauthz.Person `json:"person"`
	}
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) error {
		id := r.PathValue("id")
		src := ""
		if in.Policies != nil {
			src = *in.Policies
		} else {
			p, err := appauthz.GetPolicies(r.Context(), tx, id)
			if err != nil {
				return err
			}
			src = p.Source
		}
		set, err := appauthz.Parse(src)
		if err != nil {
			return err
		}
		if out.Person, err = appauthz.FindPerson(r.Context(), tx, id, in.Principal); err != nil {
			return err
		}
		out.Answer, err = appauthz.Evaluate(set, out.Person, in.Question)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// authzCheck answers an application's backend, which authenticates with
// one of the application's API keys:
//
//	POST /api/v1/authz/check
//	Authorization: Bearer grantra_ak_...
//
// Answers are not audited one by one: apps may ask on every request.
func (a *API) authzCheck(w http.ResponseWriter, r *http.Request) {
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer realm="grantra"`)
		httpjson.Error(w, http.StatusUnauthorized, "send an application API key as Authorization: Bearer grantra_ak_...")
		return
	}
	var q appauthz.Question
	if !decodeBodyMax(w, r, &q, 64<<10, `{"principal": {"email": "ana@example.com"}, "action": "approve", "resource": {"type": "Invoice", "id": "42"}}`) {
		return
	}
	var out appauthz.Answer
	err := db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) error {
		app, err := appauthz.AppForKey(r.Context(), tx, strings.TrimSpace(secret))
		if err != nil {
			return err
		}
		out, err = appauthz.Check(r.Context(), tx, app, q)
		return err
	})
	if errors.Is(err, appauthz.ErrBadKey) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="grantra", error="invalid_token"`)
		httpjson.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	a.respond(w, r, http.StatusOK, out, err)
}
