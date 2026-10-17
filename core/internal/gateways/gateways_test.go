package gateways

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	keys   vault.KeyProvider
	s      *Service
	ctx    context.Context
}

func testKey(t *testing.T) vault.KeyProvider {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	key, err := vault.NewLocalKey(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	keys := testKey(t)
	return &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "gw"), keys: keys, s: New(keys), ctx: context.Background()}
}

func (e *env) tx(fn func(pgx.Tx) error) error { return db.InTenant(e.ctx, e.pool, e.tenant, fn) }

func (e *env) must(fn func(pgx.Tx) error) {
	e.t.Helper()
	if err := e.tx(fn); err != nil {
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

func (e *env) identity(name string) string {
	return e.one(`INSERT INTO identities (tenant_id, display_name, email, kind) VALUES ($1, $2, $3, 'human') RETURNING id`,
		e.tenant, name, name+"@example.com")
}

func (e *env) vaultAccount(name, kind, secret string) string {
	var a vault.Account
	e.must(func(tx pgx.Tx) (err error) {
		a, err = vault.New(e.keys, nil).Create(e.ctx, tx, e.tenant, "identity:admin",
			vault.Input{Name: name, Username: "app_owner", Kind: kind, Secret: secret})
		return err
	})
	return a.ID
}

func (e *env) create(in Input) (Target, error) {
	var out Target
	err := e.tx(func(tx pgx.Tx) (err error) { out, err = Create(e.ctx, tx, e.tenant, "identity:admin", in); return err })
	return out, err
}

func (e *env) grant(identity, entitlement, expires string) {
	e.exec(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	        VALUES ($1, $2, $3, 'manual', 'test', now() + $4::interval)`, e.tenant, identity, entitlement, expires)
}

func ptr[T any](v T) *T { return &v }

func isInvalid(err error) bool {
	var inv *apperr.Invalid
	return errors.As(err, &inv)
}

func TestTargets(t *testing.T) {
	e := newEnv(t)
	acct := e.vaultAccount("orders owner", vault.KindPassword, "s3cret")
	key := e.vaultAccount("deploy key", vault.KindSSHKey, "-----BEGIN OPENSSH PRIVATE KEY-----\nx\n-----END OPENSSH PRIVATE KEY-----")

	pg, err := e.create(Input{Name: "orders-db", Kind: Postgres, Settings: Settings{Address: ptr("10.0.0.7"),
		Database: ptr("orders"), VaultAccountID: &acct}})
	if err != nil {
		t.Fatal(err)
	}
	if pg.Address != "10.0.0.7:5432" || pg.TLSMode != TLSVerify || pg.MaxTokenHours != 8 || pg.Risk != "high" ||
		pg.VaultAccountName != "orders owner" || len(pg.KubeGroups) != 0 || pg.EntitlementName != "Connect to the orders-db database" {
		t.Fatalf("created %+v", pg)
	}
	// Nobody holds it for good: people activate it.
	if e.one(`SELECT jit_only::text FROM entitlements WHERE id = $1`, pg.EntitlementID) != "true" {
		t.Error("gateway access is not just in time only")
	}
	my, err := e.create(Input{Name: "billing", Kind: MySQL, Risk: "critical", Settings: Settings{Address: ptr("db.internal"),
		VaultAccountID: &acct, TLSMode: ptr(TLSRequire)}})
	if err != nil || my.Address != "db.internal:3306" || my.Risk != "critical" {
		t.Fatalf("mysql = %+v %v", my, err)
	}
	kube, err := e.create(Input{Name: "prod", Kind: Kubernetes, Settings: Settings{Address: ptr("https://10.0.0.1:6443/")}})
	if err != nil || kube.Address != "https://10.0.0.1:6443" || strings.Join(kube.KubeGroups, ",") != "grantra:prod" || kube.VaultAccountID != "" {
		t.Fatalf("kubernetes = %+v %v", kube, err)
	}

	for name, in := range map[string]Input{
		"bad name":              {Name: "Orders", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct}},
		"bad kind":              {Name: "x1", Kind: "oracle", Settings: Settings{Address: ptr("h"), VaultAccountID: &acct}},
		"low risk":              {Name: "x2", Kind: Postgres, Risk: "low", Settings: Settings{Address: ptr("h"), VaultAccountID: &acct}},
		"no vault account":      {Name: "x3", Kind: Postgres, Settings: Settings{Address: ptr("h")}},
		"ssh key account":       {Name: "x4", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &key}},
		"unknown vault account": {Name: "x5", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: ptr("00000000-0000-0000-0000-000000000000")}},
		"bad vault account id":  {Name: "x6", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: ptr("nope")}},
		"bad port":              {Name: "x7", Kind: MySQL, Settings: Settings{Address: ptr("h:99999"), VaultAccountID: &acct}},
		"bad database":          {Name: "x8", Kind: Postgres, Settings: Settings{Address: ptr("h"), Database: ptr("a b"), VaultAccountID: &acct}},
		"plain http cluster":    {Name: "x9", Kind: Kubernetes, Settings: Settings{Address: ptr("http://10.0.0.1:6443")}},
		"cluster without TLS":   {Name: "x10", Kind: Kubernetes, Settings: Settings{Address: ptr("https://k"), TLSMode: ptr(TLSDisable)}},
		"system:masters":        {Name: "x11", Kind: Kubernetes, Settings: Settings{Address: ptr("https://k"), KubeGroups: []string{"system:masters"}}},
		"group twice":           {Name: "x12", Kind: Kubernetes, Settings: Settings{Address: ptr("https://k"), KubeGroups: []string{"ops", "ops"}}},
		"database groups":       {Name: "x13", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, KubeGroups: []string{"ops"}}},
		"cluster database":      {Name: "x14", Kind: Kubernetes, Settings: Settings{Address: ptr("https://k"), Database: ptr("x")}},
		"bad CA":                {Name: "x15", Kind: Kubernetes, Settings: Settings{Address: ptr("https://k"), CACertificate: ptr("not a cert")}},
		"bad TLS mode":          {Name: "x16", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, TLSMode: ptr("maybe")}},
		"too long tokens":       {Name: "x17", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, MaxTokenHours: ptr(25)}},
	} {
		if _, err := e.create(in); !isInvalid(err) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	var conflict *apperr.Conflict
	if _, err := e.create(Input{Name: "orders-db", Kind: MySQL, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct}}); !errors.As(err, &conflict) {
		t.Errorf("duplicate name: %v", err)
	}

	// Settings left out stay; the name and kind never change.
	var up Target
	e.must(func(tx pgx.Tx) (err error) {
		up, err = Update(e.ctx, tx, e.tenant, "identity:admin", kube.ID, Settings{KubeGroups: []string{" ops ", "auditors"},
			CACertificate: ptr(testCA(t)), MaxTokenHours: ptr(2)})
		return err
	})
	if up.Address != kube.Address || strings.Join(up.KubeGroups, ",") != "ops,auditors" || up.CACertificate == "" || up.MaxTokenHours != 2 {
		t.Errorf("updated %+v", up)
	}
	if p, err := up.CertPool(); err != nil || p == nil {
		t.Errorf("cert pool = %v %v", p, err)
	}
	if err := e.tx(func(tx pgx.Tx) error {
		_, err := Update(e.ctx, tx, e.tenant, "identity:admin", pg.ID, Settings{VaultAccountID: ptr("")})
		return err
	}); !isInvalid(err) {
		t.Errorf("database without its vault account: %v", err)
	}

	// The vault account going away leaves the target without credentials.
	e.must(func(tx pgx.Tx) error { return vault.Delete(e.ctx, tx, e.tenant, "identity:admin", acct) })
	var got Target
	e.must(func(tx pgx.Tx) (err error) { got, err = Get(e.ctx, tx, pg.ID); return err })
	if got.VaultAccountID != "" {
		t.Errorf("vault account still set: %+v", got)
	}
	var credErr error
	e.must(func(tx pgx.Tx) error { _, _, credErr = e.s.Credentials(e.ctx, tx, e.tenant, got); return nil })
	if credErr == nil {
		t.Error("credentials without a vault account")
	}

	// Deleting a target removes its entitlement.
	e.must(func(tx pgx.Tx) error { return Delete(e.ctx, tx, e.tenant, "identity:admin", my.ID) })
	if n := e.one(`SELECT count(*)::text FROM entitlements WHERE id = $1`, my.EntitlementID); n != "0" {
		t.Errorf("entitlement left: %s", n)
	}
}

func testCA(t *testing.T) string {
	t.Helper()
	pool := dbtest.Open(t)
	s := New(testKey(t))
	tenant := dbtest.Tenant(t, pool, "gwca")
	var ca string
	if err := db.InTenant(context.Background(), pool, tenant, func(tx pgx.Tx) (err error) {
		ca, err = s.CACertificate(context.Background(), tx, tenant)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestTokens(t *testing.T) {
	e := newEnv(t)
	acct := e.vaultAccount("orders owner", vault.KindPassword, "s3cret")
	tg, err := e.create(Input{Name: "orders-db", Kind: Postgres, Settings: Settings{Address: ptr("h"), VaultAccountID: &acct, MaxTokenHours: ptr(4)}})
	if err != nil {
		t.Fatal(err)
	}
	ana, luis := e.identity("ana"), e.identity("luis")
	issue := func(who string) (Token, error) {
		var tok Token
		err := e.tx(func(tx pgx.Tx) (err error) { tok, err = Issue(e.ctx, tx, e.tenant, who, tg.ID); return err })
		return tok, err
	}
	var conflict *apperr.Conflict
	if _, err := issue(luis); !errors.As(err, &conflict) {
		t.Fatalf("token without access: %v", err)
	}
	// An activation of one hour gives a token of one hour, not four.
	e.grant(ana, tg.EntitlementID, "1 hour")
	tok, err := issue(ana)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(tok.Token, TokenPrefix) || len(tok.Token) < 40 || time.Until(tok.ValidBefore) > time.Hour+time.Minute {
		t.Fatalf("token %+v", tok)
	}
	if n := e.one(`SELECT count(*)::text FROM gateway_tokens WHERE token_hash = $1`, []byte(tok.Token)); n != "0" {
		t.Error("the token is stored in clear")
	}
	authorize := func(token, kind, name string) (Grant, error) {
		var g Grant
		err := e.tx(func(tx pgx.Tx) (err error) { g, err = Authorize(e.ctx, tx, token, kind, name); return err })
		return g, err
	}
	g, err := authorize(tok.Token, Postgres, "orders-db")
	if err != nil || g.IdentityID != ana || g.IdentityEmail != "ana@example.com" || g.Target.ID != tg.ID || g.TokenID != tok.ID {
		t.Fatalf("authorize = %+v %v", g, err)
	}
	var denied *Denied
	for name, c := range map[string]struct{ token, kind, target string }{
		"another target": {tok.Token, Postgres, "billing"},
		"another kind":   {tok.Token, MySQL, "orders-db"},
		"unknown":        {TokenPrefix + "nope", Postgres, "orders-db"},
		"no prefix":      {strings.TrimPrefix(tok.Token, TokenPrefix), Postgres, "orders-db"},
	} {
		if _, err := authorize(c.token, c.kind, c.target); !errors.As(err, &denied) {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A session runs while the token and the access last.
	var session string
	e.must(func(tx pgx.Tx) (err error) {
		session, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: tg.ID, TargetName: tg.Name, Kind: Postgres, IdentityID: ana,
			TokenID: tok.ID, ClientAddress: "10.1.1.1:5555", Gateway: "gw1", Database: "orders", Account: "app_owner"}, acct)
		return err
	})
	heartbeat := func() string {
		var r string
		e.must(func(tx pgx.Tx) (err error) { r, err = Heartbeat(e.ctx, tx, session); return err })
		return r
	}
	if r := heartbeat(); r != "" {
		t.Fatalf("heartbeat = %q", r)
	}
	e.must(func(tx pgx.Tx) error {
		return AddEvents(e.ctx, tx, e.tenant, session, []Event{{At: time.Now(), Kind: EventQuery, Text: "SELECT 1"},
			{At: time.Now(), Kind: EventQuery, Text: "DELETE FROM orders\x00 WHERE id = 7"}})
	})
	var found []Event
	e.must(func(tx pgx.Tx) (err error) { found, err = SearchEvents(e.ctx, tx, "delete from", "", 10); return err })
	if len(found) != 1 || found[0].Text != "DELETE FROM orders WHERE id = 7" || found[0].IdentityName != "ana" {
		t.Errorf("search = %+v", found)
	}
	var s Session
	e.must(func(tx pgx.Tx) (err error) { s, err = GetSession(e.ctx, tx, session); return err })
	if s.Events != 2 || s.Database != "orders" || s.Account != "app_owner" {
		t.Errorf("session = %+v", s)
	}
	// The person leaving ends the session and refuses the token.
	e.exec(`UPDATE identities SET status = 'terminated' WHERE id = $1`, ana)
	if r := heartbeat(); r != EndAccessEnded {
		t.Errorf("heartbeat after leaving = %q", r)
	}
	if _, err := authorize(tok.Token, Postgres, "orders-db"); !errors.Is(err, ErrNoAccess) {
		t.Errorf("authorize after leaving = %v", err)
	}
	e.exec(`UPDATE identities SET status = 'active' WHERE id = $1`, ana)

	// An expired token ends its session too.
	e.exec(`UPDATE gateway_tokens SET valid_before = now() - interval '1 second' WHERE id = $1`, tok.ID)
	if r := heartbeat(); r != EndAccessEnded {
		t.Errorf("heartbeat after the token expired = %q", r)
	}
	if _, err := authorize(tok.Token, Postgres, "orders-db"); !errors.Is(err, ErrBadToken) {
		t.Errorf("expired token = %v", err)
	}

	// An admin ending a session revokes its token.
	tok2, err := issue(ana)
	if err != nil {
		t.Fatal(err)
	}
	e.must(func(tx pgx.Tx) (err error) {
		session, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: tg.ID, TargetName: tg.Name, Kind: Postgres, IdentityID: ana,
			TokenID: tok2.ID, ClientAddress: "10.1.1.1:5555", Gateway: "gw1"}, acct)
		return err
	})
	e.must(func(tx pgx.Tx) error { return Terminate(e.ctx, tx, e.tenant, "identity:admin", session) })
	if r := heartbeat(); r != EndTerminated {
		t.Errorf("heartbeat after terminate = %q", r)
	}
	if _, err := authorize(tok2.Token, Postgres, "orders-db"); !errors.Is(err, ErrBadToken) {
		t.Errorf("token of a terminated session = %v", err)
	}
	e.must(func(tx pgx.Tx) error { return EndSession(e.ctx, tx, e.tenant, session, EndTerminated, "") })
	if err := e.tx(func(tx pgx.Tx) error { return Terminate(e.ctx, tx, e.tenant, "identity:admin", session) }); !errors.As(err, &conflict) {
		t.Errorf("terminate an ended session: %v", err)
	}

	// Sessions their gateway stopped touching are swept as lost.
	tok3, err := issue(ana)
	if err != nil {
		t.Fatal(err)
	}
	e.must(func(tx pgx.Tx) (err error) {
		session, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: tg.ID, TargetName: tg.Name, Kind: Postgres, IdentityID: ana,
			TokenID: tok3.ID, ClientAddress: "10.1.1.1:5555", Gateway: "gw1"}, acct)
		return err
	})
	e.exec(`UPDATE gateway_sessions SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, session)
	var n int
	e.must(func(tx pgx.Tx) (err error) { n, err = EndLostSessions(e.ctx, tx, e.tenant); return err })
	if n != 1 || heartbeat() != EndLost {
		t.Errorf("lost sessions = %d", n)
	}
	var list []Session
	e.must(func(tx pgx.Tx) (err error) {
		list, err = ListSessions(e.ctx, tx, SessionFilter{TargetID: tg.ID, Limit: 10})
		return err
	})
	if len(list) != 3 {
		t.Errorf("sessions = %d", len(list))
	}

	// Deleting the target refuses its tokens and ends its sessions.
	tok4, err := issue(ana)
	if err != nil {
		t.Fatal(err)
	}
	e.must(func(tx pgx.Tx) error { return Delete(e.ctx, tx, e.tenant, "identity:admin", tg.ID) })
	if _, err := authorize(tok4.Token, Postgres, "orders-db"); !errors.Is(err, ErrBadToken) {
		t.Errorf("token of a deleted target = %v", err)
	}
	if r := heartbeat(); r != EndLost {
		t.Errorf("heartbeat of a session of a deleted target = %q", r)
	}
}

func TestTLS(t *testing.T) {
	e := newEnv(t)
	var caPEM string
	var cert1, cert2 [][]byte
	e.must(func(tx pgx.Tx) (err error) {
		if caPEM, err = e.s.CACertificate(e.ctx, tx, e.tenant); err != nil {
			return err
		}
		c, err := e.s.ServingCertificate(e.ctx, tx, e.tenant, []string{"gateway.example.com", "10.0.0.9"})
		if err != nil {
			return err
		}
		cert1 = c.Certificate
		c, err = e.s.ServingCertificate(e.ctx, tx, e.tenant, []string{"gateway.example.com"})
		cert2 = c.Certificate
		return err
	})
	block, _ := pem.Decode([]byte(caPEM))
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA {
		t.Fatalf("CA = %v %v", ca, err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaf, err := x509.ParseCertificate(cert1[0])
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gateway.example.com", "10.0.0.9"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots}); err != nil {
			t.Errorf("verify %s: %v", name, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "other.example.com", Roots: roots}); err == nil {
		t.Error("certificate valid for another name")
	}
	if time.Until(leaf.NotAfter) > ServingCertDays*24*time.Hour {
		t.Errorf("certificate lasts until %s", leaf.NotAfter)
	}
	// Both certificates chain to the one CA, created once.
	leaf2, _ := x509.ParseCertificate(cert2[0])
	if _, err := leaf2.Verify(x509.VerifyOptions{DNSName: "gateway.example.com", Roots: roots}); err != nil {
		t.Error(err)
	}
	if n := e.one(`SELECT count(*)::text FROM gateway_tls_cas`); n != "1" {
		t.Errorf("CAs = %s", n)
	}
	// The CA key is sealed: another vault key cannot use it.
	other := New(testKey(t))
	if err := e.tx(func(tx pgx.Tx) error {
		_, err := other.ServingCertificate(e.ctx, tx, e.tenant, []string{"x"})
		return err
	}); err == nil {
		t.Error("signed with another vault key")
	}
	// Re-wrapped for a new key, it opens with it.
	two := twoKeys{cur: testKey(t), old: e.keys}
	var n int
	e.must(func(tx pgx.Tx) (err error) { n, err = Rewrap(e.ctx, tx, e.tenant, two); return err })
	if n != 1 {
		t.Errorf("rewrapped %d", n)
	}
	e.must(func(tx pgx.Tx) error {
		_, err := New(two.cur).ServingCertificate(e.ctx, tx, e.tenant, []string{"x"})
		return err
	})
	if disabled := New(nil); disabled.Enabled() {
		t.Error("enabled without keys")
	}
}

// twoKeys wraps with cur and unwraps with whichever key wrapped.
type twoKeys struct{ cur, old vault.KeyProvider }

func (k twoKeys) KeyID() string                            { return k.cur.KeyID() }
func (k twoKeys) Wrap(dataKey, aad []byte) ([]byte, error) { return k.cur.Wrap(dataKey, aad) }
func (k twoKeys) Unwrap(keyID string, wrapped, aad []byte) ([]byte, error) {
	if keyID == k.old.KeyID() {
		return k.old.Unwrap(keyID, wrapped, aad)
	}
	return k.cur.Unwrap(keyID, wrapped, aad)
}

func TestCleanTextAndKubeconfig(t *testing.T) {
	long := strings.Repeat("é", MaxEventText)
	if got := CleanText(long); len(got) > MaxEventText || !strings.HasSuffix(got, "é") {
		t.Errorf("cut %d bytes", len(got))
	}
	if got := CleanText("a\x00b\xffc"); got != "abc" {
		t.Errorf("clean = %q", got)
	}
	k := Kubeconfig("gw.example.com:8443", "prod", "grt_abc", "CA")
	for _, want := range []string{`server: "https://gw.example.com:8443/kubernetes/prod"`, `token: "grt_abc"`,
		"certificate-authority-data: " + base64.StdEncoding.EncodeToString([]byte("CA")), "current-context: grantra-prod"} {
		if !strings.Contains(k, want) {
			t.Errorf("kubeconfig lacks %q:\n%s", want, k)
		}
	}
	if strings.Contains(Kubeconfig("gw:8443", "prod", "grt_abc", ""), "certificate-authority-data") {
		t.Error("CA data without a CA")
	}
}
