package itdr

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/notify"
)

// digestSize is how many alerts one email lists.
const digestSize = 20

// notifyAdmins emails the admins one digest of the high and critical
// alerts a scan opened or made more severe. Medium and low ones wait on
// the Threats page.
func notifyAdmins(ctx context.Context, tx pgx.Tx, tenantID string, found []raised) error {
	var serious []raised
	for _, r := range found {
		if severityRank[r.severity] >= severityRank["high"] {
			serious = append(serious, r)
		}
	}
	if len(serious) == 0 {
		return nil
	}
	slices.SortStableFunc(serious, func(a, b raised) int { return severityRank[b.severity] - severityRank[a.severity] })
	admins, err := notify.Admins(ctx, tx)
	if err != nil {
		return err
	}
	// Nobody is told about a threat to themselves: whoever took over their
	// account would read it too. The other admins deal with it.
	for _, admin := range admins {
		var shown []map[string]any
		count := 0
		for _, r := range serious {
			if r.identityID == admin {
				continue
			}
			count++
			if len(shown) < digestSize {
				shown = append(shown, map[string]any{"summary": r.summary, "severity": r.severity, "escalated": !r.created})
			}
		}
		if count == 0 {
			continue
		}
		if err := notify.Enqueue(ctx, tx, tenantID, notify.ThreatDetected, []string{admin}, map[string]any{
			"count": count, "alerts": shown, "more": count - len(shown)}); err != nil {
			return err
		}
	}
	return nil
}
