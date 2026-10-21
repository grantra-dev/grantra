package entra

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
)

// fakeGraph is a tenant with two groups, the Global Administrator role and
// three users, one of them disabled. signIns says whether the tenant's
// licence shows last sign-ins.
func fakeGraph(t *testing.T, signIns bool) *connectortest.Server {
	t.Helper()
	srv := connectortest.New(t, func(r *http.Request) bool {
		return strings.HasSuffix(r.URL.Path, "/token") || r.Header.Get("Authorization") == "Bearer graph-token"
	})
	srv.Handle("POST /contoso.onmicrosoft.com/oauth2/v2.0/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //nolint:errcheck
		id, secret, _ := r.BasicAuth()
		if id != "app-1" || secret != "SECRET" || r.Form.Get("scope") != srv.URL+"/.default" {
			connectortest.Reply(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client", "error_description": "AADSTS7000215: Invalid client secret"})
			return
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"access_token": "graph-token", "token_type": "Bearer", "expires_in": 3600})
	})
	srv.Handle("GET /v1.0/groups", func(w http.ResponseWriter, r *http.Request) {
		// Two pages, to follow @odata.nextLink.
		if r.URL.Query().Get("$skiptoken") == "" {
			connectortest.Reply(w, http.StatusOK, map[string]any{
				"value":           []map[string]any{{"id": "g1", "displayName": "Finance", "description": "Money people", "securityEnabled": true}},
				"@odata.nextLink": srv.URL + "/v1.0/groups?$skiptoken=2",
			})
			return
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"value": []map[string]any{{"id": "g2", "displayName": "All staff"}}})
	})
	srv.JSON("GET /v1.0/groups/g1/members", map[string]any{"value": []map[string]any{
		{"@odata.type": "#microsoft.graph.user", "id": "u1"},
		{"@odata.type": "#microsoft.graph.group", "id": "g2"}, // nested groups are not accounts
	}})
	srv.JSON("GET /v1.0/groups/g2/members", map[string]any{"value": []map[string]any{{"id": "u1"}, {"id": "u2"}, {"id": "u3"}}})
	srv.JSON("GET /v1.0/directoryRoles", map[string]any{"value": []map[string]any{{"id": "r1", "displayName": "Global Administrator", "description": "Can do anything"}}})
	srv.JSON("GET /v1.0/directoryRoles/r1/members", map[string]any{"value": []map[string]any{{"id": "u1"}}})
	srv.Handle("GET /v1.0/users", func(w http.ResponseWriter, r *http.Request) {
		withSignIns := strings.Contains(r.URL.Query().Get("$select"), "signInActivity")
		if withSignIns && !signIns {
			connectortest.Reply(w, http.StatusForbidden, map[string]any{"error": map[string]string{
				"code": "Authentication_RequestFromNonPremiumTenantOrB2CTenant", "message": "Neither tenant is B2C or tenant doesn't have premium license"}})
			return
		}
		users := []map[string]any{
			{"id": "u1", "userPrincipalName": "ana@contoso.com", "mail": "ana@contoso.com", "displayName": "Ana", "employeeId": "E1",
				"accountEnabled": true, "jobTitle": "CFO", "department": "Finance", "userType": "Member"},
			{"id": "u2", "userPrincipalName": "luis@contoso.com", "displayName": "Luis", "accountEnabled": false},
			{"id": "u3", "userPrincipalName": "guest_x#EXT#@contoso.com", "mail": "guest@partner.com", "userType": "Guest"},
		}
		if withSignIns {
			users[0]["signInActivity"] = map[string]any{"lastSignInDateTime": "2026-10-01T08:00:00Z"}
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"value": users})
	})
	return srv
}

func newClient(t *testing.T, srv *connectortest.Server, secret string) *Client {
	t.Helper()
	cfg, _ := json.Marshal(Config{TenantID: "contoso.onmicrosoft.com", ClientID: "app-1", ClientSecret: secret,
		GraphURL: srv.URL, LoginURL: srv.URL, AllowHTTP: true})
	r, err := New(cfg, connectortest.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*Client)
}

func TestRead(t *testing.T) {
	for _, signIns := range []bool{true, false} {
		srv := fakeGraph(t, signIns)
		snap, err := newClient(t, srv, "env:SECRET").Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range snap.Entitlements {
			names = append(names, e.NativeID+"="+e.Name)
		}
		if want := []string{"g1=Finance", "g2=All staff", "role:r1=Global Administrator (directory role)"}; !slices.Equal(names, want) {
			t.Errorf("entitlements %v, want %v", names, want)
		}
		if len(snap.Accounts) != 3 {
			t.Fatalf("got %d accounts", len(snap.Accounts))
		}
		ana, luis, guest := snap.Accounts[0], snap.Accounts[1], snap.Accounts[2]
		if ana.Username != "ana@contoso.com" || ana.ExternalID != "E1" || !ana.Active || ana.Attributes["title"] != "CFO" ||
			!slices.Equal(ana.Entitlements, []string{"g1", "g2", "role:r1"}) {
			t.Errorf("ana = %+v", ana)
		}
		if luis.Active || luis.Email != "luis@contoso.com" {
			t.Errorf("luis = %+v", luis)
		}
		if guest.Email != "guest@partner.com" || guest.Attributes["user_type"] != "Guest" {
			t.Errorf("guest = %+v", guest)
		}
		wantLogin := time.Time{}
		if signIns {
			wantLogin = time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
		}
		if !ana.LastLogin.Equal(wantLogin) {
			t.Errorf("with sign-ins %v: last login %v", signIns, ana.LastLogin)
		}
	}
}

func TestWrongSecret(t *testing.T) {
	srv := fakeGraph(t, true)
	_, err := newClient(t, srv, "env:WRONG").Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Invalid client secret") {
		t.Fatalf("got %v", err)
	}
}

func TestWrites(t *testing.T) {
	srv := fakeGraph(t, true)
	srv.Status("POST /v1.0/groups/g1/members/$ref", http.StatusBadRequest, map[string]any{"error": map[string]string{
		"message": "One or more added object references already exist for the following modified properties: 'members'."}})
	srv.Status("POST /v1.0/directoryRoles/r1/members/$ref", http.StatusNoContent, nil)
	srv.Status("DELETE /v1.0/groups/g1/members/u1/$ref", http.StatusNotFound, nil)
	srv.Status("PATCH /v1.0/users/u2", http.StatusNoContent, nil)
	c := newClient(t, srv, "env:SECRET")
	var _ connector.Writer = c
	ctx := context.Background()
	for _, err := range []error{
		c.AddMember(ctx, "g1", "u1"), // already a member
		c.AddMember(ctx, "role:r1", "u2"),
		c.RemoveMember(ctx, "g1", "u1"), // already gone
		c.SetActive(ctx, "u2", false),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []string{
		"POST /contoso.onmicrosoft.com/oauth2/v2.0/token",
		`POST /v1.0/groups/g1/members/$ref {"@odata.id":"` + srv.URL + `/v1.0/directoryObjects/u1"}`,
		`POST /v1.0/directoryRoles/r1/members/$ref {"@odata.id":"` + srv.URL + `/v1.0/directoryObjects/u2"}`,
		"DELETE /v1.0/groups/g1/members/u1/$ref",
		`PATCH /v1.0/users/u2 {"accountEnabled":false}`,
	}
	got := srv.Calls()
	got[0] = strings.SplitN(got[0], " grant_type", 2)[0]
	if !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestConfig(t *testing.T) {
	for _, raw := range []string{
		`{"client_id": "a", "client_secret": "env:S"}`,
		`{"tenant_id": "t", "client_id": "a"}`,
		`{"tenant_id": "t/../x", "client_id": "a", "client_secret": "env:S"}`,
		`{"tenant_id": "t", "client_id": "a", "client_secret": "env:S", "graph_url": "http://graph.local"}`,
		`{"tenant_id": "t", "client_id": "a", "client_secret": "env:S", "tenant": "typo"}`,
	} {
		if _, err := New(json.RawMessage(raw), connectortest.Secrets); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}
