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
