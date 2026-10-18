package proxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// Desktops open in the browser. The console's page runs Apache Guacamole's
// JavaScript client, which talks the Guacamole protocol over a WebSocket to
// ServeRDP; ServeRDP relays it to guacd, which logs in to the desktop over
// RDP with the vault account. The browser never sees that password: guacd
// gets it from the gateway, in the handshake.
//
// Everything guacd sends the browser is recorded before it is sent, so the
// screen can be played back; what the person types is not (it would hold
// passwords).

// rdpSessions tracks the running desktop sessions, so the gateway can end
// them and wait for them when it stops.
type rdpSessions struct {
	mu      sync.Mutex
	closed  bool
	live    map[*live]bool
	running sync.WaitGroup
}

func (rs *rdpSessions) add(l *live) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closed {
		return false
	}
	if rs.live == nil {
		rs.live = map[*live]bool{}
	}
	rs.live[l] = true
	rs.running.Add(1)
	return true
}

func (rs *rdpSessions) done(l *live) {
	rs.mu.Lock()
	delete(rs.live, l)
	rs.mu.Unlock()
	rs.running.Done()
}

// StopRDP ends the running desktop sessions, recording why, and waits for
// them; new ones are refused.
func (s *Server) StopRDP() {
	s.rdp.mu.Lock()
	s.rdp.closed = true
	running := make([]*live, 0, len(s.rdp.live))
	for l := range s.rdp.live {
		running = append(running, l)
	}
	s.rdp.mu.Unlock()
	for _, l := range running {
		l.halt(stopShutdown, "Grantra is restarting; this session ends now. Connect again in a moment.")
	}
	s.rdp.running.Wait()
}

// Guacamole status codes the browser's client understands.
const (
	guacUpstreamError    = "515"
	guacUpstreamNotFound = "519"
	guacSessionClosed    = "521"
	guacForbidden        = "771"
)

// The instructions a browser may send guacd: input, display size,
// clipboard and the acknowledgements of streams. File transfer, audio
// input and anything else is dropped (guacd has drives and audio input
// off anyway).
var rdpClientOpcodes = []string{"ack", "blob", "clipboard", "disconnect", "end", "key", "mouse", "nop", "size", "sync", "touch"}

var timezonePattern = regexp.MustCompile(`^[A-Za-z0-9_+-]{1,32}(/[A-Za-z0-9_+-]{1,32}){0,2}$`)

// rdpDisplay is the screen the browser asked for: ?width=&height=&dpi=
// and ?timezone=, all optional.
func rdpDisplay(r *http.Request) (width, height, dpi, timezone string) {
	q := r.URL.Query()
	num := func(name string, def, lo, hi int) string {
		n, err := strconv.Atoi(q.Get(name))
		if err != nil {
			n = def
		}
		return strconv.Itoa(min(max(n, lo), hi))
	}
	width, height, dpi = num("width", 1280, 200, 8192), num("height", 800, 200, 8192), num("dpi", 96, 48, 400)
	if tz := q.Get("timezone"); timezonePattern.MatchString(tz) {
		timezone = tz
	}
	return width, height, dpi, timezone
}

// splitDomain splits a Windows login, DOMAIN\user, into its domain and
// user name. Other forms (user, user@domain) are the user name.
func splitDomain(login string) (domain, user string) {
	if d, u, ok := strings.Cut(login, `\`); ok && d != "" && u != "" {
		return d, u
	}
	return "", login
}

// rdpParameters are the connection parameters Grantra gives guacd, by
// name; every other parameter is left empty, guacd's default.
func rdpParameters(t gateways.Target, username, password, width, height, dpi, timezone string) map[string]string {
	host, port, _ := net.SplitHostPort(t.Address)
	domain, user := splitDomain(username)
	p := map[string]string{
		"hostname": host, "port": port, "domain": domain, "username": user, "password": password,
		"width": width, "height": height, "dpi": dpi, "timezone": timezone,
		// Never "any": it lets whoever sits in between pick the legacy RDP
		// encryption and read the password.
		"security": t.RDPSecurity, "ignore-cert": strconv.FormatBool(t.TLSMode == gateways.TLSRequire),
		"client-name":   "Grantra",
		"resize-method": "display-update",
		// Nothing leaves or enters the desktop but the screen, input and
		// the clipboard as the target allows.
		"disable-audio": "true", "enable-audio-input": "false", "enable-printing": "false", "enable-drive": "false",
		"disable-download": "true", "disable-upload": "true", "enable-sftp": "false",
		"disable-copy":  strconv.FormatBool(t.Clipboard != gateways.ClipboardBoth),
		"disable-paste": strconv.FormatBool(t.Clipboard == gateways.ClipboardNone),
	}
	return p
}

// ServeRDP serves a desktop session for g on the WebSocket r asks for, until
// the person closes it, their access ends, or ctx does. The caller checked
// that the person holds the access (g comes from a token issued for this
// session) and that r comes from the console's own pages; originPatterns
// are the hosts it may come from besides r's own.
func (s *Server) ServeRDP(ctx context.Context, w http.ResponseWriter, r *http.Request, g gateways.Grant, originPatterns []string) {
	s.defaults()
	// The session outlives the HTTP server's timeouts for requests.
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Time{})  //nolint:errcheck
	rc.SetWriteDeadline(time.Time{}) //nolint:errcheck
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"guacamole"}, OriginPatterns: originPatterns})
	if err != nil {
		// Accept answered the request.
		s.Log.WarnContext(ctx, "desktop session refused", "target", g.Target.Name, "err", err)
		return
	}
	defer ws.CloseNow() //nolint:errcheck
	ws.SetReadLimit(1 << 20)
	if ws.Subprotocol() != "guacamole" {
		ws.Close(websocket.StatusPolicyViolation, guacForbidden) //nolint:errcheck
		return
	}
	// Writes to the browser: one at a time, each bounded so a stuck browser
	// does not hold the session.
	send := func(msg []byte) error {
		wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return ws.Write(wctx, websocket.MessageText, msg)
	}
	fail := func(code, msg string) {
		send(guacEncode("error", msg, code))          //nolint:errcheck
		ws.Close(websocket.StatusNormalClosure, code) //nolint:errcheck
	}
	if s.Guacd == "" {
		fail(guacUpstreamNotFound, "Desktops are not set up on this Grantra: it needs guacd (GRANTRA_GUACD_ADDR).")
		return
	}

	var (
		connMu sync.Mutex
		conn   net.Conn
		closed bool
		// killMu lets a halt finish telling the person why before the
		// session's own end closes the connection under it.
		killMu sync.Mutex
	)
	kill := func(reason, msg string) {
		killMu.Lock()
		defer killMu.Unlock()
		if msg != "" {
			send(guacEncode("error", msg, guacSessionClosed)) //nolint:errcheck
		}
		ws.Close(websocket.StatusNormalClosure, guacSessionClosed) //nolint:errcheck
		connMu.Lock()
		closed = true
		if conn != nil {
			conn.Close()
		}
		connMu.Unlock()
	}
	user, password, err := s.credentials(ctx, g.Target)
	if err != nil {
		s.Log.ErrorContext(ctx, "gateway could not open a desktop's vault account", "target", g.Target.Name, "err", err)
		fail(guacUpstreamError, "Grantra could not open the account it logs in to "+g.Target.Name+" with.")
		return
	}
	l, err := s.begin(ctx, g, gateways.Session{ClientAddress: r.RemoteAddr, Account: user}, kill)
	if err != nil {
		s.Log.ErrorContext(ctx, "gateway could not start a desktop session", "target", g.Target.Name, "err", err)
		fail(guacUpstreamError, publicError(err))
		return
	}
	if !s.rdp.add(l) {
		l.halt(stopShutdown, "Grantra is restarting; connect again in a moment.")
		l.finish("")
		return
	}
	defer s.rdp.done(l)
	var errText string
	defer func() { l.finish(errText) }()
	// failSession tells the person why the session failed, then ends it.
	failSession := func(code, msg string) {
		send(guacEncode("error", msg, code)) //nolint:errcheck
		l.halt(gateways.EndFailed, "")
	}

	dialer := net.Dialer{Timeout: s.DialTimeout}
	c, err := dialer.DialContext(ctx, "tcp", s.Guacd)
	if err != nil {
		l.log.ErrorContext(ctx, "gateway could not reach guacd", "guacd", s.Guacd, "err", err)
		errText = "could not reach guacd: " + err.Error()
		failSession(guacUpstreamNotFound, "Grantra cannot reach its desktop proxy (guacd) right now.")
		return
	}
	connMu.Lock()
	conn = c
	stopped := closed
	connMu.Unlock()
	defer c.Close()
	if stopped {
		return
	}
	gr := newGuacReader(bufio.NewReaderSize(c, 64<<10))
	width, height, dpi, tz := rdpDisplay(r)
	if err := guacHandshake(c, gr, rdpParameters(g.Target, user, password, width, height, dpi, tz), width, height, dpi, tz,
		g.IdentityName); err != nil {
		if l.stoppedFor() != "" {
			return
		}
		errText = err.Error()
		var ge *guacError
		if errors.As(err, &ge) {
			failSession(ge.code, "The desktop proxy refused the connection: "+ge.msg)
		} else {
			failSession(guacUpstreamError, "The desktop proxy did not answer as expected.")
		}
		return
	}
	// The client's tunnel opens on its first instruction, which names it.
	if err := send(guacEncode("", l.id)); err != nil {
		return
	}

	var wg sync.WaitGroup
	wg.Add(1)
	// The browser's input goes to guacd.
	go func() {
		defer wg.Done()
		defer kill("", "")
		for {
			_, msg, err := ws.Read(context.WithoutCancel(ctx))
			if err != nil {
				return
			}
			br := newGuacReader(bufio.NewReader(bytes.NewReader(msg)))
			for {
				in, err := br.read()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					// A client sending what is not Guacamole is not one.
					return
				}
				switch {
				case in.opcode() == "":
					// The client's own tunnel pings, answered here.
					if len(in.args()) == 2 && in.args()[0] == "ping" {
						if send(guacEncode("", "ping", in.args()[1])) != nil {
							return
						}
					}
				case slices.Contains(rdpClientOpcodes, in.opcode()):
					c.SetWriteDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
					if _, err := c.Write(in.raw); err != nil {
						return
					}
					if in.opcode() == "disconnect" {
						return
					}
				}
			}
		}
	}()

	// The desktop's screen goes to the browser, recorded first.
	var batch []byte
	for {
		in, err := gr.read()
		if err != nil {
			if l.stoppedFor() == "" && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				errText = "guacd: " + err.Error()
			}
			break
		}
		if in.opcode() == "error" && len(in.args()) > 0 && errText == "" {
			errText = gateways.CleanText("the desktop: " + in.args()[0])
		}
		if !l.record(in.raw) {
			break
		}
		batch = append(batch, in.raw...)
		// One WebSocket message holds whole instructions, as many as are
		// ready.
		if gr.r.Buffered() == 0 || len(batch) >= 64<<10 {
			if send(batch) != nil {
				break
			}
			batch = batch[:0]
		}
		if in.opcode() == "disconnect" {
			break
		}
	}
	if len(batch) > 0 && l.stoppedFor() == "" {
		send(batch) //nolint:errcheck
	}
	kill("", "")
	wg.Wait()
}

// guacError is an error guacd answered the handshake with.
type guacError struct{ msg, code string }

func (e *guacError) Error() string {
	return "guacd refused the connection: " + e.msg + " (" + e.code + ")"
}

// guacHandshake asks guacd for an RDP connection with params, and waits for
// it to be ready.
func guacHandshake(c net.Conn, gr *guacReader, params map[string]string, width, height, dpi, tz, name string) error {
	c.SetDeadline(time.Now().Add(30 * time.Second)) //nolint:errcheck
	defer c.SetDeadline(time.Time{})                //nolint:errcheck
	if _, err := c.Write(guacEncode("select", "rdp")); err != nil {
		return err
	}
	args, err := gr.read()
	if err != nil {
		return err
	}
	if args.opcode() == "error" {
		return handshakeError(args)
	}
	if args.opcode() != "args" {
		return errors.New("guacd answered select with " + args.opcode())
	}
	names := args.args()
	values := []string{}
	if len(names) > 0 && strings.HasPrefix(names[0], "VERSION_") {
		values = append(values, names[0])
		names = names[1:]
	}
	for _, n := range names {
		values = append(values, params[n])
	}
	var out []byte
	out = append(out, guacEncode("size", width, height, dpi)...)
	out = append(out, guacEncode("audio")...)
	out = append(out, guacEncode("video")...)
	out = append(out, guacEncode("image", "image/png", "image/jpeg", "image/webp")...)
	if tz != "" {
		out = append(out, guacEncode("timezone", tz)...)
	}
	out = append(out, guacEncode("name", name)...)
	out = append(out, guacEncode(append([]string{"connect"}, values...)...)...)
	if _, err := c.Write(out); err != nil {
		return err
	}
	ready, err := gr.read()
	if err != nil {
		return err
	}
	switch ready.opcode() {
	case "ready":
		return nil
	case "error":
		return handshakeError(ready)
	}
	return errors.New("guacd answered connect with " + ready.opcode())
}

func handshakeError(in instruction) error {
	e := &guacError{msg: "unknown error", code: guacUpstreamError}
	if a := in.args(); len(a) >= 2 {
		e.msg = gateways.CleanText(a[0])
		if _, err := strconv.Atoi(a[1]); err == nil && len(a[1]) <= 5 {
			e.code = a[1]
		}
	}
	return e
}
