package flatfile

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector"
)

func parse(t *testing.T, cfg, file string) connector.Snapshot {
	t.Helper()
	snap, err := Parse(json.RawMessage(cfg), strings.NewReader(file))
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestParseOneRowPerAccount(t *testing.T) {
	snap := parse(t, `{}`, "Account ID,Username,Email,Display Name,Employee ID,Status,Entitlements,Branch\n"+
		"1,ana,ana@example.com,Ana,E1,active,Admin; Billing ;Admin,Madrid\n"+
		"2,luis,,,,disabled,,\n"+
		"3,marta,,,,,Billing,\n")
	if len(snap.Accounts) != 3 {
		t.Fatalf("accounts = %+v", snap.Accounts)
	}
	ana, luis, marta := snap.Accounts[0], snap.Accounts[1], snap.Accounts[2]
	if ana.NativeID != "1" || ana.Username != "ana" || ana.Email != "ana@example.com" || ana.DisplayName != "Ana" ||
		ana.ExternalID != "E1" || !ana.Active || !slices.Equal(ana.Entitlements, []string{"Admin", "Billing"}) ||
		ana.Attributes["branch"] != "Madrid" {
		t.Errorf("ana = %+v", ana)
	}
	if luis.Active || len(luis.Entitlements) != 0 || len(luis.Attributes) != 0 {
		t.Errorf("luis = %+v", luis)
	}
	if !marta.Active {
		t.Errorf("an empty status is active: %+v", marta)
	}
	var names []string
	for _, e := range snap.Entitlements {
		if e.NativeID != e.Name {
			t.Errorf("entitlement %+v", e)
		}
		names = append(names, e.Name)
	}
	if !slices.Equal(names, []string{"Admin", "Billing"}) {
		t.Errorf("entitlements = %v", names)
	}
}

// A "users join roles" export has a row per account and entitlement.
func TestParseRowPerMembership(t *testing.T) {
	snap := parse(t, `{"columns": {"username": "Login", "entitlements": "Role"}}`,
		"Login,Role,Status,email\n"+
			"ana,Admin,,\n"+
			"ana,Billing,active,ana@example.com\n"+
			"luis,Billing,disabled,\n"+
			"ana,Admin,,\n")
	if len(snap.Accounts) != 2 || len(snap.Entitlements) != 2 {
		t.Fatalf("snapshot = %+v", snap)
	}
	ana := snap.Accounts[0]
	if ana.NativeID != "ana" || ana.Username != "ana" || ana.Email != "ana@example.com" || !ana.Active ||
		!slices.Equal(ana.Entitlements, []string{"Admin", "Billing"}) {
		t.Errorf("ana = %+v", ana)
	}
	if luis := snap.Accounts[1]; luis.Active || !slices.Equal(luis.Entitlements, []string{"Billing"}) {
		t.Errorf("luis = %+v", luis)
	}
	// A status the first rows left out is taken from a later one.
	snap = parse(t, `{}`, "username,status\nana,\nana,disabled\n")
	if snap.Accounts[0].Active {
		t.Errorf("status from the second row was not taken: %+v", snap.Accounts[0])
	}
}

func TestParseSeparator(t *testing.T) {
	snap := parse(t, `{"separator": "|"}`, "username,entitlements\nana,A|B; C\n")
	if got := snap.Accounts[0].Entitlements; !slices.Equal(got, []string{"A", "B; C"}) {
		t.Errorf("entitlements = %v", got)
	}
}

func TestParseProblems(t *testing.T) {
	for name, tt := range map[string]struct{ cfg, file, want string }{
		"no id column":    {`{}`, "email\nana@example.com\n", `no "account_id" column, nor "username"`},
		"empty id":        {`{}`, "username,email\n,ana@example.com\n", "line 2: username is empty"},
		"bad status":      {`{}`, "username,status\nana,on leave\n", `line 2: unknown status "on leave"`},
		"conflict":        {`{}`, "username,email\nana,a@example.com\nana,b@example.com\n", `line 3: account "ana": email is "b@example.com" here but "a@example.com" on line 2`},
		"status conflict": {`{}`, "username,status\nana,active\nana,disabled\n", `line 3: account "ana": status is "disabled" here but "active" on line 2`},
		"attr conflict":   {`{}`, "username,branch\nana,Madrid\nana,Lima\n", `branch is "Lima" here but "Madrid"`},
		"ragged":          {`{}`, "username\nana,extra\n", "line 2:"},
		"empty":           {`{}`, "", "the file is empty"},
		"unknown field":   {`{"columns": {"salary": "Pay"}}`, "username\nana\n", `unknown field "salary"`},
		"unknown option":  {`{"delimiter": ","}`, "username\nana\n", "unknown field"},
		"blank separator": {`{"separator": " "}`, "username\nana\n", "separator"},
	} {
		_, err := Parse(json.RawMessage(tt.cfg), strings.NewReader(tt.file))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tt.want)
		}
	}

	// Many problems are summed up, not listed in full.
	var b strings.Builder
	b.WriteString("username,status\n")
	for range maxReported + 5 {
		b.WriteString("ana,maybe\n")
	}
	_, err := Parse(nil, strings.NewReader(b.String()))
	if err == nil || !strings.HasSuffix(err.Error(), "; and 5 more") {
		t.Errorf("many problems: %v", err)
	}
}

func TestConnector(t *testing.T) {
	r, err := connector.New(Kind, json.RawMessage(`{"columns": {"username": "Login"}}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Read(context.Background()); !errors.Is(err, ErrUploadOnly) {
		t.Errorf("read without a file = %v", err)
	}
	if _, err := connector.New(Kind, json.RawMessage(`{"columns": {"nope": "x"}}`), nil); err == nil {
		t.Error("bad config accepted")
	}
}
