package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// settingsPage is Settings → General: every setting as the tenant has it,
// and whether a change that makes Grantra less strict waits for a second
// admin (it does when the tenant has another active admin).
type settingsPage struct {
	Settings    []settings.Setting `json:"settings"`
	SecondAdmin bool               `json:"second_admin"`
}

func (a *API) listSettings(w http.ResponseWriter, r *http.Request) {
	var out settingsPage
	err := a.guardedOn(r, authz.ReadSettings, authz.SettingsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		if out.Settings, err = settings.Load(r.Context(), tx); err != nil {
			return err
		}
		out.SecondAdmin, err = settings.SecondAdmin(r.Context(), tx, me.Actor())
		return err
	})
	out.Settings = httpjson.NonNil(out.Settings)
	a.respond(w, r, http.StatusOK, out, err)
}

// saveSetting changes one setting: {"value": 90, "reason": "..."}. A change
// that waits for a second admin answers 202 with the change in pending.
func (a *API) saveSetting(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Value  *int   `json:"value"`
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body, `{"value": 90, "reason": "why"}`) {
		return
	}
	var out settings.Setting
	err := a.guardedOn(r, authz.ManageSettings, authz.SettingsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		if body.Value == nil {
			return apperr.Invalidf("say the new value")
		}
		out, err = settings.Save(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("key"), *body.Value, body.Reason)
		return err
	})
	status := http.StatusOK
	if out.Pending != nil {
		status = http.StatusAccepted
	}
	a.respond(w, r, status, out, err)
}

// decideSettingChange approves or rejects a change another admin proposed,
// or withdraws one's own: {"approve": true, "comment": "..."}.
func (a *API) decideSettingChange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Approve *bool  `json:"approve"`
		Comment string `json:"comment"`
	}
	if !decodeBody(w, r, &body, `{"approve": true, "comment": "..."}`) {
		return
	}
	var out settings.Change
	err := a.guardedOn(r, authz.ManageSettings, authz.SettingsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		if body.Approve == nil {
			return apperr.Invalidf("say whether the change is approved")
		}
		out, err = settings.Decide(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), *body.Approve, body.Comment)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}
