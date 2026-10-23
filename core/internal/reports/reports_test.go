package reports

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
)

func TestDefinitions(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range List() {
		if seen[d.ID] || d.Title == "" || d.Description == "" || len(d.Tables) == 0 {
			t.Errorf("report %q is a duplicate or misses a title, description or table", d.ID)
		}
		seen[d.ID] = true
		tables := map[string]Table{}
		for _, tb := range d.Tables {
			if tb.Name == "" || tb.Title == "" || len(tb.Columns) == 0 || tb.sql == "" {
				t.Errorf("%s: table %q is incomplete", d.ID, tb.Name)
			}
			keys := map[string]bool{}
			for _, c := range tb.Columns {
				if keys[c.Key] || c.Title == "" {
					t.Errorf("%s/%s: column %q is a duplicate or has no title", d.ID, tb.Name, c.Key)
				}
				keys[c.Key] = true
			}
			tables[tb.Name] = tb
		}
		for _, f := range d.facts {
			tb, ok := tables[f.table]
			if !ok || (f.column != "" && !slices.ContainsFunc(tb.Columns, func(c Column) bool { return c.Key == f.column })) {
				t.Errorf("%s: fact %q counts a column that is not there", d.ID, f.label)
			}
		}
	}
	// Every built-in control points to reports that exist.
	for _, f := range builtins {
		in := FrameworkInput{Name: f.Name, Controls: slices.Clone(f.Controls)}
		if err := in.validate(); err != nil {
			t.Errorf("%s: %v", f.ID, err)
		}
	}
}

func TestLastPeriod(t *testing.T) {
	day := func(s string) time.Time { tm, _ := time.Parse(time.DateOnly, s); return tm }
	for _, tc := range []struct {
		now              string
		months, start    int
		wantFrom, wantTo string
	}{
		{"2026-10-23", 3, 1, "2026-07-01", "2026-09-30"},
		{"2026-10-01", 3, 1, "2026-07-01", "2026-09-30"},
		{"2026-09-30", 3, 1, "2026-04-01", "2026-06-30"},
		{"2026-10-23", 3, 4, "2026-07-01", "2026-09-30"},
		{"2026-02-10", 3, 4, "2025-10-01", "2025-12-31"},
		{"2026-10-23", 12, 1, "2025-01-01", "2025-12-31"},
		{"2026-10-23", 12, 7, "2025-07-01", "2026-06-30"},
		{"2026-01-15", 1, 1, "2025-12-01", "2025-12-31"},
		{"2026-03-15", 6, 1, "2025-07-01", "2025-12-31"},
	} {
		from, to := LastPeriod(day(tc.now), tc.months, tc.start)
		if from.Format(time.DateOnly) != tc.wantFrom || to.Format(time.DateOnly) != tc.wantTo {
			t.Errorf("LastPeriod(%s, %d, %d) = %s to %s, want %s to %s", tc.now, tc.months, tc.start,
				from.Format(time.DateOnly), to.Format(time.DateOnly), tc.wantFrom, tc.wantTo)
		}
	}
}

func TestPack(t *testing.T) {
	def, _ := Lookup(Leavers)
	res := Result{Report: def, Params: Params{From: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)}}
	for _, tb := range def.Tables {
		rows := [][]string{}
		for range 600 {
			row := make([]string, len(tb.Columns))
			row[0] = "<script>alert(1)</script>"
			rows = append(rows, row)
		}
		res.Tables = append(res.Tables, TableResult{Table: tb, Rows: rows, RowCount: len(rows)})
	}
	res.Facts = res.count(def.facts)
	if res.Facts[0].Value != 600 {
		t.Errorf("facts = %v", res.Facts)
	}
	fw, _ := builtin("sox")
	sum := Summary{Title: "SOX", Framework: &fw, Params: res.Params, GeneratedAt: time.Now(), GeneratedBy: Who{Actor: "identity:x", Name: "Ana"}, Results: []Result{res}}

	page := string(HTML(sum))
	switch {
	case strings.Contains(page, "<script>alert"):
		t.Error("the summary does not escape values")
	case strings.Count(page, "&lt;script&gt;alert(1)") != 2*summaryRows:
		t.Errorf("the summary shows %d rows", strings.Count(page, "&lt;script&gt;alert(1)"))
	case !strings.Contains(page, "first 500 of 600 rows"):
		t.Error("the summary does not say it shows only some rows")
	case !strings.Contains(page, "ITGC-AC-02") || !strings.Contains(page, "Leavers and orphan accounts"):
		t.Error("the summary misses the controls")
	}
	// The print button's script is exactly what the CSP allows.
	sha := sha256.Sum256([]byte(printScript))
	if !strings.Contains(page, "<script>"+printScript+"</script>") || !strings.Contains(SummaryCSP, base64.StdEncoding.EncodeToString(sha[:])) {
		t.Error("the print script does not match the CSP")
	}

	data, manifestSHA, err := Pack(sum, "tenant")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	files := map[string][]byte{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		names = append(names, f.Name)
		files[f.Name] = b
	}
	if want := []string{"controls.csv", "leavers/leavers.csv", "leavers/orphans.csv", "summary.html", "manifest.json"}; !slices.Equal(names, want) {
		t.Errorf("pack = %v, want %v", names, want)
	}
	if SHA256(files["manifest.json"]) != manifestSHA {
		t.Error("the returned hash is not the manifest's")
	}
	var m Manifest
	if err := json.Unmarshal(files["manifest.json"], &m); err != nil {
		t.Fatal(err)
	}
	if m.Format != PackFormat || m.Framework != "sox" || m.From != "2026-07-01" || m.To != "2026-09-30" || len(m.Files) != 4 || m.Files[1].Rows != 600 {
		t.Errorf("manifest = %+v", m)
	}
	for _, f := range m.Files {
		if SHA256(files[f.Path]) != f.SHA256 || len(files[f.Path]) != f.Bytes {
			t.Errorf("manifest entry %s does not match the file", f.Path)
		}
	}
	if !strings.Contains(string(files["controls.csv"]), "ITGC-AC-04,Privileged access is restricted and monitored,Privileged access; Administration changes,privileged; admin") {
		t.Errorf("controls.csv = %s", files["controls.csv"])
	}
	// Formula-looking cells stay text in the CSV files.
	if !strings.Contains(string(files["leavers/leavers.csv"]), "<script>") {
		t.Errorf("leavers.csv = %.200s", files["leavers/leavers.csv"])
	}

	var v Verification
	if _, err := v.checkPack(zr); err != nil || len(v.Problems) != 0 {
		t.Errorf("check own pack = %v %v", err, v.Problems)
	}
}

// Every report's queries run against the schema, on a tenant with nothing
// in it, for any period and application.
func TestRunEmpty(t *testing.T) {
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "reports")
	ctx := context.Background()
	err := db.InTenant(ctx, pool, tenant, func(tx pgx.Tx) error {
		var app string
		if err := tx.QueryRow(ctx, `INSERT INTO applications (tenant_id, name, connector) VALUES ($1, 'ERP', 'csv') RETURNING id`, tenant).Scan(&app); err != nil {
			return err
		}
		for _, d := range List() {
			for _, appID := range []string{"", app} {
				if appID != "" && !d.Application {
					continue
				}
				p := Params{ApplicationID: appID}
				if err := p.Check(ctx, tx, time.Now()); err != nil {
					return err
				}
				res, err := Run(ctx, tx, d.ID, p, time.Now())
				if err != nil {
					t.Errorf("%s (application %q): %v", d.ID, appID, err)
					continue
				}
				if len(res.Tables) != len(d.Tables) || len(res.Facts) != len(d.facts) {
					t.Errorf("%s = %d tables and %d facts", d.ID, len(res.Tables), len(res.Facts))
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
