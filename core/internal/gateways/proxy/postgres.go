package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// ServePostgres runs the PostgreSQL gateway on l until ctx ends. People
// connect as the user named like the target, with their token as the
// password, over TLS:
//
//	PGPASSWORD=grt_... psql "host=gateway port=15432 user=orders-db dbname=orders sslmode=require"
func (s *Server) ServePostgres(ctx context.Context, l net.Listener) error {
	return s.serve(ctx, l, s.handlePostgres)
}

// Startup packet codes.
const (
	pgCancelRequest = 80877102
	pgSSLRequest    = 80877103
	pgGSSEncRequest = 80877104
)

// pgMaxMessage bounds one message, as PostgreSQL does.
const pgMaxMessage = 1 << 30

// pgLogHead is how much of a query message is read to log it; the rest is
// passed on without being held.
const pgLogHead = 64 << 10

// runtimeParams are the startup parameters passed on to the database. The
// rest (options, replication, anything unknown) is dropped.
var runtimeParams = []string{"application_name", "client_encoding", "DateStyle", "IntervalStyle", "TimeZone",
	"extra_float_digits", "search_path"}

type pgCancelKey struct {
	pid    uint32
	secret string
}

type pgCancelTarget struct {
	address string
	key     []byte
}

// readConn reads through a bufio.Reader that may hold bytes already read
// from the connection.
type readConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *readConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (s *Server) handlePostgres(ctx context.Context, nc net.Conn) {
	nc.SetDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
	client, cr, startup, err := s.pgStartup(nc)
	if err != nil {
		if !errors.Is(err, errCancelled) {
			s.Log.DebugContext(ctx, "PostgreSQL gateway startup failed", "client", nc.RemoteAddr().String(), "err", err)
		}
		return
	}
	defer client.Close()
	cw := bufio.NewWriter(client)
	// wmu guards cw: the gateway may write to the client from the session's
	// checks while messages from the database are passed on.
	var wmu sync.Mutex
	fatal := func(code, msg string) {
		wmu.Lock()
		defer wmu.Unlock()
		pgSend(cw, &pgproto3.ErrorResponse{Severity: "FATAL", SeverityUnlocalized: "FATAL", Code: code, Message: msg})
		cw.Flush() //nolint:errcheck
	}
	if _, ok := client.(*tls.Conn); !ok {
		fatal("28000", "The Grantra gateway only accepts TLS: connect with sslmode=require (or verify-full with the gateway CA).")
		return
	}
	params := startup.Parameters
	user := params["user"]
	database := params["database"]
	if database == "" {
		database = user
	}
	if r := strings.ToLower(params["replication"]); r != "" && r != "false" && r != "off" && r != "no" && r != "0" {
		fatal("28000", "Replication connections do not go through the Grantra gateway.")
		return
	}
	// The gateway speaks protocol 3.0, and no _pq_ extensions.
	var unknown []string
	for k := range params {
		if strings.HasPrefix(k, "_pq_.") {
			unknown = append(unknown, k)
		}
	}
	if startup.ProtocolVersion != pgproto3.ProtocolVersion30 || len(unknown) > 0 {
		sort.Strings(unknown)
		pgSend(cw, &pgproto3.NegotiateProtocolVersion{NewestMinorProtocol: 0, UnrecognizedOptions: unknown})
	}
	pgSend(cw, &pgproto3.AuthenticationCleartextPassword{})
	if err := cw.Flush(); err != nil {
		return
	}
	token, err := pgReadPassword(cr)
	if err != nil {
		return
	}
	g, err := s.authorize(ctx, token, gateways.Postgres, user)
	if err != nil {
		s.Log.WarnContext(ctx, "PostgreSQL gateway refused a token", "target", user, "client", nc.RemoteAddr().String(), "reason", err.Error())
		fatal("28P01", "Grantra: "+publicError(err))
		return
	}
	if database == user && g.Target.Database != "" {
		database = g.Target.Database
	}
	username, secret, err := s.credentials(ctx, g.Target)
	if err != nil {
		s.Log.ErrorContext(ctx, "PostgreSQL gateway could not open a target's credentials", "target", g.Target.Name, "err", err)
		fatal("08004", "Grantra cannot log in to "+g.Target.Name+": its credentials are not set up; tell an admin.")
		return
	}

	var up struct {
		sync.Mutex
		conn   net.Conn
		killed bool
	}
	// Ending a session tells the person why, between two messages from the
	// database, then closes both sides. Neither side may hold it up for
	// long.
	kill := func(_ string, msg string) {
		up.Lock()
		up.killed = true
		conn := up.conn
		up.Unlock()
		if conn != nil {
			conn.SetReadDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
		}
		client.SetWriteDeadline(time.Now().Add(2 * time.Second)) //nolint:errcheck
		fatal("57P01", "Grantra: "+msg)
		client.Close()
		if conn != nil {
			conn.Close()
		}
	}
	sess, err := s.begin(ctx, g, gateways.Session{ClientAddress: nc.RemoteAddr().String(), Database: database, Account: username}, kill)
	if err != nil {
		s.Log.ErrorContext(ctx, "PostgreSQL gateway could not record a session start", "err", err)
		fatal("08004", "Grantra could not start logging this session; try again.")
		return
	}
	hj, err := s.pgConnect(ctx, g.Target, username, secret, database, params)
	if err != nil {
		sess.log.WarnContext(ctx, "PostgreSQL gateway could not reach a target", "err", err)
		fatal("08001", fmt.Sprintf("Grantra could not open %s: %v", g.Target.Name, err))
		sess.finish(err.Error())
		return
	}
	upstream := hj.Conn
	defer upstream.Close()
	up.Lock()
	killed := up.killed
	up.conn = upstream
	up.Unlock()
	if killed {
		sess.finish("")
		return
	}
	// Cancel requests reach the database through the gateway.
	key := pgCancelKey{hj.PID, hex.EncodeToString(hj.SecretKey)}
	s.cancels.Store(key, pgCancelTarget{address: g.Target.Address, key: hj.SecretKey})
	defer s.cancels.Delete(key)

	// The database said hello; the client hears it from the gateway.
	wmu.Lock()
	pgSend(cw, &pgproto3.AuthenticationOk{})
	names := make([]string, 0, len(hj.ParameterStatuses))
	for k := range hj.ParameterStatuses {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		pgSend(cw, &pgproto3.ParameterStatus{Name: k, Value: hj.ParameterStatuses[k]})
	}
	pgSend(cw, &pgproto3.BackendKeyData{ProcessID: hj.PID, SecretKey: hj.SecretKey})
	pgSend(cw, &pgproto3.ReadyForQuery{TxStatus: hj.TxStatus})
	err = cw.Flush()
	wmu.Unlock()
	if err != nil {
		sess.finish("")
		return
	}
	nc.SetDeadline(time.Time{})       //nolint:errcheck
	upstream.SetDeadline(time.Time{}) //nolint:errcheck
	sess.log.InfoContext(ctx, "PostgreSQL session started", "database", database)

	done := make(chan struct{}, 2)
	// Database to client, message by message, so the gateway can say why
	// it ends a session between two of them.
	go func() {
		defer func() { done <- struct{}{} }()
		ur := bufio.NewReader(upstream)
		for {
			head, err := pgReadHead(ur)
			if err != nil {
				return
			}
			wmu.Lock()
			err = pgCopyBody(cw, ur, head, nil)
			if err == nil && ur.Buffered() == 0 {
				err = cw.Flush()
			}
			wmu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	// Client to database, logging every query on the way.
	go func() {
		defer func() { done <- struct{}{} }()
		uw := bufio.NewWriter(upstream)
		for {
			head, err := pgReadHead(cr)
			if err == nil {
				err = pgCopyBody(uw, cr, head, func(typ byte, start []byte) bool {
					if text := pgQueryText(typ, start); text != "" {
						return sess.event(gateways.EventQuery, text)
					}
					return true
				})
			}
			if err == nil && cr.Buffered() == 0 {
				err = uw.Flush()
			}
			if err != nil {
				uw.Flush() //nolint:errcheck
				return
			}
		}
	}()
	<-done
	client.Close()
	upstream.Close()
	<-done
	sess.finish("")
}

var errCancelled = errors.New("cancel request")

// pgStartup negotiates TLS and reads the startup message. It answers cancel
// requests on its own.
func (s *Server) pgStartup(nc net.Conn) (net.Conn, *bufio.Reader, *pgproto3.StartupMessage, error) {
	conf := s.TLS.Clone()
	conf.NextProtos = []string{"postgresql"}
	var c net.Conn = nc
	r := bufio.NewReader(nc)
	secure := false
	// PostgreSQL 17 clients may start TLS straight away
	// (sslnegotiation=direct), with the "postgresql" ALPN.
	if first, err := r.Peek(1); err != nil {
		return nil, nil, nil, err
	} else if first[0] == 0x16 {
		t := tls.Server(&readConn{nc, r}, conf)
		if err := t.Handshake(); err != nil {
			return nil, nil, nil, err
		}
		if t.ConnectionState().NegotiatedProtocol != "postgresql" {
			return nil, nil, nil, errors.New("direct TLS without the postgresql ALPN")
		}
		c, r, secure = t, bufio.NewReader(t), true
	}
	for {
		var head [4]byte
		if _, err := io.ReadFull(r, head[:]); err != nil {
			return nil, nil, nil, err
		}
		n := int(binary.BigEndian.Uint32(head[:]))
		if n < 8 || n > 10000 {
			return nil, nil, nil, fmt.Errorf("invalid startup packet length %d", n)
		}
		body := make([]byte, n-4)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, nil, nil, err
		}
		switch code := binary.BigEndian.Uint32(body); code {
		case pgSSLRequest:
			if secure {
				return nil, nil, nil, errors.New("SSL requested twice")
			}
			if _, err := c.Write([]byte{'S'}); err != nil {
				return nil, nil, nil, err
			}
			// Anything sent before the TLS handshake could be injected by
			// someone in the middle.
			if r.Buffered() > 0 {
				return nil, nil, nil, errors.New("data received before the TLS handshake")
			}
			t := tls.Server(c, conf)
			if err := t.Handshake(); err != nil {
				return nil, nil, nil, err
			}
			c, r, secure = t, bufio.NewReader(t), true
		case pgGSSEncRequest:
			if _, err := c.Write([]byte{'N'}); err != nil {
				return nil, nil, nil, err
			}
		case pgCancelRequest:
			s.pgForwardCancel(body)
			return nil, nil, nil, errCancelled
		default:
			var m pgproto3.StartupMessage
			if err := m.Decode(body); err != nil {
				return nil, nil, nil, err
			}
			return c, r, &m, nil
		}
	}
}

// pgForwardCancel passes a cancel request on to the database whose session
// it names, when this gateway runs it.
func (s *Server) pgForwardCancel(body []byte) {
	if len(body) < 12 {
		return
	}
	pid := binary.BigEndian.Uint32(body[4:8])
	v, ok := s.cancels.Load(pgCancelKey{pid, hex.EncodeToString(body[8:])})
	if !ok {
		return
	}
	t := v.(pgCancelTarget)
	conn, err := net.DialTimeout("tcp", t.address, s.DialTimeout)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(s.DialTimeout)) //nolint:errcheck
	msg := binary.BigEndian.AppendUint32(nil, uint32(12+len(t.key)))
	msg = binary.BigEndian.AppendUint32(msg, pgCancelRequest)
	msg = binary.BigEndian.AppendUint32(msg, pid)
	msg = append(msg, t.key...)
	conn.Write(msg) //nolint:errcheck
	// The database closes the connection once it read the request.
	io.Copy(io.Discard, conn) //nolint:errcheck
}

// pgReadPassword reads the password message answering a cleartext
// password request.
func pgReadPassword(r *bufio.Reader) (string, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return "", err
	}
	n := int(binary.BigEndian.Uint32(head[1:]))
	if head[0] != 'p' || n < 5 || n > 1024 {
		return "", errors.New("expected a password message")
	}
	body := make([]byte, n-4)
	if _, err := io.ReadFull(r, body); err != nil {
		return "", err
	}
	return string(bytes.TrimRight(body, "\x00")), nil
}

// pgConnect logs in to the database and hands over the raw connection.
func (s *Server) pgConnect(ctx context.Context, t gateways.Target, username, secret, database string, client map[string]string) (*pgconn.HijackedConn, error) {
	host, portText, err := net.SplitHostPort(t.Address)
	if err != nil {
		return nil, err
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return nil, err
	}
	cfg, err := pgconn.ParseConfig("")
	if err != nil {
		return nil, err
	}
	cfg.Host, cfg.Port, cfg.User, cfg.Password, cfg.Database = host, uint16(port), username, secret, database
	if cfg.TLSConfig, err = upstreamTLS(t, host); err != nil {
		return nil, err
	}
	cfg.Fallbacks = nil
	cfg.SSLNegotiation = "postgres"
	cfg.ConnectTimeout = s.DialTimeout
	cfg.MinProtocolVersion, cfg.MaxProtocolVersion = "3.0", "3.0"
	cfg.ValidateConnect, cfg.AfterConnect, cfg.OnNotice, cfg.OnNotification, cfg.OnPgError = nil, nil, nil, nil, nil
	cfg.RuntimeParams = map[string]string{}
	for _, k := range runtimeParams {
		if v, ok := client[k]; ok {
			cfg.RuntimeParams[k] = v
		}
	}
	if cfg.RuntimeParams["application_name"] == "" {
		cfg.RuntimeParams["application_name"] = "grantra-gateway"
	}
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			// Which login failed how is for the gateway's log; the person
			// is told to see an admin.
			if strings.HasPrefix(pe.Code, "28") {
				return nil, errors.New("the database refused the gateway's login (" + pe.Code + "); tell an admin")
			}
			return nil, errors.New(pe.Message)
		}
		return nil, err
	}
	hj, err := conn.Hijack()
	if err != nil {
		conn.Close(ctx) //nolint:errcheck
		return nil, err
	}
	if hj.Frontend.ReadBufferLen() > 0 {
		hj.Conn.Close()
		return nil, errors.New("the database sent more than expected after logging in")
	}
	return hj, nil
}

func pgSend(w *bufio.Writer, msg pgproto3.BackendMessage) {
	buf, err := msg.Encode(nil)
	if err == nil {
		w.Write(buf) //nolint:errcheck
	}
}

// pgReadHead reads a message's type and length.
func pgReadHead(r *bufio.Reader) ([5]byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return head, err
	}
	if n := binary.BigEndian.Uint32(head[1:]); n < 4 || n > pgMaxMessage {
		return head, fmt.Errorf("invalid message length %d", n)
	}
	return head, nil
}

// pgCopyBody writes the message whose head was read, copying its body from
// r to w. When inspect is set, it is given the message type and the start
// of the body of the messages that run statements first, and the message
// is dropped (and copying stops) when it returns false.
func pgCopyBody(w *bufio.Writer, r *bufio.Reader, head [5]byte, inspect func(typ byte, start []byte) bool) error {
	body := int64(binary.BigEndian.Uint32(head[1:])) - 4
	var start []byte
	if inspect != nil && (head[0] == 'Q' || head[0] == 'P' || head[0] == 'F') {
		start = make([]byte, min(body, pgLogHead))
		if _, err := io.ReadFull(r, start); err != nil {
			return err
		}
		if !inspect(head[0], start) {
			return errors.New("stopped")
		}
	}
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	if _, err := w.Write(start); err != nil {
		return err
	}
	_, err := io.CopyN(w, r, body-int64(len(start)))
	return err
}

// pgQueryText is the statement a message runs, for the log: a simple query,
// a statement being prepared, or a function called by OID.
func pgQueryText(typ byte, head []byte) string {
	cstring := func(b []byte) string {
		if i := bytes.IndexByte(b, 0); i >= 0 {
			return string(b[:i])
		}
		return string(b)
	}
	switch typ {
	case 'Q':
		return cstring(head)
	case 'P':
		if i := bytes.IndexByte(head, 0); i >= 0 {
			return cstring(head[i+1:])
		}
	case 'F':
		if len(head) >= 4 {
			return fmt.Sprintf("-- function call (fast path) of OID %d", binary.BigEndian.Uint32(head[:4]))
		}
	}
	return ""
}
