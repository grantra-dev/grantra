package workday

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
)

func TestRows(t *testing.T) {
	srv := connectortest.New(t, func(r *http.Request) bool {
		u, p, ok := r.BasicAuth()
		return ok && u == "ISU_Grantra" && p == "SECRET"
	})
	srv.Handle("GET /ccx/service/customreport2/acme/ISU_Grantra/Grantra_Workers", func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("format") != "json" || q.Get("Effective_as_of") != "today" {
			t.Errorf("query %v", q)
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"Report_Entry": []map[string]any{
			{"employee_id": "21001", "display_name": "Ana García", "email": "ana@acme.com", "start_date": "2024-02-01-08:00",
				"FTE": 1, "Is_Manager": true, "Cost_Centers": []map[string]string{{"ID": "CC1"}}},
			{"employee_id": "21002", "display_name": "Luis", "manager_id": "21001", "status": "Terminated", "end_date": "2026-10-01"},
		}})
	})
	cfg, _ := json.Marshal(Connection{ReportURL: srv.URL + "/ccx/service/customreport2/acme/ISU_Grantra/Grantra_Workers?format=csv&Effective_as_of=today",
		Username: "ISU_Grantra", Password: "env:SECRET", AllowHTTP: true})
	f, err := New(cfg, connectortest.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := f.Rows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ana, luis := rows[0], rows[1]
	if ana["start_date"] != "2024-02-01" || ana["FTE"] != "1" || ana["Is_Manager"] != "true" || ana["Cost_Centers"] != "" {
		t.Errorf("ana = %v", ana)
	}
	if luis["manager_id"] != "21001" || luis["status"] != "Terminated" || luis["end_date"] != "2026-10-01" {
		t.Errorf("luis = %v", luis)
	}
	cfg, _ = json.Marshal(Connection{ReportURL: srv.URL + "/ccx/service/customreport2/x?format=json", Username: "ISU_Grantra", Password: "env:WRONG", AllowHTTP: true})
	if f, err = New(cfg, connectortest.Secrets); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Rows(context.Background()); err == nil {
		t.Error("read with a wrong password")
	}
}

func TestConfig(t *testing.T) {
	for _, raw := range []string{
		`{"report_url": "https://wd2.workday.com/ccx/service/customreport2/acme/x/r", "password": "env:P"}`,
		`{"report_url": "https://wd2.workday.com/somewhere/else", "username": "u", "password": "env:P"}`,
		`{"report_url": "http://wd2.workday.com/ccx/service/customreport2/acme/x/r", "username": "u", "password": "env:P"}`,
		`{"report_url": "https://wd2.workday.com/ccx/service/customreport2/acme/x/r", "username": "u"}`,
	} {
		if _, err := New(json.RawMessage(raw), connectortest.Secrets); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
}
