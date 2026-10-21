package bamboohr

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
)

func TestRows(t *testing.T) {
	srv := connectortest.New(t, func(r *http.Request) bool {
		u, _, ok := r.BasicAuth()
		return ok && u == "KEY"
	})
	srv.Handle("POST /api/gateway.php/acme/v1/reports/custom", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("format") != "JSON" || q.Get("onlyCurrent") != "false" {
			t.Errorf("query %v", q)
		}
		var body struct{ Fields []string }
		json.NewDecoder(r.Body).Decode(&body) //nolint:errcheck
		if !slices.Contains(body.Fields, "supervisorEId") || !slices.Contains(body.Fields, "terminationDate") {
			t.Errorf("fields %v", body.Fields)
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"employees": []map[string]any{
			{"id": "40", "employeeNumber": "E1", "firstName": "Ana", "lastName": "García", "workEmail": "ana@acme.com",
				"status": "Active", "hireDate": "2024-02-01", "terminationDate": "0000-00-00", "jobTitle": "CFO", "supervisorEId": nil},
			{"id": "41", "employeeNumber": "", "displayName": "Luis Pérez", "status": "Inactive", "terminationDate": "2026-10-01", "supervisorEId": "40"},
		}})
	})
	f, err := New(json.RawMessage(`{"company": "acme", "api_key": "env:KEY", "api_url": "`+srv.URL+`", "allow_http": true}`), connectortest.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.Rows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ana, luis := rows[0], rows[1]
	if ana["employee_id"] != "E1" || ana["email"] != "ana@acme.com" || ana["title"] != "CFO" || ana["start_date"] != "2024-02-01" ||
		ana["end_date"] != "" || ana["manager_id"] != "" {
		t.Errorf("ana = %v", ana)
	}
	// No employee number: BambooHR's id stands in. The manager is named by
	// their employee number.
	if luis["employee_id"] != "41" || luis["manager_id"] != "E1" || luis["status"] != "Inactive" || luis["display_name"] != "Luis Pérez" {
		t.Errorf("luis = %v", luis)
	}
}

func TestConfig(t *testing.T) {
	for _, raw := range []string{`{"api_key": "env:K"}`, `{"company": "acme.bamboohr.com", "api_key": "env:K"}`, `{"company": "acme"}`} {
		if _, err := New(json.RawMessage(raw), connectortest.Secrets); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}
