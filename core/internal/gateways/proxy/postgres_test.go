package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// upstreamPostgres creates a login role with a password on the test
// database, the account the gateway logs in with, and returns its name,
// password and the database's address and name.
func upstreamPostgres(t *testing.T) (role, password, address, database string) {
	t.Helper()
	dbURL := os.Getenv("GRANTRA_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("GRANTRA_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	b := make([]byte, 6)
	rand.Read(b) //nolint:errcheck
	role, password = "gw_upstream_"+hex.EncodeToString(b), hex.EncodeToString(b)+"-pw"
	if _, err := admin.Exec(ctx, "CREATE ROLE "+role+" LOGIN PASSWORD '"+password+"'"); err != nil {
		t.Skipf("cannot create a login role on the test database: %v", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), dbURL)
		if err != nil {
			return
		}
		defer c.Close(context.Background())
		c.Exec(context.Background(), "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1", role) //nolint:errcheck
		c.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)                                                       //nolint:errcheck
	})
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "5432"
	}
	return role, password, net.JoinHostPort(host, port), strings.TrimPrefix(u.Path, "/")
}

func TestPostgres(t *testing.T) {
	e := newEnv(t)
	role, password, address, database := upstreamPostgres(t)
	acct := e.vaultAccount("orders owner", role, password)
	tg := e.target(gateways.Input{Name: "orders-db", Kind: gateways.Postgres, Settings: gateways.Settings{
		Address: &address, Database: &database, VaultAccountID: &acct, TLSMode: ptr(gateways.TLSDisable)}})
	ana := e.identity("ana")
	assignment := e.grant(ana, tg)
	token := e.token(ana, tg)
	srv := e.server()
	addr := e.listen(srv.ServePostgres)
	host, port, _ := net.SplitHostPort(addr)

	connect := func(user, password, sslmode string, extra ...string) (*pgx.Conn, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn := "host=" + host + " port=" + port + " user=" + user + " password=" + password + " sslmode=" + sslmode +
			" sslrootcert=" + e.caFile + " " + strings.Join(extra, " ")
		return pgx.Connect(ctx, conn)
	}

	// Without TLS, the token never leaves the client.
	if _, err := connect("orders-db", token, "disable"); err == nil || !strings.Contains(err.Error(), "only accepts TLS") {
		t.Errorf("plain connection: %v", err)
	}
	if _, err := connect("orders-db", gateways.TokenPrefix+"nope", "verify-full"); err == nil || !strings.Contains(err.Error(), "unknown, expired or revoked") {
		t.Errorf("unknown token: %v", err)
	}
	if _, err := connect("billing", token, "verify-full"); err == nil || !strings.Contains(err.Error(), "the token is for orders-db") {
		t.Errorf("token of another target: %v", err)
	}
	if _, err := connect("orders-db", token, "verify-full", "replication=database"); err == nil || !strings.Contains(err.Error(), "Replication") {
		t.Errorf("replication: %v", err)
	}

	// The gateway logs in as the vault account, to the target's database
	// when the client names none, and logs every statement.
	conn, err := connect("orders-db", token, "verify-full", "application_name=psql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var user, db, app string
	if err := conn.QueryRow(ctx, "SELECT current_user, current_database(), current_setting('application_name')").Scan(&user, &db, &app); err != nil {
		t.Fatal(err)
	}
	if user != role || db != database || app != "psql" {
		t.Errorf("logged in as %s on %s (%s)", user, db, app)
	}
	var n int
	if err := conn.QueryRow(ctx, "SELECT $1::int + 1", 41).Scan(&n); err != nil || n != 42 {
		t.Errorf("extended query = %d %v", n, err)
	}
	if _, err := conn.Exec(ctx, "SELECT 'simple'", pgx.QueryExecModeSimpleProtocol); err != nil {
		t.Fatal(err)
	}
	// A query cancelled by the client is cancelled on the database.
	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	start := time.Now()
	_, err = conn.Exec(cctx, "SELECT pg_sleep(20)")
	cancel()
	var pe *pgconn.PgError
	if err == nil || time.Since(start) > 5*time.Second {
		t.Errorf("cancel took %s: %v", time.Since(start), err)
	} else if errors.As(err, &pe) && pe.Code != "57014" {
		t.Errorf("cancel = %v", pe)
	}
	conn.Close(ctx)
	s := e.session(tg.ID, ended)
	if s.EndReason != gateways.EndClosed || s.IdentityID != ana || s.Database != database || s.Account != role {
		t.Errorf("session = %+v", s)
	}
	log := e.events(s.ID)
	for _, want := range []string{"SELECT current_user, current_database(), current_setting('application_name')", "SELECT $1::int + 1",
		"SELECT 'simple'", "SELECT pg_sleep(20)"} {
		if !slices.Contains(log, want) {
			t.Errorf("log lacks %q: %q", want, log)
		}
	}
	if !strings.Contains(e.one(`SELECT details::text FROM audit_events WHERE action = 'gateway.session_started' ORDER BY id DESC LIMIT 1`), acct) {
		t.Error("the session start does not name the vault account in the audit log")
	}

	// Losing the access ends the session, and the person is told why.
	conn, err = connect("orders-db", token, "verify-full")
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`DELETE FROM access_assignments WHERE id = $1`, assignment)
	s = e.session(tg.ID, ended)
	if s.EndReason != gateways.EndAccessEnded {
		t.Errorf("session ended %q", s.EndReason)
	}
	if _, err := conn.Exec(ctx, "SELECT 1"); err == nil {
		t.Error("query ran after the access ended")
	}
	conn.Close(ctx)

	// An admin ending a session ends it, and revokes its token.
	e.grant(ana, tg)
	token = e.token(ana, tg)
	conn, err = connect("orders-db", token, "verify-full")
	if err != nil {
		t.Fatal(err)
	}
	running := e.session(tg.ID, func(s gateways.Session) bool { return s.EndedAt == nil })
	e.must(func(tx pgx.Tx) error { return gateways.Terminate(e.ctx, tx, e.tenant, "identity:admin", running.ID) })
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndTerminated {
		t.Errorf("session ended %q", s.EndReason)
	}
	conn.Close(ctx)
	if _, err := connect("orders-db", token, "verify-full"); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Errorf("token of an ended session: %v", err)
	}

	// The log has a limit: reaching it ends the session before the query
	// runs.
	token = e.token(ana, tg)
	srv2 := e.server()
	srv2.MaxEvents = 2
	addr2 := e.listen(srv2.ServePostgres)
	host2, port2, _ := net.SplitHostPort(addr2)
	conn, err = pgx.Connect(ctx, "host="+host2+" port="+port2+" user=orders-db password="+token+" sslmode=require")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 2 {
		if _, err := conn.Exec(ctx, "SELECT 1", pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatalf("query %d: %v", i, err)
		}
	}
	if _, err := conn.Exec(ctx, "SELECT 'one too many'", pgx.QueryExecModeSimpleProtocol); err == nil ||
		!strings.Contains(err.Error(), "log limit") {
		t.Errorf("third query: %v", err)
	}
	conn.Close(ctx)
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndRecordingFull || s.Events != 2 {
		t.Errorf("session = %+v", s)
	}
}

func TestPostgresQueryText(t *testing.T) {
	for _, c := range []struct {
		typ  byte
		body string
		want string
	}{
		{'Q', "SELECT 1\x00", "SELECT 1"},
		{'P', "stmt1\x00SELECT $1\x00\x00\x00", "SELECT $1"},
		{'P', "\x00UPDATE t SET a = 1", "UPDATE t SET a = 1"},
		{'F', "\x00\x00\x04\xd2rest", "-- function call (fast path) of OID 1234"},
		{'B', "anything", ""},
	} {
		if got := pgQueryText(c.typ, []byte(c.body)); got != c.want {
			t.Errorf("%c %q = %q", c.typ, c.body, got)
		}
	}
}

func ptr[T any](v T) *T { return &v }
