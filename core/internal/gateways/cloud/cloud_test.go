package cloud

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

// fakeClouds answers like AWS STS and sign-in, Google's token endpoint and
// Resource Manager, and Microsoft Entra, Graph and Azure Resource Manager.
type fakeClouds struct {
	*httptest.Server
	t  *testing.T
	mu sync.Mutex

	awsDeny  bool
	assumed  url.Values
	sessions []map[string]string

	policy      map[string]any
	setPolicies int
	conflicts   int // setIamPolicy answers 409 this many times first

	assignments map[string]map[string]any
	deleteFails bool
	scopes      []string
}

const (
	subscription = "/subscriptions/0b1f6471-1bf0-4dda-aec3-111122223333"
	readerRole   = subscription + "/providers/Microsoft.Authorization/roleDefinitions/acdd72a7-3385-48ef-bd42-f606fba81ae7"
	anaOID       = "6f1c7d2e-0000-4000-8000-00000000a0a0"
)

func newFakeClouds(t *testing.T) *fakeClouds {
	f := &fakeClouds{t: t, assignments: map[string]map[string]any{},
		policy: map[string]any{"version": 1, "etag": "BwY=", "bindings": []any{map[string]any{"role": "roles/owner", "members": []any{"user:boss@example.com"}}},
			"auditConfigs": []any{map[string]any{"service": "allServices"}}}}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeClouds) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	w.Header().Set("Content-Type", "application/json")
	bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	switch {
	case r.URL.Path == "/sts/":
		form, _ := url.ParseQuery(string(body))
		if form.Get("Action") != "AssumeRole" || !strings.Contains(r.Header.Get("Authorization"), "Credential=AKIAGRANTRA/") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if f.awsDeny {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `<ErrorResponse><Error><Type>Sender</Type><Code>AccessDenied</Code><Message>User is not authorized to perform sts:AssumeRole</Message></Error><RequestId>1</RequestId></ErrorResponse>`)
			return
		}
		f.assumed = form
		fmt.Fprintf(w, `<AssumeRoleResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><AssumeRoleResult><Credentials>
			<AccessKeyId>ASIATEMP</AccessKeyId><SecretAccessKey>temp-secret</SecretAccessKey><SessionToken>temp-token</SessionToken>
			<Expiration>%s</Expiration></Credentials><AssumedRoleUser><Arn>arn:aws:sts::123456789012:assumed-role/ReadOnly/%s</Arn>
			<AssumedRoleId>AROA:x</AssumedRoleId></AssumedRoleUser></AssumeRoleResult><ResponseMetadata><RequestId>1</RequestId></ResponseMetadata></AssumeRoleResponse>`,
			time.Now().Add(time.Hour).UTC().Format(time.RFC3339), form.Get("RoleSessionName"))
	case r.URL.Path == "/federation":
		form, _ := url.ParseQuery(string(body))
		var session map[string]string
		json.Unmarshal([]byte(form.Get("Session")), &session) //nolint:errcheck
		f.sessions = append(f.sessions, session)
		fmt.Fprint(w, `{"SigninToken":"signin-token"}`)

	case r.URL.Path == "/google/token":
		form, _ := url.ParseQuery(string(body))
		if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" || form.Get("assertion") == "" {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, `{"access_token":"google-token","token_type":"Bearer","expires_in":3600}`)
	case strings.HasPrefix(r.URL.Path, "/v1/projects/shop-prod-123:"):
		if bearer != "google-token" {
			http.Error(w, `{"error":{"status":"UNAUTHENTICATED"}}`, http.StatusUnauthorized)
			return
		}
		var req map[string]any
		json.Unmarshal(body, &req) //nolint:errcheck
		if strings.HasSuffix(r.URL.Path, ":getIamPolicy") {
			if fmt.Sprint(req["options"]) != "map[requestedPolicyVersion:3]" {
				f.t.Errorf("getIamPolicy %s", body)
			}
			json.NewEncoder(w).Encode(f.policy) //nolint:errcheck
			return
		}
		if f.conflicts > 0 {
			f.conflicts--
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"error":{"status":"ABORTED","message":"etag mismatch"}}`)
			return
		}
		f.setPolicies++
		f.policy = req["policy"].(map[string]any)
		json.NewEncoder(w).Encode(f.policy) //nolint:errcheck

	case r.URL.Path == "/login/contoso.example/oauth2/v2.0/token":
		form, _ := url.ParseQuery(string(body))
		if form.Get("client_id") != "app-id" || form.Get("client_secret") != "app-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":"invalid_client","error_description":"bad secret"}`)
			return
		}
		f.scopes = append(f.scopes, form.Get("scope"))
		fmt.Fprintf(w, `{"access_token":"azure:%s","token_type":"Bearer","expires_in":3600}`, form.Get("scope"))
	case strings.HasPrefix(r.URL.Path, "/graph/v1.0/users"):
		if bearer != "azure:https://graph.microsoft.com/.default" {
			http.Error(w, `{"error":{"code":"InvalidAuthenticationToken"}}`, http.StatusUnauthorized)
			return
		}
		// Ana's user principal name is not her email: she is found by mail.
		if r.URL.Path == "/graph/v1.0/users" && r.URL.Query().Get("$filter") == "mail eq 'ana@example.com'" {
			fmt.Fprintf(w, `{"value":[{"id":%q}]}`, anaOID)
			return
		}
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"Request_ResourceNotFound","message":"no such user"}}`)
	case strings.HasPrefix(r.URL.Path, "/arm"+subscription):
		if bearer != "azure:https://management.azure.com/.default" || r.URL.Query().Get("api-version") != azureAPIVersion {
			http.Error(w, `{"error":{"code":"AuthenticationFailed"}}`, http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/arm")
		switch {
		case strings.HasSuffix(path, "/roleDefinitions") && r.URL.Query().Get("$filter") == "roleName eq 'Reader'":
			fmt.Fprintf(w, `{"value":[{"id":%q}]}`, readerRole)
		case strings.Contains(path, "/roleAssignments/") && r.Method == http.MethodPut:
			var req map[string]any
			json.Unmarshal(body, &req) //nolint:errcheck
			f.assignments[path] = req["properties"].(map[string]any)
			w.WriteHeader(http.StatusCreated)
			fmt.Fprint(w, `{}`)
		case strings.Contains(path, "/roleAssignments/") && r.Method == http.MethodDelete:
			if f.deleteFails {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, `{"error":{"code":"InternalServerError","message":"try later"}}`)
				return
			}
			if _, ok := f.assignments[path]; !ok {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `{"error":{"code":"RoleAssignmentNotFound"}}`)
				return
			}
			delete(f.assignments, path)
			fmt.Fprint(w, `{}`)
		default:
			http.Error(w, `{"error":{"code":"NotFound"}}`, http.StatusNotFound)
		}
	default:
		f.t.Errorf("unexpected %s %s", r.Method, r.URL)
		http.NotFound(w, r)
	}
}

func (f *fakeClouds) bindings() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, b := range f.policy["bindings"].([]any) {
		out = append(out, b.(map[string]any))
	}
	return out
}

type env struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	tenant string
	keys   vault.KeyProvider
	c      *Consoles
	clouds *fakeClouds
}

func newEnv(t *testing.T) *env {
	pool := dbtest.Open(t)
	k := make([]byte, 32)
	rand.Read(k) //nolint:errcheck
	keys, err := vault.NewLocalKey(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeClouds(t)
	tenant := dbtest.Tenant(t, pool, "cloud")
	return &env{t: t, ctx: context.Background(), pool: pool, tenant: tenant, keys: keys, clouds: f,
		c: &Consoles{Pool: pool, TenantID: tenant, Gateways: gateways.New(keys), Issuer: "https://grantra.example.com",
			Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
			Endpoints: Endpoints{AWSSTS: f.URL + "/sts/", AWSSignin: f.URL + "/federation", AzureLogin: f.URL + "/login",
				AzureManagement: f.URL + "/arm", MicrosoftGraph: f.URL + "/graph", GoogleResourceMgr: f.URL, NoDefaultCredentials: true}}}
}

func (e *env) must(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := db.InTenant(e.ctx, e.pool, e.tenant, fn); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) one(sql string, args ...any) (out string) {
	e.t.Helper()
	e.must(func(tx pgx.Tx) error { return tx.QueryRow(e.ctx, sql, args...).Scan(&out) })
	return out
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.must(func(tx pgx.Tx) error { _, err := tx.Exec(e.ctx, sql, args...); return err })
}

func (e *env) account(name, username, secret string) string {
	var a vault.Account
	e.must(func(tx pgx.Tx) (err error) {
		a, err = vault.New(e.keys, nil).Create(e.ctx, tx, e.tenant, "identity:admin",
			vault.Input{Name: name, Username: username, Kind: vault.KindPassword, Secret: secret})
		return err
	})
	return a.ID
}

func (e *env) target(in gateways.Input) gateways.Target {
	e.t.Helper()
	var out gateways.Target
	e.must(func(tx pgx.Tx) (err error) {
		out, err = gateways.Create(e.ctx, tx, e.tenant, "identity:admin", in)
		return err
	})
	return out
}

func (e *env) grant(identity string, t gateways.Target, hours int) string {
	return e.one(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	              VALUES ($1, $2, $3, 'manual', 'test', now() + make_interval(hours => $4)) RETURNING id`, e.tenant, identity, t.EntitlementID, hours)
}

func (e *env) session(id string) gateways.Session {
	e.t.Helper()
	var s gateways.Session
	e.must(func(tx pgx.Tx) (err error) { s, err = gateways.GetSession(e.ctx, tx, id); return err })
	return s
}

func (e *env) endSessions() int {
	e.t.Helper()
	n, err := e.c.EndSessions(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func serviceAccountKey(t *testing.T, tokenURI string) string {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	b, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "shop-prod-123", "private_key_id": "1",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "grantra@shop-prod-123.iam.gserviceaccount.com", "token_uri": tokenURI})
	return string(b)
}

func ptr[T any](v T) *T { return &v }

func TestAWS(t *testing.T) {
	e := newEnv(t)
	acct := e.account("aws broker", "AKIAGRANTRA", "long-term-secret")
	tg := e.target(gateways.Input{Name: "prod-aws", Kind: gateways.AWS, Settings: gateways.Settings{
		Address: ptr("arn:aws:iam::123456789012:role/ReadOnly"), VaultAccountID: &acct, MaxTokenHours: ptr(2)}})
	ana := e.one(`INSERT INTO identities (tenant_id, display_name, email, kind) VALUES ($1, 'Ana', 'ana@example.com', 'human') RETURNING id`, e.tenant)
	assignment := e.grant(ana, tg, 1)

	out, err := e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	// The role session lasts as long as the access, and is named after Ana.
	secs := e.clouds.assumed.Get("DurationSeconds")
	if e.clouds.assumed.Get("RoleArn") != tg.Address || e.clouds.assumed.Get("RoleSessionName") != "ana@example.com" ||
		secs < "3590" || secs > "3600" {
		t.Errorf("assumed %v", e.clouds.assumed)
	}
	u, _ := url.Parse(out.URL)
	if !strings.HasPrefix(out.URL, e.clouds.URL+"/federation?") || u.Query().Get("Action") != "login" || u.Query().Get("SigninToken") != "signin-token" ||
		u.Query().Get("Destination") != "https://console.aws.amazon.com/" || u.Query().Get("Issuer") != "https://grantra.example.com" {
		t.Errorf("url = %s", out.URL)
	}
	if len(e.clouds.sessions) != 1 || e.clouds.sessions[0]["sessionId"] != "ASIATEMP" || e.clouds.sessions[0]["sessionToken"] != "temp-token" {
		t.Errorf("sign-in got %v", e.clouds.sessions)
	}
	if out.Credentials == nil || out.Credentials.AccessKeyID != "ASIATEMP" || out.Credentials.SecretAccessKey != "temp-secret" {
		t.Errorf("credentials = %+v", out.Credentials)
	}
	s := e.session(out.SessionID)
	if s.Kind != gateways.AWS || s.Account != "arn:aws:sts::123456789012:assumed-role/ReadOnly/ana@example.com" || s.EndedAt != nil ||
		!s.ValidBefore.Equal(out.ValidBefore) || s.Gateway != "console" {
		t.Errorf("session = %+v", s)
	}

	// Losing the access does not end what AWS issued: the session runs until
	// its credentials expire.
	e.exec(`DELETE FROM access_assignments WHERE id = $1`, assignment)
	if n := e.endSessions(); n != 0 {
		t.Errorf("%d ended early", n)
	}
	e.exec(`UPDATE gateway_tokens SET valid_before = now() - interval '1 second' WHERE id = (SELECT token_id FROM gateway_sessions WHERE id = $1)`, out.SessionID)
	if n := e.endSessions(); n != 1 {
		t.Errorf("%d ended", n)
	}
	if s := e.session(out.SessionID); s.EndReason != gateways.EndAccessEnded {
		t.Errorf("ended %+v", s)
	}

	// Without the access there is nothing to open.
	if _, err := e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5"); err == nil {
		t.Error("opened without access")
	}
	// AWS sessions last 15 minutes at least: access ending sooner is too
	// short.
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'manual', 'test', now() + interval '10 minutes')`, e.tenant, ana, tg.EntitlementID)
	_, err = e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5")
	var cf *apperr.Conflict
	if !errors.As(err, &cf) || !strings.Contains(err.Error(), "at least 15 minutes") {
		t.Errorf("short access: %v", err)
	}
	// AWS refusing fails the session and says why.
	e.exec(`UPDATE access_assignments SET expires_at = now() + interval '1 hour' WHERE identity_id = $1`, ana)
	e.clouds.awsDeny = true
	_, err = e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5")
	if !errors.As(err, &cf) || !strings.Contains(err.Error(), "AccessDenied: User is not authorized") {
		t.Fatalf("denied: %v", err)
	}
	var failed gateways.Session
	e.must(func(tx pgx.Tx) error {
		list, err := gateways.ListSessions(e.ctx, tx, gateways.SessionFilter{TargetID: tg.ID, Limit: 1})
		failed = list[0]
		return err
	})
	if failed.EndReason != gateways.EndFailed || !strings.Contains(failed.Error, "AccessDenied") {
		t.Errorf("failed session = %+v", failed)
	}
	if e.one(`SELECT (revoked_at IS NOT NULL)::text FROM gateway_tokens WHERE id = $1`, failed.TokenID) != "true" {
		t.Error("the failed session's token still works")
	}
}

func TestGoogleCloud(t *testing.T) {
	e := newEnv(t)
	acct := e.account("gcp broker", "grantra@shop-prod-123.iam.gserviceaccount.com", serviceAccountKey(t, e.clouds.URL+"/google/token"))
	tg := e.target(gateways.Input{Name: "prod-gcp", Kind: gateways.GCP, Settings: gateways.Settings{
		Address: ptr("shop-prod-123"), CloudRole: ptr("roles/compute.viewer"), VaultAccountID: &acct}})
	ana := e.one(`INSERT INTO identities (tenant_id, display_name, email, kind) VALUES ($1, 'Ana', 'ana@example.com', 'human') RETURNING id`, e.tenant)
	assignment := e.grant(ana, tg, 2)

	// Another change to the policy races with Grantra's: it tries again.
	e.clouds.conflicts = 1
	out, err := e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://console.cloud.google.com/home/dashboard?authuser=ana%40example.com&project=shop-prod-123" || out.Credentials != nil {
		t.Errorf("opened %+v", out)
	}
	b := e.clouds.bindings()
	if len(b) != 2 || fmt.Sprint(b[0]["members"]) != "[user:boss@example.com]" {
		t.Fatalf("bindings = %v", b)
	}
	cond := b[1]["condition"].(map[string]any)
	end := out.ValidBefore.UTC().Format(time.RFC3339)
	if b[1]["role"] != "roles/compute.viewer" || fmt.Sprint(b[1]["members"]) != "[user:ana@example.com]" ||
		cond["expression"] != `request.time < timestamp("`+end+`")` || !strings.HasPrefix(cond["title"].(string), "grantra-") {
		t.Errorf("binding = %v", b[1])
	}
	// The rest of the policy goes back untouched, as version 3.
	e.clouds.mu.Lock()
	if e.clouds.policy["auditConfigs"] == nil || fmt.Sprint(e.clouds.policy["version"]) != "3" || e.clouds.policy["etag"] != "BwY=" {
		t.Errorf("policy = %v", e.clouds.policy)
	}
	e.clouds.mu.Unlock()
	s := e.session(out.SessionID)
	if s.Account != "user:ana@example.com" || !strings.Contains(s.CloudGrant, cond["title"].(string)) {
		t.Errorf("session = %+v", s)
	}

	// Opening again while it runs signs in to the same session.
	again, err := e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5")
	if err != nil || again.SessionID != out.SessionID || len(e.clouds.bindings()) != 2 {
		t.Errorf("again = %+v %v", again, err)
	}

	// While she holds the access it stays; when she loses it, the binding
	// goes and the session ends.
	if n := e.endSessions(); n != 0 {
		t.Errorf("%d ended", n)
	}
	e.exec(`DELETE FROM access_assignments WHERE id = $1`, assignment)
	if n := e.endSessions(); n != 1 {
		t.Errorf("%d ended", n)
	}
	if b := e.clouds.bindings(); len(b) != 1 || b[0]["role"] != "roles/owner" {
		t.Errorf("after the end: %v", b)
	}
	if s := e.session(out.SessionID); s.EndReason != gateways.EndAccessEnded {
		t.Errorf("ended %+v", s)
	}

	// An admin ending a session takes the binding back too.
	e.grant(ana, tg, 2)
	out, err = e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	e.must(func(tx pgx.Tx) error { return gateways.Terminate(e.ctx, tx, e.tenant, "identity:admin", out.SessionID) })
	// Until it has ended, she waits: a new session would share its grant.
	var cf *apperr.Conflict
	if _, err := e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5"); !errors.As(err, &cf) || !strings.Contains(err.Error(), "still ending") {
		t.Errorf("open while ending = %v", err)
	}
	if n := e.endSessions(); n != 1 || len(e.clouds.bindings()) != 1 {
		t.Errorf("%d ended, bindings %v", n, e.clouds.bindings())
	}
	if s := e.session(out.SessionID); s.EndReason != gateways.EndTerminated {
		t.Errorf("terminated %+v", s)
	}
}

func TestAzure(t *testing.T) {
	e := newEnv(t)
	acct := e.account("azure app", "app-id", "app-secret")
	tg := e.target(gateways.Input{Name: "prod-azure", Kind: gateways.Azure, Settings: gateways.Settings{
		Address: ptr(subscription), CloudRole: ptr("Reader"), CloudDirectory: ptr("contoso.example"), VaultAccountID: &acct}})
	ana := e.one(`INSERT INTO identities (tenant_id, display_name, email, kind) VALUES ($1, 'Ana', 'ana@example.com', 'human') RETURNING id`, e.tenant)
	assignment := e.grant(ana, tg, 2)

	out, err := e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	if out.URL != "https://portal.azure.com/#@contoso.example/resource"+subscription {
		t.Errorf("url = %s", out.URL)
	}
	e.clouds.mu.Lock()
	if len(e.clouds.assignments) != 1 {
		t.Fatalf("assignments = %v", e.clouds.assignments)
	}
	var path string
	for p, a := range e.clouds.assignments {
		path = p
		if a["principalId"] != anaOID || a["roleDefinitionId"] != readerRole || a["principalType"] != "User" ||
			!strings.Contains(a["description"].(string), "Ana until") {
			t.Errorf("assignment = %v", a)
		}
	}
	e.clouds.mu.Unlock()
	name := path[strings.LastIndex(path, "/")+1:]
	if len(name) != 36 || strings.Count(name, "-") != 4 {
		t.Errorf("assignment name %s", name)
	}
	if s := e.session(out.SessionID); s.Account != anaOID || !strings.Contains(s.CloudGrant, path) {
		t.Errorf("session = %+v", s)
	}

	// Taking the assignment back fails: the session stays until it works.
	e.exec(`DELETE FROM access_assignments WHERE id = $1`, assignment)
	e.clouds.deleteFails = true
	if _, err := e.c.EndSessions(e.ctx); err == nil || !strings.Contains(err.Error(), "try later") {
		t.Errorf("failing end = %v", err)
	}
	if s := e.session(out.SessionID); s.EndedAt != nil {
		t.Errorf("ended while the role is still assigned: %+v", s)
	}
	e.clouds.deleteFails = false
	if n := e.endSessions(); n != 1 || len(e.clouds.assignments) != 0 {
		t.Errorf("%d ended, assignments %v", n, e.clouds.assignments)
	}

	// A wrong secret is Entra's refusal, shown as such.
	e.must(func(tx pgx.Tx) error {
		return vault.New(e.keys, nil).SetSecret(e.ctx, tx, e.tenant, "identity:admin", acct, "wrong")
	})
	e.grant(ana, tg, 2)
	_, err = e.c.Open(e.ctx, ana, tg.ID, "1.2.3.4:5")
	if err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Errorf("wrong secret: %v", err)
	}
	// Nothing was granted: the session ended right away.
	var list []gateways.Session
	e.must(func(tx pgx.Tx) (err error) {
		list, err = gateways.ListSessions(e.ctx, tx, gateways.SessionFilter{TargetID: tg.ID, Active: true, Limit: 5})
		return err
	})
	if len(list) != 0 {
		t.Errorf("still running: %+v", list)
	}
}
