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

func TestFromEnvLoginSettings(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://localhost/grantra")
	t.Setenv("GRANTRA_PUBLIC_URL", "https://grantra.example.com/")
	t.Setenv("GRANTRA_OIDC_ISSUER", "https://login.example.com/realms/grantra")
	t.Setenv("GRANTRA_SESSION_IDLE", "10m")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://grantra.example.com" {
		t.Errorf("PublicURL = %q, want trailing slash trimmed", cfg.PublicURL)
	}
	if !cfg.OIDC.Enabled() || cfg.OIDC.ClientID != "grantra-console" {
		t.Errorf("OIDC = %+v, want enabled with default client id", cfg.OIDC)
	}
	if cfg.SessionIdle != 10*time.Minute || cfg.SessionMax != 8*time.Hour {
		t.Errorf("sessions = %v/%v, want 10m/8h", cfg.SessionIdle, cfg.SessionMax)
	}
}

func TestFromEnvRejectsBadPublicURL(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://localhost/grantra")
	t.Setenv("GRANTRA_PUBLIC_URL", "grantra.example.com")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error for a public URL without scheme")
	}
}

func TestFromEnvSMTP(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://localhost/grantra")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SMTP.Enabled() {
		t.Error("SMTP enabled without a host")
	}

	t.Setenv("GRANTRA_SMTP_HOST", "smtp.example.com")
	t.Setenv("GRANTRA_SMTP_FROM", "Grantra <grantra@example.com>")
	cfg, err = FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SMTP.Enabled() || cfg.SMTP.Port != 587 || cfg.SMTP.TLS != "starttls" {
		t.Errorf("SMTP = %+v, want enabled on 587 with starttls", cfg.SMTP)
	}

	bad := map[string]string{
		"GRANTRA_SMTP_PORT":     "mail",
		"GRANTRA_SMTP_TLS":      "maybe",
		"GRANTRA_SMTP_FROM":     "not an address",
		"GRANTRA_SMTP_USERNAME": "grantra",
	}
	for key, value := range bad {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, value)
			if _, err := FromEnv(); err == nil {
				t.Errorf("expected error for %s=%q", key, value)
			}
		})
	}
}

func TestFromEnvSMTPNeedsFrom(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://localhost/grantra")
	t.Setenv("GRANTRA_SMTP_HOST", "smtp.example.com")
	if _, err := FromEnv(); err == nil {
		t.Fatal("expected error without GRANTRA_SMTP_FROM")
	}
}
