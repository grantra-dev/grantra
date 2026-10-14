package sod

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/notify"
)

// digestSize is how many violations one email lists by name.
const digestSize = 20

// notifyAdmins emails the admins one digest of the violations a scan found:
// new ones, and ones whose exception ended.
func notifyAdmins(ctx context.Context, tx pgx.Tx, tenantID string, detected, reopened []string) error {
	ids := append(slices.Clone(detected), reopened...)
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, selectViolations+`
		WHERE v.id = ANY($1::uuid[])
		ORDER BY CASE p.severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END, i.display_name
		LIMIT $2`, ids, digestSize)
	if err != nil {
		return err
	}
	list, err := pgx.CollectRows(rows, scanViolation)
	if err != nil {
		return err
	}
	var shown []map[string]any
	for _, v := range list {
		shown = append(shown, map[string]any{
			"identity": v.IdentityName,
			"policy":   v.PolicyName,
			"severity": v.Severity,
			"reopened": slices.Contains(reopened, v.ID),
		})
	}
	admins, err := notify.Admins(ctx, tx)
	if err != nil {
		return err
	}
	return notify.Enqueue(ctx, tx, tenantID, notify.SoDViolation, admins, map[string]any{
		"count":      len(ids),
		"violations": shown,
		"more":       len(ids) - len(shown),
	})
}
