package sshaccess

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

type env struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
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

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	return &env{t: t, pool: pool, tenant: dbtest.Tenant(t, pool, "ssh"), s: New(testKey(t)), ctx: context.Background()}
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
	return e.one(`INSERT INTO identities (tenant_id, display_name, kind) VALUES ($1, $2, 'human') RETURNING id`, e.tenant, name)
}

func (e *env) create(in Input) (Target, error) {
	var out Target
	err := e.tx(func(tx pgx.Tx) (err error) { out, err = Create(e.ctx, tx, e.tenant, "identity:admin", in); return err })
	return out, err
}

func (e *env) target(in Input) Target {
	e.t.Helper()
	out, err := e.create(in)
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

// grant gives access to an entitlement until expires.
func (e *env) grant(identity, entitlement string, expires time.Time) string {
	return e.one(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	              VALUES ($1, $2, $3, 'manual', 'test', $4) RETURNING id`, e.tenant, identity, entitlement, expires)
}

func (e *env) issue(identity, target, key string) (Certificate, error) {
	var c Certificate
	err := e.tx(func(tx pgx.Tx) (err error) {
		c, err = e.s.Issue(e.ctx, tx, e.tenant, identity, target, key)
		return err
	})
	return c, err
}

func userKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s, string(ssh.MarshalAuthorizedKey(s.PublicKey()))
}

func isInvalid(err error) bool  { return errors.As(err, new(*apperr.Invalid)) }
func isConflict(err error) bool { return errors.As(err, new(*apperr.Conflict)) }

func TestCertificateAuthority(t *testing.T) {
	e := newEnv(t)
	var first, second string
	e.must(func(tx pgx.Tx) (err error) { first, err = e.s.PublicCA(e.ctx, tx, e.tenant, PeopleCA); return err })
	e.must(func(tx pgx.Tx) (err error) { second, err = e.s.PublicCA(e.ctx, tx, e.tenant, PeopleCA); return err })
	if first != second || !strings.HasPrefix(first, "ssh-ed25519 ") {
		t.Fatalf("CA = %q then %q, want one stable ed25519 key", first, second)
	}
	// The gateway has a CA of its own.
	var gw string
	e.must(func(tx pgx.Tx) (err error) { gw, err = e.s.PublicCA(e.ctx, tx, e.tenant, GatewayCA); return err })
	if gw == first || !strings.HasPrefix(gw, "ssh-ed25519 ") {
		t.Errorf("gateway CA = %q, people CA %q", gw, first)
	}
	// The private key is sealed: another master key cannot open it.
	other := New(testKey(t))
	err := e.tx(func(tx pgx.Tx) error { _, err := other.signer(e.ctx, tx, e.tenant, PeopleCA); return err })
	if err == nil {
		t.Error("a different vault key opened the CA")
	}
	// Moving to another master key: grantra vault rewrap re-wraps both CAs,
	// which then open with the new key alone and stay the same CAs.
	next := testKey(t)
	var n int
	e.must(func(tx pgx.Tx) (err error) { n, err = Rewrap(e.ctx, tx, e.tenant, twoKeys{next, e.s.keys}); return err })
	if n != 2 {
		t.Fatalf("rewrapped %d CAs, want 2", n)
	}
	moved := New(next)
	var again string
	e.must(func(tx pgx.Tx) (err error) { again, err = moved.PublicCA(e.ctx, tx, e.tenant, PeopleCA); return err })
	e.must(func(tx pgx.Tx) error { _, err := moved.signer(e.ctx, tx, e.tenant, GatewayCA); return err })
	if again != first {
		t.Errorf("CA changed on rewrap: %q, was %q", again, first)
	}
	if err := e.tx(func(tx pgx.Tx) error { _, err := e.s.signer(e.ctx, tx, e.tenant, PeopleCA); return err }); err == nil {
		t.Error("the old key still opens the CA after rewrap")
	}

	// Without a key nothing is signed.
	off := New(nil)
	err = e.tx(func(tx pgx.Tx) error { _, err := off.Issue(e.ctx, tx, e.tenant, "x", "y", "z"); return err })
	if !errors.Is(err, ErrDisabled) {
		t.Errorf("issue without a key = %v", err)
	}
}

func TestTargetsAndCertificates(t *testing.T) {
	e := newEnv(t)
	ana, luis := e.identity("Ana"), e.identity("Luis")

	for _, in := range []Input{
		{Name: "Prod DB", Login: "root", Settings: Settings{Address: "db"}},
		{Name: "prod-db", Login: "root user", Settings: Settings{Address: "db"}},
		{Name: "prod-db", Login: "root", Settings: Settings{Address: ""}},
		{Name: "prod-db", Login: "root", Settings: Settings{Address: "db:99999"}},
		{Name: "prod-db", Login: "root", Risk: "low", Settings: Settings{Address: "db"}},
		{Name: "prod-db", Login: "root", Settings: Settings{Address: "db", MaxCertificateHours: 25}},
	} {
		if _, err := e.create(in); !isInvalid(err) {
			t.Errorf("create %+v = %v, want invalid", in, err)
		}
	}
	tg := e.target(Input{Name: "prod-db", Login: "postgres", Settings: Settings{Address: "10.0.0.5"}})
	if tg.Address != "10.0.0.5:22" || !tg.ViaGateway || tg.MaxCertificateHours != 8 || tg.Risk != "high" ||
		tg.EntitlementName != "Log in to prod-db as postgres" {
		t.Fatalf("target = %+v", tg)
	}
	if _, err := e.create(Input{Name: "prod-db", Login: "root", Settings: Settings{Address: "x"}}); !isConflict(err) {
		t.Errorf("duplicate name = %v", err)
	}
	if jit := e.one(`SELECT jit_only::text FROM entitlements WHERE id = $1`, tg.EntitlementID); jit != "true" {
		t.Errorf("SSH access is just-in-time only by default, got %s", jit)
	}
	if app := e.one(`SELECT a.name FROM applications a JOIN entitlements e ON e.application_id = a.id WHERE e.id = $1`, tg.EntitlementID); app != ApplicationName {
		t.Errorf("application = %q", app)
	}

	signer, pub := userKey(t)
	if _, err := e.issue(ana, tg.ID, pub); !isConflict(err) {
		t.Errorf("certificate without access = %v, want conflict", err)
	}

	// Access for 2 hours: the certificate ends with it, not after 8 hours.
	until := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	e.grant(ana, tg.EntitlementID, until)
	c, err := e.issue(ana, tg.ID, pub)
	if err != nil {
		t.Fatal(err)
	}
	if !c.ValidBefore.Equal(until) || c.Principal != "grantra:prod-db" || !c.ViaGateway {
		t.Errorf("certificate = %+v, want valid until %s for principal grantra:prod-db", c, until)
	}
	if c.ValidAfter.After(time.Now().Add(-50 * time.Second)) {
		t.Errorf("valid_after = %s, want backdated a minute for clock skew", c.ValidAfter)
	}
	parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	cert := parsed.(*ssh.Certificate)
	var caLine string
	e.must(func(tx pgx.Tx) (err error) { caLine, err = e.s.PublicCA(e.ctx, tx, e.tenant, PeopleCA); return err })
	ca, _ := ParseCA(caLine)
	checker := ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return string(k.Marshal()) == string(ca.Marshal()) }}
	if err := checker.CheckCert("grantra:prod-db", cert); err != nil {
		t.Errorf("certificate does not check against the CA: %v", err)
	}
	// No Unix account matches it: a server takes it only where its
	// AuthorizedPrincipalsFile names the target.
	for _, account := range []string{"postgres", "prod-db", "root"} {
		if err := checker.CheckCert(account, cert); err == nil {
			t.Errorf("certificate logs in as %s", account)
		}
	}
	if cert.KeyId != KeyIDPrefix+c.ID || cert.Serial != c.Serial || string(cert.Key.Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Errorf("certificate key id %q serial %d", cert.KeyId, cert.Serial)
	}
	if _, ok := cert.Permissions.Extensions["permit-port-forwarding"]; ok {
		t.Error("certificate allows port forwarding")
	}
	if n := e.one(`SELECT count(*)::text FROM audit_events WHERE action = 'ssh.certificate_issued' AND target_id = $1`, tg.ID); n != "1" {
		t.Errorf("audited certificates = %s", n)
	}

	// Longer access: the target's maximum caps it. Straight to the server,
	// the principal is the same.
	off := false
	e.must(func(tx pgx.Tx) error {
		_, err := Update(e.ctx, tx, e.tenant, "identity:admin", tg.ID, Settings{ViaGateway: &off, MaxCertificateHours: 1})
		return err
	})
	e.grant(luis, tg.EntitlementID, time.Now().Add(24*time.Hour))
	c, err = e.issue(luis, tg.ID, pub)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(c.ValidBefore); d > time.Hour || d < 59*time.Minute || c.Principal != "grantra:prod-db" {
		t.Errorf("certificate = %+v, want an hour for grantra:prod-db", c)
	}

	// Inactive people hold nothing.
	if h := e.holders(tg.ID); len(h) != 2 {
		t.Errorf("holders = %v", h)
	}
	e.exec(`UPDATE identities SET status = 'terminated' WHERE id = $1`, luis)
	if h := e.holders(tg.ID); len(h) != 1 || h[0] != ana {
		t.Errorf("holders after a leaver = %v", h)
	}
	// A leaver whose access is still assigned gets no certificate either.
	if _, err := e.issue(luis, tg.ID, pub); !isConflict(err) {
		t.Errorf("certificate for a leaver = %v, want conflict", err)
	}
	for _, missing := range []string{"00000000-0000-0000-0000-000000000000", "nope"} {
		if err := e.tx(func(tx pgx.Tx) error { _, err := Holders(e.ctx, tx, missing); return err }); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("holders of %s = %v, want not found", missing, err)
		}
	}

	rsaKey, _ := rsa.GenerateKey(rand.Reader, 1024)
	weak, _ := ssh.NewPublicKey(&rsaKey.PublicKey)
	for _, bad := range []string{"", "not a key", string(ssh.MarshalAuthorizedKey(weak)), c.Certificate, pub + pub} {
		if _, err := e.issue(ana, tg.ID, bad); !isInvalid(err) {
			t.Errorf("issue for %.30q = %v, want invalid", bad, err)
		}
	}
}

func (e *env) holders(id string) []string {
	e.t.Helper()
	var h []string
	e.must(func(tx pgx.Tx) (err error) { h, err = Holders(e.ctx, tx, id); return err })
	return h
}

func (e *env) authorize(c Certificate, user string) (Grant, error) {
	var g Grant
	err := e.tx(func(tx pgx.Tx) (err error) { g, err = Authorize(e.ctx, tx, c.ID, c.Serial, user); return err })
	return g, err
}

func TestAuthorizeAndSessions(t *testing.T) {
	e := newEnv(t)
	ana := e.identity("Ana")
	tg := e.target(Input{Name: "web-1", Login: "deploy", Settings: Settings{Address: "web-1:2222"}})
	_, pub := userKey(t)
	x := e.grant(ana, tg.EntitlementID, time.Now().Add(time.Hour))
	c, err := e.issue(ana, tg.ID, pub)
	if err != nil {
		t.Fatal(err)
	}
	g, err := e.authorize(c, "web-1")
	if err != nil || g.Login != "deploy" || g.Address != "web-1:2222" || g.IdentityID != ana || g.HostKey != "" {
		t.Fatalf("authorize = %+v, %v", g, err)
	}
	if _, err := e.authorize(c, "web-2"); err == nil {
		t.Error("certificate let in to another target")
	}
	forged := c
	forged.Serial++
	if _, err := e.authorize(forged, "web-1"); err == nil {
		t.Error("certificate with another serial let in")
	}

	// Host keys are learned once and then kept.
	e.must(func(tx pgx.Tx) error {
		if k, err := LearnHostKey(e.ctx, tx, e.tenant, tg.ID, "ssh-ed25519 AAAA1"); err != nil || k != "ssh-ed25519 AAAA1" {
			t.Errorf("learn = %q, %v", k, err)
		}
		if k, err := LearnHostKey(e.ctx, tx, e.tenant, tg.ID, "ssh-ed25519 AAAA2"); err != nil || k != "ssh-ed25519 AAAA1" {
			t.Errorf("learn again = %q, %v", k, err)
		}
		return nil
	})

	var id string
	e.must(func(tx pgx.Tx) (err error) {
		id, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: tg.ID, TargetName: tg.Name, Login: tg.Login, IdentityID: ana,
			CertificateID: c.ID, ClientAddress: "10.1.1.1:5555", Gateway: "gw-1", Kind: "shell", Width: 120, Height: 40})
		if err != nil {
			return err
		}
		if err := AppendRecording(e.ctx, tx, e.tenant, id, 0, []byte("{\"version\":2}\n"), false); err != nil {
			return err
		}
		if err := AppendRecording(e.ctx, tx, e.tenant, id, 1, []byte("[0.5,\"o\",\"hi\"]\n"), true); err != nil {
			return err
		}
		return AddCommands(e.ctx, tx, e.tenant, id, []Command{
			{At: time.Now(), Offset: 0.5, Command: "psql -c 'DROP TABLE orders'"},
			{At: time.Now(), Offset: 1, Command: "echo 100%_done"},
		})
	})
	heartbeat := func() string {
		var r string
		e.must(func(tx pgx.Tx) (err error) { r, err = Heartbeat(e.ctx, tx, id); return err })
		return r
	}
	if r := heartbeat(); r != "" {
		t.Errorf("heartbeat with access = %q", r)
	}
	search := func(q string) []Command {
		var out []Command
		e.must(func(tx pgx.Tx) (err error) { out, err = SearchCommands(e.ctx, tx, q, "", 50); return err })
		return out
	}
	if got := search("drop table"); len(got) != 1 || got[0].TargetName != "web-1" || got[0].IdentityName != "Ana" || got[0].Offset != 0.5 {
		t.Errorf("search drop table = %+v", got)
	}
	// % and _ are literal, not wildcards.
	if got := search("0%_d"); len(got) != 1 {
		t.Errorf("search with wildcards = %+v", got)
	}
	if got := search("1%d"); len(got) != 0 {
		t.Errorf("%% matched as a wildcard: %+v", got)
	}

	var buf bytes.Buffer
	started := false
	e.must(func(tx pgx.Tx) error {
		if err := RecordingViewed(e.ctx, tx, e.tenant, "identity:auditor", id); err != nil {
			return err
		}
		return WriteRecording(e.ctx, tx, id, func() { started = true }, &buf)
	})
	rec := buf.Bytes()
	if !started {
		t.Error("WriteRecording did not call start")
	}
	var viewed int
	e.must(func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `SELECT count(*) FROM audit_events WHERE action = 'ssh.recording_viewed' AND target_id = $1`, id).Scan(&viewed)
	})
	if viewed != 1 {
		t.Errorf("recording_viewed audit events = %d", viewed)
	}
	e.must(func(tx pgx.Tx) error {
		if err := RecordingViewed(e.ctx, tx, e.tenant, "identity:auditor", "00000000-0000-0000-0000-000000000000"); !errors.Is(err, apperr.ErrNotFound) {
			t.Errorf("viewing a missing session's recording = %v", err)
		}
		return nil
	})
	if string(rec) != "{\"version\":2}\n[0.5,\"o\",\"hi\"]\n" {
		t.Errorf("recording = %q", rec)
	}
	s := e.session(id)
	if s.RecordingBytes != int64(len(rec)) || !s.RecordingTruncated || s.Width != 120 {
		t.Errorf("session = %+v", s)
	}

	// Access goes: the next heartbeat says so.
	e.exec(`UPDATE access_assignments SET expires_at = now() - interval '1 second' WHERE id = $1`, x)
	if r := heartbeat(); r != EndAccessEnded {
		t.Errorf("heartbeat without access = %q", r)
	}
	if _, err := e.authorize(c, "web-1"); !errors.Is(err, ErrNoAccess) {
		t.Errorf("authorize without access = %v", err)
	}

	// An admin ends it.
	e.must(func(tx pgx.Tx) error { return Terminate(e.ctx, tx, e.tenant, "identity:admin", id) })
	if r := heartbeat(); r != EndTerminated {
		t.Errorf("heartbeat after terminate = %q", r)
	}
	exit := 130
	e.must(func(tx pgx.Tx) error { return EndSession(e.ctx, tx, e.tenant, id, EndTerminated, "", &exit) })
	e.must(func(tx pgx.Tx) error { return EndSession(e.ctx, tx, e.tenant, id, EndClosed, "", nil) })
	if s := e.session(id); s.EndReason != EndTerminated || s.ExitStatus == nil || *s.ExitStatus != 130 {
		t.Errorf("ended session = %+v", s)
	}
	err = e.tx(func(tx pgx.Tx) error { return Terminate(e.ctx, tx, e.tenant, "identity:admin", id) })
	if !isConflict(err) {
		t.Errorf("terminate an ended session = %v", err)
	}

	// A session its gateway stopped touching is lost.
	var lost string
	e.must(func(tx pgx.Tx) (err error) {
		lost, err = StartSession(e.ctx, tx, e.tenant, Session{TargetID: tg.ID, TargetName: tg.Name, Login: tg.Login, IdentityID: ana,
			ClientAddress: "x", Gateway: "gw-1", Kind: "exec", Command: "uptime", Width: 80, Height: 24})
		return err
	})
	e.exec(`UPDATE ssh_sessions SET last_seen_at = now() - interval '10 minutes' WHERE id = $1`, lost)
	e.must(func(tx pgx.Tx) error {
		n, err := EndLostSessions(e.ctx, tx, e.tenant)
		if n != 1 {
			t.Errorf("lost sessions = %d", n)
		}
		return err
	})
	if s := e.session(lost); s.EndReason != EndLost {
		t.Errorf("lost session = %+v", s)
	}

	// Deleting the target keeps its recordings, and stops its certificates.
	e.must(func(tx pgx.Tx) error { return Delete(e.ctx, tx, e.tenant, "identity:admin", tg.ID) })
	if s := e.session(id); s.TargetID != "" || s.TargetName != "web-1" {
		t.Errorf("session after delete = %+v", s)
	}
	if n := e.one(`SELECT count(*)::text FROM entitlements WHERE id = $1`, tg.EntitlementID); n != "0" {
		t.Error("the target's entitlement outlived it")
	}
	if _, err := e.authorize(c, "web-1"); err == nil {
		t.Error("certificate of a deleted target let in")
	}
}

func (e *env) session(id string) Session {
	e.t.Helper()
	var s Session
	e.must(func(tx pgx.Tx) (err error) { s, err = GetSession(e.ctx, tx, id); return err })
	return s
}
