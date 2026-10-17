package proxy

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// fakeCluster is a Kubernetes API server that answers with what it got.
type fakeCluster struct {
	*httptest.Server
	mu      sync.Mutex
	headers []http.Header
	watches chan struct{} // one value per watch that ended
}

func newFakeCluster(t *testing.T) *fakeCluster {
	c := &fakeCluster{watches: make(chan struct{}, 10)}
	c.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.headers = append(c.headers, r.Header.Clone())
		c.mu.Unlock()
		switch {
		case r.URL.Query().Get("watch") == "true":
			w.WriteHeader(http.StatusOK)
			fmt.Fprintln(w, `{"type":"ADDED"}`)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			c.watches <- struct{}{}
		case strings.HasSuffix(r.URL.Path, "/exec"):
			if r.Header.Get("Upgrade") == "" {
				http.Error(w, "upgrade required", http.StatusBadRequest)
				return
			}
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: SPDY/3.1\r\n\r\n") //nolint:errcheck
			rw.Flush()                                                                                             //nolint:errcheck
			line, _ := rw.ReadString('\n')
			rw.WriteString("echo " + line) //nolint:errcheck
			rw.Flush()                     //nolint:errcheck
		case strings.Contains(r.URL.Path, "/secrets"):
			http.Error(w, `{"kind":"Status","code":403}`, http.StatusForbidden)
		default:
			json.NewEncoder(w).Encode(map[string]any{"path": r.URL.Path, "query": r.URL.RawQuery}) //nolint:errcheck
		}
	}))
	c.EnableHTTP2 = true
	c.StartTLS()
	t.Cleanup(c.Close)
	return c
}

func (c *fakeCluster) last() http.Header {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.headers[len(c.headers)-1]
}

func (c *fakeCluster) caPEM() string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Certificate().Raw}))
}

func TestKubernetes(t *testing.T) {
	e := newEnv(t)
	cluster := newFakeCluster(t)
	acct := e.vaultAccount("prod deployer", "grantra-gateway", "sa-token\n")
	ca := cluster.caPEM()
	tg := e.target(gateways.Input{Name: "prod", Kind: gateways.Kubernetes, Settings: gateways.Settings{
		Address: &cluster.URL, VaultAccountID: &acct, CACertificate: &ca}})
	other := e.target(gateways.Input{Name: "staging", Kind: gateways.Kubernetes, Settings: gateways.Settings{
		Address: &cluster.URL, VaultAccountID: &acct, CACertificate: &ca}})
	ana := e.identity("ana")
	assignment := e.grant(ana, tg)
	e.grant(ana, other)
	token := e.token(ana, tg)
	srv := e.server()
	// Events are stored when the session ends, so each has its status.
	srv.FlushEvery = time.Hour
	addr := e.listen(srv.ServeKubernetes)
	base := "https://" + addr + gateways.KubePathPrefix
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: e.roots}, ForceAttemptHTTP2: true}}

	get := func(path, token string, header ...string) (*http.Response, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, base+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		for i := 0; i < len(header); i += 2 {
			req.Header.Add(header[i], header[i+1])
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, string(b)
	}

	for _, c := range []struct {
		path, token string
		code        int
		msg         string
	}{
		{"prod/api/v1/pods", "", http.StatusUnauthorized, "kubeconfig"},
		{"prod/api/v1/pods", gateways.TokenPrefix + "nope", http.StatusUnauthorized, "unknown, expired or revoked"},
		{"staging/api/v1/pods", token, http.StatusForbidden, "the token is for prod"},
	} {
		resp, body := get(c.path, c.token)
		if resp.StatusCode != c.code || !strings.Contains(body, c.msg) || !strings.Contains(body, `"kind":"Status"`) {
			t.Errorf("%s: %d %s", c.path, resp.StatusCode, body)
		}
	}
	if resp, err := client.Get("https://" + addr + "/version"); err != nil || resp.StatusCode != http.StatusNotFound {
		t.Errorf("outside the prefix: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}

	// Whoever the client claims to be, the cluster sees the gateway's
	// service account acting as the person, with the target's groups.
	resp, body := get("prod/api/v1/namespaces/default/pods?limit=5", token,
		"Impersonate-User", "admin", "Impersonate-Group", "system:masters", "X-Remote-User", "admin", "impersonate-extra-scopes", "all")
	if resp.StatusCode != http.StatusOK || resp.ProtoMajor != 2 || !strings.Contains(body, `"path":"/api/v1/namespaces/default/pods"`) ||
		!strings.Contains(body, `"query":"limit=5"`) {
		t.Errorf("pods: %d %s %s", resp.StatusCode, resp.Proto, body)
	}
	h := cluster.last()
	running := e.session(tg.ID, func(s gateways.Session) bool { return s.EndedAt == nil })
	if h.Get("Authorization") != "Bearer sa-token" || h.Get("Impersonate-User") != "grantra:ana@example.com" ||
		!slices.Equal(h.Values("Impersonate-Group"), []string{"grantra:prod"}) || h.Get("X-Remote-User") != "" ||
		h.Get("Impersonate-Extra-Scopes") != "" || h.Get("Impersonate-Extra-Grantra-Identity") != ana ||
		h.Get("Impersonate-Extra-Grantra-Session") != running.ID {
		t.Errorf("the cluster got %v", h)
	}
	if running.Account != "grantra:ana@example.com" || running.Kind != gateways.Kubernetes {
		t.Errorf("session = %+v", running)
	}
	get("prod/version", token)
	get("prod/apis/apps/v1", token)
	if resp, _ := get("prod/api/v1/namespaces/default/secrets", token); resp.StatusCode != http.StatusForbidden {
		t.Errorf("secrets: %d", resp.StatusCode)
	}

	// exec and port-forward upgrade the connection (over HTTP/1.1).
	conn, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: e.roots, ServerName: "127.0.0.1", NextProtos: []string{"http/1.1"}})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "POST %sprod/api/v1/namespaces/default/pods/web/exec?command=sh HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n"+
		"Connection: Upgrade\r\nUpgrade: SPDY/3.1\r\nContent-Length: 0\r\n\r\n", gateways.KubePathPrefix, addr, token)
	br := bufio.NewReader(conn)
	conn.SetDeadline(time.Now().Add(10 * time.Second)) //nolint:errcheck
	upgraded, err := http.ReadResponse(br, nil)
	if err != nil || upgraded.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("exec: %v %v", upgraded, err)
	}
	fmt.Fprint(conn, "ls\n")
	if line, err := br.ReadString('\n'); err != nil || line != "echo ls\n" {
		t.Errorf("exec stream = %q %v", line, err)
	}
	conn.Close()

	// A watch runs until the access ends, then the gateway ends it.
	req, _ := http.NewRequest(http.MethodGet, base+"prod/api/v1/pods?watch=true", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	watch, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if line, err := bufio.NewReader(watch.Body).ReadString('\n'); err != nil || !strings.Contains(line, "ADDED") {
		t.Fatalf("watch = %q %v", line, err)
	}
	e.exec(`DELETE FROM access_assignments WHERE id = $1`, assignment)
	if s := e.session(tg.ID, ended); s.EndReason != gateways.EndAccessEnded || s.ID != running.ID {
		t.Errorf("session = %+v", s)
	}
	select {
	case <-cluster.watches:
	case <-time.After(10 * time.Second):
		t.Error("the watch still runs on the cluster")
	}
	io.ReadAll(watch.Body) //nolint:errcheck
	watch.Body.Close()
	if resp, body := get("prod/api/v1/pods", token); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "access") {
		t.Errorf("after the access ended: %d %s", resp.StatusCode, body)
	}

	// Discovery is not logged; everything else is, with what the cluster
	// answered.
	evs := map[string]int{}
	for _, ev := range e.eventList(running.ID) {
		status := 0
		if ev.Status != nil {
			status = *ev.Status
		}
		evs[ev.Text] = status
	}
	want := map[string]int{
		"GET /api/v1/namespaces/default/pods?limit=5":              200,
		"GET /api/v1/namespaces/default/secrets":                   403,
		"POST /api/v1/namespaces/default/pods/web/exec?command=sh": 101,
		"GET /api/v1/pods?watch=true":                              200,
	}
	if fmt.Sprint(evs) != fmt.Sprint(want) {
		t.Errorf("log = %v", evs)
	}

	// A session without requests for a while ends; the next request starts
	// another.
	idle := e.server()
	idle.KubeIdle = 200 * time.Millisecond
	idleBase := "https://" + e.listen(idle.ServeKubernetes) + gateways.KubePathPrefix
	tok := e.token(ana, other)
	for range 2 {
		req, _ := http.NewRequest(http.MethodGet, idleBase+"staging/api/v1/pods", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			t.Fatalf("staging: %v %v", resp, err)
		}
		resp.Body.Close()
		if s := e.session(other.ID, ended); s.EndReason != gateways.EndClosed {
			t.Errorf("idle session = %+v", s)
		}
	}
	if n := e.one(`SELECT count(*)::text FROM gateway_sessions WHERE target_id = $1`, other.ID); n != "2" {
		t.Errorf("staging sessions = %s", n)
	}
}

func TestKubeDiscovery(t *testing.T) {
	for path, want := range map[string]bool{
		"/version": true, "/openapi/v3": true, "/api": true, "/api/v1": true, "/apis": true, "/apis/apps": true, "/apis/apps/v1": true,
		"/api/v1/pods": false, "/apis/apps/v1/deployments": false, "/healthz": false,
	} {
		if got := kubeDiscovery(http.MethodGet, path); got != want {
			t.Errorf("%s = %v", path, got)
		}
	}
	if kubeDiscovery(http.MethodPost, "/api") {
		t.Error("POST is discovery")
	}
}
