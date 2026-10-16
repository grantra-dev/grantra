package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
	"github.com/grantra-dev/grantra/core/internal/vault"
)

func newSigner(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// fakeServer is an SSH server that trusts a CA, like a real one with
// TrustedUserCAKeys, and runs a tiny shell: it echoes keys, answers
// "sudo" with a password prompt it does not echo, and "exit N" ends with
// status N. Commands run without a shell print "ran <command>".
type fakeServer struct {
	addr   string
	host   ssh.Signer
	logins chan string
}

func startServer(t *testing.T, ca ssh.PublicKey, login string) *fakeServer {
	t.Helper()
	f := &fakeServer{host: newSigner(t), logins: make(chan string, 10)}
	checker := &ssh.CertChecker{IsUserAuthority: func(k ssh.PublicKey) bool { return bytes.Equal(k.Marshal(), ca.Marshal()) }}
	conf := &ssh.ServerConfig{PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if c.User() != login {
			return nil, errors.New("wrong login")
		}
		p, err := checker.Authenticate(c, k)
		if err == nil {
			f.logins <- k.(*ssh.Certificate).KeyId
		}
		return p, err
	}}
	conf.AddHostKey(f.host)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	f.addr = l.Addr().String()
	go func() {
		for {
			nc, err := l.Accept()
			if err != nil {
				return
			}
			go f.serve(nc, conf)
		}
	}()
	return f
}

func (f *fakeServer) serve(nc net.Conn, conf *ssh.ServerConfig) {
	_, chans, reqs, err := ssh.NewServerConn(nc, conf)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(reqs)
	for nch := range chans {
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range creqs {
				switch req.Type {
				case "pty-req", "window-change":
					req.Reply(true, nil) //nolint:errcheck
				case "exec":
					req.Reply(true, nil) //nolint:errcheck
					var e struct{ Command string }
					ssh.Unmarshal(req.Payload, &e) //nolint:errcheck
					io.WriteString(ch, "ran "+e.Command+"\n")
					exit(ch, 0)
					return
				case "shell":
					req.Reply(true, nil) //nolint:errcheck
					go func() {
						for range creqs {
						}
					}()
					shell(ch)
					return
				default:
					req.Reply(false, nil) //nolint:errcheck
				}
			}
		}()
	}
}

func exit(ch ssh.Channel, status uint32) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, status)
	ch.SendRequest("exit-status", false, b) //nolint:errcheck
}

func shell(ch ssh.Channel) {
	io.WriteString(ch, "$ ")
	echo := true
	var line []byte
	buf := make([]byte, 256)
	for {
		n, err := ch.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			if b != '\r' {
				line = append(line, b)
				if echo {
					ch.Write([]byte{b}) //nolint:errcheck
				}
				continue
			}
			cmd := string(line)
			line = line[:0]
			switch {
			case !echo:
				echo = true
				io.WriteString(ch, "\r\nok\r\n$ ")
			case cmd == "sudo":
				echo = false
				io.WriteString(ch, "\r\nPassword: ")
			case strings.HasPrefix(cmd, "exit "):
				io.WriteString(ch, "\r\nbye\r\n")
				exit(ch, uint32(cmd[5]-'0'))
				return
			default:
				io.WriteString(ch, "\r\nyou said "+cmd+"\r\n$ ")
			}
		}
	}
}

type env struct {
	t      *testing.T
	ctx    context.Context
	pool   *pgxpool.Pool
	tenant string
	svc    *sshaccess.Service
	ca     ssh.PublicKey
	gw     string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	pool := dbtest.Open(t)
	k := make([]byte, 32)
	rand.Read(k) //nolint:errcheck
	key, err := vault.NewLocalKey(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, ctx: context.Background(), pool: pool, tenant: dbtest.Tenant(t, pool, "gateway"), svc: sshaccess.New(key)}
	e.must(func(tx pgx.Tx) error {
		line, err := e.svc.PublicCA(e.ctx, tx, e.tenant)
		if err != nil {
			return err
		}
		e.ca, err = sshaccess.ParseCA(line)
		return err
	})
	srv := &Server{Pool: pool, TenantID: e.tenant, SSH: e.svc, HostKey: newSigner(t), Name: "gw-test",
		Log: slog.New(slog.DiscardHandler), CheckEvery: 50 * time.Millisecond, FlushEvery: 20 * time.Millisecond}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	e.gw = l.Addr().String()
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("serve: %v", err)
		}
	})
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

// person is someone with access to a target and a certificate for it.
type person struct {
	id         string
	assignment string
	key        ssh.Signer
	cert       *ssh.Certificate
}

func (e *env) target(name, login, addr string) sshaccess.Target {
	e.t.Helper()
	var tg sshaccess.Target
	e.must(func(tx pgx.Tx) (err error) {
		tg, err = sshaccess.Create(e.ctx, tx, e.tenant, "identity:admin",
			sshaccess.Input{Name: name, Login: login, Settings: sshaccess.Settings{Address: addr}})
		return err
	})
	return tg
}

func (e *env) person(name string, tg sshaccess.Target) person {
	e.t.Helper()
	p := person{key: newSigner(e.t)}
	p.id = e.one(`INSERT INTO identities (tenant_id, display_name, kind) VALUES ($1, $2, 'human') RETURNING id`, e.tenant, name)
	p.assignment = e.one(`INSERT INTO access_assignments (tenant_id, identity_id, entitlement_id, origin, granted_by, expires_at)
	                      VALUES ($1, $2, $3, 'manual', 'test', now() + interval '1 hour') RETURNING id`, e.tenant, p.id, tg.EntitlementID)
	e.must(func(tx pgx.Tx) error {
		c, err := e.svc.Issue(e.ctx, tx, e.tenant, p.id, tg.ID, string(ssh.MarshalAuthorizedKey(p.key.PublicKey())))
		if err != nil {
			return err
		}
		k, _, _, _, err := ssh.ParseAuthorizedKey([]byte(c.Certificate))
		p.cert, _ = k.(*ssh.Certificate)
		return err
	})
	return p
}

func (e *env) dial(user string, auth ssh.Signer) (*ssh.Client, error) {
	return ssh.Dial("tcp", e.gw, &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.PublicKeys(auth)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second})
}

func (e *env) connect(user string, p person) *ssh.Client {
	e.t.Helper()
	signer, err := ssh.NewCertSigner(p.cert, p.key)
	if err != nil {
		e.t.Fatal(err)
	}
	c, err := e.dial(user, signer)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { c.Close() })
	return c
}

// buffer is a concurrency-safe output buffer.
type buffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type shellSession struct {
	sess   *ssh.Session
	stdin  io.WriteCloser
	out    *buffer
	errOut *buffer
}

func (e *env) shell(c *ssh.Client) shellSession {
	e.t.Helper()
	sess, err := c.NewSession()
	if err != nil {
		e.t.Fatal(err)
	}
	s := shellSession{sess: sess, out: &buffer{}, errOut: &buffer{}}
	sess.Stdout, sess.Stderr = s.out, s.errOut
	if s.stdin, err = sess.StdinPipe(); err != nil {
		e.t.Fatal(err)
	}
	if err := sess.RequestPty("xterm", 40, 120, ssh.TerminalModes{}); err != nil {
		e.t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (s shellSession) typeLine(t *testing.T, line, expect string) {
	t.Helper()
	for _, k := range line {
		io.WriteString(s.stdin, string(k)) //nolint:errcheck
	}
	io.WriteString(s.stdin, "\r") //nolint:errcheck
	waitFor(t, expect, func() bool { return strings.Contains(s.out.String(), expect) })
}

func (e *env) lastSession(identity string) sshaccess.Session {
	e.t.Helper()
	var list []sshaccess.Session
	e.must(func(tx pgx.Tx) (err error) {
		list, err = sshaccess.ListSessions(e.ctx, tx, sshaccess.SessionFilter{IdentityID: identity, Limit: 1})
		return err
	})
	if len(list) == 0 {
		e.t.Fatal("no session recorded")
	}
	return list[0]
}

func (e *env) ended(identity string) sshaccess.Session {
	e.t.Helper()
	var s sshaccess.Session
	waitFor(e.t, "the session to end", func() bool { s = e.lastSession(identity); return s.EndedAt != nil })
	return s
}

func TestShellIsRecorded(t *testing.T) {
	e := newEnv(t)
	tg := e.target("app-1", "deploy", "127.0.0.1:1")
	srv := startServer(t, e.ca, "deploy")
	e.exec(`UPDATE ssh_targets SET address = $2 WHERE id = $1`, tg.ID, srv.addr)
	ana := e.person("Ana", tg)

	s := e.shell(e.connect("app-1", ana))
	waitFor(t, "the prompt", func() bool { return strings.Contains(s.out.String(), "$ ") })
	s.typeLine(t, "uptime", "you said uptime")
	s.typeLine(t, "sudo", "Password: ")
	s.typeLine(t, "hunter2", "ok")
	s.typeLine(t, "exit 3", "bye")
	var ee *ssh.ExitError
	if err := s.sess.Wait(); !errors.As(err, &ee) || ee.ExitStatus() != 3 {
		t.Errorf("exit = %v, want status 3", err)
	}

	got := e.ended(ana.id)
	if got.EndReason != sshaccess.EndClosed || got.ExitStatus == nil || *got.ExitStatus != 3 || got.Kind != "shell" ||
		got.Width != 120 || got.Height != 40 || got.TargetName != "app-1" || got.Login != "deploy" || got.Gateway != "gw-test" {
		t.Errorf("session = %+v", got)
	}
	// The gateway logged in with its own one-off certificate.
	if id := <-srv.logins; id != "grantra-gateway:"+got.ID {
		t.Errorf("gateway logged in as %q", id)
	}
	var rec []byte
	var cmds []sshaccess.Command
	e.must(func(tx pgx.Tx) (err error) {
		if rec, err = sshaccess.Recording(e.ctx, tx, e.tenant, "identity:auditor", got.ID); err != nil {
			return err
		}
		cmds, err = sshaccess.SessionCommands(e.ctx, tx, got.ID)
		return err
	})
	lines := strings.Split(strings.TrimSpace(string(rec)), "\n")
	if !strings.HasPrefix(lines[0], `{"version":2,"width":120,"height":40,`) || !strings.Contains(string(rec), "you said uptime") {
		t.Errorf("recording = %q", rec)
	}
	if strings.Contains(string(rec), "hunter2") {
		t.Error("the recording holds what the server never showed")
	}
	var typed []string
	for _, c := range cmds {
		typed = append(typed, c.Command)
	}
	if strings.Join(typed, "|") != "uptime|sudo|exit 3" {
		t.Errorf("commands = %q", typed)
	}
	// The host key was learned on first use.
	if hk := e.one(`SELECT host_key FROM ssh_targets WHERE id = $1`, tg.ID); hk != strings.TrimSpace(string(ssh.MarshalAuthorizedKey(srv.host.PublicKey()))) {
		t.Errorf("host key = %q", hk)
	}
	for _, action := range []string{"ssh.session_started", "ssh.session_ended", "ssh.host_key_learned"} {
		if n := e.one(`SELECT count(*)::text FROM audit_events WHERE action = $1`, action); n != "1" {
			t.Errorf("%s audited %s times", action, n)
		}
	}

	// Commands without a shell are recorded too.
	sess, err := e.connect("app-1", ana).NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output("cat /etc/hostname")
	if err != nil || string(out) != "ran cat /etc/hostname\n" {
		t.Errorf("exec = %q, %v", out, err)
	}
	exec := e.ended(ana.id)
	if exec.Kind != "exec" || exec.Command != "cat /etc/hostname" || *exec.ExitStatus != 0 {
		t.Errorf("exec session = %+v", exec)
	}
	var found []sshaccess.Command
	e.must(func(tx pgx.Tx) (err error) {
		found, err = sshaccess.SearchCommands(e.ctx, tx, "/ETC/host", "", 10)
		return err
	})
	if len(found) != 1 || found[0].SessionID != exec.ID {
		t.Errorf("search = %+v", found)
	}
}

func TestSessionEndsWithAccess(t *testing.T) {
	e := newEnv(t)
	srv := startServer(t, e.ca, "root")
	tg := e.target("db-1", "root", srv.addr)
	ana, luis := e.person("Ana", tg), e.person("Luis", tg)

	s := e.shell(e.connect("db-1", ana))
	s.typeLine(t, "select 1", "you said select 1")
	e.exec(`UPDATE access_assignments SET expires_at = now() - interval '1 second' WHERE id = $1`, ana.assignment)
	if err := s.sess.Wait(); err == nil {
		t.Error("session went on without access")
	}
	if !strings.Contains(s.errOut.String(), "Your access to db-1 ended") {
		t.Errorf("stderr = %q", s.errOut.String())
	}
	if got := e.ended(ana.id); got.EndReason != sshaccess.EndAccessEnded {
		t.Errorf("session = %+v", got)
	}
	// And it cannot come back.
	signer, _ := ssh.NewCertSigner(ana.cert, ana.key)
	if _, err := e.dial("db-1", signer); err == nil {
		t.Error("connected without access")
	}

	// An admin ends a session.
	s = e.shell(e.connect("db-1", luis))
	s.typeLine(t, "ls", "you said ls")
	running := e.lastSession(luis.id)
	e.must(func(tx pgx.Tx) error { return sshaccess.Terminate(e.ctx, tx, e.tenant, "identity:admin", running.ID) })
	s.sess.Wait() //nolint:errcheck
	if !strings.Contains(s.errOut.String(), "An admin ended this session") {
		t.Errorf("stderr = %q", s.errOut.String())
	}
	if got := e.ended(luis.id); got.EndReason != sshaccess.EndTerminated {
		t.Errorf("session = %+v", got)
	}
}

func TestGatewayRefuses(t *testing.T) {
	e := newEnv(t)
	srv := startServer(t, e.ca, "root")
	tg := e.target("db-1", "root", srv.addr)
	e.target("db-2", "root", srv.addr)
	ana := e.person("Ana", tg)
	signer, _ := ssh.NewCertSigner(ana.cert, ana.key)

	// Another target than the certificate's.
	if _, err := e.dial("db-2", signer); err == nil {
		t.Error("certificate for db-1 opened db-2")
	}
	// A plain key.
	if _, err := e.dial("db-1", ana.key); err == nil {
		t.Error("a plain key got in")
	}
	// A certificate from another CA, otherwise identical.
	forged := *ana.cert
	if err := forged.SignCert(rand.Reader, newSigner(t)); err != nil {
		t.Fatal(err)
	}
	if fs, _ := ssh.NewCertSigner(&forged, ana.key); fs != nil {
		if _, err := e.dial("db-1", fs); err == nil {
			t.Error("a certificate from another CA got in")
		}
	}
	// Targets reached directly are not served through the gateway.
	e.exec(`UPDATE ssh_targets SET via_gateway = false WHERE id = $1`, tg.ID)
	if _, err := e.dial("db-1", signer); err == nil {
		t.Error("gateway served a direct target")
	}
	e.exec(`UPDATE ssh_targets SET via_gateway = true WHERE id = $1`, tg.ID)

	// A server whose host key changed is not trusted.
	e.exec(`UPDATE ssh_targets SET host_key = $2 WHERE id = $1`, tg.ID,
		strings.TrimSpace(string(ssh.MarshalAuthorizedKey(newSigner(t).PublicKey()))))
	sess, err := e.connect("db-1", ana).NewSession()
	if err != nil {
		t.Fatal(err)
	}
	var stderr buffer
	sess.Stderr = &stderr
	err = sess.Run("id")
	var ee *ssh.ExitError
	if !errors.As(err, &ee) || ee.ExitStatus() != 255 || !strings.Contains(stderr.String(), "host key changed") {
		t.Errorf("run = %v, stderr %q", err, stderr.String())
	}
	if got := e.ended(ana.id); got.EndReason != sshaccess.EndFailed || !strings.Contains(got.Error, "host key changed") {
		t.Errorf("session = %+v", got)
	}
	// Port forwarding is refused.
	c := e.connect("db-1", ana)
	if _, err := c.Dial("tcp", srv.addr); err == nil {
		t.Error("port forwarding through the gateway worked")
	}
}
