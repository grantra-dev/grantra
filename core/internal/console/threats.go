package console

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/itdr"
	"github.com/grantra-dev/grantra/core/internal/servicetoken"
)

func (a *API) scanner() itdr.Scanner {
	return itdr.Scanner{Pool: a.pool, TenantID: a.tenantID, Secrets: a.Secrets.Resolve}
}

// signInSource is where sign-ins come from, and when the last one came.
type signInSource struct {
	Source     string     `json:"source"`
	LastAt     *time.Time `json:"last_at,omitempty"`
	LastDay    int        `json:"last_day"`
	Identified int        `json:"identified"`
}

type threatsOverview struct {
	// Open is how many alerts are open, per severity.
	Open itdr.Counts `json:"open"`
	// Sources are the providers that sent sign-ins in the last 30 days.
	Sources []signInSource `json:"sources"`
	// GrantraLogin says whether Grantra Login's sign-ins are read.
	GrantraLogin bool `json:"grantra_login"`
	// LoginEventTypes are the Keycloak events read from it.
	LoginEventTypes []string `json:"login_event_types"`
	// TokenScope is the scope a script's token needs to send sign-ins.
	TokenScope string `json:"token_scope"`
}

// getThreats answers the top of the Threats page: open alerts and where
// sign-ins come from.
func (a *API) getThreats(w http.ResponseWriter, r *http.Request) {
	out := threatsOverview{LoginEventTypes: grantralogin.SignInEventTypes, TokenScope: servicetoken.ScopeSendSignIns}
	err := a.guardedOn(r, authz.ReadThreats, authz.ThreatsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		ctx := r.Context()
		if out.Open, err = itdr.OpenCounts(ctx, tx); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM applications WHERE connector = $1)`, grantralogin.Kind).
			Scan(&out.GrantraLogin); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT source, max(occurred_at), count(*) FILTER (WHERE occurred_at > now() - interval '1 day'),
			       count(*) FILTER (WHERE occurred_at > now() - interval '1 day' AND identity_id IS NOT NULL)
			FROM signin_events WHERE occurred_at > now() - interval '30 days'
			GROUP BY source ORDER BY source`)
		if err != nil {
			return err
		}
		out.Sources, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (signInSource, error) {
			var s signInSource
			err := row.Scan(&s.Source, &s.LastAt, &s.LastDay, &s.Identified)
			return s, err
		})
		return err
	})
	out.Sources = httpjson.NonNil(out.Sources)
	a.respond(w, r, http.StatusOK, out, err)
}

// listThreatAlerts pages through alerts, most recently seen first:
// ?status=open&severity=&identity=&detection=&limit=&before=<alert id>.
func (a *API) listThreatAlerts(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	identity, ok := identityParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var out []itdr.Alert
	err := a.guardedOn(r, authz.ReadThreats, authz.ThreatsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = itdr.Alerts(r.Context(), tx, itdr.AlertFilter{Status: q.Get("status"), Severity: q.Get("severity"),
			IdentityID: identity, Detection: q.Get("detection"), Before: q.Get("before"), Limit: limit})
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"alerts": httpjson.NonNil(out)}, err)
}

func (a *API) getThreatAlert(w http.ResponseWriter, r *http.Request) {
	var out itdr.Alert
	err := a.guardedOn(r, authz.ReadThreats, authz.ThreatsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = itdr.Get(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// closeThreatAlert resolves or dismisses an alert: {"status": "dismissed",
// "note": "Ana is at the Lisbon office this week"}. Nobody closes an alert
// about themselves.
func (a *API) closeThreatAlert(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Status string `json:"status"`
		Note   string `json:"note"`
	}
	if !decodeBody(w, r, &body, `{"status": "resolved", "note": "password reset, sessions ended"}`) {
		return
	}
	id := r.PathValue("id")
	var out itdr.Alert
	err := a.inTenant(r, func(tx pgx.Tx, me authz.Subject) error {
		about, err := itdr.AlertIdentity(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if err := a.check(r.Context(), authz.Request{Principal: me, Action: authz.CloseThreatAlert,
			Resource: authz.ThreatAlertResource(id, about)}); err != nil {
			return err
		}
		out, err = itdr.Close(r.Context(), tx, a.tenantID, me.Actor(), id, body.Status, body.Note)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) listThreatDetections(w http.ResponseWriter, r *http.Request) {
	var out []itdr.Setting
	err := a.guardedOn(r, authz.ReadThreats, authz.ThreatsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = itdr.Settings(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"detections": httpjson.NonNil(out)}, err)
}

// setThreatDetection changes a detection: {"enabled": true, "severity":
// "high", "auto_response": "require_mfa", "values": {"failures": 20}}.
func (a *API) setThreatDetection(w http.ResponseWriter, r *http.Request) {
	var body itdr.SettingInput
	if !decodeBody(w, r, &body, `{"enabled": true, "auto_response": "none", "values": {"failures": 20}}`) {
		return
	}
	var out itdr.Setting
	err := a.guardedOn(r, authz.ManageThreats, authz.ThreatsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = itdr.SaveSetting(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"), body)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// scanThreats reads new sign-ins and runs the detections now.
func (a *API) scanThreats(w http.ResponseWriter, r *http.Request) {
	if _, err := a.allowed(r, authz.ManageThreats, authz.ThreatsResource()); err != nil {
		a.fail(w, r, err)
		return
	}
	res, err := a.scanner().Scan(r.Context())
	if errors.Is(err, itdr.ErrScanRunning) {
		a.fail(w, r, err)
		return
	}
	if err != nil {
		// What was read and raised stands; say what failed.
		a.log.WarnContext(r.Context(), "threat scan", "err", err)
		a.respond(w, r, http.StatusOK, map[string]any{"result": res, "error": err.Error()}, nil)
		return
	}
	a.respond(w, r, http.StatusOK, map[string]any{"result": res}, nil)
}

// listSignIns pages through sign-ins, newest first:
// ?identity=&outcome=failure&ip=&limit=&before=<event id>.
func (a *API) listSignIns(w http.ResponseWriter, r *http.Request) {
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	before, ok := beforeCursor(w, r)
	if !ok {
		return
	}
	identity, ok := identityParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	var out []itdr.Event
	err := a.guardedOn(r, authz.ReadThreats, authz.ThreatsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = itdr.Events(r.Context(), tx, itdr.EventFilter{IdentityID: identity, Outcome: q.Get("outcome"),
			IP: q.Get("ip"), Before: before, Limit: limit})
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"events": httpjson.NonNil(out)}, err)
}

// receiveSignIns takes sign-ins from another identity provider, from a
// script with a service token: {"source": "okta", "events": [{"id": "...",
// "time": "2026-10-19T08:30:00Z", "user": "ana@example.com", "outcome":
// "success", "ip": "203.0.113.7", "country": "ES"}]}. They are looked at
// within a minute.
func (a *API) receiveSignIns(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string        `json:"source"`
		Events []itdr.SignIn `json:"events"`
	}
	if !decodeBodyMax(w, r, &body, 4<<20, `{"source": "okta", "events": [{"id": "evt-1", "time": "2026-10-19T08:30:00Z", "user": "ana@example.com", "outcome": "success", "ip": "203.0.113.7"}]}`) {
		return
	}
	actor, err := a.caller(r, authz.ManageThreats, authz.ThreatsResource(), servicetoken.ScopeSendSignIns, "")
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var n int
	err = db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) (err error) {
		n, err = itdr.Receive(r.Context(), tx, a.tenantID, actor, body.Source, body.Events)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"received": len(body.Events), "new": n}, err)
}

// respondToThreat acts on a threat to a person: {"action": "suspend",
// "alert_id": "...", "reason": "..."}; "days" says how long a required
// second factor lasts. Nobody responds about themselves.
func (a *API) respondToThreat(w http.ResponseWriter, r *http.Request) {
	var body itdr.ResponseInput
	if !decodeBody(w, r, &body, `{"action": "revoke_sessions", "alert_id": "<alert id>", "reason": "sign-in from an unknown country"}`) {
		return
	}
	var me authz.Subject
	err := a.guardedOnIdentity(r, authz.RespondToThreat, func(_ pgx.Tx, s, _ authz.Subject) error {
		me = s
		return nil
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	rs := itdr.Responder{Pool: a.pool, TenantID: a.tenantID, Secrets: a.Secrets.Resolve}
	out, err := rs.Respond(r.Context(), me.Actor(), r.PathValue("id"), body)
	a.respond(w, r, http.StatusOK, out, err)
}

func (a *API) listThreatResponses(w http.ResponseWriter, r *http.Request) {
	var out []itdr.Response
	err := a.guardedOn(r, authz.ReadThreats, authz.ThreatsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = itdr.IdentityResponses(r.Context(), tx, r.PathValue("id"))
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"responses": httpjson.NonNil(out)}, err)
}
