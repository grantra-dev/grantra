package console

import (
	"context"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/reviews"
)

func TestAccessReviews(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	boss := e.identity("boss", "")
	ana := e.identity("ana", boss)

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`))
	ent := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements", `{"native_id": "users", "name": "CRM users"}`))
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(),
			`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
			e.tenant, ana, ent.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	body := `{"name": "Q4", "application_ids": ["` + app.ID + `"]}`
	for _, who := range []string{auditor, boss} {
		if rec := e.do(who, http.MethodPost, "/api/v1/review-campaigns", body); rec.Code != http.StatusForbidden {
			t.Errorf("launch by non-admin = %d", rec.Code)
		}
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/review-campaigns", body)
	c := decode[reviews.Campaign](t, rec)
	if rec.Code != http.StatusCreated || c.Progress["pending"] != 1 {
		t.Fatalf("launch = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(auditor, http.MethodGet, "/api/v1/review-campaigns/"+c.ID, ""); rec.Code != http.StatusOK {
		t.Errorf("auditor reads campaign = %d", rec.Code)
	}
	if rec := e.do(boss, http.MethodGet, "/api/v1/review-campaigns", ""); rec.Code != http.StatusForbidden {
		t.Errorf("boss lists campaigns = %d", rec.Code)
	}
	if rec := e.do(boss, http.MethodGet, "/api/v1/review-items?scope=all", ""); rec.Code != http.StatusForbidden {
		t.Errorf("boss lists all items = %d", rec.Code)
	}

	mine := decode[struct{ Items []reviews.Item }](t, e.do(boss, http.MethodGet, "/api/v1/review-items", ""))
	if len(mine.Items) != 1 || mine.Items[0].IdentityID != ana {
		t.Fatalf("boss's items = %+v", mine.Items)
	}
	path := "/api/v1/review-items/" + mine.Items[0].ID + "/decision"
	if rec := e.do(ana, http.MethodPost, path, `{"decision": "keep"}`); rec.Code != http.StatusForbidden {
		t.Errorf("ana reviews herself = %d", rec.Code)
	}
	if rec := e.do(auditor, http.MethodPost, path, `{"decision": "keep"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor decides = %d", rec.Code)
	}
	rec = e.do(boss, http.MethodPost, path, `{"decision": "revoke", "comment": "left the team"}`)
	if it := decode[reviews.Item](t, rec); rec.Code != http.StatusOK || it.Decision != reviews.Revoke {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM access_assignments WHERE identity_id = $1`, ana); n != 0 {
		t.Errorf("revoked access is still assigned")
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/review-campaigns/"+c.ID+"/close", ""); rec.Code != http.StatusConflict {
		t.Errorf("close a finished campaign = %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action IN ('review_campaign.launched', 'review_item.decided', 'review_campaign.completed', 'access.removed')`); n != 4 {
		t.Errorf("audit events = %d", n)
	}
}
