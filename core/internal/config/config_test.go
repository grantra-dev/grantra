package config

import (
	"strings"
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
	if cfg.SessionIdle != 10*time.Minute || cfg.SessionMax != 0 {
		t.Errorf("sessions = %v/%v, want 10m and no limit set", cfg.SessionIdle, cfg.SessionMax)
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

func TestFromEnvDemo(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://localhost/grantra")
	t.Setenv("GRANTRA_DEMO", "true")
	cfg, err := FromEnv()
	if err != nil || !cfg.Demo {
		t.Fatalf("Demo = %v, %v", cfg.Demo, err)
	}
	t.Setenv("GRANTRA_DEMO", "sure")
	if _, err := FromEnv(); err == nil {
		t.Error("expected error for GRANTRA_DEMO=sure")
	}
}

func TestFromEnvMigration(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://grantra_app@localhost/grantra")
	cfg, err := FromEnv()
	if err != nil || !cfg.MigrateOnStart || cfg.MigrateDatabaseURL != "" {
		t.Fatalf("defaults: %+v, %v", cfg, err)
	}
	t.Setenv("GRANTRA_MIGRATE_DATABASE_URL", "postgres://grantra_owner@localhost/grantra")
	t.Setenv("GRANTRA_MIGRATE_ON_START", "false")
	cfg, err = FromEnv()
	if err != nil || cfg.MigrateOnStart || cfg.MigrateDatabaseURL != "postgres://grantra_owner@localhost/grantra" {
		t.Fatalf("set: %+v, %v", cfg, err)
	}
	t.Setenv("GRANTRA_MIGRATE_ON_START", "later")
	if _, err := FromEnv(); err == nil {
		t.Error("expected error for GRANTRA_MIGRATE_ON_START=later")
	}
}

func TestFromEnvGateway(t *testing.T) {
	t.Setenv("GRANTRA_DATABASE_URL", "postgres://localhost/grantra")
	t.Setenv("GRANTRA_SSH_GATEWAY_ADDRESS", "gw.example.com:2222")
	t.Setenv("GRANTRA_GATEWAY_MYSQL_ADDRESS", "db.example.com:3306")
	t.Setenv("GRANTRA_GATEWAY_TLS_NAMES", "gw.example.com, ,10.0.0.9")
	t.Setenv("GRANTRA_GATEWAY_POSTGRES_ADDR", "off")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	g := cfg.Gateway
	// The listeners people reach default to the SSH gateway's host.
	if g.KubernetesAddress != "gw.example.com:8443" || g.PostgresAddress != "gw.example.com:15432" || g.MySQLAddress != "db.example.com:3306" {
		t.Errorf("addresses = %q %q %q", g.KubernetesAddress, g.PostgresAddress, g.MySQLAddress)
	}
	if strings.Join(g.TLSNames, "|") != "gw.example.com|10.0.0.9" || g.PostgresAddr != Off || g.KubernetesAddr != ":8443" {
		t.Errorf("gateway = %+v", g)
	}
	t.Setenv("GRANTRA_GATEWAY_TLS_CERT_FILE", "/etc/gw/tls.crt")
	if _, err := FromEnv(); err == nil {
		t.Error("a certificate without its key")
	}
}
