package itdr

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/login"
	"github.com/grantra-dev/grantra/core/internal/textutil"
)

// loginCursor is where reading Grantra Login's events got to: the time of
// the newest event read, in Unix milliseconds.
const loginCursor = "grantra-login-events"

// firstRead is how far back the first read of Grantra Login's events goes.
const firstRead = 24 * time.Hour

// loginErrors are the Keycloak sign-in errors that are someone trying a
// password (or a second factor): the rest are expired pages, wrong
// redirect URIs and the like, which say nothing about an attack.
var loginErrors = map[string]bool{
	"invalid_user_credentials":  true,
	"user_not_found":            true,
	"user_disabled":             true,
	"user_temporarily_disabled": true,
}

// signInFromEvent turns a Keycloak event into a sign-in; false when it is
// not a sign-in attempt.
func signInFromEvent(ev grantralogin.Event) (SignIn, bool) {
	s := SignIn{
		ID:          ev.ID,
		Time:        time.UnixMilli(ev.Time).UTC(),
		User:        textutil.Clip(ev.Details["username"], 320),
		IP:          ev.IPAddress,
		Application: textutil.Clip(ev.ClientID, 200),
	}
	switch ev.Type {
	case "LOGIN":
		s.Outcome = Success
	case "LOGIN_ERROR":
		if !loginErrors[ev.Error] {
			return SignIn{}, false
		}
		s.Outcome = Failure
		// Keycloak reports a wrong second factor as invalid credentials
		// too; what tells it from a wrong password is that the person had
		// already picked a credential to use (seen on Keycloak 26.4).
		if ev.Error == "invalid_user_credentials" && (ev.Details["selected_credential_id"] != "" ||
			strings.HasPrefix(ev.Details["credential_type"], "otp") || strings.HasPrefix(ev.Details["credential_type"], "webauthn")) {
			s.Outcome = MFAFailure
		}
	default:
		return SignIn{}, false
	}
	if s.ID == "" {
		// Older Keycloaks have no event ids: the event's own facts make one.
		h := sha256.Sum256([]byte(strings.Join([]string{strconv.FormatInt(ev.Time, 10), ev.Type, ev.UserID,
			ev.SessionID, ev.IPAddress, ev.ClientID, ev.Error, ev.Details["username"]}, "\x00")))
		s.ID = hex.EncodeToString(h[:16])
	}
	if s.IP != "" {
		if _, err := netip.ParseAddr(s.IP); err != nil {
			s.IP = ""
		}
	}
	return s, true
}

// ReadGrantraLogin reads Grantra Login's new sign-in events and stores
// them, turning on Keycloak's event store first if it is off. It answers
// login.ErrNotConnected when no application uses the grantra-login
// connector.
func ReadGrantraLogin(ctx context.Context, pool *pgxpool.Pool, tenantID string, secrets connector.SecretResolver) (int, error) {
	var (
		app    login.App
		c      *grantralogin.Client
		cursor string
	)
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) (err error) {
		if app, c, err = login.Open(ctx, tx, secrets); err != nil {
			return err
		}
		cursor, err = getCursor(ctx, tx, loginCursor)
		return err
	})
	if err != nil {
		return 0, err
	}
	from := time.Now().Add(-firstRead).UnixMilli()
	if cursor != "" {
		if from, err = strconv.ParseInt(cursor, 10, 64); err != nil {
			return 0, err
		}
	}

	if err := c.EnsureEvents(ctx); err != nil {
		return 0, err
	}
	var (
		events []SignIn
		users  = map[string]bool{}
		latest = from
	)
	native := map[int]string{} // index in events -> Keycloak user id
	err = c.Events(ctx, from, func(ev grantralogin.Event) {
		latest = max(latest, ev.Time)
		s, ok := signInFromEvent(ev)
		if !ok {
			return
		}
		if ev.UserID != "" {
			native[len(events)] = ev.UserID
			users[ev.UserID] = true
		}
		events = append(events, s)
	})
	if err != nil {
		return 0, err
	}

	var n int
	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if len(events) > 0 {
			// Sign-ins land on the identity whose Grantra Login account it is.
			ids := make([]string, 0, len(users))
			for u := range users {
				ids = append(ids, u)
			}
			rows, err := tx.Query(ctx,
				`SELECT native_id, coalesce(identity_id::text, ''), coalesce(username, '') FROM accounts
				 WHERE application_id = $1 AND native_id = ANY($2)`, app.ID, ids)
			if err != nil {
				return err
			}
			type owner struct{ identity, username string }
			owners := map[string]owner{}
			for rows.Next() {
				var nid string
				var o owner
				if err := rows.Scan(&nid, &o.identity, &o.username); err != nil {
					rows.Close()
					return err
				}
				owners[nid] = o
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return err
			}
			for i := range events {
				o, ok := owners[native[i]]
				if !ok {
					continue
				}
				events[i].identityID = o.identity
				if events[i].User == "" {
					// A wrong second factor carries no username.
					events[i].User = o.username
				}
			}
			for start := 0; start < len(events); start += MaxBatch {
				added, err := store(ctx, tx, tenantID, SourceGrantraLogin, events[start:min(start+MaxBatch, len(events))])
				if err != nil {
					return err
				}
				n += added
			}
		}
		return setCursor(ctx, tx, tenantID, loginCursor, strconv.FormatInt(latest, 10))
	})
	return n, err
}

// getCursor returns where reading name got to; "" the first time.
func getCursor(ctx context.Context, tx pgx.Tx, name string) (string, error) {
	var pos string
	err := tx.QueryRow(ctx, `SELECT position FROM itdr_cursors WHERE name = $1`, name).Scan(&pos)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return pos, err
}

func setCursor(ctx context.Context, tx pgx.Tx, tenantID, name, pos string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO itdr_cursors (tenant_id, name, position) VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, name) DO UPDATE SET position = EXCLUDED.position, updated_at = now()`, tenantID, name, pos)
	return err
}
