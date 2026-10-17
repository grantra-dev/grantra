package proxy

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/gateways"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

type env struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	tenant string
	keys   vault.KeyProvider
	gw     *gateways.Service
	// serverTLS is the gateway's certificate; roots trusts it, and caFile
	// holds the CA for clients that read it from a file.
	serverTLS *tls.Config
	roots     *x509.CertPool
	caFile    string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	keys, err := vault.NewLocalKey(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: context.Background(), pool: pool, tenant: dbtest.Tenant(t, pool, "proxy"), keys: keys, gw: gateways.New(keys)}
	var caPEM string
	var cert *tls.Certificate
	e.must(func(tx pgx.Tx) (err error) {
		if caPEM, err = e.gw.CACertificate(e.ctx, tx, e.tenant); err != nil {
			return err
		}
		cert, err = e.gw.ServingCertificate(e.ctx, tx, e.tenant, []string{"localhost", "127.0.0.1"})
		return err
	})
	e.serverTLS = &tls.Config{Certificates: []tls.Certificate{*cert}, MinVersion: tls.VersionTLS12}
	e.roots = x509.NewCertPool()
	e.roots.AppendCertsFromPEM([]byte(caPEM))
	e.caFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(e.caFile, []byte(caPEM), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
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

func (e *env) identity(name string) string {
	return e.one(`INSERT INTO identities (tenant_id, display_name, email, kind) VALUES ($1, $2, $3, 'human') RETURNING id`,
		e.tenant, name, name+"@example.com")
}

func (e *env) vaultAccount(name, username, secret string) string {
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

// grant gives identity the target's access for an hour and returns the
// assignment.
func (e *env) grant(identity string, t gateways.Target) string {
	return e.one(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	              VALUES ($1, $2, $3, 'manual', 'test', now() + interval '1 hour') RETURNING id`, e.tenant, identity, t.EntitlementID)
}

func (e *env) token(identity string, t gateways.Target) string {
	e.t.Helper()
	var tok gateways.Token
	e.must(func(tx pgx.Tx) (err error) {
		tok, err = gateways.Issue(e.ctx, tx, e.tenant, identity, t.ID)
		return err
	})
	return tok.Token
}

// server returns a gateway checking sessions every 200ms.
func (e *env) server() *Server {
	return &Server{Pool: e.pool, TenantID: e.tenant, Gateways: e.gw, TLS: e.serverTLS, Name: "test-gateway",
		Log:        slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
		CheckEvery: 200 * time.Millisecond, FlushEvery: 50 * time.Millisecond, DialTimeout: 5 * time.Second}
}

// listen runs serve on a local port until the test ends and returns its
// address.
func (e *env) listen(serve func(context.Context, net.Listener) error) string {
	e.t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		e.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, l) }()
	e.t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			e.t.Error(err)
		}
	})
	return l.Addr().String()
}

// session waits for the newest session of target to match ok and returns
// it.
func (e *env) session(targetID string, ok func(gateways.Session) bool) gateways.Session {
	e.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var list []gateways.Session
		e.must(func(tx pgx.Tx) (err error) {
			list, err = gateways.ListSessions(e.ctx, tx, gateways.SessionFilter{TargetID: targetID, Limit: 1})
			return err
		})
		if len(list) == 1 && ok(list[0]) {
			return list[0]
		}
		if time.Now().After(deadline) {
			e.t.Fatalf("no session matched: %+v", list)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (e *env) events(sessionID string) []string {
	e.t.Helper()
	evs := e.eventList(sessionID)
	out := make([]string, len(evs))
	for i, ev := range evs {
		out[i] = ev.Text
	}
	return out
}

func ended(s gateways.Session) bool { return s.EndedAt != nil }

func (e *env) eventList(sessionID string) []gateways.Event {
	e.t.Helper()
	var evs []gateways.Event
	e.must(func(tx pgx.Tx) (err error) {
		evs, err = gateways.SessionEvents(e.ctx, tx, sessionID, 0, 1000)
		return err
	})
	return evs
}
