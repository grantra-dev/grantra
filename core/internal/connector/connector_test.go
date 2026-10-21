package connector

import (
	"slices"
	"testing"
)

func TestEnvSecretsOnlyReadsPrefixedVariables(t *testing.T) {
	t.Setenv("GRANTRA_SECRET_CRM_TOKEN", "t0ken")
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://grantra:hunter2@db/grantra")

	if v, err := EnvSecrets("env:GRANTRA_SECRET_CRM_TOKEN"); err != nil || v != "t0ken" {
		t.Fatalf("prefixed secret: got %q, %v", v, err)
	}
	for _, ref := range []string{
		"env:GRANTRA_DATABASE_URL", // the server's own settings stay out of reach
		"env:GRANTRA_SECRET_",
		"env:",
		"GRANTRA_SECRET_CRM_TOKEN",
		"env:GRANTRA_SECRET_UNSET",
	} {
		if v, err := EnvSecrets(ref); err == nil {
			t.Errorf("%q resolved to %q, want an error", ref, v)
		}
	}
}

func TestBuilder(t *testing.T) {
	var b Builder
	b.Entitlement(Entitlement{NativeID: "g1", Name: "Finance"})
	b.Entitlement(Entitlement{NativeID: "g1", Name: "Duplicate"})
	b.Entitlement(Entitlement{NativeID: "g2"})
	b.Grant("u1", "g1")
	b.Grant("u1", "g1")   // seen twice
	b.Grant("g2", "g1")   // a nested group, not an account
	b.Grant("u1", "gone") // an entitlement that was never read
	b.Account(Account{NativeID: "u1", Entitlements: []string{"g2", "g1"}})
	b.Account(Account{NativeID: "u2"})
	snap := b.Snapshot()
	if len(snap.Entitlements) != 2 || snap.Entitlements[0].Name != "Finance" || snap.Entitlements[1].Name != "g2" {
		t.Errorf("entitlements = %+v", snap.Entitlements)
	}
	if len(snap.Accounts) != 2 || !slices.Equal(snap.Accounts[0].Entitlements, []string{"g1", "g2"}) || snap.Accounts[1].Entitlements != nil {
		t.Errorf("accounts = %+v", snap.Accounts)
	}
	if got := Attributes("title", "CFO", "department", "", "odd"); len(got) != 1 || got["title"] != "CFO" {
		t.Errorf("attributes = %v", got)
	}
}
