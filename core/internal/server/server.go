// Package server exposes Grantra's HTTP API.
package server

import (
	"context"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"github.com/grantra-dev/grantra/core/internal/auth"
	"github.com/grantra-dev/grantra/core/internal/console"
)

// Pinger reports whether a dependency such as the database is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Version is set at build time with -ldflags "-X .../server.Version=...".
var Version = "dev"

// New returns the root HTTP handler. login and api may be nil when console
// login is not configured; the endpoints that need a signed-in person then
// answer 503.
func New(log *slog.Logger, db Pinger, login *auth.Service, api *console.API) http.Handler {
	mux := http.NewServeMux()

	// Liveness: the process is up.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Readiness: the process can serve traffic (database reachable).
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			log.WarnContext(ctx, "readiness check failed", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	})

	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": Version})
	})

	if login != nil && api != nil {
		login.Register(mux)
		api.Register(mux, login.Require)
	} else {
		mux.HandleFunc("/api/v1/", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "login is not configured"})
		})
	}

	// Placeholder home page until the React console lands: shows who is signed
	// in and offers login and logout.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		data := struct {
			LoginEnabled bool
			Principal    *auth.Principal
		}{LoginEnabled: login != nil}
		if login != nil {
			p, ok, err := login.Lookup(r)
			if err != nil {
				log.ErrorContext(r.Context(), "session lookup failed", "err", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if ok {
				data.Principal = &p
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := homePage.Execute(w, data); err != nil {
			log.ErrorContext(r.Context(), "render home", "err", err)
		}
	})

	return securityHeaders(mux)
}

var homePage = template.Must(template.New("home").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Grantra</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;padding:0 1rem}</style>
</head><body>
<h1>Grantra</h1>
{{if not .LoginEnabled}}<p>Grantra is running. Console login is not configured (set GRANTRA_OIDC_ISSUER).</p>
{{else if .Principal}}<p>Signed in as <strong>{{.Principal.DisplayName}}</strong>{{with .Principal.Email}} ({{.}}){{end}}.</p>
<form method="post" action="/auth/logout"><button type="submit">Sign out</button></form>
{{else}}<p><a href="/auth/login">Sign in</a></p>
{{end}}</body></html>
`))

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) //nolint:errcheck
}
