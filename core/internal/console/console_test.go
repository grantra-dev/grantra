package console

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

// signedIn stands in for auth.Service.Require: the X-Test-Identity header
// names the signed-in person.
func signedIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Test-Identity")
		if id == "" {
			http.Error(w, "not signed in", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.ContextWithPrincipal(r.Context(), auth.Principal{IdentityID: id})))
	})
}

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	mux    *http.ServeMux
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "console")
	if err := authz.EnsureBuiltinApp(context.Background(), pool, tenant); err != nil {
		t.Fatal(err)
	}
	az, err := authz.New()
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(pool, tenant, az, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux, signedIn)
	return &env{t: t, pool: pool, tenant: tenant, mux: mux}
}

func (e *env) identity(name, managerID string, roles ...string) string {
	e.t.Helper()
	var id string
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		var mgr *string
		if managerID != "" {
			mgr = &managerID
		}
		if err := tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email, manager_id) VALUES ($1, $2, $2 || '@example.com', $3) RETURNING id`,
			e.tenant, name, mgr).Scan(&id); err != nil {
			return err
		}
		for _, r := range roles {
			if err := authz.Grant(context.Background(), tx, e.tenant, r, id, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *env) do(as, method, path, body string, headers ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if as != "" {
		req.Header.Set("X-Test-Identity", as)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func (e *env) count(sql string, args ...any) int {
	e.t.Helper()
	var n int
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql, args...).Scan(&n)
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(rec.Body).Decode(&v); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return v
}

func TestReadAccess(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	boss := e.identity("boss", "")
	ana := e.identity("ana", boss)
	luis := e.identity("luis", "")

	tests := []struct {
		as, path string
		want     int
	}{
		{ana, "/api/v1/identities/" + ana, http.StatusOK},
		{boss, "/api/v1/identities/" + ana, http.StatusOK},
		{ana, "/api/v1/identities/" + boss, http.StatusForbidden},
		{luis, "/api/v1/identities/" + ana, http.StatusForbidden},
		{helpdesk, "/api/v1/identities/" + ana, http.StatusOK},
		{auditor, "/api/v1/identities/" + ana, http.StatusOK},
		{admin, "/api/v1/identities/00000000-0000-0000-0000-000000000000", http.StatusNotFound},
		{admin, "/api/v1/identities/not-a-uuid", http.StatusNotFound},

		{ana, "/api/v1/identities", http.StatusForbidden},
		{boss, "/api/v1/identities", http.StatusForbidden},
		{helpdesk, "/api/v1/identities", http.StatusOK},
		{auditor, "/api/v1/identities", http.StatusOK},
		{admin, "/api/v1/identities?limit=500", http.StatusBadRequest},
		{admin, "/api/v1/identities?after=x", http.StatusBadRequest},

		{admin, "/api/v1/audit-events", http.StatusOK},
		{auditor, "/api/v1/audit-events?limit=10", http.StatusOK},
		{helpdesk, "/api/v1/audit-events", http.StatusForbidden},
		{ana, "/api/v1/audit-events", http.StatusForbidden},

		{admin, "/api/v1/platform-roles", http.StatusOK},
		{auditor, "/api/v1/platform-roles", http.StatusOK},
		{helpdesk, "/api/v1/platform-roles", http.StatusForbidden},
		{ana, "/api/v1/platform-roles", http.StatusForbidden},

		{"", "/api/v1/me", http.StatusUnauthorized},
	}
	for _, tt := range tests {
		if rec := e.do(tt.as, http.MethodGet, tt.path, ""); rec.Code != tt.want {
			t.Errorf("GET %s as %s = %d, want %d: %s", tt.path, tt.as, rec.Code, tt.want, rec.Body)
		}
	}

	// Listing pages by id.
	first := decode[struct{ Identities []identity }](t, e.do(admin, http.MethodGet, "/api/v1/identities?limit=4", ""))
	rest := decode[struct{ Identities []identity }](t, e.do(admin, http.MethodGet, "/api/v1/identities?after="+first.Identities[3].ID, ""))
	if len(first.Identities) != 4 || len(rest.Identities) != 2 {
		t.Errorf("pages = %d + %d identities, want 4 + 2", len(first.Identities), len(rest.Identities))
	}
}

func TestMe(t *testing.T) {
	e := newEnv(t)
	boss := e.identity("boss", "", authz.RoleAuditor)
	ana := e.identity("ana", boss)

	got := decode[me](t, e.do(boss, http.MethodGet, "/api/v1/me", ""))
	if got.IdentityID != boss || !got.ManagesPeople || got.OwnsApplications || len(got.PlatformRoles) != 1 || got.PlatformRoles[0] != authz.RoleAuditor {
		t.Errorf("boss me = %+v", got)
	}
	got = decode[me](t, e.do(ana, http.MethodGet, "/api/v1/me", ""))
	if got.ManagesPeople || got.PlatformRoles == nil || len(got.PlatformRoles) != 0 {
		t.Errorf("ana me = %+v", got)
	}
}

func TestPlatformRoleChanges(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	ana := e.identity("ana", "")
	put := func(as, role, who, body string, headers ...string) int {
		return e.do(as, http.MethodPut, "/api/v1/platform-roles/"+role+"/members/"+who, body, headers...).Code
	}
	del := func(as, role, who string) int {
		return e.do(as, http.MethodDelete, "/api/v1/platform-roles/"+role+"/members/"+who, "").Code
	}

	// Only admins grant, and never to themselves.
	for _, tt := range []struct {
		name string
		as   string
		role string
		who  string
		want int
	}{
		{"helpdesk grants", helpdesk, authz.RoleHelpdesk, ana, http.StatusForbidden},
		{"auditor grants", auditor, authz.RoleAuditor, ana, http.StatusForbidden},
		{"employee grants", ana, authz.RoleAdmin, ana, http.StatusForbidden},
		{"admin grants to self", admin, authz.RoleAuditor, admin, http.StatusForbidden},
		{"unknown role", admin, "grantra-root", ana, http.StatusNotFound},
		{"unknown person", admin, authz.RoleAuditor, "00000000-0000-0000-0000-000000000000", http.StatusNotFound},
	} {
		if got := put(tt.as, tt.role, tt.who, ""); got != tt.want {
			t.Errorf("%s = %d, want %d", tt.name, got, tt.want)
		}
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'authz.denied'`); n != 4 {
		t.Errorf("authz.denied audit events = %d, want 4", n)
	}

	// A time-bound grant shows up with its expiry.
	until := time.Now().Add(48 * time.Hour).UTC().Truncate(time.Second)
	if got := put(admin, authz.RoleAuditor, ana, `{"expires_at":"`+until.Format(time.RFC3339)+`"}`); got != http.StatusNoContent {
		t.Fatalf("grant auditor = %d", got)
	}
	if got := put(admin, authz.RoleAuditor, ana, `{"expires_at":"2000-01-01T00:00:00Z"}`); got != http.StatusBadRequest {
		t.Errorf("grant with past expiry = %d, want 400", got)
	}
	if got := put(admin, authz.RoleAuditor, ana, `{"expires":"soon"}`); got != http.StatusBadRequest {
		t.Errorf("grant with unknown field = %d, want 400", got)
	}
	roles := decode[struct{ Roles []platformRole }](t, e.do(admin, http.MethodGet, "/api/v1/platform-roles", ""))
	var found bool
	for _, r := range roles.Roles {
		for _, m := range r.Members {
			if r.Name == authz.RoleAuditor && m.IdentityID == ana {
				found = m.ExpiresAt != nil && m.ExpiresAt.Equal(until)
			}
		}
	}
	if !found {
		t.Errorf("ana not listed as auditor until %s: %+v", until, roles.Roles)
	}
	// The new auditor can now read the audit log, and still cannot grant.
	if got := e.do(ana, http.MethodGet, "/api/v1/audit-events", "").Code; got != http.StatusOK {
		t.Errorf("new auditor reads audit log = %d", got)
	}

	// Cross-site writes are refused before anything else.
	if got := put(admin, authz.RoleHelpdesk, ana, "", "Sec-Fetch-Site", "cross-site"); got != http.StatusForbidden {
		t.Errorf("cross-site grant = %d, want 403", got)
	}

	// Revoking: the last admin stays, everything else goes.
	if got := del(admin, authz.RoleAdmin, admin); got != http.StatusConflict {
		t.Errorf("revoke last admin = %d, want 409", got)
	}
	if got := del(admin, authz.RoleAuditor, ana); got != http.StatusNoContent {
		t.Errorf("revoke auditor = %d", got)
	}
	if got := del(admin, authz.RoleAuditor, ana); got != http.StatusNotFound {
		t.Errorf("revoke again = %d, want 404", got)
	}
	if got := del(auditor, authz.RoleHelpdesk, helpdesk); got != http.StatusForbidden {
		t.Errorf("auditor revokes = %d, want 403", got)
	}
	if got := e.do(ana, http.MethodGet, "/api/v1/audit-events", "").Code; got != http.StatusForbidden {
		t.Errorf("former auditor reads audit log = %d, want 403", got)
	}

	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'platform_role.granted' AND actor = $1`, "identity:"+admin); n != 1 {
		t.Errorf("platform_role.granted audit events = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'platform_role.revoked' AND target_id = $1`, ana); n != 1 {
		t.Errorf("platform_role.revoked audit events = %d, want 1", n)
	}
}

func TestOffboardedAdminLosesAccess(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	err := db.InTenant(context.Background(), e.pool, e.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `UPDATE identities SET status = 'suspended' WHERE id = $1`, admin)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := e.do(admin, http.MethodGet, "/api/v1/identities", "").Code; got != http.StatusForbidden {
		t.Errorf("suspended admin lists identities = %d, want 403", got)
	}
}
