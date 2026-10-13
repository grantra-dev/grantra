package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func TestEndpoints(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	tests := []struct {
		name string
		db   Pinger
		path string
		want int
	}{
		{"healthz", fakeDB{}, "/healthz", http.StatusOK},
		{"readyz ok", fakeDB{}, "/readyz", http.StatusOK},
		{"readyz db down", fakeDB{errors.New("down")}, "/readyz", http.StatusServiceUnavailable},
		{"version", fakeDB{}, "/api/v1/version", http.StatusOK},
		{"unknown", fakeDB{}, "/nope", http.StatusNotFound},
		{"home without login", fakeDB{}, "/", http.StatusOK},
		{"me without login", fakeDB{}, "/api/v1/me", http.StatusServiceUnavailable},
		{"identities without login", fakeDB{}, "/api/v1/identities", http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			New(log, tt.db, nil, nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Errorf("GET %s = %d, want %d", tt.path, rec.Code, tt.want)
			}
			if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Errorf("missing security headers on %s", tt.path)
			}
		})
	}
}
