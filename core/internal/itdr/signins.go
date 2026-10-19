// Package itdr is identity threat detection and response. It collects
// sign-ins from Grantra Login and from any other identity provider a
// script feeds, looks at each one for signs of an attack (a password
// spray, MFA fatigue, a sign-in from the other side of the world an hour
// after the last one, a leaver signing in), watches for access appearing
// outside Grantra, and raises alerts. Security responds to an alert by
// ending the person's sessions, requiring a second factor or suspending
// them, by hand or automatically.
package itdr

import (
	"context"
	"net/netip"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Sign-in outcomes.
const (
	Success = "success"
	// Failure is a wrong password or an unknown user.
	Failure = "failure"
	// MFAFailure is a second factor refused or wrong: the password was
	// right.
	MFAFailure = "mfa_failure"
)

// Module is the console module of threat detection. While a tenant has it
// off, scans do nothing.
const Module = "itdr"

// On reports whether the tenant has threat detection on.
func On(ctx context.Context, tx pgx.Tx) (bool, error) {
	var off bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM tenant_modules WHERE module = $1 AND NOT enabled)`, Module).Scan(&off)
	return !off, err
}

// SourceGrantraLogin is the source of the sign-ins read from Grantra Login.
const SourceGrantraLogin = "grantra-login"

// MaxBatch is how many sign-ins one call may send.
const MaxBatch = 1000

// SignIn is one attempt to sign in, as a provider reports it.
type SignIn struct {
	// ID is the provider's id for the event, unique within the source.
	ID   string    `json:"id"`
	Time time.Time `json:"time"`
	// User is who tried: an email or a username.
	User    string `json:"user"`
	Outcome string `json:"outcome"`
	IP      string `json:"ip,omitempty"`
	// Country is an ISO 3166 code, such as "ES", when the provider knows it.
	Country string `json:"country,omitempty"`
	// Latitude and Longitude, when the provider knows them, let Grantra spot
	// travel nobody could do.
	Latitude    *float64 `json:"latitude,omitempty"`
	Longitude   *float64 `json:"longitude,omitempty"`
	Application string   `json:"application,omitempty"`
	UserAgent   string   `json:"user_agent,omitempty"`

	// identityID is who it was, when the source knows it for sure (a
	// Grantra Login account linked to an identity).
	identityID string
}

var (
	sourcePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,62}$`)
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
)

// clip shortens s to at most n bytes, on a character boundary.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// check validates a sign-in from a script and tidies it: n is its place in
// the batch, for the error.
func (s *SignIn) check(n int, now time.Time) error {
	bad := func(format string, args ...any) error {
		return apperr.Invalidf("event %d: "+format, append([]any{n}, args...)...)
	}
	s.ID = strings.TrimSpace(s.ID)
	if s.ID == "" || len(s.ID) > 200 {
		return bad("id is required, up to 200 characters, and must be unique for the source")
	}
	if s.Time.IsZero() {
		return bad("time is required, as RFC 3339 (2026-10-19T08:30:00Z)")
	}
	if s.Time.After(now.Add(5 * time.Minute)) {
		return bad("time %s is in the future", s.Time.Format(time.RFC3339))
	}
	switch s.Outcome {
	case Success, Failure, MFAFailure:
	default:
		return bad("outcome must be %s, %s or %s", Success, Failure, MFAFailure)
	}
	s.User = clip(s.User, 320)
	if s.User == "" {
		return bad("user is required: the email or username that tried to sign in")
	}
	if s.IP = strings.TrimSpace(s.IP); s.IP != "" {
		if _, err := netip.ParseAddr(s.IP); err != nil {
			return bad("ip %q is not an IP address", s.IP)
		}
	}
	if s.Country = strings.ToUpper(strings.TrimSpace(s.Country)); s.Country != "" && !countryPattern.MatchString(s.Country) {
		return bad("country must be a two-letter ISO code such as ES")
	}
	if (s.Latitude == nil) != (s.Longitude == nil) {
		return bad("give both latitude and longitude, or neither")
	}
	if s.Latitude != nil && (*s.Latitude < -90 || *s.Latitude > 90 || *s.Longitude < -180 || *s.Longitude > 180) {
		return bad("latitude must be between -90 and 90 and longitude between -180 and 180")
	}
	s.Application = clip(s.Application, 200)
	s.UserAgent = clip(s.UserAgent, 500)
	return nil
}

// Receive stores sign-ins a script sends from another identity provider,
// such as Okta or Entra ID, under source (its name, like "okta"). Events
// already stored are skipped, so a script can send overlapping windows.
// People are matched by email, or by the username of one of their
// accounts. It returns how many were new.
func Receive(ctx context.Context, tx pgx.Tx, tenantID, actor, source string, events []SignIn) (int, error) {
	source = strings.TrimSpace(source)
	if !sourcePattern.MatchString(source) {
		return 0, apperr.Invalidf("source must be a short lowercase name for the provider, such as okta or entra")
	}
	if source == SourceGrantraLogin {
		return 0, apperr.Invalidf("%s sign-ins are read from Grantra Login itself; name the provider these come from", SourceGrantraLogin)
	}
	if len(events) == 0 || len(events) > MaxBatch {
		return 0, apperr.Invalidf("send between 1 and %d events", MaxBatch)
	}
	now := time.Now()
	for i := range events {
		if err := events[i].check(i, now); err != nil {
			return 0, err
		}
	}
	n, err := store(ctx, tx, tenantID, source, events)
	if err != nil {
		return 0, err
	}
	return n, db.Audit(ctx, tx, tenantID, actor, "itdr.sign_ins_received", "", "",
		map[string]any{"source": source, "events": len(events), "new": n})
}

// resolveUsers finds the one identity each name belongs to: by email, or
// by the username of one of their accounts. Names that match nobody, or
// more than one person, are left out.
func resolveUsers(ctx context.Context, tx pgx.Tx, names []string) (map[string]string, error) {
	rows, err := tx.Query(ctx, `
		WITH u AS (SELECT DISTINCT lower(n) AS name FROM unnest($1::text[]) n),
		m AS (
			SELECT u.name, i.id FROM u JOIN identities i ON lower(i.email) = u.name
			UNION
			SELECT u.name, a.identity_id FROM u JOIN accounts a ON lower(a.username) = u.name
			WHERE a.identity_id IS NOT NULL AND a.status <> 'deleted')
		SELECT name, min(id::text) FROM m GROUP BY name HAVING count(DISTINCT id) = 1`, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, id string
		if err := rows.Scan(&name, &id); err != nil {
			return nil, err
		}
		out[name] = id
	}
	return out, rows.Err()
}

// store inserts sign-ins, skipping the ones the source already sent, and
// returns how many were new.
func store(ctx context.Context, tx pgx.Tx, tenantID, source string, events []SignIn) (int, error) {
	var names []string
	for _, e := range events {
		if e.identityID == "" && e.User != "" {
			names = append(names, e.User)
		}
	}
	who := map[string]string{}
	if len(names) > 0 {
		var err error
		if who, err = resolveUsers(ctx, tx, names); err != nil {
			return 0, err
		}
	}
	added := 0
	err := db.SendBatch(ctx, tx, events, func(b *pgx.Batch, e SignIn) {
		identity := e.identityID
		if identity == "" {
			identity = who[strings.ToLower(e.User)]
		}
		b.Queue(`INSERT INTO signin_events (tenant_id, source, external_id, occurred_at, identity_id, username, outcome,
			ip, country, latitude, longitude, application, user_agent)
			VALUES ($1, $2, $3, $4, $5::uuid, $6, $7, $8::inet, $9, $10, $11, $12, $13)
			ON CONFLICT (tenant_id, source, external_id) DO NOTHING`,
			tenantID, source, e.ID, e.Time, db.Nullable(identity), db.Nullable(e.User), e.Outcome,
			db.Nullable(e.IP), db.Nullable(e.Country), e.Latitude, e.Longitude, db.Nullable(e.Application), db.Nullable(e.UserAgent))
	}, func(br pgx.BatchResults, _ SignIn) error {
		tag, err := br.Exec()
		added += int(tag.RowsAffected())
		return err
	})
	return added, err
}

// Event is a stored sign-in, as the console lists it.
type Event struct {
	ID           int64     `json:"id"`
	Source       string    `json:"source"`
	OccurredAt   time.Time `json:"occurred_at"`
	IdentityID   string    `json:"identity_id,omitempty"`
	IdentityName string    `json:"identity_name,omitempty"`
	Username     string    `json:"username,omitempty"`
	Outcome      string    `json:"outcome"`
	IP           string    `json:"ip,omitempty"`
	Country      string    `json:"country,omitempty"`
	Latitude     *float64  `json:"latitude,omitempty"`
	Longitude    *float64  `json:"longitude,omitempty"`
	Application  string    `json:"application,omitempty"`
	UserAgent    string    `json:"user_agent,omitempty"`
}

// EventFilter narrows a list of sign-ins.
type EventFilter struct {
	IdentityID string
	Outcome    string
	IP         string
	// Before pages back: the events after the one with this id in the list.
	Before int64
	Limit  int
}

// Events lists sign-ins, newest first.
func Events(ctx context.Context, tx pgx.Tx, f EventFilter) ([]Event, error) {
	if f.IdentityID != "" && !db.ValidUUID(f.IdentityID) {
		return nil, apperr.Invalidf("identity_id must be an identity id")
	}
	switch f.Outcome {
	case "", Success, Failure, MFAFailure:
	default:
		return nil, apperr.Invalidf("outcome must be %s, %s or %s", Success, Failure, MFAFailure)
	}
	if f.IP != "" {
		if _, err := netip.ParseAddr(f.IP); err != nil {
			return nil, apperr.Invalidf("ip must be an IP address")
		}
	}
	rows, err := tx.Query(ctx, `
		SELECT s.id, s.source, s.occurred_at, coalesce(s.identity_id::text, ''), coalesce(i.display_name, ''),
		       coalesce(s.username, ''), s.outcome, coalesce(host(s.ip), ''), coalesce(s.country, ''),
		       s.latitude, s.longitude, coalesce(s.application, ''), coalesce(s.user_agent, '')
		FROM signin_events s LEFT JOIN identities i ON i.id = s.identity_id
		WHERE (nullif($1, '')::uuid IS NULL OR s.identity_id = nullif($1, '')::uuid)
		  AND ($2 = '' OR s.outcome = $2)
		  AND (nullif($3, '')::inet IS NULL OR s.ip = nullif($3, '')::inet)
		  AND ($4 = 0 OR (s.occurred_at, s.id) < (SELECT b.occurred_at, b.id FROM signin_events b WHERE b.id = $4))
		ORDER BY s.occurred_at DESC, s.id DESC LIMIT $5`, f.IdentityID, f.Outcome, f.IP, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var e Event
		err := row.Scan(&e.ID, &e.Source, &e.OccurredAt, &e.IdentityID, &e.IdentityName, &e.Username, &e.Outcome,
			&e.IP, &e.Country, &e.Latitude, &e.Longitude, &e.Application, &e.UserAgent)
		return e, err
	})
}
