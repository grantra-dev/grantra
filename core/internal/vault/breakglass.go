package vault

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

// setResponders replaces who may open a break-glass account.
func setResponders(ctx context.Context, tx pgx.Tx, tenantID, id string, ids []string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM vault_break_glass_responders WHERE vault_account_id = $1`, id); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx,
		`INSERT INTO vault_break_glass_responders (tenant_id, vault_account_id, identity_id)
		 SELECT $1, $2, i.id FROM identities i WHERE i.id = ANY($3::uuid[]) AND i.status = 'active'`, tenantID, id, ids)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(ids) {
		return apperr.Invalidf("break-glass responders must be active people")
	}
	return nil
}

func loadResponders(ctx context.Context, tx pgx.Tx, list []Account) error {
	ids := make([]string, len(list))
	for i := range list {
		ids[i] = list[i].ID
	}
	rows, err := tx.Query(ctx,
		`SELECT r.vault_account_id, i.id, i.display_name FROM vault_break_glass_responders r JOIN identities i ON i.id = r.identity_id
		 WHERE r.vault_account_id = ANY($1::uuid[]) ORDER BY i.display_name`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			account string
			r       Responder
		)
		if err := rows.Scan(&account, &r.ID, &r.Name); err != nil {
			return err
		}
		if i := slices.IndexFunc(list, func(a Account) bool { return a.ID == account }); i >= 0 {
			list[i].Responders = append(list[i].Responders, r)
		}
	}
	return rows.Err()
}

// Responders returns who may open a vault account in an emergency right
// now: its listed responders who are still active, or the platform admins
// when none is, except whoever last armed it (see break_glass_set_by).
// breakGlass says whether it is a break-glass account at all.
func Responders(ctx context.Context, tx pgx.Tx, id string) (ids []string, breakGlass bool, err error) {
	if !db.ValidUUID(id) {
		return nil, false, apperr.ErrNotFound
	}
	var setBy string
	err = tx.QueryRow(ctx,
		`SELECT break_glass, coalesce(break_glass_set_by, '') FROM vault_accounts WHERE id = $1`, id).Scan(&breakGlass, &setBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, apperr.ErrNotFound
	}
	if err != nil || !breakGlass {
		return nil, breakGlass, err
	}
	rows, err := tx.Query(ctx,
		`SELECT r.identity_id::text FROM vault_break_glass_responders r JOIN identities i ON i.id = r.identity_id
		 WHERE r.vault_account_id = $1 AND i.status = 'active' ORDER BY 1`, id)
	if err != nil {
		return nil, true, err
	}
	if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, true, err
	}
	if len(ids) == 0 {
		if ids, err = notify.Admins(ctx, tx); err != nil {
			return nil, true, err
		}
	}
	return slices.DeleteFunc(ids, func(i string) bool { return db.IdentityActor(i) == setBy }), true, nil
}

// BreakGlassUse returns one break-glass check-out.
func BreakGlassUse(ctx context.Context, tx pgx.Tx, id string) (Checkout, error) {
	if !db.ValidUUID(id) {
		return Checkout{}, apperr.ErrNotFound
	}
	list, err := checkouts(ctx, tx, `c.id = $1 AND c.break_glass`, []any{id})
	if err != nil {
		return Checkout{}, err
	}
	if len(list) == 0 {
		return Checkout{}, apperr.ErrNotFound
	}
	return list[0], nil
}

// BreakGlassUses lists the latest break-glass check-outs, only the ones
// still to review with pending.
func BreakGlassUses(ctx context.Context, tx pgx.Tx, pending bool) ([]Checkout, error) {
	where := `c.break_glass`
	if pending {
		where += ` AND c.review_status = 'pending'`
	}
	return checkouts(ctx, tx, where, nil)
}

// ReviewBreakGlass records an admin's review of a break-glass use: justified,
// or flagged with a reason. The caller has checked the reviewer is not the
// person who used it.
func ReviewBreakGlass(ctx context.Context, tx pgx.Tx, tenantID, actor, id string, justified bool, comment string) (Checkout, error) {
	comment = strings.TrimSpace(comment)
	switch {
	case len(comment) > 2000:
		return Checkout{}, apperr.Invalidf("comment must be at most 2000 characters")
	case !justified && comment == "":
		return Checkout{}, apperr.Invalidf("say why the use is flagged")
	}
	status := "flagged"
	if justified {
		status = "justified"
	}
	var account string
	err := tx.QueryRow(ctx,
		`UPDATE vault_checkouts SET review_status = $2, reviewed_by = $3, reviewed_at = now(), review_comment = nullif($4, '')
		 WHERE id = $1 AND review_status = 'pending' RETURNING vault_account_id`, id, status, actor, comment).Scan(&account)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := BreakGlassUse(ctx, tx, id); err != nil {
			return Checkout{}, err
		}
		return Checkout{}, apperr.Conflictf("this break-glass use was already reviewed")
	}
	if err != nil {
		return Checkout{}, err
	}
	if err := db.Audit(ctx, tx, tenantID, actor, "vault.break_glass_reviewed", "vault_account", account,
		map[string]any{"checkout_id": id, "review_status": status, "comment": comment}); err != nil {
		return Checkout{}, err
	}
	return BreakGlassUse(ctx, tx, id)
}

// RemindBreakGlassReviews emails the admins about break-glass uses still
// waiting for review a while (the tenant's settings.BreakGlassReminderHours)
// after they started or were last reminded. Each admin hears about the uses they may review: not their own.
func RemindBreakGlassReviews(ctx context.Context, tx pgx.Tx, tenantID string) (int, error) {
	rows, err := tx.Query(ctx,
		`UPDATE vault_checkouts c SET reminded_at = now()
		 FROM vault_accounts va, identities i
		 WHERE va.id = c.vault_account_id AND i.id = c.identity_id AND c.review_status = 'pending'
		   AND coalesce(c.reminded_at, c.checked_out_at) <= now() - make_interval(hours => `+settings.SQL(settings.BreakGlassReminderHours)+`)
		 RETURNING c.identity_id::text, i.display_name, va.name, c.reason, c.checked_out_at`)
	if err != nil {
		return 0, err
	}
	type use struct {
		identity, person, account, reason string
		at                                time.Time
	}
	uses, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (use, error) {
		var u use
		err := row.Scan(&u.identity, &u.person, &u.account, &u.reason, &u.at)
		return u, err
	})
	if err != nil || len(uses) == 0 {
		return 0, err
	}
	admins, err := notify.Members(ctx, tx, authz.RoleAdmin)
	if err != nil {
		return 0, err
	}
	for _, admin := range admins {
		var list []any
		for _, u := range uses {
			if u.identity != admin {
				list = append(list, map[string]any{"person": u.person, "account": u.account, "reason": u.reason,
					"checked_out_at": u.at.UTC().Format(time.RFC3339)})
			}
		}
		if len(list) == 0 {
			continue
		}
		if err := notify.Enqueue(ctx, tx, tenantID, notify.BreakGlassDue, []string{admin},
			map[string]any{"count": len(list), "uses": list}); err != nil {
			return 0, err
		}
	}
	return len(uses), nil
}

// RemindDue runs RemindBreakGlassReviews in its own transaction.
func RemindDue(ctx context.Context, pool *pgxpool.Pool, tenantID string) (n int, err error) {
	err = db.InTenant(ctx, pool, tenantID, func(tx pgx.Tx) error {
		n, err = RemindBreakGlassReviews(ctx, tx, tenantID)
		return err
	})
	return n, err
}
