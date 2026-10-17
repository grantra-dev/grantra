package gateways

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Session end reasons.
const (
	EndClosed      = "closed"
	EndAccessEnded = "access_ended"
	EndTerminated  = "terminated"
	EndLost        = "lost"
	EndFailed      = "failed"
	// EndRecordingFull ends a session whose log reached the limit, so
	// nothing runs unlogged.
	EndRecordingFull = "recording_full"
)

// Event kinds.
const (
	EventQuery   = "query"
	EventRequest = "request"
)

// Session is one database connection, or a run of requests to a cluster
// with one token, through one gateway.
type Session struct {
	ID            string     `json:"id"`
	TargetID      string     `json:"target_id,omitempty"`
	TargetName    string     `json:"target_name"`
	Kind          string     `json:"kind"`
	IdentityID    string     `json:"identity_id"`
	IdentityName  string     `json:"identity_name"`
	TokenID       string     `json:"token_id,omitempty"`
	ClientAddress string     `json:"client_address"`
	Gateway       string     `json:"gateway"`
	Database      string     `json:"database,omitempty"`
	Account       string     `json:"account,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	EndReason     string     `json:"end_reason,omitempty"`
	Error         string     `json:"error,omitempty"`
	// TerminateRequested says an admin asked the gateway to end it.
	TerminateRequested bool `json:"terminate_requested"`
	Events             int  `json:"events"`
}

// Event is a query run in a database session or a request made to a
// cluster.
type Event struct {
	ID           int64     `json:"id"`
	SessionID    string    `json:"session_id"`
	At           time.Time `json:"at"`
	Kind         string    `json:"kind"`
	Text         string    `json:"text"`
	Status       *int      `json:"status,omitempty"`
	TargetName   string    `json:"target_name,omitempty"`
	IdentityID   string    `json:"identity_id,omitempty"`
	IdentityName string    `json:"identity_name,omitempty"`
}

// MaxEventText bounds the text kept of one event.
const MaxEventText = 4000

// CleanText makes event text storable: valid UTF-8 without NUL (which
// Postgres text refuses), at most MaxEventText bytes, cut between
// characters.
func CleanText(s string) string {
	text := strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "")
	if len(text) > MaxEventText {
		cut := MaxEventText
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}

// StartSession records that a session began. account is the vault account
// the gateway logs in with, if any, for the audit log.
func StartSession(ctx context.Context, tx pgx.Tx, tenantID string, s Session, vaultAccountID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`INSERT INTO gateway_sessions (tenant_id, target_id, target_name, kind, identity_id, token_id, client_address, gateway,
		                               database, account)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
		tenantID, s.TargetID, s.TargetName, s.Kind, s.IdentityID, db.Nullable(s.TokenID), s.ClientAddress, s.Gateway,
		db.Nullable(s.Database), db.Nullable(s.Account)).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, db.Audit(ctx, tx, tenantID, db.IdentityActor(s.IdentityID), "gateway.session_started", "gateway_target", s.TargetID,
		map[string]any{"session_id": id, "kind": s.Kind, "token_id": s.TokenID, "client_address": s.ClientAddress,
			"gateway": s.Gateway, "database": s.Database, "account": s.Account, "vault_account_id": vaultAccountID})
}

// AddEvents logs queries or requests of a session.
func AddEvents(ctx context.Context, tx pgx.Tx, tenantID, sessionID string, events []Event) error {
	if len(events) == 0 {
		return nil
	}
	at := make([]time.Time, len(events))
	kinds := make([]string, len(events))
	texts := make([]string, len(events))
	statuses := make([]*int, len(events))
	for i, e := range events {
		at[i], kinds[i], texts[i], statuses[i] = e.At, e.Kind, CleanText(e.Text), e.Status
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO gateway_events (tenant_id, session_id, at, kind, text, status)
		 SELECT $1, $2, a, k, t, s FROM unnest($3::timestamptz[], $4::text[], $5::text[], $6::int[]) AS e(a, k, t, s)`,
		tenantID, sessionID, at, kinds, texts, statuses); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE gateway_sessions SET events = events + $2 WHERE id = $1`, sessionID, len(events))
	return err
}

// Heartbeat marks a session as alive and says whether it may go on, and if
// not why: its person lost access or its token ran out or was revoked, or
// an admin asked to end it.
func Heartbeat(ctx context.Context, tx pgx.Tx, sessionID string) (reason string, err error) {
	var identity, target *string
	var terminate, ended, tokenLive bool
	err = tx.QueryRow(ctx,
		`UPDATE gateway_sessions s SET last_seen_at = now() WHERE id = $1
		 RETURNING identity_id::text, target_id::text, terminate_requested_by IS NOT NULL, ended_at IS NOT NULL,
		           EXISTS (SELECT 1 FROM gateway_tokens k WHERE k.id = s.token_id AND k.revoked_at IS NULL AND k.valid_before > now())`,
		sessionID).Scan(&identity, &target, &terminate, &ended, &tokenLive)
	if err != nil {
		return "", err
	}
	switch {
	case terminate:
		return EndTerminated, nil
	case ended:
		// Swept as lost while the gateway could not reach the database.
		return EndLost, nil
	case target == nil, !tokenLive:
		return EndAccessEnded, nil
	}
	ok, err := holds(ctx, tx, *identity, *target)
	if err != nil || ok {
		return "", err
	}
	return EndAccessEnded, nil
}

// EndSession records how a session ended.
func EndSession(ctx context.Context, tx pgx.Tx, tenantID, sessionID, reason, errText string) error {
	var identity string
	var target *string
	err := tx.QueryRow(ctx,
		`UPDATE gateway_sessions SET ended_at = now(), end_reason = $2, error = $3, last_seen_at = now()
		 WHERE id = $1 AND ended_at IS NULL RETURNING identity_id, target_id`,
		sessionID, reason, db.Nullable(errText)).Scan(&identity, &target)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	targetID := ""
	if target != nil {
		targetID = *target
	}
	return db.Audit(ctx, tx, tenantID, db.IdentityActor(identity), "gateway.session_ended", "gateway_target", targetID,
		map[string]any{"session_id": sessionID, "reason": reason, "error": errText})
}

// LostAfter is how long a session goes untouched by its gateway before it
// counts as lost. Gateways touch their sessions every 30 seconds.
const LostAfter = 3 * time.Minute

// EndLostSessions ends the sessions whose gateway stopped touching them,
// such as when it crashed.
func EndLostSessions(ctx context.Context, tx pgx.Tx, tenantID string) (int, error) {
	rows, err := tx.Query(ctx,
		`SELECT id FROM gateway_sessions WHERE ended_at IS NULL AND last_seen_at < now() - make_interval(secs => $1)`,
		LostAfter.Seconds())
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := EndSession(ctx, tx, tenantID, id, EndLost, "the gateway stopped reporting on this session"); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// SessionFilter narrows ListSessions.
type SessionFilter struct {
	TargetID   string
	IdentityID string
	// Active keeps only sessions still running.
	Active bool
	Limit  int
}

const sessionSelect = `SELECT s.id, coalesce(s.target_id::text, ''), s.target_name, s.kind, s.identity_id, i.display_name,
	coalesce(s.token_id::text, ''), s.client_address, s.gateway, coalesce(s.database, ''), coalesce(s.account, ''),
	s.started_at, s.last_seen_at, s.ended_at, coalesce(s.end_reason, ''), coalesce(s.error, ''),
	s.terminate_requested_by IS NOT NULL, s.events
	FROM gateway_sessions s JOIN identities i ON i.id = s.identity_id`

func scanSession(row pgx.CollectableRow) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.TargetID, &s.TargetName, &s.Kind, &s.IdentityID, &s.IdentityName, &s.TokenID, &s.ClientAddress,
		&s.Gateway, &s.Database, &s.Account, &s.StartedAt, &s.LastSeenAt, &s.EndedAt, &s.EndReason, &s.Error,
		&s.TerminateRequested, &s.Events)
	return s, err
}

// ListSessions returns sessions, newest first.
func ListSessions(ctx context.Context, tx pgx.Tx, f SessionFilter) ([]Session, error) {
	if (f.TargetID != "" && !db.ValidUUID(f.TargetID)) || (f.IdentityID != "" && !db.ValidUUID(f.IdentityID)) {
		return []Session{}, nil
	}
	rows, err := tx.Query(ctx, sessionSelect+`
		WHERE ($1 = '' OR s.target_id::text = $1) AND ($2 = '' OR s.identity_id::text = $2) AND (NOT $3 OR s.ended_at IS NULL)
		ORDER BY s.started_at DESC LIMIT $4`, f.TargetID, f.IdentityID, f.Active, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanSession)
}

// GetSession returns one session.
func GetSession(ctx context.Context, tx pgx.Tx, id string) (Session, error) {
	if !db.ValidUUID(id) {
		return Session{}, apperr.ErrNotFound
	}
	rows, err := tx.Query(ctx, sessionSelect+` WHERE s.id = $1`, id)
	if err != nil {
		return Session{}, err
	}
	s, err := pgx.CollectExactlyOneRow(rows, scanSession)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, apperr.ErrNotFound
	}
	return s, err
}

// SessionEvents returns the queries or requests of a session, in order, at
// most limit of them after the event id after.
func SessionEvents(ctx context.Context, tx pgx.Tx, id string, after int64, limit int) ([]Event, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, session_id, at, kind, text, status FROM gateway_events WHERE session_id = $1 AND id > $2 ORDER BY id LIMIT $3`,
		id, after, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var e Event
		return e, row.Scan(&e.ID, &e.SessionID, &e.At, &e.Kind, &e.Text, &e.Status)
	})
}

// Terminate asks the gateway to end a running session, which it does
// within 30 seconds, and revokes the token it runs on: on a cluster every
// request is a new connection, so ending the session alone would not keep
// the person out. While they hold the access, they can get a new token.
func Terminate(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	if !db.ValidUUID(id) {
		return apperr.ErrNotFound
	}
	var ended bool
	var token *string
	err := tx.QueryRow(ctx,
		`UPDATE gateway_sessions SET terminate_requested_by = coalesce(terminate_requested_by, $2) WHERE id = $1
		 RETURNING ended_at IS NOT NULL, token_id::text`, id, actor).Scan(&ended, &token)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if ended {
		return apperr.Conflictf("this session has already ended")
	}
	if token != nil {
		if _, err := tx.Exec(ctx, `UPDATE gateway_tokens SET revoked_at = now(), revoked_by = $2 WHERE id = $1 AND revoked_at IS NULL`,
			*token, actor); err != nil {
			return err
		}
	}
	return db.Audit(ctx, tx, tenantID, actor, "gateway.session_terminate_requested", "gateway_session", id,
		map[string]any{"token_id": token})
}

// SearchEvents finds queries and requests containing q (any case), newest
// first, optionally on one target.
func SearchEvents(ctx context.Context, tx pgx.Tx, q, targetID string, limit int) ([]Event, error) {
	q = strings.TrimSpace(q)
	if q == "" || len(q) > 500 {
		return nil, apperr.Invalidf("q must be between 1 and 500 characters")
	}
	if targetID != "" && !db.ValidUUID(targetID) {
		return []Event{}, nil
	}
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
	rows, err := tx.Query(ctx,
		`SELECT e.id, e.session_id, e.at, e.kind, e.text, e.status, s.target_name, s.identity_id, i.display_name
		 FROM gateway_events e JOIN gateway_sessions s ON s.id = e.session_id JOIN identities i ON i.id = s.identity_id
		 WHERE e.text ILIKE $1 AND ($2 = '' OR s.target_id::text = $2)
		 ORDER BY e.at DESC, e.id DESC LIMIT $3`, pattern, targetID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Event, error) {
		var e Event
		return e, row.Scan(&e.ID, &e.SessionID, &e.At, &e.Kind, &e.Text, &e.Status, &e.TargetName, &e.IdentityID, &e.IdentityName)
	})
}
