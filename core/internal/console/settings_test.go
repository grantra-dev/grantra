package console

import (
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

func TestSettings(t *testing.T) {
	e := newEnv(t)
	admin := e.identity("admin", "", authz.RoleAdmin)
	auditor := e.identity("auditor", "", authz.RoleAuditor)
	helpdesk := e.identity("helpdesk", "", authz.RoleHelpdesk)
	ana := e.identity("ana", "")

	// Everyone gets the settings the console explains things with.
	if got := decode[me](t, e.do(ana, http.MethodGet, "/api/v1/me", "")).Settings; got[settings.UnusedDays] != 90 || len(got) == 0 {
		t.Errorf("me settings = %v", got)
	} else if _, ok := got[settings.BreakGlassMinutes]; ok {
		t.Errorf("me settings include the break-glass limit: %v", got)
	}

	page := decode[settingsPage](t, e.do(auditor, http.MethodGet, "/api/v1/settings", ""))
	if len(page.Settings) != len(settings.List()) {
		t.Errorf("auditor's page = %d settings", len(page.Settings))
	}
	if p := decode[settingsPage](t, e.do(admin, http.MethodGet, "/api/v1/settings", "")); p.SecondAdmin {
		t.Errorf("the only admin's page says a second admin approves")
	}
	for _, tc := range []struct {
		who, method, path, body string
		want                    int
	}{
		{ana, http.MethodGet, "/api/v1/settings", "", http.StatusForbidden},
		{helpdesk, http.MethodGet, "/api/v1/settings", "", http.StatusForbidden},
		{auditor, http.MethodPut, "/api/v1/settings/" + settings.UnusedDays, `{"value": 60}`, http.StatusForbidden},
		{admin, http.MethodPut, "/api/v1/settings/nope.unknown", `{"value": 60}`, http.StatusNotFound},
		{admin, http.MethodPut, "/api/v1/settings/" + settings.UnusedDays, `{}`, http.StatusBadRequest},
		{admin, http.MethodPut, "/api/v1/settings/" + settings.UnusedDays, `{"value": 1}`, http.StatusBadRequest},
		{admin, http.MethodPost, "/api/v1/setting-changes/not-an-id/decision", `{"approve": true}`, http.StatusNotFound},
	} {
		if rec := e.do(tc.who, tc.method, tc.path, tc.body); rec.Code != tc.want {
			t.Errorf("%s %s %s = %d, want %d: %s", tc.method, tc.path, tc.body, rec.Code, tc.want, rec.Body)
		}
	}

	// The only admin changes things at once.
	rec := e.do(admin, http.MethodPut, "/api/v1/settings/"+settings.BreakGlassMinutes, `{"value": 90, "reason": "night shifts"}`)
	if s := decode[settings.Setting](t, rec); rec.Code != http.StatusOK || s.Value != 90 || s.Pending != nil {
		t.Fatalf("save as the only admin = %d %+v", rec.Code, s)
	}

	// With a second admin, a less strict change waits for them.
	other := e.identity("other admin", "", authz.RoleAdmin)
	rec = e.do(admin, http.MethodPut, "/api/v1/settings/"+settings.BreakGlassMinutes, `{"value": 120}`)
	s := decode[settings.Setting](t, rec)
	if rec.Code != http.StatusAccepted || s.Value != 90 || s.Pending == nil {
		t.Fatalf("loosen with a second admin = %d %+v", rec.Code, s)
	}
	if p := decode[settingsPage](t, e.do(admin, http.MethodGet, "/api/v1/settings", "")); !p.SecondAdmin {
		t.Errorf("the page does not say a second admin approves")
	}
	inbox := func(who string) []settings.Change {
		return decode[struct {
			SettingChanges []settings.Change `json:"setting_changes"`
			Total          int
		}](t, e.do(who, http.MethodGet, "/api/v1/me/inbox", "")).SettingChanges
	}
	if got := inbox(admin); len(got) != 0 {
		t.Errorf("the proposer's own inbox has %v", got)
	}
	if got := inbox(other); len(got) != 1 || got[0].ID != s.Pending.ID {
		t.Fatalf("the other admin's inbox = %+v", got)
	}
	path := "/api/v1/setting-changes/" + s.Pending.ID + "/decision"
	if rec := e.do(admin, http.MethodPost, path, `{"approve": true}`); rec.Code != http.StatusConflict {
		t.Errorf("approving one's own = %d", rec.Code)
	}
	if rec := e.do(auditor, http.MethodPost, path, `{"approve": true}`); rec.Code != http.StatusForbidden {
		t.Errorf("an auditor approving = %d", rec.Code)
	}
	if rec := e.do(other, http.MethodPost, path, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("deciding without saying = %d", rec.Code)
	}
	rec = e.do(other, http.MethodPost, path, `{"approve": true, "comment": "agreed in the CAB"}`)
	if c := decode[settings.Change](t, rec); rec.Code != http.StatusOK || c.Status != settings.Approved {
		t.Fatalf("approve = %d %+v", rec.Code, c)
	}
	if got := inbox(other); len(got) != 0 {
		t.Errorf("inbox after approving = %+v", got)
	}
	page = decode[settingsPage](t, e.do(admin, http.MethodGet, "/api/v1/settings", ""))
	for _, s := range page.Settings {
		if s.Key == settings.BreakGlassMinutes && (s.Value != 120 || s.Pending != nil) {
			t.Errorf("after approval = %+v", s)
		}
	}
}
