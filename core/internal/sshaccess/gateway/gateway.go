// Package gateway is the SSH gateway people reach their servers through:
// ssh <target>@gateway with a certificate Grantra signed. It checks access
// when they connect and every 30 seconds after, logs in to the target with
// a one-off certificate of its own, records the session in asciicast v2 and
// indexes the commands typed.
package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/ssh"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/sshaccess"
)

// Server is an SSH gateway for one tenant.
type Server struct {
	Pool     *pgxpool.Pool
	TenantID string
	SSH      *sshaccess.Service
	// HostKey is the gateway's own host key, the one people's known_hosts
	// remembers.
	HostKey ssh.Signer
	// Name tells gateways apart on recorded sessions, such as the host name.
	Name string
	Log  *slog.Logger

	// CheckEvery is how often a running session's access is checked
	// (default 30 seconds); FlushEvery how often its recording is stored
	// (default 2 seconds). MaxRecording caps one recording (default 100 MB).
	CheckEvery   time.Duration
	FlushEvery   time.Duration
	MaxRecording int64
	// DialTimeout bounds reaching a target (default 10 seconds).
	DialTimeout time.Duration

	ca ssh.PublicKey
}

func (s *Server) defaults() {
	if s.CheckEvery == 0 {
		s.CheckEvery = 30 * time.Second
	}
	if s.FlushEvery == 0 {
		s.FlushEvery = 2 * time.Second
	}
	if s.MaxRecording == 0 {
		s.MaxRecording = 100 << 20
	}
	if s.DialTimeout == 0 {
		s.DialTimeout = 10 * time.Second
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
}

// Serve accepts connections on l until ctx ends. Sessions still running
// then are told the gateway is stopping and closed.
func (s *Server) Serve(ctx context.Context, l net.Listener) error {
	s.defaults()
	err := db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) error {
		line, err := s.SSH.PublicCA(ctx, tx, s.TenantID)
		if err != nil {
			return err
		}
		s.ca, err = sshaccess.ParseCA(line)
		return err
	})
	if err != nil {
		return fmt.Errorf("load the SSH certificate authority: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { l.Close() })
	defer stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handle(ctx, conn)
		}()
	}
}

const grantKey = "grantra-grant"

func (s *Server) config(ctx context.Context) *ssh.ServerConfig {
	checker := &ssh.CertChecker{
		IsUserAuthority: func(auth ssh.PublicKey) bool { return bytes.Equal(auth.Marshal(), s.ca.Marshal()) },
	}
	conf := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-Grantra",
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			cert, ok := key.(*ssh.Certificate)
			if !ok {
				return nil, errors.New("not a certificate: log in with the certificate Grantra signed for you")
			}
			// Signed by this tenant's CA, for this user (the target name),
			// within its validity.
			perms, err := checker.Authenticate(conn, key)
			if err != nil {
				return nil, err
			}
			id, ok := strings.CutPrefix(cert.KeyId, sshaccess.KeyIDPrefix)
			if !ok {
				return nil, errors.New("certificate was not signed for a person")
			}
			var g sshaccess.Grant
			err = db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
				g, err = sshaccess.Authorize(ctx, tx, id, cert.Serial, conn.User())
				return err
			})
			if err != nil {
				s.Log.WarnContext(ctx, "SSH gateway refused a certificate", "target", conn.User(), "certificate_id", id,
					"client", conn.RemoteAddr().String(), "reason", err.Error())
				return nil, err
			}
			perms.ExtraData = map[any]any{grantKey: g}
			return perms, nil
		},
	}
	conf.AddHostKey(s.HostKey)
	return conf
}

func (s *Server) handle(ctx context.Context, nc net.Conn) {
	defer nc.Close()
	nc.SetDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
	conn, chans, reqs, err := ssh.NewServerConn(nc, s.config(ctx))
	if err != nil {
		s.Log.DebugContext(ctx, "SSH gateway handshake failed", "client", nc.RemoteAddr().String(), "err", err)
		return
	}
	nc.SetDeadline(time.Time{}) //nolint:errcheck
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	// No port forwarding or other global requests.
	go ssh.DiscardRequests(reqs)
	g := conn.Permissions.ExtraData[grantKey].(sshaccess.Grant)
	var wg sync.WaitGroup
	for nch := range chans {
		if nch.ChannelType() != "session" {
			nch.Reject(ssh.Prohibited, "the Grantra gateway only opens shells and commands") //nolint:errcheck
			continue
		}
		ch, creqs, err := nch.Accept()
		if err != nil {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.session(ctx, conn, g, ch, creqs)
		}()
	}
	wg.Wait()
}

type ptyRequest struct {
	Term          string
	Width, Height uint32
	PixW, PixH    uint32
	Modes         string
}

type windowChange struct {
	Width, Height uint32
	PixW, PixH    uint32
}

// session runs one session channel: it waits for a shell or exec request,
// then bridges it to the target.
func (s *Server) session(ctx context.Context, conn *ssh.ServerConn, g sshaccess.Grant, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	var pty *ptyRequest
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			var p ptyRequest
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				req.Reply(false, nil) //nolint:errcheck
				continue
			}
			pty = &p
			req.Reply(true, nil) //nolint:errcheck
		case "shell", "exec":
			var command string
			if req.Type == "exec" {
				var e struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &e); err != nil {
					req.Reply(false, nil) //nolint:errcheck
					continue
				}
				command = e.Command
			}
			req.Reply(true, nil) //nolint:errcheck
			s.bridge(ctx, conn, g, ch, reqs, pty, command, req.Type == "exec")
			return
		default:
			// No sftp or other subsystems, agent or X11 forwarding: what
			// they carry would not be recorded.
			if req.WantReply {
				req.Reply(false, nil) //nolint:errcheck
			}
		}
	}
}

func (s *Server) bridge(ctx context.Context, conn *ssh.ServerConn, g sshaccess.Grant, ch ssh.Channel, reqs <-chan *ssh.Request,
	pty *ptyRequest, command string, exec bool) {
	info := sshaccess.Session{TargetID: g.TargetID, TargetName: g.TargetName, Login: g.Login, IdentityID: g.IdentityID,
		CertificateID: g.CertificateID, ClientAddress: conn.RemoteAddr().String(), Gateway: s.Name, Kind: "shell",
		Command: command, Width: 80, Height: 24}
	if exec {
		info.Kind = "exec"
	}
	term := ""
	if pty != nil {
		info.Width, info.Height, term = int(min(pty.Width, 1000)), int(min(pty.Height, 1000)), pty.Term
	}
	err := db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
		info.ID, err = sshaccess.StartSession(ctx, tx, s.TenantID, info)
		return err
	})
	if err != nil {
		s.Log.ErrorContext(ctx, "SSH gateway could not record a session start", "err", err)
		fmt.Fprintf(ch.Stderr(), "Grantra could not start recording this session; try again.\r\n")
		sendExit(ch, 255)
		return
	}
	log := s.Log.With("session", info.ID, "target", g.TargetName, "identity", g.IdentityID)
	rec := newRecorder(time.Now, s.MaxRecording, info.Width, info.Height, term, g.TargetName+" as "+g.Login)
	rec.lines.noEcho = pty == nil
	if exec {
		rec.command(command)
	}

	// Store the recording as it goes, so a crash loses seconds of it.
	flushCtx, stopFlush := context.WithCancel(context.WithoutCancel(ctx))
	flushed := make(chan struct{})
	seq := 0
	flush := func(final bool) {
		data, cmds, truncated := rec.drain(final)
		if len(data) == 0 && len(cmds) == 0 && !final {
			return
		}
		err := db.InTenant(flushCtx, s.Pool, s.TenantID, func(tx pgx.Tx) error {
			if err := sshaccess.AppendRecording(flushCtx, tx, s.TenantID, info.ID, seq, data, truncated); err != nil {
				return err
			}
			return sshaccess.AddCommands(flushCtx, tx, s.TenantID, info.ID, cmds)
		})
		if err != nil {
			log.ErrorContext(flushCtx, "SSH gateway could not store a recording", "err", err)
			return
		}
		if len(data) > 0 {
			seq++
		}
	}
	go func() {
		defer close(flushed)
		t := time.NewTicker(s.FlushEvery)
		defer t.Stop()
		for {
			select {
			case <-flushCtx.Done():
				return
			case <-t.C:
				flush(false)
			}
		}
	}()

	end := func(reason, errText string, exit *int) {
		stopFlush()
		<-flushed
		flushCtx = context.WithoutCancel(ctx)
		flush(true)
		err := db.InTenant(flushCtx, s.Pool, s.TenantID, func(tx pgx.Tx) error {
			return sshaccess.EndSession(flushCtx, tx, s.TenantID, info.ID, reason, errText, exit)
		})
		if err != nil {
			log.ErrorContext(flushCtx, "SSH gateway could not record a session end", "err", err)
		}
		log.InfoContext(flushCtx, "SSH session ended", "reason", reason, "error", errText)
	}

	client, tsess, err := s.connect(ctx, g, info.ID, pty)
	if err != nil {
		log.WarnContext(ctx, "SSH gateway could not reach a target", "err", err)
		fmt.Fprintf(ch.Stderr(), "Grantra could not open %s: %v\r\n", g.TargetName, err)
		sendExit(ch, 255)
		end(sshaccess.EndFailed, err.Error(), nil)
		return
	}
	defer client.Close()
	// When the person's connection drops, the target's goes with it.
	go func() {
		conn.Wait() //nolint:errcheck
		client.Close()
	}()
	log.InfoContext(ctx, "SSH session started", "kind", info.Kind)

	stdin, err1 := tsess.StdinPipe()
	stdout, err2 := tsess.StdoutPipe()
	stderr, err3 := tsess.StderrPipe()
	if err := errors.Join(err1, err2, err3); err != nil {
		end(sshaccess.EndFailed, err.Error(), nil)
		return
	}
	if exec {
		err = tsess.Start(command)
	} else {
		err = tsess.Shell()
	}
	if err != nil {
		fmt.Fprintf(ch.Stderr(), "Grantra could not start the session on %s: %v\r\n", g.TargetName, err)
		sendExit(ch, 255)
		end(sshaccess.EndFailed, err.Error(), nil)
		return
	}

	// Why the gateway ended the session, when it did.
	var (
		stopMu sync.Mutex
		stopBy string
	)
	stopWith := func(reason, msg string) {
		stopMu.Lock()
		defer stopMu.Unlock()
		if stopBy != "" {
			return
		}
		stopBy = reason
		fmt.Fprintf(ch.Stderr(), "\r\n[Grantra] %s\r\n", msg)
		client.Close()
	}

	// Window changes, and nothing else, go on to the target.
	go func() {
		for req := range reqs {
			ok := false
			if req.Type == "window-change" {
				var w windowChange
				if ssh.Unmarshal(req.Payload, &w) == nil {
					ok = tsess.WindowChange(int(w.Height), int(w.Width)) == nil
					rec.resize(int(w.Width), int(w.Height))
				}
			}
			if req.WantReply {
				req.Reply(ok, nil) //nolint:errcheck
			}
		}
	}()
	go func() {
		_, _ = io.Copy(stdin, readFunc(func(p []byte) (int, error) {
			n, err := ch.Read(p)
			if n > 0 {
				rec.input(p[:n])
			}
			return n, err
		}))
		stdin.Close()
	}()
	var outputs sync.WaitGroup
	outputs.Add(2)
	for _, pipe := range []struct {
		r io.Reader
		w io.Writer
	}{{stdout, ch}, {stderr, ch.Stderr()}} {
		go func() {
			defer outputs.Done()
			_, _ = io.Copy(writeFunc(func(p []byte) (int, error) {
				rec.output(p)
				return pipe.w.Write(p)
			}), pipe.r)
		}()
	}

	// Access is checked again while the session runs.
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go func() {
		t := time.NewTicker(s.CheckEvery)
		defer t.Stop()
		for {
			select {
			case <-watchCtx.Done():
				if ctx.Err() != nil {
					stopWith("shutdown", "The Grantra gateway is stopping; this session ends now.")
				}
				return
			case <-t.C:
			}
			var reason string
			err := db.InTenant(watchCtx, s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
				reason, err = sshaccess.Heartbeat(watchCtx, tx, info.ID)
				return err
			})
			switch {
			case err != nil:
				if watchCtx.Err() == nil {
					log.WarnContext(watchCtx, "SSH gateway could not check a session's access", "err", err)
				}
			case reason == sshaccess.EndTerminated:
				stopWith(reason, "An admin ended this session.")
				return
			case reason != "":
				stopWith(reason, "Your access to "+g.TargetName+" ended, so this session ends now.")
				return
			}
		}
	}()

	waitErr := tsess.Wait()
	outputs.Wait()
	stopWatch()
	var exit *int
	var ee *ssh.ExitError
	switch {
	case waitErr == nil:
		zero := 0
		exit = &zero
	case errors.As(waitErr, &ee):
		status := ee.ExitStatus()
		exit = &status
	}
	if exit != nil {
		sendExit(ch, *exit)
	}
	stopMu.Lock()
	reason := stopBy
	stopMu.Unlock()
	switch reason {
	case "":
		end(sshaccess.EndClosed, "", exit)
	case "shutdown":
		end(sshaccess.EndClosed, "the gateway stopped", exit)
	default:
		end(reason, "", exit)
	}
}

// connect logs in to the target with a fresh key and a certificate that
// lasts two minutes, checking the target's host key against the one on
// record (learning it the first time).
func (s *Server) connect(ctx context.Context, g sshaccess.Grant, sessionID string, pty *ptyRequest) (*ssh.Client, *ssh.Session, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	key, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, nil, err
	}
	var cert *ssh.Certificate
	err = db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
		cert, err = s.SSH.GatewayCertificate(ctx, tx, s.TenantID, sessionID, g.Login, sshPub)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	signer, err := ssh.NewCertSigner(cert, key)
	if err != nil {
		return nil, nil, err
	}
	conf := &ssh.ClientConfig{
		User:            g.Login,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: s.hostKeyCheck(ctx, g),
		Timeout:         s.DialTimeout,
	}
	if known, _, _, _, err := ssh.ParseAuthorizedKey([]byte(g.HostKey)); err == nil {
		conf.HostKeyAlgorithms = hostKeyAlgorithms(known.Type())
	}
	dialCtx, cancel := context.WithTimeout(ctx, s.DialTimeout)
	defer cancel()
	nc, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", g.Address)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot reach %s: %w", g.Address, err)
	}
	nc.SetDeadline(time.Now().Add(s.DialTimeout)) //nolint:errcheck
	c, chans, reqs, err := ssh.NewClientConn(nc, g.Address, conf)
	if err != nil {
		nc.Close()
		return nil, nil, err
	}
	nc.SetDeadline(time.Time{}) //nolint:errcheck
	client := ssh.NewClient(c, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		return nil, nil, err
	}
	if pty != nil {
		if err := sess.RequestPty(pty.Term, int(pty.Height), int(pty.Width), ssh.TerminalModes{}); err != nil {
			client.Close()
			return nil, nil, err
		}
	}
	return client, sess, nil
}

func hostKeyAlgorithms(keyType string) []string {
	if keyType == ssh.KeyAlgoRSA {
		return []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA}
	}
	return []string{keyType}
}

// ErrHostKeyChanged is returned when a target's host key is not the one on
// record.
var ErrHostKeyChanged = errors.New("the server's host key changed; an admin must check it and click Forget host key in Grantra if the change is expected")

func (s *Server) hostKeyCheck(ctx context.Context, g sshaccess.Grant) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		line := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
		known := g.HostKey
		if known == "" {
			err := db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
				known, err = sshaccess.LearnHostKey(ctx, tx, s.TenantID, g.TargetID, line)
				return err
			})
			if err != nil {
				return err
			}
		}
		if known != line {
			s.Log.WarnContext(ctx, "SSH target host key changed", "target", g.TargetName, "expected", known, "got", line)
			return ErrHostKeyChanged
		}
		return nil
	}
}

func sendExit(ch ssh.Channel, status int) {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, uint32(status))
	ch.SendRequest("exit-status", false, payload) //nolint:errcheck
}

type readFunc func([]byte) (int, error)

func (f readFunc) Read(p []byte) (int, error) { return f(p) }

type writeFunc func([]byte) (int, error)

func (f writeFunc) Write(p []byte) (int, error) { return f(p) }
