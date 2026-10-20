package console

import (
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/attackpaths"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/insights"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

func TestInsightsAPI(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Cloud"}`))
	ent := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/entitlements",
		`{"name": "Org admin", "risk": "critical"}`))
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, granted_at)
	        VALUES ($1, $2, $3, 'manual', 'test', now() - interval '200 days')`, e.tenant, ana, ent.ID)
	// The cloud says when its accounts last signed in: Ana's, long ago.
	e.exec(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, last_login_at)
	        VALUES ($1, $2, $3, 'ana', now() - interval '120 days')`, e.tenant, app.ID, ana)

	// Admins and auditors read; nobody else does.
	for _, path := range []string{"/api/v1/attack-paths", "/api/v1/attack-paths/" + ent.ID, "/api/v1/identities/" + ana + "/reach",
		"/api/v1/unused-access", "/api/v1/role-suggestions"} {
		if rec := e.do(ana, http.MethodGet, path, ""); rec.Code != http.StatusForbidden {
			t.Errorf("employee GET %s = %d", path, rec.Code)
		}
		if rec := e.do(auditor, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("auditor GET %s = %d %s", path, rec.Code, rec.Body)
		}
	}

	targets := decode[struct{ Targets []attackpaths.Target }](t, e.do(auditor, http.MethodGet, "/api/v1/attack-paths", "")).Targets
	found := false
	for _, tg := range targets {
		if tg.ID == ent.ID {
			found = tg.People == 1 && tg.Standing == 1
		}
	}
	if !found {
		t.Errorf("targets = %+v, want Org admin reached by Ana", targets)
	}
	d := decode[attackpaths.Detail](t, e.do(auditor, http.MethodGet, "/api/v1/attack-paths/"+ent.ID, ""))
	if len(d.Paths) != 1 || d.Paths[0].IdentityID != ana || d.OrphanAccounts == nil {
		t.Errorf("detail = %+v", d)
	}
	reach := decode[struct{ Targets []attackpaths.Reach }](t, e.do(auditor, http.MethodGet, "/api/v1/identities/"+ana+"/reach", "")).Targets
	if len(reach) != 1 || reach[0].ID != ent.ID {
		t.Errorf("Ana reaches %+v", reach)
	}
	for _, path := range []string{"/api/v1/attack-paths/nope", "/api/v1/identities/nope/reach"} {
		if rec := e.do(auditor, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d", path, rec.Code)
		}
	}

	unused := decode[struct {
		Access []insights.Unused
		Days   int
	}](t, e.do(auditor, http.MethodGet, "/api/v1/unused-access?identity="+ana, ""))
	if len(unused.Access) != 1 || unused.Access[0].Name != "Org admin" || unused.Days != 90 {
		t.Errorf("unused = %+v", unused)
	}
	// Two years without use before it counts: nothing is unused yet.
	e.exec(`INSERT INTO tenant_settings (tenant_id, key, value, updated_by) VALUES ($1, $2, 730, 'test')`, e.tenant, settings.UnusedDays)
	longer := decode[struct {
		Access []insights.Unused
		Days   int
	}](t, e.do(auditor, http.MethodGet, "/api/v1/unused-access?identity="+ana, ""))
	if len(longer.Access) != 0 || longer.Days != 730 {
		t.Errorf("unused after two years = %+v", longer)
	}
	e.exec(`DELETE FROM tenant_settings`)
	for _, q := range []string{"identity=nope", "after=nope", "limit=0"} {
		if rec := e.do(auditor, http.MethodGet, "/api/v1/unused-access?"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("unused ?%s = %d", q, rec.Code)
		}
	}
}
