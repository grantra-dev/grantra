package googleworkspace

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
)

// serviceAccountKey is a JSON key like the ones Google Cloud hands out.
func serviceAccountKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	b, _ := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "grantra@acme-iam.iam.gserviceaccount.com",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
	})
	return string(b)
}

// fakeGoogle hands out a read token for the read-only scopes and a write
// token for the others, acting as admin@acme.com.
func fakeGoogle(t *testing.T) *connectortest.Server {
	t.Helper()
	srv := connectortest.New(t, func(r *http.Request) bool {
		if r.URL.Path == "/token" {
			return true
		}
		want := "Bearer read-token"
		if r.Method != http.MethodGet {
			want = "Bearer write-token"
		}
		return r.Header.Get("Authorization") == want
	})
	srv.Handle("POST /token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //nolint:errcheck
		parts := strings.Split(r.Form.Get("assertion"), ".")
		var claims struct {
			Sub   string `json:"sub"`
			Scope string `json:"scope"`
		}
		if len(parts) == 3 {
			payload, _ := base64.RawURLEncoding.DecodeString(parts[1])
			json.Unmarshal(payload, &claims) //nolint:errcheck
		}
		token := map[string]string{
			strings.Join(ReadScopes, " "):  "read-token",
			strings.Join(WriteScopes, " "): "write-token",
		}[claims.Scope]
		if claims.Sub != "admin@acme.com" || token == "" {
			connectortest.Reply(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized_client",
				"error_description": "Client is unauthorized to retrieve access tokens using this method"})
			return
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"access_token": token, "token_type": "Bearer", "expires_in": 3600})
	})
	const dir = "/admin/directory/v1"
	srv.Handle("GET "+dir+"/groups", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("customer") != "my_customer" {
			t.Errorf("groups of customer %q", r.URL.Query().Get("customer"))
		}
		if r.URL.Query().Get("pageToken") == "" {
			connectortest.Reply(w, http.StatusOK, map[string]any{
				"groups":        []map[string]any{{"id": "g1", "email": "finance@acme.com", "name": "Finance"}},
				"nextPageToken": "p2",
			})
			return
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"groups": []map[string]any{{"id": "g2", "email": "eng@acme.com"}}})
	})
	srv.JSON("GET "+dir+"/groups/g1/members", map[string]any{"members": []map[string]any{
		{"id": "u1", "type": "USER", "role": "OWNER"}, {"id": "g2", "type": "GROUP"},
	}})
	srv.JSON("GET "+dir+"/groups/g2/members", map[string]any{})
	srv.JSON("GET "+dir+"/customer/my_customer/roles", map[string]any{"items": []map[string]any{
		{"roleId": "91", "roleName": "_SEED_ADMIN_ROLE", "roleDescription": "Google Apps Administrator Seed Role", "isSuperAdminRole": true},
		{"roleId": "92", "roleName": "_HELP_DESK_ADMIN_ROLE"},
	}})
	srv.JSON("GET "+dir+"/customer/my_customer/roleassignments", map[string]any{"items": []map[string]any{
		{"roleAssignmentId": "a1", "roleId": "91", "assignedTo": "u1", "scopeType": "CUSTOMER"},
	}})
	srv.JSON("GET "+dir+"/users", map[string]any{"users": []map[string]any{
		{"id": "u1", "primaryEmail": "ana@acme.com", "name": map[string]string{"fullName": "Ana García"},
			"lastLoginTime": "2026-10-02T09:00:00.000Z", "orgUnitPath": "/Finance",
			"externalIds":   []map[string]string{{"type": "custom", "value": "x"}, {"type": "organization", "value": "E1"}},
			"organizations": []map[string]any{{"title": "Intern"}, {"title": "CFO", "department": "Finance", "primary": true}}},
		{"id": "u2", "primaryEmail": "luis@acme.com", "suspended": true, "lastLoginTime": "1970-01-01T00:00:00.000Z"},
	}})
	return srv
}

func newClient(t *testing.T, srv *connectortest.Server) *Client {
	t.Helper()
	key := serviceAccountKey(t)
	cfg, _ := json.Marshal(Config{AdminEmail: "admin@acme.com", ServiceAccountKey: "env:KEY",
		APIURL: srv.URL, TokenURL: srv.URL + "/token", AllowHTTP: true})
	r, err := New(cfg, func(string) (string, error) { return key, nil })
	if err != nil {
		t.Fatal(err)
	}
	return r.(*Client)
}

func TestRead(t *testing.T) {
	srv := fakeGoogle(t)
	snap, err := newClient(t, srv).Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range snap.Entitlements {
		names = append(names, e.NativeID+"="+e.Name)
	}
	if want := []string{"g1=Finance", "g2=eng@acme.com", "role:91=Super Admin (admin role)", "role:92=HELP_DESK_ADMIN_ROLE (admin role)"}; !slices.Equal(names, want) {
		t.Errorf("entitlements %v, want %v", names, want)
	}
	ana, luis := snap.Accounts[0], snap.Accounts[1]
	if ana.Email != "ana@acme.com" || ana.ExternalID != "E1" || !ana.Active || ana.Attributes["title"] != "CFO" ||
		ana.Attributes["org_unit"] != "/Finance" || !slices.Equal(ana.Entitlements, []string{"g1", "role:91"}) ||
		!ana.LastLogin.Equal(time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC)) {
		t.Errorf("ana = %+v", ana)
	}
	if luis.Active || !luis.LastLogin.IsZero() {
		t.Errorf("luis = %+v", luis)
	}
	if calls := srv.Calls(); len(calls) != 1 {
		t.Errorf("reading asked for more than the read-only token: %v", calls)
	}
}

func TestWrites(t *testing.T) {
	srv := fakeGoogle(t)
	srv.Status("POST /admin/directory/v1/groups/g1/members", http.StatusConflict, map[string]any{"error": map[string]string{"message": "Member already exists."}})
	srv.Status("DELETE /admin/directory/v1/groups/g1/members/u2", http.StatusNotFound, nil)
	srv.JSON("PATCH /admin/directory/v1/users/u2", map[string]any{"id": "u2"})
	c := newClient(t, srv)
	var _ connector.Writer = c
	ctx := context.Background()
	for _, err := range []error{c.AddMember(ctx, "g1", "u1"), c.RemoveMember(ctx, "g1", "u2"), c.SetActive(ctx, "u2", true)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AddMember(ctx, "role:91", "u2"); err == nil {
		t.Error("changed an admin role")
	}
	got := srv.Calls()[1:] // after the token
	want := []string{
		`POST /admin/directory/v1/groups/g1/members {"id":"u1","role":"MEMBER"}`,
		"DELETE /admin/directory/v1/groups/g1/members/u2",
		`PATCH /admin/directory/v1/users/u2 {"suspended":false}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestConfig(t *testing.T) {
	for _, raw := range []string{
		`{"service_account_key": "env:KEY"}`,
		`{"admin_email": "admin@acme.com"}`,
		`{"admin_email": "admin@acme.com", "service_account_key": "env:KEY"}`, // the key is not JSON
	} {
		if _, err := New(json.RawMessage(raw), connectortest.Secrets); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}
