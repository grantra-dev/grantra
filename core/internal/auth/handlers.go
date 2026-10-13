package auth

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"

	"github.com/grantra-dev/grantra/core/internal/db"
)

// handleLogin starts the authorization code flow with PKCE: it records the
// flow, drops a short-lived cookie that ties it to this browser and sends the
// browser to the provider.
func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.providerFor(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "login provider unavailable", "err", err)
		http.Error(w, "The login provider is unavailable. Try again in a moment.", http.StatusServiceUnavailable)
		return
	}

	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()
	returnTo := safeReturnTo(r.URL.Query().Get("return_to"))
	err = db.InTenant(ctx, s.pool, s.tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM oidc_login_flows WHERE expires_at <= now()`); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO oidc_login_flows (tenant_id, state_hash, nonce, code_verifier, return_to, expires_at)
			 VALUES ($1, $2, $3, $4, $5, now() + make_interval(secs => $6))`,
			s.tenantID, hash(state), nonce, verifier, returnTo, loginTTL.Seconds())
		return err
	})
	if err != nil {
		s.log.ErrorContext(ctx, "store login flow", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     loginCookie,
		Value:    state,
		Path:     "/auth/callback",
		MaxAge:   int(loginTTL.Seconds()),
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, p.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}

// handleCallback finishes the login: it checks the flow belongs to this
// browser, exchanges the code, verifies the ID token and opens a session for
// the identity behind it.
func (s *Service) handleCallback(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()

	http.SetCookie(w, &http.Cookie{Name: loginCookie, Path: "/auth/callback", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode})

	if e := q.Get("error"); e != "" {
		s.log.InfoContext(ctx, "login cancelled at the provider", "error", e, "description", q.Get("error_description"))
		http.Error(w, "Login was cancelled or refused by the login provider.", http.StatusUnauthorized)
		return
	}
	state := q.Get("state")
	c, err := r.Cookie(loginCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(state)) != 1 {
		http.Error(w, "This login link is invalid or has expired. Start again.", http.StatusBadRequest)
		return
	}

	var nonce, verifier, returnTo string
	err = db.InTenant(ctx, s.pool, s.tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`DELETE FROM oidc_login_flows WHERE state_hash = $1 AND expires_at > now()
			 RETURNING nonce, code_verifier, return_to`, hash(state)).Scan(&nonce, &verifier, &returnTo)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		http.Error(w, "This login link is invalid or has expired. Start again.", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.log.ErrorContext(ctx, "load login flow", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	p, err := s.providerFor(ctx)
	if err != nil {
		s.log.ErrorContext(ctx, "login provider unavailable", "err", err)
		http.Error(w, "The login provider is unavailable. Try again in a moment.", http.StatusServiceUnavailable)
		return
	}
	pctx := oidc.ClientContext(ctx, s.client)
	tok, err := p.oauth.Exchange(pctx, q.Get("code"), oauth2.VerifierOption(verifier))
	if err != nil {
		s.log.WarnContext(ctx, "code exchange failed", "err", err)
		http.Error(w, "Login failed. Start again.", http.StatusUnauthorized)
		return
	}
	rawID, _ := tok.Extra("id_token").(string)
	idt, err := p.verifier.Verify(pctx, rawID)
	if err != nil || subtle.ConstantTimeCompare([]byte(idt.Nonce), []byte(nonce)) != 1 {
		s.log.WarnContext(ctx, "ID token rejected", "err", err)
		http.Error(w, "Login failed. Start again.", http.StatusUnauthorized)
		return
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := idt.Claims(&claims); err != nil {
		s.log.WarnContext(ctx, "ID token claims unreadable", "err", err)
		http.Error(w, "Login failed. Start again.", http.StatusUnauthorized)
		return
	}

	token := randomToken()
	res, err := s.openSession(ctx, login{
		issuer:        idt.Issuer,
		subject:       idt.Subject,
		email:         claims.Email,
		emailVerified: claims.EmailVerified,
		idToken:       rawID,
	}, token)
	if err != nil {
		s.log.ErrorContext(ctx, "open session", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	switch res {
	case notEnrolled:
		http.Error(w, "You signed in correctly, but you are not registered in Grantra. Ask your Grantra administrator.", http.StatusForbidden)
		return
	case inactive:
		http.Error(w, "Your Grantra account is not active. Ask your Grantra administrator.", http.StatusForbidden)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies(),
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

// handleLogout ends the Grantra session and, when the provider supports it,
// sends the browser on to end the provider session too.
func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.secureCookies(), SameSite: http.SameSiteLaxMode})

	var idToken string
	if c, err := r.Cookie(sessionCookie); err == nil {
		idToken, err = s.closeSession(ctx, c.Value)
		if err != nil {
			s.log.ErrorContext(ctx, "close session", "err", err)
		}
	}

	target := s.cfg.PublicURL + "/"
	if idToken != "" {
		if p, err := s.providerFor(ctx); err == nil && p.endSession != "" {
			v := url.Values{
				"id_token_hint":            {idToken},
				"post_logout_redirect_uri": {s.cfg.PublicURL + "/"},
				"client_id":                {s.cfg.ClientID},
			}
			target = p.endSession + "?" + v.Encode()
		}
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
