package itdr

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// Alert statuses.
const (
	Open      = "open"
	Resolved  = "resolved"
	Dismissed = "dismissed"
)

// Alert is a threat Grantra found.
type Alert struct {
	ID        string `json:"id"`
	Detection string `json:"detection"`
	Title     string `json:"title"`
	Severity  string `json:"severity"`
	Status    string `json:"status"`
	// IdentityID is who the alert is about; empty for an alert about an
	// address, such as a password spray.
	IdentityID     string         `json:"identity_id,omitempty"`
	IdentityName   string         `json:"identity_name,omitempty"`
	IdentityStatus string         `json:"identity_status,omitempty"`
	Subject        string         `json:"subject"`
	Summary        string         `json:"summary"`
	Evidence       map[string]any `json:"evidence"`
	// Occurrences is how many times the detection fired while the alert
	// was open.
	Occurrences int        `json:"occurrences"`
	FirstSeenAt time.Time  `json:"first_seen_at"`
	LastSeenAt  time.Time  `json:"last_seen_at"`
	CreatedAt   time.Time  `json:"created_at"`
	ClosedBy    string     `json:"closed_by,omitempty"`
	ClosedAt    *time.Time `json:"closed_at,omitempty"`
	CloseNote   string     `json:"close_note,omitempty"`
	// Responses is what was done about it, newest first; only Get fills it.
	Responses []Response `json:"responses,omitempty"`
}

const selectAlerts = `SELECT t.id, t.detection, t.severity, t.status, coalesce(t.identity_id::text, ''),
	coalesce(i.display_name, ''), coalesce(i.status, ''), t.subject, t.summary, t.evidence, t.occurrences,
	t.first_seen_at, t.last_seen_at, t.created_at, coalesce(t.closed_by, ''), t.closed_at, coalesce(t.close_note, '')
	FROM threat_alerts t LEFT JOIN identities i ON i.id = t.identity_id`

func scanAlert(row pgx.CollectableRow) (Alert, error) {
	var a Alert
	err := row.Scan(&a.ID, &a.Detection, &a.Severity, &a.Status, &a.IdentityID, &a.IdentityName, &a.IdentityStatus,
		&a.Subject, &a.Summary, &a.Evidence, &a.Occurrences, &a.FirstSeenAt, &a.LastSeenAt, &a.CreatedAt,
		&a.ClosedBy, &a.ClosedAt, &a.CloseNote)
	a.Title = Title(a.Detection)
	return a, err
}

// Title is the name people see for a detection.
func Title(detection string) string {
	if d, ok := definition(detection); ok {
		return d.Title
	}
	return detection
}

// AlertFilter narrows a list of alerts.
type AlertFilter struct {
	Status     string
	Severity   string
	IdentityID string
	Detection  string
	// Before pages back: the alerts after the one with this id in the list.
	Before string
	Limit  int
}

// Alerts lists alerts, most recently seen first.
func Alerts(ctx context.Context, tx pgx.Tx, f AlertFilter) ([]Alert, error) {
	switch f.Status {
	case "", Open, Resolved, Dismissed:
	default:
		return nil, apperr.Invalidf("status must be open, resolved or dismissed")
	}
	if f.Severity != "" && severityRank[f.Severity] == 0 {
		return nil, apperr.Invalidf("severity must be low, medium, high or critical")
	}
	if f.IdentityID != "" && !db.ValidUUID(f.IdentityID) {
		return nil, apperr.Invalidf("identity_id must be an identity id")
	}
	if _, ok := definition(f.Detection); f.Detection != "" && !ok {
		return nil, apperr.Invalidf("unknown detection %q", f.Detection)
	}
	if f.Before != "" && !db.ValidUUID(f.Before) {
		return nil, apperr.Invalidf("before must be an alert id")
	}
	rows, err := tx.Query(ctx, selectAlerts+`
		WHERE ($1 = '' OR t.status = $1) AND ($2 = '' OR t.severity = $2)
		  AND (nullif($3, '')::uuid IS NULL OR t.identity_id = nullif($3, '')::uuid)
		  AND ($4 = '' OR t.detection = $4)
		  AND ($5 = '' OR (t.last_seen_at, t.id) < (SELECT b.last_seen_at, b.id FROM threat_alerts b WHERE b.id = nullif($5, '')::uuid))
		ORDER BY t.last_seen_at DESC, t.id DESC LIMIT $6`,
		f.Status, f.Severity, f.IdentityID, f.Detection, f.Before, f.Limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, scanAlert)
}

// Get returns one alert with what was done about it.
func Get(ctx context.Context, tx pgx.Tx, id string) (Alert, error) {
	if !db.ValidUUID(id) {
		return Alert{}, apperr.ErrNotFound
	}
	a, err := db.QueryOne(ctx, tx, scanAlert, selectAlerts+` WHERE t.id = $1`, id)
	if err != nil {
		return Alert{}, err
	}
	a.Responses, err = responses(ctx, tx, `r.alert_id = $1`, id)
	return a, err
}

// Counts is how many alerts are open, per severity.
type Counts map[string]int

// OpenCounts counts the open alerts per severity, leaving out the ones
// about the identity except (an admin's own alerts are not theirs to act on).
func OpenCounts(ctx context.Context, tx pgx.Tx, except string) (Counts, error) {
	rows, err := tx.Query(ctx,
		`SELECT severity, count(*) FROM threat_alerts
		 WHERE status = 'open' AND (nullif($1, '') IS NULL OR identity_id IS DISTINCT FROM nullif($1, '')::uuid)
		 GROUP BY severity`, except)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := Counts{}
	for rows.Next() {
		var sev string
		var n int
		if err := rows.Scan(&sev, &n); err != nil {
			return nil, err
		}
		out[sev] = n
	}
	return out, rows.Err()
}

// Close resolves (a real threat, dealt with) or dismisses (not a threat)
// an open alert, with a note.
func Close(ctx context.Context, tx pgx.Tx, tenantID, actor, id, status, note string) (Alert, error) {
	if status != Resolved && status != Dismissed {
		return Alert{}, apperr.Invalidf("status must be resolved or dismissed")
	}
	note = strings.TrimSpace(note)
	if len(note) > 2000 {
		return Alert{}, apperr.Invalidf("note must be at most 2000 characters")
	}
	if status == Dismissed && note == "" {
		return Alert{}, apperr.Invalidf("say why this is not a threat, so whoever looks later knows")
	}
	if !db.ValidUUID(id) {
		return Alert{}, apperr.ErrNotFound
	}
	tag, err := tx.Exec(ctx,
		`UPDATE threat_alerts SET status = $2, closed_by = $3, closed_at = now(), close_note = nullif($4, '')
		 WHERE id = $1 AND status = 'open'`, id, status, actor, note)
	if err != nil {
		return Alert{}, err
	}
	if tag.RowsAffected() == 0 {
		if _, err := Get(ctx, tx, id); err != nil {
			return Alert{}, err
		}
		return Alert{}, apperr.Conflictf("this alert is already closed")
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "itdr.alert_"+status, "threat_alert", id, map[string]any{"note": note}); err != nil {
		return Alert{}, err
	}
	return Get(ctx, tx, id)
}

// AlertIdentity returns who an alert is about ("" for an address), for
// the policies.
func AlertIdentity(ctx context.Context, tx pgx.Tx, id string) (string, error) {
	if !db.ValidUUID(id) {
		return "", apperr.ErrNotFound
	}
	return db.QueryOne(ctx, tx, pgx.RowTo[string],
		`SELECT coalesce(identity_id::text, '') FROM threat_alerts WHERE id = $1`, id)
}

// finding is something a detection saw.
type finding struct {
	detection  string
	identityID string
	// subject is the identity id, or "ip:<address>".
	subject string
	summary string
	// raised makes the alert one step more severe than the detection's,
	// such as a successful sign-in right after many failures.
	raised   bool
	evidence map[string]any
	at       time.Time
}

// raised is an alert a scan opened, or made more severe.
type raised struct {
	id, detection, identityID, severity, summary string
	created                                      bool
}

// raise opens an alert for f, or adds to the open one about the same
// subject, keeping the higher severity and the latest evidence. It returns
// the alert when it is new or got more severe, which is when people hear
// about it and automatic responses run.
func raise(ctx context.Context, tx pgx.Tx, tenantID string, s Setting, f finding) (*raised, error) {
	sev := s.Severity
	if f.raised {
		sev = raiseSeverity(sev)
	}
	var (
		r       = raised{detection: f.detection, identityID: f.identityID}
		prevSev string
	)
	err := tx.QueryRow(ctx, `
		WITH prev AS (SELECT severity FROM threat_alerts WHERE detection = $2 AND subject = $5 AND status = 'open')
		INSERT INTO threat_alerts AS t (tenant_id, detection, severity, identity_id, subject, summary, evidence, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $4::uuid, $5, $6, $7, $8, $8)
		ON CONFLICT (tenant_id, detection, subject) WHERE status = 'open' DO UPDATE SET
		  occurrences = t.occurrences + 1,
		  last_seen_at = greatest(t.last_seen_at, EXCLUDED.last_seen_at),
		  first_seen_at = least(t.first_seen_at, EXCLUDED.first_seen_at),
		  severity = CASE WHEN `+rankSQL("EXCLUDED.severity")+` > `+rankSQL("t.severity")+` THEN EXCLUDED.severity ELSE t.severity END,
		  summary = CASE WHEN `+rankSQL("EXCLUDED.severity")+` >= `+rankSQL("t.severity")+` THEN EXCLUDED.summary ELSE t.summary END,
		  evidence = CASE WHEN `+rankSQL("EXCLUDED.severity")+` >= `+rankSQL("t.severity")+` THEN EXCLUDED.evidence ELSE t.evidence END
		RETURNING t.id, t.severity, t.summary, coalesce((SELECT severity FROM prev), '')`,
		tenantID, f.detection, sev, db.Nullable(f.identityID), f.subject, f.summary, f.evidence, f.at).
		Scan(&r.id, &r.severity, &r.summary, &prevSev)
	if err != nil {
		return nil, err
	}
	r.created = prevSev == ""
	if !r.created && r.severity == prevSev {
		return nil, nil
	}
	action := "itdr.alert_raised"
	if !r.created {
		action = "itdr.alert_escalated"
	}
	if err := db.Audit(ctx, tx, tenantID, "system:itdr", action, "threat_alert", r.id, map[string]any{
		"detection": f.detection, "severity": r.severity, "subject": f.subject, "summary": r.summary}); err != nil {
		return nil, err
	}
	return &r, nil
}

// rankSQL orders a severity column in SQL like severityRank.
func rankSQL(col string) string {
	return `array_position(ARRAY['low', 'medium', 'high', 'critical'], ` + col + `)`
}

// raiseSeverity is the severity one step above sev, up to critical.
func raiseSeverity(sev string) string {
	for name, rank := range severityRank {
		if rank == severityRank[sev]+1 {
			return name
		}
	}
	return sev
}
