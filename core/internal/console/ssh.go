package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
)

// SSH targets, sessions and recordings sit with the vault: vault.manage
// manages them and vault.read sees them. Only people holding a target's
// access get certificates for it.

// getSSH says whether certificates can be signed, where the gateway is, and
// the CA public key servers trust.
func (a *API) getSSH(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": a.SSH.Enabled(), "gateway_address": a.GatewayAddress, "ca_public_key": ""}
	err := a.inTenant(r, func(tx pgx.Tx, _ authz.Subject) error {
		if !a.SSH.Enabled() {
			return nil
		}
		ca, err := a.SSH.PublicCA(r.Context(), tx, a.tenantID)
		out["ca_public_key"] = ca
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// listSSHTargets shows admins and auditors every target, and everyone else
// the ones they hold access to.
func (a *API) listSSHTargets(w http.ResponseWriter, r *http.Request) {
	var out []sshaccess.Target
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		reader := a.authz.Authorize(authz.Request{Principal: me, Action: authz.ReadVault, Resource: authz.VaultResource()}).Allowed
		held, err := sshaccess.List(r.Context(), tx, me.ID)
		if err != nil {
			return err
		}
		if !reader {
			out = held
		} else if out, err = sshaccess.List(r.Context(), tx, ""); err != nil {
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

func (a *API) createSSHTarget(w http.ResponseWriter, r *http.Request) {
	var in sshaccess.Input
	if !decodeBody(w, r, &in, `{"name": "prod-db", "login": "postgres", "address": "10.0.0.5:22"}`) {
		return
	}
	var out sshaccess.Target
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = sshaccess.Create(r.Context(), tx, a.tenantID, me.Actor(), in)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

func (a *API) updateSSHTarget(w http.ResponseWriter, r *http.Request) {
	var in sshaccess.Settings
	if !decodeBody(w, r, &in, `{"address": "10.0.0.5:22", "max_certificate_hours": 8, "via_gateway": true}`) {
		return
	}
	var out sshaccess.Target
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = sshaccess.Update(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), in)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) deleteSSHTarget(w http.ResponseWriter, r *http.Request) {
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) error {
		return sshaccess.Delete(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

// issueSSHCertificate signs a certificate for the caller's public key, for a
// target they hold access to.
func (a *API) issueSSHCertificate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		PublicKey string `json:"public_key"`
	}
	if !decodeBodyMax(w, r, &in, 32<<10, `{"public_key": "ssh-ed25519 AAAA... ana@laptop"}`) {
		return
	}
	var out sshaccess.Certificate
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		id := r.PathValue("id")
		holders, err := sshaccess.Holders(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ConnectSSH, Resource: authz.SSHTargetResource(id, holders)}); err != nil {
			return err
		}
		out, err = a.SSH.Issue(r.Context(), tx, a.tenantID, me.ID, id, in.PublicKey)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

// listSSHSessions lists gateway sessions, newest first: ?target=, ?identity=,
// ?active=true and ?limit= (default 50, at most 200).
func (a *API) listSSHSessions(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := sshaccess.SessionFilter{TargetID: q.Get("target"), IdentityID: q.Get("identity"), Active: q.Get("active") == "true", Limit: limit}
	var out []sshaccess.Session
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = sshaccess.ListSessions(r.Context(), tx, f)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"sessions": nonNil(out)}, err)
}

func (a *API) getSSHSession(w http.ResponseWriter, r *http.Request) {
	var out struct {
		sshaccess.Session
		Commands []sshaccess.Command `json:"commands"`
	}
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		if out.Session, err = sshaccess.GetSession(r.Context(), tx, r.PathValue("id")); err != nil {
			return err
		}
		out.Commands, err = sshaccess.SessionCommands(r.Context(), tx, out.ID)
		out.Commands = nonNil(out.Commands)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// sshRecording returns a session's recording as an asciicast v2 file.
// Watching one is audited.
func (a *API) sshRecording(w http.ResponseWriter, r *http.Request) {
	var rec []byte
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		rec, err = sshaccess.Recording(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
		return err
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(rec) //nolint:errcheck
}

func (a *API) terminateSSHSession(w http.ResponseWriter, r *http.Request) {
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) error {
		return sshaccess.Terminate(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

// searchSSHCommands finds commands typed through the gateway: ?q= (any
// case), ?target= and ?limit=.
func (a *API) searchSSHCommands(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var out []sshaccess.Command
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = sshaccess.SearchCommands(r.Context(), tx, r.URL.Query().Get("q"), r.URL.Query().Get("target"), limit)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"commands": nonNil(out)}, err)
}
