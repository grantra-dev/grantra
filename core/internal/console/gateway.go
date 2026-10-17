package console

import (
	"context"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
)

// Clusters and databases behind the gateway sit with the vault, like SSH:
// vault.manage manages them and vault.read sees them and their sessions.
// Only people holding a target's access get tokens for it.

// GatewayAddresses are where people reach the gateway's listeners
// (host:port), shown in the console with the commands to connect.
type GatewayAddresses struct {
	Kubernetes string
	Postgres   string
	MySQL      string
	// CAFile, when set, holds the PEM certificate clients verify the
	// gateway with, for an operator's own TLS certificate; otherwise it is
	// the tenant's gateway CA.
	CAFile string
}

// gatewayCA is the PEM certificate clients verify the gateway with.
func (a *API) gatewayCA(ctx context.Context, tx pgx.Tx) (string, error) {
	if a.GatewayAddresses.CAFile != "" {
		b, err := os.ReadFile(a.GatewayAddresses.CAFile)
		return string(b), err
	}
	return a.Gateways.CACertificate(ctx, tx, a.tenantID)
}

// getGateway says whether people can connect, where the listeners are and
// the CA the gateway's certificate chains to.
func (a *API) getGateway(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": a.Gateways.Enabled(), "kubernetes_address": a.GatewayAddresses.Kubernetes,
		"postgres_address": a.GatewayAddresses.Postgres, "mysql_address": a.GatewayAddresses.MySQL, "ca_certificate": ""}
	err := a.inTenant(r, func(tx pgx.Tx, _ authz.Subject) error {
		if !a.Gateways.Enabled() {
			return nil
		}
		ca, err := a.gatewayCA(r.Context(), tx)
		out["ca_certificate"] = ca
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// gatewayCAFile serves the CA certificate without signing in, for clients
// that verify the gateway:
//
//	curl -o grantra-gateway-ca.pem https://grantra.example.com/api/v1/gateway/ca.pem
func (a *API) gatewayCAFile(w http.ResponseWriter, r *http.Request) {
	if !a.Gateways.Enabled() {
		httpjson.Error(w, http.StatusNotFound, gateways.ErrDisabled.Error())
		return
	}
	var ca string
	err := db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) (err error) {
		ca, err = a.gatewayCA(r.Context(), tx)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	io.WriteString(w, ca) //nolint:errcheck
}

// listGatewayTargets shows admins and auditors every target, and everyone
// else the ones they hold access to.
func (a *API) listGatewayTargets(w http.ResponseWriter, r *http.Request) {
	var out []gateways.Target
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		reader := a.authz.Authorize(authz.Request{Principal: me, Action: authz.ReadVault, Resource: authz.VaultResource()}).Allowed
		held, err := gateways.List(r.Context(), tx, me.ID)
		if err != nil {
			return err
		}
		if !reader {
			out = held
			// What the gateway logs in with is for admins and auditors.
			for i := range out {
				out[i].VaultAccountID, out[i].VaultAccountName, out[i].CACertificate = "", "", ""
			}
		} else if out, err = gateways.List(r.Context(), tx, ""); err != nil {
			return err
		}
		mine := map[string]bool{}
		for _, t := range held {
			mine[t.ID] = true
		}
		for i := range out {
			out[i].Held = mine[out[i].ID]
		}
		return nil
	})
	a.respond(w, r, http.StatusOK, map[string]any{"targets": nonNil(out)}, err)
}

func (a *API) createGatewayTarget(w http.ResponseWriter, r *http.Request) {
	var in gateways.Input
	if !decodeBodyMax(w, r, &in, 128<<10, `{"name": "orders-db", "kind": "postgres", "address": "10.0.0.7:5432", "vault_account_id": "..."}`) {
		return
	}
	var out gateways.Target
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = gateways.Create(r.Context(), tx, a.tenantID, me.Actor(), in)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

func (a *API) updateGatewayTarget(w http.ResponseWriter, r *http.Request) {
	var in gateways.Settings
	if !decodeBodyMax(w, r, &in, 128<<10, `{"address": "10.0.0.7:5432", "max_token_hours": 8}`) {
		return
	}
	var out gateways.Target
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = gateways.Update(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), in)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) deleteGatewayTarget(w http.ResponseWriter, r *http.Request) {
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) error {
		return gateways.Delete(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

// issueGatewayToken gives the caller a token for a target they hold access
// to, and for a cluster the kubeconfig that uses it.
func (a *API) issueGatewayToken(w http.ResponseWriter, r *http.Request) {
	var out struct {
		gateways.Token
		Kubeconfig    string `json:"kubeconfig,omitempty"`
		CACertificate string `json:"ca_certificate"`
	}
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if !a.Gateways.Enabled() {
			return gateways.ErrDisabled
		}
		id := r.PathValue("id")
		holders, err := gateways.Holders(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ConnectGateway, Resource: authz.GatewayTargetResource(id, holders)}); err != nil {
			return err
		}
		if out.Token, err = gateways.Issue(r.Context(), tx, a.tenantID, me.ID, id); err != nil {
			return err
		}
		if out.CACertificate, err = a.gatewayCA(r.Context(), tx); err != nil {
			return err
		}
		if out.Kind == gateways.Kubernetes {
			addr := a.GatewayAddresses.Kubernetes
			if addr == "" {
				addr = "GATEWAY:8443"
			}
			out.Kubeconfig = gateways.Kubeconfig(addr, out.TargetName, out.Token.Token, out.CACertificate)
		}
		return nil
	})
	if err == nil {
		w.Header().Set("Cache-Control", "no-store")
	}
	a.respond(w, r, http.StatusCreated, out, err)
}

// listGatewaySessions lists sessions, newest first: ?target=, ?identity=,
// ?active=true and ?limit= (default 50, at most 200).
func (a *API) listGatewaySessions(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := gateways.SessionFilter{TargetID: q.Get("target"), IdentityID: q.Get("identity"), Active: q.Get("active") == "true", Limit: limit}
	var out []gateways.Session
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = gateways.ListSessions(r.Context(), tx, f)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"sessions": nonNil(out)}, err)
}

// getGatewaySession returns a session with its queries or requests, 500 at
// a time: ?after= is the last event id already read.
func (a *API) getGatewaySession(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if r.URL.Query().Get("after") == "" {
		after, err = 0, nil
	}
	if err != nil || after < 0 {
		httpjson.Error(w, http.StatusBadRequest, "after must be an event id")
		return
	}
	var out struct {
		gateways.Session
		Log []gateways.Event `json:"log"`
	}
	err = a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		if out.Session, err = gateways.GetSession(r.Context(), tx, r.PathValue("id")); err != nil {
			return err
		}
		if out.Log, err = gateways.SessionEvents(r.Context(), tx, out.ID, after, 500); err != nil {
			return err
		}
		out.Log = nonNil(out.Log)
		// Like watching an SSH recording, reading a session's log is in
		// the audit log.
		if after == 0 {
			return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "gateway.session_viewed", "gateway_session", out.ID, nil)
		}
		return nil
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) terminateGatewaySession(w http.ResponseWriter, r *http.Request) {
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) error {
		return gateways.Terminate(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

// searchGatewayEvents finds queries and cluster requests: ?q= (any case),
// ?target= and ?limit=.
func (a *API) searchGatewayEvents(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var out []gateways.Event
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = gateways.SearchEvents(r.Context(), tx, r.URL.Query().Get("q"), r.URL.Query().Get("target"), limit)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"events": nonNil(out)}, err)
}
