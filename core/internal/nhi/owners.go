package nhi

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/notify"
)

// handover is a non-human identity that passed from an owner who left to
// someone else, or to nobody.
type handover struct {
	ID, Name, Kind, From, ToID, To string
}

// OwnersLeft hands the non-human identities that the people in leavers own
// to each leaver's manager, when actor (the HR sync or the end-date check)
// makes them leavers. A leaver with no active manager leaves them with no
// owner, flagged for an admin. Admins hear about every one, and each new
// owner about what they got.
func OwnersLeft(ctx context.Context, tx pgx.Tx, tenantID, actor string, leavers []string) error {
	if len(leavers) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		WITH moved AS (
			SELECT n.id, l.display_name AS from_name, m.id AS to_id, m.display_name AS to_name
			FROM identities n
			JOIN identities l ON l.id = n.manager_id
			LEFT JOIN identities m ON m.id = l.manager_id AND m.status = 'active' AND m.kind = 'human'
			WHERE n.kind <> 'human' AND n.status <> 'terminated' AND n.manager_id = ANY($1::uuid[])
			FOR UPDATE OF n
		)
		UPDATE identities n SET manager_id = moved.to_id, updated_at = now()
		FROM moved WHERE n.id = moved.id
		RETURNING n.id, n.display_name, n.kind, moved.from_name, coalesce(moved.to_id::text, ''), coalesce(moved.to_name, '')`,
		leavers)
	if err != nil {
		return err
	}
	moved, err := pgx.CollectRows(rows, pgx.RowToStructByPos[handover])
	if err != nil || len(moved) == 0 {
		return err
	}
	err = db.SendBatch(ctx, tx, moved, func(b *pgx.Batch, h handover) {
		db.QueueAudit(b, tenantID, actor, "identity.owner_changed", "identity", h.ID,
			map[string]any{"display_name": h.Name, "from": h.From, "owner_id": h.ToID, "to": h.To, "reason": "owner left"})
	}, func(br pgx.BatchResults, _ handover) error { return db.ReadAudit(br, "identity.owner_changed") })
	if err != nil {
		return err
	}

	item := func(h handover) map[string]any {
		return map[string]any{"name": h.Name, "kind": h.Kind, "from": h.From, "to": h.To}
	}
	all := make([]map[string]any, len(moved))
	byOwner := map[string][]map[string]any{}
	for i, h := range moved {
		all[i] = item(h)
		if h.ToID != "" {
			byOwner[h.ToID] = append(byOwner[h.ToID], item(h))
		}
	}
	admins, err := notify.Admins(ctx, tx)
	if err != nil {
		return err
	}
	if err := notify.Enqueue(ctx, tx, tenantID, notify.OwnerLeft, admins,
		map[string]any{"identities": all, "count": len(all)}); err != nil {
		return err
	}
	for owner, list := range byOwner {
		if err := notify.Enqueue(ctx, tx, tenantID, notify.OwnerLeft, []string{owner},
			map[string]any{"identities": list, "count": len(list), "yours": true}); err != nil {
			return err
		}
	}
	return nil
}
