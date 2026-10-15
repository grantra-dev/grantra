package grantralogin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector"
)

// fakeKeycloak is just enough of the Keycloak Admin API for the connector:
// one realm, paged with first and max like the real one.
type fakeKeycloak struct {
	mu       sync.Mutex
	users    []map[string]any
	groups   map[string][]string // group id -> member user ids
	roles    map[string][]string // role name -> holders
	tokens   int
	emails   []string // users sent the setup email
	logouts  []string
	failMail bool
}

func (f *fakeKeycloak) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/realms/grantra/protocol/openid-connect/token" {
			if id, secret, _ := r.BasicAuth(); id != "grantra-connector" || secret != "s3cret" || r.FormValue("grant_type") != "client_credentials" {
				w.WriteHeader(http.StatusUnauthorized)
				json.NewEncoder(w).Encode(map[string]string{"error_description": "Invalid client credentials"}) //nolint:errcheck
				return
			}
			f.tokens++
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 300}) //nolint:errcheck
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path, ok := strings.CutPrefix(r.URL.Path, "/admin/realms/grantra")
		if !ok {
			http.NotFound(w, r)
			return
		}
		parts := strings.Split(strings.Trim(path, "/"), "/")
		page := func(all []map[string]any) {
			first, _ := strconv.Atoi(r.URL.Query().Get("first"))
			max, _ := strconv.Atoi(r.URL.Query().Get("max"))
			from := min(first, len(all))
			to := min(from+max, len(all))
			json.NewEncoder(w).Encode(all[from:to]) //nolint:errcheck
		}
		members := func(ids []string) {
			var out []map[string]any
			for _, id := range ids {
				out = append(out, map[string]any{"id": id})
			}
			page(out)
		}
		switch {
		case r.Method == http.MethodGet && path == "/users" && r.URL.Query().Get("exact") == "true":
			var out []map[string]any
			for _, u := range f.users {
				if u["username"] == r.URL.Query().Get("username") {
					out = append(out, u)
				}
			}
			json.NewEncoder(w).Encode(out) //nolint:errcheck
		case r.Method == http.MethodGet && path == "/users":
			page(f.users)
		case r.Method == http.MethodPost && path == "/users":
			var u map[string]any
			json.NewDecoder(r.Body).Decode(&u) //nolint:errcheck
			for _, x := range f.users {
				if x["username"] == u["username"] {
					w.WriteHeader(http.StatusConflict)
					json.NewEncoder(w).Encode(map[string]string{"errorMessage": "User exists with same username"}) //nolint:errcheck
					return
				}
			}
			u["id"] = fmt.Sprintf("u%d", len(f.users)+1)
			f.users = append(f.users, u)
			w.Header().Set("Location", "http://kc/admin/realms/grantra/users/"+u["id"].(string))
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodPut && len(parts) == 3 && parts[2] == "execute-actions-email":
			if f.failMail {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"errorMessage": "Failed to send execute actions email"}) //nolint:errcheck
				return
			}
			f.emails = append(f.emails, parts[1])
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && len(parts) == 2 && parts[0] == "users":
			var patch map[string]any
			json.NewDecoder(r.Body).Decode(&patch) //nolint:errcheck
			for _, u := range f.users {
				if u["id"] == parts[1] {
					u["enabled"] = patch["enabled"]
					w.WriteHeader(http.StatusNoContent)
					return
				}
			}
			http.NotFound(w, r)
		case r.Method == http.MethodPost && len(parts) == 3 && parts[2] == "logout":
			f.logouts = append(f.logouts, parts[1])
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 4 && parts[0] == "users" && parts[2] == "groups":
			ids := f.groups[parts[3]]
			if r.Method == http.MethodPut && !slices.Contains(ids, parts[1]) {
				f.groups[parts[3]] = append(ids, parts[1])
			}
			if r.Method == http.MethodDelete {
				f.groups[parts[3]] = slices.DeleteFunc(ids, func(s string) bool { return s == parts[1] })
			}
			w.WriteHeader(http.StatusNoContent)
		case len(parts) == 4 && parts[2] == "role-mappings" && parts[3] == "realm":
			var rs []map[string]string
			json.NewDecoder(r.Body).Decode(&rs) //nolint:errcheck
			for _, role := range rs {
				if role["id"] != "id-"+role["name"] {
					t.Errorf("role mapping with id %q for %q", role["id"], role["name"])
				}
				ids := f.roles[role["name"]]
				if r.Method == http.MethodPost && !slices.Contains(ids, parts[1]) {
					f.roles[role["name"]] = append(ids, parts[1])
				}
				if r.Method == http.MethodDelete {
					f.roles[role["name"]] = slices.DeleteFunc(ids, func(s string) bool { return s == parts[1] })
				}
			}
			w.WriteHeader(http.StatusNoContent)
		case path == "/groups":
			page([]map[string]any{
				{"id": "g1", "name": "staff", "path": "/staff", "subGroupCount": 1},
				{"id": "g2", "name": "vpn", "path": "/vpn", "attributes": map[string]any{"description": []string{"VPN access"}}},
			})
		case path == "/groups/g1/children":
			page([]map[string]any{{"id": "g3", "name": "finance", "path": "/staff/finance"}})
		case len(parts) == 3 && parts[0] == "groups" && parts[2] == "members":
			members(f.groups[parts[1]])
		case path == "/roles":
			var out []map[string]any
			for _, n := range []string{"default-roles-grantra", "offline_access", "uma_authorization", "wiki-editor"} {
				out = append(out, map[string]any{"id": "id-" + n, "name": n})
			}
			page(out)
		case len(parts) == 2 && parts[0] == "roles":
			json.NewEncoder(w).Encode(map[string]string{"id": "id-" + parts[1], "name": parts[1]}) //nolint:errcheck
		case len(parts) == 3 && parts[0] == "roles" && parts[2] == "users":
			members(f.roles[parts[1]])
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
}

func newFake() *fakeKeycloak {
	user := func(id, username, email, first, last string, enabled bool, attrs map[string][]string) map[string]any {
		return map[string]any{"id": id, "username": username, "email": email, "firstName": first, "lastName": last, "enabled": enabled, "attributes": attrs}
	}
	return &fakeKeycloak{
		users: []map[string]any{
			user("u1", "ana", "ana@example.com", "Ana", "García", true, map[string][]string{"employee_id": {"E1"}, "department": {"Sales"}}),
			user("u2", "luis", "luis@example.com", "Luis", "Pérez", true, nil),
			user("u3", "old", "old@example.com", "Old", "Timer", false, nil),
		},
		groups: map[string][]string{"g1": {"u1", "u2", "u3"}, "g2": {"u2"}, "g3": {"u1"}},
		roles:  map[string][]string{"wiki-editor": {"u1"}, "default-roles-grantra": {"u1", "u2", "u3"}},
	}
}

func secrets(ref string) (string, error) {
	if ref == "env:GRANTRA_SECRET_LOGIN" {
		return "s3cret", nil
	}
	return "wrong", nil
}

func newClient(t *testing.T, f *fakeKeycloak, extra string) *Client {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	r, err := New(json.RawMessage(`{"url": "`+srv.URL+`/", "client_secret": "env:GRANTRA_SECRET_LOGIN", "allow_http": true, "page_size": 2`+extra+`}`), secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*Client)
}

func TestRead(t *testing.T) {
	f := newFake()
	snap, err := newClient(t, f, "").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var ents []string
	for _, e := range snap.Entitlements {
		ents = append(ents, e.NativeID+"="+e.Name)
	}
	want := []string{"group:g1=staff", "group:g2=vpn", "group:g3=staff/finance", "role:wiki-editor=wiki-editor"}
	if !slices.Equal(ents, want) {
		t.Fatalf("entitlements = %v, want %v", ents, want)
	}
	if d := snap.Entitlements[1].Description; d != "VPN access" {
		t.Errorf("vpn description = %q", d)
	}
	if len(snap.Accounts) != 3 {
		t.Fatalf("accounts = %+v", snap.Accounts)
	}
	ana := snap.Accounts[0]
	if ana.Username != "ana" || ana.Email != "ana@example.com" || ana.DisplayName != "Ana García" || ana.ExternalID != "E1" ||
		!ana.Active || ana.Attributes["department"] != "Sales" {
		t.Errorf("ana = %+v", ana)
	}
	if got := ana.Entitlements; !slices.Equal(got, []string{"group:g1", "group:g3", "role:wiki-editor"}) {
		t.Errorf("ana holds %v", got)
	}
	if snap.Accounts[2].Active {
		t.Error("a disabled user reads as active")
	}
	if f.tokens != 1 {
		t.Errorf("signed in %d times, want the token reused", f.tokens)
	}
}

func TestReadWithoutRoles(t *testing.T) {
	snap, err := newClient(t, newFake(), `, "roles": false`).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Entitlements {
		if strings.HasPrefix(e.NativeID, rolePrefix) {
			t.Errorf("read role %s with roles off", e.NativeID)
		}
	}
}

func TestBadCredentials(t *testing.T) {
	f := newFake()
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	r, err := New(json.RawMessage(`{"url": "`+srv.URL+`", "client_secret": "env:OTHER", "allow_http": true}`), secrets)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Read(context.Background())
	if err == nil || !strings.Contains(err.Error(), "Invalid client credentials") {
		t.Fatalf("err = %v", err)
	}
}

func TestCreateAccount(t *testing.T) {
	f := newFake()
	c := newClient(t, f, "")
	ctx := context.Background()
	in := connector.NewAccount{Username: "marta@example.com", Email: "marta@example.com", DisplayName: "Marta Ruiz Gil", ExternalID: "E9"}
	id, err := c.CreateAccount(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	u := f.users[len(f.users)-1]
	if id != "u4" || u["firstName"] != "Marta" || u["lastName"] != "Ruiz Gil" || u["emailVerified"] != true {
		t.Errorf("created %s: %v", id, u)
	}
	if attrs := u["attributes"].(map[string]any); attrs["employee_id"].([]any)[0] != "E9" {
		t.Errorf("attributes = %v", attrs)
	}
	if _, ok := u["password"]; ok {
		t.Error("Grantra set a password")
	}
	if !slices.Equal(f.emails, []string{"u4"}) {
		t.Errorf("setup emails sent to %v", f.emails)
	}

	// A retry after a failed email finds the user instead of failing.
	f.failMail = true
	if _, err := c.CreateAccount(ctx, in); err == nil || !strings.Contains(err.Error(), "email failed") {
		t.Fatalf("err = %v", err)
	}
	f.failMail = false
	again, err := c.CreateAccount(ctx, in)
	if err != nil || again != "u4" || len(f.users) != 4 {
		t.Fatalf("retry = %q, %v (%d users)", again, err, len(f.users))
	}
}

func TestCreateAccountWithoutSetupEmail(t *testing.T) {
	f := newFake()
	if _, err := newClient(t, f, `, "setup_email": false`).CreateAccount(context.Background(),
		connector.NewAccount{Username: "x", Email: "x@example.com"}); err != nil {
		t.Fatal(err)
	}
	if len(f.emails) != 0 || f.users[3]["requiredActions"] != nil {
		t.Errorf("emails %v, user %v", f.emails, f.users[3])
	}
}

func TestWrites(t *testing.T) {
	f := newFake()
	c := newClient(t, f, "")
	ctx := context.Background()
	for _, step := range []func() error{
		func() error { return c.SetActive(ctx, "u2", false) },
		func() error { return c.AddMember(ctx, "group:g2", "u1") },
		func() error { return c.AddMember(ctx, "group:g2", "u1") },
		func() error { return c.RemoveMember(ctx, "group:g1", "u3") },
		func() error { return c.AddMember(ctx, "role:wiki-editor", "u2") },
		func() error { return c.RemoveMember(ctx, "role:wiki-editor", "u1") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if f.users[1]["enabled"] != false || !slices.Equal(f.logouts, []string{"u2"}) {
		t.Errorf("disable: %v, logouts %v", f.users[1], f.logouts)
	}
	if !slices.Equal(f.groups["g2"], []string{"u2", "u1"}) || !slices.Equal(f.groups["g1"], []string{"u1", "u2"}) {
		t.Errorf("groups = %v", f.groups)
	}
	if !slices.Equal(f.roles["wiki-editor"], []string{"u2"}) {
		t.Errorf("roles = %v", f.roles)
	}
	if err := c.AddMember(ctx, "nope", "u1"); err == nil {
		t.Error("an unknown entitlement id was accepted")
	}
	if err := c.SetActive(ctx, "missing", true); !hasStatus(err, http.StatusNotFound) {
		t.Errorf("missing user: %v", err)
	}
}

func TestConfig(t *testing.T) {
	for _, cfg := range []string{
		`{"client_secret": "env:GRANTRA_SECRET_LOGIN"}`,
		`{"url": "http://kc:8080", "client_secret": "env:GRANTRA_SECRET_LOGIN"}`,
		`{"url": "https://kc"}`,
		`{"url": "https://kc", "client_secret": "env:GRANTRA_SECRET_LOGIN", "page_size": 5000}`,
		`{"url": "https://kc", "client_secret": "env:GRANTRA_SECRET_LOGIN", "typo": 1}`,
		`{"url": "ftp://kc", "client_secret": "env:GRANTRA_SECRET_LOGIN"}`,
	} {
		if _, err := New(json.RawMessage(cfg), secrets); err == nil {
			t.Errorf("%s was accepted", cfg)
		}
	}
	r, err := New(json.RawMessage(`{"url": "https://login.example.com/", "client_secret": "env:GRANTRA_SECRET_LOGIN"}`), secrets)
	if err != nil {
		t.Fatal(err)
	}
	c := r.(*Client)
	if c.realm != "grantra" || c.clientID != "grantra-connector" || !c.setupEmail || !c.roles || c.base.String() != "https://login.example.com" {
		t.Errorf("defaults = %+v", c)
	}
	if _, ok := r.(connector.AccountCreator); !ok {
		t.Error("the connector cannot provision")
	}
}
