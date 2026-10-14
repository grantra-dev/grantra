package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net/mail"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
)

// Mailer delivers one message to one address.
type Mailer interface {
	Send(ctx context.Context, from, to string, msg []byte) error
}

// Sender delivers the outbox.
type Sender struct {
	Pool     *pgxpool.Pool
	TenantID string
	// Mailer is nil when email is not set up; notifications are then
	// skipped rather than kept, so turning email on later does not send a
	// backlog of stale ones.
	Mailer Mailer
	// From is the sender address, like "Grantra <grantra@example.com>".
	From string
	// PublicURL is where people reach the console, for links.
	PublicURL string
	Log       *slog.Logger
}

const (
	// maxAttempts is how often a send is tried before it is given up.
	maxAttempts = 6
	// maxAge is how old a notification may be and still be sent: past it,
	// the news is stale.
	maxAge = 72 * time.Hour
	// keep is how long delivered and given-up notifications stay on record.
	keep = 30 * 24 * time.Hour
	// batch is how many notifications one pass sends at most.
	batch = 100
)

// retryAfter is how long to wait before the next attempt, by attempts made.
var retryAfter = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Result counts what a pass did.
type Result struct {
	Sent    int `json:"sent"`
	Retried int `json:"retried"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`
}

// SendDue sends the notifications that are due, one at a time.
func (s *Sender) SendDue(ctx context.Context) (Result, error) {
	var res Result
	err := db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM notification_outbox WHERE status <> 'pending' AND created_at < now() - make_interval(secs => $1)`,
			keep.Seconds())
		return err
	})
	if err != nil {
		return res, err
	}
	for range batch {
		outcome, err := s.sendOne(ctx)
		if err != nil || outcome == "" {
			return res, err
		}
		switch outcome {
		case "sent":
			res.Sent++
		case "retry":
			res.Retried++
		case "failed":
			res.Failed++
		case "skipped":
			res.Skipped++
		}
	}
	return res, nil
}

// sendOne sends the oldest due notification. It returns "" when none is due.
func (s *Sender) sendOne(ctx context.Context) (string, error) {
	var outcome string
	err := db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) error {
		var (
			id         int64
			kind       Kind
			data       map[string]any
			attempts   int
			createdAt  time.Time
			identityID string
			name       string
			email      string
			active     bool
			optedOut   bool
		)
		err := tx.QueryRow(ctx,
			`SELECT o.id, o.kind, o.data, o.attempts, o.created_at, i.id, i.display_name, coalesce(i.email, ''),
			        i.status = 'active',
			        EXISTS (SELECT 1 FROM notification_opt_outs x WHERE x.identity_id = i.id AND x.kind = o.kind)
			 FROM notification_outbox o JOIN identities i ON i.id = o.identity_id
			 WHERE o.status = 'pending' AND o.next_attempt_at <= now()
			 ORDER BY o.next_attempt_at, o.id LIMIT 1
			 FOR UPDATE OF o SKIP LOCKED`).
			Scan(&id, &kind, &data, &attempts, &createdAt, &identityID, &name, &email, &active, &optedOut)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		skip := func(reason string) error {
			outcome = "skipped"
			_, err := tx.Exec(ctx, `UPDATE notification_outbox SET status = 'skipped', last_error = $2 WHERE id = $1`, id, reason)
			return err
		}
		switch {
		case s.Mailer == nil:
			return skip("email is not set up")
		case !active || email == "":
			return skip("the recipient is not active or has no email")
		case optedOut:
			return skip("the recipient turned these emails off")
		case time.Since(createdAt) > maxAge:
			return skip("too old to send")
		}
		msg, err := s.message(id, kind, data, identityID, name, email)
		if err != nil {
			// A template that cannot render will not render next time either.
			s.Log.ErrorContext(ctx, "render notification", "id", id, "kind", kind, "err", err)
			outcome = "failed"
			_, err := tx.Exec(ctx, `UPDATE notification_outbox SET status = 'failed', last_error = $2 WHERE id = $1`, id, err.Error())
			return err
		}
		from, _ := mail.ParseAddress(s.From)
		if sendErr := s.Mailer.Send(ctx, from.Address, email, msg); sendErr != nil {
			attempts++
			if attempts >= maxAttempts {
				outcome = "failed"
				s.Log.WarnContext(ctx, "gave up sending a notification", "id", id, "kind", kind, "err", sendErr)
				_, err := tx.Exec(ctx,
					`UPDATE notification_outbox SET status = 'failed', attempts = $2, last_error = $3 WHERE id = $1`,
					id, attempts, sendErr.Error())
				return err
			}
			outcome = "retry"
			s.Log.WarnContext(ctx, "notification not sent, will retry", "id", id, "kind", kind, "attempt", attempts, "err", sendErr)
			_, err := tx.Exec(ctx,
				`UPDATE notification_outbox SET attempts = $2, last_error = $3, next_attempt_at = now() + make_interval(secs => $4)
				 WHERE id = $1`, id, attempts, sendErr.Error(), retryAfter[attempts-1].Seconds())
			return err
		}
		outcome = "sent"
		_, err = tx.Exec(ctx,
			`UPDATE notification_outbox SET status = 'sent', attempts = attempts + 1, sent_at = now(), last_error = NULL WHERE id = $1`, id)
		return err
	})
	return outcome, err
}

// message builds the email for one notification.
func (s *Sender) message(id int64, kind Kind, data map[string]any, identityID, name, email string) ([]byte, error) {
	subject, body, err := render(kind, view{
		Name:        name,
		RecipientID: identityID,
		URL:         s.PublicURL + pages[kind],
		Data:        data,
		SettingsURL: s.PublicURL + "/settings/notifications",
	})
	if err != nil {
		return nil, err
	}
	from, err := mail.ParseAddress(s.From)
	if err != nil {
		return nil, fmt.Errorf("from address: %w", err)
	}
	to := mail.Address{Name: name, Address: email}
	token := make([]byte, 8)
	_, _ = rand.Read(token)
	domain := from.Address[bytes.LastIndexByte([]byte(from.Address), '@')+1:]

	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<%d.%s@%s>", id, hex.EncodeToString(token), domain))
	header("MIME-Version", "1.0")
	header("Content-Type", `text/plain; charset="utf-8"`)
	header("Content-Transfer-Encoding", "quoted-printable")
	// Out-of-office replies and mail loops stay out.
	header("Auto-Submitted", "auto-generated")
	header("X-Grantra-Notification", string(kind))
	b.WriteString("\r\n")
	qp := quotedprintable.NewWriter(&b)
	if _, err := qp.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := qp.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
