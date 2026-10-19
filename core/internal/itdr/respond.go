package itdr

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/login"
	"github.com/grantra-dev/grantra/core/internal/notify"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// Responses.
const (
	// None is no automatic response.
	None = "none"
	// RevokeSessions signs the person out everywhere Grantra can reach:
	// the console, Grantra Login, live SSH, database, cluster and desktop
	// sessions, and vault check-outs.
	RevokeSessions = "revoke_sessions"
	// RequireMFA makes Grantra Login ask them for a second factor for a
	// while, whatever access they hold, and signs them out so it asks now.
	RequireMFA = "require_mfa"
	// Suspend stops the identity: no access holds, their Grantra Login
	// accounts are disabled and their sessions end. HR syncs leave a
	// suspension alone until someone reinstates them.
	Suspend = "suspend"
	// Reinstate lifts a suspension and enables again the accounts it
	// disabled.
	Reinstate = "reinstate"
)

// Actor is how the audit log names automatic responses and detections.
const Actor = "system:itdr"

// DefaultMFADays is how long a required second factor lasts by default.
const DefaultMFADays = 7

// Response is something done about a threat to someone.
type Response struct {
	ID         string         `json:"id"`
	IdentityID string         `json:"identity_id"`
	AlertID    string         `json:"alert_id,omitempty"`
	Action     string         `json:"action"`
	Actor      string         `json:"actor"`
	Reason     string         `json:"reason,omitempty"`
	Details    map[string]any `json:"details"`
	// Error says what could not be done, such as Grantra Login being
	// down; everything else was.
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func responses(ctx context.Context, tx pgx.Tx, where string, args ...any) ([]Response, error) {
	rows, err := tx.Query(ctx, `SELECT r.id, r.identity_id, coalesce(r.alert_id::text, ''), r.action, r.actor,
		coalesce(r.reason, ''), r.details, coalesce(r.error, ''), r.created_at
		FROM threat_responses r WHERE `+where+` ORDER BY r.created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Response, error) {
		var r Response
		err := row.Scan(&r.ID, &r.IdentityID, &r.AlertID, &r.Action, &r.Actor, &r.Reason, &r.Details, &r.Error, &r.CreatedAt)
		return r, err
	})
}

// IdentityResponses lists what was done about threats to someone, newest
// first.
func IdentityResponses(ctx context.Context, tx pgx.Tx, identityID string) ([]Response, error) {
	if !db.ValidUUID(identityID) {
		return nil, apperr.ErrNotFound
	}
	return responses(ctx, tx, `r.identity_id = $1`, identityID)
}

// ResponseInput asks for a response.
type ResponseInput struct {
	Action string `json:"action"`
	// AlertID is the alert it answers, if any.
	AlertID string `json:"alert_id"`
	Reason  string `json:"reason"`
	// Days is how long RequireMFA lasts, 1 to 90 (default 7).
	Days int `json:"days"`
}

// Responder takes responses: in the database, and in Grantra Login.
type Responder struct {
	Pool     *pgxpool.Pool
	TenantID string
	Secrets  connector.SecretResolver
}

// loginTarget is Grantra Login and someone's accounts in it.
type loginTarget struct {
	client   *grantralogin.Client
	accounts []string
}

// openLogin returns Grantra Login and identityID's accounts in it; a nil
// client when Grantra Login is not connected.
func (rs Responder) openLogin(ctx context.Context, tx pgx.Tx, identityID string) (loginTarget, error) {
	app, c, err := login.Open(ctx, tx, rs.Secrets)
	if errors.Is(err, login.ErrNotConnected) {
		return loginTarget{}, nil
	}
	if err != nil {
		return loginTarget{}, err
	}
	rows, err := tx.Query(ctx,
		`SELECT native_id FROM accounts WHERE application_id = $1 AND identity_id = $2 AND status <> 'deleted' ORDER BY native_id`,
		app.ID, identityID)
	if err != nil {
		return loginTarget{}, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	return loginTarget{client: c, accounts: ids}, err
}

// Respond acts on a threat to identityID, as actor. The database part
// happens first and always; what Grantra Login could not do is in the
// response's error.
func (rs Responder) Respond(ctx context.Context, actor, identityID string, in ResponseInput) (Response, error) {
	switch in.Action {
	case RevokeSessions, RequireMFA, Suspend, Reinstate:
	default:
		return Response{}, apperr.Invalidf("action must be %s, %s, %s or %s", RevokeSessions, RequireMFA, Suspend, Reinstate)
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len(in.Reason) > 2000 {
		return Response{}, apperr.Invalidf("reason must be at most 2000 characters")
	}
	if in.Action == RequireMFA {
		switch {
		case in.Days == 0:
			in.Days = DefaultMFADays
		case in.Days < 1 || in.Days > 90:
			return Response{}, apperr.Invalidf("days must be between 1 and 90")
		}
	} else if in.Days != 0 {
		return Response{}, apperr.Invalidf("days only applies to %s", RequireMFA)
	}
	if !db.ValidUUID(identityID) || (in.AlertID != "" && !db.ValidUUID(in.AlertID)) {
		return Response{}, apperr.ErrNotFound
	}

	// Requiring a second factor only means something when Grantra Login
	// can ask for one: check before changing anything.
	var lt loginTarget
	err := db.InTenant(ctx, rs.Pool, rs.TenantID, func(tx pgx.Tx) (err error) {
		lt, err = rs.openLogin(ctx, tx, identityID)
		return err
	})
	if err != nil {
		return Response{}, err
	}
	mfaMode := ""
	if in.Action == RequireMFA {
		if lt.client == nil {
			return Response{}, apperr.Conflictf("requiring a second factor needs Grantra Login; when people sign in with another provider, require it there")
		}
		p, err := lt.client.Policy(ctx)
		if err != nil {
			return Response{}, fmt.Errorf("read the Grantra Login sign-in policy: %w", err)
		}
		if p.MFA == grantralogin.MFAOptional {
			return Response{}, apperr.Conflictf("Grantra Login's sign-in policy asks nobody for a second factor: set it to %q or %q under Grantra Login first",
				grantralogin.MFASensitive, grantralogin.MFARequired)
		}
		mfaMode = p.MFA
	}

	out := Response{IdentityID: identityID, AlertID: in.AlertID, Action: in.Action, Actor: actor, Reason: in.Reason, Details: map[string]any{}}
	var reenable []string
	err = db.InTenant(ctx, rs.Pool, rs.TenantID, func(tx pgx.Tx) error {
		var status string
		if err := tx.QueryRow(ctx, `SELECT status FROM identities WHERE id = $1 FOR UPDATE`, identityID).Scan(&status); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.ErrNotFound
			}
			return err
		}
		if in.AlertID != "" {
			var about *string
			err := tx.QueryRow(ctx, `SELECT identity_id::text FROM threat_alerts WHERE id = $1`, in.AlertID).Scan(&about)
			if errors.Is(err, pgx.ErrNoRows) {
				return apperr.ErrNotFound
			}
			if err != nil {
				return err
			}
			if about == nil || *about != identityID {
				return apperr.Invalidf("that alert is not about this person")
			}
		}
		switch in.Action {
		case Suspend:
			if status != "active" {
				return apperr.Conflictf("only active people can be suspended; this one is %s", status)
			}
			admins, err := notify.Admins(ctx, tx)
			if err != nil {
				return err
			}
			if slices.Equal(admins, []string{identityID}) {
				return apperr.Conflictf("this is the only active Grantra admin: suspending them would leave nobody to run Grantra. Make someone else an admin first")
			}
			if _, err := tx.Exec(ctx, `UPDATE identities SET status = 'suspended', updated_at = now() WHERE id = $1`, identityID); err != nil {
				return err
			}
			if err := db.Audit(ctx, tx, rs.TenantID, actor, "identity.suspended", "identity", identityID,
				map[string]any{"reason": in.Reason, "alert_id": in.AlertID}); err != nil {
				return err
			}
		case Reinstate:
			if status != "suspended" {
				return apperr.Conflictf("only suspended people can be reinstated; this one is %s", status)
			}
			if _, err := tx.Exec(ctx, `UPDATE identities SET status = 'active', updated_at = now() WHERE id = $1`, identityID); err != nil {
				return err
			}
			// Enable again what the last suspension disabled, and only that:
			// an account someone disabled for another reason stays so.
			var disabled []string
			err := tx.QueryRow(ctx,
				`SELECT coalesce(ARRAY(SELECT jsonb_array_elements_text(details->'disabled_accounts')), '{}')
				 FROM threat_responses WHERE identity_id = $1 AND action = 'suspend' ORDER BY created_at DESC LIMIT 1`,
				identityID).Scan(&disabled)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			reenable = disabled
			if err := db.Audit(ctx, tx, rs.TenantID, actor, "identity.reinstated", "identity", identityID,
				map[string]any{"reason": in.Reason}); err != nil {
				return err
			}
		case RequireMFA:
			var until time.Time
			if err := tx.QueryRow(ctx,
				`INSERT INTO mfa_overrides (tenant_id, identity_id, until, created_by)
				 VALUES ($1, $2, now() + make_interval(days => $3), $4)
				 ON CONFLICT (tenant_id, identity_id) DO UPDATE SET until = greatest(mfa_overrides.until, EXCLUDED.until),
				   created_by = EXCLUDED.created_by, created_at = now()
				 RETURNING until`, rs.TenantID, identityID, in.Days, actor).Scan(&until); err != nil {
				return err
			}
			out.Details["mfa_until"] = until
		}
		if in.Action != Reinstate {
			ended, err := endSessions(ctx, tx, rs.TenantID, actor, identityID)
			if err != nil {
				return err
			}
			for k, v := range ended {
				out.Details[k] = v
			}
		}
		return tx.QueryRow(ctx,
			`INSERT INTO threat_responses (tenant_id, identity_id, alert_id, action, actor, reason, details)
			 VALUES ($1, $2, $3::uuid, $4, $5, nullif($6, ''), $7) RETURNING id, created_at`,
			rs.TenantID, identityID, db.Nullable(in.AlertID), in.Action, actor, in.Reason, out.Details).Scan(&out.ID, &out.CreatedAt)
	})
	if err != nil {
		return Response{}, err
	}

	// Grantra Login, outside any transaction.
	var problems []string
	if lt.client != nil {
		out.Details["grantra_login_accounts"] = len(lt.accounts)
		var disabled []string
		for _, acc := range lt.accounts {
			var err error
			switch in.Action {
			case RevokeSessions:
				err = lt.client.Logout(ctx, acc)
			case RequireMFA:
				if mfaMode == grantralogin.MFASensitive {
					err = lt.client.AddMember(ctx, "role:"+grantralogin.MFARole, acc)
				}
				if err == nil {
					err = lt.client.Logout(ctx, acc)
				}
			case Suspend:
				if err = lt.client.SetActive(ctx, acc, false); err == nil {
					disabled = append(disabled, acc)
				}
			case Reinstate:
				if slices.Contains(reenable, acc) {
					err = lt.client.SetActive(ctx, acc, true)
				}
			}
			if err != nil {
				problems = append(problems, fmt.Sprintf("Grantra Login account %s: %v", acc, err))
			}
		}
		if in.Action == Suspend {
			out.Details["disabled_accounts"] = httpjson.NonNil(disabled)
		}
	}
	out.Error = strings.Join(problems, "; ")
	err = db.InTenant(ctx, rs.Pool, rs.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE threat_responses SET details = $2, error = nullif($3, '') WHERE id = $1`,
			out.ID, out.Details, out.Error); err != nil {
			return err
		}
		return db.Audit(ctx, tx, rs.TenantID, actor, "itdr.response", "identity", identityID, map[string]any{
			"action": in.Action, "alert_id": in.AlertID, "details": out.Details, "error": out.Error})
	})
	return out, err
}

// endSessions ends identityID's sessions in Grantra: console sign-ins,
// live gateway sessions and vault check-outs. It returns how many of each.
func endSessions(ctx context.Context, tx pgx.Tx, tenantID, actor, identityID string) (map[string]any, error) {
	tag, err := tx.Exec(ctx, `DELETE FROM sessions WHERE identity_id = $1`, identityID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"console_sessions": tag.RowsAffected()}

	ids := func(sql string) ([]string, error) {
		rows, err := tx.Query(ctx, sql, identityID)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, pgx.RowTo[string])
	}
	ssh, err := ids(`SELECT id::text FROM ssh_sessions WHERE identity_id = $1 AND ended_at IS NULL AND terminate_requested_by IS NULL`)
	if err != nil {
		return nil, err
	}
	for _, id := range ssh {
		if err := sshaccess.Terminate(ctx, tx, tenantID, actor, id); err != nil {
			return nil, err
		}
	}
	// AWS console sessions cannot be cut short; they end with their
	// credentials, within the hour.
	gw, err := ids(`SELECT id::text FROM gateway_sessions WHERE identity_id = $1 AND ended_at IS NULL
		AND terminate_requested_by IS NULL AND kind <> '` + gateways.AWS + `'`)
	if err != nil {
		return nil, err
	}
	for _, id := range gw {
		if err := gateways.Terminate(ctx, tx, tenantID, actor, id); err != nil {
			return nil, err
		}
	}
	var aws int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM gateway_sessions WHERE identity_id = $1 AND ended_at IS NULL AND kind = $2`,
		identityID, gateways.AWS).Scan(&aws); err != nil {
		return nil, err
	}
	checkouts, err := ids(`SELECT id::text FROM vault_checkouts WHERE identity_id = $1 AND ended_at IS NULL`)
	if err != nil {
		return nil, err
	}
	for _, id := range checkouts {
		if err := vault.Checkin(ctx, tx, tenantID, actor, id, "ended"); err != nil {
			return nil, err
		}
	}
	out["ssh_sessions"] = len(ssh)
	out["gateway_sessions"] = len(gw)
	out["vault_checkouts"] = len(checkouts)
	if aws > 0 {
		out["aws_sessions_left"] = aws
	}
	return out, nil
}
