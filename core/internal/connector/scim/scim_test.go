package scim

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector"
)

// fakeSCIM serves users and groups in pages of at most two, like a real
// service that ignores the requested count.
func fakeSCIM(t *testing.T, users, groups []map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"detail": "bad token"}) //nolint:errcheck
			return
		}
		var all []map[string]any
		switch {
		case strings.HasSuffix(r.URL.Path, "/scim/v2/Users"):
			all = users
		case strings.HasSuffix(r.URL.Path, "/scim/v2/Groups"):
			all = groups
		default:
			http.NotFound(w, r)
			return
		}
		start, _ := strconv.Atoi(r.URL.Query().Get("startIndex"))
		from := min(start-1, len(all))
		to := min(from+2, len(all))
		w.Header().Set("Content-Type", "application/scim+json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"schemas":      []string{"urn:ietf:params:scim:api:messages:2.0:ListResponse"},
			"totalResults": len(all), "startIndex": start, "itemsPerPage": to - from,
			"Resources": all[from:to],
		})
	}))
}

func secrets(ref string) (string, error) {
	if ref == "env:TOKEN" {
		return "s3cret", nil
	}
	return "wrong", nil
}

func newClient(t *testing.T, url string, extra string) connector.Reader {
	t.Helper()
	r, err := New(json.RawMessage(`{"base_url": "`+url+`/scim/v2/", "token": "env:TOKEN", "allow_http": true`+extra+`}`), secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRead(t *testing.T) {
	f := false
	users := []map[string]any{
		{"id": "u1", "userName": "ana@example.com", "externalId": "E1", "displayName": "Ana", "active": true,
			"emails": []map[string]any{{"value": "old@example.com"}, {"value": "ana@example.com", "primary": true}}, "title": "CTO"},
		{"id": "u2", "userName": "luis", "name": map[string]any{"givenName": "Luis", "familyName": "Pérez"}, "active": f,
			"urn:ietf:params:scim:schemas:extension:enterprise:2.0:User": map[string]any{"department": "Sales", "employeeNumber": "E2"},
			"groups": []map[string]any{{"value": "g2"}}},
		{"id": "u3", "userName": "bot"},
	}
	groups := []map[string]any{
		{"id": "g1", "displayName": "Admins", "members": []map[string]any{{"value": "u1"}}},
		{"id": "g2", "displayName": "Sales", "members": []map[string]any{{"value": "u1"}, {"value": "u2"}}},
		{"id": "g3"},
	}
	srv := fakeSCIM(t, users, groups)
	defer srv.Close()

	snap, err := newClient(t, srv.URL, "").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Entitlements) != 3 || snap.Entitlements[2].Name != "g3" {
		t.Errorf("entitlements = %+v", snap.Entitlements)
	}
	if len(snap.Accounts) != 3 {
		t.Fatalf("accounts = %+v", snap.Accounts)
	}
	ana, luis, bot := snap.Accounts[0], snap.Accounts[1], snap.Accounts[2]
	if ana.Email != "ana@example.com" || ana.ExternalID != "E1" || !ana.Active || ana.Attributes["title"] != "CTO" {
		t.Errorf("ana = %+v", ana)
	}
	slices.Sort(ana.Entitlements)
	if !slices.Equal(ana.Entitlements, []string{"g1", "g2"}) {
		t.Errorf("ana's groups = %v", ana.Entitlements)
	}
	if luis.DisplayName != "Luis Pérez" || luis.Active || luis.ExternalID != "E2" || luis.Attributes["department"] != "Sales" {
		t.Errorf("luis = %+v", luis)
	}
	if !slices.Equal(luis.Entitlements, []string{"g2"}) {
		t.Errorf("luis's groups = %v (listed both on the group and the user, once)", luis.Entitlements)
	}
	if !bot.Active || len(bot.Entitlements) != 0 {
		t.Errorf("bot = %+v", bot)
	}
}

func TestReadFailures(t *testing.T) {
	srv := fakeSCIM(t, nil, nil)
	defer srv.Close()
	bad, err := New(json.RawMessage(`{"base_url": "`+srv.URL+`/scim/v2", "token": "env:OTHER", "allow_http": true}`), secrets)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.Read(context.Background()); err == nil || !strings.Contains(err.Error(), "bad token") {
		t.Errorf("wrong token = %v", err)
	}
	missing, _ := New(json.RawMessage(`{"base_url": "`+srv.URL+`/nope", "token": "env:TOKEN", "allow_http": true}`), secrets)
	if _, err := missing.Read(context.Background()); err == nil {
		t.Error("404 read without error")
	}
}

func TestConfig(t *testing.T) {
	for _, cfg := range []string{
		`{"base_url": "http://crm.example.com/scim/v2", "token": "env:TOKEN"}`,
		`{"base_url": "crm.example.com", "token": "env:TOKEN"}`,
		`{"base_url": "https://crm.example.com/scim/v2"}`,
		`{"base_url": "https://crm.example.com/scim/v2", "token": "env:TOKEN", "page_size": 5000}`,
		`{"base_url": "https://crm.example.com/scim/v2", "token": "env:TOKEN", "pagesize": 10}`,
	} {
		if _, err := New(json.RawMessage(cfg), secrets); err == nil {
			t.Errorf("%s: no error", cfg)
		}
	}
	if _, err := New(json.RawMessage(`{"base_url": "https://crm.example.com/scim/v2", "token": "env:TOKEN"}`), secrets); err != nil {
		t.Error(err)
	}
	if _, err := connector.New(Kind, json.RawMessage(`{}`), secrets); err == nil {
		t.Error("registered scim connector accepted an empty config")
	}
	if _, err := connector.New("ldap", nil, secrets); err == nil {
		t.Error("unknown kind accepted")
	}
}
