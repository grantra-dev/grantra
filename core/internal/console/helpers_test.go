package console

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/apperr"
	"github.com/grantra-dev/grantra/core/internal/authz"
	"github.com/grantra-dev/grantra/core/internal/lifecycle"
)

// TestFail pins the status every kind of error answers with, including the
// errors other packages define on top of apperr.
func TestFail(t *testing.T) {
	a := &API{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, tc := range []struct {
		err  error
		code int
	}{
		{errDenied, http.StatusForbidden},
		{fmt.Errorf("wrapped: %w", errDenied), http.StatusForbidden},
		{apperr.ErrNotFound, http.StatusNotFound},
		{authz.ErrNotFound, http.StatusNotFound},
		{lifecycle.ErrNotFound, http.StatusNotFound},
		{authz.ErrLastAdmin, http.StatusConflict},
		{authz.ErrInactive, http.StatusConflict},
		{lifecycle.ErrNameTaken, http.StatusConflict},
		{apperr.Conflictf("taken"), http.StatusConflict},
		{apperr.Invalidf("bad"), http.StatusBadRequest},
		{fmt.Errorf("x: %w", apperr.Invalidf("bad")), http.StatusBadRequest},
		{fmt.Errorf("boom"), http.StatusInternalServerError},
	} {
		w := httptest.NewRecorder()
		a.fail(w, httptest.NewRequest(http.MethodGet, "/api/v1/x", nil), tc.err)
		if w.Code != tc.code {
			t.Errorf("fail(%v) = %d, want %d", tc.err, w.Code, tc.code)
		}
	}
	// Grantra Login's invalid requests stay 400s.
	w := httptest.NewRecorder()
	a.loginFail(w, httptest.NewRequest(http.MethodGet, "/api/v1/login", nil), apperr.Invalidf("redirect URI must use https"))
	if w.Code != http.StatusBadRequest {
		t.Errorf("loginFail(invalid) = %d, want 400", w.Code)
	}
}
