package notify

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"mime/quotedprintable"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	return &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "notify")}
}

func (e *env) tx(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(context.Background(), e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) identity(name, email, status string) string {
	e.t.Helper()
	var id string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email, status) VALUES ($1, $2, nullif($3, ''), $4) RETURNING id`,
			e.tenant, name, email, status).Scan(&id)
	})
	return id
}

func (e *env) enqueue(kind Kind, to ...string) {
	e.t.Helper()
	e.tx(func(tx pgx.Tx) error {
		return Enqueue(context.Background(), tx, e.tenant, kind, to, approvalData)
	})
}

// statuses returns the status of each queued notification by recipient.
func (e *env) statuses() map[string]string {
	e.t.Helper()
	out := map[string]string{}
	e.tx(func(tx pgx.Tx) error {
		rows, err := tx.Query(context.Background(), `SELECT identity_id, status FROM notification_outbox`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, st string
			if err := rows.Scan(&id, &st); err != nil {
				return err
			}
			out[id] = st
		}
		return rows.Err()
	})
	return out
}

var approvalData = map[string]any{
	"requester_id": "r", "beneficiary_id": "r", "requester": "Ana", "beneficiary": "Ana",
	"target": "CRM users", "application": "CRM", "step": "manager", "justification": "I sell things",
	"duration_days": 30, "due_at": "2026-10-21T08:00:00Z",
}

type sent struct{ from, to, msg string }

type fakeMailer struct {
	mu   sync.Mutex
	err  error
	sent []sent
}

func (m *fakeMailer) Send(_ context.Context, from, to string, msg []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	// Undo quoted-printable so tests can look for whole lines.
	head, body, _ := strings.Cut(string(msg), "\r\n\r\n")
	plain, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if err != nil {
		return err
	}
	m.sent = append(m.sent, sent{from, to, head + "\r\n\r\n" + string(plain)})
	return nil
}

func (e *env) sender(m Mailer) *Sender {
	return &Sender{Pool: e.pool, TenantID: e.tenant, Mailer: m, From: "Grantra <grantra@example.com>",
		PublicURL: "https://grantra.example.com", Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestEnqueueFilters(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana", "ana@example.com", "active")
	noEmail := e.identity("Nomail", "", "active")
	gone := e.identity("Gone", "gone@example.com", "terminated")
	quiet := e.identity("Quiet", "quiet@example.com", "active")
	e.tx(func(tx pgx.Tx) error {
		return SetEnabled(context.Background(), tx, e.tenant, quiet, ApprovalNeeded, false)
	})
	e.enqueue(ApprovalNeeded, ana, ana, "", noEmail, gone, quiet)
	if got := e.statuses(); len(got) != 1 || got[ana] != "pending" {
		t.Errorf("queued = %v, want one for Ana", got)
	}
	// The opt-out is per kind.
	e.enqueue(RequestDecided, quiet)
	if got := e.statuses(); got[quiet] != "pending" {
		t.Errorf("queued = %v, want Quiet to still get decisions", got)
	}
	e.tx(func(tx pgx.Tx) error {
		if err := Enqueue(context.Background(), tx, e.tenant, "nope", []string{ana}, nil); err == nil {
			t.Error("unknown kind queued")
		}
		return nil
	})
}

func TestSettings(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana", "ana@example.com", "active")
	ctx := context.Background()
	e.tx(func(tx pgx.Tx) error {
		if err := SetEnabled(ctx, tx, e.tenant, ana, RequestExpiring, false); err != nil {
			return err
		}
		// Turning it off twice is fine.
		if err := SetEnabled(ctx, tx, e.tenant, ana, RequestExpiring, false); err != nil {
			return err
		}
		s, err := Settings(ctx, tx, ana)
		if err != nil {
			return err
		}
		if len(s) != len(Kinds) {
			t.Fatalf("settings = %v", s)
		}
		for _, x := range s {
			if x.Enabled != (x.Kind != RequestExpiring) {
				t.Errorf("%s enabled = %v", x.Kind, x.Enabled)
			}
		}
		if err := SetEnabled(ctx, tx, e.tenant, ana, RequestExpiring, true); err != nil {
			return err
		}
		s, err = Settings(ctx, tx, ana)
		for _, x := range s {
			if !x.Enabled {
				t.Errorf("%s still off", x.Kind)
			}
		}
		return err
	})
}

func TestSenderDelivers(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana Núñez", "ana@example.com", "active")
	e.enqueue(ApprovalNeeded, ana)
	m := &fakeMailer{}
	res, err := e.sender(m).SendDue(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Sent != 1 || len(m.sent) != 1 {
		t.Fatalf("result = %+v, sent %d", res, len(m.sent))
	}
	got := m.sent[0]
	if got.from != "grantra@example.com" || got.to != "ana@example.com" {
		t.Errorf("envelope = %s -> %s", got.from, got.to)
	}
	for _, want := range []string{
		"Subject: Ana needs your approval for CRM users",
		"Auto-Submitted: auto-generated",
		"X-Grantra-Notification: approval_needed",
		"https://grantra.example.com/approvals",
		"https://grantra.example.com/settings/notifications",
		"Hi Ana Núñez,",
		"Ana asked for CRM users in CRM, and the request is waiting for you.",
		"Wed 21 Oct 2026 08:00 UTC",
	} {
		if !strings.Contains(got.msg, want) {
			t.Errorf("message lacks %q:\n%s", want, got.msg)
		}
	}
	if e.statuses()[ana] != "sent" {
		t.Errorf("status = %v", e.statuses())
	}
	// Nothing is sent twice.
	if res, _ := e.sender(m).SendDue(context.Background()); res != (Result{}) {
		t.Errorf("second pass = %+v", res)
	}
}

func TestSenderRetriesAndGivesUp(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana", "ana@example.com", "active")
	e.enqueue(RequestDecided, ana)
	ctx := context.Background()
	s := e.sender(&fakeMailer{err: errors.New("421 try later")})
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		res, err := s.SendDue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		want := Result{Retried: 1}
		if attempt == maxAttempts {
			want = Result{Failed: 1}
		}
		if res != want {
			t.Fatalf("attempt %d = %+v, want %+v", attempt, res, want)
		}
		// Make the retry due now.
		e.tx(func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE notification_outbox SET next_attempt_at = now()`)
			return err
		})
	}
	var attempts int
	var lastErr string
	e.tx(func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT attempts, last_error FROM notification_outbox`).Scan(&attempts, &lastErr)
	})
	if e.statuses()[ana] != "failed" || attempts != maxAttempts || lastErr != "421 try later" {
		t.Errorf("status %v, attempts %d, error %q", e.statuses(), attempts, lastErr)
	}
}

func TestSenderSkips(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	left := e.identity("Left", "left@example.com", "active")
	quiet := e.identity("Quiet", "quiet@example.com", "active")
	old := e.identity("Old", "old@example.com", "active")
	off := e.identity("Off", "off@example.com", "active")
	e.enqueue(RequestDecided, left, quiet, old)
	e.tx(func(tx pgx.Tx) error {
		// Things change between queueing and sending.
		if _, err := tx.Exec(ctx, `UPDATE identities SET status = 'terminated' WHERE id = $1`, left); err != nil {
			return err
		}
		if err := SetEnabled(ctx, tx, e.tenant, quiet, RequestDecided, false); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE notification_outbox SET created_at = now() - interval '4 days' WHERE identity_id = $1`, old)
		return err
	})
	m := &fakeMailer{}
	res, err := e.sender(m).SendDue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res != (Result{Skipped: 3}) || len(m.sent) != 0 {
		t.Errorf("result = %+v, sent %v", res, m.sent)
	}
	// Without a mailer, email is off: the queue is dropped, not kept.
	e.enqueue(RequestDecided, off)
	if res, _ := e.sender(nil).SendDue(ctx); res != (Result{Skipped: 1}) {
		t.Errorf("no mailer = %+v", res)
	}
	for id, st := range e.statuses() {
		if st != "skipped" {
			t.Errorf("%s is %s", id, st)
		}
	}
}
