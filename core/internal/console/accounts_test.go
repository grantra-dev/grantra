package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
	_ "github.com/grantra-dev/grantra/core/internal/connector/okta"
	_ "github.com/grantra-dev/grantra/core/internal/connector/scim"
	"github.com/grantra-dev/grantra/core/internal/vault"
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
	t.Setenv("GRANTRA_SECRET_TEST_CRM_TOKEN", "t0ken")

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "CRM"}`))
	conn := "/api/v1/applications/" + app.ID + "/connector"
	cfg := `{"kind": "scim", "config": {"base_url": "` + scim.URL + `/scim/v2", "token": "env:GRANTRA_SECRET_TEST_CRM_TOKEN", "allow_http": true}}`
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

// TestConnectorSecrets types an Okta token in the console and reads Okta
// with it.
func TestConnectorSecrets(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)

	okta := connectortest.New(t, func(r *http.Request) bool { return r.Header.Get("Authorization") == "SSWS 00typed-in-console" })
	okta.JSON("GET /api/v1/groups", []map[string]any{{"id": "g1", "profile": map[string]string{"name": "Finance"}}})
	okta.JSON("GET /api/v1/groups/g1/users", []map[string]any{{"id": "u1"}})
	okta.JSON("GET /api/v1/iam/assignees/users", map[string]any{"value": []any{}})
	okta.JSON("GET /api/v1/users", []map[string]any{{"id": "u1", "status": "ACTIVE", "profile": map[string]string{"login": "ana@example.com", "email": "ana@example.com"}}})

	secret := `{"label": "Okta API token", "value": "00typed-in-console"}`
	if rec := e.do(admin, http.MethodPost, "/api/v1/connector-secrets", secret); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), vault.KeyEnv) {
		t.Fatalf("store a secret with no vault key = %d %s", rec.Code, rec.Body)
	}
	key, err := vault.NewLocalKey("MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if err != nil {
		t.Fatal(err)
	}
	e.api.Secrets.Keys = key
	if rec := e.do(auditor, http.MethodPost, "/api/v1/connector-secrets", secret); rec.Code != http.StatusForbidden {
		t.Errorf("auditor stores a secret = %d", rec.Code)
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/connector-secrets", secret)
	ref := decode[struct{ Ref string }](t, rec).Ref
	if rec.Code != http.StatusCreated || !strings.HasPrefix(ref, "secret:") {
		t.Fatalf("store a secret = %d %s", rec.Code, ref)
	}

	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Okta"}`))
	cfg := `{"kind": "okta", "config": {"org_url": "` + okta.URL + `", "token": "` + ref + `", "allow_http": true, "apps": false}}`
	if rec := e.do(admin, http.MethodPut, "/api/v1/applications/"+app.ID+"/connector", cfg); rec.Code != http.StatusNoContent {
		t.Fatalf("set connector = %d %s", rec.Code, rec.Body)
	}
	rec = e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/aggregations", "")
	if run := decode[accounts.Run](t, rec); rec.Code != http.StatusOK || run.Counts["accounts"] != 1 {
		t.Fatalf("aggregate = %d %+v", rec.Code, run)
	}
	// The secret is in no response and no audit event.
	if n := e.count(`SELECT count(*) FROM audit_events WHERE details::text LIKE '%typed-in-console%'`); n != 0 {
		t.Errorf("the secret is in %d audit events", n)
	}
}
