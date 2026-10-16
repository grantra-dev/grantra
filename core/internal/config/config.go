// Package config loads Grantra settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the runtime settings for the core server.
type Config struct {
	HTTPAddr        string
	DatabaseURL     string
	ShutdownTimeout time.Duration
	LogLevel        string

	// MigrateDatabaseURL, when set, is the role migrations run as: it owns
	// the tables, and DatabaseURL's role only gets data permissions on them.
	MigrateDatabaseURL string
	// MigrateOnStart applies migrations when the server starts. Production
	// installs turn it off and run grantra migrate as a separate step.
	MigrateOnStart bool

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

	SMTP SMTP

	// Demo loads a sample company on start when the tenant has no HR source
	// yet. For trying Grantra out, never for real data.
	Demo bool
	// DemoLoginURL, when set, connects the demo's Grantra Login application
	// to the Keycloak at this URL.
	DemoLoginURL string

	Gateway Gateway
}

// Gateway configures the SSH gateway (grantra gateway).
type Gateway struct {
	// Addr is where the gateway listens.
	Addr string
	// HostKeyFile holds the gateway's own SSH host key. It is created the
	// first time; keep it, or people see a host key warning.
	HostKeyFile string
	// PublicAddress is how people reach the gateway (host or host:port),
	// shown in the console with the command to connect. Optional.
	PublicAddress string
}

// SMTP configures the server Grantra sends notification emails through.
// Email is off when Host is empty.
type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	// From is the sender address, like "Grantra <grantra@example.com>".
	From string
	// TLS is "starttls" (the default), "tls" or "none".
	TLS string
}

// Enabled reports whether notification email is configured.
func (s SMTP) Enabled() bool { return s.Host != "" }

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
		HTTPAddr:           getenv("GRANTRA_HTTP_ADDR", ":8080"),
		DatabaseURL:        os.Getenv("GRANTRA_DATABASE_URL"),
		MigrateDatabaseURL: os.Getenv("GRANTRA_MIGRATE_DATABASE_URL"),
		MigrateOnStart:     true,
		ShutdownTimeout:    15 * time.Second,
		LogLevel:           getenv("GRANTRA_LOG_LEVEL", "info"),
		PublicURL:          strings.TrimSuffix(getenv("GRANTRA_PUBLIC_URL", "http://localhost:8080"), "/"),
		TenantSlug:         getenv("GRANTRA_TENANT", "default"),
		TenantName:         getenv("GRANTRA_TENANT_NAME", "Default"),
		BootstrapEmail:     os.Getenv("GRANTRA_BOOTSTRAP_EMAIL"),
		OIDC: OIDC{
			Issuer:       os.Getenv("GRANTRA_OIDC_ISSUER"),
			DiscoveryURL: os.Getenv("GRANTRA_OIDC_DISCOVERY_URL"),
			ClientID:     getenv("GRANTRA_OIDC_CLIENT_ID", "grantra-console"),
			ClientSecret: os.Getenv("GRANTRA_OIDC_CLIENT_SECRET"),
		},
		Gateway: Gateway{
			Addr:          getenv("GRANTRA_GATEWAY_ADDR", ":2222"),
			HostKeyFile:   getenv("GRANTRA_GATEWAY_HOST_KEY_FILE", "gateway_host_key"),
			PublicAddress: os.Getenv("GRANTRA_SSH_GATEWAY_ADDRESS"),
		},
		SessionIdle: 30 * time.Minute,
		SessionMax:  8 * time.Hour,
		SMTP: SMTP{
			Host:     os.Getenv("GRANTRA_SMTP_HOST"),
			Port:     587,
			Username: os.Getenv("GRANTRA_SMTP_USERNAME"),
			Password: os.Getenv("GRANTRA_SMTP_PASSWORD"),
			From:     os.Getenv("GRANTRA_SMTP_FROM"),
			TLS:      getenv("GRANTRA_SMTP_TLS", "starttls"),
		},
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
	if v := os.Getenv("GRANTRA_MIGRATE_ON_START"); v != "" {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("GRANTRA_MIGRATE_ON_START must be true or false, got %q", v)
		}
		cfg.MigrateOnStart = on
	}
	if v := os.Getenv("GRANTRA_DEMO"); v != "" {
		demo, err := strconv.ParseBool(v)
		if err != nil {
			return Config{}, fmt.Errorf("GRANTRA_DEMO must be true or false, got %q", v)
		}
		cfg.Demo = demo
	}
	cfg.DemoLoginURL = os.Getenv("GRANTRA_DEMO_LOGIN_URL")
	if err := cfg.SMTP.load(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (s *SMTP) load() error {
	if v := os.Getenv("GRANTRA_SMTP_PORT"); v != "" {
		port, err := strconv.Atoi(v)
		if err != nil || port < 1 || port > 65535 {
			return fmt.Errorf("GRANTRA_SMTP_PORT must be a port number, got %q", v)
		}
		s.Port = port
	}
	if !s.Enabled() {
		return nil
	}
	switch s.TLS {
	case "starttls", "tls", "none":
	default:
		return fmt.Errorf(`GRANTRA_SMTP_TLS must be "starttls", "tls" or "none", got %q`, s.TLS)
	}
	if s.From == "" {
		return errors.New("GRANTRA_SMTP_FROM is required when GRANTRA_SMTP_HOST is set")
	}
	if _, err := mail.ParseAddress(s.From); err != nil {
		return fmt.Errorf("GRANTRA_SMTP_FROM: %w", err)
	}
	if (s.Username == "") != (s.Password == "") {
		return errors.New("set both GRANTRA_SMTP_USERNAME and GRANTRA_SMTP_PASSWORD, or neither")
	}
	return nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
