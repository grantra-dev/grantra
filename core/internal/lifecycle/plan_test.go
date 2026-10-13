package lifecycle

import (
	"strings"
	"testing"
	"time"
)

const src = "source-1"

var today = time.Date(2026, 10, 13, 15, 0, 0, 0, time.UTC)

func date(s string) *time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return &t
}

func plan(t *testing.T, feed string, current []Current, mutate ...func(*Config)) Result {
	t.Helper()
	cfg := Config{}
	for _, m := range mutate {
		m(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	recs, issues, err := ParseCSV(strings.NewReader(feed), cfg)
	if err != nil || len(issues) > 0 {
		t.Fatal(err, issues)
	}
	return Plan(src, cfg, recs, current, today)
}

func byExt(r Result) map[string]Change {
	m := map[string]Change{}
	for _, c := range r.Changes {
		m[c.ExternalID] = c
	}
	return m
}

func staff(id, ext, name, status, manager, dept string) Current {
	return Current{ID: id, SourceID: src, ExternalID: ext, Kind: "human", Status: status, DisplayName: name,
		Email: strings.ToLower(name) + "@example.com", ManagerExternalID: manager,
		Attributes: map[string]string{"department": dept}}
}

const header = "employee_id,display_name,email,manager_id,department,status,start_date,end_date\n"

func TestPlanFirstSync(t *testing.T) {
	r := plan(t, header+
		"E1,Ana,ana@example.com,,Eng,,,\n"+
		"E2,Luis,luis@example.com,E1,Eng,,2026-11-02,\n"+
		"E3,Old,old@example.com,E1,Eng,terminated,,\n"+
		"E4,Pat,pat@example.com,E9,Eng,,,\n", nil)
	got := byExt(r)
	if got["E1"].Kind != Joiner || got["E1"].Status != "active" {
		t.Errorf("E1 = %+v", got["E1"])
	}
	if got["E2"].Kind != Joiner || got["E2"].Status != "pending" {
		t.Errorf("future starter = %+v", got["E2"])
	}
	if got["E3"].Kind != Update || got["E3"].Status != "terminated" || got["E3"].Reason != ReasonAlreadyGone {
		t.Errorf("already gone = %+v", got["E3"])
	}
	if len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0].Message, "E9") {
		t.Errorf("warnings = %v, want the unknown manager", r.Warnings)
	}
	if c := r.Counts(); c[Joiner] != 3 || c[Leaver] != 0 {
		t.Errorf("counts = %v", c)
	}
}

func TestPlanMoversLeaversRehires(t *testing.T) {
	current := []Current{
		staff("id-ana", "E1", "Ana", "active", "", "Eng"),
		staff("id-luis", "E2", "Luis", "active", "E1", "Eng"),
		staff("id-marta", "E3", "Marta", "active", "E1", "Eng"),
		staff("id-pedro", "E4", "Pedro", "terminated", "E1", "Eng"),
		staff("id-sara", "E5", "Sara", "active", "E1", "Eng"),
		staff("id-gone", "E6", "Gone", "active", "E1", "Eng"),
		staff("id-sus", "E7", "Sus", "suspended", "E1", "Eng"),
		staff("id-new", "E8", "New", "pending", "E1", "Eng"),
		staff("id-ex", "E9", "Ex", "terminated", "E1", "Eng"),
	}
	current[7].StartDate = date("2026-10-13")
	r := plan(t, header+
		"E1,Ana,ana@example.com,,Eng,,,\n"+ // unchanged
		"E2,Luis,luis@example.com,E1,Sales,,,\n"+ // department: mover
		"E3,Marta,marta@example.com,E2,Eng,,,\n"+ // manager: mover
		"E4,Pedro,pedro@example.com,E1,Eng,,,\n"+ // back: rehire
		"E5,Sara,sara@new.example.com,E1,Eng,,,2026-10-12\n"+ // last day passed: leaver
		"E7,Sus,sus@example.com,E1,Eng,,,\n"+ // stays suspended
		"E8,New,new@example.com,E1,Eng,,2026-10-13,\n"+ // first day: becomes active
		"E9,Ex,ex@example.com,E1,Eng,terminated,,\n", // still gone
		// E6 is missing: leaver
		current, func(c *Config) { c.MaxLeaverPercent = 100 })
	got := byExt(r)

	want := map[string]struct {
		kind   Kind
		status string
	}{
		"E2": {Mover, "active"},
		"E3": {Mover, "active"},
		"E4": {Rehire, "active"},
		"E5": {Leaver, "terminated"},
		"E6": {Leaver, "terminated"},
		"E8": {Update, "active"},
	}
	for ext, w := range want {
		if got[ext].Kind != w.kind || got[ext].Status != w.status {
			t.Errorf("%s = %s/%s, want %s/%s (%+v)", ext, got[ext].Kind, got[ext].Status, w.kind, w.status, got[ext])
		}
	}
	for _, ext := range []string{"E1", "E7", "E9"} {
		if c, ok := got[ext]; ok {
			t.Errorf("%s changed: %+v", ext, c)
		}
	}
	if r.Unchanged != 3 {
		t.Errorf("unchanged = %d, want 3", r.Unchanged)
	}
	if ch := got["E2"].Changes["department"]; ch.From != "Eng" || ch.To != "Sales" {
		t.Errorf("E2 changes = %v", got["E2"].Changes)
	}
	if ch := got["E3"].Changes[FieldManagerID]; ch.From != "E1" || ch.To != "E2" {
		t.Errorf("E3 changes = %v", got["E3"].Changes)
	}
	if got["E5"].Reason != ReasonEndDate || got["E6"].Reason != ReasonMissing {
		t.Errorf("leaver reasons = %q, %q", got["E5"].Reason, got["E6"].Reason)
	}
	if _, ok := got["E5"].Changes[FieldEmail]; !ok {
		t.Errorf("E5 email change missing: %v", got["E5"].Changes)
	}
	if r.Active != 5 {
		t.Errorf("active before = %d, want 5", r.Active)
	}
}

func TestPlanMissingIgnored(t *testing.T) {
	r := plan(t, header+"E1,Ana,ana@example.com,,Eng,,,\n",
		[]Current{staff("id-ana", "E1", "Ana", "active", "", "Eng"), staff("id-luis", "E2", "Luis", "active", "", "Eng")},
		func(c *Config) { c.Missing = "ignore" })
	if len(r.Changes) != 0 {
		t.Errorf("changes = %+v, want none", r.Changes)
	}
}

func TestPlanLinksUnownedIdentities(t *testing.T) {
	current := []Current{
		// The bootstrap admin: no source, no employee id.
		{ID: "id-admin", Kind: "human", Status: "active", DisplayName: "admin@example.com", Email: "Admin@Example.com", Attributes: map[string]string{}},
		// Created by hand with the employee id already set.
		{ID: "id-hand", ExternalID: "E2", Kind: "human", Status: "active", DisplayName: "Luis", Attributes: map[string]string{}},
		// Owned by another source.
		{ID: "id-other", SourceID: "source-2", ExternalID: "E3", Kind: "human", Status: "active", DisplayName: "Marta", Attributes: map[string]string{}},
		// A service account with a matching email is never taken over.
		{ID: "id-svc", Kind: "service", Status: "active", DisplayName: "bot", Email: "bot@example.com", Attributes: map[string]string{}},
	}
	feed := header +
		"E1,Ana Admin,admin@example.com,,IT,,,\n" +
		"E2,Luis,,,IT,,,\n" +
		"E4,Bot,bot@example.com,,IT,,,\n"
	r := plan(t, feed, current)
	got := byExt(r)
	if c := got["E1"]; c.IdentityID != "id-admin" || !c.Linked || c.Kind != Mover {
		t.Errorf("admin = %+v, want linked mover (department set)", c)
	}
	if c := got["E2"]; c.IdentityID != "id-hand" || !c.Linked {
		t.Errorf("hand-made = %+v, want linked", c)
	}
	if c := got["E4"]; c.IdentityID != "" || c.Kind != Joiner {
		t.Errorf("bot = %+v, want a new joiner", c)
	}

	r = plan(t, feed+"E3,Marta,,,IT,,,\n", current)
	if len(r.Errors) != 1 || !strings.Contains(r.Errors[0].Message, "another source") {
		t.Errorf("errors = %v, want the employee id of another source", r.Errors)
	}
}

func TestPlanSelfManager(t *testing.T) {
	r := plan(t, header+"E1,Ana,,E1,Eng,,,\n", nil)
	if len(r.Warnings) != 1 || r.Changes[0].record.ManagerExternalID != "" {
		t.Errorf("warnings = %v, record = %+v", r.Warnings, r.Changes[0].record)
	}
}

func TestTooManyLeavers(t *testing.T) {
	var current []Current
	var feed strings.Builder
	feed.WriteString(header)
	for i := range 50 {
		ext := "E" + string(rune('A'+i%26)) + string(rune('a'+i/26))
		current = append(current, staff("id-"+ext, ext, "P"+ext, "active", "", "Eng"))
		if i >= 6 { // 6 of 50 missing: 12%
			feed.WriteString(ext + ",P" + ext + ",p" + strings.ToLower(ext) + "@example.com,,Eng,,,\n")
		}
	}
	r := plan(t, feed.String(), current)
	if !r.TooManyLeavers(10) {
		t.Errorf("6 of 50 leavers passes a 10%% limit; counts %v", r.Counts())
	}
	if r.TooManyLeavers(15) {
		t.Error("6 of 50 leavers fails a 15% limit")
	}
	small := Result{Active: 4, Changes: []Change{{Kind: Leaver}, {Kind: Leaver}, {Kind: Leaver}}}
	if small.TooManyLeavers(10) {
		t.Error("three leavers must always pass")
	}
}
