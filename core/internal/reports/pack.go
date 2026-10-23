package reports

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/csvfile"
	"github.com/grantra-dev/grantra/core/internal/db"
)

// PackFormat is the version of the evidence pack layout, in its manifest.
const PackFormat = 1

// summaryRows is the most rows of a table the printable summary shows; the
// CSV files have them all.
const summaryRows = 500

// MaxVerifySize is the largest file Verify reads.
const MaxVerifySize = 200 << 20

// Who is who downloads a report, as the audit log and the pack name them.
type Who struct {
	Actor string `json:"actor"`
	Name  string `json:"name,omitempty"`
}

// PackFile is one file of an evidence pack.
type PackFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
	Rows   int    `json:"rows,omitempty"`
}

// Manifest describes an evidence pack: what it covers, who made it, and
// the hash of every other file in it. Its own SHA-256 is in the audit log.
type Manifest struct {
	Format      int        `json:"grantra_evidence_pack"`
	Title       string     `json:"title"`
	Report      string     `json:"report,omitempty"`
	Framework   string     `json:"framework,omitempty"`
	From        string     `json:"from"`
	To          string     `json:"to"`
	Application string     `json:"application,omitempty"`
	Tenant      string     `json:"tenant"`
	GeneratedAt time.Time  `json:"generated_at"`
	GeneratedBy Who        `json:"generated_by"`
	TimeZone    string     `json:"time_zone"`
	Files       []PackFile `json:"files"`
}

// CSV is one table of a result as a CSV file.
func CSV(t TableResult) []byte {
	var b bytes.Buffer
	header := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		header[i] = c.Title
	}
	csvfile.Write(&b, header, t.Rows) //nolint:errcheck // a bytes.Buffer does not fail
	return b.Bytes()
}

//go:embed summary.html
var summaryFS embed.FS

// printScript is the summary's only script: its print button.
const printScript = `document.querySelector(".print").addEventListener("click", () => window.print());`

// SummaryCSP is the Content-Security-Policy to serve the summary with: no
// requests anywhere, its own styles, and only its print button's script.
var SummaryCSP = func() string {
	sum := sha256.Sum256([]byte(printScript))
	return "default-src 'none'; style-src 'unsafe-inline'; script-src 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) +
		"'; frame-ancestors 'none'"
}()

var summaryTemplate = template.Must(template.New("summary.html").Funcs(template.FuncMap{
	"head":        func(rows [][]string) [][]string { return rows[:min(len(rows), summaryRows)] },
	"more":        func(rows [][]string) bool { return len(rows) > summaryRows },
	"printScript": func() template.JS { return printScript },
	"day":         func(t time.Time) string { return t.Format(time.DateOnly) },
	"when":        func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") },
	"csv":         func(report, table string) string { return path.Join(report, table+".csv") },
	"report": func(id string) string {
		d, _ := Lookup(id)
		return d.Title
	},
}).ParseFS(summaryFS, "summary.html"))

// Summary is what the printable summary shows.
type Summary struct {
	Title     string
	Framework *Framework
	Params    Params
	// Application is the application the reports were narrowed to.
	Application string
	GeneratedAt time.Time
	GeneratedBy Who
	Results     []Result
	// InPack says the summary goes in a pack, next to the CSV files.
	InPack bool
}

// HTML is the printable summary of results.
func HTML(s Summary) []byte {
	var b bytes.Buffer
	if err := summaryTemplate.Execute(&b, s); err != nil {
		// The template is fixed and its data has no methods that fail.
		panic(err)
	}
	return b.Bytes()
}

// Pack zips the results as an evidence pack: a CSV file for each table, the
// printable summary and the manifest. It returns the pack and the manifest's
// SHA-256, which Record writes to the audit log.
func Pack(s Summary, tenantID string) (zipped []byte, manifestSHA string, err error) {
	s.InPack = true
	m := Manifest{Format: PackFormat, Title: s.Title, From: s.Params.From.Format(time.DateOnly), To: s.Params.To.Format(time.DateOnly),
		Application: s.Application, Tenant: tenantID, GeneratedAt: s.GeneratedAt.UTC(), GeneratedBy: s.GeneratedBy, TimeZone: "UTC"}
	if s.Framework != nil {
		m.Framework = s.Framework.ID
	} else if len(s.Results) == 1 {
		m.Report = s.Results[0].Report.ID
	}
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	write := func(name string, data []byte) error {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: m.GeneratedAt})
		if err == nil {
			_, err = w.Write(data)
		}
		return err
	}
	add := func(name string, data []byte, rows int) error {
		m.Files = append(m.Files, PackFile{Path: name, SHA256: SHA256(data), Bytes: len(data), Rows: rows})
		return write(name, data)
	}
	if s.Framework != nil {
		rows := make([][]string, 0, len(s.Framework.Controls))
		for _, c := range s.Framework.Controls {
			titles := make([]string, len(c.Reports))
			for i, r := range c.Reports {
				d, _ := Lookup(r)
				titles[i] = d.Title
			}
			rows = append(rows, []string{c.ID, c.Title, strings.Join(titles, "; "), strings.Join(c.Reports, "; ")})
		}
		var cb bytes.Buffer
		csvfile.Write(&cb, []string{"Control", "Title", "Reports", "Folders"}, rows) //nolint:errcheck // a bytes.Buffer does not fail
		if err := add("controls.csv", cb.Bytes(), len(rows)); err != nil {
			return nil, "", err
		}
	}
	for _, r := range s.Results {
		for _, t := range r.Tables {
			if err := add(path.Join(r.Report.ID, t.Name+".csv"), CSV(t), len(t.Rows)); err != nil {
				return nil, "", err
			}
		}
	}
	if err := add("summary.html", HTML(s), 0); err != nil {
		return nil, "", err
	}
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, "", err
	}
	if err := write("manifest.json", manifest); err != nil {
		return nil, "", err
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return b.Bytes(), SHA256(manifest), nil
}

// SHA256 is the hex SHA-256 of data.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Download is one report or pack someone downloaded, for the audit log.
type Download struct {
	// Kind is "report" or "framework", and ID the one downloaded.
	Kind, ID string
	// Format is csv, html or zip; Table the table of a CSV file.
	Format, Table string
	Params        Params
	// SHA256 is the hash of the file, or of the manifest for a pack.
	SHA256 string
	Files  int
}

// Record writes a download to the audit log with its hash, which Verify
// looks up.
func Record(ctx context.Context, tx pgx.Tx, tenantID, actor string, d Download) error {
	details := map[string]any{"format": d.Format, "sha256": d.SHA256,
		"from": d.Params.From.Format(time.DateOnly), "to": d.Params.To.Format(time.DateOnly)}
	if d.Table != "" {
		details["table"] = d.Table
	}
	if d.Params.ApplicationID != "" {
		details["application_id"] = d.Params.ApplicationID
	}
	if d.Files > 0 {
		details["files"] = d.Files
	}
	return db.Audit(ctx, tx, tenantID, actor, "report.generated", d.Kind, d.ID, details)
}

// Verification says whether a file came out of Grantra unchanged.
type Verification struct {
	// Valid means every file matches the manifest and Grantra recorded
	// handing out the manifest (or the file, when it is not a pack).
	Valid       bool      `json:"valid"`
	Pack        bool      `json:"pack"`
	Manifest    *Manifest `json:"manifest,omitempty"`
	SHA256      string    `json:"sha256"`
	GeneratedAt time.Time `json:"generated_at,omitzero"`
	GeneratedBy string    `json:"generated_by,omitempty"`
	Problems    []string  `json:"problems"`
}

// Verify checks a downloaded file: a pack against its manifest and the
// manifest against the audit log, or a single file against the audit log.
func Verify(ctx context.Context, tx pgx.Tx, data []byte) (Verification, error) {
	v := Verification{Problems: []string{}}
	signed := data
	if zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data))); err == nil {
		v.Pack = true
		manifest, err := v.checkPack(zr)
		if err != nil {
			return v, err
		}
		signed = manifest
	}
	v.SHA256 = SHA256(signed)
	err := tx.QueryRow(ctx,
		`SELECT occurred_at, `+actor("actor")+` FROM audit_events
		 WHERE action = 'report.generated' AND details->>'sha256' = $1 ORDER BY id LIMIT 1`, v.SHA256).Scan(&v.GeneratedAt, &v.GeneratedBy)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		v.Problems = append(v.Problems, "Grantra has no record of handing out this file: it was changed, or it comes from another Grantra")
	case err != nil:
		return v, err
	}
	v.Valid = len(v.Problems) == 0
	return v, nil
}

// checkPack checks every file of a pack against its manifest, and returns
// the manifest as it is in the pack.
func (v *Verification) checkPack(zr *zip.Reader) ([]byte, error) {
	read := func(f *zip.File) ([]byte, error) {
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer rc.Close()
		// Pack files are no bigger than the pack, which was size checked;
		// this stops a zip bomb.
		return io.ReadAll(io.LimitReader(rc, MaxVerifySize))
	}
	i := slices.IndexFunc(zr.File, func(f *zip.File) bool { return f.Name == "manifest.json" })
	if i < 0 {
		return nil, apperr.Invalidf("this zip has no manifest.json, so it is not a Grantra evidence pack")
	}
	raw, err := read(zr.File[i])
	if err != nil {
		return nil, apperr.Invalidf("the manifest cannot be read: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil || m.Format == 0 {
		return nil, apperr.Invalidf("manifest.json is not a Grantra evidence pack manifest")
	}
	v.Manifest = &m
	listed := map[string]bool{"manifest.json": true}
	for _, pf := range m.Files {
		listed[pf.Path] = true
		j := slices.IndexFunc(zr.File, func(f *zip.File) bool { return f.Name == pf.Path })
		if j < 0 {
			v.Problems = append(v.Problems, pf.Path+" is missing")
			continue
		}
		data, err := read(zr.File[j])
		if err != nil {
			v.Problems = append(v.Problems, pf.Path+" cannot be read")
			continue
		}
		if SHA256(data) != pf.SHA256 {
			v.Problems = append(v.Problems, pf.Path+" was changed")
		}
	}
	for _, f := range zr.File {
		if !listed[f.Name] {
			v.Problems = append(v.Problems, f.Name+" was added")
		}
	}
	return raw, nil
}
