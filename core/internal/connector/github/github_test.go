package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
)

// fakeGitHub is the acme organization with two teams, an owner, a member
// and an outside collaborator, behind SAML when saml is set. prefix is where
// the REST API lives: "" as on api.github.com, "/api/v3" as on GitHub
// Enterprise Server.
func fakeGitHub(t *testing.T, prefix string, saml bool) *connectortest.Server {
	t.Helper()
	srv := connectortest.New(t, func(r *http.Request) bool { return r.Header.Get("Authorization") == "Bearer TOKEN" })
	org := prefix + "/orgs/acme"
	srv.Handle("GET "+org+"/teams", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "" {
			w.Header().Set("Link", `<`+srv.URL+org+`/teams?per_page=100&page=2>; rel="next", <`+srv.URL+org+`/teams?per_page=100&page=2>; rel="last"`)
			connectortest.Reply(w, http.StatusOK, []map[string]any{{"slug": "platform", "name": "Platform", "description": "Infra"}})
			return
		}
		connectortest.Reply(w, http.StatusOK, []map[string]any{{"slug": "sre", "name": "SRE"}})
	})
	srv.JSON("GET "+org+"/teams/platform/members", []map[string]any{{"id": 1, "login": "ana"}, {"id": 2, "login": "luis"}})
	srv.JSON("GET "+org+"/teams/sre/members", []map[string]any{{"id": 1, "login": "ana"}})
	srv.Handle("GET "+org+"/members", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("role") == "admin" {
			connectortest.Reply(w, http.StatusOK, []map[string]any{{"id": 1, "login": "ana"}})
			return
		}
		connectortest.Reply(w, http.StatusOK, []map[string]any{{"id": 1, "login": "ana"}, {"id": 2, "login": "luis"}})
	})
	srv.JSON("GET "+org+"/outside_collaborators", []map[string]any{{"id": 3, "login": "contractor"}})
	graphql := "/graphql"
	if prefix != "" {
		graphql = "/api/graphql"
	}
	srv.Handle("POST "+graphql, func(w http.ResponseWriter, r *http.Request) {
		var q struct {
			Variables struct {
				Org   string  `json:"org"`
				After *string `json:"after"`
			} `json:"variables"`
		}
		json.NewDecoder(r.Body).Decode(&q) //nolint:errcheck
		if q.Variables.Org != "acme" {
			t.Errorf("SAML identities of %q", q.Variables.Org)
		}
		if !saml {
			connectortest.Reply(w, http.StatusOK, map[string]any{"data": map[string]any{"organization": map[string]any{"samlIdentityProvider": nil}}})
			return
		}
		node := func(email, login string) map[string]any {
			return map[string]any{"samlIdentity": map[string]any{"nameId": email}, "user": map[string]any{"login": login}}
		}
		page := map[string]any{"pageInfo": map[string]any{"hasNextPage": true, "endCursor": "c1"}, "nodes": []any{node("ana@acme.com", "ana")}}
		if q.Variables.After != nil {
			page = map[string]any{"pageInfo": map[string]any{"hasNextPage": false}, "nodes": []any{node("luis@acme.com", "luis"), node("x", "nobody")}}
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"data": map[string]any{"organization": map[string]any{"samlIdentityProvider": map[string]any{"externalIdentities": page}}}})
	})
	return srv
}

func newClient(t *testing.T, srv *connectortest.Server, prefix string) *Client {
	t.Helper()
	r, err := New(json.RawMessage(`{"organization": "acme", "token": "env:TOKEN", "api_url": "`+srv.URL+prefix+`", "allow_http": true}`), connectortest.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*Client)
}

func TestRead(t *testing.T) {
	for _, prefix := range []string{"", "/api/v3"} {
		for _, saml := range []bool{true, false} {
			srv := fakeGitHub(t, prefix, saml)
			snap, err := newClient(t, srv, prefix).Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range snap.Entitlements {
				names = append(names, e.NativeID+"="+e.Name)
			}
			if want := []string{"role:owner=Organization owner", "team:platform=Platform", "team:sre=SRE"}; !slices.Equal(names, want) {
				t.Errorf("entitlements %v, want %v", names, want)
			}
			if len(snap.Accounts) != 3 {
				t.Fatalf("got %d accounts", len(snap.Accounts))
			}
			ana, luis, contractor := snap.Accounts[0], snap.Accounts[1], snap.Accounts[2]
			if ana.NativeID != "1" || ana.Username != "ana" || !slices.Equal(ana.Entitlements, []string{"team:platform", "team:sre", Owner}) {
				t.Errorf("ana = %+v", ana)
			}
			wantEmail := ""
			if saml {
				wantEmail = "luis@acme.com"
			}
			if luis.Email != wantEmail || luis.Attributes["membership"] != "member" {
				t.Errorf("saml %v: luis = %+v", saml, luis)
			}
			if contractor.Attributes["membership"] != "outside collaborator" || len(contractor.Entitlements) != 0 {
				t.Errorf("contractor = %+v", contractor)
			}
		}
	}
}

func TestWrites(t *testing.T) {
	srv := fakeGitHub(t, "", false)
	for id, login := range map[int]string{1: "ana", 2: "luis", 3: "contractor"} {
		srv.JSON(fmt.Sprintf("GET /user/%d", id), map[string]any{"id": id, "login": login})
	}
	srv.JSON("GET /orgs/acme/memberships/ana", map[string]string{"role": "admin", "state": "active"})
	srv.JSON("GET /orgs/acme/memberships/luis", map[string]string{"role": "member", "state": "active"})
	srv.Status("GET /orgs/acme/memberships/contractor", http.StatusNotFound, map[string]string{"message": "Not Found"})
	srv.JSON("PUT /orgs/acme/memberships/{login}", map[string]string{"role": "admin"})
	srv.JSON("PUT /orgs/acme/teams/sre/memberships/luis", map[string]string{"state": "active"})
	srv.Status("DELETE /orgs/acme/teams/sre/memberships/contractor", http.StatusNotFound, nil)
	srv.Status("DELETE /orgs/acme/memberships/luis", http.StatusNoContent, nil)
	srv.Status("DELETE /orgs/acme/outside_collaborators/contractor", http.StatusNoContent, nil)
	c := newClient(t, srv, "")
	var _ connector.Writer = c
	ctx := context.Background()
	for _, err := range []error{
		c.AddMember(ctx, "team:sre", "2"),
		c.RemoveMember(ctx, "team:sre", "3"), // not in the team
		c.AddMember(ctx, Owner, "1"),         // an owner already
		c.AddMember(ctx, Owner, "2"),
		c.RemoveMember(ctx, Owner, "2"), // not an owner (in this fake)
		c.SetActive(ctx, "2", false),
		c.SetActive(ctx, "3", false),
		c.SetActive(ctx, "3", true), // Grantra does not invite
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AddMember(ctx, Owner, "3"); err == nil || !strings.Contains(err.Error(), "invite them first") {
		t.Errorf("made an outside collaborator an owner: %v", err)
	}
	want := []string{
		`PUT /orgs/acme/teams/sre/memberships/luis {"role":"member"}`,
		"DELETE /orgs/acme/teams/sre/memberships/contractor",
		`PUT /orgs/acme/memberships/luis {"role":"admin"}`,
		"DELETE /orgs/acme/memberships/luis",
		"DELETE /orgs/acme/outside_collaborators/contractor",
	}
	if got := srv.Calls(); !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
