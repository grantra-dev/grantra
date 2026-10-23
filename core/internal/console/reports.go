package console

import (
	"cmp"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/httpjson"
	"github.com/grantra-dev/grantra/core/internal/reports"
	"github.com/grantra-dev/grantra/core/internal/servicetoken"
)

// previewRows is how many rows of each table the console shows; downloads
// have them all.
const previewRows = 100

// listReports answers the reports with the period they open on.
func (a *API) listReports(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"reports": reports.List()}
	err := a.guardedOn(r, authz.ReadReports, authz.ReportsResource(), func(tx pgx.Tx, _ authz.Subject) error {
		from, to, err := reports.DefaultPeriod(r.Context(), tx, time.Now())
		out["default_from"], out["default_to"] = from.Format(time.DateOnly), to.Format(time.DateOnly)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}

// reportParams reads ?from=YYYY-MM-DD&to=YYYY-MM-DD&application=<id>. It
// answers 400 itself and returns false when a value is unusable.
func reportParams(w http.ResponseWriter, r *http.Request) (reports.Params, bool) {
	q := r.URL.Query()
	p := reports.Params{ApplicationID: q.Get("application")}
	for _, d := range []struct {
		name string
		to   *time.Time
	}{{"from", &p.From}, {"to", &p.To}} {
		if v := q.Get(d.name); v != "" {
			t, err := reports.Day(v)
			if err != nil {
				httpjson.Error(w, http.StatusBadRequest, d.name+": "+err.Error())
				return p, false
			}
			*d.to = t
		}
	}
	return p, true
}

// download is a file to send once the transaction that recorded it is
// committed.
type download struct {
	name, contentType string
	inline            bool
	data              []byte
}

func (d download) send(w http.ResponseWriter) {
	disposition := "attachment"
	if d.inline {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", d.contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=%q", disposition, d.name))
	w.Header().Set("Cache-Control", "no-store")
	if d.contentType == "text/html; charset=utf-8" {
		w.Header().Set("Content-Security-Policy", reports.SummaryCSP)
	}
	w.Write(d.data) //nolint:errcheck // the client went away
}

// packName is the file name, without extension, of a download of id for the
// period of p.
func packName(id string, p reports.Params) string {
	return fmt.Sprintf("grantra-%s-%s-%s", id, p.From.Format(time.DateOnly), p.To.Format(time.DateOnly))
}

// reportFormats are what ?format= takes on a report.
var reportFormats = []string{"json", "csv", "html", "zip"}

// getReport runs a report: ?format=json (the default) for the console, a
// preview of each table; csv with ?table= for one table; html for the
// printable summary; zip for the evidence pack. Downloads go to the audit
// log with their SHA-256. Scripts call it with a report.read token.
func (a *API) getReport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	def, ok := reports.Lookup(id)
	if !ok {
		httpjson.Error(w, http.StatusNotFound, "not found")
		return
	}
	p, ok := reportParams(w, r)
	if !ok {
		return
	}
	format := cmp.Or(r.URL.Query().Get("format"), "json")
	if !slices.Contains(reportFormats, format) {
		httpjson.Error(w, http.StatusBadRequest, "format must be json, csv, html or zip")
		return
	}
	table := r.URL.Query().Get("table")
	if format == "csv" && !slices.ContainsFunc(def.Tables, func(t reports.Table) bool { return t.Name == table }) {
		httpjson.Error(w, http.StatusBadRequest, "a CSV file is one table: give ?table= one of the report's tables")
		return
	}
	actor, err := a.caller(r, authz.ReadReports, authz.ReportsResource(), servicetoken.ScopeReadReports, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var (
		preview reports.Result
		file    download
	)
	err = db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) error {
		now := time.Now()
		if err := p.Check(r.Context(), tx, now); err != nil {
			return err
		}
		res, err := reports.Run(r.Context(), tx, id, p, now)
		if err != nil || format == "json" {
			preview = res.Preview(previewRows)
			return err
		}
		who, err := reports.WhoIs(r.Context(), tx, actor)
		if err != nil {
			return err
		}
		sum := reports.Summary{Title: def.Title, Params: res.Params, Application: res.Application, GeneratedAt: res.GeneratedAt,
			GeneratedBy: who, Results: []reports.Result{res}}
		name := packName(id, p)
		dl := reports.Download{Kind: "report", ID: id, Format: format, Params: p}
		switch format {
		case "csv":
			i := slices.IndexFunc(res.Tables, func(t reports.TableResult) bool { return t.Name == table })
			file = download{name: name + "-" + table + ".csv", contentType: "text/csv; charset=utf-8", data: reports.CSV(res.Tables[i])}
			dl.Table = table
		case "html":
			file = download{name: name + ".html", contentType: "text/html; charset=utf-8", inline: true, data: reports.HTML(sum)}
		case "zip":
			file = download{name: name + ".zip", contentType: "application/zip"}
			file.data, dl.SHA256, err = reports.Pack(sum, a.tenantID)
			if err != nil {
				return err
			}
			dl.Files = len(res.Tables) + 2
		}
		if dl.SHA256 == "" {
			dl.SHA256 = reports.SHA256(file.data)
		}
		return reports.Record(r.Context(), tx, a.tenantID, actor, dl)
	})
	if err != nil || format == "json" {
		a.respond(w, r, http.StatusOK, preview, err)
		return
	}
	file.send(w)
}

func (a *API) listReportFrameworks(w http.ResponseWriter, r *http.Request) {
	var out []reports.Framework
	err := a.guardedOn(r, authz.ReadReports, authz.ReportsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = reports.Frameworks(r.Context(), tx)
		return err
	})
	a.respond(w, r, http.StatusOK, map[string]any{"frameworks": httpjson.NonNil(out)}, err)
}

// saveReportFramework adds a framework (POST) or changes one (PUT):
// {"name": "DORA", "enabled": true, "controls": [{"id": "Art. 9", "title":
// "...", "reports": ["access"]}]}. A built-in framework keeps its name.
func (a *API) saveReportFramework(w http.ResponseWriter, r *http.Request) {
	var in reports.FrameworkInput
	if !decodeBody(w, r, &in, `{"name": "DORA", "enabled": true, "controls": [{"id": "Art. 9", "title": "Protection", "reports": ["access"]}]}`) {
		return
	}
	id, status := r.PathValue("id"), http.StatusOK
	if id == "" {
		status = http.StatusCreated
	}
	var out reports.Framework
	err := a.guardedOn(r, authz.ManageReports, authz.ReportsResource(), func(tx pgx.Tx, me authz.Subject) (err error) {
		out, err = reports.SaveFramework(r.Context(), tx, a.tenantID, me.Actor(), id, in)
		return err
	})
	a.respond(w, r, status, out, err)
}

// deleteReportFramework removes a framework the tenant added, or brings a
// built-in one back to its defaults.
func (a *API) deleteReportFramework(w http.ResponseWriter, r *http.Request) {
	err := a.guardedOn(r, authz.ManageReports, authz.ReportsResource(), func(tx pgx.Tx, me authz.Subject) error {
		return reports.DeleteFramework(r.Context(), tx, a.tenantID, me.Actor(), r.PathValue("id"))
	})
	a.noContent(w, r, err)
}

// frameworkPack is the evidence pack of a framework for a period: every
// report its controls point to, the controls, a summary and the manifest.
func (a *API) frameworkPack(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	p, ok := reportParams(w, r)
	if !ok {
		return
	}
	if p.ApplicationID != "" {
		httpjson.Error(w, http.StatusBadRequest, "a framework's pack covers every application")
		return
	}
	actor, err := a.caller(r, authz.ReadReports, authz.ReportsResource(), servicetoken.ScopeReadReports, id)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	var file download
	err = db.InTenant(r.Context(), a.pool, a.tenantID, func(tx pgx.Tx) error {
		now := time.Now()
		if err := p.Check(r.Context(), tx, now); err != nil {
			return err
		}
		fw, err := reports.GetFramework(r.Context(), tx, id)
		if err != nil {
			return err
		}
		if len(fw.Controls) == 0 {
			return apperr.Invalidf("%s has no controls yet", fw.Name)
		}
		who, err := reports.WhoIs(r.Context(), tx, actor)
		if err != nil {
			return err
		}
		sum := reports.Summary{Title: fw.Name, Framework: &fw, Params: p, GeneratedAt: now, GeneratedBy: who}
		files := 3 // controls, summary and manifest
		for _, c := range fw.Controls {
			for _, rid := range c.Reports {
				if slices.ContainsFunc(sum.Results, func(res reports.Result) bool { return res.Report.ID == rid }) {
					continue
				}
				res, err := reports.Run(r.Context(), tx, rid, p, now)
				if err != nil {
					return err
				}
				sum.Results = append(sum.Results, res)
				files += len(res.Tables)
			}
		}
		file = download{name: packName(id, p) + ".zip", contentType: "application/zip"}
		var sha string
		if file.data, sha, err = reports.Pack(sum, a.tenantID); err != nil {
			return err
		}
		return reports.Record(r.Context(), tx, a.tenantID, actor,
			reports.Download{Kind: "report_framework", ID: id, Format: "zip", Params: p, SHA256: sha, Files: files})
	})
	if err != nil {
		a.fail(w, r, err)
		return
	}
	file.send(w)
}

// verifyReport checks a downloaded evidence pack or report, sent as the
// body, against its manifest and the audit log.
func (a *API) verifyReport(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, reports.MaxVerifySize))
	if err != nil {
		httpjson.Error(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("send a file of at most %d MB", reports.MaxVerifySize>>20))
		return
	}
	if len(data) == 0 {
		httpjson.Error(w, http.StatusBadRequest, "send the file to check as the body")
		return
	}
	var out reports.Verification
	err = a.guardedOn(r, authz.ReadReports, authz.ReportsResource(), func(tx pgx.Tx, _ authz.Subject) (err error) {
		out, err = reports.Verify(r.Context(), tx, data)
		return err
	})
	a.respond(w, r, http.StatusOK, out, err)
}
