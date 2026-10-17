// Package proxy is the gateway people reach Kubernetes clusters and
// PostgreSQL and MySQL databases through, with a token Grantra issued
// them. It checks the token and the access when they connect (on a
// cluster, for every request) and every 30 seconds after, logs in to the
// target with the account kept in the vault, and logs every query and
// cluster request.
//
// Clients always talk TLS to the gateway, with a certificate of the
// tenant's gateway CA (or one the operator gives it).
package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/gateways"
)

// Server is the database and cluster gateway for one tenant.
type Server struct {
	Pool     *pgxpool.Pool
	TenantID string
	Gateways *gateways.Service
	// TLS holds the gateway's certificate, the one clients verify.
	TLS *tls.Config
	// Name tells gateways apart on sessions, such as the host name.
	Name string
	Log  *slog.Logger

	// CheckEvery is how often a running session's access is checked
	// (default 30 seconds); FlushEvery how often its log is stored
	// (default 2 seconds). MaxEvents caps the queries or requests logged in
	// one session (default 100,000); a session reaching it ends.
	CheckEvery time.Duration
	FlushEvery time.Duration
	MaxEvents  int
	// DialTimeout bounds reaching a target (default 10 seconds).
	DialTimeout time.Duration
	// KubeIdle ends a cluster session after this long without requests
	// (default 10 minutes); the next request starts a new one.
	KubeIdle time.Duration
	// InClusterTokenFile and InClusterCAFile are the gateway's own service
	// account, for clusters without a vault account (default the paths
	// Kubernetes mounts them at).
	InClusterTokenFile string
	InClusterCAFile    string

	cancels sync.Map // pgCancelKey -> pgCancelTarget
	kube    kubeSessions
}

func (s *Server) defaults() {
	if s.CheckEvery == 0 {
		s.CheckEvery = 30 * time.Second
	}
	if s.FlushEvery == 0 {
		s.FlushEvery = 2 * time.Second
	}
	if s.MaxEvents == 0 {
		s.MaxEvents = 100_000
	}
	if s.DialTimeout == 0 {
		s.DialTimeout = 10 * time.Second
	}
	if s.KubeIdle == 0 {
		s.KubeIdle = 10 * time.Minute
	}
	if s.InClusterTokenFile == "" {
		s.InClusterTokenFile = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	}
	if s.InClusterCAFile == "" {
		s.InClusterCAFile = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	}
	if s.Log == nil {
		s.Log = slog.Default()
	}
}

// serve accepts connections on l until ctx ends, handling each with
// handle, and waits for them to finish.
func (s *Server) serve(ctx context.Context, l net.Listener, handle func(context.Context, net.Conn)) error {
	s.defaults()
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
			defer conn.Close()
			handle(ctx, conn)
		}()
	}
}

// maxFailedChecks is how many access checks in a row may fail (the database
// is unreachable) before a session ends.
const maxFailedChecks = 3

// stopShutdown is the reason a session stops when the gateway does.
const stopShutdown = "shutdown"

// live is a running session: it logs its events, checks its access every
// CheckEvery, and ends it when the access or the token does.
type live struct {
	s      *Server
	id     string
	target string
	log    *slog.Logger
	// kill stops the connection(s) the session runs on, telling the person
	// msg when the protocol can.
	kill func(reason, msg string)

	mu      sync.Mutex
	pending []*gateways.Event
	count   int
	reason  string

	stopped  chan struct{}
	loopDone chan struct{}
	once     sync.Once
}

// begin records that a session started and starts checking it. kill is
// called (once) when it must stop; the caller then calls finish.
func (s *Server) begin(ctx context.Context, g gateways.Grant, info gateways.Session, kill func(reason, msg string)) (*live, error) {
	info.TargetID, info.TargetName, info.Kind = g.Target.ID, g.Target.Name, g.Target.Kind
	info.IdentityID, info.TokenID, info.Gateway = g.IdentityID, g.TokenID, s.Name
	var id string
	err := db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
		id, err = gateways.StartSession(ctx, tx, s.TenantID, info, g.Target.VaultAccountID)
		return err
	})
	if err != nil {
		return nil, err
	}
	l := &live{s: s, id: id, target: g.Target.Name, kill: kill, stopped: make(chan struct{}), loopDone: make(chan struct{}),
		log: s.Log.With("session", id, "target", g.Target.Name, "identity", g.IdentityID)}
	go l.loop(ctx, g.ValidBefore)
	return l, nil
}

// halt stops the session for reason; the first reason wins.
func (l *live) halt(reason, msg string) {
	l.mu.Lock()
	if l.reason != "" {
		l.mu.Unlock()
		return
	}
	l.reason = reason
	l.mu.Unlock()
	l.kill(reason, msg)
}

// stoppedFor returns why the session was halted, if it was.
func (l *live) stoppedFor() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.reason
}

// event logs a query or request. It returns false, and halts the session,
// when the session's log is full or the session is stopping: the query
// must not run.
func (l *live) event(kind, text string) bool {
	_, ok := l.eventRef(kind, text)
	return ok
}

// eventRef is event, returning the event so its status can be set while it
// is not stored yet (see setStatus).
func (l *live) eventRef(kind, text string) (*gateways.Event, bool) {
	l.mu.Lock()
	if l.reason != "" {
		l.mu.Unlock()
		return nil, false
	}
	if l.count >= l.s.MaxEvents {
		l.mu.Unlock()
		l.halt(gateways.EndRecordingFull, "This session reached the gateway's log limit, so it ends now. Connect again to go on.")
		return nil, false
	}
	l.count++
	e := &gateways.Event{At: time.Now(), Kind: kind, Text: text}
	l.pending = append(l.pending, e)
	l.mu.Unlock()
	return e, true
}

// setStatus sets the status of an event, which counts only if it is not
// stored yet (a long watch is stored without it).
func (l *live) setStatus(e *gateways.Event, status int) {
	l.mu.Lock()
	e.Status = &status
	l.mu.Unlock()
}

// flush stores the pending events. What a failed flush could not store is
// tried again on the next.
func (l *live) flush(ctx context.Context) {
	l.mu.Lock()
	events := make([]gateways.Event, len(l.pending))
	for i, e := range l.pending {
		events[i] = *e
	}
	taken := l.pending
	l.pending = nil
	l.mu.Unlock()
	if len(events) == 0 {
		return
	}
	err := db.InTenant(ctx, l.s.Pool, l.s.TenantID, func(tx pgx.Tx) error {
		return gateways.AddEvents(ctx, tx, l.s.TenantID, l.id, events)
	})
	if err != nil {
		l.log.ErrorContext(ctx, "gateway could not store a session's log", "err", err)
		l.mu.Lock()
		l.pending = append(taken, l.pending...)
		l.mu.Unlock()
	}
}

func (l *live) loop(ctx context.Context, validBefore time.Time) {
	defer close(l.loopDone)
	flushCtx := context.WithoutCancel(ctx)
	flushes := time.NewTicker(l.s.FlushEvery)
	defer flushes.Stop()
	checks := time.NewTicker(l.s.CheckEvery)
	defer checks.Stop()
	// The session ends with the token (Grantra never issues one without an
	// end).
	expiry := time.NewTimer(time.Until(validBefore))
	defer expiry.Stop()
	failed := 0
	for {
		select {
		case <-l.stopped:
			return
		case <-ctx.Done():
			l.halt(stopShutdown, "The Grantra gateway is stopping; this session ends now.")
			<-l.stopped
			return
		case <-flushes.C:
			l.flush(flushCtx)
		case <-expiry.C:
			l.halt(gateways.EndAccessEnded, "Your token for "+l.target+" ran out, so this session ends now. Get a new one in Grantra.")
		case <-checks.C:
			var reason string
			err := db.InTenant(ctx, l.s.Pool, l.s.TenantID, func(tx pgx.Tx) (err error) {
				reason, err = gateways.Heartbeat(ctx, tx, l.id)
				return err
			})
			switch {
			case err != nil:
				if ctx.Err() != nil {
					continue
				}
				l.log.WarnContext(ctx, "gateway could not check a session's access", "err", err)
				// Sessions do not go on unchecked for long.
				if failed++; failed >= maxFailedChecks {
					l.halt(gateways.EndFailed, "Grantra cannot check your access right now, so this session ends.")
				}
				continue
			case reason == gateways.EndTerminated:
				l.halt(reason, "An admin ended this session.")
			case reason != "":
				l.halt(reason, "Your access to "+l.target+" ended, so this session ends now.")
			}
			failed = 0
		}
	}
}

// finish stops checking the session, stores the rest of its log and
// records how it ended: why it was halted, or closed.
func (l *live) finish(errText string) {
	l.once.Do(func() {
		close(l.stopped)
		<-l.loopDone
		ctx := context.Background()
		l.flush(ctx)
		reason := l.stoppedFor()
		switch reason {
		case "":
			reason = gateways.EndClosed
		case stopShutdown:
			reason, errText = gateways.EndClosed, "the gateway stopped"
		case kubeStopIdle:
			reason = gateways.EndClosed
		case gateways.EndFailed:
			if errText == "" {
				errText = "access could not be checked"
			}
		}
		err := db.InTenant(ctx, l.s.Pool, l.s.TenantID, func(tx pgx.Tx) error {
			return gateways.EndSession(ctx, tx, l.s.TenantID, l.id, reason, errText)
		})
		if err != nil {
			l.log.ErrorContext(ctx, "gateway could not record a session end", "err", err)
		}
		l.log.InfoContext(ctx, "gateway session ended", "reason", reason, "error", errText)
	})
}

// authorize checks a token for the target of kind named name.
func (s *Server) authorize(ctx context.Context, token, kind, name string) (gateways.Grant, error) {
	var g gateways.Grant
	err := db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
		g, err = gateways.Authorize(ctx, tx, token, kind, name)
		return err
	})
	return g, err
}

// credentials opens the account the gateway logs in to a target with.
func (s *Server) credentials(ctx context.Context, t gateways.Target) (username, secret string, err error) {
	err = db.InTenant(ctx, s.Pool, s.TenantID, func(tx pgx.Tx) (err error) {
		username, secret, err = s.Gateways.Credentials(ctx, tx, s.TenantID, t)
		return err
	})
	return username, secret, err
}

// upstreamTLS is how the gateway talks TLS to a target at host, or nil for
// no TLS.
func upstreamTLS(t gateways.Target, host string) (*tls.Config, error) {
	switch t.TLSMode {
	case gateways.TLSDisable:
		return nil, nil
	case gateways.TLSRequire:
		return &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}, nil //nolint:gosec // the admin chose not to verify
	}
	pool, err := t.CertPool()
	if err != nil {
		return nil, err
	}
	return &tls.Config{ServerName: host, RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}

// publicError is what a person is told when the gateway turns them away:
// the reasons Grantra itself gives, and a generic one for anything else
// (which goes to the log).
func publicError(err error) string {
	var denied *gateways.Denied
	if errors.As(err, &denied) {
		return denied.Msg
	}
	return "Grantra could not check your access right now; try again"
}
