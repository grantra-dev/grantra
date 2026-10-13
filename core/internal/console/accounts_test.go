package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	_ "github.com/grantra-dev/grantra/core/internal/connector/scim"
)

func TestConnectorsAndAccounts(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")

	scim := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer t0ken" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var res []map[string]any
		switch r.URL.Path {
		case "/scim/v2/Users":
			res = []map[string]any{
				{"id": "u1", "userName": "ana@example.com", "emails": []map[string]any{{"value": "ana@example.com"}}},
				{"id": "u2", "userName": "ghost"},
			}
		case "/scim/v2/Groups":
			res = []map[string]any{{"id": "g1", "displayName": "Sales", "members": []map[string]any{{"value": "u1"}, {"value": "u2"}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"totalResults": len(res), "Resources": res}) //nolint:errcheck
	}))
	defer scim.Close()
	t.Setenv("TEST_CRM_TOKEN", "t0ken")

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`))
	conn := "/api/v1/applications/" + app.ID + "/connector"
	cfg := `{"kind": "scim", "config": {"base_url": "` + scim.URL + `/scim/v2", "token": "env:TEST_CRM_TOKEN", "allow_http": true}}`
	if rec := e.do(auditor, http.MethodPut, conn, cfg); rec.Code != http.StatusForbidden {
		t.Errorf("auditor sets connector = %d", rec.Code)
	}
	for _, bad := range []string{
		`{"kind": "scim", "config": {"base_url": "` + scim.URL + `", "token": "env:NOT_SET_ANYWHERE", "allow_http": true}}`,
		`{"kind": "scim", "config": {"base_url": "` + scim.URL + `", "token": "t0ken-in-clear", "allow_http": true}}`,
		`{"kind": "ldap"}`,
	} {
		if rec := e.do(admin, http.MethodPut, conn, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("bad connector %s = %d %s", bad, rec.Code, rec.Body)
		}
	}
	if rec := e.do(admin, http.MethodPut, conn, cfg); rec.Code != http.StatusNoContent {
		t.Fatalf("set connector = %d %s", rec.Code, rec.Body)
	}

	agg := "/api/v1/applications/" + app.ID + "/aggregations"
	if rec := e.do(auditor, http.MethodPost, agg, ""); rec.Code != http.StatusForbidden {
		t.Errorf("auditor aggregates = %d", rec.Code)
	}
	rec := e.do(admin, http.MethodPost, agg, "")
	run := decode[accounts.Run](t, rec)
	if rec.Code != http.StatusOK || run.Counts["accounts"] != 2 || run.Counts["orphans"] != 1 {
		t.Fatalf("aggregate = %d %+v", rec.Code, run)
	}

	orphans := decode[struct{ Accounts []accounts.Account }](t, e.do(auditor, http.MethodGet, "/api/v1/applications/"+app.ID+"/accounts?orphans=true", ""))
	if len(orphans.Accounts) != 1 || orphans.Accounts[0].NativeID != "u2" || orphans.Accounts[0].Entitlements[0] != "Sales" {
		t.Fatalf("orphans = %+v", orphans.Accounts)
	}
	link := "/api/v1/accounts/" + orphans.Accounts[0].ID + "/identity"
	if rec := e.do(auditor, http.MethodPut, link, `{"identity_id": "`+ana+`"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor links = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPut, link, `{"identity_id": "`+ana+`"}`); rec.Code != http.StatusOK {
		t.Errorf("link = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(admin, http.MethodPut, link, `{"identity_id": "00000000-0000-0000-0000-000000000000"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("link to nobody real = %d", rec.Code)
	}

	// Ana sees both her accounts.
	mine := decode[struct{ Accounts []accounts.Account }](t, e.do(ana, http.MethodGet, "/api/v1/identities/"+ana+"/accounts", ""))
	if len(mine.Accounts) != 2 {
		t.Errorf("ana's accounts = %+v", mine.Accounts)
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/identities/"+admin+"/accounts", ""); rec.Code != http.StatusForbidden {
		t.Errorf("ana reads admin's accounts = %d", rec.Code)
	}

	// A broken connector answers 502 and is recorded.
	scim.Close()
	if rec := e.do(admin, http.MethodPost, agg, ""); rec.Code != http.StatusBadGateway {
		t.Errorf("aggregate with the service down = %d", rec.Code)
	}
	runs := decode[struct{ Runs []accounts.Run }](t, e.do(auditor, http.MethodGet, "/api/v1/applications/"+app.ID+"/runs", ""))
	if len(runs.Runs) != 2 || runs.Runs[0].Status != accounts.RunFailed {
		t.Errorf("runs = %+v", runs.Runs)
	}
	apps := decode[struct{ Applications []catalog.Application }](t, e.do(auditor, http.MethodGet, "/api/v1/applications", ""))
	for _, a := range apps.Applications {
		if a.ID == app.ID && (a.Connector != "scim" || string(a.Config) == "{}") {
			t.Errorf("application = %+v", a)
		}
	}
}
