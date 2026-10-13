// Package auth signs people in to the Grantra console through an OpenID
// Connect provider (Grantra Login, the bundled Keycloak, or the company's own
// IdP) and keeps them signed in with a server-side session.
//
// The browser only ever holds an opaque, HttpOnly session cookie. Tokens from
// the provider stay on the server (the "backend for frontend" pattern), so a
// script running in the page cannot steal them.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/oauth2"
)

// Config holds the login settings.
type Config struct {
	// Issuer is the provider's issuer URL, exactly as it appears in tokens.
	Issuer string
	// DiscoveryURL is where provider metadata is fetched when it must differ
	// from Issuer. Optional.
	DiscoveryURL string
	ClientID     string
	ClientSecret string
	// PublicURL is the browser-facing base URL of Grantra, without trailing slash.
	PublicURL   string
	SessionIdle time.Duration
	SessionMax  time.Duration
}

// Principal is the person behind an authenticated request.
type Principal struct {
	IdentityID  string `json:"identity_id"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email,omitempty"`
}

const (
	sessionCookie = "grantra_session"
	loginCookie   = "grantra_login"
	loginTTL      = 10 * time.Minute
)

// Service implements login, logout and session checks for one tenant.
type Service struct {
	cfg      Config
	pool     *pgxpool.Pool
	tenantID string
	log      *slog.Logger
	client   *http.Client

	mu sync.Mutex
	p  *provider
}

type provider struct {
	oauth      oauth2.Config
	verifier   *oidc.IDTokenVerifier
	endSession string
}

// New returns a login service for the given tenant. The provider is contacted
// lazily, on the first login, so Grantra starts even while the IdP is down.
func New(cfg Config, pool *pgxpool.Pool, tenantID string, log *slog.Logger) *Service {
	return &Service{
		cfg:      cfg,
		pool:     pool,
		tenantID: tenantID,
		log:      log,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Register adds the login routes to mux.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", s.handleLogin)
	mux.HandleFunc("GET /auth/callback", s.handleCallback)
	mux.HandleFunc("POST /auth/logout", s.handleLogout)
}

type principalKey struct{}

// PrincipalFrom returns the signed-in person stored in ctx by Require.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(Principal)
	return p, ok
}

// Require rejects requests without a valid session with 401 and passes the
// signed-in person to next through the request context.
func (s *Service) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok, err := s.Lookup(r)
		if err != nil {
			s.log.ErrorContext(r.Context(), "session lookup failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "not signed in", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}

// providerFor discovers the OIDC provider on first use and caches it.
func (s *Service) providerFor(ctx context.Context) (*provider, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.p != nil {
		return s.p, nil
	}
	ctx = oidc.ClientContext(context.WithoutCancel(ctx), s.client)
	discovery := s.cfg.Issuer
	if s.cfg.DiscoveryURL != "" {
		discovery = s.cfg.DiscoveryURL
		ctx = oidc.InsecureIssuerURLContext(ctx, s.cfg.Issuer)
	}
	op, err := oidc.NewProvider(ctx, discovery)
	if err != nil {
		return nil, fmt.Errorf("discover OIDC provider: %w", err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	if err := op.Claims(&meta); err != nil {
		return nil, fmt.Errorf("read provider metadata: %w", err)
	}
	s.p = &provider{
		oauth: oauth2.Config{
			ClientID:     s.cfg.ClientID,
			ClientSecret: s.cfg.ClientSecret,
			Endpoint:     op.Endpoint(),
			RedirectURL:  s.cfg.PublicURL + "/auth/callback",
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		verifier:   op.Verifier(&oidc.Config{ClientID: s.cfg.ClientID}),
		endSession: meta.EndSession,
	}
	return s.p, nil
}

func (s *Service) secureCookies() bool {
	return strings.HasPrefix(s.cfg.PublicURL, "https://")
}

// safeReturnTo keeps post-login redirects on Grantra itself, so the login
// endpoint cannot be used to bounce people to another site.
func safeReturnTo(v string) string {
	if !strings.HasPrefix(v, "/") || strings.HasPrefix(v, "//") || strings.ContainsAny(v, "\\\r\n\t") {
		return "/"
	}
	return v
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
