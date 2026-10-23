package csvfile

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"Employee ID":      "employee_id",
		" employee_id ":    "employee_id",
		"Año de ingreso":   "año_de_ingreso",
		"Cost-Center (EU)": "cost_center_eu",
		"!!!":              "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// read collects every row of a file as field -> value, with the extra
// columns under "+name".
func read(t *testing.T, f *File, fields []string) []map[string]string {
	t.Helper()
	var out []map[string]string
	for {
		line, err := f.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		row := map[string]string{"line": string(rune('0' + line))}
		for _, field := range fields {
			if v := f.Get(field); v != "" {
				row[field] = v
			}
		}
		f.Extra(func(name, v string) { row["+"+name] = v })
		out = append(out, row)
	}
}

func TestFile(t *testing.T) {
	fields := []string{"id", "name", "dept"}
	in := "\xef\xbb\xbfWorker, Name ,dept,Cost Center,\n" +
		"W1, Ana ,Eng,CC-1,\n" +
		" , , , ,\n" +
		"\n" +
		"W2,Luis,,,\n"
	f, err := Open(strings.NewReader(in), fields, map[string]string{"id": "Worker"})
	if err != nil {
		t.Fatal(err)
	}
	if !f.Has("id") || !f.Has("name") || !f.Has("dept") || f.Column("id") != "Worker" || f.Column("name") != "name" {
		t.Fatalf("columns: %+v", f.col)
	}
	rows := read(t, f, fields)
	want := []map[string]string{
		{"line": "2", "id": "W1", "name": "Ana", "dept": "Eng", "+cost_center": "CC-1"},
		{"line": "5", "id": "W2", "name": "Luis"},
	}
	if len(rows) != len(want) {
		t.Fatalf("rows = %v", rows)
	}
	for i := range want {
		if len(rows[i]) != len(want[i]) {
			t.Errorf("row %d = %v, want %v", i, rows[i], want[i])
		}
		for k, v := range want[i] {
			if rows[i][k] != v {
				t.Errorf("row %d %s = %q, want %q", i, k, rows[i][k], v)
			}
		}
	}
}

// A column named like a field that the mapping moved elsewhere is neither
// that field nor an extra attribute.
func TestFileMappedFieldShadow(t *testing.T) {
	f, err := Open(strings.NewReader("Org Unit,dept\nEng,ignored\n"), []string{"dept"}, map[string]string{"dept": "Org Unit"})
	if err != nil {
		t.Fatal(err)
	}
	rows := read(t, f, []string{"dept"})
	if len(rows) != 1 || rows[0]["dept"] != "Eng" || len(rows[0]) != 2 {
		t.Errorf("rows = %v", rows)
	}
}

func TestFileProblems(t *testing.T) {
	for name, in := range map[string]string{
		"empty":            "",
		"only a BOM":       "\xef\xbb\xbf",
		"duplicate column": "id,Name,name\n",
		"bad header":       "\"id,name\n",
	} {
		if _, err := Open(strings.NewReader(in), []string{"id"}, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	f, err := Open(strings.NewReader("id,name\nA,Ana\nB,Bob,extra\nC,Cal\n"), []string{"id", "name"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); err == nil || !strings.HasPrefix(err.Error(), "line 3:") {
		t.Errorf("ragged row: err = %v", err)
	}
}

func TestValidateMapping(t *testing.T) {
	fields := []string{"id", "name"}
	if err := ValidateMapping(map[string]string{"id": "Worker ID"}, fields); err != nil {
		t.Error(err)
	}
	for _, bad := range []map[string]string{{"nope": "x"}, {"id": " "}, {"id": ""}} {
		if err := ValidateMapping(bad, fields); err == nil {
			t.Errorf("%v: no error", bad)
		}
	}
}

func TestWrite(t *testing.T) {
	var b strings.Builder
	err := Write(&b, []string{"Name", "Note"}, [][]string{
		{"Ana", "fine, with a comma"},
		{"=HYPERLINK(\"http://evil\")", "+1"},
		{"-2", "@sum"},
		{"", ""},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "\xef\xbb\xbfName,Note\nAna,\"fine, with a comma\"\n\"'=HYPERLINK(\"\"http://evil\"\")\",'+1\n'-2,'@sum\n,\n"
	if b.String() != want {
		t.Errorf("Write =\n%q\nwant\n%q", b.String(), want)
	}
	// What Grantra writes, it reads back.
	f, err := Open(strings.NewReader(b.String()), []string{"name"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Next(); err != nil || f.Get("name") != "Ana" {
		t.Errorf("read back = %q, %v", f.Get("name"), err)
	}
}
