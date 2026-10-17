package proxy

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http/httpguts"

	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// ServeKubernetes runs the Kubernetes gateway on l until ctx ends. kubectl
// reaches a cluster at https://gateway:8443/kubernetes/<target> with its
// token as the bearer token; the console hands out the kubeconfig.
func (s *Server) ServeKubernetes(ctx context.Context, l net.Listener) error {
	s.defaults()
	conf := s.TLS.Clone()
	conf.NextProtos = []string{"h2", "http/1.1"}
	srv := &http.Server{
		Handler:           http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { s.serveKube(ctx, w, r) }),
		TLSConfig:         conf,
		ReadHeaderTimeout: 30 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.Log.Handler(), slog.LevelDebug),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	// Watches and exec sessions do not end on their own: stopping closes
	// them, after their sessions said why.
	stop := context.AfterFunc(ctx, func() { srv.Close() })
	defer stop()
	err := srv.Serve(tls.NewListener(l, conf))
	srv.Close()
	s.kube.stop()
	if errors.Is(err, http.ErrServerClosed) || ctx.Err() != nil {
		return nil
	}
	return err
}

// kubeSessions are the cluster sessions running on this gateway, by token.
type kubeSessions struct {
	mu      sync.Mutex
	byToken map[string]*kubeSession
	ups     map[string]*kubeUpstream
	closed  bool
	// running counts the sessions not finished yet.
	running sync.WaitGroup
}

// errKubeStopping is returned for a request coming in while the gateway
// stops.
var errKubeStopping = errors.New("the gateway is stopping")

// stop ends the running sessions, recording why, and waits for them.
func (ks *kubeSessions) stop() {
	ks.mu.Lock()
	ks.closed = true
	sessions := make([]*kubeSession, 0, len(ks.byToken))
	for _, k := range ks.byToken {
		sessions = append(sessions, k)
	}
	ks.mu.Unlock()
	for _, k := range sessions {
		k.live.halt(stopShutdown, "")
	}
	ks.running.Wait()
}

// kubeSession is a run of requests with one token. Each request is
// checked on its own; the session also checks its access every 30 seconds
// and ends the requests still running (watches, exec) when the access ends.
type kubeSession struct {
	live *live
	user string

	mu       sync.Mutex
	inflight map[int]context.CancelFunc
	next     int
	last     time.Time
	ended    bool
}

// add registers a running request, unless the session ended.
func (k *kubeSession) add(cancel context.CancelFunc) (int, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.ended {
		return 0, false
	}
	k.next++
	k.inflight[k.next] = cancel
	k.last = time.Now()
	return k.next, true
}

func (k *kubeSession) remove(id int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.inflight, id)
	k.last = time.Now()
}

// kubeStopIdle is the reason a cluster session stops after going idle.
const kubeStopIdle = "idle"

// kubeSession returns the running session of a token, starting one.
func (s *Server) kubeSession(ctx context.Context, g gateways.Grant, client string) (*kubeSession, error) {
	s.kube.mu.Lock()
	defer s.kube.mu.Unlock()
	if s.kube.closed {
		return nil, errKubeStopping
	}
	if s.kube.byToken == nil {
		s.kube.byToken = map[string]*kubeSession{}
	}
	if k, ok := s.kube.byToken[g.TokenID]; ok {
		return k, nil
	}
	who := g.IdentityEmail
	if who == "" {
		who = g.IdentityID
	}
	k := &kubeSession{user: "grantra:" + who, inflight: map[int]context.CancelFunc{}, last: time.Now()}
	kill := func(string, string) {
		s.kube.mu.Lock()
		if s.kube.byToken[g.TokenID] == k {
			delete(s.kube.byToken, g.TokenID)
		}
		s.kube.mu.Unlock()
		k.mu.Lock()
		k.ended = true
		for _, cancel := range k.inflight {
			cancel()
		}
		k.mu.Unlock()
		// Called from the session's own checks too: finishing waits for
		// them.
		go func() {
			defer s.kube.running.Done()
			k.live.finish("")
		}()
	}
	l, err := s.begin(ctx, g, gateways.Session{ClientAddress: client, Account: k.user}, kill)
	if err != nil {
		return nil, err
	}
	k.live = l
	s.kube.running.Add(1)
	s.kube.byToken[g.TokenID] = k
	go s.kubeIdle(ctx, k)
	return k, nil
}

// kubeIdle ends a session that ran no request for KubeIdle.
func (s *Server) kubeIdle(ctx context.Context, k *kubeSession) {
	t := time.NewTicker(min(s.KubeIdle/4, 30*time.Second))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-k.live.stopped:
			return
		case <-t.C:
		}
		k.mu.Lock()
		idle := len(k.inflight) == 0 && time.Since(k.last) >= s.KubeIdle
		k.mu.Unlock()
		if idle {
			k.live.halt(kubeStopIdle, "")
			return
		}
	}
}

// kubeUpstream is how the gateway reaches a cluster.
type kubeUpstream struct {
	base      *url.URL
	transport *http.Transport
	// upgrade is for exec, attach and port-forward, which upgrade an
	// HTTP/1.1 connection: HTTP/2 cannot carry them.
	upgrade *http.Transport
	// token is the vault account's service account token; empty for the
	// gateway's own (in-cluster) one, read from its file on every request,
	// since Kubernetes rotates it.
	token  string
	loaded time.Time
}

// kubeUpstreamFresh is how long the gateway keeps a cluster's credentials
// before opening them again, so a rotated token is picked up.
const kubeUpstreamFresh = 5 * time.Minute

func (s *Server) kubeUpstream(ctx context.Context, t gateways.Target) (*kubeUpstream, error) {
	key := t.ID + "|" + t.UpdatedAt.String()
	s.kube.mu.Lock()
	if s.kube.ups == nil {
		s.kube.ups = map[string]*kubeUpstream{}
	}
	up, ok := s.kube.ups[key]
	s.kube.mu.Unlock()
	if ok && time.Since(up.loaded) < kubeUpstreamFresh {
		return up, nil
	}
	base, err := url.Parse(t.Address)
	if err != nil {
		return nil, err
	}
	conf, err := upstreamTLS(t, base.Hostname())
	if err != nil {
		return nil, err
	}
	up = &kubeUpstream{base: base, loaded: time.Now()}
	if t.VaultAccountID != "" {
		if _, up.token, err = s.credentials(ctx, t); err != nil {
			return nil, err
		}
		up.token = strings.TrimSpace(up.token)
	} else if t.CACertificate == "" && t.TLSMode == gateways.TLSVerify {
		// The gateway's own service account: trust its cluster's CA.
		pem, err := os.ReadFile(s.InClusterCAFile)
		if err != nil {
			return nil, errors.New(t.Name + " has no vault account, and the gateway does not run in a cluster: " + err.Error())
		}
		conf.RootCAs = x509.NewCertPool()
		conf.RootCAs.AppendCertsFromPEM(pem)
	}
	up.transport = &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: s.DialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     conf,
		TLSHandshakeTimeout: s.DialTimeout,
		ForceAttemptHTTP2:   true,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
	}
	up.upgrade = up.transport.Clone()
	up.upgrade.ForceAttemptHTTP2 = false
	up.upgrade.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if up.upgrade.TLSClientConfig != nil {
		up.upgrade.TLSClientConfig.NextProtos = []string{"http/1.1"}
	}
	s.kube.mu.Lock()
	// Older entries of the target go: its settings or credentials changed,
	// or are opened again. Requests running on them finish.
	for k, old := range s.kube.ups {
		if strings.HasPrefix(k, t.ID+"|") {
			old.transport.CloseIdleConnections()
			old.upgrade.CloseIdleConnections()
			delete(s.kube.ups, k)
		}
	}
	s.kube.ups[key] = up
	s.kube.mu.Unlock()
	return up, nil
}

func (s *Server) serveKube(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	rawRest, ok := strings.CutPrefix(r.URL.EscapedPath(), gateways.KubePathPrefix)
	if !ok {
		kubeStatus(w, http.StatusNotFound, "NotFound", "clusters are at "+gateways.KubePathPrefix+"<name> on the Grantra gateway")
		return
	}
	name, rawPath, _ := strings.Cut(rawRest, "/")
	rawPath = "/" + rawPath
	path, err := url.PathUnescape(rawPath)
	if err != nil {
		kubeStatus(w, http.StatusBadRequest, "BadRequest", "invalid path")
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		kubeStatus(w, http.StatusUnauthorized, "Unauthorized", "Grantra: connect with the kubeconfig Grantra gives you")
		return
	}
	g, err := s.authorize(r.Context(), strings.TrimSpace(token), gateways.Kubernetes, name)
	if err != nil {
		s.Log.WarnContext(ctx, "Kubernetes gateway refused a token", "target", name, "client", r.RemoteAddr, "reason", err.Error())
		var denied *gateways.Denied
		if errors.Is(err, gateways.ErrBadToken) {
			kubeStatus(w, http.StatusUnauthorized, "Unauthorized", "Grantra: "+err.Error())
		} else if errors.As(err, &denied) {
			kubeStatus(w, http.StatusForbidden, "Forbidden", "Grantra: "+denied.Msg)
		} else {
			kubeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Grantra: "+publicError(err))
		}
		return
	}
	up, err := s.kubeUpstream(ctx, g.Target)
	if err != nil {
		s.Log.ErrorContext(ctx, "Kubernetes gateway could not set up a cluster", "target", g.Target.Name, "err", err)
		kubeStatus(w, http.StatusBadGateway, "ServiceUnavailable", "Grantra cannot reach "+g.Target.Name+": it is not set up right; tell an admin")
		return
	}
	var k *kubeSession
	var slot int
	// A session ending right now is replaced by a new one.
	for range 2 {
		if k, err = s.kubeSession(ctx, g, r.RemoteAddr); errors.Is(err, errKubeStopping) {
			kubeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Grantra: the gateway is stopping; try again")
			return
		} else if err != nil {
			s.Log.ErrorContext(ctx, "Kubernetes gateway could not record a session start", "err", err)
			kubeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Grantra could not start logging this session; try again")
			return
		}
		reqCtx, cancel := context.WithCancel(r.Context())
		defer cancel()
		if slot, ok = k.add(cancel); ok {
			r = r.WithContext(reqCtx)
			break
		}
	}
	if !ok {
		kubeStatus(w, http.StatusServiceUnavailable, "ServiceUnavailable", "Grantra: this session is ending; try again")
		return
	}
	defer k.remove(slot)
	var ev *gateways.Event
	if !kubeDiscovery(r.Method, path) {
		text := r.Method + " " + path
		if r.URL.RawQuery != "" {
			text += "?" + r.URL.RawQuery
		}
		if ev, ok = k.live.eventRef(gateways.EventRequest, text); !ok {
			kubeStatus(w, http.StatusForbidden, "Forbidden", "Grantra: this session ended; try again to start a new one")
			return
		}
	}
	transport := up.transport
	if httpguts.HeaderValuesContainsToken(r.Header["Connection"], "upgrade") {
		transport = up.upgrade
	}
	proxy := &httputil.ReverseProxy{
		Transport:     transport,
		FlushInterval: -1,
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path, pr.Out.URL.RawPath = path, rawPath
			pr.SetURL(up.base)
			kubeHeaders(pr.Out.Header, up, s.InClusterTokenFile, k.user, g, k.live.id)
		},
		ModifyResponse: func(resp *http.Response) error {
			if ev != nil {
				k.live.setStatus(ev, resp.StatusCode)
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			if ev != nil {
				k.live.setStatus(ev, http.StatusBadGateway)
			}
			if r.Context().Err() != nil {
				return
			}
			k.live.log.WarnContext(ctx, "Kubernetes gateway could not reach a cluster", "err", err)
			kubeStatus(w, http.StatusBadGateway, "ServiceUnavailable", "Grantra could not reach "+g.Target.Name)
		},
	}
	proxy.ServeHTTP(w, r)
}

// kubeHeaders makes the request to the cluster: authenticated as the
// gateway's service account, acting as the person, with the target's
// groups. Whatever identity the client claimed goes.
func kubeHeaders(h http.Header, up *kubeUpstream, tokenFile, user string, g gateways.Grant, sessionID string) {
	for k := range h {
		ck := http.CanonicalHeaderKey(k)
		if strings.HasPrefix(ck, "Impersonate-") || strings.HasPrefix(ck, "X-Remote-") || ck == "Authorization" {
			delete(h, k)
		}
	}
	token := up.token
	if token == "" {
		if b, err := os.ReadFile(tokenFile); err == nil {
			token = strings.TrimSpace(string(b))
		}
	}
	h.Set("Authorization", "Bearer "+token)
	h.Set("Impersonate-User", user)
	for _, group := range g.Target.KubeGroups {
		h.Add("Impersonate-Group", group)
	}
	h.Set("Impersonate-Extra-Grantra-Identity", g.IdentityID)
	h.Set("Impersonate-Extra-Grantra-Session", sessionID)
}

// kubeDiscovery reports whether a request only reads what the cluster
// serves (API discovery, OpenAPI, version), which kubectl asks for all the
// time and is not logged.
func kubeDiscovery(method, path string) bool {
	if method != http.MethodGet {
		return false
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch parts[0] {
	case "version", "openapi":
		return true
	case "api":
		return len(parts) <= 2
	case "apis":
		return len(parts) <= 3
	}
	return false
}

// kubeStatus answers with a Kubernetes Status, which kubectl shows.
func kubeStatus(w http.ResponseWriter, code int, reason, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
		"kind": "Status", "apiVersion": "v1", "metadata": map[string]any{}, "status": "Failure",
		"message": msg, "reason": reason, "code": code,
	})
}
