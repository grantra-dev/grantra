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

func TestWrite(t *testing.T) {
	type call struct {
		Method string         `json:"method"`
		Path   string         `json:"path"`
		Body   map[string]any `json:"body"`
	}
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer s3cret" || r.Header.Get("Content-Type") != "application/scim+json" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		calls = append(calls, call{r.Method, r.URL.EscapedPath(), body})
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/scim/v2/Users":
			if body["userName"] == "taken@example.com" {
				w.WriteHeader(http.StatusConflict)
				json.NewEncoder(w).Encode(map[string]any{"detail": "userName is already taken"}) //nolint:errcheck
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]any{"id": "u-123"}) //nolint:errcheck
		case r.Method == http.MethodPatch && r.URL.Path == "/scim/v2/Groups/missing":
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer srv.Close()
	w, ok := newClient(t, srv.URL, "").(connector.Writer)
	if !ok {
		t.Fatal("the scim connector does not write")
	}
	ctx := context.Background()

	id, err := w.CreateAccount(ctx, connector.NewAccount{Username: "ana@example.com", Email: "ana@example.com", DisplayName: "Ana", ExternalID: "E1"})
	if err != nil || id != "u-123" {
		t.Fatalf("create = %q, %v", id, err)
	}
	if _, err := w.CreateAccount(ctx, connector.NewAccount{Username: "taken@example.com"}); err == nil || !strings.Contains(err.Error(), "already taken") {
		t.Errorf("create taken = %v", err)
	}
	if err := w.SetActive(ctx, "u-123", false); err != nil {
		t.Error(err)
	}
	if err := w.AddMember(ctx, "g/1", "u-123"); err != nil {
		t.Error(err)
	}
	if err := w.RemoveMember(ctx, "g/1", "u-123"); err != nil {
		t.Error(err)
	}
	if err := w.AddMember(ctx, "missing", "u-123"); err == nil {
		t.Error("add to a missing group succeeded")
	}

	got, _ := json.Marshal(calls)
	for _, want := range []string{
		`"method":"POST","path":"/scim/v2/Users"`,
		`"userName":"ana@example.com"`,
		`"externalId":"E1"`,
		`"emails":[{"primary":true,"type":"work","value":"ana@example.com"}]`,
		`"method":"PATCH","path":"/scim/v2/Users/u-123"`,
		`{"op":"replace","path":"active","value":false}`,
		`"path":"/scim/v2/Groups/g%2F1"`,
		`{"op":"add","path":"members","value":[{"value":"u-123"}]}`,
		`{"op":"remove","path":"members[value eq \"u-123\"]"}`,
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("requests miss %s:\n%s", want, got)
		}
	}
}
