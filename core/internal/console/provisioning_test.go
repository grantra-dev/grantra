package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/provisioning"
)

func TestProvisioning(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")

	// A SCIM service with one group and no users, that remembers writes.
	var (
		mu      sync.Mutex
		users   []map[string]any
		members []map[string]any
	)
	scim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		if r.Method != http.MethodGet {
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /scim/v2/Users":
			json.NewEncoder(w).Encode(map[string]any{"totalResults": len(users), "Resources": users}) //nolint:errcheck
		case "GET /scim/v2/Groups":
			g := []map[string]any{{"id": "g1", "displayName": "Sales", "members": members}}
			json.NewEncoder(w).Encode(map[string]any{"totalResults": 1, "Resources": g}) //nolint:errcheck
		case "POST /scim/v2/Users":
			users = append(users, map[string]any{"id": "u1", "userName": body["userName"]})
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": "u1"}) //nolint:errcheck
		case "PATCH /scim/v2/Groups/g1":
			members = append(members, map[string]any{"value": "u1"})
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer scim.Close()
	t.Setenv("GRANTRA_SECRET_TEST_CRM_TOKEN", "t0ken")

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`))
	base := "/api/v1/applications/" + app.ID
	cfg := `{"kind": "scim", "config": {"base_url": "` + scim.URL + `/scim/v2", "token": "env:GRANTRA_SECRET_TEST_CRM_TOKEN", "allow_http": true}}`
	if rec := e.do(admin, http.MethodPut, base+"/connector", cfg); rec.Code != http.StatusNoContent {
		t.Fatalf("set connector = %d %s", rec.Code, rec.Body)
	}

	runs := base + "/provisioning-runs"
	if rec := e.do(admin, http.MethodPost, runs+"?dry_run=true", ""); rec.Code != http.StatusConflict {
		t.Errorf("dry run before any aggregation = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(admin, http.MethodPost, base+"/aggregations", ""); rec.Code != http.StatusOK {
		t.Fatalf("aggregate = %d %s", rec.Code, rec.Body)
	}
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by)
			 SELECT $1, $2, id, 'manual', 'test' FROM entitlements WHERE application_id = $3`, e.tenant, ana, app.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	if rec := e.do(admin, http.MethodPost, runs, ""); rec.Code != http.StatusConflict {
		t.Errorf("run while off = %d %s", rec.Code, rec.Body)
	}
	plan := decode[provisioning.Run](t, e.do(admin, http.MethodPost, runs+"?dry_run=true", ""))
	if plan.Status != accounts.RunPlanned || len(plan.Actions) != 2 || len(users) != 0 {
		t.Errorf("dry run = %+v", plan)
	}

	settings := base + "/provisioning"
	if rec := e.do(auditor, http.MethodPut, settings, `{"mode": "on"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor turns provisioning on = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPut, settings, `{"mode": "always"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad mode = %d", rec.Code)
	}
	rec := e.do(admin, http.MethodPut, settings, `{"mode": "on"}`)
	if s := decode[provisioning.Settings](t, rec); rec.Code != http.StatusOK || s.MaxChangePercent != 10 {
		t.Errorf("turn on = %d %+v", rec.Code, s)
	}
	if rec := e.do(auditor, http.MethodPost, runs, ""); rec.Code != http.StatusForbidden {
		t.Errorf("auditor provisions = %d", rec.Code)
	}
	rec = e.do(admin, http.MethodPost, runs, "")
	run := decode[provisioning.Run](t, rec)
	if rec.Code != http.StatusOK || run.Status != accounts.RunSucceeded || len(users) != 1 || len(members) != 1 {
		t.Errorf("run = %d %+v", rec.Code, run)
	}
	if rec := e.do(admin, http.MethodPost, runs+"?force=maybe", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad force = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action IN ('application.provisioning_set', 'application.provisioned', 'provisioning.create_account', 'provisioning.add_member')`); n != 5 {
		t.Errorf("audit events = %d", n)
	}
}
