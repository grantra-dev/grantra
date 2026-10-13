package console

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// setConnector configures how Grantra reads an application:
// {"kind": "scim", "config": {"base_url": "...", "token": "env:CRM_SCIM_TOKEN"}}.
func (a *API) setConnector(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Kind   string          `json:"kind"`
		Config json.RawMessage `json:"config"`
	}
	if !decodeBody(w, r, &body, `{"kind": "scim", "config": {"base_url": "https://crm.example.com/scim/v2", "token": "env:CRM_SCIM_TOKEN"}}`) {
		return
	}
	id := r.PathValue("id")
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) error {
		if err := accounts.SetConnector(r.Context(), tx, id, body.Kind, body.Config, a.secrets); err != nil {
			return err
		}
		// The config holds secret references, never secrets, so it is safe to log.
		return db.Audit(r.Context(), tx, a.tenantID, "identity:"+me.ID, "application.connector_set", "application", id,
			map[string]any{"kind": body.Kind, "config": body.Config})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// aggregate reads an application now. 200 when the read succeeded, 502 when
// the application could not be read (nothing is changed then).
func (a *API) aggregate(w http.ResponseWriter, r *http.Request) {
	var me authz.Subject
	err := a.guarded(r, authz.ManageCatalog, func(_ pgx.Tx, s authz.Subject) error {
		me = s
		return nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	run, err := accounts.Aggregate(r.Context(), a.pool, a.tenantID, r.PathValue("id"), "identity:"+me.ID, a.secrets)
	status := http.StatusOK
	if run.Status == accounts.RunFailed {
		status = http.StatusBadGateway
	}
	a.respond(w, r, status, run, err)
}

func (a *API) listConnectorRuns(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var out []accounts.Run
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = accounts.Runs(r.Context(), tx, r.PathValue("id"), limit)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"runs": nonNil(out)}, err)
}

// listAccounts pages through an application's accounts:
// ?orphans=true&limit=50&after=<account id>.
func (a *API) listAccounts(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	orphans, err := parseBool(q.Get("orphans"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "orphans must be true or false")
		return
	}
	after := q.Get("after")
	if after != "" && !db.ValidUUID(after) {
		writeError(w, http.StatusBadRequest, "after must be an account id")
		return
	}
	var out []accounts.Account
	err = a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = accounts.List(r.Context(), tx, r.PathValue("id"), accounts.Filter{Orphans: orphans, After: after, Limit: limit})
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"accounts": nonNil(out)}, err)
}

// linkAccount ties an account to an identity by hand: {"identity_id": "..."},
// or {"identity_id": null} to mark it as nobody's.
func (a *API) linkAccount(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IdentityID *string `json:"identity_id"`
	}
	if !decodeBody(w, r, &body, `{"identity_id": "<identity id>"}`) {
		return
	}
	identity := ""
	if body.IdentityID != nil {
		identity = *body.IdentityID
	}
	var out accounts.Account
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = accounts.Link(r.Context(), tx, r.PathValue("id"), identity); err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, "identity:"+me.ID, "account.linked", "account", out.ID,
			map[string]any{"application_id": out.ApplicationID, "native_id": out.NativeID, "identity_id": out.IdentityID})
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// identityAccounts lists the accounts an identity has in every application,
// for whoever may see the identity.
func (a *API) identityAccounts(w http.ResponseWriter, r *http.Request) {
	var out []accounts.Account
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		target, err := authz.LoadSubject(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadIdentity, Resource: authz.IdentityResource(target)}); err != nil {
			return err
		}
		out, err = accounts.ForIdentity(r.Context(), tx, target.ID)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"accounts": nonNil(out)}, err)
}
