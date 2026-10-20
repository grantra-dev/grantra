package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/grantra-dev/grantra/core/internal/db"
	"github.com/grantra-dev/grantra/core/internal/db/dbtest"
	"github.com/grantra-dev/grantra/core/internal/settings"
)

func TestSafeReturnTo(t *testing.T) {
	tests := map[string]string{
		"":                       "/",
		"/requests?tab=mine":     "/requests?tab=mine",
		"//evil.example.com":     "/",
		"/\\evil.example.com":    "/",
		"https://evil.example":   "/",
		"/ok\r\nSet-Cookie: x=1": "/",
		"/\t/evil.example.com":   "/", // browsers drop tabs, leaving //evil...
	}
	for in, want := range tests {
		if got := safeReturnTo(in); got != want {
			t.Errorf("safeReturnTo(%q) = %q, want %q", in, got, want)
		}
	}
}

// fakeIdP is a minimal OpenID Connect provider: discovery, keys, a token
// endpoint that hands out ID tokens for codes the test registers, and an end
// session endpoint.
type fakeIdP struct {
	*httptest.Server
	key *rsa.PrivateKey

	mu    sync.Mutex
	codes map[string]idClaims
}

type idClaims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Nonce         string
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key, codes: map[string]idClaims{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"issuer":                                idp.URL,
			"authorization_endpoint":                idp.URL + "/authorize",
			"token_endpoint":                        idp.URL + "/token",
			"jwks_uri":                              idp.URL + "/keys",
			"end_session_endpoint":                  idp.URL + "/logout",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{ //nolint:errcheck
			{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"},
		}})
	})
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil || r.PostForm.Get("code_verifier") == "" {
			http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		idp.mu.Lock()
		c, ok := idp.codes[r.PostForm.Get("code")]
		delete(idp.codes, r.PostForm.Get("code"))
		idp.mu.Unlock()
		if !ok {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{ //nolint:errcheck
			"access_token": "unused",
			"token_type":   "Bearer",
			"id_token":     idp.sign(t, c),
		})
	})
	idp.Server = httptest.NewServer(mux)
	t.Cleanup(idp.Close)
	return idp
}

func (idp *fakeIdP) sign(t *testing.T, c idClaims) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: idp.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	raw, err := jwt.Signed(signer).Claims(jwt.Claims{
		Issuer:   idp.URL,
		Subject:  c.Subject,
		Audience: jwt.Audience{"grantra-console"},
		IssuedAt: jwt.NewNumericDate(now),
		Expiry:   jwt.NewNumericDate(now.Add(5 * time.Minute)),
	}).Claims(map[string]any{
		"nonce":          c.Nonce,
		"email":          c.Email,
		"email_verified": c.EmailVerified,
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type harness struct {
	t      *testing.T
	pool   *pgxpool.Pool
	tenant string
	idp    *fakeIdP
	svc    *Service
	mux    *http.ServeMux
}

// These tests need a disposable PostgreSQL database. They are skipped unless
// GRANTRA_TEST_DATABASE_URL is set (CI sets it).
func newHarness(t *testing.T) *harness {
	t.Helper()
	pool := dbtest.Open(t)
	tenant := dbtest.Tenant(t, pool, "auth")

	idp := newFakeIdP(t)
	svc := New(Config{
		Issuer:    idp.URL,
		ClientID:  "grantra-console",
		PublicURL: "http://grantra.test",
	}, pool, tenant, slog.New(slog.NewTextHandler(io.Discard, nil)))
	mux := http.NewServeMux()
	svc.Register(mux)
	mux.Handle("GET /me", svc.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		json.NewEncoder(w).Encode(p) //nolint:errcheck
	})))
	return &harness{t: t, pool: pool, tenant: tenant, idp: idp, svc: svc, mux: mux}
}

func (h *harness) exec(sql string, args ...any) {
	h.t.Helper()
	err := db.InTenant(context.Background(), h.pool, h.tenant, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), sql, args...)
		return err
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) count(sql string, args ...any) int {
	h.t.Helper()
	var n int
	err := db.InTenant(context.Background(), h.pool, h.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), sql, args...).Scan(&n)
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return n
}

func (h *harness) addIdentity(email, status string) string {
	h.t.Helper()
	var id string
	err := db.InTenant(context.Background(), h.pool, h.tenant, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(),
			`INSERT INTO identities (tenant_id, display_name, email, status) VALUES ($1, $2, $2, $3) RETURNING id`,
			h.tenant, email, status).Scan(&id)
	})
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) do(req *http.Request, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func cookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.MaxAge >= 0 {
			return c
		}
	}
	return nil
}

// login runs the whole browser flow; tamper may change the claims the IdP
// returns after it has seen the authorization request.
func (h *harness) login(c idClaims, tamper func(*idClaims)) *httptest.ResponseRecorder {
	h.t.Helper()
	start := h.do(httptest.NewRequest(http.MethodGet, "/auth/login?return_to=/requests", nil))
	if start.Code != http.StatusFound {
		h.t.Fatalf("GET /auth/login = %d: %s", start.Code, start.Body)
	}
	loc, err := url.Parse(start.Header().Get("Location"))
	if err != nil {
		h.t.Fatal(err)
	}
	q := loc.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		h.t.Fatalf("authorization request without PKCE: %s", loc)
	}
	c.Nonce = q.Get("nonce")
	if tamper != nil {
		tamper(&c)
	}
	h.idp.mu.Lock()
	h.idp.codes["code-"+q.Get("state")] = c
	h.idp.mu.Unlock()

	cb := httptest.NewRequest(http.MethodGet,
		"/auth/callback?code=code-"+url.QueryEscape(q.Get("state"))+"&state="+url.QueryEscape(q.Get("state")), nil)
	return h.do(cb, cookie(start, loginCookie))
}

func (h *harness) me(session *http.Cookie) (int, Principal) {
	h.t.Helper()
	rec := h.do(httptest.NewRequest(http.MethodGet, "/me", nil), session)
	var p Principal
	if rec.Code == http.StatusOK {
		if err := json.NewDecoder(rec.Body).Decode(&p); err != nil {
			h.t.Fatal(err)
		}
	}
	return rec.Code, p
}

func TestLoginLinksByVerifiedEmail(t *testing.T) {
	h := newHarness(t)
	id := h.addIdentity("Ana@Example.com", "active")

	rec := h.login(idClaims{Subject: "sub-ana", Email: "ana@example.com", EmailVerified: true}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/requests" {
		t.Fatalf("callback = %d to %q, want 303 to /requests: %s", rec.Code, rec.Header().Get("Location"), rec.Body)
	}
	session := cookie(rec, sessionCookie)
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie = %+v, want HttpOnly and SameSite=Lax", session)
	}
	if code, p := h.me(session); code != http.StatusOK || p.IdentityID != id {
		t.Fatalf("me = %d %+v, want identity %s", code, p, id)
	}

	// Later logins follow the subject, even if the email at the IdP changed.
	rec = h.login(idClaims{Subject: "sub-ana", Email: "ana.new@example.com", EmailVerified: true}, nil)
	if code, p := h.me(cookie(rec, sessionCookie)); code != http.StatusOK || p.IdentityID != id {
		t.Fatalf("second login me = %d %+v, want identity %s", code, p, id)
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE action = 'auth.login' AND target_id = $1`, id); n != 2 {
		t.Errorf("auth.login audit events = %d, want 2", n)
	}
}

func TestLoginRefusesUnknownPeople(t *testing.T) {
	h := newHarness(t)
	h.addIdentity("bob@example.com", "active")
	h.addIdentity("leaver@example.com", "terminated")

	tests := []struct {
		name   string
		claims idClaims
	}{
		{"not in Grantra", idClaims{Subject: "sub-x", Email: "nobody@example.com", EmailVerified: true}},
		{"unverified email", idClaims{Subject: "sub-y", Email: "bob@example.com", EmailVerified: false}},
		{"terminated identity", idClaims{Subject: "sub-z", Email: "leaver@example.com", EmailVerified: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := h.login(tt.claims, nil)
			if rec.Code != http.StatusForbidden || cookie(rec, sessionCookie) != nil {
				t.Fatalf("callback = %d, want 403 without session", rec.Code)
			}
		})
	}
	if n := h.count(`SELECT count(*) FROM audit_events WHERE action = 'auth.login_denied'`); n != len(tests) {
		t.Errorf("auth.login_denied audit events = %d, want %d", n, len(tests))
	}
	if n := h.count(`SELECT count(*) FROM sessions`); n != 0 {
		t.Errorf("sessions = %d, want 0", n)
	}
}

func TestCallbackRejectsForgedOrReplayedFlows(t *testing.T) {
	h := newHarness(t)
	h.addIdentity("eve@example.com", "active")
	claims := idClaims{Subject: "sub-eve", Email: "eve@example.com", EmailVerified: true}

	t.Run("wrong nonce", func(t *testing.T) {
		rec := h.login(claims, func(c *idClaims) { c.Nonce = "attacker" })
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("callback = %d, want 401", rec.Code)
		}
	})

	t.Run("state not bound to this browser", func(t *testing.T) {
		start := h.do(httptest.NewRequest(http.MethodGet, "/auth/login", nil))
		loc, _ := url.Parse(start.Header().Get("Location"))
		state := loc.Query().Get("state")
		cb := httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state="+url.QueryEscape(state), nil)
		if rec := h.do(cb, &http.Cookie{Name: loginCookie, Value: "other"}); rec.Code != http.StatusBadRequest {
			t.Fatalf("callback = %d, want 400", rec.Code)
		}
		if rec := h.do(httptest.NewRequest(http.MethodGet, "/auth/callback?code=x&state="+url.QueryEscape(state), nil)); rec.Code != http.StatusBadRequest {
			t.Fatalf("callback without cookie = %d, want 400", rec.Code)
		}
	})

	t.Run("replayed callback", func(t *testing.T) {
		start := h.do(httptest.NewRequest(http.MethodGet, "/auth/login", nil))
		loc, _ := url.Parse(start.Header().Get("Location"))
		q := loc.Query()
		c := claims
		c.Nonce = q.Get("nonce")
		h.idp.mu.Lock()
		h.idp.codes["c1"] = c
		h.idp.codes["c2"] = c
		h.idp.mu.Unlock()
		lc := cookie(start, loginCookie)
		first := h.do(httptest.NewRequest(http.MethodGet, "/auth/callback?code=c1&state="+url.QueryEscape(q.Get("state")), nil), lc)
		if first.Code != http.StatusSeeOther {
			t.Fatalf("first callback = %d, want 303", first.Code)
		}
		second := h.do(httptest.NewRequest(http.MethodGet, "/auth/callback?code=c2&state="+url.QueryEscape(q.Get("state")), nil), lc)
		if second.Code != http.StatusBadRequest {
			t.Fatalf("replayed callback = %d, want 400", second.Code)
		}
	})
}

func TestSessionEnds(t *testing.T) {
	h := newHarness(t)
	id := h.addIdentity("carla@example.com", "active")
	signIn := func() *http.Cookie {
		t.Helper()
		rec := h.login(idClaims{Subject: "sub-carla", Email: "carla@example.com", EmailVerified: true}, nil)
		s := cookie(rec, sessionCookie)
		if code, _ := h.me(s); code != http.StatusOK {
			t.Fatalf("me right after login = %d", code)
		}
		return s
	}

	t.Run("after idle timeout", func(t *testing.T) {
		s := signIn()
		h.exec(`UPDATE sessions SET last_seen_at = now() - interval '31 minutes'`)
		if code, _ := h.me(s); code != http.StatusUnauthorized {
			t.Fatalf("me = %d, want 401", code)
		}
	})

	t.Run("as long as the tenant says", func(t *testing.T) {
		h.exec(`INSERT INTO tenant_settings (tenant_id, key, value, updated_by) VALUES ($1, $2, 60, 'test'), ($1, $3, 2, 'test')`,
			h.tenant, settings.SessionIdleMinutes, settings.SessionMaxHours)
		defer h.exec(`DELETE FROM tenant_settings`)
		s := signIn()
		left := time.Duration(h.count(`SELECT extract(epoch FROM expires_at - now())::int FROM sessions WHERE token_hash = $1`, hash(s.Value))) * time.Second
		if left > 2*time.Hour || left < 2*time.Hour-time.Minute {
			t.Errorf("session lasts %v, want the tenant's 2 hours", left)
		}
		h.exec(`UPDATE sessions SET last_seen_at = now() - interval '31 minutes'`)
		if code, _ := h.me(s); code != http.StatusOK {
			t.Fatalf("me after 31 idle minutes of 60 = %d, want 200", code)
		}
	})

	t.Run("after max age", func(t *testing.T) {
		s := signIn()
		h.exec(`UPDATE sessions SET expires_at = now() - interval '1 second'`)
		if code, _ := h.me(s); code != http.StatusUnauthorized {
			t.Fatalf("me = %d, want 401", code)
		}
	})

	t.Run("when HR offboards the person", func(t *testing.T) {
		s := signIn()
		h.exec(`UPDATE identities SET status = 'terminated' WHERE id = $1`, id)
		defer h.exec(`UPDATE identities SET status = 'active' WHERE id = $1`, id)
		if code, _ := h.me(s); code != http.StatusUnauthorized {
			t.Fatalf("me = %d, want 401", code)
		}
		if n := h.count(`SELECT count(*) FROM sessions`); n != 0 {
			t.Errorf("sessions = %d, want the session deleted", n)
		}
	})

	t.Run("on logout", func(t *testing.T) {
		s := signIn()
		rec := h.do(httptest.NewRequest(http.MethodPost, "/auth/logout", nil), s)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("logout = %d, want 303", rec.Code)
		}
		loc, _ := url.Parse(rec.Header().Get("Location"))
		if !strings.HasPrefix(loc.String(), h.idp.URL+"/logout?") || loc.Query().Get("id_token_hint") == "" ||
			loc.Query().Get("post_logout_redirect_uri") != "http://grantra.test/" {
			t.Fatalf("logout redirect = %s, want the IdP end session endpoint with hints", loc)
		}
		if code, _ := h.me(s); code != http.StatusUnauthorized {
			t.Fatalf("me after logout = %d, want 401", code)
		}
		if n := h.count(`SELECT count(*) FROM audit_events WHERE action = 'auth.logout' AND target_id = $1`, id); n != 1 {
			t.Errorf("auth.logout audit events = %d, want 1", n)
		}
	})

	t.Run("without a cookie", func(t *testing.T) {
		if code, _ := h.me(&http.Cookie{Name: sessionCookie, Value: "made-up"}); code != http.StatusUnauthorized {
			t.Fatalf("me with a made-up token = %d, want 401", code)
		}
	})
}
