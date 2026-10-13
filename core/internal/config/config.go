// Package config loads Grantra settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config holds the runtime settings for the core server.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	ShutdownTimeout time.Duration
	LogLevel        string

	// PublicURL is where people reach Grantra in the browser, such as
	// https://grantra.example.com. Login redirects and cookies are built from it.
	PublicURL string

	// TenantSlug and TenantName name the single tenant of a self-hosted install.
	TenantSlug string
	TenantName string

	// BootstrapEmail, when set, creates a first active identity with this email
	// if the tenant has none, so the first admin can sign in.
	BootstrapEmail string

	OIDC OIDC

	// SessionIdle signs people out after this long without activity;
	// SessionMax signs them out this long after login, active or not.
	SessionIdle time.Duration
	SessionMax  time.Duration
}

// OIDC configures console login through an OpenID Connect provider: Grantra
// Login (the bundled Keycloak) or the company's own IdP.
type OIDC struct {
	// Issuer is the provider's issuer URL, exactly as it appears in tokens.
	Issuer string
	// DiscoveryURL is where Grantra fetches the provider metadata, when it must
	// differ from Issuer (for example a container network address). Optional.
	DiscoveryURL string
	ClientID     string
	ClientSecret string
}

// Enabled reports whether console login is configured.
func (o OIDC) Enabled() bool { return o.Issuer != "" }

// FromEnv reads the configuration from environment variables, applying defaults.
func FromEnv() (Config, error) {
	cfg := Config{
		HTTPAddr:        getenv("GRANTRA_HTTP_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("GRANTRA_DATABASE_URL"),
		ShutdownTimeout: 15 * time.Second,
		LogLevel:        getenv("GRANTRA_LOG_LEVEL", "info"),
		PublicURL:       strings.TrimSuffix(getenv("GRANTRA_PUBLIC_URL", "http://localhost:8080"), "/"),
		TenantSlug:      getenv("GRANTRA_TENANT", "default"),
		TenantName:      getenv("GRANTRA_TENANT_NAME", "Default"),
		BootstrapEmail:  os.Getenv("GRANTRA_BOOTSTRAP_EMAIL"),
		OIDC: OIDC{
			Issuer:       os.Getenv("GRANTRA_OIDC_ISSUER"),
			DiscoveryURL: os.Getenv("GRANTRA_OIDC_DISCOVERY_URL"),
			ClientID:     getenv("GRANTRA_OIDC_CLIENT_ID", "grantra-console"),
			ClientSecret: os.Getenv("GRANTRA_OIDC_CLIENT_SECRET"),
		},
		SessionIdle: 30 * time.Minute,
		SessionMax:  8 * time.Hour,
	}
	durations := []struct {
		key string
		dst *time.Duration
	}{
		{"GRANTRA_SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout},
		{"GRANTRA_SESSION_IDLE", &cfg.SessionIdle},
		{"GRANTRA_SESSION_MAX", &cfg.SessionMax},
	}
	for _, d := range durations {
		v := os.Getenv(d.key)
		if v == "" {
			continue
		}
		parsed, err := time.ParseDuration(v)
		if err != nil {
			return Config{}, fmt.Errorf("%s: %w", d.key, err)
		}
		if parsed <= 0 {
			return Config{}, fmt.Errorf("%s must be positive", d.key)
		}
		*d.dst = parsed
	}
	if cfg.DatabaseURL == "" {
		return Config{}, errors.New("GRANTRA_DATABASE_URL is required")
	}
	if u, err := url.Parse(cfg.PublicURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Config{}, fmt.Errorf("GRANTRA_PUBLIC_URL must be an absolute http(s) URL, got %q", cfg.PublicURL)
	}
	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
