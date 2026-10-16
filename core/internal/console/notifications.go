package console

import (
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/notify"
)

type notificationSettings struct {
	// EmailEnabled says whether this install sends email at all, and
	// HasEmail whether the person has an address to send to.
	EmailEnabled bool             `json:"email_enabled"`
	HasEmail     bool             `json:"has_email"`
	Kinds        []notify.Setting `json:"kinds"`
}

// Everyone manages their own notifications, so these need no policy.
func (a *API) notificationSettings(tx pgx.Tx, r *http.Request, me authz.Subject) (notificationSettings, error) {
	out := notificationSettings{EmailEnabled: a.EmailEnabled}
	if err := tx.QueryRow(r.Context(),
		`SELECT coalesce(email, '') <> '' FROM identities WHERE id = $1`, me.ID).Scan(&out.HasEmail); err != nil {
		return out, err
	}
	var err error
	out.Kinds, err = notify.Settings(r.Context(), tx, me.ID)
	return out, err
}

func (a *API) getNotifications(w http.ResponseWriter, r *http.Request) {
	var out notificationSettings
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		var err error
		out, err = a.notificationSettings(tx, r, me)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) setNotification(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeBody(w, r, &body, `{"enabled": false}`) {
		return
	}
	if body.Enabled == nil {
		httpjson.Error(w, http.StatusBadRequest, "enabled is required")
		return
	}
	kind := notify.Kind(r.PathValue("kind"))
	var out notificationSettings
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		if err := notify.SetEnabled(r.Context(), tx, a.tenantID, me.ID, kind, *body.Enabled); err != nil {
			return err
		}
		if err := db.Audit(r.Context(), tx, a.tenantID, me.Actor(), "notifications.updated", "identity", me.ID,
			map[string]any{"kind": kind, "enabled": *body.Enabled}); err != nil {
			return err
		}
		var err error
		out, err = a.notificationSettings(tx, r, me)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}
