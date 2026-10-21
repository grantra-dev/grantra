// Package workday reads people from Workday through a custom report shared
// as a web service (Reports as a Service, RaaS), in JSON. The report decides
// which fields Grantra sees, so it works with any Workday tenant: name its
// columns like Grantra's fields (employee_id, email, manager_id...) or map
// them in the source's column mapping.
package workday

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

// Kind is the source kind.
const Kind = "workday"

func init() { lifecycle.RegisterFeed(Kind, New) }

// Connection is how Grantra reaches the report.
type Connection struct {
	// ReportURL is the report's JSON web service address, e.g.
	// https://wd2-impl-services1.workday.com/ccx/service/customreport2/acme/ISU_Grantra/Grantra_Workers?format=json
	ReportURL string `json:"report_url"`
	// Username is the integration system user the report is shared with,
	// and Password a reference to its password.
	Username  string `json:"username"`
	Password  string `json:"password"`
	AllowHTTP bool   `json:"allow_http,omitempty"`
}

// Report reads one RaaS report.
type Report struct {
	api *httpapi.Client
	url string
}

// New builds a Workday feed from a source's connection settings.
func New(raw json.RawMessage, secrets connector.SecretResolver) (lifecycle.Feed, error) {
	var c Connection
	if err := connector.DecodeConfig(Kind, raw, &c); err != nil {
		return nil, err
	}
	// The report URL carries its format in the query, which BaseURL refuses.
	base, query, _ := strings.Cut(c.ReportURL, "?")
	u, err := httpapi.BaseURL(Kind, "report_url", base, c.AllowHTTP)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(u.Path, "/customreport2/") {
		return nil, errors.New("workday config: report_url must be a custom report's web service address (…/ccx/service/customreport2/…)")
	}
	if c.Username == "" {
		return nil, errors.New("workday config: username is required: the integration system user the report is shared with")
	}
	password, err := connector.Secret(Kind, "password", c.Password, "env:GRANTRA_SECRET_WORKDAY", secrets)
	if err != nil {
		return nil, err
	}
	api := httpapi.New(u, httpapi.Basic(c.Username, password))
	q := "format=json"
	for p := range strings.SplitSeq(query, "&") {
		if p != "" && !strings.HasPrefix(p, "format=") {
			q += "&" + p
		}
	}
	return &Report{api: api, url: u.String() + "?" + q}, nil
}

// workdayDate is how Workday writes a date with its time zone, such as
// 2026-10-01-07:00.
var workdayDate = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})[-+]\d{2}:\d{2}$`)

// Rows returns one row per report entry. Fields that hold several values
// (lists, nested objects) are left out: give the report single values.
func (r *Report) Rows(ctx context.Context) ([]map[string]string, error) {
	var report struct {
		Entries []map[string]any `json:"Report_Entry"`
	}
	if _, err := r.api.Get(ctx, r.url, &report); err != nil {
		return nil, err
	}
	rows := make([]map[string]string, 0, len(report.Entries))
	for _, e := range report.Entries {
		row := map[string]string{}
		for k, v := range e {
			text, ok := lifecycle.Text(v)
			if !ok {
				continue
			}
			if m := workdayDate.FindStringSubmatch(text); m != nil {
				text = m[1]
			}
			row[k] = text
		}
		rows = append(rows, row)
	}
	return rows, nil
}
