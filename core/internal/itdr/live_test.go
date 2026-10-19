package itdr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/connector"
	"github.com/grantra-dev/grantra/core/internal/connector/grantralogin"
	"github.com/grantra-dev/grantra/core/internal/login"
)

// TestLiveGrantraLogin reads sign-ins from, and responds in, a real
// Keycloak that imported deploy/keycloak/grantra-realm.json, such as the
// one in docker-compose, with a test database:
//
//	GRANTRA_TEST_KEYCLOAK_URL=http://localhost:8081 GRANTRA_TEST_DATABASE_URL=... go test ./internal/itdr -run Live
//
// It fails sign-ins for luis, changes the sign-in policy and disables and
// enables luis, so never point it at a real realm.
func TestLiveGrantraLogin(t *testing.T) {
	base := os.Getenv("GRANTRA_TEST_KEYCLOAK_URL")
	if base == "" {
		t.Skip("GRANTRA_TEST_KEYCLOAK_URL is not set")
	}
	e := newEnv(t)
	ctx := context.Background()
	secrets := func(string) (string, error) { return "grantra-dev-connector-secret", nil }
	cfg := `{"url": "` + base + `", "client_secret": "env:X", "allow_http": true}`
	r, err := connector.New(grantralogin.Kind, json.RawMessage(cfg), secrets)
	if err != nil {
		t.Fatal(err)
	}
	c := r.(*grantralogin.Client)
	snap, err := c.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(snap.Accounts, func(a connector.Account) bool { return a.Username == "luis" })
	if i < 0 {
		t.Fatal("no luis in the realm")
	}
	native := snap.Accounts[i].NativeID

	e.admin("Boss")
	luis := e.identity("Luis")
	var app string
	e.tx(func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO applications (tenant_id, name, connector, config) VALUES ($1, 'Grantra Login', $2, $3) RETURNING id`,
			e.tenant, grantralogin.Kind, cfg).Scan(&app); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO accounts (tenant_id, application_id, identity_id, native_id, username) VALUES ($1, $2, $3, $4, 'luis')`,
			e.tenant, app, luis, native)
		return err
	})

	// Three wrong passwords through the token endpoint.
	for range 3 {
		resp, err := http.PostForm(base+"/realms/grantra/protocol/openid-connect/token", url.Values{
			"grant_type": {"password"}, "client_id": {"admin-cli"}, "username": {"luis"}, "password": {"wrong"}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	n, err := ReadGrantraLogin(ctx, e.pool, e.tenant, secrets)
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 {
		t.Fatalf("read %d sign-ins, want at least the 3 failures", n)
	}
	if again, err := ReadGrantraLogin(ctx, e.pool, e.tenant, secrets); err != nil || again != 0 {
		t.Errorf("second read = %d, %v; want nothing new", again, err)
	}
	if got := e.count(`SELECT count(*) FROM signin_events WHERE source = $1 AND identity_id = $2 AND outcome = 'failure'`,
		SourceGrantraLogin, luis); got < 3 {
		t.Errorf("Luis's failures = %d, want at least 3", got)
	}

	rs := Responder{Pool: e.pool, TenantID: e.tenant, Secrets: secrets}
	before, err := c.Policy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.SetPolicy(context.Background(), before); err != nil {
			t.Errorf("put the sign-in policy back: %v", err)
		}
	})
	if err := c.SetPolicy(ctx, grantralogin.Policy{MFA: grantralogin.MFAOptional}); err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Respond(ctx, "identity:boss", luis, ResponseInput{Action: RequireMFA}); !errors.As(err, new(*apperr.Conflict)) {
		t.Errorf("require MFA with an optional policy = %v, want conflict", err)
	}
	if err := c.SetPolicy(ctx, grantralogin.Policy{MFA: grantralogin.MFASensitive}); err != nil {
		t.Fatal(err)
	}
	out, err := rs.Respond(ctx, "identity:boss", luis, ResponseInput{Action: RequireMFA, Days: 1})
	if err != nil || out.Error != "" {
		t.Fatalf("require MFA = %+v, %v", out, err)
	}
	holders, err := c.RoleHolders(ctx, grantralogin.MFARole)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(holders, native) {
		t.Errorf("MFA role holders = %v, want luis", holders)
	}
	// The MFA sync keeps him there while the override lasts.
	if _, err := login.SyncMFA(ctx, e.pool, e.tenant, secrets); err != nil {
		t.Fatal(err)
	}
	if holders, _ = c.RoleHolders(ctx, grantralogin.MFARole); !slices.Contains(holders, native) {
		t.Errorf("after the MFA sync, holders = %v, want luis still", holders)
	}
	e.exec(`DELETE FROM mfa_overrides`)
	if _, err := login.SyncMFA(ctx, e.pool, e.tenant, secrets); err != nil {
		t.Fatal(err)
	}
	if holders, _ = c.RoleHolders(ctx, grantralogin.MFARole); slices.Contains(holders, native) {
		t.Errorf("with the override gone, holders = %v, want luis out", holders)
	}

	enabled := func() bool {
		snap, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range snap.Accounts {
			if a.NativeID == native {
				return a.Active
			}
		}
		t.Fatal("luis is gone")
		return false
	}
	t.Cleanup(func() { _ = c.SetActive(context.Background(), native, true) })
	out, err = rs.Respond(ctx, "identity:boss", luis, ResponseInput{Action: Suspend})
	if err != nil || out.Error != "" {
		t.Fatalf("suspend = %+v, %v", out, err)
	}
	if enabled() {
		t.Error("luis is still enabled after the suspension")
	}
	if d, _ := out.Details["disabled_accounts"].([]string); !slices.Equal(d, []string{native}) {
		t.Errorf("disabled accounts = %v", out.Details["disabled_accounts"])
	}
	out, err = rs.Respond(ctx, "identity:boss", luis, ResponseInput{Action: Reinstate})
	if err != nil || out.Error != "" {
		t.Fatalf("reinstate = %+v, %v", out, err)
	}
	if !enabled() {
		t.Error("luis is still disabled after being reinstated")
	}
	if out, err = rs.Respond(ctx, "identity:boss", luis, ResponseInput{Action: RevokeSessions}); err != nil || out.Error != "" ||
		!strings.Contains(strings.Join(mapKeys(out.Details), ","), "grantra_login_accounts") {
		t.Errorf("revoke = %+v, %v", out, err)
	}
}

func mapKeys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
