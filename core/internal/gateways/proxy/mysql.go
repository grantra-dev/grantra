package proxy

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // the MySQL protocol's own hashes
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// ServeMySQL runs the MySQL gateway on l until ctx ends. People connect as
// the user named like the target, with their token as the password, over
// TLS:
//
//	mysql -h gateway -P 13306 -u orders-db -p --ssl-mode=REQUIRED
func (s *Server) ServeMySQL(ctx context.Context, l net.Listener) error {
	return s.serve(ctx, l, s.handleMySQL)
}

// Capability flags.
const (
	myLongPassword     = 1 << 0
	myFoundRows        = 1 << 1
	myLongFlag         = 1 << 2
	myConnectWithDB    = 1 << 3
	myLocalFiles       = 1 << 7
	myIgnoreSpace      = 1 << 8
	myProtocol41       = 1 << 9
	myInteractive      = 1 << 10
	mySSL              = 1 << 11
	myTransactions     = 1 << 13
	mySecureConnection = 1 << 15
	myMultiStatements  = 1 << 16
	myMultiResults     = 1 << 17
	myPSMultiResults   = 1 << 18
	myPluginAuth       = 1 << 19
	myPluginAuthLenenc = 1 << 21
	mySessionTrack     = 1 << 23
	myDeprecateEOF     = 1 << 24
)

// myGatewayCaps is what the gateway offers clients. Not offered: local
// files (LOAD DATA LOCAL would send file contents the gateway does not
// log), compression, connection attributes and query attributes.
const myGatewayCaps = myLongPassword | myFoundRows | myLongFlag | myConnectWithDB | myIgnoreSpace | myProtocol41 |
	myInteractive | mySSL | myTransactions | mySecureConnection | myMultiStatements | myMultiResults | myPSMultiResults |
	myPluginAuth | myPluginAuthLenenc | mySessionTrack | myDeprecateEOF

// mySessionCaps change how the conversation after logging in goes, so the
// client and the database must agree on them.
const mySessionCaps = myFoundRows | myLongFlag | myIgnoreSpace | myProtocol41 | myInteractive | myTransactions |
	myMultiStatements | myMultiResults | myPSMultiResults | mySessionTrack | myDeprecateEOF

// Commands.
const (
	myComQuit        = 0x01
	myComInitDB      = 0x02
	myComQuery       = 0x03
	myComChangeUser  = 0x11
	myComBinlogDump  = 0x12
	myComRegister    = 0x15
	myComPrepare     = 0x16
	myComBinlogGTID  = 0x1e
	myComClone       = 0x23
	myMaxPayload     = 1<<24 - 1
	myHandshakeLimit = 1 << 20
)

const (
	myCachingSHA2 = "caching_sha2_password"
	myNative      = "mysql_native_password"
	myClear       = "mysql_clear_password"
)

// myError is an error the database sent while the gateway logged in.
type myError struct {
	code  uint16
	state string
	msg   string
}

func (e *myError) Error() string { return e.msg }

func myReadPacket(r io.Reader) (payload []byte, seq byte, err error) {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return nil, 0, err
	}
	n := int(head[0]) | int(head[1])<<8 | int(head[2])<<16
	if n > myHandshakeLimit {
		return nil, 0, fmt.Errorf("packet of %d bytes while logging in", n)
	}
	payload = make([]byte, n)
	_, err = io.ReadFull(r, payload)
	return payload, head[3], err
}

func myWritePacket(w io.Writer, seq byte, payload []byte) error {
	head := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), seq}
	_, err := w.Write(append(head, payload...))
	return err
}

func myErrPacket(code uint16, state, msg string) []byte {
	b := []byte{0xff}
	b = binary.LittleEndian.AppendUint16(b, code)
	b = append(b, '#')
	b = append(b, state...)
	return append(b, msg...)
}

// cstring reads a NUL-terminated string at the start of b.
func cstring(b []byte) (string, []byte, bool) {
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return "", nil, false
	}
	return string(b[:i]), b[i+1:], true
}

// lenenc reads a length-encoded integer.
func lenenc(b []byte) (uint64, []byte, bool) {
	if len(b) == 0 {
		return 0, nil, false
	}
	switch b[0] {
	case 0xfc:
		if len(b) < 3 {
			return 0, nil, false
		}
		return uint64(binary.LittleEndian.Uint16(b[1:])), b[3:], true
	case 0xfd:
		if len(b) < 4 {
			return 0, nil, false
		}
		return uint64(b[1]) | uint64(b[2])<<8 | uint64(b[3])<<16, b[4:], true
	case 0xfe:
		if len(b) < 9 {
			return 0, nil, false
		}
		return binary.LittleEndian.Uint64(b[1:]), b[9:], true
	case 0xfb, 0xff:
		return 0, nil, false
	}
	return uint64(b[0]), b[1:], true
}

func appendLenenc(b []byte, n uint64) []byte {
	switch {
	case n < 251:
		return append(b, byte(n))
	case n < 1<<16:
		return binary.LittleEndian.AppendUint16(append(b, 0xfc), uint16(n))
	case n < 1<<24:
		return append(b, 0xfd, byte(n), byte(n>>8), byte(n>>16))
	}
	return binary.LittleEndian.AppendUint64(append(b, 0xfe), n)
}

// myHandshake is a client's handshake response.
type myHandshake struct {
	caps      uint32
	maxPacket uint32
	charset   byte
	user      string
	auth      []byte
	db        string
	plugin    string
}

func myParseHandshake(p []byte) (myHandshake, error) {
	bad := errors.New("malformed handshake response")
	if len(p) < 32 {
		return myHandshake{}, bad
	}
	h := myHandshake{caps: binary.LittleEndian.Uint32(p), maxPacket: binary.LittleEndian.Uint32(p[4:]), charset: p[8]}
	rest := p[32:]
	var ok bool
	if h.user, rest, ok = cstring(rest); !ok {
		return h, bad
	}
	switch {
	case h.caps&myPluginAuthLenenc != 0:
		var n uint64
		if n, rest, ok = lenenc(rest); !ok || n > uint64(len(rest)) {
			return h, bad
		}
		h.auth, rest = rest[:n], rest[n:]
	case h.caps&mySecureConnection != 0:
		if len(rest) < 1 || int(rest[0]) > len(rest)-1 {
			return h, bad
		}
		h.auth, rest = rest[1:1+int(rest[0])], rest[1+int(rest[0]):]
	default:
		var a string
		if a, rest, ok = cstring(rest); !ok {
			return h, bad
		}
		h.auth = []byte(a)
	}
	if h.caps&myConnectWithDB != 0 && len(rest) > 0 {
		if h.db, rest, ok = cstring(rest); !ok {
			return h, bad
		}
	}
	if h.caps&myPluginAuth != 0 && len(rest) > 0 {
		if h.plugin, _, ok = cstring(rest); !ok {
			return h, bad
		}
	}
	return h, nil
}

// myScramble is 20 random bytes without NUL, which ends it on the wire.
func myScramble() ([]byte, error) {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	for i := range b {
		b[i] = b[i]&0x7f | 1
	}
	return b, nil
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i%len(b)]
	}
	return out
}

// myAuthResponse answers a database's challenge with the password, for its
// authentication plugin.
func myAuthResponse(plugin string, password, scramble []byte, secure bool) ([]byte, error) {
	switch plugin {
	case myNative:
		if len(password) == 0 {
			return nil, nil
		}
		h1 := sha1.Sum(password) //nolint:gosec
		h2 := sha1.Sum(h1[:])    //nolint:gosec
		h3 := sha1.New()         //nolint:gosec
		h3.Write(scramble[:min(20, len(scramble))])
		h3.Write(h2[:])
		return xorBytes(h1[:], h3.Sum(nil)), nil
	case myCachingSHA2:
		if len(password) == 0 {
			return nil, nil
		}
		h1 := sha256.Sum256(password)
		h2 := sha256.Sum256(h1[:])
		h3 := sha256.New()
		h3.Write(h2[:])
		h3.Write(scramble[:min(20, len(scramble))])
		return xorBytes(h1[:], h3.Sum(nil)), nil
	case myClear:
		if !secure {
			return nil, errors.New("the database asks for the password in clear text without TLS; set the target's TLS mode to verify or require")
		}
		return append(append([]byte{}, password...), 0), nil
	}
	return nil, fmt.Errorf("the database asks for the %q authentication plugin, which the gateway does not speak", plugin)
}

func (s *Server) handleMySQL(ctx context.Context, nc net.Conn) {
	nc.SetDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
	scramble, err := myScramble()
	if err != nil {
		return
	}
	greeting := []byte{10}
	greeting = append(greeting, "8.0.36-grantra-gateway\x00"...)
	// The connection id clients learn here is not the database's (the
	// gateway connects later), so it is one no database hands out: a
	// client's KILL QUERY with it (on Ctrl+C) finds nothing instead of
	// someone else's query.
	greeting = binary.LittleEndian.AppendUint32(greeting, binary.LittleEndian.Uint32(scramble[:4])|0x80000000)
	greeting = append(greeting, scramble[:8]...)
	greeting = append(greeting, 0)
	greeting = binary.LittleEndian.AppendUint16(greeting, uint16(myGatewayCaps&0xffff))
	greeting = append(greeting, 255) // utf8mb4
	greeting = binary.LittleEndian.AppendUint16(greeting, 2)
	greeting = binary.LittleEndian.AppendUint16(greeting, uint16(myGatewayCaps>>16))
	greeting = append(greeting, 21)
	greeting = append(greeting, make([]byte, 10)...)
	greeting = append(greeting, scramble[8:]...)
	greeting = append(greeting, 0)
	greeting = append(greeting, myCachingSHA2+"\x00"...)
	if err := myWritePacket(nc, 0, greeting); err != nil {
		return
	}
	var client net.Conn = nc
	cr := bufio.NewReader(nc)
	p, seq, err := myReadPacket(cr)
	if err != nil || len(p) < 4 {
		return
	}
	fail := func(seq byte, code uint16, state, msg string) {
		myWritePacket(client, seq, myErrPacket(code, state, msg)) //nolint:errcheck
	}
	caps := binary.LittleEndian.Uint32(p)
	if caps&myProtocol41 == 0 {
		return
	}
	if caps&mySSL == 0 || len(p) != 32 {
		fail(seq+1, 1045, "28000", "The Grantra gateway only accepts TLS: connect with --ssl-mode=REQUIRED (or VERIFY_CA with the gateway CA).")
		return
	}
	// Unlike Postgres, MySQL sends nothing before TLS starts, so the
	// client's hello may already be buffered.
	t := tls.Server(&readConn{nc, cr}, s.TLS)
	if err := t.Handshake(); err != nil {
		return
	}
	client = t
	defer client.Close()
	cr = bufio.NewReader(client)
	if p, seq, err = myReadPacket(cr); err != nil {
		return
	}
	hs, err := myParseHandshake(p)
	if err != nil {
		return
	}
	if hs.caps&myPluginAuth == 0 || hs.caps&mySecureConnection == 0 {
		fail(seq+1, 1045, "28000", "This client is too old for the Grantra gateway: it needs pluggable authentication.")
		return
	}
	// The token comes in clear text over TLS: caching_sha2_password's
	// "full authentication". A client that started with another plugin
	// switches to it first.
	if hs.plugin != myCachingSHA2 {
		seq++
		if err := myWritePacket(client, seq, append(append([]byte{0xfe}, myCachingSHA2+"\x00"...), append(scramble, 0)...)); err != nil {
			return
		}
		if _, seq, err = myReadPacket(cr); err != nil {
			return
		}
	}
	seq++
	if err := myWritePacket(client, seq, []byte{0x01, 0x04}); err != nil {
		return
	}
	pw, seq, err := myReadPacket(cr)
	if err != nil {
		return
	}
	token := string(bytes.TrimRight(pw, "\x00"))
	seq++

	g, err := s.authorize(ctx, token, gateways.MySQL, hs.user)
	if err != nil {
		s.Log.WarnContext(ctx, "MySQL gateway refused a token", "target", hs.user, "client", nc.RemoteAddr().String(), "reason", err.Error())
		fail(seq, 1045, "28000", "Grantra: "+publicError(err))
		return
	}
	database := hs.db
	if database == "" {
		database = g.Target.Database
	}
	username, secret, err := s.credentials(ctx, g.Target)
	if err != nil {
		s.Log.ErrorContext(ctx, "MySQL gateway could not open a target's credentials", "target", g.Target.Name, "err", err)
		fail(seq, 1045, "28000", "Grantra cannot log in to "+g.Target.Name+": its credentials are not set up; tell an admin.")
		return
	}
	var up struct {
		sync.Mutex
		conn   net.Conn
		killed bool
	}
	kill := func(string, string) {
		up.Lock()
		up.killed = true
		if up.conn != nil {
			up.conn.Close()
		}
		up.Unlock()
		client.Close()
	}
	sess, err := s.begin(ctx, g, gateways.Session{ClientAddress: nc.RemoteAddr().String(), Database: database, Account: username}, kill)
	if err != nil {
		s.Log.ErrorContext(ctx, "MySQL gateway could not record a session start", "err", err)
		fail(seq, 1045, "08004", "Grantra could not start logging this session; try again.")
		return
	}
	upstream, ur, ok, err := s.myConnect(ctx, g.Target, username, secret, database, hs)
	if err != nil {
		sess.log.WarnContext(ctx, "MySQL gateway could not reach a target", "err", err)
		code, state, msg := uint16(2003), "HY000", err.Error()
		var me *myError
		if errors.As(err, &me) {
			code, state = me.code, me.state
			// Which login failed how is for the gateway's log.
			if me.code == 1045 {
				msg = "the database refused the gateway's login; tell an admin"
			}
		}
		fail(seq, code, state, fmt.Sprintf("Grantra could not open %s: %s", g.Target.Name, msg))
		sess.finish(err.Error())
		return
	}
	defer upstream.Close()
	up.Lock()
	killed := up.killed
	up.conn = upstream
	up.Unlock()
	if killed {
		sess.finish("")
		return
	}
	if err := myWritePacket(client, seq, ok); err != nil {
		sess.finish("")
		return
	}
	nc.SetDeadline(time.Time{})       //nolint:errcheck
	upstream.SetDeadline(time.Time{}) //nolint:errcheck
	sess.log.InfoContext(ctx, "MySQL session started", "database", database)

	done := make(chan struct{}, 2)
	go func() {
		defer func() { done <- struct{}{} }()
		io.Copy(client, ur) //nolint:errcheck
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		uw := bufio.NewWriter(upstream)
		more := false
		for {
			err := myCopyPacket(uw, cr, &more, func(cmd byte, start []byte) bool {
				switch cmd {
				case myComQuery, myComPrepare:
					return sess.event(gateways.EventQuery, string(start))
				case myComInitDB:
					return sess.event(gateways.EventQuery, "USE "+string(start))
				case myComChangeUser, myComBinlogDump, myComBinlogGTID, myComRegister, myComClone:
					// Another login, or a replication stream: neither
					// would be logged.
					sess.log.WarnContext(ctx, "MySQL gateway refused a command", "command", cmd)
					return false
				}
				return true
			})
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

// myCopyPacket copies one packet from r to w. A packet with sequence 0
// starts a command: inspect gets the command byte and the start of its
// argument first, and the packet is dropped (and copying stops) when it
// returns false. Any other packet must carry on a command too long for
// one packet (more says so, and is updated): a client sends nothing else
// once logged in, and a command passed off as one would go unlogged.
func myCopyPacket(w *bufio.Writer, r *bufio.Reader, more *bool, inspect func(cmd byte, start []byte) bool) error {
	var head [4]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return err
	}
	n := int64(head[0]) | int64(head[1])<<8 | int64(head[2])<<16
	if (head[3] == 0) == *more {
		return errors.New("packet out of order")
	}
	*more = n == myMaxPayload
	var start []byte
	if head[3] == 0 && n > 0 {
		start = make([]byte, min(n, pgLogHead))
		if _, err := io.ReadFull(r, start); err != nil {
			return err
		}
		if !inspect(start[0], start[1:]) {
			return errors.New("stopped")
		}
	}
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	if _, err := w.Write(start); err != nil {
		return err
	}
	_, err := io.CopyN(w, r, n-int64(len(start)))
	return err
}

// myConnect logs in to the database for hs's client and returns the
// connection and the database's OK packet.
func (s *Server) myConnect(ctx context.Context, t gateways.Target, username, password, database string, hs myHandshake) (net.Conn, *bufio.Reader, []byte, error) {
	dialCtx, cancel := context.WithTimeout(ctx, s.DialTimeout)
	defer cancel()
	nc, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", t.Address)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("cannot reach %s: %w", t.Address, err)
	}
	conn, r, ok, err := s.myLogin(nc, t, username, password, database, hs)
	if err != nil {
		nc.Close()
		return nil, nil, nil, err
	}
	return conn, r, ok, nil
}

func (s *Server) myLogin(nc net.Conn, t gateways.Target, username, password, database string, hs myHandshake) (net.Conn, *bufio.Reader, []byte, error) {
	nc.SetDeadline(time.Now().Add(s.DialTimeout)) //nolint:errcheck
	var conn net.Conn = nc
	r := bufio.NewReader(nc)
	p, _, err := myReadPacket(r)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(p) > 0 && p[0] == 0xff {
		return nil, nil, nil, myParseErr(p)
	}
	// The greeting: protocol 10, version, connection id, scramble, flags.
	bad := errors.New("the database sent a greeting the gateway does not understand")
	if len(p) < 1 || p[0] != 10 {
		return nil, nil, nil, bad
	}
	_, rest, ok := cstring(p[1:])
	if !ok || len(rest) < 4+8+1+2+1+2+2+1+10 {
		return nil, nil, nil, bad
	}
	rest = rest[4:]
	scramble := append([]byte{}, rest[:8]...)
	rest = rest[9:]
	upCaps := uint32(binary.LittleEndian.Uint16(rest))
	upCaps |= uint32(binary.LittleEndian.Uint16(rest[5:])) << 16
	authLen := int(rest[7])
	rest = rest[18:]
	if upCaps&mySecureConnection != 0 {
		n := max(13, authLen-8)
		if len(rest) < n {
			return nil, nil, nil, bad
		}
		scramble = append(scramble, bytes.TrimRight(rest[:n], "\x00")...)
		rest = rest[n:]
	}
	plugin := myNative
	if upCaps&myPluginAuth != 0 {
		if name, _, ok := cstring(rest); ok {
			plugin = name
		} else if len(rest) > 0 {
			plugin = string(rest)
		}
	}
	const needed = myProtocol41 | mySecureConnection | myPluginAuth
	if upCaps&needed != needed {
		return nil, nil, nil, errors.New("the database is too old for the gateway: it needs MySQL 5.7 or later")
	}
	caps := hs.caps&myGatewayCaps&mySessionCaps | needed
	if missing := caps &^ upCaps; missing != 0 {
		return nil, nil, nil, fmt.Errorf("the database lacks protocol features the client uses (capability flags %#x)", missing)
	}
	caps |= upCaps & myPluginAuthLenenc
	if database != "" {
		caps |= myConnectWithDB
	}
	seq := byte(1)
	secure := false
	if conf, err := upstreamTLS(t, hostOf(t.Address)); err != nil {
		return nil, nil, nil, err
	} else if conf != nil {
		if upCaps&mySSL == 0 {
			return nil, nil, nil, errors.New("the database does not offer TLS; set the target's TLS mode to disable if that is expected")
		}
		caps |= mySSL
		req := binary.LittleEndian.AppendUint32(nil, caps)
		req = binary.LittleEndian.AppendUint32(req, hs.maxPacket)
		req = append(req, hs.charset)
		req = append(req, make([]byte, 23)...)
		if err := myWritePacket(conn, seq, req); err != nil {
			return nil, nil, nil, err
		}
		seq++
		tc := tls.Client(conn, conf)
		if err := tc.Handshake(); err != nil {
			return nil, nil, nil, fmt.Errorf("TLS with the database: %w", err)
		}
		conn, r, secure = tc, bufio.NewReader(tc), true
	}
	pw := []byte(password)
	auth, err := myAuthResponse(plugin, pw, scramble, secure)
	if err != nil {
		return nil, nil, nil, err
	}
	resp := binary.LittleEndian.AppendUint32(nil, caps)
	resp = binary.LittleEndian.AppendUint32(resp, hs.maxPacket)
	resp = append(resp, hs.charset)
	resp = append(resp, make([]byte, 23)...)
	resp = append(resp, username...)
	resp = append(resp, 0)
	if caps&myPluginAuthLenenc != 0 {
		resp = appendLenenc(resp, uint64(len(auth)))
	} else {
		resp = append(resp, byte(len(auth)))
	}
	resp = append(resp, auth...)
	if database != "" {
		resp = append(resp, database...)
		resp = append(resp, 0)
	}
	resp = append(resp, plugin...)
	resp = append(resp, 0)
	if err := myWritePacket(conn, seq, resp); err != nil {
		return nil, nil, nil, err
	}
	for range 5 {
		p, seq, err = myReadPacket(r)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(p) == 0 {
			return nil, nil, nil, bad
		}
		var reply []byte
		switch p[0] {
		case 0x00:
			return conn, r, p, nil
		case 0xff:
			return nil, nil, nil, myParseErr(p)
		case 0xfe:
			// Switch to another plugin, with a new scramble.
			name, data, ok := cstring(p[1:])
			if !ok {
				name, data = string(p[1:]), nil
			}
			plugin, scramble = name, bytes.TrimRight(data, "\x00")
			if reply, err = myAuthResponse(plugin, pw, scramble, secure); err != nil {
				return nil, nil, nil, err
			}
		case 0x01:
			if plugin != myCachingSHA2 || len(p) < 2 {
				return nil, nil, nil, bad
			}
			switch {
			case p[1] == 0x03 && len(p) == 2:
				// Fast authentication worked; the OK follows.
				continue
			case p[1] == 0x04 && len(p) == 2 && secure:
				reply = append(append([]byte{}, pw...), 0)
			case p[1] == 0x04 && len(p) == 2:
				// No TLS: ask for the database's RSA key.
				reply = []byte{0x02}
			default:
				// The RSA key it sent.
				if reply, err = myRSAPassword(p[1:], pw, scramble); err != nil {
					return nil, nil, nil, err
				}
			}
		default:
			return nil, nil, nil, bad
		}
		if err := myWritePacket(conn, seq+1, reply); err != nil {
			return nil, nil, nil, err
		}
	}
	return nil, nil, nil, errors.New("logging in to the database did not finish")
}

// myRSAPassword encrypts the password with the database's RSA public key,
// for caching_sha2_password without TLS.
func myRSAPassword(keyPEM, password, scramble []byte) ([]byte, error) {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, errors.New("the database sent an unreadable RSA key")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rk, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("the database sent a key that is not RSA")
	}
	if len(scramble) == 0 {
		return nil, errors.New("the database sent no scramble")
	}
	return rsa.EncryptOAEP(sha1.New(), rand.Reader, rk, xorBytes(append(append([]byte{}, password...), 0), scramble), nil) //nolint:gosec
}

func myParseErr(p []byte) error {
	e := &myError{code: 1045, state: "28000", msg: "the database refused the login"}
	if len(p) >= 3 {
		e.code = binary.LittleEndian.Uint16(p[1:])
		rest := p[3:]
		if len(rest) >= 6 && rest[0] == '#' {
			e.state, rest = string(rest[1:6]), rest[6:]
		}
		e.msg = string(rest)
	}
	return e
}

func hostOf(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return host
}
