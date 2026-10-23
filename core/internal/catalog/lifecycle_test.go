package catalog

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

// TestEveryReferenceIsKnown fails when a migration adds a table pointing at
// entitlements or applications without deciding whether deleting one would
// lose history: add it to entitlementUses, or to the lists below when it
// may go with the application.
func TestEveryReferenceIsKnown(t *testing.T) {
	pool := dbtest.Open(t)
	refs := func(table string) []string {
		rows, err := pool.Query(context.Background(),
			`SELECT DISTINCT conrelid::regclass::text FROM pg_constraint
			 WHERE contype = 'f' AND confrelid = $1::regclass ORDER BY 1`, table)
		if err != nil {
			t.Fatal(err)
		}
		got, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	var known []string
	for _, u := range entitlementUses {
		known = append(known, u.table)
	}
	slices.Sort(known)
	if got := refs("entitlements"); !slices.Equal(got, known) {
		t.Errorf("tables referencing entitlements = %v, entitlementUses has %v", got, known)
	}
	// Accounts block deleting an application; the rest is operational and
	// goes with it (a report schedule narrowed to it too; its packs stay).
	app := []string{"accounts", "app_api_keys", "app_authz_policies", "connector_runs", "entitlements", "jit_provisioning_queue", "report_schedules"}
	if got := refs("applications"); !slices.Equal(got, app) {
		t.Errorf("tables referencing applications = %v, want %v", got, app)
	}
}
