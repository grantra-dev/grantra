package proxy

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// fakeGuacd answers the handshake like guacd, sends a screen, and records
// what the browser sends it.
type fakeGuacd struct {
	addr string
	// refuse, when set, is the error guacd answers connect with.
	refuse string

	mu       sync.Mutex
	params   map[string]string
	setup    []string // the instructions before connect
	got      []string // what came after the handshake
	sent     bytes.Buffer
	sessions chan net.Conn
}

var fakeArgs = []string{"hostname", "port", "domain", "username", "password", "width", "height", "dpi", "timezone", "security",
	"ignore-cert", "disable-copy", "disable-paste", "enable-drive", "enable-sftp", "disable-upload", "recording-path"}

func newFakeGuacd(t *testing.T) *fakeGuacd {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	g := &fakeGuacd{addr: l.Addr().String(), sessions: make(chan net.Conn, 10)}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go g.serve(t, c)
		}
	}()
	return g
}

func (g *fakeGuacd) serve(t *testing.T, c net.Conn) {
	defer c.Close()
	r := newGuacReader(bufio.NewReader(c))
	sel, err := r.read()
	if err != nil || sel.opcode() != "select" || sel.args()[0] != "rdp" {
		t.Errorf("select = %v %v", sel.elems(), err)
		return
	}
	c.Write(guacEncode(append([]string{"args", "VERSION_1_5_0"}, fakeArgs...)...)) //nolint:errcheck
	for {
		in, err := r.read()
		if err != nil {
			return
		}
		if in.opcode() != "connect" {
			g.mu.Lock()
			g.setup = append(g.setup, strings.Join(in.elems(), ","))
			g.mu.Unlock()
			continue
		}
		g.mu.Lock()
		g.params = map[string]string{"version": in.args()[0]}
		for i, name := range fakeArgs {
			g.params[name] = in.args()[i+1]
		}
		g.mu.Unlock()
		break
	}
	if g.refuse != "" {
		c.Write(guacEncode("error", g.refuse, "769")) //nolint:errcheck
		return
	}
	screen := string(guacEncode("ready", "$conn")) + string(guacEncode("size", "0", "1024", "768")) + string(guacEncode("sync", "100"))
	c.Write([]byte(screen)) //nolint:errcheck
	g.mu.Lock()
	g.sent.WriteString(screen[len(guacEncode("ready", "$conn")):])
	g.mu.Unlock()
	g.sessions <- c
	for {
		in, err := r.read()
		if err != nil {
			return
		}
		g.mu.Lock()
		g.got = append(g.got, strings.Join(in.elems(), ","))
		g.mu.Unlock()
		// Each key press draws something.
		if in.opcode() == "key" {
			out := guacEncode("sync", "200")
			g.mu.Lock()
			g.sent.Write(out)
			g.mu.Unlock()
			c.Write(out) //nolint:errcheck
		}
	}
}

func (g *fakeGuacd) received() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.got)
}

// desktopServer serves desktop sessions for target t over HTTP, issuing a
// token for whoever ?as= names, as the console does for the person signed
// in.
func (e *env) desktopServer(s *Server, t gateways.Target) string {
	ctx, cancel := context.WithCancel(context.Background())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var g gateways.Grant
		e.must(func(tx pgx.Tx) error {
			tok, err := gateways.Issue(e.ctx, tx, e.tenant, r.URL.Query().Get("as"), t.ID)
			if err != nil {
				return err
			}
			g, err = gateways.Authorize(e.ctx, tx, tok.Token, t.Kind, t.Name)
			return err
		})
		s.ServeRDP(ctx, w, r, g, nil)
	}))
	e.t.Cleanup(func() {
		cancel()
		s.StopRDP()
		srv.Close()
	})
	return strings.TrimPrefix(srv.URL, "http://")
}

// browser is the console's Guacamole client.
type browser struct {
	t       *testing.T
	ws      *websocket.Conn
	pending []instruction
}

func (e *env) openDesktop(addr, query string, header http.Header) (*browser, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, "ws://"+addr+"/rdp?"+query, &websocket.DialOptions{Subprotocols: []string{"guacamole"}, HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	e.t.Cleanup(func() { ws.CloseNow() }) //nolint:errcheck
	return &browser{t: e.t, ws: ws}, resp, nil
}

// next returns the next instruction from the gateway.
func (b *browser) next() (instruction, error) {
	for len(b.pending) == 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_, msg, err := b.ws.Read(ctx)
		cancel()
		if err != nil {
			return instruction{}, err
		}
		r := newGuacReader(bufio.NewReader(bytes.NewReader(msg)))
		for {
			in, err := r.read()
			if err != nil {
				break
			}
			b.pending = append(b.pending, in)
		}
	}
	in := b.pending[0]
	b.pending = b.pending[1:]
	return in, nil
}

func (b *browser) expect(opcode string) instruction {
	b.t.Helper()
	in, err := b.next()
	if err != nil || in.opcode() != opcode {
		b.t.Fatalf("want %q, got %v %v", opcode, in.elems(), err)
	}
	return in
}

// drain reads until the gateway closes the connection, as a browser does.
func (b *browser) drain() {
	for {
		if _, err := b.next(); err != nil {
			return
		}
	}
}

// until skips instructions until one with opcode.
func (b *browser) until(opcode string) instruction {
	b.t.Helper()
	for {
		in, err := b.next()
		if err != nil {
			b.t.Fatalf("want %q, got %v", opcode, err)
		}
		if in.opcode() == opcode {
			return in
		}
	}
}

func (b *browser) send(elems ...string) {
	b.t.Helper()
	if err := b.ws.Write(context.Background(), websocket.MessageText, guacEncode(elems...)); err != nil {
		b.t.Fatal(err)
	}
}

func TestDesktop(t *testing.T) {
	e := newEnv(t)
	guacd := newFakeGuacd(t)
	acct := e.vaultAccount("desktop admin", `CORP\admin`, "desk-pw")
	addr := "10.0.0.9"
	tg := e.target(gateways.Input{Name: "win-1", Kind: gateways.RDP, Settings: gateways.Settings{Address: &addr, VaultAccountID: &acct}})
	ana := e.identity("ana")
	assignment := e.grant(ana, tg)
	srv := e.server()
	srv.Guacd = guacd.addr
	base := e.desktopServer(srv, tg)

	b, _, err := e.openDesktop(base, "as="+ana+"&width=99999&height=768&timezone=Europe/Madrid&dpi=x", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The tunnel opens with the session's id.
	first := b.expect("")
	running := e.session(tg.ID, func(s gateways.Session) bool { return s.EndedAt == nil })
	if len(first.args()) != 1 || first.args()[0] != running.ID || running.Account != `CORP\admin` {
		t.Errorf("first = %v, session %+v", first.elems(), running)
	}
	b.expect("size")
	b.expect("sync")
	guacd.mu.Lock()
	params, setup := guacd.params, guacd.setup
	guacd.mu.Unlock()
	want := map[string]string{"version": "VERSION_1_5_0", "hostname": "10.0.0.9", "port": "3389", "domain": "CORP", "username": "admin",
		"password": "desk-pw", "width": "8192", "height": "768", "dpi": "96", "timezone": "Europe/Madrid", "security": "nla",
		"ignore-cert": "false", "disable-copy": "true", "disable-paste": "false", "enable-drive": "false", "enable-sftp": "false",
		"disable-upload": "true", "recording-path": ""}
	for k, v := range want {
		if params[k] != v {
			t.Errorf("%s = %q, want %q", k, params[k], v)
		}
	}
	if !slices.Equal(setup, []string{"size,8192,768,96", "audio", "video", "image,image/png,image/jpeg,image/webp",
		"timezone,Europe/Madrid", "name,ana"}) {
		t.Errorf("setup = %v", setup)
	}

	// The client's pings are answered by the gateway; input goes to guacd;
	// file transfers and anything else stay out.
	b.send("", "ping", "12345")
	if p := b.expect(""); !slices.Equal(p.args(), []string{"ping", "12345"}) {
		t.Errorf("ping = %v", p.elems())
	}
	b.send("file", "1", "text/plain", "secrets.txt")
	b.send("put", "1", "2", "text/plain", "x")
	b.send("mouse", "10", "20", "1")
	b.send("key", "65", "1")
	b.expect("sync")
	if got := guacd.received(); !slices.Equal(got, []string{"mouse,10,20,1", "key,65,1"}) {
		t.Errorf("guacd got %v", got)
	}

	// Ending the access ends the session, telling the person.
	e.exec(`DELETE FROM access_assignments WHERE id = $1`, assignment)
	msg := b.expect("error")
	if msg.args()[1] != guacSessionClosed || !strings.Contains(msg.args()[0], "access to win-1 ended") {
		t.Errorf("error = %v", msg.elems())
	}
	if _, err := b.next(); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("after the error: %v", err)
	}
	s := e.session(tg.ID, ended)
	if s.EndReason != gateways.EndAccessEnded || s.ID != running.ID {
		t.Errorf("session = %+v", s)
	}
	// What the person saw is what was recorded.
	var rec bytes.Buffer
	e.must(func(tx pgx.Tx) error {
		return gateways.WriteRecording(e.ctx, tx, s.ID, func() {}, &rec)
	})
	guacd.mu.Lock()
	sent := guacd.sent.String()
	guacd.mu.Unlock()
	if rec.String() != sent || s.RecordingBytes != int64(len(sent)) {
		t.Errorf("recording = %q, sent %q (%d bytes)", rec.String(), sent, s.RecordingBytes)
	}

	// Closing the tab closes the session.
	e.grant(ana, tg)
	b, _, err = e.openDesktop(base, "as="+ana, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.expect("")
	b.expect("size")
	b.ws.Close(websocket.StatusNormalClosure, "") //nolint:errcheck
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndClosed || s.Error != "" {
		t.Errorf("closed session = %+v", s)
	}

	// Pages of other sites cannot open desktops with the person's cookie.
	_, resp, err := e.openDesktop(base, "as="+ana, http.Header{"Origin": {"https://evil.example"}})
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-site: %v %v", resp, err)
	}
}

func TestDesktopLimitsAndFailures(t *testing.T) {
	e := newEnv(t)
	guacd := newFakeGuacd(t)
	acct := e.vaultAccount("desktop admin", "admin", "desk-pw")
	addr := "10.0.0.9"
	tg := e.target(gateways.Input{Name: "win-1", Kind: gateways.RDP, Settings: gateways.Settings{Address: &addr, VaultAccountID: &acct,
		TLSMode: ptrTo(gateways.TLSRequire), Clipboard: ptrTo(gateways.ClipboardBoth)}})
	ana := e.identity("ana")
	e.grant(ana, tg)

	// A session whose recording is full ends: nothing reaches the person
	// unrecorded.
	small := e.server()
	small.Guacd = guacd.addr
	small.MaxRecording = 40
	b, _, err := e.openDesktop(e.desktopServer(small, tg), "as="+ana, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.expect("")
	b.expect("size")
	b.expect("sync")
	b.send("key", "65", "1")
	if msg := b.expect("error"); !strings.Contains(msg.args()[0], "recording limit") {
		t.Errorf("full: %v", msg.elems())
	}
	b.drain()
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndRecordingFull || s.RecordingBytes > 40 {
		t.Errorf("full session = %+v", s)
	}
	guacd.mu.Lock()
	if guacd.params["ignore-cert"] != "true" || guacd.params["disable-copy"] != "false" {
		t.Errorf("params = %v", guacd.params)
	}
	guacd.mu.Unlock()

	// guacd refusing the connection fails the session, saying why.
	guacd.refuse = "Authentication failure (invalid credentials?)"
	srv := e.server()
	srv.Guacd = guacd.addr
	b, _, err = e.openDesktop(e.desktopServer(srv, tg), "as="+ana, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg := b.expect("error"); msg.args()[1] != "769" || !strings.Contains(msg.args()[0], "Authentication failure") {
		t.Errorf("refused: %v", msg.elems())
	}
	b.drain()
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndFailed || !strings.Contains(s.Error, "Authentication failure") {
		t.Errorf("refused session = %+v", s)
	}

	// So does guacd being away.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	away := l.Addr().String()
	l.Close()
	srv = e.server()
	srv.Guacd = away
	b, _, err = e.openDesktop(e.desktopServer(srv, tg), "as="+ana, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg := b.expect("error"); msg.args()[1] != guacUpstreamNotFound {
		t.Errorf("away: %v", msg.elems())
	}
	b.drain()
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndFailed || !strings.Contains(s.Error, "could not reach guacd") {
		t.Errorf("away session = %+v", s)
	}

	// Stopping the gateway ends the sessions running.
	guacd.refuse = ""
	srv = e.server()
	srv.Guacd = guacd.addr
	b, _, err = e.openDesktop(e.desktopServer(srv, tg), "as="+ana, nil)
	if err != nil {
		t.Fatal(err)
	}
	b.expect("")
	stopped := make(chan struct{})
	go func() { srv.StopRDP(); close(stopped) }()
	if msg := b.until("error"); !strings.Contains(msg.args()[0], "restarting") {
		t.Errorf("stop: %v", msg.elems())
	}
	b.drain()
	<-stopped
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndClosed || s.Error != "the gateway stopped" {
		t.Errorf("stopped session = %+v", s)
	}
}

func ptrTo[T any](v T) *T { return &v }

// TestDesktopWithGuacd opens a real desktop through a real guacd, when the
// environment has them: GRANTRA_TEST_GUACD_ADDR, and GRANTRA_TEST_RDP_HOST
// (host:port as guacd reaches it) with GRANTRA_TEST_RDP_LOGIN and
// GRANTRA_TEST_RDP_PASSWORD.
func TestDesktopWithGuacd(t *testing.T) {
	guacdAddr, host := os.Getenv("GRANTRA_TEST_GUACD_ADDR"), os.Getenv("GRANTRA_TEST_RDP_HOST")
	if guacdAddr == "" || host == "" {
		t.Skip("set GRANTRA_TEST_GUACD_ADDR and GRANTRA_TEST_RDP_HOST to test against a real guacd and desktop (and GRANTRA_TEST_RDP_SECURITY=tls for one without NLA, such as xrdp)")
	}
	e := newEnv(t)
	acct := e.vaultAccount("desktop", os.Getenv("GRANTRA_TEST_RDP_LOGIN"), os.Getenv("GRANTRA_TEST_RDP_PASSWORD"))
	tg := e.target(gateways.Input{Name: "desktop", Kind: gateways.RDP, Settings: gateways.Settings{Address: &host, VaultAccountID: &acct,
		TLSMode: ptrTo(gateways.TLSRequire), RDPSecurity: ptrTo(os.Getenv("GRANTRA_TEST_RDP_SECURITY"))}})
	ana := e.identity("ana")
	e.grant(ana, tg)
	srv := e.server()
	srv.Guacd = guacdAddr
	b, _, err := e.openDesktop(e.desktopServer(srv, tg), "as="+ana+"&width=1024&height=768", nil)
	if err != nil {
		t.Fatal(err)
	}
	b.expect("")
	// The desktop draws itself: images, then a sync per frame, which the
	// client acknowledges.
	images := 0
	deadline := time.Now().Add(30 * time.Second)
	for images < 5 && time.Now().Before(deadline) {
		in, err := b.next()
		if err != nil {
			t.Fatal(err)
		}
		switch in.opcode() {
		case "img":
			images++
		case "sync":
			b.send("sync", in.args()[0])
		case "error":
			t.Fatalf("desktop error: %v", in.elems())
		}
	}
	if images < 5 {
		t.Fatalf("only %d images", images)
	}
	b.send("disconnect")
	s := e.session(tg.ID, ended)
	if s.EndReason != gateways.EndClosed || s.RecordingBytes < 1000 {
		t.Errorf("session = %+v", s)
	}
}
