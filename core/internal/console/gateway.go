package console

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/gateways/cloud"
	"github.com/grantra-dev/grantra/core/internal/gateways/proxy"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
)

// Clusters, databases, desktops and cloud consoles behind the gateway sit
// with the vault, like SSH: vault.manage manages them and vault.read sees
// them and their sessions. Only people holding a target's access connect to
// it.

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

// Desktops is where desktop sessions run: the gateway relaying them to
// guacd, the context that ends them when Grantra stops, and the origins
// (host or host:port) the console's pages come from besides the request's
// own host, such as behind a proxy that changes it.
type Desktops struct {
	Gateway *proxy.Server
	Context context.Context
	Origins []string
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
		"postgres_address": a.GatewayAddresses.Postgres, "mysql_address": a.GatewayAddresses.MySQL, "ca_certificate": "",
		"desktops_enabled": a.Desktops.Gateway != nil, "consoles_enabled": a.Consoles != nil}
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
		reader := a.may(me, authz.ReadVault, authz.VaultResource())
		var err error
		if out, err = gateways.List(r.Context(), tx, me.ID, !reader); err != nil || reader {
			return err
		}
		// What the gateway logs in with is for admins and auditors.
		for i := range out {
			out[i].VaultAccountID, out[i].VaultAccountName, out[i].CACertificate = "", "", ""
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
		t, err := a.checkConnect(r.Context(), tx, me, id)
		if err != nil {
			return err
		}
		if t.Kind == gateways.RDP || gateways.Cloud(t.Kind) {
			return apperr.Invalidf("%s opens from the console, without a token", t.Name)
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

// checkConnect checks that me may connect to the target id: they hold its
// access right now.
func (a *API) checkConnect(ctx context.Context, tx pgx.Tx, me authz.Subject, id string) (gateways.Target, error) {
	holders, err := gateways.Holders(ctx, tx, id)
	if err != nil {
		return gateways.Target{}, err
	}
	if err := a.check(ctx, authz.Request{Principal: me, Action: authz.ConnectGateway, Resource: authz.GatewayTargetResource(id, holders)}); err != nil {
		return gateways.Target{}, err
	}
	return gateways.Get(ctx, tx, id)
}

// connectDesktop opens a desktop session on a WebSocket, for the console's
// Guacamole client: ?width=&height=&dpi=&timezone= describe the screen.
// sameOrigin says whether r comes from Grantra's own pages, or from no page
// at all (a browser always says which page it comes from).
func (d Desktops) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	return slices.ContainsFunc(d.Origins, func(o string) bool { return strings.EqualFold(o, u.Host) })
}

func (a *API) connectDesktop(w http.ResponseWriter, r *http.Request) {
	var g gateways.Grant
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if a.Desktops.Gateway == nil {
			return apperr.Conflictf("desktops are off: Grantra needs guacd to reach them (set GRANTRA_GUACD_ADDR)")
		}
		if !a.Gateways.Enabled() {
			return gateways.ErrDisabled
		}
		// Only the console's own pages open a desktop: nothing else gets a
		// token issued. The WebSocket handshake checks the origin again.
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || !a.Desktops.sameOrigin(r) {
			return apperr.Invalidf("a desktop opens from Grantra's console, over a WebSocket")
		}
		t, err := a.checkConnect(r.Context(), tx, me, r.PathValue("id"))
		if err != nil {
			return err
		}
		if t.Kind != gateways.RDP {
			return apperr.Invalidf("%s is not a desktop", t.Name)
		}
		// Each session runs on a token of its own, which ends with the
		// access and which ending the session revokes. Nobody sees it.
		tok, err := gateways.Issue(r.Context(), tx, a.tenantID, me.ID, t.ID)
		if err != nil {
			return err
		}
		g, err = gateways.Authorize(r.Context(), tx, tok.Token, t.Kind, t.Name)
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	a.Desktops.Gateway.ServeRDP(a.Desktops.Context, w, r, g, a.Desktops.Origins)
}

// openConsole signs the caller in to a cloud console target they hold
// access to: the URL to open, and on AWS credentials for the CLI.
func (a *API) openConsole(w http.ResponseWriter, r *http.Request) {
	if a.Consoles == nil {
		a.fail(w, r, apperr.Conflictf("cloud consoles are off on this Grantra"))
		return
	}
	var me authz.Subject
	err := a.inTenant(r, func(tx pgx.Tx, s authz.Subject) error {
		me = s
		_, err := a.checkConnect(r.Context(), tx, s, r.PathValue("id"))
		return err
	})
	var out cloud.Opened
	if err == nil {
		out, err = a.Consoles.Open(r.Context(), me.ID, r.PathValue("id"), r.RemoteAddr)
	}
	if err == nil {
		w.Header().Set("Cache-Control", "no-store")
	}
	a.respond(w, r, http.StatusCreated, out, err)
}

// gatewayRecording returns a desktop session's screen recording, in the
// Guacamole protocol, for the console's player. Watching one is audited.
func (a *API) gatewayRecording(w http.ResponseWriter, r *http.Request) {
	a.sendRecording(w, r, "application/octet-stream", gateways.RecordingViewed, gateways.WriteRecording)
}

// sendRecording sends the recording of the session in the path, for those
// who may read the vault: viewed checks the session and audits the
// viewing, committed before the first byte so a download cut short is
// still in the log; write streams it.
func (a *API) sendRecording(w http.ResponseWriter, r *http.Request, contentType string,
	viewed func(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error,
	write func(ctx context.Context, tx pgx.Tx, id string, start func(), w io.Writer) error) {
	id := r.PathValue("id")
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) error {
		return viewed(r.Context(), tx, a.tenantID, me.Actor(), id)
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	started := false
	err = a.inTenant(r, func(tx pgx.Tx, _ authz.Subject) error {
		return write(r.Context(), tx, id, func() {
			started = true
			w.Header().Set("Content-Type", contentType)
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}, w)
	})
	if err != nil {
		if started {
			// Too late to say so: the client sees a recording cut short.
			a.log.ErrorContext(r.Context(), "could not send a session recording", "session", id, "err", err)
			return
		}
		a.fail(w, r, err)
	}
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
