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
