// Package connectortest fakes the web APIs connectors talk to, for tests.
package connectortest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Server is a fake API. Requests that fail its auth check get 401; requests
// nothing handles get 404.
type Server struct {
	*httptest.Server
	mux   *http.ServeMux
	mu    sync.Mutex
	calls []string
}

// New starts a fake API that lets in only requests auth accepts (all, when
// nil). It stops when the test ends.
func New(t *testing.T, auth func(*http.Request) bool) *Server {
	t.Helper()
	s := &Server{mux: http.NewServeMux()}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(strings.NewReader(string(body)))
		call := r.Method + " " + r.URL.Path
		if len(body) > 0 {
			call += " " + string(body)
		}
		s.mu.Lock()
		s.calls = append(s.calls, call)
		s.mu.Unlock()
		if auth != nil && !auth(r) {
			Reply(w, http.StatusUnauthorized, map[string]string{"message": "bad credentials"})
			return
		}
		s.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// Handle answers requests matching pattern, a net/http ServeMux pattern
// such as "GET /api/v1/groups/{id}/users".
func (s *Server) Handle(pattern string, h http.HandlerFunc) { s.mux.HandleFunc(pattern, h) }

// JSON answers requests matching pattern with body.
func (s *Server) JSON(pattern string, body any) {
	s.Handle(pattern, func(w http.ResponseWriter, _ *http.Request) { Reply(w, http.StatusOK, body) })
}

// Status answers requests matching pattern with status and body.
func (s *Server) Status(pattern string, status int, body any) {
	s.Handle(pattern, func(w http.ResponseWriter, _ *http.Request) { Reply(w, status, body) })
}

// Calls lists the requests made so far other than reads, as "METHOD /path
// body".
func (s *Server) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.calls {
		if !strings.HasPrefix(c, "GET ") {
			out = append(out, c)
		}
	}
	return out
}

// Reply writes body as JSON, or nothing when body is nil.
func Reply(w http.ResponseWriter, status int, body any) {
	if body == nil {
		w.WriteHeader(status)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(body) //nolint:errcheck
}

// Secrets resolves every reference to its own name with "env:" cut off, so
// a config naming "env:TOKEN" gets the secret "TOKEN".
func Secrets(ref string) (string, error) { return strings.TrimPrefix(ref, "env:"), nil }
