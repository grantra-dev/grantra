package awsidc

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/connectortest"
)

const (
	instance = "arn:aws:sso:::instance/ssoins-1"
	admin    = "arn:aws:sso:::permissionSet/ssoins-1/ps-admin"
	readOnly = "arn:aws:sso:::permissionSet/ssoins-1/ps-ro"
)

// fakeAWS answers the Identity Center and identity store calls, by their
// X-Amz-Target, for an instance with two groups, two permission sets on
// two accounts and three users.
func fakeAWS(t *testing.T, instances int) *connectortest.Server {
	t.Helper()
	srv := connectortest.New(t, func(r *http.Request) bool {
		return strings.Contains(r.Header.Get("Authorization"), "Credential=AKIDGRANTRA/")
	})
	answers := map[string]func(in map[string]any) any{
		"SWBExternalService.ListInstances": func(map[string]any) any {
			list := []map[string]any{{"InstanceArn": instance, "IdentityStoreId": "d-123"}}
			if instances == 2 {
				list = append(list, map[string]any{"InstanceArn": "arn:aws:sso:::instance/ssoins-2", "IdentityStoreId": "d-456"})
			}
			return map[string]any{"Instances": list}
		},
		"AWSIdentityStore.ListGroups": func(map[string]any) any {
			return map[string]any{"Groups": []map[string]any{{"GroupId": "g-admins", "DisplayName": "Cloud admins"}, {"GroupId": "g-devs", "DisplayName": "Developers"}}}
		},
		"AWSIdentityStore.ListGroupMemberships": func(in map[string]any) any {
			users := map[string][]string{"g-admins": {"u-ana"}, "g-devs": {"u-ana", "u-luis"}}[in["GroupId"].(string)]
			var out []map[string]any
			for _, u := range users {
				out = append(out, map[string]any{"GroupId": in["GroupId"], "MembershipId": "m-" + u, "MemberId": map[string]string{"UserId": u}})
			}
			return map[string]any{"GroupMemberships": out}
		},
		"SWBExternalService.ListPermissionSets": func(map[string]any) any {
			return map[string]any{"PermissionSets": []string{admin, readOnly}}
		},
		"SWBExternalService.DescribePermissionSet": func(in map[string]any) any {
			name := map[string]string{admin: "AdministratorAccess", readOnly: "ReadOnlyAccess"}[in["PermissionSetArn"].(string)]
			return map[string]any{"PermissionSet": map[string]any{"Name": name, "PermissionSetArn": in["PermissionSetArn"]}}
		},
		"SWBExternalService.ListAccountsForProvisionedPermissionSet": func(in map[string]any) any {
			if in["PermissionSetArn"] == admin {
				return map[string]any{"AccountIds": []string{"111111111111"}}
			}
			return map[string]any{"AccountIds": []string{"111111111111", "222222222222"}}
		},
		"SWBExternalService.ListAccountAssignments": func(in map[string]any) any {
			var list []map[string]any
			switch in["PermissionSetArn"].(string) + "@" + in["AccountId"].(string) {
			case admin + "@111111111111":
				list = []map[string]any{{"PrincipalType": "GROUP", "PrincipalId": "g-admins"}}
			case readOnly + "@111111111111":
				list = []map[string]any{{"PrincipalType": "GROUP", "PrincipalId": "g-devs"}, {"PrincipalType": "USER", "PrincipalId": "u-luis"}}
			case readOnly + "@222222222222":
				list = []map[string]any{{"PrincipalType": "USER", "PrincipalId": "u-bot"}}
			}
			return map[string]any{"AccountAssignments": list}
		},
		"AWSIdentityStore.ListUsers": func(in map[string]any) any {
			if in["IdentityStoreId"] != "d-123" {
				t.Errorf("users of identity store %v", in["IdentityStoreId"])
			}
			if in["NextToken"] == nil {
				return map[string]any{"NextToken": "p2", "Users": []map[string]any{{"UserId": "u-ana", "UserName": "ana@acme.com", "DisplayName": "Ana",
					"Emails": []map[string]any{{"Value": "ana@acme.com", "Primary": true}}, "Title": "CTO", "UserStatus": "ENABLED"}}}
			}
			return map[string]any{"Users": []map[string]any{
				{"UserId": "u-luis", "UserName": "luis", "Name": map[string]string{"GivenName": "Luis", "FamilyName": "Pérez"}, "UserStatus": "DISABLED"},
				{"UserId": "u-bot", "UserName": "deploy-bot"},
			}}
		},
	}
	srv.Handle("POST /{$}", func(w http.ResponseWriter, r *http.Request) {
		target := r.Header.Get("X-Amz-Target")
		answer, ok := answers[target]
		if !ok {
			connectortest.Reply(w, http.StatusBadRequest, map[string]string{"__type": "UnknownOperationException", "message": target})
			return
		}
		var in map[string]any
		json.NewDecoder(r.Body).Decode(&in) //nolint:errcheck
		w.Header().Set("Content-Type", "application/x-amz-json-1.1")
		json.NewEncoder(w).Encode(answer(in)) //nolint:errcheck
	})
	return srv
}

func newClient(t *testing.T, srv *connectortest.Server, extra string) connector.Reader {
	t.Helper()
	r, err := New(json.RawMessage(`{"region": "eu-west-1", "access_key_id": "AKIDGRANTRA", "secret_access_key": "env:SECRET",
		"endpoint_url": "`+srv.URL+`", "allow_http": true`+extra+`}`), connectortest.Secrets)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRead(t *testing.T) {
	srv := fakeAWS(t, 1)
	snap, err := newClient(t, srv, "").Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range snap.Entitlements {
		names = append(names, e.Name)
	}
	if want := []string{"Cloud admins", "Developers", "AdministratorAccess on 111111111111", "ReadOnlyAccess on 111111111111", "ReadOnlyAccess on 222222222222"}; !slices.Equal(names, want) {
		t.Errorf("entitlements %v, want %v", names, want)
	}
	if len(snap.Accounts) != 3 {
		t.Fatalf("got %d accounts", len(snap.Accounts))
	}
	ana, luis, bot := snap.Accounts[0], snap.Accounts[1], snap.Accounts[2]
	adminOn1 := assignmentPrefix + "111111111111:" + admin
	roOn1 := assignmentPrefix + "111111111111:" + readOnly
	// Assignments to a group reach each member.
	if ana.Email != "ana@acme.com" || !ana.Active || ana.Attributes["title"] != "CTO" ||
		!slices.Equal(ana.Entitlements, []string{"g-admins", "g-devs", adminOn1, roOn1}) {
		t.Errorf("ana = %+v", ana)
	}
	if luis.Active || luis.DisplayName != "Luis Pérez" || !slices.Equal(luis.Entitlements, []string{"g-devs", roOn1}) {
		t.Errorf("luis = %+v", luis)
	}
	if !bot.Active || !slices.Equal(bot.Entitlements, []string{assignmentPrefix + "222222222222:" + readOnly}) {
		t.Errorf("bot = %+v", bot)
	}
}

func TestPicksTheInstance(t *testing.T) {
	srv := fakeAWS(t, 2)
	if _, err := newClient(t, srv, "").Read(context.Background()); err == nil || !strings.Contains(err.Error(), "set instance_arn") {
		t.Fatalf("two instances: %v", err)
	}
	if _, err := newClient(t, srv, `, "instance_arn": "`+instance+`"`).Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := newClient(t, srv, `, "instance_arn": "arn:aws:sso:::instance/nope"`).Read(context.Background()); err == nil {
		t.Fatal("read an instance that does not exist")
	}
}

func TestConfig(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"region": "eu-west-1", "secret_access_key": "env:S"}`,
		`{"region": "eu-west-1", "access_key_id": "AKID"}`,
		`{"region": "eu-west-1", "instance_arn": "ssoins-1"}`,
	} {
		if _, err := New(json.RawMessage(raw), connectortest.Secrets); err == nil {
			t.Errorf("%s was accepted", raw)
		}
	}
	if _, ok := any(&Client{}).(connector.Writer); ok {
		t.Error("the AWS connector writes; its docs say it only reads")
	}
}
