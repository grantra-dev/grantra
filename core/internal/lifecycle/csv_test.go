package lifecycle

import (
	"os"
	"strings"
	"testing"
)

func TestParseCSV(t *testing.T) {
	feed := "\xef\xbb\xbfEmployee ID,First Name,Last Name,Email,Manager ID,Department,Title,Status,Start Date,Cost Center\n" +
		"E1,Ana,García,ana@example.com,,Engineering,CTO,active,2020-01-01,CC-1\n" +
		"E2,Luis,Pérez,luis@example.com,E1,Engineering,Developer,,2026-11-01,\n" +
		"\n" +
		"E3,Marta,Ruiz,,E1,Sales,,Terminated,,\n"
	cfg := Config{}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	recs, issues, err := ParseCSV(strings.NewReader(feed), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
	if len(recs) != 3 {
		t.Fatalf("records = %d, want 3", len(recs))
	}
	ana, luis, marta := recs[0], recs[1], recs[2]
	if ana.ExternalID != "E1" || ana.DisplayName != "Ana García" || ana.Email != "ana@example.com" || ana.Line != 2 {
		t.Errorf("ana = %+v", ana)
	}
	if ana.Attributes["department"] != "Engineering" || ana.Attributes["title"] != "CTO" || ana.Attributes["cost_center"] != "CC-1" {
		t.Errorf("ana attributes = %v", ana.Attributes)
	}
	if luis.ManagerExternalID != "E1" || luis.StartDate == nil || luis.StartDate.Format("2006-01-02") != "2026-11-01" {
		t.Errorf("luis = %+v", luis)
	}
	if _, ok := luis.Attributes["cost_center"]; ok {
		t.Errorf("empty values must not become attributes: %v", luis.Attributes)
	}
	if !marta.Terminated || marta.Line != 5 {
		t.Errorf("marta = %+v", marta)
	}
}

func TestParseCSVColumnMapping(t *testing.T) {
	feed := "Worker,Full name,Dept,department\nW1,Ana,Eng,ignored\n"
	cfg := Config{Columns: map[string]string{FieldEmployeeID: "Worker", FieldDisplayName: "Full name", FieldDepartment: "Dept"}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	recs, issues, err := ParseCSV(strings.NewReader(feed), cfg)
	if err != nil || len(issues) != 0 {
		t.Fatal(err, issues)
	}
	if recs[0].ExternalID != "W1" || recs[0].DisplayName != "Ana" || recs[0].Attributes["department"] != "Eng" {
		t.Errorf("record = %+v", recs[0])
	}
}

func TestParseCSVProblems(t *testing.T) {
	cfg := Config{}
	cfg.Validate() //nolint:errcheck

	for name, feed := range map[string]string{
		"empty":            "",
		"no employee id":   "name,email\nAna,ana@example.com\n",
		"no name":          "employee_id,email\nE1,ana@example.com\n",
		"duplicate column": "employee_id,display_name,Display Name\nE1,Ana,Ana\n",
		"ragged":           "employee_id,display_name\nE1,Ana,extra\n",
	} {
		if _, _, err := ParseCSV(strings.NewReader(feed), cfg); err == nil {
			t.Errorf("%s: no error", name)
		}
	}

	feed := "employee_id,display_name,email,status,start_date,end_date\n" +
		",Nobody,,,,\n" +
		"E1,Ana,not-an-email,,,\n" +
		"E1,Ana again,,,,\n" +
		"E2,,,,,\n" +
		"E3,Luis,,on holiday,,\n" +
		"E4,Marta,,,31/10/2026,\n" +
		"E5,Pedro,,,2026-10-01,2026-09-01\n"
	recs, issues, err := ParseCSV(strings.NewReader(feed), cfg)
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []int{2, 3, 4, 5, 6, 7, 8}
	if len(issues) != len(wantLines) {
		t.Fatalf("issues = %v, want one per line %v", issues, wantLines)
	}
	for i, l := range wantLines {
		if issues[i].Line != l {
			t.Errorf("issue %d on line %d, want %d: %s", i, issues[i].Line, l, issues[i])
		}
	}
	// Lines with problems still yield records (except unusable ones); the
	// sync refuses to apply while there are issues.
	if len(recs) == 0 {
		t.Error("no records")
	}
}

func TestConfigValidate(t *testing.T) {
	c := Config{}
	if err := c.Validate(); err != nil || c.Missing != "leave" || c.MaxLeaverPercent != 10 {
		t.Errorf("defaults = %+v, %v", c, err)
	}
	for _, bad := range []Config{
		{Columns: map[string]string{"salary": "Salary"}},
		{Columns: map[string]string{FieldEmail: " "}},
		{Missing: "delete"},
		{MaxLeaverPercent: 101},
		{MaxLeaverPercent: -1},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v: no error", bad)
		}
	}
}

// The sample in deploy/samples must stay a valid feed.
func TestSampleFile(t *testing.T) {
	f, err := os.Open("../../../deploy/samples/hr-employees.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg := Config{}
	cfg.Validate() //nolint:errcheck
	recs, issues, err := ParseCSV(f, cfg)
	if err != nil || len(issues) > 0 || len(recs) != 6 {
		t.Fatalf("sample: %d records, issues %v, err %v", len(recs), issues, err)
	}
	r := Plan("s", cfg, recs, nil, today)
	if len(r.Warnings) > 0 || len(r.Errors) > 0 {
		t.Errorf("sample plan: warnings %v, errors %v", r.Warnings, r.Errors)
	}
}
