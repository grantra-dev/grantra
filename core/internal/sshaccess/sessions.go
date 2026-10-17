package sshaccess

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/ssh"

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
	// EndRecordingFull ends a session that outgrew the recording limit, so
	// nothing runs unrecorded.
	EndRecordingFull = "recording_full"
)

// Session is one shell or command someone ran through the gateway.
type Session struct {
	ID            string     `json:"id"`
	TargetID      string     `json:"target_id,omitempty"`
	TargetName    string     `json:"target_name"`
	Login         string     `json:"login"`
	IdentityID    string     `json:"identity_id"`
	IdentityName  string     `json:"identity_name"`
	CertificateID string     `json:"certificate_id,omitempty"`
	ClientAddress string     `json:"client_address"`
	Gateway       string     `json:"gateway"`
	Kind          string     `json:"kind"`
	Command       string     `json:"command,omitempty"`
	Width         int        `json:"width"`
	Height        int        `json:"height"`
	StartedAt     time.Time  `json:"started_at"`
	LastSeenAt    time.Time  `json:"last_seen_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	EndReason     string     `json:"end_reason,omitempty"`
	Error         string     `json:"error,omitempty"`
	ExitStatus    *int       `json:"exit_status,omitempty"`
	// TerminateRequested says an admin asked the gateway to end it.
	TerminateRequested bool  `json:"terminate_requested"`
	RecordingBytes     int64 `json:"recording_bytes"`
	RecordingTruncated bool  `json:"recording_truncated"`
}

// Grant is what the gateway needs to let a certificate through to its
// target.
type Grant struct {
	CertificateID string
	Serial        uint64
	IdentityID    string
	IdentityName  string
	TargetID      string
	TargetName    string
	Address       string
	Login         string
	HostKey       string
}

// ErrNoAccess is why the gateway turns someone away or ends their session.
var ErrNoAccess = errors.New("no access to this target right now")

// Authorize checks a certificate the gateway verified against the CA: it
// must be one Grantra signed (id and serial), for target user, through the
// gateway, and its person must still be active and hold access to it.
func Authorize(ctx context.Context, tx pgx.Tx, certID string, serial uint64, user string) (Grant, error) {
	if !db.ValidUUID(certID) {
		return Grant{}, fmt.Errorf("certificate %q was not signed by Grantra for a person", certID)
	}
	var g Grant
	var viaGateway bool
	var hostKey *string
	err := tx.QueryRow(ctx,
		`SELECT c.id, c.identity_id, i.display_name, t.id, t.name, t.address, t.login, t.host_key, t.via_gateway
		 FROM ssh_certificates c JOIN ssh_targets t ON t.id = c.target_id JOIN identities i ON i.id = c.identity_id
		 WHERE c.id = $1 AND c.serial = $2`, certID, int64(serial)).
		Scan(&g.CertificateID, &g.IdentityID, &g.IdentityName, &g.TargetID, &g.TargetName, &g.Address, &g.Login, &hostKey, &viaGateway)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, fmt.Errorf("certificate %s is unknown or its target was deleted", certID)
	}
	if err != nil {
		return Grant{}, err
	}
	if hostKey != nil {
		g.HostKey = *hostKey
	}
	g.Serial = serial
	if g.TargetName != user {
		return Grant{}, fmt.Errorf("certificate is for %s, not %s", g.TargetName, user)
	}
	if !viaGateway {
		return Grant{}, fmt.Errorf("%s is reached directly, not through the gateway", g.TargetName)
	}
	ok, err := holds(ctx, tx, g.IdentityID, g.TargetID)
	if err != nil {
		return Grant{}, err
	}
	if !ok {
		return Grant{}, ErrNoAccess
	}
	return g, nil
}

func holds(ctx context.Context, tx pgx.Tx, identityID, targetID string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT `+heldBy+` FROM ssh_targets t WHERE t.id = $2`, identityID, targetID).Scan(&ok)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

// gatewayCertMinutes is how long the certificate the gateway logs in to a
// target with lasts: only long enough to connect.
const gatewayCertMinutes = 2

// GatewayCertificate signs key for the gateway to log in to a target as
// login, for one session.
func (s *Service) GatewayCertificate(ctx context.Context, tx pgx.Tx, tenantID, sessionID, login string, key ssh.PublicKey) (*ssh.Certificate, error) {
	signer, err := s.signer(ctx, tx, tenantID, GatewayCA)
	if err != nil {
		return nil, err
	}
	now := s.now()
	cert := &ssh.Certificate{
		Key:             key,
		CertType:        ssh.UserCert,
		KeyId:           "grantra-gateway:" + sessionID,
		ValidPrincipals: []string{login},
		ValidAfter:      uint64(now.Add(-clockSkew).Unix()),
		ValidBefore:     uint64(now.Add(gatewayCertMinutes * time.Minute).Unix()),
		Permissions:     ssh.Permissions{Extensions: map[string]string{"permit-pty": ""}},
	}
	return cert, cert.SignCert(rand.Reader, signer)
}

// LearnHostKey stores a target's host key the first time the gateway sees
// it and returns the key on record, which the gateway then requires.
func LearnHostKey(ctx context.Context, tx pgx.Tx, tenantID, targetID, key string) (string, error) {
	var known string
	err := tx.QueryRow(ctx,
		`UPDATE ssh_targets SET host_key = $2 WHERE id = $1 AND host_key IS NULL RETURNING host_key`, targetID, key).Scan(&known)
	if err == nil {
		return known, db.Audit(ctx, tx, tenantID, "system:gateway", "ssh.host_key_learned", "ssh_target", targetID,
			map[string]any{"host_key": key})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	err = tx.QueryRow(ctx, `SELECT coalesce(host_key, '') FROM ssh_targets WHERE id = $1`, targetID).Scan(&known)
	return known, err
}

// StartSession records that a session began.
func StartSession(ctx context.Context, tx pgx.Tx, tenantID string, s Session) (string, error) {
	var id string
	err := tx.QueryRow(ctx,
		`INSERT INTO ssh_sessions (tenant_id, target_id, target_name, login, identity_id, certificate_id, client_address,
		                           gateway, kind, command, width, height)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12) RETURNING id`,
		tenantID, s.TargetID, s.TargetName, s.Login, s.IdentityID, db.Nullable(s.CertificateID), s.ClientAddress, s.Gateway,
		s.Kind, db.Nullable(s.Command), s.Width, s.Height).Scan(&id)
	if err != nil {
		return "", err
	}
	return id, db.Audit(ctx, tx, tenantID, db.IdentityActor(s.IdentityID), "ssh.session_started", "ssh_target", s.TargetID,
		map[string]any{"session_id": id, "login": s.Login, "kind": s.Kind, "command": s.Command, "client_address": s.ClientAddress,
			"gateway": s.Gateway})
}

// AppendRecording stores the next piece of a session's recording.
func AppendRecording(ctx context.Context, tx pgx.Tx, tenantID, sessionID string, seq int, data []byte, truncated bool) error {
	if len(data) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO ssh_session_chunks (tenant_id, session_id, seq, data) VALUES ($1, $2, $3, $4)`,
			tenantID, sessionID, seq, data); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx,
		`UPDATE ssh_sessions SET recording_bytes = recording_bytes + $2, recording_truncated = recording_truncated OR $3
		 WHERE id = $1`, sessionID, len(data), truncated)
	return err
}

// Command is a command typed in a session.
type Command struct {
	ID           int64     `json:"id"`
	SessionID    string    `json:"session_id"`
	At           time.Time `json:"at"`
	Offset       float64   `json:"offset_seconds"`
	Command      string    `json:"command"`
	TargetName   string    `json:"target_name,omitempty"`
	Login        string    `json:"login,omitempty"`
	IdentityID   string    `json:"identity_id,omitempty"`
	IdentityName string    `json:"identity_name,omitempty"`
}

// maxCommand bounds one indexed command.
const maxCommand = 4000

// cleanCommand makes a command storable: valid UTF-8 without NUL (which
// Postgres text refuses), at most maxCommand bytes, cut between characters.
func cleanCommand(cmd string) string {
	text := strings.ToValidUTF8(strings.ReplaceAll(cmd, "\x00", ""), "")
	if len(text) > maxCommand {
		cut := maxCommand
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text
}

// AddCommands indexes commands typed in a session.
func AddCommands(ctx context.Context, tx pgx.Tx, tenantID, sessionID string, cmds []Command) error {
	for _, c := range cmds {
		text := cleanCommand(c.Command)
		if _, err := tx.Exec(ctx,
			`INSERT INTO ssh_commands (tenant_id, session_id, at, offset_seconds, command) VALUES ($1, $2, $3, $4, $5)`,
			tenantID, sessionID, c.At, c.Offset, text); err != nil {
			return err
		}
	}
	return nil
}

// Heartbeat marks a session as alive and says whether it may go on, and if
// not why: its person lost access, or an admin asked to end it.
func Heartbeat(ctx context.Context, tx pgx.Tx, sessionID string) (reason string, err error) {
	var identity, target *string
	var terminate, ended bool
	err = tx.QueryRow(ctx,
		`UPDATE ssh_sessions SET last_seen_at = now() WHERE id = $1
		 RETURNING identity_id::text, target_id::text, terminate_requested_by IS NOT NULL, ended_at IS NOT NULL`, sessionID).
		Scan(&identity, &target, &terminate, &ended)
	if err != nil {
		return "", err
	}
	switch {
	case terminate:
		return EndTerminated, nil
	case ended:
		// Swept as lost while the gateway could not reach the database.
		return EndLost, nil
	case target == nil:
		return EndAccessEnded, nil
	}
	ok, err := holds(ctx, tx, *identity, *target)
	if err != nil || ok {
		return "", err
	}
	return EndAccessEnded, nil
}

// EndSession records how a session ended.
func EndSession(ctx context.Context, tx pgx.Tx, tenantID, sessionID, reason, errText string, exitStatus *int) error {
	var identity string
	var target *string
	err := tx.QueryRow(ctx,
		`UPDATE ssh_sessions SET ended_at = now(), end_reason = $2, error = $3, exit_status = $4, last_seen_at = now()
		 WHERE id = $1 AND ended_at IS NULL RETURNING identity_id, target_id`,
		sessionID, reason, db.Nullable(errText), exitStatus).Scan(&identity, &target)
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
	return db.Audit(ctx, tx, tenantID, db.IdentityActor(identity), "ssh.session_ended", "ssh_target", targetID,
		map[string]any{"session_id": sessionID, "reason": reason, "error": errText, "exit_status": exitStatus})
}

// LostAfter is how long a session goes untouched by its gateway before it
// counts as lost. Gateways touch their sessions every 30 seconds.
const LostAfter = 3 * time.Minute

// EndLostSessions ends the sessions whose gateway stopped touching them,
// such as when it crashed.
func EndLostSessions(ctx context.Context, tx pgx.Tx, tenantID string) (int, error) {
	rows, err := tx.Query(ctx,
		`SELECT id FROM ssh_sessions WHERE ended_at IS NULL AND last_seen_at < now() - make_interval(secs => $1)`,
		LostAfter.Seconds())
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if err := EndSession(ctx, tx, tenantID, id, EndLost, "the gateway stopped reporting on this session", nil); err != nil {
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

const sessionSelect = `SELECT s.id, coalesce(s.target_id::text, ''), s.target_name, s.login, s.identity_id, i.display_name,
	coalesce(s.certificate_id::text, ''), s.client_address, s.gateway, s.kind, coalesce(s.command, ''), s.width, s.height,
	s.started_at, s.last_seen_at, s.ended_at, coalesce(s.end_reason, ''), coalesce(s.error, ''), s.exit_status,
	s.terminate_requested_by IS NOT NULL, s.recording_bytes, s.recording_truncated
	FROM ssh_sessions s JOIN identities i ON i.id = s.identity_id`

func scanSession(row pgx.CollectableRow) (Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.TargetID, &s.TargetName, &s.Login, &s.IdentityID, &s.IdentityName, &s.CertificateID,
		&s.ClientAddress, &s.Gateway, &s.Kind, &s.Command, &s.Width, &s.Height, &s.StartedAt, &s.LastSeenAt, &s.EndedAt,
		&s.EndReason, &s.Error, &s.ExitStatus, &s.TerminateRequested, &s.RecordingBytes, &s.RecordingTruncated)
	return s, err
}

// ListSessions returns sessions, newest first.
func ListSessions(ctx context.Context, tx pgx.Tx, f SessionFilter) ([]Session, error) {
	if (f.TargetID != "" && !db.ValidUUID(f.TargetID)) || (f.IdentityID != "" && !db.ValidUUID(f.IdentityID)) {
		return []Session{}, nil
	}
	rows, err := tx.Query(ctx, sessionSelect+`
		WHERE ($1 = '' OR s.target_id = nullif($1, '')::uuid) AND ($2 = '' OR s.identity_id = nullif($2, '')::uuid) AND (NOT $3 OR s.ended_at IS NULL)
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
	return db.QueryOne(ctx, tx, scanSession, sessionSelect+` WHERE s.id = $1`, id)
}

// SessionCommands returns the commands typed in a session, in order.
func SessionCommands(ctx context.Context, tx pgx.Tx, id string) ([]Command, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, session_id, at, offset_seconds, command FROM ssh_commands WHERE session_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Command, error) {
		var c Command
		return c, row.Scan(&c.ID, &c.SessionID, &c.At, &c.Offset, &c.Command)
	})
}

// RecordingViewed checks that a session exists and records in the audit
// log that actor is watching its recording. Callers commit it before
// sending the recording, so a download cut short is still in the log.
func RecordingViewed(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	if _, err := GetSession(ctx, tx, id); err != nil {
		return err
	}
	return db.Audit(ctx, tx, tenantID, actor, "ssh.recording_viewed", "ssh_session", id, nil)
}

// WriteRecording writes a session's recording, an asciicast v2 file, to w
// piece by piece (it can be up to 100 MB); start is called before the
// first byte, to set headers. See RecordingViewed.
func WriteRecording(ctx context.Context, tx pgx.Tx, id string, start func(), w io.Writer) error {
	return db.WriteRows(ctx, tx, w, start, `SELECT data FROM ssh_session_chunks WHERE session_id = $1 ORDER BY seq`, id)
}

// Terminate asks the gateway to end a running session; it does within 30
// seconds.
func Terminate(ctx context.Context, tx pgx.Tx, tenantID, actor, id string) error {
	if !db.ValidUUID(id) {
		return apperr.ErrNotFound
	}
	var ended bool
	err := tx.QueryRow(ctx,
		`UPDATE ssh_sessions SET terminate_requested_by = coalesce(terminate_requested_by, $2) WHERE id = $1
		 RETURNING ended_at IS NOT NULL`, id, actor).Scan(&ended)
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.ErrNotFound
	}
	if err != nil {
		return err
	}
	if ended {
		return apperr.Conflictf("this session has already ended")
	}
	return db.Audit(ctx, tx, tenantID, actor, "ssh.session_terminate_requested", "ssh_session", id, nil)
}

// SearchCommands finds typed commands containing q (any case), newest
// first, optionally on one target.
func SearchCommands(ctx context.Context, tx pgx.Tx, q, targetID string, limit int) ([]Command, error) {
	pattern, err := db.SearchPattern(q)
	if err != nil {
		return nil, err
	}
	if targetID != "" && !db.ValidUUID(targetID) {
		return []Command{}, nil
	}
	rows, err := tx.Query(ctx,
		`SELECT c.id, c.session_id, c.at, c.offset_seconds, c.command, s.target_name, s.login, s.identity_id, i.display_name
		 FROM ssh_commands c JOIN ssh_sessions s ON s.id = c.session_id JOIN identities i ON i.id = s.identity_id
		 WHERE c.command ILIKE $1 AND ($2 = '' OR s.target_id = nullif($2, '')::uuid)
		 ORDER BY c.at DESC, c.id DESC LIMIT $3`, pattern, targetID, limit)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Command, error) {
		var c Command
		return c, row.Scan(&c.ID, &c.SessionID, &c.At, &c.Offset, &c.Command, &c.TargetName, &c.Login, &c.IdentityID, &c.IdentityName)
	})
}
