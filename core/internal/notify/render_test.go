package notify

import (
	"strings"
	"testing"
)

func TestRenderEveryKind(t *testing.T) {
	data := map[string]any{}
	for k, v := range approvalData {
		data[k] = v
	}
	data["status"] = "approved"
	data["expires_at"] = "2026-11-13T08:00:00Z"
	data["campaign"] = "Q4 review"
	data["items"] = float64(1)
	data["on_missing"] = "revoke"
	data["count"] = float64(3)
	data["more"] = float64(0)
	data["violations"] = []any{map[string]any{"identity": "Ana", "policy": "Payments", "severity": "high", "reopened": false}}
	data["person"] = "Ana"
	data["account"] = "Production database root"
	data["username"] = "postgres"
	data["reason"] = "INC-1234"
	data["checked_out_at"] = "2026-11-13T07:00:00Z"
	data["rotates"] = true
	data["identities"] = []any{map[string]any{"name": "billing-bot", "kind": "service", "from": "Ana", "to": "Boss"}}
	data["tokens"] = []any{map[string]any{"name": "Nightly HR", "prefix": "grantra_st_abc123", "creator": "Ana"}}
	data["title"], data["old_value"], data["value"], data["proposed_by"] = "Longest break-glass check-out", "60 minutes", "120 minutes", "Ana"
	data["from"], data["to"] = "2026-07-01", "2026-09-30"
	data["uses"] = []any{map[string]any{"person": "Ana", "account": "Production database root", "reason": "INC-1234", "checked_out_at": "2026-11-13T07:00:00Z"}}
	for _, k := range Kinds {
		subject, body, err := render(k.Kind, view{Name: "Boss", RecipientID: "boss", URL: "https://g/x", Data: data, SettingsURL: "https://g/s"})
		if err != nil {
			t.Fatalf("%s: %v", k.Kind, err)
		}
		if subject == "" || strings.Contains(subject+body, "<no value>") || !strings.Contains(body, "https://g/s") {
			t.Errorf("%s rendered badly:\n%s\n%s", k.Kind, subject, body)
		}
	}
}

func TestRenderReportReady(t *testing.T) {
	data := map[string]any{"title": "SOC 2", "from": "2026-07-01", "to": "2026-09-30"}
	subject, body, err := render(ReportReady, view{Name: "Ines", URL: "https://g/r", Data: data, SettingsURL: "https://g/s"})
	if err != nil || subject != "Evidence pack ready: SOC 2" || !strings.Contains(body, `"SOC 2" for 2026-07-01 to 2026-09-30 is ready`) {
		t.Errorf("ready: %q\n%s (%v)", subject, body, err)
	}
	data["error"] = "Leavers has more than 100000 rows"
	subject, body, _ = render(ReportReady, view{Name: "Ines", URL: "https://g/r", Data: data, SettingsURL: "https://g/s"})
	if subject != "A scheduled report could not be made: SOC 2" || !strings.Contains(body, "more than 100000 rows") {
		t.Errorf("failed: %q\n%s", subject, body)
	}
}

func TestRenderDecided(t *testing.T) {
	base := map[string]any{
		"requester_id": "boss", "beneficiary_id": "bob", "requester": "Boss", "beneficiary": "Bob",
		"target": "CRM users", "application": "CRM", "decided_by": "Owner", "comment": "Not needed",
		"expires_at": "", "status": "rejected",
	}
	cases := []struct {
		recipient, status, subject, line string
	}{
		{"bob", "rejected", "Rejected: CRM users", "The request for you to get CRM users in CRM was rejected by Owner."},
		{"boss", "rejected", "Rejected: CRM users for Bob", "Their reason: Not needed"},
		{"bob", "approved", "Approved: CRM users", "You now have CRM users in CRM."},
		{"boss", "expired", "Expired: CRM users for Bob", "expired because nobody decided it in time"},
	}
	for _, c := range cases {
		base["status"] = c.status
		subject, body, err := render(RequestDecided, view{Name: "X", RecipientID: c.recipient, Data: base})
		if err != nil {
			t.Fatal(err)
		}
		if subject != c.subject || !strings.Contains(body, c.line) {
			t.Errorf("%s/%s: %q\n%s", c.recipient, c.status, subject, body)
		}
	}
}

func TestSubjectIsOneLine(t *testing.T) {
	data := map[string]any{"beneficiary": "Eve\r\nBcc: victim@example.com", "target": "x"}
	subject, _, err := render(ApprovalNeeded, view{Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(subject, "\r\n") {
		t.Errorf("subject %q spans lines", subject)
	}
}

func TestRenderReviewsAndSoD(t *testing.T) {
	subject, body, err := render(ReviewAssigned, view{Name: "Boss", Data: map[string]any{
		"campaign": "Q4", "items": float64(1), "admin_items": false, "on_missing": "revoke", "due_at": "2026-10-28T08:00:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Access review: 1 item to review in Q4" || !strings.Contains(body, "What is not reviewed by then is revoked.") {
		t.Errorf("review email:\n%s\n%s", subject, body)
	}
	subject, body, err = render(SoDViolation, view{Name: "Admin", Data: map[string]any{
		"count": float64(22), "more": float64(20),
		"violations": []any{
			map[string]any{"identity": "Ana", "policy": "Payments", "severity": "critical", "reopened": true},
			map[string]any{"identity": "Bob", "policy": "Payments", "severity": "high", "reopened": false},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- Ana: Payments (critical), exception ended\n- Bob: Payments (high)\n", "...and 20 more."} {
		if !strings.Contains(body, want) {
			t.Errorf("SoD body lacks %q:\n%s", want, body)
		}
	}
	if subject != "Separation of duties: 22 new violations" {
		t.Errorf("subject %q", subject)
	}
}

func TestRenderRisk(t *testing.T) {
	data := map[string]any{}
	for k, v := range approvalData {
		data[k] = v
	}
	data["step"] = "risk"
	data["sod_conflicts"] = "Payments"
	subject, body, err := render(ApprovalNeeded, view{Name: "Admin", Data: data})
	if err != nil {
		t.Fatal(err)
	}
	if subject != "Separation of duties risk: Ana asks for CRM users" ||
		!strings.Contains(body, "this access breaks separation of duties: Payments. Approving it means accepting that risk") ||
		!strings.Contains(body, "or another Grantra admin") {
		t.Errorf("risk email:\n%s\n%s", subject, body)
	}
	_, body, err = render(RequestExpiring, view{Name: "Admin", Data: data})
	if err != nil || !strings.Contains(body, "to accept or reject a separation of duties risk (Payments).") {
		t.Errorf("risk reminder: %v\n%s", err, body)
	}
}

// Emails link to the exact request or campaign, not to the list it is in.
func TestPageForLinksToTheTask(t *testing.T) {
	request := map[string]any{"request_id": "0b7c5e3e-4f39-4a5e-9a59-d2c1a1e6a001"}
	campaign := map[string]any{"campaign_id": "6f2d9a3c-1b8e-4c47-8e1f-2a9b7c3d4e5f"}
	for _, tc := range []struct {
		kind Kind
		data map[string]any
		want string
	}{
		{ApprovalNeeded, request, "/approvals?request=0b7c5e3e-4f39-4a5e-9a59-d2c1a1e6a001"},
		{RequestExpiring, request, "/approvals?request=0b7c5e3e-4f39-4a5e-9a59-d2c1a1e6a001"},
		{RequestDecided, request, "/requests?request=0b7c5e3e-4f39-4a5e-9a59-d2c1a1e6a001"},
		{ReviewAssigned, campaign, "/reviews?campaign=6f2d9a3c-1b8e-4c47-8e1f-2a9b7c3d4e5f"},
		{CampaignDueSoon, campaign, "/reviews?campaign=6f2d9a3c-1b8e-4c47-8e1f-2a9b7c3d4e5f"},
		// Without the id, the list; kinds about many things keep their page.
		{ApprovalNeeded, map[string]any{}, "/approvals"},
		{ReviewAssigned, map[string]any{"campaign_id": ""}, "/reviews"},
		{BreakGlassUsed, request, "/vault?tab=break-glass"},
		{SoDViolation, campaign, "/governance/separation-of-duties"},
		// Ids come from the data, so they are escaped like any value.
		{RequestDecided, map[string]any{"request_id": "a&b=c"}, "/requests?request=a%26b%3Dc"},
	} {
		if got := pageFor(tc.kind, tc.data); got != tc.want {
			t.Errorf("pageFor(%s, %v) = %q, want %q", tc.kind, tc.data, got, tc.want)
		}
	}
	for _, k := range Kinds {
		if pages[k.Kind] == "" {
			t.Errorf("%s has no console page", k.Kind)
		}
	}
}
