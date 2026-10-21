package salesforce

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
)

const api = "/services/data/v62.0"

// fakeSalesforce is an org with two profiles, a permission set, a
// permission set group and three users, answering SOQL by the object
// queried.
func fakeSalesforce(t *testing.T) *connectortest.Server {
	t.Helper()
	srv := connectortest.New(t, func(r *http.Request) bool {
		return r.URL.Path == "/services/oauth2/token" || r.Header.Get("Authorization") == "Bearer sf-token"
	})
	srv.Handle("POST /services/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm() //nolint:errcheck
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "consumer-key" || r.Form.Get("client_secret") != "SECRET" {
			connectortest.Reply(w, http.StatusBadRequest, map[string]string{"error": "invalid_client", "error_description": "invalid client credentials"})
			return
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"access_token": "sf-token", "token_type": "Bearer", "instance_url": srv.URL})
	})
	records := map[string]any{
		"Profile":            []map[string]any{{"Id": "00e000000000001AAA", "Name": "System Administrator"}, {"Id": "00e000000000002AAA", "Name": "Standard User"}},
		"PermissionSet":      []map[string]any{{"Id": "0PS000000000001AAA", "Name": "Billing", "Label": "Billing admin"}},
		"PermissionSetGroup": []map[string]any{{"Id": "0PG000000000001AAA", "MasterLabel": "Sales ops"}},
		"PermissionSetAssignment": []map[string]any{
			{"AssigneeId": "005000000000001AAA", "PermissionSetId": "0PS000000000001AAA"},
			{"AssigneeId": "005000000000002AAA", "PermissionSetId": "0PSgroupagg", "PermissionSetGroupId": "0PG000000000001AAA"},
			{"AssigneeId": "005000000000001AAA", "PermissionSetId": "0PSprofile"}, // a profile's own permission set
		},
	}
	srv.Handle("GET "+api+"/query", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		from := strings.Fields(q[strings.Index(q, " FROM ")+6:])[0]
		if from == "User" {
			connectortest.Reply(w, http.StatusOK, map[string]any{"done": false, "nextRecordsUrl": api + "/query/01gUSERS-2000", "records": []map[string]any{
				{"Id": "005000000000001AAA", "Username": "ana@acme.com.prod", "Email": "ana@acme.com", "Name": "Ana", "EmployeeNumber": "E1", "IsActive": true,
					"LastLoginDate": "2026-10-04T07:30:00.000+0000", "ProfileId": "00e000000000001AAA", "UserType": "Standard", "FederationIdentifier": "ana@acme.com"},
			}})
			return
		}
		if from == "PermissionSetAssignment" && strings.Contains(q, "WHERE") {
			connectortest.Reply(w, http.StatusOK, map[string]any{"done": true, "records": []map[string]any{{"Id": "0Pa000000000001AAA"}}})
			return
		}
		connectortest.Reply(w, http.StatusOK, map[string]any{"done": true, "records": records[from]})
	})
	srv.JSON("GET "+api+"/query/01gUSERS-2000", map[string]any{"done": true, "records": []map[string]any{
		{"Id": "005000000000002AAA", "Username": "luis@acme.com.prod", "Email": "luis@acme.com", "Name": "Luis", "IsActive": false, "LastLoginDate": nil, "ProfileId": "00e000000000002AAA"},
		{"Id": "005000000000003AAA", "Username": "integration@acme.com.prod", "IsActive": true, "ProfileId": "00e000000000002AAA", "UserType": "Standard"},
	}})
	return srv
}

func newClient(t *testing.T, srv *connectortest.Server, secret string) *Client {
	t.Helper()
	r, err := New(json.RawMessage(`{"instance_url": "`+srv.URL+`", "client_id": "consumer-key", "client_secret": "`+secret+`", "allow_http": true}`), connectortest.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r.(*Client)
}

func TestRead(t *testing.T) {
	srv := fakeSalesforce(t)
	snap, err := newClient(t, srv, "env:SECRET").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range snap.Entitlements {
		names = append(names, e.NativeID+"="+e.Name)
	}
	if want := []string{"profile:00e000000000001AAA=System Administrator (profile)", "profile:00e000000000002AAA=Standard User (profile)", "0PS000000000001AAA=Billing admin", "psg:0PG000000000001AAA=Sales ops (permission set group)"}; !slices.Equal(names, want) {
		t.Errorf("entitlements %v, want %v", names, want)
	}
	if len(snap.Accounts) != 3 {
		t.Fatalf("got %d accounts", len(snap.Accounts))
	}
	ana, luis := snap.Accounts[0], snap.Accounts[1]
	if ana.Email != "ana@acme.com" || ana.ExternalID != "E1" || !ana.Active || ana.Attributes["federation_id"] != "ana@acme.com" ||
		!slices.Equal(ana.Entitlements, []string{"0PS000000000001AAA", "profile:00e000000000001AAA"}) || !ana.LastLogin.Equal(time.Date(2026, 10, 4, 7, 30, 0, 0, time.UTC)) {
		t.Errorf("ana = %+v", ana)
	}
	if luis.Active || !luis.LastLogin.IsZero() || !slices.Equal(luis.Entitlements, []string{"psg:0PG000000000001AAA", "profile:00e000000000002AAA"}) {
		t.Errorf("luis = %+v", luis)
	}
	if _, err := newClient(t, srv, "env:WRONG").Read(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid client credentials") {
		t.Errorf("wrong secret: %v", err)
	}
}

func TestWrites(t *testing.T) {
	srv := fakeSalesforce(t)
	srv.Status("POST "+api+"/sobjects/PermissionSetAssignment", http.StatusBadRequest,
		[]map[string]string{{"message": "Duplicate PermissionSetAssignment", "errorCode": "DUPLICATE_VALUE"}})
	srv.Status("DELETE "+api+"/sobjects/PermissionSetAssignment/0Pa000000000001AAA", http.StatusNoContent, nil)
	srv.Status("PATCH "+api+"/sobjects/User/005000000000002AAA", http.StatusNoContent, nil)
	c := newClient(t, srv, "env:SECRET")
	var _ connector.Writer = c
	ctx := context.Background()
	for _, err := range []error{
		c.AddMember(ctx, "psg:0PG000000000001AAA", "005000000000002AAA"), // assigned already
		c.RemoveMember(ctx, "0PS000000000001AAA", "005000000000001AAA"),
		c.AddMember(ctx, "profile:00e000000000001AAA", "005000000000002AAA"),
		c.SetActive(ctx, "005000000000002AAA", false),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, err := range []error{c.RemoveMember(ctx, "profile:00e000000000001AAA", "005000000000001AAA"), c.RemoveMember(ctx, "0PS000000000001AAA", "005000000000001AAA' OR Id != '")} {
		if err == nil {
			t.Error("a change Grantra must refuse went through")
		}
	}
	got := srv.Calls()[1:] // after the token
	want := []string{
		`POST ` + api + `/sobjects/PermissionSetAssignment {"AssigneeId":"005000000000002AAA","PermissionSetGroupId":"0PG000000000001AAA"}`,
		"DELETE " + api + "/sobjects/PermissionSetAssignment/0Pa000000000001AAA",
		`PATCH ` + api + `/sobjects/User/005000000000002AAA {"ProfileId":"00e000000000001AAA"}`,
		`PATCH ` + api + `/sobjects/User/005000000000002AAA {"IsActive":false}`,
	}
	if !slices.Equal(got, want) {
		t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
