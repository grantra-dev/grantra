package console

import (
	"net/http"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

func TestIdentitySources(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)

	// Only admins create sources.
	body := `{"name": "HR export", "kind": "csv", "config": {"columns": {"employee_id": "Worker ID"}}}`
	for _, as := range []string{auditor, helpdesk} {
		if rec := e.do(as, http.MethodPost, "/api/v1/identity-sources", body); rec.Code != http.StatusForbidden {
			t.Errorf("create as non-admin = %d", rec.Code)
		}
	}
	rec := e.do(admin, http.MethodPost, "/api/v1/identity-sources", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	src := decode[lifecycle.Source](t, rec)
	if src.Config.Missing != "leave" || src.Config.MaxLeaverPercent != 10 {
		t.Errorf("defaults not filled in: %+v", src.Config)
	}
	for _, tt := range []struct {
		body string
		want int
	}{
		{body, http.StatusConflict},
		{`{"name": "W", "kind": "workday"}`, http.StatusBadRequest},
		{`{"name": "X", "kind": "csv", "config": {"missing": "delete"}}`, http.StatusBadRequest},
		{`{"name": "X", "kind": "csv", "extra": 1}`, http.StatusBadRequest},
	} {
		if rec := e.do(admin, http.MethodPost, "/api/v1/identity-sources", tt.body); rec.Code != tt.want {
			t.Errorf("create %s = %d, want %d: %s", tt.body, rec.Code, tt.want, rec.Body)
		}
	}

	runs := "/api/v1/identity-sources/" + src.ID + "/runs"
	feed := "Worker ID,display_name,email,manager_id,department\n" +
		"W1,Ana,ana@example.com,,Eng\n" +
		"W2,Luis,luis@example.com,W1,Eng\n"

	// Auditors and helpdesk cannot feed a source; auditors can look.
	if rec := e.do(auditor, http.MethodPost, runs, feed); rec.Code != http.StatusForbidden {
		t.Errorf("sync as auditor = %d", rec.Code)
	}
	// The permission is checked before the upload is read: a file over the
	// size limit from someone who may not sync is refused, not measured.
	if rec := e.do(auditor, http.MethodPost, runs, strings.Repeat("x", maxFeedBytes+1)); rec.Code != http.StatusForbidden {
		t.Errorf("oversized sync as auditor = %d, want 403", rec.Code)
	}
	if rec := e.do(helpdesk, http.MethodGet, "/api/v1/identity-sources", ""); rec.Code != http.StatusForbidden {
		t.Errorf("list as helpdesk = %d", rec.Code)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'authz.denied' AND details->>'action' = 'identity_source.sync'`); n != 2 {
		t.Errorf("denied sync audit events = %d, want 1", n)
	}

	rec = e.do(admin, http.MethodPost, runs+"?dry_run=true", feed)
	if rec.Code != http.StatusOK {
		t.Fatalf("dry run = %d %s", rec.Code, rec.Body)
	}
	if got := decode[lifecycle.SyncResult](t, rec); got.Status != lifecycle.RunPlanned || len(got.Changes) != 2 {
		t.Errorf("dry run = %+v", got)
	}
	rec = e.do(admin, http.MethodPost, runs, feed)
	if got := decode[lifecycle.SyncResult](t, rec); rec.Code != http.StatusOK || got.Status != lifecycle.RunApplied || got.Counts[lifecycle.Joiner] != 2 {
		t.Fatalf("sync = %d %+v", rec.Code, got)
	}
	if rec := e.do(admin, http.MethodPost, runs, "Worker ID,display_name\nW1,Ana\nW1,Ana\n"); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("sync with duplicate ids = %d, want 422", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, runs+"?dry_run=maybe", feed); rec.Code != http.StatusBadRequest {
		t.Errorf("bad dry_run = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, "/api/v1/identity-sources/00000000-0000-0000-0000-000000000000/runs", feed); rec.Code != http.StatusNotFound {
		t.Errorf("unknown source = %d", rec.Code)
	}
	if rec := e.do(admin, http.MethodPost, runs, feed, "Sec-Fetch-Site", "cross-site"); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site sync = %d", rec.Code)
	}

	// The identities carry what HR said about them.
	list := decode[struct{ Identities []identity }](t, e.do(admin, http.MethodGet, "/api/v1/identities", ""))
	var luis identity
	for _, i := range list.Identities {
		if i.ExternalID == "W2" {
			luis = i
		}
	}
	if luis.SourceID != src.ID || luis.Attributes["department"] != "Eng" || luis.ManagerID == "" {
		t.Errorf("luis = %+v", luis)
	}

	// Auditors read sources, runs and lifecycle events.
	for _, path := range []string{"/api/v1/identity-sources", "/api/v1/identity-sources/" + src.ID, runs, "/api/v1/lifecycle-events", "/api/v1/lifecycle-events?identity=" + luis.ID} {
		if rec := e.do(auditor, http.MethodGet, path, ""); rec.Code != http.StatusOK {
			t.Errorf("GET %s as auditor = %d %s", path, rec.Code, rec.Body)
		}
	}
	srcs := decode[struct{ Sources []lifecycle.Source }](t, e.do(auditor, http.MethodGet, "/api/v1/identity-sources", ""))
	if len(srcs.Sources) != 1 || srcs.Sources[0].LastRun == nil || srcs.Sources[0].LastRun.Status != lifecycle.RunInvalid {
		t.Errorf("sources = %+v", srcs.Sources)
	}
	events := decode[struct{ Events []lifecycle.Event }](t, e.do(auditor, http.MethodGet, "/api/v1/lifecycle-events", ""))
	if len(events.Events) != 2 || events.Events[0].Kind != lifecycle.Joiner {
		t.Errorf("events = %+v", events.Events)
	}
	for _, path := range []string{"/api/v1/lifecycle-events?identity=x", "/api/v1/lifecycle-events?before=0"} {
		if rec := e.do(auditor, http.MethodGet, path, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
	if rec := e.do(helpdesk, http.MethodGet, "/api/v1/lifecycle-events", ""); rec.Code != http.StatusForbidden {
		t.Errorf("lifecycle events as helpdesk = %d", rec.Code)
	}

	// Too big a file never reaches the database.
	big := strings.Repeat("x", maxFeedBytes+1)
	if rec := e.do(admin, http.MethodPost, runs, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge file = %d", rec.Code)
	}
}

// Sources can be renamed and remapped after they are created, so a company
// whose HR export changes its column names fixes the source from the console.
func TestUpdateSource(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	src := decode[lifecycle.Source](t, e.do(admin, http.MethodPost, "/api/v1/identity-sources", `{"name": "HR", "kind": "csv"}`))
	e.do(admin, http.MethodPost, "/api/v1/identity-sources", `{"name": "Contractors", "kind": "csv"}`)
	path := "/api/v1/identity-sources/" + src.ID
	feed := "Worker ID,Full Name\nW1,Ana\n"

	// The export uses its own names: the sync fails until the source maps them.
	if rec := e.do(admin, http.MethodPost, path+"/runs?dry_run=true", feed); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unmapped sync = %d %s", rec.Code, rec.Body)
	}
	update := `{"name": "Workday", "config": {"columns": {"employee_id": "Worker ID", "display_name": "Full Name"}, "missing": "ignore"}}`
	if rec := e.do(auditor, http.MethodPut, path, update); rec.Code != http.StatusForbidden {
		t.Errorf("update as auditor = %d", rec.Code)
	}
	rec := e.do(admin, http.MethodPut, path, update)
	if rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	got := decode[lifecycle.Source](t, rec)
	if got.Name != "Workday" || got.Config.Columns["employee_id"] != "Worker ID" || got.Config.Missing != "ignore" || got.Config.MaxLeaverPercent != 10 {
		t.Errorf("updated = %+v", got)
	}
	if rec := e.do(admin, http.MethodPost, path+"/runs", feed); rec.Code != http.StatusOK {
		t.Errorf("mapped sync = %d %s", rec.Code, rec.Body)
	}
	if n := e.count(`SELECT count(*) FROM audit_events WHERE action = 'identity_source.updated'
		AND details->'before'->>'name' = 'HR' AND details->>'name' = 'Workday'`); n != 1 {
		t.Errorf("update audit events = %d, want 1", n)
	}

	for _, tt := range []struct {
		path, body string
		want       int
	}{
		{path, `{"name": "Contractors"}`, http.StatusConflict},
		{path, `{"name": ""}`, http.StatusBadRequest},
		{path, `{"name": "X", "config": {"columns": {"salary": "Pay"}}}`, http.StatusBadRequest},
		{path, `{"name": "X", "kind": "csv"}`, http.StatusBadRequest},
		{"/api/v1/identity-sources/00000000-0000-0000-0000-000000000000", `{"name": "X"}`, http.StatusNotFound},
		{"/api/v1/identity-sources/nope", `{"name": "X"}`, http.StatusNotFound},
	} {
		if rec := e.do(admin, http.MethodPut, tt.path, tt.body); rec.Code != tt.want {
			t.Errorf("PUT %s %s = %d, want %d: %s", tt.path, tt.body, rec.Code, tt.want, rec.Body)
		}
	}
}
