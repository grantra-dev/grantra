package grantralogin

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

func TestDiscovery(t *testing.T) {
	for _, tc := range []struct {
		in   NewIdentityProvider
		want string
	}{
		{NewIdentityProvider{Kind: KindEntra, Tenant: "acme.onmicrosoft.com"}, "https://login.microsoftonline.com/acme.onmicrosoft.com/v2.0/.well-known/openid-configuration"},
		{NewIdentityProvider{Kind: KindEntra, Tenant: "0b4c6a5e-7a35-4c2e-9f62-1d4f0d7c2a11"}, "https://login.microsoftonline.com/0b4c6a5e-7a35-4c2e-9f62-1d4f0d7c2a11/v2.0/.well-known/openid-configuration"},
		{NewIdentityProvider{Kind: KindEntra, Tenant: "common"}, ""},
		{NewIdentityProvider{Kind: KindEntra, Tenant: "organizations"}, ""},
		{NewIdentityProvider{Kind: KindEntra, Tenant: "acme/../x"}, ""},
		{NewIdentityProvider{Kind: KindOkta, Domain: "https://acme.okta.com/"}, "https://acme.okta.com/.well-known/openid-configuration"},
		{NewIdentityProvider{Kind: KindOkta, Domain: "acme.okta.com/evil?"}, ""},
		{NewIdentityProvider{Kind: KindOIDC, Issuer: "https://login.acme.com/realms/acme/"}, "https://login.acme.com/realms/acme/.well-known/openid-configuration"},
		{NewIdentityProvider{Kind: KindOIDC, Issuer: "http://login.acme.com"}, ""},
		{NewIdentityProvider{Kind: KindOIDC, Issuer: "http://localhost:8181/realms/master"}, "http://localhost:8181/realms/master/.well-known/openid-configuration"},
		{NewIdentityProvider{Kind: KindSAML, MetadataURL: "https://idp.acme.com/metadata"}, "https://idp.acme.com/metadata"},
		{NewIdentityProvider{Kind: KindSAML, MetadataURL: "https://user:pw@idp.acme.com/metadata"}, ""},
		{NewIdentityProvider{Kind: "ldap"}, ""},
	} {
		got, err := discovery(tc.in)
		var bad *InvalidError
		if tc.want == "" && !errors.As(err, &bad) || got != tc.want {
			t.Errorf("discovery(%+v) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
}

func TestCleanDomains(t *testing.T) {
	got, err := cleanDomains([]string{" Acme.io", "@acme.dev", "", "acme.io"})
	if err != nil || !slices.Equal(got, []string{"acme.dev", "acme.io"}) {
		t.Errorf("clean = %v, %v", got, err)
	}
	for _, bad := range []string{"acme", "a b.com", "*.acme.io", "acme.io/x"} {
		if _, err := cleanDomains([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseExport(t *testing.T) {
	data, err := os.ReadFile("testdata/legacy-realm.json")
	if err != nil {
		t.Fatal(err)
	}
	ex, err := parseExport(data, "grantra")
	if err != nil {
		t.Fatal(err)
	}
	if ex.realm != "legacy" || !slices.Equal(ex.names(ex.clients, "clientId"), []string{"intranet"}) ||
		!slices.Equal(ex.names(ex.roles, "name"), []string{"finance-approver"}) || len(ex.users) != 1 {
		t.Fatalf("export = %+v", ex)
	}
	u := ex.users[0]
	if _, ok := u["id"]; ok {
		t.Error("user keeps the old realm's id")
	}
	if _, ok := u["federatedIdentities"]; ok {
		t.Error("user keeps links to the old realm's providers")
	}
	if roles := u["realmRoles"].([]any); !slices.Equal(roles, []any{"default-roles-grantra", "finance-approver"}) {
		t.Errorf("realm roles = %v", roles)
	}
	if cr := u["clientRoles"].(object); len(cr) != 1 || cr["intranet"] == nil {
		t.Errorf("client roles = %v", cr)
	}
	if _, ok := ex.clients[0]["defaultClientScopes"]; ok {
		t.Error("client keeps the old realm's scopes")
	}
	if ex.clients[0]["secret"] != "intranet-secret" {
		t.Error("client loses its secret")
	}
	if len(ex.notes) != 3 || !strings.Contains(ex.notes[0], "account, legacy-realm") {
		t.Errorf("notes = %q", ex.notes)
	}

	for _, bad := range []string{`not json`, `[]`, `[{"realm": "a"}, {"realm": "b"}]`, `{"users": []}`} {
		var inv *InvalidError
		if _, err := parseExport([]byte(bad), "grantra"); !errors.As(err, &inv) {
			t.Errorf("parse %s = %v", bad, err)
		}
	}
	if ex, err := parseExport([]byte(`[{"realm": "a"}]`), "grantra"); err != nil || ex.realm != "a" || len(ex.notes) != 1 {
		t.Errorf("parse an array = %+v, %v", ex, err)
	}
}
