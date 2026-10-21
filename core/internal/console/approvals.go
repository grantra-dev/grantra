package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/approvals"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/requests"
)

// Approval policies are part of how the tenant has Grantra set up: admins
// change them, auditors read them.

func (a *API) listApprovalPolicies(w http.ResponseWriter, r *http.Request) {
	var out []approvals.Policy
	err := a.guardedOn(r, authz.ReadSettings, authz.SettingsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = approvals.List(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"policies": httpjson.NonNil(out), "builtin": approvals.Builtin}, err)
}

// saveApprovalPolicy creates (POST) or replaces (PUT) a policy:
// {"name": "Payroll", "conditions": {"application_ids": ["..."]}, "steps": [{"approver": "manager"}, {"approver": "people", "people_ids": ["..."], "required": 2}]}.
func (a *API) saveApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	var in approvals.Input
	if !decodeBody(w, r, &in, `{"name": "Payroll", "steps": [{"approver": "manager"}, {"approver": "owner"}]}`) {
		return
	}
	id := r.PathValue("id")
	status := http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	var out approvals.Policy
	err := a.guardedOn(r, authz.ManageSettings, authz.SettingsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = approvals.Save(r.Context(), tx, a.tenantID, me.Actor(), id, in)
		return err
	})
	a.respond(w, r, status, out, err)
}

func (a *API) deleteApprovalPolicy(w http.ResponseWriter, r *http.Request) {
	err := a.guardedOn(r, authz.ManageSettings, authz.SettingsResource(), func(tx pgx.Tx, me authz.Subject) error {
		return approvals.Delete(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

// reorderApprovalPolicies sets the order policies are tried in:
// {"ids": ["<first>", "<second>", ...]}.
func (a *API) reorderApprovalPolicies(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs []string `json:"ids"`
	}
	if !decodeBody(w, r, &body, `{"ids": ["<policy id>", "<policy id>"]}`) {
		return
	}
	var out []approvals.Policy
	err := a.guardedOn(r, authz.ManageSettings, authz.SettingsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		if err := approvals.Reorder(r.Context(), tx, a.tenantID, me.Actor(), body.IDs); err != nil {
			return err
		}
		out, err = approvals.List(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"policies": httpjson.NonNil(out)}, err)
}

// simulateApprovals tells who would approve a request, without making it:
// {"beneficiary_id": "...", "entitlement_id": "...", "kind": "access",
// "duration_days": 30}, with "requester_id" when someone else would ask, and
// "policy" to try a policy being edited before the saved ones.
func (a *API) simulateApprovals(w http.ResponseWriter, r *http.Request) {
	var body struct {
		requests.Input
		RequesterID string           `json:"requester_id"`
		Policy      *approvals.Input `json:"policy"`
	}
	if !decodeBody(w, r, &body, `{"beneficiary_id": "<identity id>", "entitlement_id": "<entitlement id>"}`) {
		return
	}
	if body.BeneficiaryID == "" {
		httpjson.Error(w, http.StatusBadRequest, "beneficiary_id is required: who the access would be for")
		return
	}
	if body.RequesterID == "" {
		body.RequesterID = body.BeneficiaryID
	}
	var (
		plan      approvals.Plan
		conflicts []string
	)
	err := a.guardedOn(r, authz.ReadSettings, authz.SettingsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		var draft *approvals.Policy
		if body.Policy != nil {
			p, err := approvals.Draft(r.Context(), tx, *body.Policy)
			if err != nil {
				return err
			}
			draft = &p
		}
		plan, conflicts, err = requests.Preview(r.Context(), tx, body.RequesterID, body.Input, draft)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"plan": plan, "sod_conflicts": httpjson.NonNil(conflicts)}, err)
}
