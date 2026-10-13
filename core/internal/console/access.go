package console

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// decodeBody reads a small JSON body into v, refusing unknown fields. It
// answers 400 itself and returns false when the body is unusable.
func decodeBody(w http.ResponseWriter, r *http.Request, v any, example string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "body must be JSON like "+example)
		return false
	}
	return true
}

// guarded runs fn in a tenant transaction once the policies allow action on
// the catalog.
func (a *API) guarded(r *http.Request, action authz.Action, fn func(tx pgx.Tx, me authz.Subject) error) error {
	return a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: action, Resource: authz.CatalogResource()}); err != nil {
			return err
		}
		return fn(tx, me)
	})
}

func (a *API) respond(w http.ResponseWriter, r *http.Request, status int, out any, err error) {
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, status, out)
}

func (a *API) listApplications(w http.ResponseWriter, r *http.Request) {
	var out []catalog.Application
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = catalog.ListApplications(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"applications": nonNil(out)}, err)
}

func (a *API) createApplication(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string `json:"name"`
		OwnerID string `json:"owner_id"`
	}
	if !decodeBody(w, r, &body, `{"name": "CRM", "owner_id": "<identity id>"}`) {
		return
	}
	var out catalog.Application
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = catalog.CreateApplication(r.Context(), tx, a.tenantID, body.Name, body.OwnerID); err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, "identity:"+me.ID, "application.created", "application", out.ID,
			map[string]any{"name": out.Name, "owner_id": out.OwnerID})
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

func (a *API) listEntitlements(w http.ResponseWriter, r *http.Request) {
	var out []catalog.Entitlement
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = catalog.ListEntitlements(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"entitlements": nonNil(out)}, err)
}

func (a *API) createEntitlement(w http.ResponseWriter, r *http.Request) {
	var body catalog.EntitlementInput
	if !decodeBody(w, r, &body, `{"name": "Sales reps", "native_id": "cn=sales,ou=groups", "risk": "low"}`) {
		return
	}
	var out catalog.Entitlement
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		if out, err = catalog.CreateEntitlement(r.Context(), tx, a.tenantID, r.PathValue("id"), body); err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, "identity:"+me.ID, "entitlement.created", "entitlement", out.ID,
			map[string]any{"application_id": out.ApplicationID, "name": out.Name, "native_id": out.NativeID, "risk": out.Risk})
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

func (a *API) listRoles(w http.ResponseWriter, r *http.Request) {
	var out []catalog.Role
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = catalog.ListRoles(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"roles": nonNil(out)}, err)
}

func (a *API) getRole(w http.ResponseWriter, r *http.Request) {
	var out catalog.Role
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = catalog.GetRole(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

const roleExample = `{"name": "Sales rep", "entitlement_ids": ["<entitlement id>"]}`

// saveRole creates a role (POST) or replaces one (PUT /roles/{id}).
func (a *API) saveRole(w http.ResponseWriter, r *http.Request) {
	var body catalog.RoleInput
	if !decodeBody(w, r, &body, roleExample) {
		return
	}
	id := r.PathValue("id")
	var out catalog.Role
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		action := "role.created"
		if id == "" {
			out, err = catalog.CreateRole(r.Context(), tx, a.tenantID, body)
		} else {
			action = "role.updated"
			out, err = catalog.UpdateRole(r.Context(), tx, a.tenantID, id, body)
		}
		if err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, "identity:"+me.ID, action, "role", out.ID,
			map[string]any{"name": out.Name, "owner_id": out.OwnerID, "entitlement_ids": nonNil(body.EntitlementIDs)})
	})
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	a.respond(w, r, status, out, err)
}

func (a *API) listAccessRules(w http.ResponseWriter, r *http.Request) {
	var out []access.Rule
	err := a.guarded(r, authz.ReadAccessRules, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = access.ListRules(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"rules": nonNil(out)}, err)
}

func (a *API) getAccessRule(w http.ResponseWriter, r *http.Request) {
	var out access.Rule
	err := a.guarded(r, authz.ReadAccessRules, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = access.GetRule(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

type ruleChange struct {
	access.Rule
	// Changes is what applying the rule did to people's access.
	Changes access.Changes `json:"changes"`
}

const ruleExample = `{"name": "Sales", "conditions": {"department": ["Sales"]}, "role_ids": ["<role id>"]}`

// saveAccessRule creates a rule (POST) or replaces one (PUT /access-rules/{id})
// and applies it to everyone at once, in the same transaction.
func (a *API) saveAccessRule(w http.ResponseWriter, r *http.Request) {
	var body access.RuleInput
	if !decodeBody(w, r, &body, ruleExample) {
		return
	}
	id := r.PathValue("id")
	var out ruleChange
	err := a.guarded(r, authz.ManageAccessRules, func(tx pgx.Tx, me authz.Subject) (err error) {
		actor := "identity:" + me.ID
		action := "access_rule.created"
		if id == "" {
			out.Rule, err = access.CreateRule(r.Context(), tx, a.tenantID, body)
		} else {
			action = "access_rule.updated"
			out.Rule, err = access.UpdateRule(r.Context(), tx, a.tenantID, id, body)
		}
		if err != nil {
			return err
		}
		if err := db.Audit(r.Context(), tx, a.tenantID, actor, action, "access_rule", out.ID,
			map[string]any{"name": out.Name, "conditions": out.Conditions, "enabled": out.Enabled,
				"role_ids": nonNil(body.RoleIDs), "entitlement_ids": nonNil(body.EntitlementIDs)}); err != nil {
			return err
		}
		if out.Changes, err = access.Reconcile(r.Context(), tx, a.tenantID, actor, nil); err != nil {
			return err
		}
		out.Rule, err = access.GetRule(r.Context(), tx, out.ID)
		return err
	})
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	a.respond(w, r, status, out, err)
}

func (a *API) deleteAccessRule(w http.ResponseWriter, r *http.Request) {
	var out access.Changes
	err := a.guarded(r, authz.ManageAccessRules, func(tx pgx.Tx, me authz.Subject) (err error) {
		actor := "identity:" + me.ID
		rule, err := access.GetRule(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if out, err = access.DeleteRule(r.Context(), tx, a.tenantID, actor, rule.ID); err != nil {
			return err
		}
		return db.Audit(r.Context(), tx, a.tenantID, actor, "access_rule.deleted", "access_rule", rule.ID,
			map[string]any{"name": rule.Name, "removed": out.Removed})
	})
	a.respond(w, r, http.StatusOK, map[string]any{"changes": out}, err)
}

// previewAccessRule shows who a set of conditions would select, before saving
// a rule: {"conditions": {"department": ["Sales"]}}.
func (a *API) previewAccessRule(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Conditions access.Conditions `json:"conditions"`
	}
	if !decodeBody(w, r, &body, `{"conditions": {"department": ["Sales"]}}`) {
		return
	}
	var (
		n      int
		sample []access.Match
	)
	err := a.guarded(r, authz.ReadAccessRules, func(tx pgx.Tx, _ authz.Subject) (err error) {
		n, sample, err = access.Preview(r.Context(), tx, body.Conditions, 50)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"matches": n, "sample": sample}, err)
}

// identityAccess lists what an identity should have and why. Whoever may see
// the identity may see its access: the person, their manager, helpdesk,
// auditors and admins.
func (a *API) identityAccess(w http.ResponseWriter, r *http.Request) {
	var out []access.Assignment
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		target, err := authz.LoadSubject(r.Context(), tx, r.PathValue("id"))
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.ReadIdentity, Resource: authz.IdentityResource(target)}); err != nil {
			return err
		}
		out, err = access.IdentityAccess(r.Context(), tx, target.ID)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"assignments": nonNil(out)}, err)
}
