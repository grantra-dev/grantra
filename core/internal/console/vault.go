package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// listVaultAccounts shows admins and auditors every vault account, and
// everyone else the ones they hold access to. Only vault readers see why
// other people checked an account out.
func (a *API) listVaultAccounts(w http.ResponseWriter, r *http.Request) {
	var out []vault.Account
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		reader := a.authz.Authorize(authz.Request{Principal: me, Action: authz.ReadVault, Resource: authz.VaultResource()}).Allowed
		held, err := vault.List(r.Context(), tx, me.ID)
		if err != nil {
			return err
		}
		out = held
		if reader {
			if out, err = vault.List(r.Context(), tx, ""); err != nil {
				return err
			}
		}
		mine := map[string]bool{}
		for _, h := range held {
			mine[h.ID] = true
		}
		for i := range out {
			out[i].Held = mine[out[i].ID]
		}
		if !reader {
			for i := range out {
				for j := range out[i].Checkouts {
					if out[i].Checkouts[j].IdentityID != me.ID {
						out[i].Checkouts[j].Reason = ""
					}
				}
			}
		}
		return nil
	})
	a.respond(w, r, http.StatusOK, map[string]any{"enabled": a.Vault.Enabled(), "accounts": nonNil(out)}, err)
}

func (a *API) createVaultAccount(w http.ResponseWriter, r *http.Request) {
	var in vault.Input
	if !decodeBody(w, r, &in, `{"name": "prod-db root", "username": "root", "kind": "password", "secret": "..."}`) {
		return
	}
	var out vault.Account
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = a.Vault.Create(r.Context(), tx, a.tenantID, me.Actor(), in)
		return err
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

type vaultAccountDetail struct {
	vault.Account
	vault.History
}

func (a *API) getVaultAccount(w http.ResponseWriter, r *http.Request) {
	var out vaultAccountDetail
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		if out.Account, err = vault.Get(r.Context(), tx, r.PathValue("id")); err != nil {
			return err
		}
		out.History, err = vault.GetHistory(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) updateVaultAccount(w http.ResponseWriter, r *http.Request) {
	var in vault.Settings
	if !decodeBody(w, r, &in, `{"checkout_minutes": 60, "exclusive": true, "rotate_every_days": 30, "rotate_after_checkin": true}`) {
		return
	}
	var out vault.Account
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = a.Vault.Update(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), in)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) deleteVaultAccount(w http.ResponseWriter, r *http.Request) {
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) error {
		return vault.Delete(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

// setVaultSecret stores a value an admin typed. Admins write secrets but do
// not read them back.
func (a *API) setVaultSecret(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Secret string `json:"secret"`
	}
	if !decodeBodyMax(w, r, &in, 64<<10, `{"secret": "..."}`) {
		return
	}
	err := a.guardedOn(r, authz.ManageVault, authz.VaultResource(), func(tx pgx.Tx, me authz.Subject) error {
		return a.Vault.SetSecret(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), in.Secret)
	})
	a.noContent(w, r, err)
}

// rotateVaultAccount changes the password in the target system now. It
// talks to the target, so it runs outside the console transaction.
func (a *API) rotateVaultAccount(w http.ResponseWriter, r *http.Request) {
	me, err := a.allowed(r, authz.ManageVault, authz.VaultResource())
	if err != nil {
		a.fail(w, r, err)
		return
	}
	out, err := a.Vault.Rotate(r.Context(), a.pool, a.tenantID, me.Actor(), r.PathValue("id"))
	a.respond(w, r, http.StatusOK, out, err)
}

// checkoutVaultAccount reveals a secret to someone holding access to it.
func (a *API) checkoutVaultAccount(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Reason string `json:"reason"`
	}
	if !decodeBodyMax(w, r, &in, 8<<10, `{"reason": "INC-1234: restart the replica"}`) {
		return
	}
	var out vault.Lease
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		id := r.PathValue("id")
		holders, err := vault.Holders(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.CheckoutVault, Resource: authz.VaultAccountResource(id, holders)}); err != nil {
			return err
		}
		out, err = a.Vault.Checkout(r.Context(), tx, a.tenantID, me.ID, id, in.Reason)
		return err
	})
	w.Header().Set("Cache-Control", "no-store")
	a.respond(w, r, http.StatusCreated, out, err)
}

// checkinVault ends a check-out: its owner checks it in, a vault admin can
// end anyone's.
func (a *API) checkinVault(w http.ResponseWriter, r *http.Request) {
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		owner, _, err := vault.CheckoutOwner(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		endedBy := "checked_in"
		if owner != me.ID {
			if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ManageVault, Resource: authz.VaultResource()}); err != nil {
				return err
			}
			endedBy = "ended"
		}
		return vault.Checkin(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), endedBy)
	})
	a.noContent(w, r, err)
}

// discoverVaultAccounts lists privileged, orphaned and service accounts the
// applications hold: ?limit= (default 50, at most 200).
func (a *API) discoverVaultAccounts(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var out []vault.Finding
	err := a.guardedOn(r, authz.ReadVault, authz.VaultResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = vault.Discover(r.Context(), tx, limit)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"findings": nonNil(out)}, err)
}
