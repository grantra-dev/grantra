package db

import (
	"errors"
	"testing"

	"github.com/grantra-dev/grantra/core/internal/apperr"
)

func TestContainsPattern(t *testing.T) {
	for q, want := range map[string]string{"Ana": "%ana%", "50%_OFF": `%50\%\_off%`, `C:\Temp`: `%c:\\temp%`} {
		if got := ContainsPattern(q); got != want {
			t.Errorf("ContainsPattern(%q) = %q, want %q", q, got, want)
		}
	}
}

func TestSearchPattern(t *testing.T) {
	for q, want := range map[string]string{
		"drop table": "%drop table%", "  50%_off ": `%50\%\_off%`, `C:\temp`: `%C:\\temp%`,
	} {
		if got, err := SearchPattern(q); err != nil || got != want {
			t.Errorf("SearchPattern(%q) = %q %v, want %q", q, got, err, want)
		}
	}
	for _, q := range []string{"", "   ", string(make([]byte, 501))} {
		var invalid *apperr.Invalid
		if _, err := SearchPattern(q); !errors.As(err, &invalid) {
			t.Errorf("SearchPattern(%d characters) = %v", len(q), err)
		}
	}
}
