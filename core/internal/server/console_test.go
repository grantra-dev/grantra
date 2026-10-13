package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestConsole(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ui := fstest.MapFS{
		"index.html":         {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/app-1a2b.js": {Data: []byte("console.log(1)")},
		"favicon.svg":        {Data: []byte("<svg/>")},
	}
	h := New(log, fakeDB{}, nil, nil, ui)

	tests := []struct {
		path      string
		want      int
		body      string
		immutable bool
	}{
		{"/", http.StatusOK, "id=root", false},
		{"/my-access", http.StatusOK, "id=root", false},
		{"/admin/people/42", http.StatusOK, "id=root", false},
		{"/assets/app-1a2b.js", http.StatusOK, "console.log", true},
		{"/favicon.svg", http.StatusOK, "<svg/>", false},
		{"/assets/missing.js", http.StatusNotFound, "", false},
		{"/healthz", http.StatusOK, "ok", false},
		{"/api/v1/me", http.StatusServiceUnavailable, "login is not configured", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("GET %s = %d, want %d", tt.path, rec.Code, tt.want)
			}
			if !strings.Contains(rec.Body.String(), tt.body) {
				t.Errorf("GET %s body = %q, want it to contain %q", tt.path, rec.Body.String(), tt.body)
			}
			cache := rec.Header().Get("Cache-Control")
			if tt.immutable != strings.Contains(cache, "immutable") {
				t.Errorf("GET %s Cache-Control = %q", tt.path, cache)
			}
			if tt.want == http.StatusOK && tt.body == "id=root" && rec.Header().Get("Content-Security-Policy") != consoleCSP {
				t.Errorf("GET %s CSP = %q", tt.path, rec.Header().Get("Content-Security-Policy"))
			}
		})
	}
}
