package console

import (
	"net/http"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/accounts"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
	"github.com/grantra-dev/grantra/core/internal/servicetoken"
)

type created struct {
	Token  servicetoken.Token
	Secret string
}

// bearer sends a request with a service token and no session.
func (e *env) bearer(secret, method, path, body string) int {
	e.t.Helper()
	return e.do("", method, path, body, "Authorization", "Bearer "+secret).Code
}

// A nightly job feeds an HR source with a token that can do nothing else.
func TestServiceTokens(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	hr := decode[lifecycle.Source](t, e.do(admin, http.MethodPost, "/api/v1/identity-sources", `{"name": "HR", "kind": "csv"}`))
	other := decode[lifecycle.Source](t, e.do(admin, http.MethodPost, "/api/v1/identity-sources", `{"name": "Contractors", "kind": "csv"}`))
	app := decode[struct{ ID string }](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Legacy ERP"}`))

	body := `{"name": "Nightly HR", "scopes": ["identity_source.sync:` + hr.ID + `"], "expires_in_days": 30}`
	for _, as := range []string{auditor, helpdesk} {
		if rec := e.do(as, http.MethodPost, "/api/v1/service-tokens", body); rec.Code != http.StatusForbidden {
			t.Errorf("create as non-admin = %d", rec.Code)
		}
	}
	for _, bad := range []string{
		`{"name": "", "scopes": ["identity_source.sync"], "expires_in_days": 30}`,
		`{"name": "x", "scopes": [], "expires_in_days": 30}`,
		`{"name": "x", "scopes": ["identity.read"], "expires_in_days": 30}`,
		`{"name": "x", "scopes": ["identity_source.sync"], "expires_in_days": 0}`,
		`{"name": "x", "scopes": ["identity_source.sync"], "expires_in_days": 366}`,
		`{"name": "x", "scopes": ["identity_source.sync:00000000-0000-0000-0000-000000000000"], "expires_in_days": 30}`,
		`{"name": "x", "scopes": ["identity_source.sync:nope"], "expires_in_days": 30}`,
		// The application reads by hand, not from files.
		`{"name": "x", "scopes": ["application.import_accounts:` + app.ID + `"], "expires_in_days": 30}`,
	} {
		if rec := e.do(admin, http.MethodPost, "/api/v1/service-tokens", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("create %s = %d, want 400", bad, rec.Code)
		}
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/service-tokens", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	tok := decode[created](t, rec)
	if !strings.HasPrefix(tok.Secret, servicetoken.Prefix) || !strings.HasPrefix(tok.Secret, tok.Token.Prefix) || tok.Token.CreatedBy != "identity:"+admin {
		t.Fatalf("created = %+v", tok)
	}

	// Auditors see tokens, never their secrets.
	rec = e.do(auditor, http.MethodGet, "/api/v1/service-tokens", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), tok.Secret) || strings.Contains(rec.Body.String(), "hash") {
		t.Fatalf("list = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(helpdesk, http.MethodGet, "/api/v1/service-tokens", ""); rec.Code != http.StatusForbidden {
		t.Errorf("list as helpdesk = %d", rec.Code)
	}

	feed := "employee_id,display_name\nE1,Ana\n"
	if code := e.bearer(tok.Secret, http.MethodPost, "/api/v1/identity-sources/"+hr.ID+"/runs", feed); code != http.StatusOK {
		t.Fatalf("sync with token = %d", code)
	}
	actor := "service_token:" + tok.Token.ID
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'identity_source.synced' AND actor = $1`, actor); n != 1 {
		t.Errorf("sync audited as token %d times, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM identity_source_runs WHERE actor = $1`, actor); n != 1 {
		t.Errorf("runs by token = %d", n)
	}
	// The scheme is case insensitive, as HTTP says.
	if rec := e.do("", http.MethodPost, "/api/v1/identity-sources/"+hr.ID+"/runs?dry_run=true", feed, "Authorization", "bearer "+tok.Secret); rec.Code != http.StatusOK {
		t.Errorf("lower-case scheme = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM service_tokens WHERE last_used_at IS NOT NULL`); n != 1 {
		t.Errorf("last use not noted")
	}

	// It feeds that source and nothing else.
	if code := e.bearer(tok.Secret, http.MethodPost, "/api/v1/identity-sources/"+other.ID+"/runs", feed); code != http.StatusForbidden {
		t.Errorf("sync another source = %d", code)
	}
	if code := e.bearer(tok.Secret, http.MethodPost, "/api/v1/applications/"+app.ID+"/account-imports", "username\nana\n"); code != http.StatusForbidden {
		t.Errorf("import with a sync token = %d", code)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'authz.denied' AND actor = $1`, actor); n != 2 {
		t.Errorf("denied token requests audited %d times, want 2", n)
	}
	// Tokens only open the upload endpoints.
	for _, p := range []string{"/api/v1/identity-sources", "/api/v1/me", "/api/v1/service-tokens"} {
		if code := e.bearer(tok.Secret, http.MethodGet, p, ""); code != http.StatusUnauthorized {
			t.Errorf("GET %s with a token = %d", p, code)
		}
	}
	for _, bad := range []string{"", "grantra_st_nope", "grantra_ak_" + tok.Secret[len(servicetoken.Prefix):]} {
		if code := e.bearer(bad, http.MethodPost, "/api/v1/identity-sources/"+hr.ID+"/runs", feed); code != http.StatusUnauthorized {
			t.Errorf("sync with %q = %d", bad, code)
		}
	}

	// Expired and revoked tokens stop working.
	e.exec(`UPDATE service_tokens SET expires_at = now() - interval '1 second' WHERE id = $1`, tok.Token.ID)
	if code := e.bearer(tok.Secret, http.MethodPost, "/api/v1/identity-sources/"+hr.ID+"/runs", feed); code != http.StatusUnauthorized {
		t.Errorf("expired token = %d", code)
	}
	e.exec(`UPDATE service_tokens SET expires_at = now() + interval '1 day' WHERE id = $1`, tok.Token.ID)
	if rec := e.do(auditor, http.MethodDelete, "/api/v1/service-tokens/"+tok.Token.ID, ""); rec.Code != http.StatusForbidden {
		t.Errorf("revoke as auditor = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodDelete, "/api/v1/service-tokens/"+tok.Token.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	if code := e.bearer(tok.Secret, http.MethodPost, "/api/v1/identity-sources/"+hr.ID+"/runs", feed); code != http.StatusUnauthorized {
		t.Errorf("revoked token = %d", code)
	}
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "nope"} {
		if rec := e.do(admin, http.MethodDelete, "/api/v1/service-tokens/"+id, ""); rec.Code != http.StatusNotFound {
			t.Errorf("revoke %s = %d", id, rec.Code)
		}
	}
	for _, action := range []string{"service_token.created", "service_token.revoked"} {
		if n := e.count(`SELECT count(*) FROM audit_events WHERE action = $1 AND target_id = $2`, action, tok.Token.ID); n != 1 {
			t.Errorf("%s audit events = %d", action, n)
		}
	}
}

// An application with no API takes its accounts from a file, from the
// console or from a script with a token.
func TestAccountImports(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	app := decode[struct{ ID string }](t, e.do(admin, http.MethodPost, "/api/v1/applications", `{"name": "Legacy ERP"}`))
	path := "/api/v1/applications/" + app.ID + "/account-imports"
	file := "Login,Roles\nana,Admin;Billing\nluis,Billing\n"

	if rec := e.do(admin, http.MethodPost, path, file); rec.Code != http.StatusBadRequest {
		t.Errorf("import before the csv connector = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPut, "/api/v1/applications/"+app.ID+"/connector",
		`{"kind": "csv", "config": {"columns": {"username": "Login", "entitlements": "Roles"}}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("set connector = %d %s", rec.Code, rec.Body)
	}
	if rec := e.do(auditor, http.MethodPost, path, file); rec.Code != http.StatusForbidden {
		t.Errorf("import as auditor = %d", rec.Code)
	}
	rec := e.do(admin, http.MethodPost, path+"?dry_run=true", file)
	if run := decode[accounts.Run](t, rec); rec.Code != http.StatusOK || run.Status != accounts.RunPlanned || run.Counts["accounts_new"] != 2 {
		t.Fatalf("dry run = %d %+v", rec.Code, run)
	}
	if n := e.count(`SELECT count(*) FROM accounts WHERE application_id = $1`, app.ID); n != 0 {
		t.Fatalf("dry run stored %d accounts", n)
	}
	rec = e.do(admin, http.MethodPost, path, file)
	if run := decode[accounts.Run](t, rec); rec.Code != http.StatusOK || run.Status != accounts.RunSucceeded || run.Counts["memberships_added"] != 3 {
		t.Fatalf("import = %d %+v", rec.Code, run)
	}
	rec = e.do(admin, http.MethodPost, path, "Login,Status\nana,maybe\n")
	if run := decode[accounts.Run](t, rec); rec.Code != http.StatusUnprocessableEntity || !strings.Contains(run.Error, "line 2") {
		t.Errorf("bad file = %d %+v", rec.Code, run)
	}
	if rec := e.do(admin, http.MethodPost, path+"?dry_run=perhaps", file); rec.Code != http.StatusBadRequest {
		t.Errorf("bad dry_run = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, path, strings.Repeat("x", maxFeedBytes+1)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge file = %d", rec.Code)
	}
	// The connector cannot be read without a file.
	if rec := e.do(admin, http.MethodPost, "/api/v1/applications/"+app.ID+"/aggregations", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("aggregate a csv application = %d %s", rec.Code, rec.Body)
	}

	// A script with an unpinned import token.
	tok := decode[created](t, e.do(admin, http.MethodPost, "/api/v1/service-tokens",
		`{"name": "ERP export", "scopes": ["application.import_accounts", "application.import_accounts"], "expires_in_days": 365}`))
	if len(tok.Token.Scopes) != 1 {
		t.Errorf("scopes not deduplicated: %v", tok.Token.Scopes)
	}
	if code := e.bearer(tok.Secret, http.MethodPost, path, "Login,Roles\nana,Billing\n"); code != http.StatusOK {
		t.Fatalf("import with token = %d", code)
	}
	if n := e.count(`SELECT count(*) FROM connector_runs WHERE actor = $1`, "service_token:"+tok.Token.ID); n != 1 {
		t.Errorf("runs by token = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM accounts WHERE application_id = $1 AND status = 'deleted'`, app.ID); n != 1 {
		t.Errorf("luis not gone after the token import")
	}
}
