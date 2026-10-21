package okta

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

// fakeOkta is an org with two groups, one application, a super admin and
// three users, one of them deactivated.
func fakeOkta(t *testing.T) *connectortest.Server {
	t.Helper()
	srv := connectortest.New(t, func(r *http.Request) bool { return r.Header.Get("Authorization") == "SSWS TOKEN" })
	srv.Handle("GET /api/v1/groups", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("after") == "" {
			w.Header().Set("Link", `<`+srv.URL+`/api/v1/groups?limit=200>; rel="self", <`+srv.URL+`/api/v1/groups?after=g1&limit=200>; rel="next"`)
			connectortest.Reply(w, http.StatusOK, []map[string]any{{"id": "g1", "type": "OKTA_GROUP", "profile": map[string]string{"name": "Finance", "description": "Money"}}})
			return
		}
		connectortest.Reply(w, http.StatusOK, []map[string]any{{"id": "g2", "type": "BUILT_IN", "profile": map[string]string{"name": "Everyone"}}})
	})
	srv.JSON("GET /api/v1/groups/g1/users", []map[string]any{{"id": "u1"}})
	srv.JSON("GET /api/v1/groups/g2/users", []map[string]any{{"id": "u1"}, {"id": "u2"}, {"id": "u3"}})
	srv.Handle("GET /api/v1/apps", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") != `status eq "ACTIVE"` {
			t.Errorf("apps filter %q", r.URL.Query().Get("filter"))
		}
		connectortest.Reply(w, http.StatusOK, []map[string]any{{"id": "a1", "label": "Salesforce", "status": "ACTIVE"}})
	})
	srv.JSON("GET /api/v1/apps/a1/users", []map[string]any{{"id": "u2"}})
	srv.JSON("GET /api/v1/iam/assignees/users", map[string]any{"value": []map[string]any{{"id": "u1"}}, "_links": map[string]any{}})
	srv.JSON("GET /api/v1/users/u1/roles", []map[string]any{{"id": "ra1", "type": "SUPER_ADMIN", "label": "Super Administrator", "assignmentType": "USER"}})
	srv.Handle("GET /api/v1/users", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("filter") == `status eq "DEPROVISIONED"` {
			connectortest.Reply(w, http.StatusOK, []map[string]any{{"id": "u3", "status": "DEPROVISIONED", "profile": map[string]string{"login": "old@acme.com"}}})
			return
		}
		connectortest.Reply(w, http.StatusOK, []map[string]any{
			{"id": "u1", "status": "ACTIVE", "lastLogin": "2026-10-03T10:00:00.000Z", "profile": map[string]string{
				"login": "ana@acme.com", "email": "ana@acme.com", "firstName": "Ana", "lastName": "García", "employeeNumber": "E1", "title": "CFO"}},
			{"id": "u2", "status": "SUSPENDED", "profile": map[string]string{"login": "luis@acme.com", "email": "luis@acme.com", "displayName": "Luis P."}},
		})
	})
	return srv
}

func newClient(t *testing.T, srv *connectortest.Server, extra string) *Client {
	t.Helper()
	r, err := New(json.RawMessage(`{"org_url": "`+srv.URL+`", "token": "env:TOKEN", "allow_http": true`+extra+`}`), connectortest.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*Client)
}

func TestRead(t *testing.T) {
	srv := fakeOkta(t)
	snap, err := newClient(t, srv, "").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range snap.Entitlements {
		names = append(names, e.NativeID+"="+e.Name)
	}
	if want := []string{"g1=Finance", "g2=Everyone", "app:a1=Salesforce (application)", "role:SUPER_ADMIN=Super Administrator (admin role)"}; !slices.Equal(names, want) {
		t.Errorf("entitlements %v, want %v", names, want)
	}
	if len(snap.Accounts) != 3 {
		t.Fatalf("got %d accounts", len(snap.Accounts))
	}
	ana, luis, old := snap.Accounts[0], snap.Accounts[1], snap.Accounts[2]
	if ana.DisplayName != "Ana García" || ana.ExternalID != "E1" || !ana.Active || !slices.Equal(ana.Entitlements, []string{"g1", "g2", "role:SUPER_ADMIN"}) ||
		!ana.LastLogin.Equal(time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("ana = %+v", ana)
	}
	if luis.Active || luis.DisplayName != "Luis P." || !slices.Equal(luis.Entitlements, []string{"g2", "app:a1"}) || luis.Attributes["status"] != "suspended" {
		t.Errorf("luis = %+v", luis)
	}
	if old.Active || old.Username != "old@acme.com" {
		t.Errorf("deactivated = %+v", old)
	}

	// Without application assignments.
	snap, err = newClient(t, srv, `, "apps": false`).Read(context.Background())
	if err != nil || len(snap.Entitlements) != 3 {
		t.Errorf("apps off: %d entitlements, %v", len(snap.Entitlements), err)
	}
}

func TestWrites(t *testing.T) {
	srv := fakeOkta(t)
	srv.Status("PUT /api/v1/groups/g1/users/u2", http.StatusNoContent, nil)
	srv.Status("DELETE /api/v1/groups/g1/users/u9", http.StatusNotFound, map[string]string{"errorSummary": "Not found"})
	srv.JSON("GET /api/v1/users/u1", map[string]any{"id": "u1", "status": "ACTIVE"})
	srv.JSON("GET /api/v1/users/u2", map[string]any{"id": "u2", "status": "SUSPENDED"})
	srv.JSON("GET /api/v1/users/u3", map[string]any{"id": "u3", "status": "DEPROVISIONED", "profile": map[string]string{"login": "old@acme.com"}})
	srv.Status("POST /api/v1/users/u1/lifecycle/suspend", http.StatusOK, nil)
	srv.Status("POST /api/v1/users/u2/lifecycle/unsuspend", http.StatusOK, nil)
	c := newClient(t, srv, "")
	var _ connector.Writer = c
	ctx := context.Background()
	for _, err := range []error{
		c.AddMember(ctx, "g1", "u2"),
		c.RemoveMember(ctx, "g1", "u9"),
		c.SetActive(ctx, "u1", false),
		c.SetActive(ctx, "u1", true), // active already: nothing to do
		c.SetActive(ctx, "u2", true),
		c.SetActive(ctx, "u3", false), // deactivated already
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range []error{c.SetActive(ctx, "u3", true), c.AddMember(ctx, "app:a1", "u1"), c.RemoveMember(ctx, "role:SUPER_ADMIN", "u1")} {
		if err == nil {
			t.Error("a change Grantra must refuse went through")
		}
	}
	want := []string{
		"PUT /api/v1/groups/g1/users/u2",
		"DELETE /api/v1/groups/g1/users/u9",
		"POST /api/v1/users/u1/lifecycle/suspend",
		"POST /api/v1/users/u2/lifecycle/unsuspend",
	}
	if got := srv.Calls(); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
