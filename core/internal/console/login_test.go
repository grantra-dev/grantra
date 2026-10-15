package console

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/catalog"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// fakeLogin is the part of the Keycloak Admin API the Grantra Login page
// uses, holding the realm's browser flow and the MFA role's members.
type fakeLogin struct {
	mu        sync.Mutex
	flow      string
	passkeys  bool
	copied    bool
	mfaRole   []string
	clients   []map[string]any
	forbidden bool
}

func (f *fakeLogin) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/openid-connect/token") {
			json.NewEncoder(w).Encode(map[string]any{"access_token": "tok", "expires_in": 300}) //nolint:errcheck
			return
		}
		if f.forbidden {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/admin/realms/grantra")
		var body map[string]any
		if r.Body != nil {
			json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		}
		out := func(v any) { json.NewEncoder(w).Encode(v) } //nolint:errcheck
		switch {
		case path == "" && r.Method == http.MethodGet:
			out(map[string]any{"realm": "grantra", "displayName": "Grantra Login", "loginTheme": "grantra",
				"browserFlow": f.flow, "webAuthnPolicyPasswordlessPasskeysEnabled": f.passkeys})
		case path == "" && r.Method == http.MethodPut:
			if v, ok := body["browserFlow"].(string); ok {
				f.flow = v
			}
			if v, ok := body["webAuthnPolicyPasswordlessPasskeysEnabled"].(bool); ok {
				f.passkeys = v
			}
			w.WriteHeader(http.StatusNoContent)
		case path == "/clients" && r.Method == http.MethodGet:
			if r.URL.Query().Get("first") != "0" {
				out([]any{})
				return
			}
			out(f.clients)
		case path == "/clients" && r.Method == http.MethodPost:
			body["id"] = "c" + body["clientId"].(string)
			f.clients = append(f.clients, body)
			w.Header().Set("Location", "http://kc/admin/realms/grantra/clients/"+body["id"].(string))
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(path, "/client-secret"):
			out(map[string]string{"value": "secret-" + r.Method})
		case strings.HasPrefix(path, "/clients/"):
			for _, c := range f.clients {
				if "/clients/"+c["id"].(string) == path {
					out(c)
					return
				}
			}
			http.NotFound(w, r)
		case path == "/authentication/flows" && r.Method == http.MethodGet:
			out([]any{})
		case path == "/authentication/flows/browser/copy":
			f.copied = true
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(path, "/executions") && r.Method == http.MethodGet:
			exs := []map[string]any{
				{"id": "e1", "displayName": "Cookie", "providerId": "auth-cookie", "level": 0, "requirement": "ALTERNATIVE"},
				{"id": "e2", "displayName": "grantra browser forms", "authenticationFlow": true, "level": 0, "requirement": "ALTERNATIVE"},
				{"id": "e3", "displayName": "Username Password Form", "providerId": "auth-username-password-form", "level": 1, "requirement": "REQUIRED"},
				{"id": "e4", "displayName": "grantra browser Browser - Conditional 2FA", "authenticationFlow": true, "level": 1, "requirement": "CONDITIONAL", "priority": 20},
				{"id": "e5", "displayName": "Condition - user configured", "providerId": "conditional-user-configured", "level": 2, "requirement": "REQUIRED"},
				{"id": "e6", "displayName": "OTP Form", "providerId": "auth-otp-form", "level": 2, "requirement": "ALTERNATIVE"},
			}
			if strings.Contains(path, "optional%20second%20factor") || strings.Contains(path, "optional second factor") {
				exs = []map[string]any{
					{"id": "o1", "providerId": "conditional-user-configured", "level": 0, "priority": 10},
					{"id": "o2", "providerId": "conditional-user-role", "level": 0, "priority": 20},
					{"id": "o3", "providerId": "auth-otp-form", "level": 0, "priority": 30},
					{"id": "o4", "providerId": "webauthn-authenticator", "level": 0, "priority": 40},
				}
			}
			if strings.Contains(path, "2FA") {
				exs = []map[string]any{{"id": "r1", "providerId": "conditional-user-role", "level": 0, "priority": 5}}
			}
			if strings.HasSuffix(path, "browser%20forms/executions") || strings.HasSuffix(path, "browser forms/executions") {
				exs = []map[string]any{{"id": "s1", "displayName": "grantra browser optional second factor", "authenticationFlow": true, "level": 1}}
			}
			if f.flow == "grantra browser" && f.mfaRole != nil && path == "/authentication/flows/grantra browser/executions" {
				exs = append(exs, map[string]any{"id": "e7", "providerId": "conditional-user-role", "level": 2})
			}
			out(exs)
		case path == "/roles/grantra-mfa-required/users":
			var us []map[string]string
			if r.URL.Query().Get("first") == "0" {
				for _, id := range f.mfaRole {
					us = append(us, map[string]string{"id": id})
				}
			}
			out(us)
		case path == "/roles/grantra-mfa-required":
			out(map[string]string{"id": "rid", "name": "grantra-mfa-required"})
		case strings.HasSuffix(path, "/role-mappings/realm"):
			user := strings.Split(path, "/")[2]
			if r.Method == http.MethodPost {
				f.mfaRole = append(f.mfaRole, user)
			} else {
				f.mfaRole = slices.DeleteFunc(f.mfaRole, func(s string) bool { return s == user })
			}
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete:
			// Flow steps, configs and the role itself.
			w.WriteHeader(http.StatusCreated)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGrantraLogin(t *testing.T) {
	t.Setenv("GRANTRA_SECRET_LOGIN_TEST", "s3cret")
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")
	luis := e.identity("luis", "")

	status := decode[loginStatus](t, e.do(admin, http.MethodGet, "/api/v1/login", ""))
	if status.Connected {
		t.Fatalf("connected before any connector: %+v", status)
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/login/apps", `{"client_id": "wiki", "name": "Wiki", "redirect_uris": ["https://wiki.example.com/cb"]}`); rec.Code != http.StatusConflict {
		t.Errorf("create app without Grantra Login = %d", rec.Code)
	}

	f := &fakeLogin{flow: "browser", clients: []map[string]any{
		{"id": "c0", "clientId": "account-console", "enabled": true},
		{"id": "c1", "clientId": "grantra-console", "name": "Grantra console", "enabled": true, "redirectUris": []string{"http://localhost:8080/auth/callback"}},
	}}
	srv := f.serve(t)
	app := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Grantra Login"}`))
	if rec := e.do(admin, http.MethodPut, "/api/v1/applications/"+app.ID+"/connector",
		`{"kind": "grantra-login", "config": {"url": "`+srv.URL+`", "client_secret": "env:GRANTRA_SECRET_LOGIN_TEST", "allow_http": true}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("set connector = %d %s", rec.Code, rec.Body)
	}

	status = decode[loginStatus](t, e.do(auditor, http.MethodGet, "/api/v1/login", ""))
	if !status.Connected || status.Realm.Realm != "grantra" || status.Policy.MFA != "optional" || len(status.Apps) != 1 || status.Apps[0].ClientID != "grantra-console" {
		t.Fatalf("status = %+v", status)
	}
	if rec := e.do(ana, http.MethodGet, "/api/v1/login", ""); rec.Code != http.StatusForbidden {
		t.Errorf("employee reads Grantra Login = %d", rec.Code)
	}
	if rec := e.do(auditor, http.MethodPut, "/api/v1/login/policy", `{"mfa": "required"}`); rec.Code != http.StatusForbidden {
		t.Errorf("auditor sets policy = %d", rec.Code)
	}

	// Apps.
	for _, bad := range []string{
		`{"client_id": "wiki", "name": "Wiki", "redirect_uris": ["https://*.example.com/cb"]}`,
		`{"client_id": "wiki", "name": "Wiki", "redirect_uris": ["http://wiki.example.com/cb"]}`,
		`{"client_id": "admin-cli", "name": "x", "redirect_uris": ["https://x.example.com"]}`,
		`{"client_id": "wiki", "name": "Wiki", "redirect_uris": []}`,
	} {
		if rec := e.do(admin, http.MethodPost, "/api/v1/login/apps", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("create %s = %d %s", bad, rec.Code, rec.Body)
		}
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/login/apps", `{"client_id": "wiki", "name": "Wiki", "redirect_uris": ["https://wiki.example.com/cb"]}`)
	created := decode[struct {
		App    map[string]any `json:"app"`
		Secret string         `json:"secret"`
	}](t, rec)
	if rec.Code != http.StatusCreated || created.Secret != "secret-GET" || created.App["id"] != "cwiki" {
		t.Fatalf("create app = %d %+v", rec.Code, created)
	}
	rotated := decode[map[string]string](t, e.do(admin, http.MethodPost, "/api/v1/login/apps/cwiki/secret", ""))
	if rotated["secret"] != "secret-POST" {
		t.Errorf("rotate = %v", rotated)
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/login/apps/c0/secret", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("rotate a Keycloak client = %d", rec.Code)
	}

	// Risk-based MFA: Ana holds a high-risk entitlement, Luis does not, and
	// someone Grantra does not know is in the role.
	exec := func(sql string, args ...any) {
		t.Helper()
		err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
			_, err := tx.Exec(context.Background(), sql, args...)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	erp := decode[catalog.Application](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "ERP"}`))
	approve := decode[catalog.Entitlement](t, e.do(admin, http.MethodPost, "/api/v1/applications/"+erp.ID+"/entitlements",
		`{"native_id": "approve", "name": "Approve payments", "risk": "high"}`))
	exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by) VALUES ($1, $2, $3, 'manual', 'test')`,
		e.tenant, ana, approve.ID)
	exec(`INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username) VALUES ($1, $2, $3, 'kc-ana', 'ana'), ($1, $2, $4, 'kc-luis', 'luis')`,
		e.tenant, app.ID, ana, luis)
	f.mfaRole = []string{"kc-stranger"}

	rec = e.do(admin, http.MethodPut, "/api/v1/login/policy", `{"mfa": "sensitive", "passkeys": true}`)
	set := decode[struct {
		Sync struct{ Added, Removed int } `json:"sync"`
	}](t, rec)
	if rec.Code != http.StatusOK || set.Sync.Added != 1 || set.Sync.Removed != 1 {
		t.Fatalf("set policy = %d %+v", rec.Code, set)
	}
	if !f.copied || f.flow != "grantra browser" || !f.passkeys || !slices.Equal(f.mfaRole, []string{"kc-ana"}) {
		t.Errorf("realm: flow %q passkeys %v role %v", f.flow, f.passkeys, f.mfaRole)
	}
	if rec := e.do(admin, http.MethodPut, "/api/v1/login/policy", `{"mfa": "sometimes"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad policy = %d", rec.Code)
	}

	// A service account without the roles it needs gets an answer an admin
	// can act on.
	f.forbidden = true
	status = decode[loginStatus](t, e.do(admin, http.MethodGet, "/api/v1/login", ""))
	if !status.Connected || !strings.Contains(status.Error, "manage-realm") {
		t.Errorf("forbidden status = %+v", status)
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/login/apps/cwiki/secret", ""); rec.Code != http.StatusBadGateway {
		t.Errorf("forbidden rotate = %d", rec.Code)
	}

	var audited int
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action LIKE 'login.%'`).Scan(&audited)
	})
	if err != nil || audited != 5 {
		t.Errorf("login audit events = %d, %v; want app, secret, policy and two role changes", audited, err)
	}
}
