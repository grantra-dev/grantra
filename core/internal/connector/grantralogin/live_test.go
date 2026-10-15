package grantralogin

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/connector"
)

// TestLive runs the connector against a real Keycloak that imported
// deploy/keycloak/grantra-realm.json, such as the one in docker-compose:
//
//	GRANTRA_TEST_KEYCLOAK_URL=http://localhost:8081 go test ./internal/connector/grantralogin -run Live
//
// It creates a user and changes memberships, so never point it at a real
// realm.
func TestLive(t *testing.T) {
	base := os.Getenv("GRANTRA_TEST_KEYCLOAK_URL")
	if base == "" {
		t.Skip("GRANTRA_TEST_KEYCLOAK_URL is not set")
	}
	r, err := New(json.RawMessage(`{"url": "`+base+`", "client_secret": "env:X", "allow_http": true, "page_size": 3}`),
		func(string) (string, error) { return "grantra-dev-connector-secret", nil })
	if err != nil {
		t.Fatal(err)
	}
	c := r.(*Client)
	ctx := context.Background()

	snap, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var vpn string
	for _, e := range snap.Entitlements {
		t.Logf("entitlement %s %q %q", e.NativeID, e.Name, e.Description)
		if e.Name == "VPN users" {
			vpn = e.NativeID
		}
	}
	if vpn == "" {
		t.Fatal("no VPN users group")
	}
	for _, a := range snap.Accounts {
		if a.Username == "service-account-grantra-connector" {
			t.Error("the service account reads as a person's account")
		}
	}
	t.Logf("%d accounts", len(snap.Accounts))

	id, err := c.CreateAccount(ctx, connector.NewAccount{
		Username: "live.test@example.com", Email: "live.test@example.com", DisplayName: "Live Test", ExternalID: "E-LIVE",
	})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := c.CreateAccount(ctx, connector.NewAccount{Username: "live.test@example.com", Email: "live.test@example.com"}); err != nil || again != id {
		t.Fatalf("repeat create = %q, %v", again, err)
	}
	for _, step := range []func() error{
		func() error { return c.AddMember(ctx, vpn, id) },
		func() error { return c.AddMember(ctx, "role:grantra-auditor", id) },
		func() error { return c.SetActive(ctx, id, false) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	snap, err = c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range snap.Accounts {
		if a.NativeID != id {
			continue
		}
		t.Logf("created %+v", a)
		if a.Active || !slices.Contains(a.Entitlements, vpn) || !slices.Contains(a.Entitlements, "role:grantra-auditor") {
			t.Errorf("account = %+v", a)
		}
		if a.ExternalID != "E-LIVE" {
			t.Errorf("external id = %q", a.ExternalID)
		}
	}
	for _, step := range []func() error{
		func() error { return c.RemoveMember(ctx, vpn, id) },
		func() error { return c.RemoveMember(ctx, vpn, id) },
		func() error { return c.RemoveMember(ctx, "role:grantra-auditor", id) },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.do(ctx, "DELETE", "/users/"+id, nil, nil); err != nil {
		t.Logf("clean up: %v (the service account may not delete users)", err)
	}
}

// TestLiveAdmin manages apps and the sign-in policy of a real Keycloak, like
// TestLive. It leaves the realm on the optional policy.
func TestLiveAdmin(t *testing.T) {
	base := os.Getenv("GRANTRA_TEST_KEYCLOAK_URL")
	if base == "" {
		t.Skip("GRANTRA_TEST_KEYCLOAK_URL is not set")
	}
	r, err := New(json.RawMessage(`{"url": "`+base+`", "client_secret": "env:X", "allow_http": true}`),
		func(string) (string, error) { return "grantra-dev-connector-secret", nil })
	if err != nil {
		t.Fatal(err)
	}
	c := r.(*Client)
	ctx := context.Background()

	st, err := c.Status(ctx)
	if err != nil || st.Realm != "grantra" || st.LoginTheme != "grantra" {
		t.Fatalf("status = %+v, %v", st, err)
	}
	app, secret, err := c.CreateApp(ctx, NewSSOApp{ClientID: "live-wiki", Name: "Wiki", RedirectURIs: []string{"https://wiki.example.com/callback"}})
	if err != nil || secret == "" || app.ID == "" {
		t.Fatalf("create = %+v %q %v", app, secret, err)
	}
	if again, err := c.RotateSecret(ctx, app.ID); err != nil || again == "" || again == secret {
		t.Fatalf("rotate = %q, %v", again, err)
	}
	apps, err := c.Apps(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, a := range apps {
		ids = append(ids, a.ClientID)
	}
	if !slices.Contains(ids, "live-wiki") || !slices.Contains(ids, "grantra-console") || slices.Contains(ids, "grantra-connector") {
		t.Errorf("apps = %v", ids)
	}
	defer c.do(ctx, "DELETE", "/clients/"+app.ID, nil, nil) //nolint:errcheck

	for _, p := range []Policy{{MFA: MFARequired}, {MFA: MFASensitive, Passkeys: true}, {MFA: MFAOptional}} {
		if err := c.SetPolicy(ctx, p); err != nil {
			t.Fatalf("set %+v: %v", p, err)
		}
		got, err := c.Policy(ctx)
		if err != nil || got != p {
			t.Fatalf("policy = %+v, %v; want %+v", got, err, p)
		}
	}
	if _, err := c.RoleHolders(ctx, MFARole); err != nil {
		t.Fatal(err)
	}
}
