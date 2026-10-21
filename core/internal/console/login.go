package console

import (
	"errors"
	"io"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/login"
)

// openLogin checks action on Grantra Login and returns a client for it. The
// transaction ends before the caller talks to Keycloak.
func (a *API) openLogin(r *http.Request, action authz.Action) (login.App, *grantralogin.Client, authz.Subject, error) {
	var (
		app login.App
		c   *grantralogin.Client
		me  authz.Subject
	)
	err := a.guardedOn(r, action, authz.LoginResource(), func(tx pgx.Tx, s authz.Subject) (err error) {
		me = s
		app, c, err = login.Open(r.Context(), tx, a.Secrets.Resolve)
		return err
	})
	return app, c, me, err
}

// loginFail answers errors from Grantra Login: Keycloak's refusals and
// outages are 502s with a message an admin can act on.
func (a *API) loginFail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, login.ErrNotConnected):
		httpjson.Error(w, http.StatusConflict, "Grantra Login is not connected: give an application the grantra-login connector first")
	case errors.As(err, new(*apperr.Invalid)):
		httpjson.Error(w, http.StatusBadRequest, err.Error())
	case grantralogin.IsForbidden(err):
		httpjson.Error(w, http.StatusBadGateway, "Grantra Login refused the request: the grantra-connector service account needs the realm-management roles manage-clients, manage-realm and manage-identity-providers for this")
	case grantralogin.IsNotFound(err):
		httpjson.Error(w, http.StatusNotFound, "not found in Grantra Login")
	case grantralogin.IsRemote(err):
		httpjson.Error(w, http.StatusBadGateway, "Grantra Login: "+err.Error())
	default:
		a.fail(w, r, err)
	}
}

type loginStatus struct {
	Connected   bool                  `json:"connected"`
	Application *login.App            `json:"application,omitempty"`
	Realm       *grantralogin.Status  `json:"realm,omitempty"`
	Policy      *grantralogin.Policy  `json:"policy,omitempty"`
	Apps        []grantralogin.SSOApp `json:"apps,omitempty"`
	// IdentityProviders are the providers people can sign in with instead.
	IdentityProviders []grantralogin.IdentityProvider `json:"identity_providers,omitempty"`
	// Error says why Grantra Login could not be read, when it could not.
	Error string `json:"error,omitempty"`
}

// getLogin answers what the console's Grantra Login page shows. A Grantra
// Login that is down or refuses the service account is reported in error,
// with a 200, so the page can say so.
func (a *API) getLogin(w http.ResponseWriter, r *http.Request) {
	app, c, _, err := a.openLogin(r, authz.ReadLogin)
	if errors.Is(err, login.ErrNotConnected) {
		httpjson.Write(w, http.StatusOK, loginStatus{})
		return
	}
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	out := loginStatus{Connected: true, Application: &app}
	ctx := r.Context()
	st, err := c.Status(ctx)
	if err == nil {
		out.Realm = &st
		var p grantralogin.Policy
		if p, err = c.Policy(ctx); err == nil {
			out.Policy = &p
			if out.Apps, err = c.Apps(ctx); err == nil {
				out.IdentityProviders, err = c.IdentityProviders(ctx)
			}
		}
	}
	switch {
	case err == nil:
	case grantralogin.IsForbidden(err):
		out.Error = "Grantra Login refused the grantra-connector service account. It needs the realm-management roles view-realm, manage-clients, manage-realm and manage-identity-providers for this page."
	case grantralogin.IsRemote(err):
		out.Error = err.Error()
	default:
		a.fail(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, out)
}

// createLoginApp registers an app that signs people in through Grantra Login.
// A confidential app's secret is in the answer, and only there.
func (a *API) createLoginApp(w http.ResponseWriter, r *http.Request) {
	var in grantralogin.NewSSOApp
	if !decodeBody(w, r, &in, `{"client_id": "wiki", "name": "Wiki", "redirect_uris": ["https://wiki.example.com/oauth/callback"]}`) {
		return
	}
	app, c, me, err := a.openLogin(r, authz.ManageLogin)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	created, secret, err := c.CreateApp(r.Context(), in)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	err = a.loginAudit(r, me, "login.app_created", created.ID, map[string]any{
		"application_id": app.ID, "client_id": created.ClientID, "public": created.Public, "redirect_uris": created.RedirectURIs,
	})
	a.respond(w, r, http.StatusCreated, map[string]any{"app": created, "secret": secret}, err)
}

// rotateLoginAppSecret gives an app a new secret, shown once.
func (a *API) rotateLoginAppSecret(w http.ResponseWriter, r *http.Request) {
	app, c, me, err := a.openLogin(r, authz.ManageLogin)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	id := r.PathValue("id")
	secret, err := c.RotateSecret(r.Context(), id)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	err = a.loginAudit(r, me, "login.app_secret_rotated", id, map[string]any{"application_id": app.ID})
	a.respond(w, r, http.StatusOK, map[string]string{"secret": secret}, err)
}

// setLoginPolicy writes the sign-in policy, then brings the MFA role up to
// date when the policy asks people with sensitive access for a second factor.
func (a *API) setLoginPolicy(w http.ResponseWriter, r *http.Request) {
	var p grantralogin.Policy
	if !decodeBody(w, r, &p, `{"mfa": "sensitive", "passkeys": true}`) {
		return
	}
	app, c, me, err := a.openLogin(r, authz.ManageLogin)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	if err := c.SetPolicy(r.Context(), p); err != nil {
		a.loginFail(w, r, err)
		return
	}
	if err := a.loginAudit(r, me, "login.policy_set", app.ID, map[string]any{"mfa": p.MFA, "passkeys": p.Passkeys}); err != nil {
		a.fail(w, r, err)
		return
	}
	out := map[string]any{"policy": p}
	if p.MFA == grantralogin.MFASensitive {
		sync, err := login.SyncMFA(r.Context(), a.pool, a.tenantID, a.Secrets.Resolve)
		if err != nil {
			// The policy is set; the job retries the sync.
			a.log.WarnContext(r.Context(), "MFA role sync failed", "err", err)
			out["sync_error"] = err.Error()
		} else {
			out["sync"] = sync
		}
	}
	httpjson.Write(w, http.StatusOK, out)
}

// createIdentityProvider adds a provider people can sign in with. The client
// secret goes to Grantra Login and is neither stored nor audited here.
func (a *API) createIdentityProvider(w http.ResponseWriter, r *http.Request) {
	var in grantralogin.NewIdentityProvider
	if !decodeBody(w, r, &in, `{"alias": "entra", "display_name": "Microsoft", "kind": "entra", "tenant": "acme.onmicrosoft.com", "client_id": "...", "client_secret": "..."}`) {
		return
	}
	app, c, me, err := a.openLogin(r, authz.ManageLogin)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	p, err := c.CreateIdentityProvider(r.Context(), in)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	err = a.loginAudit(r, me, "login.identity_provider_added", p.Alias, map[string]any{
		"application_id": app.ID, "kind": p.Kind, "link_by_email": p.LinkByEmail,
	})
	a.respond(w, r, http.StatusCreated, p, err)
}

// setIdentityProviderDomains routes email domains to a provider.
func (a *API) setIdentityProviderDomains(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Domains []string `json:"domains"`
	}
	if !decodeBody(w, r, &in, `{"domains": ["acme.com"]}`) {
		return
	}
	app, c, me, err := a.openLogin(r, authz.ManageLogin)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	alias := r.PathValue("alias")
	if err := c.SetDomains(r.Context(), alias, in.Domains); err != nil {
		a.loginFail(w, r, err)
		return
	}
	err = a.loginAudit(r, me, "login.domains_routed", alias, map[string]any{"application_id": app.ID, "domains": httpjson.NonNil(in.Domains)})
	a.respond(w, r, http.StatusOK, map[string]any{"domains": httpjson.NonNil(in.Domains)}, err)
}

func (a *API) deleteIdentityProvider(w http.ResponseWriter, r *http.Request) {
	app, c, me, err := a.openLogin(r, authz.ManageLogin)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	alias := r.PathValue("alias")
	if err := c.DeleteIdentityProvider(r.Context(), alias); err != nil {
		a.loginFail(w, r, err)
		return
	}
	err = a.loginAudit(r, me, "login.identity_provider_removed", alias, map[string]any{"application_id": app.ID})
	a.noContent(w, r, err)
}

// maxExportBytes bounds realm exports.
const maxExportBytes = 32 << 20

// importToLogin imports a Keycloak realm export (the body). ?dry_run=true
// only says what the import would do.
func (a *API) importToLogin(w http.ResponseWriter, r *http.Request) {
	dryRun, err := parseBool(r.URL.Query().Get("dry_run"))
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, "dry_run must be true or false")
		return
	}
	// openLogin checks the permission before the upload is read.
	app, c, me, err := a.openLogin(r, authz.ManageLogin)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxExportBytes))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			httpjson.Error(w, http.StatusRequestEntityTooLarge, "the file is larger than 32 MiB")
			return
		}
		httpjson.Error(w, http.StatusBadRequest, "could not read the file")
		return
	}
	plan, err := c.Import(r.Context(), data, dryRun)
	if err != nil {
		a.loginFail(w, r, err)
		return
	}
	if plan.Applied {
		err = a.loginAudit(r, me, "login.imported", plan.Realm, map[string]any{
			"application_id": app.ID, "users": len(plan.Users.Add), "groups": len(plan.Groups.Add),
			"roles": len(plan.Roles.Add), "apps": plan.Apps.Add,
		})
	}
	a.respond(w, r, http.StatusOK, plan, err)
}

func (a *API) loginAudit(r *http.Request, me authz.Subject, action, target string, details map[string]any) error {
	return db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) error {
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), action, "login", target, details)
	})
}
