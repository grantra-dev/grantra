package proxy

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// upstreamMySQL creates users on the MySQL in GRANTRA_TEST_MYSQL_DSN (an
// admin's DSN, such as root:secret@tcp(127.0.0.1:3306)/), skipping the test
// when it is not set (CI sets it). It returns the database's address, a
// database the users own, and a function creating a user with the given
// authentication plugin.
func upstreamMySQL(t *testing.T) (address, database string, user func(plugin string) (name, password string, ok bool)) {
	t.Helper()
	dsn := os.Getenv("GRANTRA_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("GRANTRA_TEST_MYSQL_DSN not set")
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	b := make([]byte, 4)
	rand.Read(b) //nolint:errcheck
	suffix := hex.EncodeToString(b)
	database = "gw_" + suffix
	if _, err := admin.Exec("CREATE DATABASE " + database); err != nil {
		t.Fatal(err)
	}
	var users []string
	t.Cleanup(func() {
		for _, u := range users {
			admin.Exec("DROP USER IF EXISTS '" + u + "'@'%'") //nolint:errcheck
		}
		admin.Exec("DROP DATABASE IF EXISTS " + database) //nolint:errcheck
	})
	user = func(plugin string) (string, string, bool) {
		name, password := fmt.Sprintf("gw_%s_%d_%s", plugin[:6], len(users), suffix), suffix+"-pw-"+plugin[:3]
		if _, err := admin.Exec("CREATE USER '" + name + "'@'%' IDENTIFIED WITH " + plugin + " BY '" + password + "'"); err != nil {
			t.Logf("no %s user: %v", plugin, err)
			return "", "", false
		}
		users = append(users, name)
		if _, err := admin.Exec("GRANT ALL ON " + database + ".* TO '" + name + "'@'%'"); err != nil {
			t.Fatal(err)
		}
		return name, password, true
	}
	return cfg.Addr, database, user
}

func TestMySQL(t *testing.T) {
	e := newEnv(t)
	address, database, user := upstreamMySQL(t)
	ana := e.identity("ana")
	srv := e.server()
	addr := e.listen(srv.ServeMySQL)
	if err := mysql.RegisterTLSConfig("grantra-test", &tls.Config{RootCAs: e.roots, ServerName: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	// connect opens one connection; closing it closes it for real.
	connect := func(target, token, tlsMode, db string) (*mysqlConn, error) {
		cfg := mysql.NewConfig()
		cfg.User, cfg.Passwd, cfg.Net, cfg.Addr, cfg.DBName, cfg.TLSConfig = target, token, "tcp", addr, db, tlsMode
		cfg.Timeout = 10 * time.Second
		cfg.AllowAllFiles = true
		connector, err := mysql.NewConnector(cfg)
		if err != nil {
			return nil, err
		}
		pool := sql.OpenDB(connector)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		conn, err := pool.Conn(ctx)
		if err != nil {
			pool.Close()
			return nil, err
		}
		return &mysqlConn{conn, pool}, nil
	}
	ctx := context.Background()

	// Each way the database may ask for the password: caching_sha2 with
	// and without TLS to it (the RSA key exchange), and the older native
	// plugin where the database still has it.
	for _, c := range []struct {
		name, plugin, tls string
	}{
		{"caching-sha2-tls", "caching_sha2_password", gateways.TLSRequire},
		{"caching-sha2-rsa", "caching_sha2_password", gateways.TLSDisable},
		{"native", "mysql_native_password", gateways.TLSDisable},
	} {
		t.Run(c.name, func(t *testing.T) {
			login, password, ok := user(c.plugin)
			if !ok {
				t.Skip("the database lacks this plugin")
			}
			acct := e.vaultAccount("billing "+c.name, login, password)
			tg := e.target(gateways.Input{Name: "billing-" + c.name, Kind: gateways.MySQL, Settings: gateways.Settings{
				Address: &address, Database: &database, VaultAccountID: &acct, TLSMode: ptr(c.tls)}})
			e.grant(ana, tg)
			token := e.token(ana, tg)
			for range 2 { // caching_sha2's first login and its cached second
				conn, err := connect(tg.Name, token, "grantra-test", "")
				if err != nil {
					t.Fatal(err)
				}
				var who, db string
				if err := conn.QueryRowContext(ctx, "SELECT current_user(), database()").Scan(&who, &db); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(who, login+"@") || db != database {
					t.Errorf("logged in as %s on %s", who, db)
				}
				conn.Close()
				s := e.session(tg.ID, ended)
				if s.EndReason != gateways.EndClosed || s.Account != login {
					t.Errorf("session = %+v", s)
				}
			}
		})
	}

	login, password, _ := user("caching_sha2_password")
	acct := e.vaultAccount("billing owner", login, password)
	tg := e.target(gateways.Input{Name: "billing", Kind: gateways.MySQL, Settings: gateways.Settings{
		Address: &address, VaultAccountID: &acct, TLSMode: ptr(gateways.TLSRequire)}})
	assignment := e.grant(ana, tg)
	token := e.token(ana, tg)

	if _, err := connect("billing", token, "false", database); err == nil || !strings.Contains(err.Error(), "only accepts TLS") {
		t.Errorf("plain connection: %v", err)
	}
	if _, err := connect("billing", gateways.TokenPrefix+"nope", "grantra-test", database); err == nil || !strings.Contains(err.Error(), "unknown, expired or revoked") {
		t.Errorf("unknown token: %v", err)
	}

	conn, err := connect("billing", token, "grantra-test", database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE invoices (id int PRIMARY KEY, total int)"); err != nil {
		t.Fatal(err)
	}
	// Prepared statements, and a statement over 64 KB.
	if _, err := conn.ExecContext(ctx, "INSERT INTO invoices VALUES (?, ?)", 1, 100); err != nil {
		t.Fatal(err)
	}
	big := "SELECT '" + strings.Repeat("x", 70<<10) + "'"
	var got string
	if err := conn.QueryRowContext(ctx, big).Scan(&got); err != nil || len(got) != 70<<10 {
		t.Fatalf("big query = %d %v", len(got), err)
	}
	var total int
	if err := conn.QueryRowContext(ctx, "SELECT total FROM invoices WHERE id = ?", 1).Scan(&total); err != nil || total != 100 {
		t.Fatalf("select = %d %v", total, err)
	}
	// LOAD DATA LOCAL would send a file the gateway does not log.
	if _, err := conn.ExecContext(ctx, "LOAD DATA LOCAL INFILE '/etc/hostname' INTO TABLE invoices"); err == nil {
		t.Error("LOAD DATA LOCAL ran")
	}
	conn.Close()
	s := e.session(tg.ID, ended)
	log := e.events(s.ID)
	for _, want := range []string{"CREATE TABLE invoices (id int PRIMARY KEY, total int)", "INSERT INTO invoices VALUES (?, ?)",
		"SELECT total FROM invoices WHERE id = ?"} {
		if !slices.Contains(log, want) {
			t.Errorf("log lacks %q: %q", want, log)
		}
	}
	if !slices.ContainsFunc(log, func(q string) bool { return strings.HasPrefix(q, "SELECT 'xxx") && len(q) <= gateways.MaxEventText }) {
		t.Error("the big query is not logged, cut")
	}

	// Losing the access ends the session.
	conn, err = connect("billing", token, "grantra-test", database)
	if err != nil {
		t.Fatal(err)
	}
	e.exec(`DELETE FROM access_assignments WHERE id = $1`, assignment)
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndAccessEnded {
		t.Errorf("session ended %q", s.EndReason)
	}
	if _, err := conn.ExecContext(ctx, "SELECT 1"); err == nil {
		t.Error("query ran after the access ended")
	}
	conn.Close()

	// A client may not log in again as another database user on the same
	// connection (COM_CHANGE_USER): that login would bypass the gateway.
	e.grant(ana, tg)
	token = e.token(ana, tg)
	raw := e.mysqlRaw(t, addr, "billing", token)
	change := append([]byte{myComChangeUser}, "root\x00"...)
	if err := myWritePacket(raw, 0, append(change, 0)); err != nil {
		t.Fatal(err)
	}
	raw.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	if _, _, err := myReadPacket(raw); err == nil {
		t.Error("COM_CHANGE_USER got an answer")
	}
	raw.Close()
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndClosed {
		t.Errorf("session after COM_CHANGE_USER = %+v", s)
	}
	// Nor pass a command off as the rest of another, which would go
	// unlogged.
	raw = e.mysqlRaw(t, addr, "billing", token)
	if err := myWritePacket(raw, 3, append([]byte{myComQuery}, "DROP TABLE invoices"...)); err != nil {
		t.Fatal(err)
	}
	raw.SetReadDeadline(time.Now().Add(5 * time.Second)) //nolint:errcheck
	if _, _, err := myReadPacket(raw); err == nil {
		t.Error("a command out of order got an answer")
	}
	raw.Close()
	conn, err = connect("billing", token, "grantra-test", database)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.QueryRowContext(ctx, "SELECT total FROM invoices WHERE id = ?", 1).Scan(&total); err != nil {
		t.Errorf("invoices after the out of order command: %v", err)
	}
	conn.Close()

	var n string
	e.must(func(tx pgx.Tx) error {
		return tx.QueryRow(e.ctx, `SELECT count(*)::text FROM gateway_sessions WHERE target_id = $1`, tg.ID).Scan(&n)
	})
	if n != "5" { // the refused logins start none
		t.Errorf("sessions = %s", n)
	}
}

// mysqlRaw logs in through the gateway with the gateway's own client code
// and returns the connection, for sending commands no driver sends.
func (e *env) mysqlRaw(t *testing.T, addr, target, token string) net.Conn {
	t.Helper()
	nc, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{DialTimeout: 10 * time.Second}
	tg := gateways.Target{TLSMode: gateways.TLSRequire, Address: addr}
	conn, _, _, err := s.myLogin(nc, tg, target, token, "", myHandshake{caps: myGatewayCaps, maxPacket: 1 << 24, charset: 255})
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

type mysqlConn struct {
	*sql.Conn
	pool *sql.DB
}

func (c *mysqlConn) Close() error {
	c.Conn.Close()
	return c.pool.Close()
}
