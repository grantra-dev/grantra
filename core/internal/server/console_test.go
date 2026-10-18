package server

import (
	"compress/gzip"
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

func TestConsoleGzip(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	script := strings.Repeat("console.log('grantra');\n", 200)
	ui := fstest.MapFS{
		"index.html":           {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/app-1a2b.js":   {Data: []byte(script)},
		"assets/tiny-1a2b.js":  {Data: []byte("1")},
		"assets/logo-1a2b.png": {Data: []byte(strings.Repeat("x", 4096))},
	}
	h := New(log, fakeDB{}, nil, nil, ui)

	get := func(path, accept string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if accept != "" {
			req.Header.Set("Accept-Encoding", accept)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := get("/assets/app-1a2b.js", "br, gzip;q=0.8")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip asked: %d, Content-Encoding %q", rec.Code, rec.Header().Get("Content-Encoding"))
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") {
		t.Errorf("Content-Type = %q", rec.Header().Get("Content-Type"))
	}
	if rec.Header().Get("Vary") != "Accept-Encoding" || !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("headers = %v", rec.Header())
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(zr); string(got) != script {
		t.Errorf("unzipped body differs (%d bytes)", len(got))
	}
	// The second time comes from the cache, and is the same.
	if again := get("/assets/app-1a2b.js", "gzip"); again.Header().Get("Content-Encoding") != "gzip" ||
		again.Header().Get("Content-Length") != rec.Header().Get("Content-Length") {
		t.Errorf("cached response differs: %v", again.Header())
	}

	for _, tt := range []struct{ path, accept string }{
		{"/assets/app-1a2b.js", ""},
		{"/assets/app-1a2b.js", "gzip;q=0"},
		{"/assets/app-1a2b.js", "identity"},
		{"/assets/tiny-1a2b.js", "gzip"},  // compressing would make it bigger
		{"/assets/logo-1a2b.png", "gzip"}, // images are compressed already
	} {
		rec := get(tt.path, tt.accept)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Encoding") != "" {
			t.Errorf("GET %s (%q) = %d, Content-Encoding %q", tt.path, tt.accept, rec.Code, rec.Header().Get("Content-Encoding"))
		}
	}
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"":              false,
		"gzip":          true,
		"GZIP":          true,
		"deflate, gzip": true,
		"gzip;q=0":      false,
		"gzip; q=0.000": false,
		"gzip;q=0.5":    true,
		"*":             true,
		"*;q=0, gzip":   true,
		"gzip;q=0, *":   false,
		"br, identity":  false,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("acceptsGzip(%q) = %v, want %v", header, got, want)
		}
	}
}
