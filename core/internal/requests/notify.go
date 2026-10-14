package requests

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
)

// ReminderWindow is how long before a step expires its approvers get a
// reminder.
const ReminderWindow = 24 * time.Hour

// mailData is what the request emails show, captured when the event happens.
func mailData(r Request) map[string]any {
	d := map[string]any{
		"request_id":     r.ID,
		"requester_id":   r.RequesterID,
		"requester":      r.RequesterName,
		"beneficiary_id": r.BeneficiaryID,
		"beneficiary":    r.BeneficiaryName,
		"target":         r.TargetName,
		"application":    r.Application,
		"justification":  r.Justification,
		"duration_days":  r.DurationDays,
		"status":         r.Status,
		"step":           "",
		"due_at":         "",
		"decided_by":     "",
		"comment":        "",
		"expires_at":     "",
	}
	if s := r.Current(); s != nil {
		d["step"] = s.Kind
		if s.DueAt != nil {
			d["due_at"] = s.DueAt.UTC().Format(time.RFC3339)
		}
	}
	return d
}

// approvers returns who decides the step a request waits on: its approver,
// or the admins when it has none or they are no longer active. Nobody is
// asked about their own access.
func approvers(ctx context.Context, tx pgx.Tx, r Request) ([]string, error) {
	s := r.Current()
	if s == nil {
		return nil, nil
	}
	var to []string
	if s.ApproverID != "" && s.approverActive {
		to = []string{s.ApproverID}
	} else {
		admins, err := notify.Admins(ctx, tx)
		if err != nil {
			return nil, err
		}
		to = admins
	}
	return slices.DeleteFunc(to, func(id string) bool { return id == r.BeneficiaryID }), nil
}

// notifyApprovers tells whoever decides the step a request now waits on.
func notifyApprovers(ctx context.Context, tx pgx.Tx, tenantID, id string, kind notify.Kind) error {
	r, err := Get(ctx, tx, id, false)
	if err != nil {
		return err
	}
	to, err := approvers(ctx, tx, r)
	if err != nil {
		return err
	}
	return notify.Enqueue(ctx, tx, tenantID, kind, to, mailData(r))
}

// notifyDecided tells the requester and the person the access is for how a
// request ended. Withdrawn requests and those for people who left say
// nothing: the people involved already know, or are gone.
func notifyDecided(ctx context.Context, tx pgx.Tx, tenantID, id string) error {
	r, err := Get(ctx, tx, id, false)
	if err != nil {
		return err
	}
	if r.Status != Approved && r.Status != Rejected && r.Status != Expired {
		return nil
	}
	d := mailData(r)
	for _, s := range r.Steps {
		if s.Status != StepRejected {
			continue
		}
		d["comment"] = s.Comment
		d["decided_by"] = s.DecidedBy
		if who, ok := strings.CutPrefix(s.DecidedBy, "identity:"); ok {
			var name string
			if err := tx.QueryRow(ctx, `SELECT display_name FROM identities WHERE id = $1`, who).Scan(&name); err == nil {
				d["decided_by"] = name
			}
		}
	}
	if r.Status == Approved {
		var expires *time.Time
		if err := tx.QueryRow(ctx,
			`SELECT expires_at FROM access_assignments WHERE request_id = $1`, r.ID).Scan(&expires); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if expires != nil {
			d["expires_at"] = expires.UTC().Format(time.RFC3339)
		}
	}
	return notify.Enqueue(ctx, tx, tenantID, notify.RequestDecided, []string{r.RequesterID, r.BeneficiaryID}, d)
}

// RemindDue reminds approvers once about steps that expire within
// ReminderWindow. It returns how many steps it reminded about.
func RemindDue(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int, error) {
	var n int
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`UPDATE access_request_steps s SET reminded_at = now()
			 FROM access_requests r
			 WHERE r.id = s.request_id AND r.status = 'pending' AND s.status = 'pending' AND s.reminded_at IS NULL
			   AND s.due_at > now() AND s.due_at <= now() + make_interval(secs => $1)
			 RETURNING s.request_id`, ReminderWindow.Seconds())
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := notifyApprovers(ctx, tx, tenantID, id, notify.RequestExpiring); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}
