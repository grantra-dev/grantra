package config

import (
	"testing"
	"time"
)

func TestFromEnvDefaults(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://localhost/grantra")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want :8080", cfg.HTTPAddr)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 15s", cfg.ShutdownTimeout)
	}
}

func TestFromEnvRequiresDatabaseURL(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error when GRANTRA_DATABASE_URL is empty")
	}
}

func TestFromEnvRejectsBadTimeout(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://localhost/grantra")
	t.Setenv("GRANTRA_SHUTDOWN_TIMEOUT", "soon")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for invalid duration")
	}
}
