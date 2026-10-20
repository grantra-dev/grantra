package reviews

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// notifyReviewers emails everyone with items left to review in campaign
// id, with how many. Items with nobody to ask count for every admin.
func notifyReviewers(ctx context.Context, tx pgx.Tx, tenantID, id string, kind notify.Kind) error {
	var name, onMissing string
	var due time.Time
	if err := tx.QueryRow(ctx, `SELECT name, on_missing, due_at FROM review_campaigns WHERE id = $1`, id).
		Scan(&name, &onMissing, &due); err != nil {
		return err
	}
	rows, err := tx.Query(ctx,
		`SELECT coalesce(reviewer_id::text, ''), count(*) FROM review_items
		 WHERE campaign_id = $1 AND decision = 'pending' GROUP BY 1`, id)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for rows.Next() {
		var reviewer string
		var n int
		if err := rows.Scan(&reviewer, &n); err != nil {
			return err
		}
		counts[reviewer] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	unassigned := counts[""]
	delete(counts, "")
	admins := map[string]bool{}
	if unassigned > 0 {
		ids, err := notify.Admins(ctx, tx)
		if err != nil {
			return err
		}
		for _, a := range ids {
			counts[a] += unassigned
			admins[a] = true
		}
	}
	for reviewer, n := range counts {
		data := map[string]any{
			"campaign_id": id,
			"campaign":    name,
			"due_at":      due.UTC().Format(time.RFC3339),
			"on_missing":  onMissing,
			"items":       n,
			"admin_items": admins[reviewer],
		}
		if err := notify.Enqueue(ctx, tx, tenantID, kind, []string{reviewer}, data); err != nil {
			return err
		}
	}
	return nil
}

// RemindDue reminds reviewers once about campaigns that end within the
// tenant's settings.ReviewReminderHours. It returns how many campaigns it
// reminded about.
func RemindDue(ctx context.Context, pool *pgxpool.Pool, tenantID string) (int, error) {
	var n int
	err := db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`UPDATE review_campaigns SET reminded_at = now()
			 WHERE status = 'active' AND reminded_at IS NULL
			   AND due_at > now() AND due_at <= now() + make_interval(hours => `+settings.SQL(settings.ReviewReminderHours)+`)
			 RETURNING id`)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := notifyReviewers(ctx, tx, tenantID, id, notify.CampaignDueSoon); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}
