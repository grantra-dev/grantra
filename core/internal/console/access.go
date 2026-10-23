package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/access"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
)

func (a *API) listApplications(w http.ResponseWriter, r *http.Request) {
	var out []catalog.Application
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = catalog.ListApplications(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"applications": httpjson.NonNil(out)}, err)
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
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "application.created", "application", out.ID,
			map[string]any{"name": out.Name, "owner_id": out.OwnerID})
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

// updateApplication replaces an application's name, description, owner
// and read schedule.
func (a *API) updateApplication(w http.ResponseWriter, r *http.Request) {
	var body catalog.ApplicationInput
	if !decodeBody(w, r, &body, `{"name": "CRM", "description": "Sales pipeline", "owner_id": "<identity id>", "read_every_hours": 1}`) {
		return
	}
	var out catalog.Application
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = catalog.UpdateApplication(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), body)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// deleteApplication removes an application without history; the others are
// retired instead.
func (a *API) deleteApplication(w http.ResponseWriter, r *http.Request) {
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) error {
		return catalog.DeleteApplication(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

// retireApplication takes an application out of service, {"retired": true},
// or brings it back, {"retired": false}.
func (a *API) retireApplication(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Retired bool `json:"retired"`
	}
	if !decodeBody(w, r, &body, `{"retired": true}`) {
		return
	}
	var out catalog.Application
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = catalog.Retire(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), body.Retired)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) listEntitlements(w http.ResponseWriter, r *http.Request) {
	var out []catalog.Entitlement
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = catalog.ListEntitlements(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"entitlements": httpjson.NonNil(out)}, err)
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
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "entitlement.created", "entitlement", out.ID,
			map[string]any{"application_id": out.ApplicationID, "name": out.Name, "native_id": out.NativeID, "risk": out.Risk})
	})
	a.respond(w, r, http.StatusCreated, out, err)
}

// updateEntitlement changes an entitlement's name, description and risk.
func (a *API) updateEntitlement(w http.ResponseWriter, r *http.Request) {
	var body catalog.EntitlementChange
	if !decodeBody(w, r, &body, `{"name": "Sales reps", "description": "Can see every deal", "risk": "medium"}`) {
		return
	}
	var out catalog.Entitlement
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = catalog.UpdateEntitlement(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), body)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// deleteEntitlement removes an entitlement added by hand and never used.
func (a *API) deleteEntitlement(w http.ResponseWriter, r *http.Request) {
	err := a.guarded(r, authz.ManageCatalog, func(tx pgx.Tx, me authz.Subject) error {
		return catalog.DeleteEntitlement(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

func (a *API) listRoles(w http.ResponseWriter, r *http.Request) {
	var out []catalog.Role
	err := a.guarded(r, authz.ReadCatalog, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = catalog.ListRoles(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"roles": httpjson.NonNil(out)}, err)
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
		return db.Audit(r.Context(), tx, a.tenantID, me.Actor(), action, "role", out.ID,
			map[string]any{"name": out.Name, "owner_id": out.OwnerID, "entitlement_ids": httpjson.NonNil(body.EntitlementIDs)})
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
	a.respond(w, r, http.StatusOK, map[string]any{"rules": httpjson.NonNil(out)}, err)
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
		actor := me.Actor()
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
				"role_ids": httpjson.NonNil(body.RoleIDs), "entitlement_ids": httpjson.NonNil(body.EntitlementIDs)}); err != nil {
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
		actor := me.Actor()
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

// listAttributes lists the HR attributes people have and their common
// values, for writing a rule's conditions.
func (a *API) listAttributes(w http.ResponseWriter, r *http.Request) {
	var out []access.Attribute
	err := a.guarded(r, authz.ReadAccessRules, func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = access.Attributes(r.Context(), tx, 50)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"attributes": httpjson.NonNil(out)}, err)
}

// identityAccess lists what an identity should have and why. Whoever may see
// the identity may see its access: the person, their manager, helpdesk,
// auditors and admins.
func (a *API) identityAccess(w http.ResponseWriter, r *http.Request) {
	var out []access.Assignment
	err := a.guardedOnIdentity(r, authz.ReadIdentity, func(tx pgx.Tx, _, target authz.Subject) (err error) {
		out, err = access.IdentityAccess(r.Context(), tx, target.ID)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"assignments": httpjson.NonNil(out)}, err)
}
