// Package bamboohr reads people from BambooHR through its API, with a custom
// report of the fields Grantra needs, former employees included so they
// become leavers.
package bamboohr

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/httpapi"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

// Kind is the source kind.
const Kind = "bamboohr"

func init() { lifecycle.RegisterFeed(Kind, New) }

// Connection is how Grantra reaches BambooHR.
type Connection struct {
	// Company is the company's subdomain: acme for acme.bamboohr.com.
	Company string `json:"company"`
	// APIKey is a reference to an API key of a user who can see every
	// employee's job information.
	APIKey string `json:"api_key"`
	// APIURL is for tests. Default https://api.bamboohr.com.
	APIURL    string `json:"api_url,omitempty"`
	AllowHTTP bool   `json:"allow_http,omitempty"`
}

// Client reads one BambooHR company.
type Client struct {
	api *httpapi.Client
}

var companyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// New builds a BambooHR feed from a source's connection settings.
func New(raw json.RawMessage, secrets connector.SecretResolver) (lifecycle.Feed, error) {
	var c Connection
	if err := connector.DecodeConfig(Kind, raw, &c); err != nil {
		return nil, err
	}
	if !companyPattern.MatchString(c.Company) {
		return nil, errors.New("bamboohr config: company must be your BambooHR subdomain, e.g. acme for acme.bamboohr.com")
	}
	key, err := connector.Secret(Kind, "api_key", c.APIKey, "env:GRANTRA_SECRET_BAMBOOHR", secrets)
	if err != nil {
		return nil, err
	}
	u, err := httpapi.BaseURL(Kind, "api_url", cmp.Or(c.APIURL, "https://api.bamboohr.com"), c.AllowHTTP)
	if err != nil {
		return nil, err
	}
	u.Path += "/api/gateway.php/" + url.PathEscape(c.Company) + "/v1"
	// BambooHR takes the API key as the user name, with any password.
	return &Client{api: httpapi.New(u, func(_ context.Context, r *http.Request) error {
		r.SetBasicAuth(key, "x")
		return nil
	})}, nil
}

// fields are what the report asks for, and the Grantra field each becomes.
var fields = map[string]string{
	"employeeNumber":  lifecycle.FieldEmployeeID,
	"firstName":       lifecycle.FieldFirstName,
	"lastName":        lifecycle.FieldLastName,
	"displayName":     lifecycle.FieldDisplayName,
	"workEmail":       lifecycle.FieldEmail,
	"department":      lifecycle.FieldDepartment,
	"jobTitle":        lifecycle.FieldTitle,
	"location":        lifecycle.FieldLocation,
	"status":          lifecycle.FieldStatus,
	"hireDate":        lifecycle.FieldStartDate,
	"terminationDate": lifecycle.FieldEndDate,
	"division":        "division",
}

// Rows returns every employee, current and former. The employee number is
// their employee id, or BambooHR's own id when they have none; managers are
// found by their BambooHR id and named by that same employee id.
func (c *Client) Rows(ctx context.Context) ([]map[string]string, error) {
	ask := []string{"id", "supervisorEId"}
	for f := range fields {
		ask = append(ask, f)
	}
	var report struct {
		Employees []map[string]any `json:"employees"`
	}
	if _, err := c.api.Do(ctx, http.MethodPost, "/reports/custom?format=JSON&onlyCurrent=false", map[string]any{
		"title": "Grantra", "fields": ask,
	}, &report); err != nil {
		return nil, err
	}
	str := func(e map[string]any, k string) string {
		switch v := e[k].(type) {
		case string:
			return strings.TrimSpace(v)
		case float64:
			return fmt.Sprint(v)
		}
		return ""
	}
	employeeID := map[string]string{} // BambooHR id -> employee id
	for _, e := range report.Employees {
		employeeID[str(e, "id")] = cmp.Or(str(e, "employeeNumber"), str(e, "id"))
	}
	rows := make([]map[string]string, 0, len(report.Employees))
	for _, e := range report.Employees {
		row := map[string]string{lifecycle.FieldEmployeeID: employeeID[str(e, "id")]}
		for from, to := range fields {
			if v := str(e, from); v != "" && v != "0000-00-00" && to != lifecycle.FieldEmployeeID {
				row[to] = v
			}
		}
		if m := str(e, "supervisorEId"); m != "" {
			row[lifecycle.FieldManagerID] = employeeID[m]
		}
		rows = append(rows, row)
	}
	return rows, nil
}
